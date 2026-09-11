package dicomxml

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"os"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestNativeModelRoundTrip(t *testing.T) {
	tagEmpty := core.NewTag(0x0008, 0x0050)
	tagValues := core.NewTag(0x0008, 0x0008)
	tagPN := core.NewTag(0x0010, 0x0010)
	tagSeq := core.NewTag(0x0008, 0x1115)
	tagInline := core.NewTag(0x0028, 0x2000)
	tagBulk := core.TagPixelData
	tagGroupLength := core.NewTag(0x0008, 0x0000)
	obj := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0005), VR: core.VRCS}, Value: core.StringValue{"ISO_IR 192"}},
		{Header: core.ElementHeader{Tag: tagEmpty, VR: core.VRSH}, Value: core.StringValue(nil)},
		{Header: core.ElementHeader{Tag: tagValues, VR: core.VRCS}, Value: core.StringValue{"PRIMARY", "", "OTHER"}},
		{Header: core.ElementHeader{Tag: tagPN, VR: core.VRPN}, Value: core.StringValue{"Doe^Jane^^Dr^PhD=山田^花子=ヤマダ^ハナコ"}},
		{Header: core.ElementHeader{Tag: tagSeq, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000E), VR: core.VRUI}, Value: core.StringValue{"1.2.3.4"}},
		}}}}},
		core.NewRawElement(tagInline, core.VROB, []byte{0, 1, 2, 3, 254, 255}),
		{Header: core.ElementHeader{Tag: tagBulk, VR: core.VROB}, Value: core.BulkDataValue{URI: "https://example.invalid/bulk/1"}},
		core.NewRawElement(tagGroupLength, core.VRUL, []byte{0, 0, 0, 0}),
	}, std.Dictionary)

	xmlData, err := Marshal(obj, DefaultOptions())
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	text := string(xmlData)
	for _, want := range []string{
		`xmlns="` + Namespace + `"`, `xml:space="preserve"`, `keyword="PatientName"`,
		`<PersonName number="1">`, `<FamilyName>Doe</FamilyName>`, `<Ideographic>`,
		`<Value number="2"></Value>`, `<InlineBinary>AAECA/7/</InlineBinary>`,
		`<BulkData uri="https://example.invalid/bulk/1"></BulkData>`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("XML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, `tag="00080000"`) {
		t.Fatal("group-length element was emitted")
	}

	got, err := Unmarshal(xmlData, std.Dictionary)
	if err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if values, err := got.LookupStrings(tagValues); err != nil || strings.Join(values, "|") != "PRIMARY||OTHER" {
		t.Fatalf("values = %q, %v", values, err)
	}
	if value, err := got.LookupString(tagPN); err != nil || value != "Doe^Jane^^Dr^PhD=山田^花子=ヤマダ^ハナコ" {
		t.Fatalf("PN = %q, %v", value, err)
	}
	if values, err := got.LookupStrings(tagEmpty); err != nil || len(values) != 0 {
		t.Fatalf("empty = %q, %v", values, err)
	}
	items, ok := got.GetSequence(tagSeq)
	if !ok || len(items) != 1 {
		t.Fatalf("sequence items = %d, %v", len(items), ok)
	}
	if value, _ := items[0].LookupString(core.NewTag(0x0020, 0x000E)); value != "1.2.3.4" {
		t.Fatalf("nested UID = %q", value)
	}
	if elem, _ := got.Get(tagInline); !bytes.Equal(mustRaw(t, elem), []byte{0, 1, 2, 3, 254, 255}) {
		t.Fatalf("inline = %v", mustRaw(t, elem))
	}
	if elem, _ := got.Get(tagBulk); elem.Value.(core.BulkDataValue).URI != "https://example.invalid/bulk/1" {
		t.Fatalf("bulk = %#v", elem.Value)
	}
	if charset, _ := got.LookupString(core.NewTag(0x0008, 0x0005)); charset != "ISO_IR 192" {
		t.Fatalf("SpecificCharacterSet = %q", charset)
	}
}

func TestOfficialNativeModelFixture(t *testing.T) {
	data, err := os.ReadFile("testdata/ps3.18-native-model.xml")
	if err != nil {
		t.Fatal(err)
	}
	obj, err := Unmarshal(data, std.Dictionary)
	if err != nil {
		t.Fatalf("Unmarshal fixture: %v", err)
	}
	value, err := obj.LookupString(core.NewTag(0x0020, 0x000D))
	if err != nil || value != "1.2.840.10008.1.2.3.4" {
		t.Fatalf("UID = %q, %v", value, err)
	}
	encoded, err := Marshal(obj, DefaultOptions())
	if err != nil {
		t.Fatalf("Marshal fixture: %v", err)
	}
	if !bytes.Contains(encoded, []byte(`xmlns="`+Namespace+`"`)) || !bytes.Contains(encoded, []byte(`xml:space="preserve"`)) {
		t.Fatalf("missing required root attributes: %s", encoded)
	}
}

