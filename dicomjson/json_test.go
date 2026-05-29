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
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
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

func TestMarshalPreservesEncapsulatedPixelDataValueField(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xAA, 0xBB, 0xCC},
		[]byte{0x10, 0x20, 0x30, 0x40},
	)
	encoded := dicomtest.EncodeElement(pixel, transfer.JPEGBaseline)
	const explicitLongHeaderLength = 12
	wantValueField := encoded[explicitLongHeaderLength:]

	deferred, err := object.ReadDataSetWithOptions(bytes.NewReader(encoded), transfer.JPEGBaseline, object.ReadFileOptions{
		DeferPixelData: true,
	})
	if err != nil {
		t.Fatalf("ReadDataSetWithOptions() error = %v", err)
	}
	t.Cleanup(func() { _ = deferred.Close() })

	for _, test := range []struct {
		name string
		obj  *object.Object
	}{
		{name: "materialized fragments", obj: object.FromElements([]core.Element{pixel}, std.Dictionary)},
		{name: "deferred fragments", obj: deferred},
	} {
		t.Run(test.name, func(t *testing.T) {
			var streamed []byte
			bulkJSON, err := Marshal(test.obj, Options{
				Pretty: false,
				PixelDataBulkDataURIFunc: func(tag core.Tag, vr core.VR, open func() (io.ReadCloser, error)) (string, error) {
					reader, err := open()
					if err != nil {
						return "", err
					}
					defer reader.Close()
					streamed, err = io.ReadAll(reader)
					return "https://example.test/pixel-data", err
				},
			})
			if err != nil {
				t.Fatalf("streaming Marshal() error = %v", err)
			}
			if !bytes.Equal(streamed, wantValueField) {
				t.Fatalf("streamed Pixel Data = % X, want % X", streamed, wantValueField)
			}
			if !bytes.Contains(bulkJSON, []byte(`"BulkDataURI":"https://example.test/pixel-data"`)) {
				t.Fatalf("streaming Marshal() = %s, want BulkDataURI", bulkJSON)
			}

			encodedJSON, err := MarshalCompact(test.obj)
			if err != nil {
				t.Fatalf("MarshalCompact() error = %v", err)
			}
			var decoded map[string]Element
			if err := json.Unmarshal(encodedJSON, &decoded); err != nil {
				t.Fatalf("json.Unmarshal() error = %v", err)
			}
			gotValueField, err := base64.StdEncoding.DecodeString(decoded["7FE00010"].InlineBinary)
			if err != nil {
				t.Fatalf("InlineBinary is not base64: %v", err)
			}
			if !bytes.Equal(gotValueField, wantValueField) {
				t.Fatalf("InlineBinary Value Field = % X, want % X", gotValueField, wantValueField)
			}
		})
	}
}

func TestMarshalStreamsDeferredPrimitiveValue(t *testing.T) {
	tag := core.NewTag(0x0011, 0x1010)
	want := []byte{0x01, 0x02, 0x03, 0x04}
	encoded := dicomtest.EncodeElement(core.NewRawElement(tag, core.VROB, want), transfer.ExplicitVRLittleEndian)
	obj, err := object.ReadDataSetWithOptions(bytes.NewReader(encoded), transfer.ExplicitVRLittleEndian, object.ReadFileOptions{
		InlineValueBytesThreshold: 1,
	})
	if err != nil {
		t.Fatalf("ReadDataSetWithOptions() error = %v", err)
	}
	t.Cleanup(func() { _ = obj.Close() })

	encodedJSON, err := MarshalCompact(obj)
	if err != nil {
		t.Fatalf("MarshalCompact() error = %v", err)
	}
	var decoded map[string]Element
	if err := json.Unmarshal(encodedJSON, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := base64.StdEncoding.DecodeString(decoded[tag.HexString()].InlineBinary)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("InlineBinary = % X, %v; want % X", got, err, want)
	}
}

