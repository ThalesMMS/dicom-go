package object

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"sort"
	"strconv"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

var tagSpecificCharacterSet = core.NewTag(0x0008, 0x0005)

// Slice removal is faster than allocating an index for ordinary data sets.
// Private-heavy objects cross this threshold, where indexed tombstones avoid
// the quadratic tail of repeated updates and removals.
const orderIndexThreshold = 2048

// TextOptions configures how textual values are decoded from raw element bytes.
type TextOptions struct {
	AllowUnsupportedCharsetFallback bool
	FallbackCharacterSet            dicomenc.SpecificCharacterSet
}

type sequenceItemTemplate struct {
	elements          map[core.Tag]core.Element
	order             []core.Tag
	orderIndex        map[core.Tag]int
	staleOrder        int
	ambiguousDeferred map[core.Tag]bool
	deferredCount     int
	itemOffset        int64
	itemOffsetSet     bool
	inheritedCharset  dicomenc.SpecificCharacterSet
	inheritedErr      error
}

type Object struct {
	elements      map[core.Tag]core.Element
	order         []core.Tag
	orderIndex    map[core.Tag]int
	staleOrder    int
	sequenceCache map[core.Tag][]sequenceItemTemplate
	sharedBacking bool
	dict          dictionary.DataDictionary
	text          TextOptions
	byteOrder     binary.ByteOrder
	itemOffset    int64
	itemOffsetSet bool

	valueProvider            valueProvider
	source                   io.Closer
	deferredCount            int
	ambiguousDeferred        map[core.Tag]bool
	transferSyntaxResolution *TransferSyntaxResolution

	charsetCached bool
	charset       dicomenc.SpecificCharacterSet
	charsetErr    error

	// Sequence items fall back to the encapsulating data set's charset unless
	// they carry their own Specific Character Set (PS3.5 Section 7.5.3).
	inheritedCharsetSet bool
	inheritedCharset    dicomenc.SpecificCharacterSet
	inheritedCharsetErr error
}

type valueProvider interface {
	CopyValueTo(tag core.Tag, w io.Writer) (int64, error)
}

func New(dict dictionary.DataDictionary) *Object {
	return &Object{
		elements: map[core.Tag]core.Element{},
		dict:     dict,
	}
}

func NewWithTextOptions(dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := New(dict)
	obj.text = opts
	return obj
}

// ValueByteOrder returns the byte order of raw binary element values in this
// object. Objects built manually default to Little Endian; reader APIs set this
// from the dataset transfer syntax.
func (o *Object) ValueByteOrder() binary.ByteOrder {
	if o == nil || o.byteOrder == nil {
		return binary.LittleEndian
	}
	return o.byteOrder
}

// ItemOffset returns the absolute encoded offset of the sequence Item tag from
// which this object was parsed. Constructed top-level objects have no offset.
func (o *Object) ItemOffset() (int64, bool) {
	if o == nil || !o.itemOffsetSet {
		return 0, false
	}
	return o.itemOffset, true
}

// SetValueByteOrder records the byte order of raw binary element values.
// A nil order resets to the Little Endian default used by constructed objects.
func (o *Object) SetValueByteOrder(order binary.ByteOrder) {
	if o == nil {
		return
	}
	if order == nil {
		order = binary.LittleEndian
	}
	o.byteOrder = order
	o.sequenceCache = nil
}

// FromDataSet builds an Object facade over a core.DataSet.
//
// Duplicate tags use last-wins semantics. Only the last element for a tag is
// retained, and its position in the resulting object matches the tag's last
// occurrence in the source data set.
func FromDataSet(ds core.DataSet, dict dictionary.DataDictionary) *Object {
	obj := FromElements(ds.Elements, dict)
	obj.itemOffset = ds.ItemOffset
	obj.itemOffsetSet = ds.ItemOffsetSet
	return obj
}

// FromElements builds an Object from a sequence of elements.
//
// Duplicate tags use last-wins semantics. Only the last element for a tag is
// retained, and its position in the resulting object matches the tag's last
// occurrence in the input.
func FromElements(elements []core.Element, dict dictionary.DataDictionary) *Object {
	obj := New(dict)
	for _, elem := range elements {
		obj.Put(elem)
	}
	return obj
}

