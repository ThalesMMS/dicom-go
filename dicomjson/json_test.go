package dicomjson

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"math"
	"sort"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestMarshalSerializesStringLikeVR(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "OT"),
		},
	}, std.Dictionary)

	got, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\"00080060\":{\"vr\":\"CS\",\"Value\":[\"OT\"]}}"
	if string(got) != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestMarshalSerializesBinaryVRAsInlineBinary(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewOBElement(core.NewTag(0x7FE0, 0x0010), []byte{0x01, 0x02, 0x03, 0x04}),
		},
	}, std.Dictionary)

	got, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\"7FE00010\":{\"vr\":\"OB\",\"InlineBinary\":\"AQIDBA==\"}}"
	if string(got) != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestMarshalSerializesSequenceRecursively(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewSequenceElement(
				core.NewTag(0x0008, 0x1111),
				core.DataSet{
					Elements: []core.Element{
						dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Yamada^Tarou=山田^太郎=やまだ^たろう"),
						dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "SEQ-001"),
					},
				},
				core.DataSet{
					Elements: []core.Element{
						dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Doe^Jane"),
					},
				},
			),
		},
	}, std.Dictionary)

	got, err := MarshalPretty(obj)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}

	entry := decoded["00081111"]
	if entry["vr"] != "SQ" {
		t.Fatalf("vr = %v, want SQ", entry["vr"])
	}
	items, ok := entry["Value"].([]any)
	if !ok {
		t.Fatalf("Value type = %T, want []any", entry["Value"])
	}
	if len(items) != 2 {
		t.Fatalf("sequence item count = %d, want 2", len(items))
	}

	firstItem, ok := items[0].(map[string]any)
	if !ok {
		t.Fatalf("first item type = %T, want map[string]any", items[0])
	}
	if _, ok := firstItem["00100020"]; !ok {
		t.Fatalf("first item missing nested LO element: %#v", firstItem)
	}
}

func TestMarshalSerializesPersonNameComponents(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Yamada^Tarou=山田^太郎=やまだ^たろう"),
		},
	}, std.Dictionary)

	got, err := MarshalPretty(obj)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}

	entry := decoded["00100010"]
	values, ok := entry["Value"].([]any)
	if !ok || len(values) != 1 {
		t.Fatalf("PN Value = %#v, want single value", entry["Value"])
	}
	components, ok := values[0].(map[string]any)
	if !ok {
		t.Fatalf("PN component type = %T, want map[string]any", values[0])
	}
	if components["Alphabetic"] != "Yamada^Tarou" {
		t.Fatalf("Alphabetic = %v, want Yamada^Tarou", components["Alphabetic"])
	}
	if components["Ideographic"] != "山田^太郎" {
		t.Fatalf("Ideographic = %v, want 山田^太郎", components["Ideographic"])
	}
	if components["Phonetic"] != "やまだ^たろう" {
		t.Fatalf("Phonetic = %v, want やまだ^たろう", components["Phonetic"])
	}
}

func TestMarshalHandlesEmptyValues(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"Doe^John", "", "Roe^Jane"},
			},
			{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO},
				Value:  core.StringValue{"A", "", "C"},
			},
			core.NewRawElement(core.NewTag(0x0010, 0x0030), core.VRLO, []byte{}),
			core.NewRawElement(core.NewTag(0x7FE0, 0x0010), core.VROB, []byte{}),
		},
	}, std.Dictionary)

	got, err := MarshalPretty(obj)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]map[string]any
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}

	pnValues, ok := decoded["00100010"]["Value"].([]any)
	if !ok || len(pnValues) != 3 {
		t.Fatalf("PN Value = %#v, want 3 entries", decoded["00100010"]["Value"])
	}
	if pnValues[1] != nil {
		t.Fatalf("PN empty component = %#v, want nil", pnValues[1])
	}

	loValues, ok := decoded["00100020"]["Value"].([]any)
	if !ok || len(loValues) != 3 {
		t.Fatalf("LO Value = %#v, want 3 entries", decoded["00100020"]["Value"])
	}
	if loValues[1] != nil {
		t.Fatalf("LO empty component = %#v, want nil", loValues[1])
	}

	if _, ok := decoded["00100030"]["Value"]; ok {
		t.Fatalf("zero-length LO unexpectedly included Value: %#v", decoded["00100030"])
	}
	if _, ok := decoded["7FE00010"]["Value"]; ok {
		t.Fatalf("zero-length OB unexpectedly included Value: %#v", decoded["7FE00010"])
	}
	if _, ok := decoded["7FE00010"]["InlineBinary"]; ok {
		t.Fatalf("zero-length OB unexpectedly included InlineBinary: %#v", decoded["7FE00010"])
	}
}