func TestMarshalRejectsInvalidFragmentOffsetTable(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(core.TagPixelData, []byte{0x01})
	obj := object.FromElements([]core.Element{pixel}, std.Dictionary)

	_, err := MarshalCompact(obj)
	if err == nil || !strings.Contains(err.Error(), "Basic Offset Table length 1 is not a multiple of 4") {
		t.Fatalf("MarshalCompact() error = %v, want invalid Basic Offset Table length", err)
	}
}

func TestMarshalRejectsUnavailableDeferredValueButAllowsExplicitEmptyValue(t *testing.T) {
	tag := core.NewTag(0x0011, 0x1010)
	missing := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tag, VR: core.VROB, Length: 4, LengthSet: true},
		Value:  nil,
	}}, std.Dictionary)
	if _, err := MarshalCompact(missing); err == nil || !strings.Contains(err.Error(), "no value provider") {
		t.Fatalf("MarshalCompact(missing deferred) error = %v, want no value provider", err)
	}

	empty := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: tag, VR: core.VROB, Length: 0, LengthSet: true},
		Value:  nil,
	}}, std.Dictionary)
	got, err := MarshalCompact(empty)
	if err != nil {
		t.Fatalf("MarshalCompact(empty) error = %v", err)
	}
	const want = `{"00111010":{"vr":"OB"}}`
	if string(got) != want {
		t.Fatalf("MarshalCompact(empty) = %s, want %s", got, want)
	}
}