func FromDataSetWithTextOptions(ds core.DataSet, dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := dataSetObjectWithTextOptions(ds, dict, opts)
	return &obj
}

func dataSetObjectWithTextOptions(ds core.DataSet, dict dictionary.DataDictionary, opts TextOptions) Object {
	obj := Object{
		elements: map[core.Tag]core.Element{},
		dict:     dict,
		text:     opts,
	}
	for _, elem := range ds.Elements {
		obj.Put(elem)
	}
	obj.itemOffset = ds.ItemOffset
	obj.itemOffsetSet = ds.ItemOffsetSet
	return obj
}

func FromElementsWithTextOptions(elements []core.Element, dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := NewWithTextOptions(dict, opts)
	for _, elem := range elements {
		obj.Put(elem)
	}
	return obj
}

// fromParsedDataSetWithTextOptions preserves duplicate-tag ambiguity needed to
// prevent deferred replay from returning an earlier occurrence of the tag.
func fromParsedDataSetWithTextOptions(ds core.DataSet, dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := NewWithTextOptions(dict, opts)
	obj.itemOffset = ds.ItemOffset
	obj.itemOffsetSet = ds.ItemOffsetSet
	for _, elem := range ds.Elements {
		obj.put(elem, true)
	}
	return obj
}

func (o *Object) SetTextOptions(opts TextOptions) {
	if o == nil {
		return
	}
	o.text = opts
	o.invalidateCharacterSetCache()
}

func (o *Object) setValueProvider(p valueProvider) {
	if o == nil {
		return
	}
	o.valueProvider = p
}

// Close releases any source handle kept alive for deferred value streaming.
func (o *Object) Close() error {
	if o == nil {
		return nil
	}
	o.valueProvider = nil
	if o.source == nil {
		return nil
	}
	err := o.source.Close()
	o.source = nil
	return err
}

func (o *Object) Put(elem core.Element) {
	o.put(elem, false)
}

func (o *Object) put(elem core.Element, parsedDuplicateIsAmbiguous bool) {
	if o == nil {
		return
	}
	o.detachBacking()
	if o.elements == nil {
		o.elements = map[core.Tag]core.Element{}
	}
	tag := elem.Tag()
	delete(o.sequenceCache, tag)
	if existing, exists := o.elements[tag]; exists {
		if o.orderIndex == nil && len(o.order) >= orderIndexThreshold {
			o.ensureOrderIndex()
		}
		if !o.ambiguousDeferred[tag] {
			o.deferredCount -= countDeferredElement(existing)
		}
		if parsedDuplicateIsAmbiguous {
			if o.ambiguousDeferred == nil {
				o.ambiguousDeferred = map[core.Tag]bool{}
			}
			o.ambiguousDeferred[tag] = true
		} else {
			delete(o.ambiguousDeferred, tag)
		}
		if o.orderIndex == nil {
			o.removeTagFromOrder(tag)
		} else if _, ordered := o.orderIndex[tag]; ordered {
			o.staleOrder++
		}
	}
	if o.orderIndex != nil {
		o.orderIndex[tag] = len(o.order)
	}
	o.order = append(o.order, tag)
	o.elements[tag] = elem
	if !o.ambiguousDeferred[tag] {
		o.deferredCount += countDeferredElement(elem)
	}
	if tag == tagSpecificCharacterSet {
		o.invalidateCharacterSetCache()
	}
	if o.orderIndex != nil {
		o.compactOrderIfNeeded()
	}
}

// Remove deletes the element identified by tag and reports whether it was
// present. It is the inverse of Put: the tag is dropped from both the value map
// and the ordering, deferred-value bookkeeping is reconciled, and the
// character-set cache is invalidated when Specific Character Set is removed.
//
// Most de-identification only needs to *blank* a Type-2 element — a zero-length
// value via Put(core.NewRawElement(tag, vr, nil)) — which keeps the tag present
// with an empty value. Remove is for the rarer case of truly deleting a Type-3
// or private element.
func (o *Object) Remove(tag core.Tag) bool {
	if o == nil {
		return false
	}
	existing, ok := o.elements[tag]
	if !ok {
		return false
	}
	o.detachBacking()
	delete(o.sequenceCache, tag)
	if !o.ambiguousDeferred[tag] {
		o.deferredCount -= countDeferredElement(existing)
	}
	delete(o.elements, tag)
	delete(o.ambiguousDeferred, tag)
	if o.orderIndex == nil && len(o.order) < orderIndexThreshold {
		o.removeTagFromOrder(tag)
	} else {
		o.ensureOrderIndex()
		if _, ordered := o.orderIndex[tag]; ordered {
			delete(o.orderIndex, tag)
			o.staleOrder++
		}
	}
	if tag == tagSpecificCharacterSet {
		o.invalidateCharacterSetCache()
	}
	if o.orderIndex != nil {
		o.compactOrderIfNeeded()
	}
	return true
}

