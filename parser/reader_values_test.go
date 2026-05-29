package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"testing"
)

func TestNextReadsZeroLengthDefinedValueAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
	}{
		{name: "explicit little endian pn", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit little endian lo", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "explicit little endian us", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "explicit little endian ob", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
		{name: "implicit little endian pn", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "implicit little endian lo", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "implicit little endian us", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "implicit little endian ob", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
		{name: "explicit big endian pn", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit big endian lo", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO},
		{name: "explicit big endian us", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS},
		{name: "explicit big endian ob", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, 0, nil)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			if tok.Kind != TokenElement {
				t.Fatalf("token kind = %v, want %v", tok.Kind, TokenElement)
			}
			if tok.Element.Tag() != tt.tag {
				t.Fatalf("unexpected tag: got %s want %s", tok.Element.Tag(), tt.tag)
			}
			if tok.Element.VR() != tt.vr {
				t.Fatalf("unexpected VR: got %s want %s", tok.Element.VR(), tt.vr)
			}
			if tok.Header.Length != 0 {
				t.Fatalf("header length = %s, want 0", tok.Header.Length)
			}
			if tok.Element.EncodedLength() != 0 {
				t.Fatalf("element encoded length = %s, want 0", tok.Element.EncodedLength())
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if len(raw) != 0 {
				t.Fatalf("raw length = %d, want 0", len(raw))
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr)
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}
func TestNextPreservesRawMultiValueStringsAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
		value  []byte
		want   []string
	}{
		{name: "explicit little endian pn", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "explicit little endian lo", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
		{name: "implicit little endian pn", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "implicit little endian lo", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
		{name: "explicit big endian pn", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("ALPHA^ONE\\BETA^TWO  "), want: []string{"ALPHA^ONE", "BETA^TWO"}},
		{name: "explicit big endian lo", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, value: []byte("LEFT\\RIGHT  "), want: []string{"LEFT", "RIGHT"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(tt.value)), tt.value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if !bytes.Equal(raw, tt.value) {
				t.Fatalf("raw bytes = %q, want %q", raw, tt.value)
			}
			gotStrings := tok.Element.StringValues()
			if len(gotStrings) != len(tt.want) {
				t.Fatalf("string value count = %d, want %d", len(gotStrings), len(tt.want))
			}
			for i := range tt.want {
				if gotStrings[i] != tt.want[i] {
					t.Fatalf("string value %d = %q, want %q", i, gotStrings[i], tt.want[i])
				}
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr) + int64(len(tt.value))
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}
func TestNextRejectsOddLengthByDefaultAcrossTransferSyntaxes(t *testing.T) {
	testRejectOddLengthAcrossTransferSyntaxes(t, "default policy", ReaderOptions{Dictionary: std.Dictionary})
}
func TestNextRejectsOddLengthWhenExplicitlyConfiguredAcrossTransferSyntaxes(t *testing.T) {
	testRejectOddLengthAcrossTransferSyntaxes(t, "explicit reject policy", ReaderOptions{
		Dictionary:      std.Dictionary,
		OddLengthPolicy: RejectOddLength,
	})
}
func TestNextAcceptsOddLengthWhenConfiguredAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		tag    core.Tag
		vr     core.VR
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN},
		{name: "explicit little endian long header", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := []byte("ODD")
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(value)), value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{
				Dictionary:      std.Dictionary,
				OddLengthPolicy: AcceptOddLength,
			})

			tok, err := reader.Next()
			if err != nil {
				t.Fatal(err)
			}
			raw, ok := tok.Element.RawBytes()
			if !ok {
				t.Fatalf("expected raw value")
			}
			if !bytes.Equal(raw, value) {
				t.Fatalf("raw bytes = %q, want %q", raw, value)
			}
			if tok.Header.Length != 3 {
				t.Fatalf("header length = %s, want 3", tok.Header.Length)
			}
			wantPos := definedHeaderLength(tt.syntax, tt.vr) + int64(len(value))
			if got := reader.Position(); got != wantPos {
				t.Fatalf("reader position = %d, want %d", got, wantPos)
			}
		})
	}
}
func TestNextReadsDefinedValuesAcrossTransferSyntaxes(t *testing.T) {
	tests := []struct {
		name    string
		syntax  transfer.Syntax
		elems   []integrationElement
		wantEnd int64
	}{
		{
			name:   "explicit little endian",
			syntax: transfer.ExplicitVRLittleEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x40, 0x00}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
		{
			name:   "implicit little endian",
			syntax: transfer.ImplicitVRLittleEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x40, 0x00}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
		{
			name:   "explicit big endian",
			syntax: transfer.ExplicitVRBigEndian,
			elems: []integrationElement{
				{tag: core.NewTag(0x0010, 0x0010), vr: core.VRPN, value: []byte("DOE^JOHN"), wantString: "DOE^JOHN"},
				{tag: core.NewTag(0x0028, 0x0010), vr: core.VRUS, value: []byte{0x00, 0x40}},
				{tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, value: []byte{0x00, 0x01}},
			},
		},
	}

	for i := range tests {
		var total int64
		for _, elem := range tests[i].elems {
			total += definedHeaderLength(tests[i].syntax, elem.vr) + int64(len(elem.value))
		}
		tests[i].wantEnd = total
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stream []byte
			var positions []int64
			var offset int64
			for _, elem := range tt.elems {
				stream = append(stream, definedElementBytes(tt.syntax, elem.tag, elem.vr, uint32(len(elem.value)), elem.value)...)
				offset += definedHeaderLength(tt.syntax, elem.vr) + int64(len(elem.value))
				positions = append(positions, offset)
			}

			reader := NewReader(bytes.NewReader(stream), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})
			for i, want := range tt.elems {
				tok, err := reader.Next()
				if err != nil {
					t.Fatalf("reading element %d: %v", i, err)
				}
				if tok.Kind != TokenElement {
					t.Fatalf("element %d token kind = %v, want %v", i, tok.Kind, TokenElement)
				}
				if tok.Element.Tag() != want.tag {
					t.Fatalf("element %d tag = %s, want %s", i, tok.Element.Tag(), want.tag)
				}
				if tok.Element.VR() != want.vr {
					t.Fatalf("element %d VR = %s, want %s", i, tok.Element.VR(), want.vr)
				}
				raw, ok := tok.Element.RawBytes()
				if !ok {
					t.Fatalf("element %d expected raw value", i)
				}
				if !bytes.Equal(raw, want.value) {
					t.Fatalf("element %d raw bytes = %v, want %v", i, raw, want.value)
				}
				if want.wantString != "" && tok.Element.StringValue() != want.wantString {
					t.Fatalf("element %d string value = %q, want %q", i, tok.Element.StringValue(), want.wantString)
				}
				if got := reader.Position(); got != positions[i] {
					t.Fatalf("reader position after element %d = %d, want %d", i, got, positions[i])
				}
			}

			if got := reader.Position(); got != tt.wantEnd {
				t.Fatalf("final reader position = %d, want %d", got, tt.wantEnd)
			}
		})
	}
}
func TestReadHeaderExplicitVRLongFormsDoNotConsumeValue(t *testing.T) {
	tests := []struct {
		name         string
		syntax       transfer.Syntax
		tag          core.Tag
		vr           core.VR
		length       uint32
		value        []byte
		headerLength int64
	}{
		{name: "ob little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0002, 0x0001), vr: core.VROB, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "od little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0018, 0x9808), vr: core.VROD, length: 8, value: []byte{1, 2, 3, 4, 5, 6, 7, 8}, headerLength: 12},
		{name: "of little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0018, 0x9820), vr: core.VROF, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ol little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0066, 0x0123), vr: core.VROL, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ow little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0028, 0x0100), vr: core.VROW, length: 2, value: []byte{1, 0}, headerLength: 12},
		{name: "sq little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x1111), vr: core.VRSQ, length: 8, value: []byte("SEQUENCE"), headerLength: 12},
		{name: "un little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x7777, 0x0010), vr: core.VRUN, length: 4, value: []byte("DATA"), headerLength: 12},
		{name: "uc little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x4000), vr: core.VRUC, length: 6, value: []byte("AUTHOR"), headerLength: 12},
		{name: "ur little endian", syntax: transfer.ExplicitVRLittleEndian, tag: core.NewTag(0x0008, 0x0120), vr: core.VRUR, length: 18, value: []byte("https://dicom.dev/"), headerLength: 12},
		{name: "of big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0018, 0x9820), vr: core.VROF, length: 4, value: []byte{1, 2, 3, 4}, headerLength: 12},
		{name: "ut big endian", syntax: transfer.ExplicitVRBigEndian, tag: core.NewTag(0x0040, 0xA160), vr: core.VRUT, length: 6, value: []byte("REPORT"), headerLength: 12},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := append(explicitLongHeaderBytes(tt.syntax.ByteOrder, tt.tag, tt.vr, [2]byte{}, tt.length), tt.value...)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			header, err := reader.readHeader()
			if err != nil {
				t.Fatal(err)
			}
			if header.Tag != tt.tag {
				t.Fatalf("header tag = %s, want %s", header.Tag, tt.tag)
			}
			if header.VR != tt.vr {
				t.Fatalf("header VR = %s, want %s", header.VR, tt.vr)
			}
			if header.Length != core.Length(tt.length) {
				t.Fatalf("header length = %s, want %d", header.Length, tt.length)
			}
			if got := reader.Position(); got != tt.headerLength {
				t.Fatalf("reader position after header = %d, want %d", got, tt.headerLength)
			}
		})
	}
}
func TestReadHeaderStrictReservedRejectsNonZeroBytes(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0040, 0xA160), core.VRUT, [2]byte{0x12, 0x34}, 6)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:          std.Dictionary,
		StrictReservedBytes: true,
	})

	_, err := reader.readHeader()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrNonZeroReservedBytes) {
		t.Fatalf("expected reserved-byte error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpValidateReserved {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}
func TestReadHeaderLenientReservedAllowsNonZeroBytes(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0040, 0xA160), core.VRUT, [2]byte{0x12, 0x34}, 6)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRUT {
		t.Fatalf("header VR = %s, want %s", header.VR, core.VRUT)
	}
	if header.Length != 6 {
		t.Fatalf("header length = %s, want 6", header.Length)
	}
}
func TestReadHeaderImplicitVRSkipsDictionaryForSequenceControlTags(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0010, 0x0010), core.VRPN)}
	reader := NewReader(
		bytes.NewReader(dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.Tag != core.TagItem {
		t.Fatalf("unexpected tag: %s", header.Tag)
	}
	if header.VR != core.VRUN {
		t.Fatalf("sequence control VR = %s, want %s", header.VR, core.VRUN)
	}
	if dict.byTagCalls != 0 {
		t.Fatalf("dictionary was consulted for sequence control tag: %d calls", dict.byTagCalls)
	}
}
func TestReadHeaderImplicitVRSpecialCasesPixelDataAndOverlayData(t *testing.T) {
	tests := []struct {
		name string
		tag  core.Tag
	}{
		{name: "pixel data", tag: core.NewTag(0x7FE0, 0x0010)},
		{name: "overlay data", tag: core.NewTag(0x6000, 0x3000)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, tt.tag, 8)),
				transfer.ImplicitVRLittleEndian,
				ReaderOptions{Dictionary: &countingDictionary{}},
			)

			header, err := reader.readHeader()
			if err != nil {
				t.Fatal(err)
			}
			if header.VR != core.VROW {
				t.Fatalf("implicit VR for %s = %s, want %s", tt.tag, header.VR, core.VROW)
			}
		})
	}
}
func TestReadHeaderImplicitVRDoesNotTreatOddOverlayGroupAsOverlayData(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x6001, 0x3000), core.VRUN)}
	reader := NewReader(
		bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x6001, 0x3000), 8)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRUN {
		t.Fatalf("implicit VR for odd overlay-like tag = %s, want %s", header.VR, core.VRUN)
	}
	if dict.byTagCalls != 1 {
		t.Fatalf("dictionary call count = %d, want 1", dict.byTagCalls)
	}
}
func TestReadHeaderImplicitVRUsesDictionaryForNormalElements(t *testing.T) {
	dict := &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0010, 0x0010), core.VRPN)}
	reader := NewReader(
		bytes.NewReader(implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0010, 0x0010), 4)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: dict},
	)

	header, err := reader.readHeader()
	if err != nil {
		t.Fatal(err)
	}
	if header.VR != core.VRPN {
		t.Fatalf("header VR = %s, want %s", header.VR, core.VRPN)
	}
	if dict.byTagCalls != 1 {
		t.Fatalf("dictionary call count = %d, want 1", dict.byTagCalls)
	}
}

