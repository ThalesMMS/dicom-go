package dicomjson

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/object"
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
	BulkDataURIFunc func(tag core.Tag, vr core.VR, data []byte) string
}

func DefaultOptions() Options {
	return Options{
		Pretty:          true,
		OmitGroupLength: true,
	}
}

func Marshal(obj *object.Object, opts Options) ([]byte, error) {
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
	return UnmarshalWithTextOptions(data, dict, object.TextOptions{})
}

func UnmarshalWithTextOptions(data []byte, dict dictionary.DataDictionary, opts object.TextOptions) (*object.Object, error) {
	ds, err := unmarshalDataSet(data, "", dict)
	if err != nil {
		return nil, err
	}
	return object.FromElementsWithTextOptions(ds.Elements, dict, opts), nil
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
		raw, ok := elem.RawBytes()
		if !ok {
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

func unmarshalDataSet(data []byte, path string, dict dictionary.DataDictionary) (core.DataSet, error) {
	var raw map[string]json.RawMessage
	if err := decodeJSON(data, &raw); err != nil {
		return core.DataSet{}, fmt.Errorf("dicomjson: decode dataset JSON: %w", err)
	}
	return unmarshalDataSetMap(raw, path, dict)
}

func unmarshalDataSetMap(raw map[string]json.RawMessage, path string, dict dictionary.DataDictionary) (core.DataSet, error) {
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
		elem, err := unmarshalElement(tag, raw[tagStr], tagPath, dict)
		if err != nil {
			return core.DataSet{}, err
		}
		elements = append(elements, elem)
	}
	return core.DataSet{Elements: elements}, nil
}

func unmarshalElement(tag core.Tag, raw json.RawMessage, path string, dict dictionary.DataDictionary) (core.Element, error) {
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
		value, err := unmarshalSequence(fields["Value"], path, dict)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRPN:
		value, err := unmarshalPersonNames(fields["Value"], valuePath)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRDS, core.VRIS:
		value, err := unmarshalNumberStrings(fields["Value"], valuePath)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRAT:
		value, err := unmarshalAttributeTags(fields["Value"], valuePath)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	case core.VRUN:
		return core.Element{}, pathError(valuePath, "can't parse JSON Value in UN")
	case core.VRFL, core.VROF, core.VRFD, core.VROD, core.VRSS, core.VRUS, core.VROW, core.VRSL, core.VRUL, core.VROL, core.VRSV, core.VRUV, core.VROV, core.VROB:
		value, err := encodeNumericValues(vr, fields["Value"], valuePath)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = core.RawValue(value)
	case core.VRAE, core.VRAS, core.VRCS, core.VRDA, core.VRDT, core.VRLO, core.VRLT, core.VRSH, core.VRST, core.VRUT, core.VRUR, core.VRTM, core.VRUC, core.VRUI:
		value, err := unmarshalStringValues(fields["Value"], valuePath)
		if err != nil {
			return core.Element{}, err
		}
		elem.Value = value
	default:
		return core.Element{}, pathError(valuePath, "unsupported VR %s", vr)
	}

	return elem, nil
}

func unmarshalStringValues(raw json.RawMessage, path string) (core.StringValue, error) {
	items, err := decodeJSONArray(raw, path)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		value, err := decodeNullableString(items[i], indexJSONPath(path, i))
		if err != nil {
			return nil, err
		}
		values[i] = value
	}
	return core.StringValue(values), nil
}

func unmarshalNumberStrings(raw json.RawMessage, path string) (core.StringValue, error) {
	items, err := decodeJSONArray(raw, path)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		token, err := decodeNullableNumberOrString(items[i], indexJSONPath(path, i))
		if err != nil {
			return nil, err
		}
		values[i] = token
	}
	return core.StringValue(values), nil
}

func unmarshalPersonNames(raw json.RawMessage, path string) (core.StringValue, error) {
	items, err := decodeJSONArray(raw, path)
	if err != nil {
		return nil, err
	}
	values := make([]string, len(items))
	for i := range items {
		itemPath := indexJSONPath(path, i)
		if isJSONNull(items[i]) {
			continue
		}
		var pn PersonNameComponents
		if err := decodeJSON(items[i], &pn); err != nil {
			return nil, pathError(itemPath, "decode person name: %v", err)
		}
		values[i] = joinPersonName(pn)
	}
	return core.StringValue(values), nil
}

func unmarshalSequence(raw json.RawMessage, path string, dict dictionary.DataDictionary) (core.SequenceValue, error) {
	items, err := decodeJSONArray(raw, joinJSONPath(path, "Value"))
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
		ds, err := unmarshalDataSetMap(dataset, itemPath, dict)
		if err != nil {
			return core.SequenceValue{}, err
		}
		seq.Items = append(seq.Items, ds)
	}
	return seq, nil
}

func unmarshalAttributeTags(raw json.RawMessage, path string) (core.RawValue, error) {
	items, err := decodeJSONArray(raw, path)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, len(items)*4)
	for i := range items {
		itemPath := indexJSONPath(path, i)
		value, err := decodeRequiredString(items[i], itemPath)
		if err != nil {
			return nil, err
		}
		tag, err := parseJSONTag(value, itemPath)
		if err != nil {
			return nil, err
		}
		var buf [4]byte
		binary.LittleEndian.PutUint16(buf[0:2], tag.Group)
		binary.LittleEndian.PutUint16(buf[2:4], tag.Element)
		data = append(data, buf[:]...)
	}
	return core.RawValue(data), nil
}

