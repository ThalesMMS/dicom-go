package dicomjson

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	dsValuePattern = regexp.MustCompile(`^[+-]?(?:[0-9]+(?:\.[0-9]*)?|\.[0-9]+)(?:[eE][+-]?[0-9]+)?$`)
	isValuePattern = regexp.MustCompile(`^[+-]?[0-9]+$`)
)

type Element struct {
	VR           string `json:"vr"`
	Value        []any  `json:"Value,omitempty"`
	InlineBinary string `json:"InlineBinary,omitempty"`
	BulkDataURI  string `json:"BulkDataURI,omitempty"`
}

type PersonNameComponents struct {
	Alphabetic  string `json:"Alphabetic,omitempty"`
	Ideographic string `json:"Ideographic,omitempty"`
	Phonetic    string `json:"Phonetic,omitempty"`
}

type Options struct {
	Pretty          bool
	OmitGroupLength bool
	// BulkDataURIFunc receives the complete encoded Value Field bytes, including
	// fragment item headers and the delimiter for encapsulated Pixel Data.
	BulkDataURIFunc func(tag core.Tag, vr core.VR, data []byte) string
	// PixelDataBulkDataURIFunc can offload Pixel Data without first collecting
	// its complete value field in memory. The callback may open the value stream
	// zero or more times and must close every reader it opens. Returning an empty
	// URI falls back to the existing byte-slice callback or InlineBinary path.
	PixelDataBulkDataURIFunc func(tag core.Tag, vr core.VR, open func() (io.ReadCloser, error)) (string, error)
	// ByteOrder is used when interpreting raw bytes for numeric VRs and AT.
	// When nil, the object's ValueByteOrder is used.
	ByteOrder binary.ByteOrder
}

// UnmarshalOptions configures DICOM JSON to object conversion.
type UnmarshalOptions struct {
	TextOptions object.TextOptions
	// ByteOrder is used when encoding numeric VRs and AT from JSON Value arrays.
	// It defaults to binary.LittleEndian.
	ByteOrder binary.ByteOrder
}

func DefaultOptions() Options {
	return Options{
		Pretty:          true,
		OmitGroupLength: true,
	}
}

// DefaultUnmarshalOptions returns the default DICOM JSON unmarshal options.
func DefaultUnmarshalOptions() UnmarshalOptions {
	return UnmarshalOptions{ByteOrder: binary.LittleEndian}
}

func Marshal(obj *object.Object, opts Options) ([]byte, error) {
	if opts.ByteOrder == nil {
		if obj == nil {
			opts.ByteOrder = binary.LittleEndian
		} else {
			opts.ByteOrder = obj.ValueByteOrder()
		}
	}
	m, err := marshalElements(obj, opts)
	if err != nil {
		return nil, err
	}
	if opts.Pretty {
		return json.MarshalIndent(m, "", "  ")
	}
	return json.Marshal(m)
}

func MarshalCompact(obj *object.Object) ([]byte, error) {
	return Marshal(obj, Options{OmitGroupLength: true, Pretty: false})
}

func MarshalPretty(obj *object.Object) ([]byte, error) {
	return Marshal(obj, Options{OmitGroupLength: true, Pretty: true})
}

func MarshalObject(obj *object.Object) ([]byte, error) {
	return Marshal(obj, DefaultOptions())
}

func MarshalObjectWithOptions(obj *object.Object, opts Options) ([]byte, error) {
	return Marshal(obj, opts)
}

func Unmarshal(data []byte, dict dictionary.DataDictionary) (*object.Object, error) {
	return UnmarshalWithOptions(data, dict, DefaultUnmarshalOptions())
}

func UnmarshalWithTextOptions(data []byte, dict dictionary.DataDictionary, opts object.TextOptions) (*object.Object, error) {
	return UnmarshalWithOptions(data, dict, UnmarshalOptions{TextOptions: opts})
}

// UnmarshalWithOptions converts DICOM JSON into an object using explicit
// conversion options.
func UnmarshalWithOptions(data []byte, dict dictionary.DataDictionary, opts UnmarshalOptions) (*object.Object, error) {
	opts.ByteOrder = byteOrderOrDefault(opts.ByteOrder)
	ds, err := unmarshalDataSet(data, "", dict, opts.ByteOrder)
	if err != nil {
		return nil, err
	}
	obj := object.FromElementsWithTextOptions(ds.Elements, dict, opts.TextOptions)
	obj.SetValueByteOrder(opts.ByteOrder)
	return obj, nil
}