func (o *Object) removeTagFromOrder(tag core.Tag) {
	for i := range o.order {
		if o.order[i] != tag {
			continue
		}
		copy(o.order[i:], o.order[i+1:])
		o.order = o.order[:len(o.order)-1]
		return
	}
}

// detachBacking preserves GetSequence's historical value semantics. Cached
// item facades share immutable maps and order slices until a caller mutates one
// through Put or Remove, at which point only that facade is copied.
func (o *Object) detachBacking() {
	if o == nil || !o.sharedBacking {
		return
	}
	elements := make(map[core.Tag]core.Element, len(o.elements))
	for tag, elem := range o.elements {
		elements[tag] = elem
	}
	o.elements = elements
	o.order = append([]core.Tag(nil), o.order...)
	if o.orderIndex != nil {
		index := make(map[core.Tag]int, len(o.orderIndex))
		for tag, position := range o.orderIndex {
			index[tag] = position
		}
		o.orderIndex = index
	}
	if o.ambiguousDeferred != nil {
		ambiguous := make(map[core.Tag]bool, len(o.ambiguousDeferred))
		for tag, value := range o.ambiguousDeferred {
			ambiguous[tag] = value
		}
		o.ambiguousDeferred = ambiguous
	}
	o.sharedBacking = false
}

func (o *Object) ensureOrderIndex() {
	if o.orderIndex != nil {
		return
	}
	o.orderIndex = make(map[core.Tag]int, len(o.elements))
	for i, tag := range o.order {
		if _, present := o.elements[tag]; present {
			o.orderIndex[tag] = i
		}
	}
	o.staleOrder = len(o.order) - len(o.orderIndex)
}

func (o *Object) compactOrderIfNeeded() {
	const minStaleEntries = 64
	if o.staleOrder < minStaleEntries || o.staleOrder*2 < len(o.order) {
		return
	}
	o.detachBacking()
	compacted := make([]core.Tag, 0, len(o.elements))
	for i, tag := range o.order {
		if current, ok := o.orderIndex[tag]; !ok || current != i {
			continue
		}
		compacted = append(compacted, tag)
	}
	clear(o.orderIndex)
	for i, tag := range compacted {
		o.orderIndex[tag] = i
	}
	o.order = compacted
	o.staleOrder = 0
}

func countDeferredElements(elements []core.Element) int {
	count := 0
	for _, elem := range elements {
		count += countDeferredElement(elem)
	}
	return count
}

func countDeferredElement(elem core.Element) int {
	if elem.Value == nil {
		return 1
	}
	seq, ok := elem.Value.(core.SequenceValue)
	if !ok {
		return 0
	}
	count := 0
	for _, item := range seq.Items {
		count += countDeferredElements(item.Elements)
	}
	return count
}

func (o *Object) Get(tag core.Tag) (core.Element, bool) {
	if o == nil {
		return core.Element{}, false
	}
	elem, ok := o.elements[tag]
	return elem, ok
}

func (o *Object) Has(tag core.Tag) bool {
	if o == nil {
		return false
	}
	_, ok := o.elements[tag]
	return ok
}

func (o *Object) Len() int {
	if o == nil {
		return 0
	}
	return len(o.elements)
}

func (o *Object) GetRaw(tag core.Tag) ([]byte, bool) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, false
	}
	raw, ok := elem.RawBytes()
	if !ok {
		return nil, false
	}
	return core.CloneBytes(raw), true
}