func TestMarshalSerializesVRValueMatrix(t *testing.T) {
	tests := []struct {
		name       string
		element    core.Element
		wantValue  []any
		wantInline []byte
	}{
		{
			name:      "AE string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x0054), core.VRAE, "DEST_AE"),
			wantValue: []any{"DEST_AE"},
		},
		{
			name:      "AS string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0010, 0x1010), core.VRAS, "042Y"),
			wantValue: []any{"042Y"},
		},
		{
			name:      "CS string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "OT"),
			wantValue: []any{"OT"},
		},
		{
			name:      "DA string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0010, 0x0030), core.VRDA, "20240604"),
			wantValue: []any{"20240604"},
		},
		{
			name:      "DS numeric string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0018, 0x1063), core.VRDS, "1.5"),
			wantValue: []any{"1.5"},
		},
		{
			name:      "DT string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x002A), core.VRDT, "20240604123000"),
			wantValue: []any{"20240604123000"},
		},
		{
			name:      "AT tag list",
			element:   core.NewRawElement(core.NewTag(0x0008, 0x0008), core.VRAT, rawATValues(core.NewTag(0x0010, 0x0010), core.TagPixelData)),
			wantValue: []any{"00100010", "7FE00010"},
		},
		{
			name:      "FL float32",
			element:   core.NewRawElement(core.NewTag(0x0018, 0x1160), core.VRFL, rawFloat32Values(1.5, -2.25)),
			wantValue: []any{1.5, -2.25},
		},
		{
			name:      "FD float64",
			element:   core.NewRawElement(core.NewTag(0x0018, 0x1164), core.VRFD, rawFloat64Values(3.5, -4.75)),
			wantValue: []any{3.5, -4.75},
		},
		{
			name:      "IS integer string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0020, 0x0013), core.VRIS, "7"),
			wantValue: []any{"7"},
		},
		{
			name:      "LO string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "PATIENT-001"),
			wantValue: []any{"PATIENT-001"},
		},
		{
			name:      "LT string",
			element:   dicomtest.NewStringElement(core.NewTag(0x4000, 0x4000), core.VRLT, "long text"),
			wantValue: []any{"long text"},
		},
		{
			name:      "SS int16",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0103), core.VRSS, rawInt16Values(-1, 2)),
			wantValue: []any{-1, 2},
		},
		{
			name:      "US uint16",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0002), core.VRUS, rawUint16Values(1, 513)),
			wantValue: []any{1, 513},
		},
		{
			name:      "SL int32",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0010), core.VRSL, rawInt32Values(-2147483648, 42)),
			wantValue: []any{-2147483648, 42},
		},
		{
			name:      "UL uint32",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0100), core.VRUL, rawUint32Values(305419896)),
			wantValue: []any{305419896},
		},
		{
			name:      "SV int64",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0011), core.VRSV, rawInt64Values(-42)),
			wantValue: []any{-42},
		},
		{
			name:      "UV uint64",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0012), core.VRUV, rawUint64Values(42)),
			wantValue: []any{42},
		},
		{
			name:      "PN components",
			element:   dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "Doe^John=山田^太郎"),
			wantValue: []any{map[string]any{"Alphabetic": "Doe^John", "Ideographic": "山田^太郎"}},
		},
		{
			name:      "SH string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x103E), core.VRSH, "SERIES"),
			wantValue: []any{"SERIES"},
		},
		{
			name:      "SQ sequence",
			element:   dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1111), core.DataSet{}),
			wantValue: []any{map[string]any{}},
		},
		{
			name:      "ST string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x4000), core.VRST, "short text"),
			wantValue: []any{"short text"},
		},
		{
			name:      "TM string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x0030), core.VRTM, "123000"),
			wantValue: []any{"123000"},
		},
		{
			name:      "UC string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x0100), core.VRUC, "CODE"),
			wantValue: []any{"CODE"},
		},
		{
			name:      "UI string",
			element:   dicomtest.NewUIElement(core.NewTag(0x0008, 0x0018), "1.2.3"),
			wantValue: []any{"1.2.3"},
		},
		{
			name:      "UR string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0008, 0x1190), core.VRUR, "https://example.test/dicom"),
			wantValue: []any{"https://example.test/dicom"},
		},
		{
			name:      "UT string",
			element:   dicomtest.NewStringElement(core.NewTag(0x0040, 0xA160), core.VRUT, "unlimited text"),
			wantValue: []any{"unlimited text"},
		},
		{
			name:       "OB binary",
			element:    core.NewRawElement(core.TagPixelData, core.VROB, []byte{1, 2, 3}),
			wantInline: []byte{1, 2, 3},
		},
		{
			name:       "OD binary",
			element:    core.NewRawElement(core.NewTag(0x0066, 0x0040), core.VROD, rawFloat64Values(1.25)),
			wantInline: rawFloat64Values(1.25),
		},
		{
			name:       "OF binary",
			element:    core.NewRawElement(core.NewTag(0x0066, 0x0029), core.VROF, rawFloat32Values(1.25)),
			wantInline: rawFloat32Values(1.25),
		},
		{
			name:       "OL binary",
			element:    core.NewRawElement(core.NewTag(0x0066, 0x0041), core.VROL, rawUint32Values(1, 2)),
			wantInline: rawUint32Values(1, 2),
		},
		{
			name:       "OV binary",
			element:    core.NewRawElement(core.NewTag(0x0066, 0x0042), core.VROV, rawUint64Values(1, 2)),
			wantInline: rawUint64Values(1, 2),
		},
		{
			name:       "OW binary",
			element:    core.NewRawElement(core.NewTag(0x0028, 0x1201), core.VROW, rawUint16Values(1, 2)),
			wantInline: rawUint16Values(1, 2),
		},
		{
			name:       "UN binary",
			element:    core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VRUN, []byte{0xde, 0xad}),
			wantInline: []byte{0xde, 0xad},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := object.FromDataSet(core.DataSet{Elements: []core.Element{tt.element}}, std.Dictionary)

			got, err := MarshalCompact(obj)
			if err != nil {
				t.Fatal(err)
			}

			var decoded map[string]Element
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatal(err)
			}
			entry, ok := decoded[tt.element.Tag().HexString()]
			if !ok {
				t.Fatalf("missing element %s in %s", tt.element.Tag().HexString(), got)
			}
			if entry.VR != tt.element.VR().String() {
				t.Fatalf("vr = %s, want %s", entry.VR, tt.element.VR())
			}
			if tt.wantValue != nil {
				assertJSONValue(t, entry.Value, tt.wantValue)
				if entry.InlineBinary != "" {
					t.Fatalf("InlineBinary = %q, want omitted", entry.InlineBinary)
				}
				return
			}
			if len(entry.Value) != 0 {
				t.Fatalf("Value = %#v, want omitted", entry.Value)
			}
			gotInline, err := base64.StdEncoding.DecodeString(entry.InlineBinary)
			if err != nil {
				t.Fatalf("InlineBinary is not base64: %v", err)
			}
			if string(gotInline) != string(tt.wantInline) {
				t.Fatalf("InlineBinary bytes = %v, want %v", gotInline, tt.wantInline)
			}
		})
	}
}