func byteOrderOrDefault(order binary.ByteOrder) binary.ByteOrder {
	if order == nil {
		return binary.LittleEndian
	}
	return order
}

func marshalElements(obj *object.Object, opts Options) (map[string]Element, error) {
	out := map[string]Element{}
	if obj == nil {
		return out, nil
	}

	for _, elem := range obj.SortedElements() {
		if opts.OmitGroupLength && elem.Tag().IsGroupLength() {
			continue
		}

		entry, err := marshalElement(obj, elem, opts)
		if err != nil {
			return nil, err
		}
		out[elem.Tag().HexString()] = entry
	}
	return out, nil
}

func marshalElement(obj *object.Object, elem core.Element, opts Options) (Element, error) {
	entry := Element{VR: elem.VR().String()}

	switch value := elem.Value.(type) {
	case core.BulkDataValue:
		entry.BulkDataURI = value.URI
		return entry, nil
	}

	switch {
	case elem.VR() == core.VRPN:
		values, err := obj.LookupStrings(elem.Tag())
		if err != nil {
			return Element{}, err
		}
		entry.Value = personNamesToAny(values)
	case elem.VR() == core.VRSQ:
		items, ok := obj.GetSequence(elem.Tag())
		if !ok {
			return entry, nil
		}
		entry.Value = make([]any, 0, len(items))
		for _, item := range items {
			nested, err := marshalElements(item, opts)
			if err != nil {
				return Element{}, err
			}
			entry.Value = append(entry.Value, nested)
		}
	case elem.VR().IsStringLike():
		values, err := obj.LookupStrings(elem.Tag())
		if err != nil {
			return Element{}, err
		}
		entry.Value = stringsToAny(values)
	default:
		if elem.Tag() == core.TagPixelData && opts.PixelDataBulkDataURIFunc != nil {
			open, ok, err := pixelDataBulkReader(obj, elem, opts.ByteOrder)
			if err != nil {
				return Element{}, err
			}
			if ok {
				uri, err := opts.PixelDataBulkDataURIFunc(elem.Tag(), elem.VR(), open)
				if err != nil {
					return Element{}, fmt.Errorf("dicomjson: offload Pixel Data: %w", err)
				}
				if uri != "" {
					entry.BulkDataURI = uri
					return entry, nil
				}
			}
		}
		raw, ok := elem.RawBytes()
		if !ok {
			var err error
			raw, err = materializeRawValue(obj, elem, opts.ByteOrder)
			if err != nil {
				return Element{}, err
			}
		}
		switch elem.VR() {
		case core.VRAT:
			values, err := marshalAttributeTags(elem.Tag(), raw, opts.ByteOrder)
			if err != nil {
				return Element{}, err
			}
			entry.Value = values
			return entry, nil
		case core.VRFL, core.VRFD, core.VRSS, core.VRUS, core.VRSL, core.VRUL, core.VRSV, core.VRUV:
			values, err := marshalNumericValues(elem.Tag(), elem.VR(), raw, opts.ByteOrder)
			if err != nil {
				return Element{}, err
			}
			entry.Value = values
			return entry, nil
		}
		if len(raw) == 0 {
			return entry, nil
		}
		if opts.BulkDataURIFunc != nil {
			if uri := opts.BulkDataURIFunc(elem.Tag(), elem.VR(), raw); uri != "" {
				entry.BulkDataURI = uri
				return entry, nil
			}
		}
		entry.InlineBinary = base64.StdEncoding.EncodeToString(raw)
	}

	return entry, nil
}

