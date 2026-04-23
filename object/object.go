package object

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
)

var tagSpecificCharacterSet = core.NewTag(0x0008, 0x0005)

// TextOptions configures how textual values are decoded from raw element bytes.
type TextOptions struct {
	AllowUnsupportedCharsetFallback bool
	FallbackCharacterSet            dicomenc.SpecificCharacterSet
}

type Object struct {
	elements map[core.Tag]core.Element
	order    []core.Tag
	dict     dictionary.DataDictionary
	text     TextOptions

	charsetCached bool
	charset       dicomenc.SpecificCharacterSet
	charsetErr    error
}

func New(dict dictionary.DataDictionary) *Object {
	return &Object{elements: map[core.Tag]core.Element{}, dict: dict}
}

func NewWithTextOptions(dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := New(dict)
	obj.text = opts
	return obj
}

// FromDataSet builds an Object facade over a core.DataSet.
//
// Duplicate tags use last-wins semantics. Only the last element for a tag is
// retained, and its position in the resulting object matches the tag's last
// occurrence in the source data set.
func FromDataSet(ds core.DataSet, dict dictionary.DataDictionary) *Object {
	return FromElements(ds.Elements, dict)
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
	return FromElementsWithTextOptions(ds.Elements, dict, opts)
}

func FromElementsWithTextOptions(elements []core.Element, dict dictionary.DataDictionary, opts TextOptions) *Object {
	obj := NewWithTextOptions(dict, opts)
	for _, elem := range elements {
		obj.Put(elem)
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

func (o *Object) Put(elem core.Element) {
	if o.elements == nil {
		o.elements = map[core.Tag]core.Element{}
	}
	tag := elem.Tag()
	if _, exists := o.elements[tag]; exists {
		for i := range o.order {
			if o.order[i] != tag {
				continue
			}
			copy(o.order[i:], o.order[i+1:])
			o.order = o.order[:len(o.order)-1]
			break
		}
	}
	o.order = append(o.order, tag)
	o.elements[tag] = elem
	if tag == tagSpecificCharacterSet {
		o.invalidateCharacterSetCache()
	}
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

	charset := dicomenc.DefaultCharacterSet
	var err error

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
		uid := strings.TrimRight(value, " \x00")
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

func (o *Object) GetSequence(tag core.Tag) ([]*Object, bool) {
	elem, ok := o.Get(tag)
	if !ok {
		return nil, false
	}
	seq, ok := elem.Value.(core.SequenceValue)
	if !ok {
		return nil, false
	}
	items := make([]*Object, 0, len(seq.Items))
	for _, item := range seq.Items {
		items = append(items, FromDataSetWithTextOptions(item, o.dict, o.text))
	}
	return items, true
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
	return core.DataSet{Elements: o.Elements()}
}

func (o *Object) Elements() []core.Element {
	if o == nil {
		return nil
	}
	out := make([]core.Element, 0, len(o.elements))
	for _, tag := range o.order {
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
		values[i] = strings.TrimRight(strings.TrimSpace(values[i]), "\x00")
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
		codec, err := o.codecForVR(elem.VR())
		if err != nil {
			return nil, err
		}
		return decodeRawTextValues(elem.VR(), value.Bytes(), codec)
	default:
		return nil, fmt.Errorf("dicom: unsupported textual value type %T for %s", elem.Value, elem.Tag())
	}
}

func (o *Object) codecForVR(vr core.VR) (dicomenc.TextCodec, error) {
	if !vr.UsesSpecificCharacterSet() {
		return dicomenc.DefaultCharacterSet, nil
	}
	charset, err := o.CharacterSet()
	if err != nil {
		if o != nil && o.text.AllowUnsupportedCharsetFallback {
			return o.text.FallbackCharacterSet, nil
		}
		return nil, err
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
	parts := bytes.Split(raw, []byte{'\\'})
	if len(parts) == 1 && len(core.TrimTextValueBytes(vr, parts[0])) == 0 {
		return nil, nil
	}

	values := make([]string, len(parts))
	for i := range parts {
		text, err := codec.Decode(core.TrimTextValueBytes(vr, parts[i]))
		if err != nil {
			return nil, fmt.Errorf("dicom: decode %s value %d: %w", vr, i, err)
		}
		values[i] = text
	}
	return values, nil
}

func (o *Object) invalidateCharacterSetCache() {
	if o == nil {
		return
	}
	o.charsetCached = false
	o.charset = dicomenc.SpecificCharacterSet{}
	o.charsetErr = nil
}