func TestMarshalUsesConfiguredBigEndianForNumericAndATValues(t *testing.T) {
	tests := []struct {
		name      string
		element   core.Element
		wantValue []any
	}{
		{
			name:      "AT tag list",
			element:   core.NewRawElement(core.NewTag(0x0008, 0x0008), core.VRAT, rawATValuesWithOrder(binary.BigEndian, core.NewTag(0x0010, 0x0020))),
			wantValue: []any{"00100020"},
		},
		{
			name:      "US rows",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0010), core.VRUS, rawUint16ValuesWithOrder(binary.BigEndian, 512)),
			wantValue: []any{512},
		},
		{
			name:      "SS signed",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0103), core.VRSS, rawInt16ValuesWithOrder(binary.BigEndian, -2)),
			wantValue: []any{-2},
		},
		{
			name:      "UL unsigned",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0100), core.VRUL, rawUint32ValuesWithOrder(binary.BigEndian, 305419896)),
			wantValue: []any{305419896},
		},
		{
			name:      "SL signed",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0011), core.VRSL, rawInt32ValuesWithOrder(binary.BigEndian, math.MinInt32)),
			wantValue: []any{-2147483648},
		},
		{
			name:      "FL float32",
			element:   core.NewRawElement(core.NewTag(0x0018, 0x1160), core.VRFL, rawFloat32ValuesWithOrder(binary.BigEndian, 1.5)),
			wantValue: []any{1.5},
		},
		{
			name:      "FD float64",
			element:   core.NewRawElement(core.NewTag(0x0018, 0x1164), core.VRFD, rawFloat64ValuesWithOrder(binary.BigEndian, 3.5)),
			wantValue: []any{3.5},
		},
		{
			name:      "SV signed 64",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0012), core.VRSV, rawInt64ValuesWithOrder(binary.BigEndian, -42)),
			wantValue: []any{-42},
		},
		{
			name:      "UV unsigned 64",
			element:   core.NewRawElement(core.NewTag(0x0028, 0x0013), core.VRUV, rawUint64ValuesWithOrder(binary.BigEndian, 42)),
			wantValue: []any{42},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj := object.FromDataSet(core.DataSet{Elements: []core.Element{tt.element}}, std.Dictionary)

			got, err := Marshal(obj, Options{
				OmitGroupLength: true,
				ByteOrder:       binary.BigEndian,
			})
			if err != nil {
				t.Fatal(err)
			}

			var decoded map[string]Element
			if err := json.Unmarshal(got, &decoded); err != nil {
				t.Fatal(err)
			}
			entry := decoded[tt.element.Tag().HexString()]
			assertJSONValue(t, entry.Value, tt.wantValue)
		})
	}
}

func TestMarshalUsesObjectBigEndianByDefaultForNumericAndATValues(t *testing.T) {
	elements := []core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0008), core.VRAT, rawATValuesWithOrder(binary.BigEndian, core.NewTag(0x0010, 0x0020))),
		core.NewRawElement(core.NewTag(0x0028, 0x0010), core.VRUS, rawUint16ValuesWithOrder(binary.BigEndian, 512)),
		core.NewRawElement(core.NewTag(0x0028, 0x0103), core.VRSS, rawInt16ValuesWithOrder(binary.BigEndian, -2)),
		core.NewRawElement(core.NewTag(0x0028, 0x0100), core.VRUL, rawUint32ValuesWithOrder(binary.BigEndian, 305419896)),
		core.NewRawElement(core.NewTag(0x0018, 0x1160), core.VRFL, rawFloat32ValuesWithOrder(binary.BigEndian, 1.5)),
	}
	obj := object.FromElements(elements, std.Dictionary)
	obj.SetValueByteOrder(binary.BigEndian)

	got, err := MarshalCompact(obj)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]Element
	if err := json.Unmarshal(got, &decoded); err != nil {
		t.Fatal(err)
	}
	assertJSONValue(t, decoded["00080008"].Value, []any{"00100020"})
	assertJSONValue(t, decoded["00280010"].Value, []any{512})
	assertJSONValue(t, decoded["00280103"].Value, []any{-2})
	assertJSONValue(t, decoded["00280100"].Value, []any{305419896})
	assertJSONValue(t, decoded["00181160"].Value, []any{1.5})
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