func pixelDataBulkReader(obj *object.Object, elem core.Element, order binary.ByteOrder) (func() (io.ReadCloser, error), bool, error) {
	if raw, ok := elem.RawBytes(); ok {
		return func() (io.ReadCloser, error) {
			return io.NopCloser(bytes.NewReader(raw)), nil
		}, true, nil
	}
	switch value := elem.Value.(type) {
	case core.FragmentSequence:
		if len(value.OffsetTable)%4 != 0 {
			return nil, false, fmt.Errorf("dicomjson: Basic Offset Table length %d is not a multiple of 4", len(value.OffsetTable))
		}
		return func() (io.ReadCloser, error) {
			return streamingValueReader(func(w io.Writer) error {
				return writeFragmentSequenceValueField(w, value, order)
			}), nil
		}, true, nil
	case nil:
		return func() (io.ReadCloser, error) {
			return streamingValueReader(func(w io.Writer) error {
				_, err := obj.CopyValueTo(elem.Tag(), w)
				return err
			}), nil
		}, true, nil
	default:
		return nil, false, nil
	}
}

func streamingValueReader(write func(io.Writer) error) io.ReadCloser {
	reader, writer := io.Pipe()
	go func() {
		writer.CloseWithError(write(writer))
	}()
	return reader
}

func materializeRawValue(obj *object.Object, elem core.Element, order binary.ByteOrder) ([]byte, error) {
	switch value := elem.Value.(type) {
	case core.FragmentSequence:
		if elem.Tag() != core.TagPixelData {
			return nil, fmt.Errorf("dicomjson: marshal %s: FragmentSequence is only valid for Pixel Data", elem.Tag())
		}
		return fragmentSequenceValueField(value, order)
	case nil:
		if elem.Header.HasLength() && elem.EncodedLength() == 0 {
			return nil, nil
		}
		var valueField bytes.Buffer
		if _, err := obj.CopyValueTo(elem.Tag(), &valueField); err != nil {
			return nil, fmt.Errorf("dicomjson: marshal deferred %s: %w", elem.Tag(), err)
		}
		return valueField.Bytes(), nil
	default:
		if length, ok := elem.CalculatedLength(); ok && length == 0 {
			return nil, nil
		}
		return nil, fmt.Errorf("dicomjson: marshal %s VR %s: unsupported value type %T", elem.Tag(), elem.VR(), elem.Value)
	}
}

func fragmentSequenceValueField(value core.FragmentSequence, order binary.ByteOrder) ([]byte, error) {
	if len(value.OffsetTable)%4 != 0 {
		return nil, fmt.Errorf("dicomjson: Basic Offset Table length %d is not a multiple of 4", len(value.OffsetTable))
	}
	var out bytes.Buffer
	if err := writeFragmentSequenceValueField(&out, value, order); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func writeFragmentSequenceValueField(out io.Writer, value core.FragmentSequence, order binary.ByteOrder) error {
	if err := writeFragmentItem(out, order, value.OffsetTable, false); err != nil {
		return err
	}
	for _, fragment := range value.Fragments {
		if err := writeFragmentItem(out, order, fragment, true); err != nil {
			return err
		}
	}
	return writeItemHeader(out, order, core.TagSequenceDelimitationItem, 0)
}

func writeFragmentItem(out io.Writer, order binary.ByteOrder, data []byte, pad bool) error {
	length := uint64(len(data))
	if pad && length%2 == 1 {
		length++
	}
	if length > uint64(^uint32(0)) {
		return fmt.Errorf("dicomjson: fragment length %d exceeds uint32", length)
	}
	if err := writeItemHeader(out, order, core.TagItem, uint32(length)); err != nil {
		return err
	}
	if _, err := out.Write(data); err != nil {
		return err
	}
	if pad && len(data)%2 == 1 {
		if _, err := out.Write([]byte{0}); err != nil {
			return err
		}
	}
	return nil
}

func writeItemHeader(out io.Writer, order binary.ByteOrder, tag core.Tag, length uint32) error {
	var header [8]byte
	order.PutUint16(header[0:2], tag.Group)
	order.PutUint16(header[2:4], tag.Element)
	order.PutUint32(header[4:8], length)
	_, err := out.Write(header[:])
	return err
}

func unmarshalDataSet(data []byte, path string, dict dictionary.DataDictionary, order binary.ByteOrder) (core.DataSet, error) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(data, &raw); err != nil {
		return core.DataSet{}, fmt.Errorf("dicomjson: decode dataset JSON: %w", err)
	}
	return unmarshalDataSetMap(raw, path, dict, order)
}