func encodeNumericValues(vr core.VR, raw json.RawMessage, path string) ([]byte, error) {
	items, err := decodeJSONArray(raw, path)
	if err != nil {
		return nil, err
	}
	data := make([]byte, 0, len(items)*8)
	for i := range items {
		itemPath := indexJSONPath(path, i)
		switch vr {
		case core.VRFL, core.VROF:
			v, err := parseFloat32(items[i], itemPath)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], math.Float32bits(v))
			data = append(data, buf[:]...)
		case core.VRFD, core.VROD:
			v, err := parseFloat64(items[i], itemPath)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], math.Float64bits(v))
			data = append(data, buf[:]...)
		case core.VRSS:
			v, err := parseInt(items[i], itemPath, 16)
			if err != nil {
				return nil, err
			}
			var buf [2]byte
			binary.LittleEndian.PutUint16(buf[:], uint16(int16(v)))
			data = append(data, buf[:]...)
		case core.VRUS, core.VROW:
			v, err := parseUint(items[i], itemPath, 16)
			if err != nil {
				return nil, err
			}
			var buf [2]byte
			binary.LittleEndian.PutUint16(buf[:], uint16(v))
			data = append(data, buf[:]...)
		case core.VRSL:
			v, err := parseInt(items[i], itemPath, 32)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(int32(v)))
			data = append(data, buf[:]...)
		case core.VRUL, core.VROL:
			v, err := parseUint(items[i], itemPath, 32)
			if err != nil {
				return nil, err
			}
			var buf [4]byte
			binary.LittleEndian.PutUint32(buf[:], uint32(v))
			data = append(data, buf[:]...)
		case core.VRSV:
			v, err := parseInt(items[i], itemPath, 64)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], uint64(v))
			data = append(data, buf[:]...)
		case core.VRUV, core.VROV:
			v, err := parseUint(items[i], itemPath, 64)
			if err != nil {
				return nil, err
			}
			var buf [8]byte
			binary.LittleEndian.PutUint64(buf[:], v)
			data = append(data, buf[:]...)
		case core.VROB:
			v, err := parseUint(items[i], itemPath, 8)
			if err != nil {
				return nil, err
			}
			data = append(data, byte(v))
		default:
			return nil, pathError(path, "unsupported numeric VR %s", vr)
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

func decodeJSONArray(raw json.RawMessage, path string) ([]json.RawMessage, error) {
	var items []json.RawMessage
	if err := decodeJSON(raw, &items); err != nil {
		return nil, pathError(path, "expected JSON array: %v", err)
	}
	return items, nil
}

func decodeNullableString(raw json.RawMessage, path string) (string, error) {
	if isJSONNull(raw) {
		return "", nil
	}
	return decodeRequiredString(raw, path)
}

func decodeRequiredString(raw json.RawMessage, path string) (string, error) {
	var value string
	if err := decodeJSON(raw, &value); err != nil {
		return "", pathError(path, "expected string: %v", err)
	}
	return value, nil
}

func decodeNullableNumberOrString(raw json.RawMessage, path string) (string, error) {
	if isJSONNull(raw) {
		return "", nil
	}
	return decodeRequiredNumberOrString(raw, path)
}

func decodeRequiredNumberOrString(raw json.RawMessage, path string) (string, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "", pathError(path, "empty JSON value")
	}
	if trimmed[0] == '"' {
		return decodeRequiredString(raw, path)
	}
	var number json.Number
	if err := decodeJSON(raw, &number); err != nil {
		return "", pathError(path, "expected number or string: %v", err)
	}
	return number.String(), nil
}

func parseInt(raw json.RawMessage, path string, bits int) (int64, error) {
	token, err := decodeRequiredNumberOrString(raw, path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseInt(token, 10, bits)
	if err != nil {
		return 0, pathError(path, "parse signed integer: %v", err)
	}
	return value, nil
}

func parseUint(raw json.RawMessage, path string, bits int) (uint64, error) {
	token, err := decodeRequiredNumberOrString(raw, path)
	if err != nil {
		return 0, err
	}
	value, err := strconv.ParseUint(token, 10, bits)
	if err != nil {
		return 0, pathError(path, "parse unsigned integer: %v", err)
	}
	return value, nil
}

func parseFloat32(raw json.RawMessage, path string) (float32, error) {
	value, err := parseFloatToken(raw, path, 32)
	if err != nil {
		return 0, err
	}
	return float32(value), nil
}

func parseFloat64(raw json.RawMessage, path string) (float64, error) {
	return parseFloatToken(raw, path, 64)
}

func parseFloatToken(raw json.RawMessage, path string, bits int) (float64, error) {
	token, err := decodeRequiredNumberOrString(raw, path)
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
		return 0, pathError(path, "parse float: %v", err)
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

func pathError(path, format string, args ...any) error {
	return fmt.Errorf("dicomjson: %s at %s", fmt.Sprintf(format, args...), path)
}