func TestReadDataSetDefersNestedWaveformDataAndRecordsAllLocations(t *testing.T) {
	waveformSequenceTag := core.NewTag(0x5400, 0x0100)
	waveformDataTag := core.NewTag(0x5400, 0x1010)
	groupLabelTag := core.NewTag(0x003A, 0x0020)
	groupData := [][]byte{
		{0x01, 0x00, 0x02, 0x00},
		{0x10, 0x00, 0x20, 0x00, 0x30, 0x00},
	}
	data := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewSequenceElement(
			waveformSequenceTag,
			core.DataSet{Elements: []core.Element{
				dicomtest.NewStringElement(groupLabelTag, core.VRSH, "GROUP 1"),
				core.NewRawElement(waveformDataTag, core.VROW, groupData[0]),
			}},
			core.DataSet{Elements: []core.Element{
				dicomtest.NewStringElement(groupLabelTag, core.VRSH, "GROUP 2"),
				core.NewRawElement(waveformDataTag, core.VROW, groupData[1]),
			}},
		),
	)

	reader := NewReader(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:        std.Dictionary,
		DeferWaveformData: true,
	})
	dataset, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if len(dataset.Elements) != 1 {
		t.Fatalf("dataset element count = %d, want 1", len(dataset.Elements))
	}
	sequence, ok := dataset.Elements[0].Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != len(groupData) {
		t.Fatalf("WaveformSequence value = %#v, want %d items", dataset.Elements[0].Value, len(groupData))
	}
	for index, item := range sequence.Items {
		if len(item.Elements) != 2 {
			t.Fatalf("item %d element count = %d, want 2", index, len(item.Elements))
		}
		if item.Elements[0].Value == nil {
			t.Fatalf("item %d metadata was unexpectedly deferred", index)
		}
		if item.Elements[1].Tag() != waveformDataTag || item.Elements[1].Value != nil {
			t.Fatalf("item %d WaveformData = %#v, want deferred nil", index, item.Elements[1])
		}
	}

	if _, ok := reader.ValueLocation(waveformDataTag); ok {
		t.Fatal("ValueLocation returned a tag-only location for duplicate WaveformData")
	}
	locations := reader.ValueLocations(waveformDataTag)
	if len(locations) != len(groupData) {
		t.Fatalf("ValueLocations count = %d, want %d", len(locations), len(groupData))
	}
	for index, location := range locations {
		if location.Tag != waveformDataTag {
			t.Fatalf("location %d tag = %s, want %s", index, location.Tag, waveformDataTag)
		}
		if location.Length != int64(len(groupData[index])) {
			t.Fatalf("location %d length = %d, want %d", index, location.Length, len(groupData[index]))
		}
		if !location.ItemOffsetSet || location.ItemOffset != sequence.Items[index].ItemOffset {
			t.Fatalf(
				"location %d item offset = %d/%v, want %d/true",
				index,
				location.ItemOffset,
				location.ItemOffsetSet,
				sequence.Items[index].ItemOffset,
			)
		}
		if index > 0 && location.ValueOffset <= locations[index-1].ValueOffset {
			t.Fatalf("locations are not in source order: %#v", locations)
		}
		var got bytes.Buffer
		if _, err := reader.CopyElementValueAt(location, &got); err != nil {
			t.Fatalf("CopyElementValueAt(%d): %v", index, err)
		}
		if !bytes.Equal(got.Bytes(), groupData[index]) {
			t.Fatalf("CopyElementValueAt(%d) = % X, want % X", index, got.Bytes(), groupData[index])
		}
	}

	locations[0].Length = 0
	fresh := reader.ValueLocations(waveformDataTag)
	if fresh[0].Length != int64(len(groupData[0])) {
		t.Fatalf("ValueLocations returned aliased storage: first length = %d", fresh[0].Length)
	}
}