func TestDecodeRejectsInvalidNativeModel(t *testing.T) {
	wrap := func(body string) string {
		return `<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve">` + body + `</NativeDicomModel>`
	}
	tests := []struct{ name, input string }{
		{"missing namespace", `<NativeDicomModel xml:space="preserve"></NativeDicomModel>`},
		{"missing xml space", `<NativeDicomModel xmlns="` + Namespace + `"></NativeDicomModel>`},
		{"lowercase tag", wrap(`<DicomAttribute tag="7fe00010" vr="OB"/>`)},
		{"group length", wrap(`<DicomAttribute tag="00080000" vr="UL"/>`)},
		{"number gap", wrap(`<DicomAttribute tag="00080008" vr="CS"><Value number="2">A</Value></DicomAttribute>`)},
		{"mixed representations", wrap(`<DicomAttribute tag="00080008" vr="CS"><Value number="1">A</Value><InlineBinary>AA==</InlineBinary></DicomAttribute>`)},
		{"invalid inline VR", wrap(`<DicomAttribute tag="00080008" vr="CS"><InlineBinary>AA==</InlineBinary></DicomAttribute>`)},
		{"invalid base64", wrap(`<DicomAttribute tag="7FE00010" vr="OB"><InlineBinary>%%%</InlineBinary></DicomAttribute>`)},
		{"bulk alternatives", wrap(`<DicomAttribute tag="7FE00010" vr="OB"><BulkData uri="/bulk" uuid="550e8400-e29b-41d4-a716-446655440000"/></DicomAttribute>`)},
		{"foreign child", wrap(`<DicomAttribute tag="00080008" vr="CS"><Value xmlns="urn:evil" number="1">A</Value></DicomAttribute>`)},
		{"doctype", `<!DOCTYPE NativeDicomModel [<!ENTITY xxe SYSTEM "file:///etc/passwd">]>` + wrap(`<DicomAttribute tag="00080008" vr="CS"><Value number="1">&xxe;</Value></DicomAttribute>`)},
		{"processing instruction", wrap(`<?evil run?><DicomAttribute tag="00080008" vr="CS"/>`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Unmarshal([]byte(test.input), std.Dictionary)
			if !errors.Is(err, ErrInvalidXML) {
				t.Fatalf("error = %v, want ErrInvalidXML", err)
			}
		})
	}
}