func TestMarshalSequenceInheritsSpecificCharacterSet(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0005), core.VRCS, []byte("ISO_IR 192")),
		dicomtest.NewSequenceElement(core.NewTag(0x0008, 0x1111), core.DataSet{Elements: []core.Element{
			core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("René^José")),
		}}),
	}, std.Dictionary)

	encoded, err := Marshal(obj, Options{})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded map[string]map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	sequenceValues, ok := decoded["00081111"]["Value"].([]any)
	if !ok || len(sequenceValues) != 1 {
		t.Fatalf("sequence Value = %#v, want one item", decoded["00081111"]["Value"])
	}
	item := sequenceValues[0].(map[string]any)
	personName := item["00100010"].(map[string]any)
	values := personName["Value"].([]any)
	components := values[0].(map[string]any)
	if components["Alphabetic"] != "René^José" {
		t.Fatalf("nested PN = %#v, want René^José", components["Alphabetic"])
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

func TestUnmarshalAcceptsConformantDSAndISLexicalForms(t *testing.T) {
	const src = `{
		"00181063": {"vr":"DS","Value":[1e2,"+.5","1.","  -12.5E+3  "]},
		"00200013": {"vr":"IS","Value":[-2147483648,"+2147483647"," 7 "]}
	}`

	obj, err := Unmarshal([]byte(src), std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	dsElement, _ := obj.Get(core.NewTag(0x0018, 0x1063))
	ds, _ := dsElement.Value.(core.StringValue)
	if len(ds) != 4 || ds[0] != "1e2" || ds[1] != "+.5" || ds[2] != "1." || ds[3] != "  -12.5E+3  " {
		t.Fatalf("DS values = %#v, want original conformant tokens", ds)
	}
	isElement, _ := obj.Get(core.NewTag(0x0020, 0x0013))
	is, _ := isElement.Value.(core.StringValue)
	if len(is) != 3 || is[0] != "-2147483648" || is[1] != "+2147483647" || is[2] != " 7 " {
		t.Fatalf("IS values = %#v, want original conformant tokens", is)
	}
}

func TestUnmarshalRejectsNonConformantDSAndISTokens(t *testing.T) {
	for _, test := range []struct {
		name  string
		tag   string
		vr    string
		value string
	}{
		{name: "IS exponent number", tag: "00200013", vr: "IS", value: "1e2"},
		{name: "IS decimal string", tag: "00200013", vr: "IS", value: `"1.5"`},
		{name: "IS embedded space", tag: "00200013", vr: "IS", value: `"12 3"`},
		{name: "IS int32 overflow", tag: "00200013", vr: "IS", value: "2147483648"},
		{name: "IS over 12 bytes", tag: "00200013", vr: "IS", value: `"0000000000001"`},
		{name: "DS repeated decimal", tag: "00181063", vr: "DS", value: `"1.2.3"`},
		{name: "DS incomplete exponent", tag: "00181063", vr: "DS", value: `"1e"`},
		{name: "DS non-finite", tag: "00181063", vr: "DS", value: `"NaN"`},
		{name: "DS over 16 bytes", tag: "00181063", vr: "DS", value: `"12345678901234567"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			src := fmt.Sprintf(`{"%s":{"vr":"%s","Value":[%s]}}`, test.tag, test.vr, test.value)
			_, err := Unmarshal([]byte(src), std.Dictionary)
			if err == nil {
				t.Fatal("Unmarshal() error = nil, want invalid numeric string error")
			}
			wantPath := test.tag + "/Value[0]"
			if !strings.Contains(err.Error(), wantPath) {
				t.Fatalf("Unmarshal() error = %q, want path %q", err, wantPath)
			}
		})
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

func TestUnmarshalWithOptionsWritesBigEndianNumericAndATBytes(t *testing.T) {
	const src = `{
		"00080016": {"vr":"AT","Value":["00100020"]},
		"00181160": {"vr":"FL","Value":[1.5]},
		"00181164": {"vr":"FD","Value":[3.5]},
		"00280010": {"vr":"US","Value":[512]},
		"00280103": {"vr":"SS","Value":[-2]},
		"00280100": {"vr":"UL","Value":[305419896]},
		"00280011": {"vr":"SL","Value":[-2147483648]},
		"00280012": {"vr":"SV","Value":[-42]},
		"00280013": {"vr":"UV","Value":[42]}
	}`

	obj, err := UnmarshalWithOptions([]byte(src), std.Dictionary, UnmarshalOptions{
		ByteOrder: binary.BigEndian,
	})
	if err != nil {
		t.Fatal(err)
	}
	if obj.ValueByteOrder() != binary.BigEndian {
		t.Fatalf("ValueByteOrder() = %T, want binary.BigEndian", obj.ValueByteOrder())
	}

	tests := []struct {
		name string
		tag  core.Tag
		want []byte
	}{
		{
			name: "AT tag list",
			tag:  core.NewTag(0x0008, 0x0016),
			want: rawATValuesWithOrder(binary.BigEndian, core.NewTag(0x0010, 0x0020)),
		},
		{
			name: "FL float32",
			tag:  core.NewTag(0x0018, 0x1160),
			want: rawFloat32ValuesWithOrder(binary.BigEndian, 1.5),
		},
		{
			name: "FD float64",
			tag:  core.NewTag(0x0018, 0x1164),
			want: rawFloat64ValuesWithOrder(binary.BigEndian, 3.5),
		},
		{
			name: "US rows",
			tag:  core.NewTag(0x0028, 0x0010),
			want: rawUint16ValuesWithOrder(binary.BigEndian, 512),
		},
		{
			name: "SS signed",
			tag:  core.NewTag(0x0028, 0x0103),
			want: rawInt16ValuesWithOrder(binary.BigEndian, -2),
		},
		{
			name: "UL unsigned",
			tag:  core.NewTag(0x0028, 0x0100),
			want: rawUint32ValuesWithOrder(binary.BigEndian, 305419896),
		},
		{
			name: "SL signed",
			tag:  core.NewTag(0x0028, 0x0011),
			want: rawInt32ValuesWithOrder(binary.BigEndian, math.MinInt32),
		},
		{
			name: "SV signed 64",
			tag:  core.NewTag(0x0028, 0x0012),
			want: rawInt64ValuesWithOrder(binary.BigEndian, -42),
		},
		{
			name: "UV unsigned 64",
			tag:  core.NewTag(0x0028, 0x0013),
			want: rawUint64ValuesWithOrder(binary.BigEndian, 42),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := obj.GetRaw(tt.tag)
			if !ok {
				t.Fatalf("missing raw value for %s", tt.tag)
			}
			if string(got) != string(tt.want) {
				t.Fatalf("raw = % X, want % X", got, tt.want)
			}
		})
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

func TestUnmarshalValueErrorsIncludeVRAndJSONType(t *testing.T) {
	tests := []struct {
		name     string
		src      string
		contains []string
	}{
		{
			name:     "string_vr_receives_number",
			src:      `{"00100020":{"vr":"LO","Value":[123]}}`,
			contains: []string{"00100020/Value[0]", "VR LO", "received number", "expected string"},
		},
		{
			name:     "un_vr_receives_value_array",
			src:      `{"00111010":{"vr":"UN","Value":[1,2]}}`,
			contains: []string{"00111010/Value", "VR UN", "received array", "InlineBinary"},
		},
		{
			name:     "sq_vr_receives_object",
			src:      `{"00081111":{"vr":"SQ","Value":{}}}`,
			contains: []string{"00081111/Value", "VR SQ", "received object", "expected JSON array"},
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

func assertJSONValue(t *testing.T, got, want []any) {
	t.Helper()
	gotJSON, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(gotJSON) != string(wantJSON) {
		t.Fatalf("Value = %s, want %s", gotJSON, wantJSON)
	}
}

func rawATValues(values ...core.Tag) []byte {
	return rawATValuesWithOrder(binary.LittleEndian, values...)
}

func rawATValuesWithOrder(order binary.ByteOrder, values ...core.Tag) []byte {
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		var buf [4]byte
		order.PutUint16(buf[0:2], value.Group)
		order.PutUint16(buf[2:4], value.Element)
		data = append(data, buf[:]...)
	}
	return data
}

func rawFloat32Values(values ...float32) []byte {
	return rawFloat32ValuesWithOrder(binary.LittleEndian, values...)
}

func rawFloat32ValuesWithOrder(order binary.ByteOrder, values ...float32) []byte {
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		var buf [4]byte
		order.PutUint32(buf[:], math.Float32bits(value))
		data = append(data, buf[:]...)
	}
	return data
}

func rawFloat64Values(values ...float64) []byte {
	return rawFloat64ValuesWithOrder(binary.LittleEndian, values...)
}

func rawFloat64ValuesWithOrder(order binary.ByteOrder, values ...float64) []byte {
	data := make([]byte, 0, len(values)*8)
	for _, value := range values {
		var buf [8]byte
		order.PutUint64(buf[:], math.Float64bits(value))
		data = append(data, buf[:]...)
	}
	return data
}

func rawInt16Values(values ...int16) []byte {
	return rawInt16ValuesWithOrder(binary.LittleEndian, values...)
}

func rawInt16ValuesWithOrder(order binary.ByteOrder, values ...int16) []byte {
	data := make([]byte, 0, len(values)*2)
	for _, value := range values {
		var buf [2]byte
		order.PutUint16(buf[:], uint16(value))
		data = append(data, buf[:]...)
	}
	return data
}

func rawUint16Values(values ...uint16) []byte {
	return rawUint16ValuesWithOrder(binary.LittleEndian, values...)
}

func rawUint16ValuesWithOrder(order binary.ByteOrder, values ...uint16) []byte {
	data := make([]byte, 0, len(values)*2)
	for _, value := range values {
		var buf [2]byte
		order.PutUint16(buf[:], value)
		data = append(data, buf[:]...)
	}
	return data
}

func rawInt32Values(values ...int32) []byte {
	return rawInt32ValuesWithOrder(binary.LittleEndian, values...)
}

func rawInt32ValuesWithOrder(order binary.ByteOrder, values ...int32) []byte {
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		var buf [4]byte
		order.PutUint32(buf[:], uint32(value))
		data = append(data, buf[:]...)
	}
	return data
}

func rawUint32Values(values ...uint32) []byte {
	return rawUint32ValuesWithOrder(binary.LittleEndian, values...)
}

func rawUint32ValuesWithOrder(order binary.ByteOrder, values ...uint32) []byte {
	data := make([]byte, 0, len(values)*4)
	for _, value := range values {
		var buf [4]byte
		order.PutUint32(buf[:], value)
		data = append(data, buf[:]...)
	}
	return data
}

func rawInt64Values(values ...int64) []byte {
	return rawInt64ValuesWithOrder(binary.LittleEndian, values...)
}

func rawInt64ValuesWithOrder(order binary.ByteOrder, values ...int64) []byte {
	data := make([]byte, 0, len(values)*8)
	for _, value := range values {
		var buf [8]byte
		order.PutUint64(buf[:], uint64(value))
		data = append(data, buf[:]...)
	}
	return data
}

func rawUint64Values(values ...uint64) []byte {
	return rawUint64ValuesWithOrder(binary.LittleEndian, values...)
}

func rawUint64ValuesWithOrder(order binary.ByteOrder, values ...uint64) []byte {
	data := make([]byte, 0, len(values)*8)
	for _, value := range values {
		var buf [8]byte
		order.PutUint64(buf[:], value)
		data = append(data, buf[:]...)
	}
	return data
}