func TestMarshalOmitsGroupLengthByDefault(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.Uint32Element(core.NewTag(0x0002, 0x0000), core.VRUL, nil, 174),
			dicomtest.NewUIElement(core.NewTag(0x0002, 0x0010), "1.2.840.10008.1.2.1"),
		},
	}, std.Dictionary)

	got, err := MarshalObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(got), "00020000") {
		t.Fatalf("MarshalObject() unexpectedly included group length: %s", got)
	}
}

func TestMarshalCompactAndPrettyFormatting(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewUIElement(core.NewTag(0x0002, 0x0010), "1.2.840.10008.1.2.1"),
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Doe^John"),
		},
	}, std.Dictionary)

	compact, err := MarshalCompact(obj)
	if err != nil {
		t.Fatal(err)
	}
	pretty, err := MarshalPretty(obj)
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(compact), "\n") {
		t.Fatalf("MarshalCompact() should not include newlines: %q", compact)
	}
	if !strings.Contains(string(pretty), "\n") {
		t.Fatalf("MarshalPretty() should include newlines: %q", pretty)
	}
}

func TestMarshalRetainsVRForMalformedSequenceElement(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			{
				Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x1111), VR: core.VRSQ},
				Value:  core.RawValue([]byte{0x01, 0x02}),
			},
		},
	}, std.Dictionary)

	got, err := MarshalCompact(obj)
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\"00081111\":{\"vr\":\"SQ\"}}"
	if string(got) != want {
		t.Fatalf("MarshalCompact() = %s, want %s", got, want)
	}
}

func TestMarshalObjectDelegatesToDefaultOptions(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.Uint32Element(core.NewTag(0x0002, 0x0000), core.VRUL, nil, 174),
			dicomtest.NewUIElement(core.NewTag(0x0002, 0x0010), "1.2.840.10008.1.2.1"),
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Doe^John"),
		},
	}, std.Dictionary)

	got, err := MarshalObject(obj)
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\n  \"00020010\": {\n    \"vr\": \"UI\",\n    \"Value\": [\n      \"1.2.840.10008.1.2.1\"\n    ]\n  },\n  \"00100010\": {\n    \"vr\": \"PN\",\n    \"Value\": [\n      {\n        \"Alphabetic\": \"Doe^John\"\n      }\n    ]\n  }\n}"
	if string(got) != want {
		t.Fatalf("MarshalObject() = %s, want %s", got, want)
	}
}

func TestMarshalUsesBulkDataURIWhenConfigured(t *testing.T) {
	tag := core.NewTag(0x7FE0, 0x0010)
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			dicomtest.NewOBElement(tag, []byte{0x01, 0x02, 0x03, 0x04}),
		},
	}, std.Dictionary)

	got, err := Marshal(obj, Options{
		BulkDataURIFunc: func(gotTag core.Tag, vr core.VR, data []byte) string {
			if gotTag != tag {
				t.Fatalf("tag = %s, want %s", gotTag, tag)
			}
			if vr != core.VROB {
				t.Fatalf("vr = %s, want OB", vr)
			}
			if string(data) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
				t.Fatalf("data = %v, want [1 2 3 4]", data)
			}
			return "https://example.test/bulk/7fe00010"
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\"7FE00010\":{\"vr\":\"OB\",\"BulkDataURI\":\"https://example.test/bulk/7fe00010\"}}"
	if string(got) != want {
		t.Fatalf("Marshal() = %s, want %s", got, want)
	}
}

func TestMarshalSerializesBulkDataValueAsBulkDataURI(t *testing.T) {
	obj := object.FromDataSet(core.DataSet{
		Elements: []core.Element{
			{
				Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB},
				Value:  core.BulkDataValue{URI: "https://example.test/bulk/7fe00010"},
			},
		},
	}, std.Dictionary)

	got, err := MarshalCompact(obj)
	if err != nil {
		t.Fatal(err)
	}

	const want = "{\"7FE00010\":{\"vr\":\"OB\",\"BulkDataURI\":\"https://example.test/bulk/7fe00010\"}}"
	if string(got) != want {
		t.Fatalf("MarshalCompact() = %s, want %s", got, want)
	}
}

func TestUnmarshalRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ds   core.DataSet
	}{
		{
			name: "minimal",
			ds:   core.DataSet{Elements: dicomtest.MinimalDataset()},
		},
		{
			name: "pixel_data",
			ds:   core.DataSet{Elements: dicomtest.DatasetWithPixelData()},
		},
		{
			name: "nested_sequence",
			ds:   dicomtest.BenchmarkSequenceDataSet(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := object.FromDataSet(tt.ds, std.Dictionary)

			encoded, err := MarshalCompact(obj)
			if err != nil {
				t.Fatal(err)
			}

			decoded, err := Unmarshal(encoded, std.Dictionary)
			if err != nil {
				t.Fatal(err)
			}

			want := comparableDataSet(core.DataSet{Elements: object.FromDataSet(tt.ds, std.Dictionary).SortedElements()})
			got := comparableDataSet(decoded.ToDataSet())
			if diff := dicomtest.DiffDataSet(got, want); diff != "" {
				t.Fatalf("round-trip dataset mismatch:\n%s", diff)
			}

			encodedAgain, err := MarshalCompact(decoded)
			if err != nil {
				t.Fatal(err)
			}
			if string(encodedAgain) != string(encoded) {
				t.Fatalf("MarshalCompact(Unmarshal(MarshalCompact(obj))) = %s, want %s", encodedAgain, encoded)
			}
		})
	}
}

func comparableDataSet(ds core.DataSet) core.DataSet {
	out := core.DataSet{Elements: make([]core.Element, len(ds.Elements))}
	for i := range ds.Elements {
		out.Elements[i] = comparableElement(ds.Elements[i])
	}
	sort.Slice(out.Elements, func(i, j int) bool {
		return out.Elements[i].Tag().Less(out.Elements[j].Tag())
	})
	return out
}

func comparableElement(elem core.Element) core.Element {
	elem.Header.Length = 0
	elem.Header.LengthSet = false
	switch value := elem.Value.(type) {
	case core.SequenceValue:
		items := make([]core.DataSet, len(value.Items))
		for i := range value.Items {
			items[i] = comparableDataSet(value.Items[i])
		}
		elem.Value = core.SequenceValue{Items: items}
	}
	return elem
}