func TestMissingKeywordPolicyIsExplicit(t *testing.T) {
	xmlData := []byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="00080008" vr="CS"><Value number="1">PRIMARY</Value></DicomAttribute></NativeDicomModel>`)
	if _, err := Unmarshal(xmlData, std.Dictionary); !errors.Is(err, ErrInvalidXML) {
		t.Fatalf("strict missing-keyword error = %v", err)
	}
	obj, err := UnmarshalWithOptions(xmlData, std.Dictionary, UnmarshalOptions{AllowMissingKeyword: true})
	if err != nil {
		t.Fatalf("tolerant missing-keyword decode: %v", err)
	}
	if value, _ := obj.LookupString(core.NewTag(0x0008, 0x0008)); value != "PRIMARY" {
		t.Fatalf("ImageType = %q", value)
	}
}

func TestBulkDataPolicies(t *testing.T) {
	xmlData := []byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><BulkData uuid="550e8400-e29b-41d4-a716-446655440000"/></DicomAttribute></NativeDicomModel>`)
	preserved, err := Unmarshal(xmlData, std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	elem, _ := preserved.Get(core.TagPixelData)
	if got := elem.Value.(core.BulkDataValue).URI; got != "urn:uuid:550e8400-e29b-41d4-a716-446655440000" {
		t.Fatalf("preserved URI = %q", got)
	}
	_, err = UnmarshalWithOptions(xmlData, std.Dictionary, UnmarshalOptions{BulkDataPolicy: RejectBulkData})
	if !errors.Is(err, ErrBulkDataRejected) {
		t.Fatalf("reject error = %v", err)
	}
	closed := false
	resolved, err := UnmarshalWithOptions(xmlData, std.Dictionary, UnmarshalOptions{
		BulkDataPolicy: ResolveBulkData,
		BulkDataResolver: func(_ context.Context, ref BulkDataReference) (io.ReadCloser, error) {
			if ref.UUID == "" || ref.Tag != core.TagPixelData {
				t.Fatalf("reference = %#v", ref)
			}
			return &closeTracker{Reader: strings.NewReader("pixels"), closed: &closed}, nil
		},
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !closed {
		t.Fatal("resolver reader was not closed")
	}
	elem, _ = resolved.Get(core.TagPixelData)
	if got := mustRaw(t, elem); string(got) != "pixels" {
		t.Fatalf("resolved = %q", got)
	}
}

func TestResolveBulkDataMaterializesNumericVRs(t *testing.T) {
	xmlData := []byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve">` +
		`<DicomAttribute tag="00280010" vr="US" keyword="Rows"><BulkData uuid="550e8400-e29b-41d4-a716-446655440001"/></DicomAttribute>` +
		`<DicomAttribute tag="00081163" vr="FD" keyword="TimeRange"><BulkData uuid="550e8400-e29b-41d4-a716-446655440002"/></DicomAttribute>` +
		`</NativeDicomModel>`)
	var rows [2]byte
	binary.LittleEndian.PutUint16(rows[:], 512)
	var timeRange [8]byte
	binary.LittleEndian.PutUint64(timeRange[:], math.Float64bits(12.5))
	obj, err := UnmarshalWithOptions(xmlData, std.Dictionary, UnmarshalOptions{
		ByteOrder: binary.LittleEndian, BulkDataPolicy: ResolveBulkData,
		BulkDataResolver: func(_ context.Context, ref BulkDataReference) (io.ReadCloser, error) {
			switch ref.UUID {
			case "550e8400-e29b-41d4-a716-446655440001":
				return io.NopCloser(bytes.NewReader(rows[:])), nil
			case "550e8400-e29b-41d4-a716-446655440002":
				return io.NopCloser(bytes.NewReader(timeRange[:])), nil
			default:
				t.Fatalf("unexpected reference %#v", ref)
				return nil, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("resolve numeric bulk data: %v", err)
	}
	rowsElement, _ := obj.Get(core.NewTag(0x0028, 0x0010))
	if got := binary.LittleEndian.Uint16(mustRaw(t, rowsElement)); got != 512 {
		t.Fatalf("Rows = %d", got)
	}
	fdElement, _ := obj.Get(core.NewTag(0x0008, 0x1163))
	if got := math.Float64frombits(binary.LittleEndian.Uint64(mustRaw(t, fdElement))); got != 12.5 {
		t.Fatalf("TimeRange = %g", got)
	}
}

type closeTracker struct {
	io.Reader
	closed *bool
}

func (r *closeTracker) Close() error { *r.closed = true; return nil }

func TestDecodeLimitsAndCancellation(t *testing.T) {
	valueXML := `<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="00080008" vr="CS" keyword="ImageType"><Value number="1">abcd</Value></DicomAttribute></NativeDicomModel>`
	inlineXML := `<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><InlineBinary>` + base64.StdEncoding.EncodeToString([]byte("abcd")) + `</InlineBinary></DicomAttribute></NativeDicomModel>`
	twoElementsXML := `<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="00080008" vr="CS" keyword="ImageType"/><DicomAttribute tag="00080050" vr="SH" keyword="AccessionNumber"/></NativeDicomModel>`
	sequenceXML := `<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="00081115" vr="SQ" keyword="ReferencedSeriesSequence"><Item number="1"><DicomAttribute tag="00081115" vr="SQ" keyword="ReferencedSeriesSequence"><Item number="1"/></DicomAttribute></Item></DicomAttribute></NativeDicomModel>`
	tests := []struct {
		name, input string
		limits      Limits
		want        error
	}{
		{"xml bytes", valueXML, Limits{MaxXMLBytes: int64(len(valueXML) - 1)}, ErrMaxXMLBytesExceeded},
		{"value bytes", valueXML, Limits{MaxValueBytes: 3}, ErrMaxValueBytesExceeded},
		{"inline bytes", inlineXML, Limits{MaxInlineBinaryBytes: 3}, ErrMaxInlineBytesExceeded},
		{"elements", valueXML, Limits{MaxElements: 1}, nil},
		{"too many elements", twoElementsXML, Limits{MaxElements: 1}, ErrMaxElementsExceeded},
		{"too many items", sequenceXML, Limits{MaxSequenceItems: 1}, ErrMaxItemsExceeded},
		{"too deep", sequenceXML, Limits{MaxSequenceDepth: 1}, ErrMaxDepthExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := UnmarshalWithOptions([]byte(test.input), std.Dictionary, UnmarshalOptions{Limits: test.limits})
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
			if test.want == nil && err != nil {
				t.Fatal(err)
			}
		})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := UnmarshalContext(ctx, []byte(valueXML), std.Dictionary, DefaultUnmarshalOptions())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error = %v", err)
	}
	if _, err := UnmarshalWithOptions([]byte(inlineXML), std.Dictionary, UnmarshalOptions{Limits: Limits{MaxValueBytes: 1, MaxInlineBinaryBytes: 4}}); err != nil {
		t.Fatalf("InlineBinary was incorrectly constrained by MaxValueBytes: %v", err)
	}
	_, err = UnmarshalWithOptions([]byte(valueXML), std.Dictionary, UnmarshalOptions{Limits: Limits{MaxElements: -1}})
	if !errors.Is(err, ErrInvalidLimits) {
		t.Fatalf("negative limit error = %v", err)
	}
}

func TestEncodeHonorsXMLLimit(t *testing.T) {
	obj := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0008), VR: core.VRCS}, Value: core.StringValue{"PRIMARY"}}}, std.Dictionary)
	_, err := Marshal(obj, Options{Limits: Limits{MaxXMLBytes: 32}})
	if !errors.Is(err, ErrMaxXMLBytesExceeded) {
		t.Fatalf("error = %v", err)
	}
}