// CopyValueTo streams the raw encoded value bytes of the element identified by tag
// into w.
//
// Lifecycle & concurrency:
//
//   - CopyValueTo is not safe for concurrent use with other Object methods.
//   - If the element was skipped during parsing, CopyValueTo requires that the
//     Object was built by a reader API such as ReadFileWithOptions,
//     ReadDataSetWithOptions, or OpenDataSetWithOptions over a seekable input
//     source with an attached valueProvider.
//   - For skipped values, CopyValueTo uses recorded value offsets when available
//     and may reparse the underlying stream as a compatibility fallback.
//
// If the element was materialized in memory, CopyValueTo copies from its RawValue.
// If the element value was skipped during parsing (e.g., because
// ReadFileOptions.InlineValueBytesThreshold was exceeded, or because Pixel Data
// was deferred), CopyValueTo can still succeed when the Object was built by a
// reader that provides a seekable input source and a valueProvider.
func (o *Object) CopyValueTo(tag core.Tag, w io.Writer) (int64, error) {
	if o == nil {
		return 0, fmt.Errorf("dicom: nil object")
	}
	elem, ok := o.Get(tag)
	if !ok {
		return 0, fmt.Errorf("dicom: missing element %s", tag)
	}
	if raw, ok := elem.RawBytes(); ok {
		return io.Copy(w, bytes.NewReader(raw))
	}
	if elem.Value != nil {
		return 0, fmt.Errorf("dicom: element %s value is not a raw byte payload", tag)
	}
	if o.ambiguousDeferred[tag] {
		return 0, fmt.Errorf("dicom: deferred value for element %s is ambiguous due to duplicate tags", tag)
	}
	if o.valueProvider == nil {
		return 0, fmt.Errorf("dicom: no value provider available for element %s", tag)
	}
	return o.valueProvider.CopyValueTo(tag, w)
}

func (o *Object) GetString(tag core.Tag) (string, bool) {
	value, err := o.LookupString(tag)
	if err != nil {
		return "", false
	}
	return value, true
}

func (o *Object) GetStrings(tag core.Tag) ([]string, bool) {
	values, err := o.LookupStrings(tag)
	if err != nil {
		return nil, false
	}
	return values, true
}

func (o *Object) LookupString(tag core.Tag) (string, error) {
	elem, ok := o.Get(tag)
	if !ok {
		return "", fmt.Errorf("dicom: missing element %s", tag)
	}
	values, err := o.decodeTextValues(elem)
	if err != nil {
		return "", err
	}
	if len(values) == 0 {
		return "", nil
	}
	return values[0], nil
}

func (o *Object) LookupStrings(tag core.Tag) ([]string, error) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, fmt.Errorf("dicom: missing element %s", tag)
	}
	values, err := o.decodeTextValues(elem)
	if err != nil {
		return nil, err
	}
	return append([]string(nil), values...), nil
}

func (o *Object) CharacterSet() (dicomenc.SpecificCharacterSet, error) {
	if o == nil {
		return dicomenc.DefaultCharacterSet, nil
	}
	if o.charsetCached {
		return o.charset, o.charsetErr
	}

	charset, err := dicomenc.DefaultCharacterSet, error(nil)
	if o.inheritedCharsetSet {
		charset, err = o.inheritedCharset, o.inheritedCharsetErr
	}

	if elem, ok := o.Get(tagSpecificCharacterSet); ok {
		values, decodeErr := decodeTextValuesWithCodec(elem, dicomenc.DefaultCharacterSet)
		if decodeErr != nil {
			err = decodeErr
		} else {
			charset, err = dicomenc.ParseCharacterSet(values...)
		}
	}

	o.charset = charset
	o.charsetErr = err
	o.charsetCached = true
	return charset, err
}

func (o *Object) GetPersonName(tag core.Tag) (core.PersonName, bool) {
	names, ok := o.GetPersonNames(tag)
	if !ok || len(names) == 0 {
		return core.PersonName{}, false
	}
	return names[0], true
}

func (o *Object) GetPersonNames(tag core.Tag) ([]core.PersonName, bool) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, false
	}
	if elem.VR() != core.VRPN {
		return nil, false
	}

	values, ok := o.GetStrings(tag)
	if !ok {
		return nil, false
	}
	names := make([]core.PersonName, len(values))
	for i := range values {
		names[i] = core.ParsePersonName(values[i])
	}
	return names, true
}

func (o *Object) GetUID(tag core.Tag) (string, bool) {
	values, ok := o.GetUIDs(tag)
	if !ok || len(values) == 0 {
		return "", false
	}
	return values[0], true
}