func TestUnmarshalParsesNumericIntegerVRsAsLittleEndianBytes(t *testing.T) {
	const src = `{
		"00280002": {"vr":"US","Value":[1,513]},
		"00280100": {"vr":"UL","Value":[305419896]},
		"00280103": {"vr":"SS","Value":[-1,-2]},
		"00280010": {"vr":"SL","Value":[-2147483648,42]}
	}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	usRaw, ok := obj.GetRaw(core.NewTag(0x0028, 0x0002))
	if !ok {
		t.Fatal("missing US raw value")
	}
	if len(usRaw) != 4 || binary.LittleEndian.Uint16(usRaw[0:2]) != 1 || binary.LittleEndian.Uint16(usRaw[2:4]) != 513 {
		t.Fatalf("US raw = %v, want little-endian [1 513]", usRaw)
	}

	ulRaw, ok := obj.GetRaw(core.NewTag(0x0028, 0x0100))
	if !ok || len(ulRaw) != 4 || binary.LittleEndian.Uint32(ulRaw) != 305419896 {
		t.Fatalf("UL raw = %v", ulRaw)
	}

	ssRaw, ok := obj.GetRaw(core.NewTag(0x0028, 0x0103))
	if !ok || len(ssRaw) != 4 || int16(binary.LittleEndian.Uint16(ssRaw[0:2])) != -1 || int16(binary.LittleEndian.Uint16(ssRaw[2:4])) != -2 {
		t.Fatalf("SS raw = %v", ssRaw)
	}

	slRaw, ok := obj.GetRaw(core.NewTag(0x0028, 0x0010))
	if !ok || len(slRaw) != 8 || int32(binary.LittleEndian.Uint32(slRaw[0:4])) != math.MinInt32 || int32(binary.LittleEndian.Uint32(slRaw[4:8])) != 42 {
		t.Fatalf("SL raw = %v", slRaw)
	}
}

func TestUnmarshalParsesNumericFloatVRsAsLittleEndianBytes(t *testing.T) {
	const src = `{
		"00181160": {"vr":"FL","Value":[1.5,-2.25]},
		"00181164": {"vr":"FD","Value":["NaN","Infinity","-Infinity",3.5]}
	}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	flRaw, ok := obj.GetRaw(core.NewTag(0x0018, 0x1160))
	if !ok || len(flRaw) != 8 {
		t.Fatalf("FL raw = %v", flRaw)
	}
	if math.Float32frombits(binary.LittleEndian.Uint32(flRaw[0:4])) != float32(1.5) {
		t.Fatalf("FL[0] = %v, want 1.5", math.Float32frombits(binary.LittleEndian.Uint32(flRaw[0:4])))
	}
	if math.Float32frombits(binary.LittleEndian.Uint32(flRaw[4:8])) != float32(-2.25) {
		t.Fatalf("FL[1] = %v, want -2.25", math.Float32frombits(binary.LittleEndian.Uint32(flRaw[4:8])))
	}

	fdRaw, ok := obj.GetRaw(core.NewTag(0x0018, 0x1164))
	if !ok || len(fdRaw) != 32 {
		t.Fatalf("FD raw = %v", fdRaw)
	}
	if !math.IsNaN(math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[0:8]))) {
		t.Fatalf("FD[0] should be NaN, got %v", math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[0:8])))
	}
	if !math.IsInf(math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[8:16])), 1) {
		t.Fatalf("FD[1] should be +Inf")
	}
	if !math.IsInf(math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[16:24])), -1) {
		t.Fatalf("FD[2] should be -Inf")
	}
	if math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[24:32])) != 3.5 {
		t.Fatalf("FD[3] = %v, want 3.5", math.Float64frombits(binary.LittleEndian.Uint64(fdRaw[24:32])))
	}
}

func TestUnmarshalTreatsDSAndISAsStringValues(t *testing.T) {
	const src = `{
		"00181063": {"vr":"DS","Value":[1.5,"2.5",null]},
		"00200013": {"vr":"IS","Value":[7,"8",null]}
	}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	dsElem, ok := obj.Get(core.NewTag(0x0018, 0x1063))
	if !ok {
		t.Fatal("missing DS element")
	}
	dsValue, ok := dsElem.Value.(core.StringValue)
	if !ok {
		t.Fatalf("DS value type = %T, want core.StringValue", dsElem.Value)
	}
	if len(dsValue) != 3 || dsValue[0] != "1.5" || dsValue[1] != "2.5" || dsValue[2] != "" {
		t.Fatalf("DS values = %v", dsValue)
	}
	dsValues, ok := obj.GetStrings(core.NewTag(0x0018, 0x1063))
	if !ok || len(dsValues) != 3 || dsValues[0] != "1.5" || dsValues[1] != "2.5" || dsValues[2] != "" {
		t.Fatalf("DS values = %v", dsValues)
	}

	isElem, ok := obj.Get(core.NewTag(0x0020, 0x0013))
	if !ok {
		t.Fatal("missing IS element")
	}
	isValue, ok := isElem.Value.(core.StringValue)
	if !ok {
		t.Fatalf("IS value type = %T, want core.StringValue", isElem.Value)
	}
	if len(isValue) != 3 || isValue[0] != "7" || isValue[1] != "8" || isValue[2] != "" {
		t.Fatalf("IS values = %v", isValue)
	}
	isValues, ok := obj.GetStrings(core.NewTag(0x0020, 0x0013))
	if !ok || len(isValues) != 3 || isValues[0] != "7" || isValues[1] != "8" || isValues[2] != "" {
		t.Fatalf("IS values = %v", isValues)
	}
}

func TestUnmarshalParsesAdditionalNumericVRs(t *testing.T) {
	const src = `{
		"00280011": {"vr":"SV","Value":["9223372036854775807"]},
		"7FE00010": {"vr":"OB","Value":[1,2,3,4]},
		"00080016": {"vr":"AT","Value":["00100010"]}
	}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	svRaw, ok := obj.GetRaw(core.NewTag(0x0028, 0x0011))
	if !ok || int64(binary.LittleEndian.Uint64(svRaw)) != math.MaxInt64 {
		t.Fatalf("SV raw = %v", svRaw)
	}

	obRaw, ok := obj.GetRaw(core.TagPixelData)
	if !ok || string(obRaw) != string([]byte{1, 2, 3, 4}) {
		t.Fatalf("OB raw = %v", obRaw)
	}
	atRaw, ok := obj.GetRaw(core.NewTag(0x0008, 0x0016))
	if !ok || len(atRaw) != 4 {
		t.Fatalf("AT raw = %v", atRaw)
	}
	if binary.LittleEndian.Uint16(atRaw[0:2]) != 0x0010 || binary.LittleEndian.Uint16(atRaw[2:4]) != 0x0010 {
		t.Fatalf("AT raw = %v, want tag 00100010", atRaw)
	}
}