func TestEncapsulatedPixelDataRequiresTransferSyntax(t *testing.T) {
	valueField := []byte{0xfe, 0xff, 0x00, 0xe0, 0, 0, 0, 0, 0xfe, 0xff, 0xdd, 0xe0, 0, 0, 0, 0}
	xmlData := []byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="7FE00010" vr="OB" keyword="PixelData"><InlineBinary>` + base64.StdEncoding.EncodeToString(valueField) + `</InlineBinary></DicomAttribute></NativeDicomModel>`)
	_, err := Unmarshal(xmlData, std.Dictionary)
	if !errors.Is(err, ErrTransferSyntaxRequired) {
		t.Fatalf("error = %v", err)
	}
	obj, err := UnmarshalWithOptions(xmlData, std.Dictionary, UnmarshalOptions{TransferSyntax: transfer.JPEGBaseline, ByteOrder: binary.LittleEndian})
	if err != nil {
		t.Fatalf("with transfer syntax: %v", err)
	}
	elem, _ := obj.Get(core.TagPixelData)
	if _, ok := elem.Value.(core.FragmentSequence); !ok {
		t.Fatalf("value type = %T", elem.Value)
	}
}

func TestPrivateAttributeRoundTrip(t *testing.T) {
	creatorTag := core.NewTag(0x0011, 0x0010)
	privateTag := core.NewTag(0x0011, 0x1001)
	obj := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: creatorTag, VR: core.VRLO}, Value: core.StringValue{"ACME"}},
		{Header: core.ElementHeader{Tag: privateTag, VR: core.VRLO}, Value: core.StringValue{"private"}},
	}, std.Dictionary)
	data, err := Marshal(obj, DefaultOptions())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`tag="00110001" privateCreator="ACME"`)) && !bytes.Contains(data, []byte(`privateCreator="ACME" tag="00110001"`)) {
		t.Fatalf("normalized private attribute missing: %s", data)
	}
	got, err := Unmarshal(data, std.Dictionary)
	if err != nil {
		t.Fatal(err)
	}
	value, err := got.LookupString(privateTag)
	if err != nil || value != "private" {
		t.Fatalf("private value = %q, %v", value, err)
	}
	// XML Infoset element order is not significant. A normalized private data
	// element can precede its explicit creator element.
	reordered := []byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="00110001" vr="LO" privateCreator="ACME"><Value number="1">private</Value></DicomAttribute><DicomAttribute tag="00110010" vr="LO"><Value number="1">ACME</Value></DicomAttribute></NativeDicomModel>`)
	got, err = Unmarshal(reordered, std.Dictionary)
	if err != nil {
		t.Fatalf("reordered private data: %v", err)
	}
	if value, _ := got.LookupString(privateTag); value != "private" {
		t.Fatalf("reordered private value = %q", value)
	}
}

func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"></NativeDicomModel>`))
	f.Add([]byte(`<!DOCTYPE x [<!ENTITY x SYSTEM "file:///nope">]><x>&x;</x>`))
	f.Add([]byte(`<NativeDicomModel xmlns="` + Namespace + `" xml:space="preserve"><DicomAttribute tag="7FE00010" vr="OB"><InlineBinary>AQID</InlineBinary></DicomAttribute></NativeDicomModel>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = UnmarshalWithOptions(data, std.Dictionary, UnmarshalOptions{Limits: Limits{
			MaxXMLBytes: 1 << 20, MaxValueBytes: 1 << 16, MaxInlineBinaryBytes: 1 << 16,
			MaxTotalBinaryBytes: 1 << 16, MaxSequenceDepth: 16, MaxElements: 10_000, MaxSequenceItems: 1_000,
		}})
	})
}

func mustRaw(t *testing.T, elem core.Element) []byte {
	t.Helper()
	raw, ok := elem.RawBytes()
	if !ok {
		t.Fatalf("value type = %T, want RawValue", elem.Value)
	}
	return raw
}