func unmarshalDataSetMap(raw map[string]json.RawMessage, path string, dict dictionary.DataDictionary, order binary.ByteOrder) (core.DataSet, error) {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	elements := make([]core.Element, 0, len(keys))
	for _, tagStr := range keys {
		tagPath := joinJSONPath(path, tagStr)
		tag, err := parseJSONTag(tagStr, tagPath)
		if err != nil {
			return core.DataSet{}, err
		}
		elem, err := unmarshalElement(tag, raw[tagStr], tagPath, dict, order)
		if err != nil {
			return core.DataSet{}, err
		}
		elements = append(elements, elem)
	}
	return core.DataSet{Elements: elements}, nil
}

func unmarshalElement(tag core.Tag, raw json.RawMessage, path string, dict dictionary.DataDictionary, order binary.ByteOrder) (core.Element, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSON(raw, &fields); err != nil {
		return core.Element{}, pathError(path, "decode element JSON: %v", err)
	}

	for key := range fields {
		switch key {
		case "vr", "Value", "InlineBinary", "BulkDataURI":
		default:
			return core.Element{}, pathError(joinJSONPath(path, key), "unrecognized data element field %q", key)
		}
	}

	vrRaw, ok := fields["vr"]
	if !ok {
		return core.Element{}, pathError(joinJSONPath(path, "vr"), "missing VR field")
	}
	var vrText string
	if err := decodeJSON(vrRaw, &vrText); err != nil {
		return core.Element{}, pathError(joinJSONPath(path, "vr"), "decode VR: %v", err)
	}
	vr, err := core.ParseVR(vrText)
	if err != nil {
		return core.Element{}, pathError(joinJSONPath(path, "vr"), "%v", err)
	}

	_, hasValue := fields["Value"]
	_, hasInline := fields["InlineBinary"]
	_, hasBulk := fields["BulkDataURI"]
	switch {
	case hasValue && hasInline:
		return core.Element{}, pathError(path, "\"Value\" conflicts with \"InlineBinary\"")
	case hasValue && hasBulk:
		return core.Element{}, pathError(path, "\"Value\" conflicts with \"BulkDataURI\"")
	case hasInline && hasBulk:
		return core.Element{}, pathError(path, "\"InlineBinary\" conflicts with \"BulkDataURI\"")
	}

	elem := core.Element{
		Header: core.ElementHeader{
			Tag: tag,
			VR:  vr,
		},
	}

	switch {
	case hasBulk:
		if vr == core.VRSQ {
			return core.Element{}, pathError(joinJSONPath(path, "BulkDataURI"), "BulkDataURI is not valid for SQ")
		}
		var uri string
		if err := decodeJSON(fields["BulkDataURI"], &uri); err != nil {
			return core.Element{}, pathError(joinJSONPath(path, "BulkDataURI"), "decode BulkDataURI: %v", err)
		}
		elem.Value = core.BulkDataValue{URI: uri}
		return elem, nil
	case hasInline:
		if vr == core.VRSQ {
			return core.Element{}, pathError(joinJSONPath(path, "InlineBinary"), "InlineBinary is not valid for SQ")
		}
		var encoded string
		if err := decodeJSON(fields["InlineBinary"], &encoded); err != nil {
			return core.Element{}, pathError(joinJSONPath(path, "InlineBinary"), "decode InlineBinary: %v", err)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return core.Element{}, pathError(joinJSONPath(path, "InlineBinary"), "inline binary data is not valid base64")
		}
		elem.Value = core.RawValue(core.CloneBytes(data))
		return elem, nil
	case !hasValue:
		elem.Value = emptyValueForVR(vr)
		return elem, nil
	}

	valuePath := joinJSONPath(path, "Value")
	switch vr {
	case core.VRSQ:
		value, err := unmarshalSequence(fields["Value"], path, valuePath, dict, order)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRPN:
		value, err := unmarshalPersonNames(fields["Value"], valuePath, vr)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRDS, core.VRIS:
		value, err := unmarshalNumberStrings(fields["Value"], valuePath, vr)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRAT:
		value, err := unmarshalAttributeTags(fields["Value"], valuePath, vr, order)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRUN:
		return core.Element{}, valueTypePathError(valuePath, vr, fields["Value"], "can't parse JSON Value in UN; use InlineBinary or BulkDataURI")
	case core.VRFL, core.VROF, core.VRFD, core.VROD, core.VRSS, core.VRUS, core.VROW, core.VRSL, core.VRUL, core.VROL, core.VRSV, core.VRUV, core.VROV, core.VROB:
		value, err := encodeNumericValues(vr, fields["Value"], valuePath, order)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = core.RawValue(value)
	case core.VRAE, core.VRAS, core.VRCS, core.VRDA, core.VRDT, core.VRLO, core.VRLT, core.VRSH, core.VRST, core.VRUT, core.VRUR, core.VRTM, core.VRUC, core.VRUI:
		value, err := unmarshalStringValues(fields["Value"], valuePath, vr)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	default:
		return core.Element{}, valueTypePathError(valuePath, vr, fields["Value"], "unsupported VR %s", vr)
	}

	return elem, nil
}