func (o *Object) GetUIDs(tag core.Tag) ([]string, bool) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, false
	}
	if elem.VR() != core.VRUI {
		return nil, false
	}

	values, err := o.decodeTextValues(elem)
	if err != nil {
		return nil, false
	}
	uids := make([]string, 0, len(values))
	for _, value := range values {
		uid := core.NormalizeUID(value)
		if uid != "" {
			uids = append(uids, uid)
		}
	}
	if len(uids) == 0 {
		return nil, false
	}
	return uids, true
}

func (o *Object) GetInt(tag core.Tag) (int64, error) {
	values, err := o.GetInts(tag)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, fmt.Errorf("dicom: element %s has no values", tag)
	}
	return values[0], nil
}

func (o *Object) GetInts(tag core.Tag) ([]int64, error) {
	values, err := o.numericStrings(tag, core.VRIS)
	if err != nil {
		return nil, err
	}
	out := make([]int64, len(values))
	for i, value := range values {
		n, parseErr := strconv.ParseInt(value, 10, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("dicom: parse IS value %d for %s: %w", i, tag, parseErr)
		}
		out[i] = n
	}
	return out, nil
}

func (o *Object) GetFloat(tag core.Tag) (float64, error) {
	values, err := o.GetFloats(tag)
	if err != nil {
		return 0, err
	}
	if len(values) == 0 {
		return 0, fmt.Errorf("dicom: element %s has no values", tag)
	}
	return values[0], nil
}

func (o *Object) GetFloats(tag core.Tag) ([]float64, error) {
	values, err := o.numericStrings(tag, core.VRDS)
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(values))
	for i, value := range values {
		n, parseErr := strconv.ParseFloat(value, 64)
		if parseErr != nil {
			return nil, fmt.Errorf("dicom: parse DS value %d for %s: %w", i, tag, parseErr)
		}
		out[i] = n
	}
	return out, nil
}

func (o *Object) LookupDate(tag core.Tag) (dcmtime.Date, error) {
	value, err := o.lookupTemporalString(tag, core.VRDA)
	if err != nil {
		return dcmtime.Date{}, err
	}
	return dcmtime.ParseDate(value)
}

func (o *Object) LookupTime(tag core.Tag) (dcmtime.Time, error) {
	value, err := o.lookupTemporalString(tag, core.VRTM)
	if err != nil {
		return dcmtime.Time{}, err
	}
	return dcmtime.ParseTime(value)
}

func (o *Object) LookupDateTime(tag core.Tag) (dcmtime.Datetime, error) {
	value, err := o.lookupTemporalString(tag, core.VRDT)
	if err != nil {
		return dcmtime.Datetime{}, err
	}
	return dcmtime.ParseDatetime(value)
}

// GetTime decodes the element identified by tag as a Go time.Time, dispatching on
// its value representation: DA (date), TM (time-of-day), and DT (datetime) are
// parsed with the dcmtime parsers; any other VR is an error. It is a convenience
// over LookupDate/LookupTime/LookupDateTime for callers that hold a tag but do
// not want to branch on VR themselves. The returned time carries only the
// components the value encoded (a TM value has a zero date; a DA value has a zero
// time-of-day).
func (o *Object) GetTime(tag core.Tag) (time.Time, error) {
	if o == nil {
		return time.Time{}, fmt.Errorf("dicom: nil object")
	}
	elem, ok := o.Get(tag)
	if !ok {
		return time.Time{}, fmt.Errorf("dicom: missing element %s", tag)
	}
	switch vr := elem.VR(); vr {
	case core.VRDA:
		da, err := o.LookupDate(tag)
		return da.Time, err
	case core.VRTM:
		tm, err := o.LookupTime(tag)
		return tm.Time, err
	case core.VRDT:
		dt, err := o.LookupDateTime(tag)
		return dt.Time, err
	default:
		return time.Time{}, fmt.Errorf("dicom: element %s has VR %s, want a date/time VR (DA, TM, DT)", tag, vr)
	}
}