func TestUnmarshalPreservesBulkDataURIReference(t *testing.T) {
	const src = `{"7FE00010":{"vr":"OB","BulkDataURI":"https://example.test/bulk/7fe00010"}}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		t.Fatal("missing Pixel Data element")
	}
	bulk, ok := elem.Value.(core.BulkDataValue)
	if !ok {
		t.Fatalf("value type = %T, want core.BulkDataValue", elem.Value)
	}
	if bulk.URI != "https://example.test/bulk/7fe00010" {
		t.Fatalf("BulkDataURI = %q", bulk.URI)
	}

	got, err := MarshalCompact(obj)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != src {
		t.Fatalf("MarshalCompact() = %s, want %s", got, src)
	}
}

func TestUnmarshalParsesInlineBinary(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte{0x01, 0x02, 0x03, 0x04})
	src := `{"7FE00010":{"vr":"OB","InlineBinary":"` + encoded + `"}}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}

	raw, ok := obj.GetRaw(core.TagPixelData)
	if !ok {
		t.Fatal("missing raw pixel data")
	}
	if string(raw) != string([]byte{0x01, 0x02, 0x03, 0x04}) {
		t.Fatalf("raw = %v, want [1 2 3 4]", raw)
	}
}

func TestUnmarshalErrorsIncludePath(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		contains []string
	}{
		{
			name:     "invalid_tag_format",
			src:      `{"0010001":{"vr":"PN","Value":["Doe^John"]}}`,
			contains: []string{`invalid tag "0010001"`, "0010001"},
		},
		{
			name:     "missing_vr_field",
			src:      `{"00100010":{"Value":["Doe^John"]}}`,
			contains: []string{"missing VR field", "00100010/vr"},
		},
		{
			name:     "conflicting_value_and_inline_binary",
			src:      `{"7FE00010":{"vr":"OB","Value":[1],"InlineBinary":"AQ=="}}`,
			contains: []string{`"Value" conflicts with "InlineBinary"`, "7FE00010"},
		},
		{
			name:     "invalid_inline_binary_base64",
			src:      `{"7FE00010":{"vr":"OB","InlineBinary":"***"}}`,
			contains: []string{"inline binary data is not valid base64", "7FE00010/InlineBinary"},
		},
		{
			name:     "type_mismatch_in_value_array",
			src:      `{"00100020":{"vr":"LO","Value":[123]}}`,
			contains: []string{"00100020/Value[0]", "expected string"},
		},
		{
			name: "nested_sequence_type_mismatch",
			src: `{
				"00081111": {
					"vr": "SQ",
					"Value": [
						{
							"00100020": {
								"vr": "LO",
								"Value": [123]
							}
						}
					]
				}
			}`,
			contains: []string{"00081111[0]/00100020/Value[0]", "expected string"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Unmarshal([]byte(tt.src), std.Dictionary)
			if err == nil {
				t.Fatal("Unmarshal() unexpectedly succeeded")
			}
			for _, want := range tt.contains {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %v, want substring %q", err, want)
				}
			}
		})
	}
}