func unmarshalStringValues(raw json.RawMessage, path string, vr core.VR) (core.StringValue, error) {
	items, err := decodeValueArray(raw, path, vr)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		value, err := decodeNullableStringValue(items[i], indexJSONPath(path, i), vr)
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return core.StringValue(values), nil
}

func unmarshalNumberStrings(raw json.RawMessage, path string, vr core.VR) (core.StringValue, error) {
	items, err := decodeValueArray(raw, path, vr)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		itemPath := indexJSONPath(path, i)
		token, err := decodeNullableNumberOrStringValue(items[i], itemPath, vr)
		if err != nil {
			return nil, err
		}
		if !isJSONNull(items[i]) {
			if err := validateNumberString(token, vr); err != nil {
				return nil, valueTypePathError(itemPath, vr, items[i], "%v", err)
			}
		}
		values[i] = token
	}
	return core.StringValue(values), nil
}

func validateNumberString(value string, vr core.VR) error {
	maxLength := 16
	pattern := dsValuePattern
	if vr == core.VRIS {
		maxLength = 12
		pattern = isValuePattern
	}
	if len(value) > maxLength {
		return fmt.Errorf("%s value is %d bytes, want at most %d", vr, len(value), maxLength)
	}
	trimmed := strings.Trim(value, " ")
	if !pattern.MatchString(trimmed) {
		return fmt.Errorf("value %q does not match %s syntax", value, vr)
	}
	if vr == core.VRIS {
		if _, err := strconv.ParseInt(trimmed, 10, 32); err != nil {
			return fmt.Errorf("IS value %q is outside signed 32-bit range: %w", value, err)
		}
	}
	return nil
}

func unmarshalPersonNames(raw json.RawMessage, path string, vr core.VR) (core.StringValue, error) {
	items, err := decodeValueArray(raw, path, vr)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		itemPath := indexJSONPath(path, i)
		if isJSONNull(items[i]) {
			continue
		}
		if jsonValueType(items[i]) != "object" {
			return nil, valueTypePathError(itemPath, vr, items[i], "expected person name object")
		}
		var pn PersonNameComponents
		if err := decodeJSON(items[i], &pn); err != nil {
			return nil, valueTypePathError(itemPath, vr, items[i], "decode person name: %v", err)
		}
		values[i] = joinPersonName(pn)
	}
	return core.StringValue(values), nil
}

func unmarshalSequence(raw json.RawMessage, path, valuePath string, dict dictionary.DataDictionary, order binary.ByteOrder) (core.SequenceValue, error) {
	items, err := decodeValueArray(raw, valuePath, core.VRSQ)
	if err != nil {
		return core.SequenceValue{}, err
	}
	seq := core.SequenceValue{Items: make([]core.DataSet, 0, len(items))}
	for i := range items {
		itemPath := indexJSONPath(path, i)
		var dataset map[string]json.RawMessage
		if err := decodeJSON(items[i], &dataset); err != nil {
			return core.SequenceValue{}, pathError(itemPath, "decode sequence item: %v", err)
		}
		ds, err := unmarshalDataSetMap(dataset, itemPath, dict, order)
		if err != nil {
			return core.SequenceValue{}, err
		}
		seq.Items = append(seq.Items, ds)
	}
	return seq, nil
}