// GetSequence returns fresh mutable facades for a sequence's items. Repeated
// calls reuse cached immutable lookup/order backings; Put and Remove detach a
// returned item before mutation, so changes never leak into another call.
func (o *Object) GetSequence(tag core.Tag) ([]*Object, bool) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, false
	}
	seq, ok := elem.Value.(core.SequenceValue)
	if !ok {
		return nil, false
	}
	if cached, ok := o.sequenceCache[tag]; ok {
		return sequenceItemFacades(o, cached), true
	}

	templates := make([]sequenceItemTemplate, len(seq.Items))
	objects := make([]Object, len(seq.Items))
	items := make([]*Object, len(seq.Items))
	inheritedCharset, inheritedCharsetErr := o.CharacterSet()
	for i, item := range seq.Items {
		objects[i] = dataSetObjectWithTextOptions(item, o.dict, o.text)
		itemObject := &objects[i]
		itemObject.SetValueByteOrder(o.ValueByteOrder())
		itemObject.setInheritedCharacterSet(inheritedCharset, inheritedCharsetErr)
		itemObject.sequenceCache = nil
		itemObject.sharedBacking = true
		templates[i] = sequenceItemTemplateFromObject(itemObject)
		items[i] = itemObject
	}
	if o.sequenceCache == nil {
		o.sequenceCache = make(map[core.Tag][]sequenceItemTemplate)
	}
	o.sequenceCache[tag] = templates
	return items, true
}

func sequenceItemTemplateFromObject(obj *Object) sequenceItemTemplate {
	return sequenceItemTemplate{
		elements:          obj.elements,
		order:             obj.order,
		orderIndex:        obj.orderIndex,
		staleOrder:        obj.staleOrder,
		ambiguousDeferred: obj.ambiguousDeferred,
		deferredCount:     obj.deferredCount,
		itemOffset:        obj.itemOffset,
		itemOffsetSet:     obj.itemOffsetSet,
		inheritedCharset:  obj.inheritedCharset,
		inheritedErr:      obj.inheritedCharsetErr,
	}
}

func sequenceItemFacades(parent *Object, templates []sequenceItemTemplate) []*Object {
	if len(templates) == 0 {
		return []*Object{}
	}
	objects := make([]Object, len(templates))
	items := make([]*Object, len(templates))
	for i, template := range templates {
		objects[i] = Object{
			elements:            template.elements,
			order:               template.order,
			orderIndex:          template.orderIndex,
			staleOrder:          template.staleOrder,
			sharedBacking:       true,
			dict:                parent.dict,
			text:                parent.text,
			byteOrder:           parent.ValueByteOrder(),
			itemOffset:          template.itemOffset,
			itemOffsetSet:       template.itemOffsetSet,
			deferredCount:       template.deferredCount,
			ambiguousDeferred:   template.ambiguousDeferred,
			inheritedCharsetSet: true,
			inheritedCharset:    template.inheritedCharset,
			inheritedCharsetErr: template.inheritedErr,
		}
		items[i] = &objects[i]
	}
	return items
}

func (o *Object) lookupTemporalString(tag core.Tag, want core.VR) (string, error) {
	elem, ok := o.Get(tag)
	if !ok {
		return "", fmt.Errorf("dicom: missing element %s", tag)
	}
	if elem.VR() != want {
		return "", fmt.Errorf("dicom: element %s has VR %s, want %s", tag, elem.VR(), want)
	}
	return o.LookupString(tag)
}

func (o *Object) MustGet(tag core.Tag) (core.Element, error) {
	elem, ok := o.Get(tag)
	if !ok {
		return core.Element{}, fmt.Errorf("dicom: missing element %s", tag)
	}
	return elem, nil
}

func (o *Object) ToDataSet() core.DataSet {
	if o == nil {
		return core.DataSet{}
	}
	return core.DataSet{Elements: o.Elements(), ItemOffset: o.itemOffset, ItemOffsetSet: o.itemOffsetSet}
}

func (o *Object) Elements() []core.Element {
	if o == nil {
		return nil
	}
	if o.orderIndex != nil {
		o.compactOrderIfNeeded()
	}
	out := make([]core.Element, 0, len(o.elements))
	for i, tag := range o.order {
		if o.orderIndex != nil {
			if current, ok := o.orderIndex[tag]; !ok || current != i {
				continue
			}
		}
		out = append(out, o.elements[tag])
	}
	return out
}

func (o *Object) SortedElements() []core.Element {
	out := o.Elements()
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tag().Group != out[j].Tag().Group {
			return out[i].Tag().Group < out[j].Tag().Group
		}
		return out[i].Tag().Element < out[j].Tag().Element
	})
	return out
}