func unmarshalAttributeTags(raw json.RawMessage, path string, vr core.VR, order binary.ByteOrder) (core.RawValue, error) {
	items, err := decodeValueArray(raw, path, vr)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, len(items)*4)
	for i := range items {
		itemPath := indexJSONPath(path, i)
		value, err := decodeRequiredStringValue(items[i], itemPath, vr)
		if err != nil {
			return nil, err
		}
		tag, err := parseJSONTag(value, itemPath)
		if err != nil {
			return nil, err
		}
		var buf [4]byte
		order.PutUint16(buf[0:2], tag.Group)
		order.PutUint16(buf[2:4], tag.Element)
		data = append(data, buf[:]...)
	}
	return core.RawValue(data), nil
}

func encodeNumericValues(vr core.VR, raw json.RawMessage, path string, order binary.ByteOrder) ([]byte, error) {
	items, err := decodeValueArray(raw, path, vr)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, len(items)*8)
	for i := range items {
		itemPath := indexJSONPath(path, i)
		switch vr {
		case core.VRFL, core.VROF:
			v, err := parseFloat32(items[i], itemPath, vr)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			order.PutUint32(buf[:], math.Float32bits(v))
			data = append(data, buf[:]...)
		case core.VRFD, core.VROD:
			v, err := parseFloat64(items[i], itemPath, vr)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			order.PutUint64(buf[:], math.Float64bits(v))
			data = append(data, buf[:]...)
		case core.VRSS:
			v, err := parseInt(items[i], itemPath, 16, vr)
			if err != nil {
				return nil, err
			}
			var buf [2]byte
			order.PutUint16(buf[:], uint16(int16(v)))
			data = append(data, buf[:]...)
		case core.VRUS, core.VROW:
			v, err := parseUint(items[i], itemPath, 16, vr)
			if err != nil {
				return nil, err
			}
			var buf [2]byte
			order.PutUint16(buf[:], uint16(v))
			data = append(data, buf[:]...)
		case core.VRSL:
			v, err := parseInt(items[i], itemPath, 32, vr)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			order.PutUint32(buf[:], uint32(int32(v)))
			data = append(data, buf[:]...)
		case core.VRUL, core.VROL:
			v, err := parseUint(items[i], itemPath, 32, vr)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			order.PutUint32(buf[:], uint32(v))
			data = append(data, buf[:]...)
		case core.VRSV:
			v, err := parseInt(items[i], itemPath, 64, vr)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			order.PutUint64(buf[:], uint64(v))
			data = append(data, buf[:]...)
		case core.VRUV, core.VROV:
			v, err := parseUint(items[i], itemPath, 64, vr)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			order.PutUint64(buf[:], v)
			data = append(data, buf[:]...)
		case core.VROB:
			v, err := parseUint(items[i], itemPath, 8, vr)
			if err != nil {
				return nil, err
			}
			data = append(data, byte(v))
		default:
			return nil, valueTypePathError(path, vr, raw, "unsupported numeric VR %s", vr)
		}
	}
	return data, nil
}

func emptyValueForVR(vr core.VR) core.Value {
	switch vr {
	case core.VRSQ:
		return core.SequenceValue{}
	case core.VRPN, core.VRAE, core.VRAS, core.VRCS, core.VRDA, core.VRDS, core.VRDT, core.VRIS, core.VRLO, core.VRLT, core.VRSH, core.VRST, core.VRTM, core.VRUC, core.VRUI, core.VRUR, core.VRUT:
		return core.StringValue(nil)
	default:
		return core.RawValue(nil)
	}
}

func stringsToAny(values []string) []any {
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 && values[0] == "" {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		if value == "" {
			out = append(out, nil)
			continue
		}
		out = append(out, value)
	}
	return out
}

func personNamesToAny(values []string) []any {
	if len(values) == 0 {
		return nil
	}
	if len(values) == 1 && values[0] == "" {
		return nil
	}
	out := make([]any, 0, len(values))
	for _, value := range values {
		if value == "" {
			out = append(out, nil)
			continue
		}
		out = append(out, splitPersonName(value))
	}
	return out
}

func splitPersonName(value string) PersonNameComponents {
	parts := strings.SplitN(value, "=", 3)
	pn := PersonNameComponents{}
	if len(parts) > 0 && parts[0] != "" {
		pn.Alphabetic = parts[0]
	}
	if len(parts) > 1 && parts[1] != "" {
		pn.Ideographic = parts[1]
	}
	if len(parts) > 2 && parts[2] != "" {
		pn.Phonetic = parts[2]
	}
	return pn
}

func joinPersonName(pn PersonNameComponents) string {
	switch {
	case pn.Ideographic == "" && pn.Phonetic == "":
		return pn.Alphabetic
	case pn.Ideographic != "" && pn.Phonetic == "":
		return pn.Alphabetic + "=" + pn.Ideographic
	case pn.Ideographic == "" && pn.Phonetic != "":
		return pn.Alphabetic + "==" + pn.Phonetic
	default:
		return pn.Alphabetic + "=" + pn.Ideographic + "=" + pn.Phonetic
	}
}

func marshalAttributeTags(tag core.Tag, raw []byte, order binary.ByteOrder) ([]any, error) {
	if len(raw)%4 != 0 {
		return nil, invalidRawValueLengthError(tag, core.VRAT, len(raw), 4)
	}
	values := make([]any, 0, len(raw)/4)
	for len(raw) > 0 {
		value := core.NewTag(
			order.Uint16(raw[0:2]),
			order.Uint16(raw[2:4]),
		)
		values = append(values, value.HexString())
		raw = raw[4:]
	}
	return values, nil
}

func marshalNumericValues(tag core.Tag, vr core.VR, raw []byte, order binary.ByteOrder) ([]any, error) {
	width := numericValueWidth(vr)
	if width == 0 {
		return nil, fmt.Errorf("dicomjson: cannot marshal tag %s VR %s as JSON Value", tag, vr)
	}
	if len(raw)%width != 0 {
		return nil, invalidRawValueLengthError(tag, vr, len(raw), width)
	}

	values := make([]any, 0, len(raw)/width)
	for len(raw) > 0 {
		switch vr {
		case core.VRFL:
			values = append(values, float64(math.Float32frombits(order.Uint32(raw[:4]))))
		case core.VRFD:
			values = append(values, math.Float64frombits(order.Uint64(raw[:8])))
		case core.VRSS:
			values = append(values, int64(int16(order.Uint16(raw[:2]))))
		case core.VRUS:
			values = append(values, uint64(order.Uint16(raw[:2])))
		case core.VRSL:
			values = append(values, int64(int32(order.Uint32(raw[:4]))))
		case core.VRUL:
			values = append(values, uint64(order.Uint32(raw[:4])))
		case core.VRSV:
			values = append(values, int64(order.Uint64(raw[:8])))
		case core.VRUV:
			values = append(values, order.Uint64(raw[:8]))
		}
		raw = raw[width:]
	}
	return values, nil
}

func numericValueWidth(vr core.VR) int {
	switch vr {
	case core.VRSS, core.VRUS:
		return 2
	case core.VRFL, core.VRSL, core.VRUL:
		return 4
	case core.VRFD, core.VRSV, core.VRUV:
		return 8
	default:
		return 0
	}
}

func invalidRawValueLengthError(tag core.Tag, vr core.VR, got, width int) error {
	return fmt.Errorf("dicomjson: cannot marshal tag %s VR %s: raw value length %d is not a multiple of %d", tag, vr, got, width)
}

func parseJSONTag(tagStr, path string) (core.Tag, error) {
	trimmed := strings.TrimSpace(tagStr)
	if len(trimmed) != 8 || strings.ContainsRune(trimmed, ',') {
		return core.Tag{}, pathError(path, "invalid tag %q", tagStr)
	}
	tag, err := core.ParseTag(trimmed)
	if err != nil {
		return core.Tag{}, pathError(path, "%v", err)
	}
	return tag, nil
}

func decodeValueArray(raw json.RawMessage, path string, vr core.VR) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := decodeJSON(raw, &items); err != nil {
		return nil, valueTypePathError(path, vr, raw, "expected JSON array: %v", err)
	}
	return items, nil
}

func decodeNullableStringValue(raw json.RawMessage, path string, vr core.VR) (string, error) {
	if isJSONNull(raw) {
		return "", nil
	}
	return decodeRequiredStringValue(raw, path, vr)
}

func decodeRequiredString(raw json.RawMessage, path string) (string, error) {
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return "", pathError(path, "expected string: %v", err)
	}
	return value, nil
}