func (o *Object) numericStrings(tag core.Tag, vr core.VR) ([]string, error) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, fmt.Errorf("dicom: missing element %s", tag)
	}
	if elem.VR() != vr {
		return nil, fmt.Errorf("dicom: element %s has VR %s, want %s", tag, elem.VR(), vr)
	}
	values, err := o.LookupStrings(tag)
	if err != nil {
		return nil, err
	}
	for i := range values {
		values[i] = core.CleanTextValue(vr, values[i])
	}
	return values, nil
}

func (o *Object) decodeTextValues(elem core.Element) ([]string, error) {
	if !elem.VR().IsStringLike() {
		return nil, fmt.Errorf("dicom: element %s with VR %s is not textual", elem.Tag(), elem.VR())
	}

	switch value := elem.Value.(type) {
	case core.StringValue:
		if len(value) == 0 {
			return nil, nil
		}
		values := make([]string, len(value))
		for i := range value {
			values[i] = core.TrimTextValue(elem.VR(), value[i])
		}
		return values, nil
	case core.RawValue:
		charset, err := o.characterSetForVR(elem.VR())
		if err != nil {
			return nil, err
		}
		return decodeRawTextValuesWithCharacterSet(elem.VR(), value.Bytes(), charset)
	default:
		return nil, fmt.Errorf("dicom: unsupported textual value type %T for %s", elem.Value, elem.Tag())
	}
}

func (o *Object) characterSetForVR(vr core.VR) (dicomenc.SpecificCharacterSet, error) {
	if !vr.UsesSpecificCharacterSet() {
		return dicomenc.DefaultCharacterSet, nil
	}
	charset, err := o.CharacterSet()
	if err != nil {
		if o != nil && o.text.AllowUnsupportedCharsetFallback {
			return o.text.FallbackCharacterSet, nil
		}
		return dicomenc.SpecificCharacterSet{}, err
	}
	return charset, nil
}

func decodeTextValuesWithCodec(elem core.Element, codec dicomenc.TextCodec) ([]string, error) {
	if !elem.VR().IsStringLike() {
		return nil, fmt.Errorf("dicom: element %s with VR %s is not textual", elem.Tag(), elem.VR())
	}

	switch value := elem.Value.(type) {
	case core.StringValue:
		if len(value) == 0 {
			return nil, nil
		}
		values := make([]string, len(value))
		for i := range value {
			values[i] = core.TrimTextValue(elem.VR(), value[i])
		}
		return values, nil
	case core.RawValue:
		return decodeRawTextValues(elem.VR(), value.Bytes(), codec)
	default:
		return nil, fmt.Errorf("dicom: unsupported textual value type %T for %s", elem.Value, elem.Tag())
	}
}

func decodeRawTextValues(vr core.VR, raw []byte, codec dicomenc.TextCodec) ([]string, error) {
	return decodeRawTextValuesFunc(vr, raw, codec.Decode)
}

func decodeRawTextValuesWithCharacterSet(vr core.VR, raw []byte, charset dicomenc.SpecificCharacterSet) ([]string, error) {
	if vr == core.VRPN {
		return decodeRawTextValuesFunc(vr, raw, charset.DecodePersonName)
	}
	return decodeRawTextValuesFunc(vr, raw, charset.Decode)
}

func decodeRawTextValuesFunc(vr core.VR, raw []byte, decode func([]byte) (string, error)) ([]string, error) {
	text, err := decode(core.TrimTextValueBytes(vr, raw))
	if err != nil {
		return nil, fmt.Errorf("dicom: decode %s value: %w", vr, err)
	}
	return core.SplitTextMultiplicity(vr, text), nil
}

func (o *Object) invalidateCharacterSetCache() {
	if o == nil {
		return
	}
	o.charsetCached = false
	o.charset = dicomenc.SpecificCharacterSet{}
	o.charsetErr = nil
	o.sequenceCache = nil
}

func (o *Object) setInheritedCharacterSet(charset dicomenc.SpecificCharacterSet, err error) {
	if o == nil {
		return
	}
	o.inheritedCharsetSet = true
	o.inheritedCharset = charset
	o.inheritedCharsetErr = err
	o.invalidateCharacterSetCache()
}