func decodeRequiredStringValue(raw json.RawMessage, path string, vr core.VR) (string, error) {
	if jsonValueType(raw) != "string" {
		return "", valueTypePathError(path, vr, raw, "expected string")
	}
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return "", valueTypePathError(path, vr, raw, "expected string: %v", err)
	}
	return value, nil
}

func decodeNullableNumberOrStringValue(raw json.RawMessage, path string, vr core.VR) (string, error) {
	if isJSONNull(raw) {
		return "", nil
	}
	return decodeRequiredNumberOrStringValue(raw, path, vr)
}

func decodeRequiredNumberOrStringValue(raw json.RawMessage, path string, vr core.VR) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", valueTypePathError(path, vr, raw, "empty JSON value")
	}
	if trimmed[0] == '"' {
		return decodeRequiredString(raw, path)
	}
	valueType := jsonValueType(raw)
	if valueType != "number" {
		return "", valueTypePathError(path, vr, raw, "expected number or string")
	}
	var number json.Number
	if err := decodeJSON(raw, &number); err != nil {
		return "", valueTypePathError(path, vr, raw, "expected number or string: %v", err)
	}
	return number.String(), nil
}

func parseInt(raw json.RawMessage, path string, bits int, vr core.VR) (int64, error) {
	token, err := decodeRequiredNumberOrStringValue(raw, path, vr)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(token, 10, bits)
	if err != nil {
		return 0, valueTypePathError(path, vr, raw, "parse signed integer: %v", err)
	}
	return value, nil
}

func parseUint(raw json.RawMessage, path string, bits int, vr core.VR) (uint64, error) {
	token, err := decodeRequiredNumberOrStringValue(raw, path, vr)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(token, 10, bits)
	if err != nil {
		return 0, valueTypePathError(path, vr, raw, "parse unsigned integer: %v", err)
	}
	return value, nil
}

func parseFloat32(raw json.RawMessage, path string, vr core.VR) (float32, error) {
	value, err := parseFloatToken(raw, path, 32, vr)
	if err != nil {
		return 0, err
	}
	return float32(value), nil
}

func parseFloat64(raw json.RawMessage, path string, vr core.VR) (float64, error) {
	return parseFloatToken(raw, path, 64, vr)
}

func parseFloatToken(raw json.RawMessage, path string, bits int, vr core.VR) (float64, error) {
	token, err := decodeRequiredNumberOrStringValue(raw, path, vr)
	if err != nil {
		return 0, err
	}
	switch strings.ToLower(token) {
	case "nan":
		return math.NaN(), nil
	case "inf", "+inf", "infinity", "+infinity":
		return math.Inf(1), nil
	case "-inf", "-infinity":
		return math.Inf(-1), nil
	}
	value, err := strconv.ParseFloat(token, bits)
	if err != nil {
		return 0, valueTypePathError(path, vr, raw, "parse float: %v", err)
	}
	return value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func decodeJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if err := ensureJSONEOF(dec); err != nil {
		return err
	}
	return nil
}

func ensureJSONEOF(dec *json.Decoder) error {
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return fmt.Errorf("unexpected trailing data")
		}
		return err
	}
	return nil
}

func joinJSONPath(prefix, suffix string) string {
	if prefix == "" {
		return suffix
	}
	return prefix + "/" + suffix
}

func indexJSONPath(path string, index int) string {
	return fmt.Sprintf("%s[%d]", path, index)
}

func jsonValueType(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "empty"
	}
	switch trimmed[0] {
	case '{':
		return "object"
	case '[':
		return "array"
	case '"':
		return "string"
	case 't', 'f':
		return "boolean"
	case 'n':
		return "null"
	case '-', '0', '1', '2', '3', '4', '5', '6', '7', '8', '9':
		return "number"
	default:
		return "invalid"
	}
}

func valueTypePathError(path string, vr core.VR, raw json.RawMessage, format string, args ...any) error {
	return pathError(path, "VR %s Value received %s: %s", vr, jsonValueType(raw), fmt.Sprintf(format, args...))
}

func pathError(path, format string, args ...any) error {
	return fmt.Errorf("dicomjson: %s at %s", fmt.Sprintf(format, args...), path)
}
