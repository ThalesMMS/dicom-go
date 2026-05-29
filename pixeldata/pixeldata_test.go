package pixeldata

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"
	"unsafe"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestMetadataValidationRuleAcceptsLeftJustifiedStoredBits(t *testing.T) {
	dataset := core.DataSet{Elements: append(
		pixelMetadataElements(1, 1, 1, 16, 12, 15, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2")),
		dicomtest.NewOBElement(core.TagPixelData, []byte{0, 0}),
	)}
	findings := MetadataValidationRule().ValidateDataSet(context.Background(), validation.DataSetContext{
		DataSet: dataset, ByteOrder: binary.LittleEndian, TransferSyntax: transfer.ExplicitVRLittleEndian,
	})
	if len(findings) != 0 {
		t.Fatalf("left-justified HighBit findings = %#v", findings)
	}
	dataset.Elements[5] = dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, 16)
	findings = MetadataValidationRule().ValidateDataSet(context.Background(), validation.DataSetContext{
		DataSet: dataset, ByteOrder: binary.LittleEndian, TransferSyntax: transfer.ExplicitVRLittleEndian,
	})
	if len(findings) != 1 || findings[0].Tag != tagHighBit {
		t.Fatalf("out-of-range HighBit findings = %#v", findings)
	}
}

func TestExtractReturnsEncapsulatedFragmentSequence(t *testing.T) {
	offsetTable := []byte{0x00, 0x00, 0x00, 0x00}
	fragment := []byte{0x01, 0x02, 0x03, 0x04}
	wantOffsetTable := append([]byte(nil), offsetTable...)
	wantFragment := append([]byte(nil), fragment...)
	obj := object.FromElements([]core.Element{
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, offsetTable, fragment),
	}, nil)

	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !pixel.Encapsulated {
		t.Fatalf("Encapsulated = false, want true")
	}
	if !bytes.Equal(pixel.Sequence.OffsetTable, wantOffsetTable) {
		t.Fatalf("offset table = %v, want %v", pixel.Sequence.OffsetTable, wantOffsetTable)
	}
	if len(pixel.Sequence.Fragments) != 1 {
		t.Fatalf("fragment count = %d, want 1", len(pixel.Sequence.Fragments))
	}
	if !bytes.Equal(pixel.Sequence.Fragments[0], wantFragment) {
		t.Fatalf("fragment = %v, want %v", pixel.Sequence.Fragments[0], wantFragment)
	}

	pixel.Sequence.OffsetTable[0] = 0xFF
	pixel.Sequence.Fragments[0][0] = 0xEE

	elem, _ := obj.Get(core.TagPixelData)
	original := elem.Value.(core.FragmentSequence)
	if !bytes.Equal(original.OffsetTable, wantOffsetTable) {
		t.Fatalf("original offset table mutated: %v", original.OffsetTable)
	}
	if !bytes.Equal(original.Fragments[0], wantFragment) {
		t.Fatalf("original fragment mutated: %v", original.Fragments[0])
	}
}

func TestExtractReturnsClonedRawPixelData(t *testing.T) {
	raw := []byte{0x10, 0x20, 0x30, 0x40}
	wantRaw := append([]byte(nil), raw...)
	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, raw),
	}, nil)

	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	if pixel.Encapsulated {
		t.Fatalf("Encapsulated = true, want false")
	}
	if !bytes.Equal(pixel.Raw, wantRaw) {
		t.Fatalf("raw = %v, want %v", pixel.Raw, wantRaw)
	}

	pixel.Raw[0] = 0xFF

	elem, _ := obj.Get(core.TagPixelData)
	original, ok := elem.RawBytes()
	if !ok {
		t.Fatal("original raw bytes missing")
	}
	if !bytes.Equal(original, wantRaw) {
		t.Fatalf("original raw mutated: %v", original)
	}
}

func TestExtractViewAliasesNativePixelData(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, []byte{0x10, 0x20, 0x30, 0x40}),
	}, nil)

	pixel, err := ExtractView(obj)
	if err != nil {
		t.Fatal(err)
	}
	pixel.Raw[0] = 0xEE
	elem, _ := obj.Get(core.TagPixelData)
	raw, _ := elem.RawBytes()
	if raw[0] != 0xEE {
		t.Fatalf("view does not alias native Pixel Data: object first byte = %#x", raw[0])
	}
}

func TestExtractViewAliasesEncapsulatedPixelData(t *testing.T) {
	obj := object.FromElements([]core.Element{
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, []byte{0, 0, 0, 0}, []byte{1, 2, 3, 4}),
	}, nil)

	pixel, err := ExtractView(obj)
	if err != nil {
		t.Fatal(err)
	}
	pixel.Sequence.OffsetTable[0] = 0xDD
	pixel.Sequence.Fragments[0][0] = 0xEE
	elem, _ := obj.Get(core.TagPixelData)
	original := elem.Value.(core.FragmentSequence)
	if original.OffsetTable[0] != 0xDD || original.Fragments[0][0] != 0xEE {
		t.Fatalf("encapsulated view does not alias object: offset=%#x fragment=%#x", original.OffsetTable[0], original.Fragments[0][0])
	}
}

func BenchmarkExtractPixelData(b *testing.B) {
	const payloadSize = 8 << 20
	native := object.FromElements([]core.Element{
		core.NewRawElement(core.TagPixelData, core.VROW, make([]byte, payloadSize)),
	}, nil)
	fragments := make([][]byte, 128)
	for i := range fragments {
		fragments[i] = make([]byte, payloadSize/len(fragments))
	}
	encapsulated := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB},
		Value:  core.FragmentSequence{OffsetTable: make([]byte, 512), Fragments: fragments},
	}}, nil)

	bench := func(b *testing.B, obj *object.Object, view bool) {
		b.Helper()
		b.SetBytes(payloadSize)
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			var err error
			if view {
				benchmarkExtractedPixelData, err = ExtractView(obj)
			} else {
				benchmarkExtractedPixelData, err = Extract(obj)
			}
			if err != nil {
				b.Fatal(err)
			}
		}
	}
	b.Run("native_clone", func(b *testing.B) { bench(b, native, false) })
	b.Run("native_view", func(b *testing.B) { bench(b, native, true) })
	b.Run("encapsulated_clone", func(b *testing.B) { bench(b, encapsulated, false) })
	b.Run("encapsulated_view", func(b *testing.B) { bench(b, encapsulated, true) })
}

var benchmarkExtractedPixelData PixelData

func TestExtractAllReturnsTopLevelPixelData(t *testing.T) {
	raw := []byte{0x10, 0x20, 0x30, 0x40}
	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, raw),
	}, nil)

	got, err := ExtractAll(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ExtractAll() returned %d item(s), want 1", len(got))
	}
	if got[0].Context != PixelDataContextTopLevel {
		t.Fatalf("context = %q, want %q", got[0].Context, PixelDataContextTopLevel)
	}
	if path := got[0].Path.String(); path != "(7FE0,0010)" {
		t.Fatalf("path = %q, want (7FE0,0010)", path)
	}
	if !bytes.Equal(got[0].Data.Raw, raw) {
		t.Fatalf("raw = % X, want % X", got[0].Data.Raw, raw)
	}
	got[0].Data.Raw[0] = 0xFF
	elem, _ := obj.Get(core.TagPixelData)
	original, _ := elem.RawBytes()
	if bytes.Equal(got[0].Data.Raw, original) {
		t.Fatal("ExtractAll() returned raw bytes sharing object storage")
	}
}

func TestExtractAllReturnsIconImageSequencePixelData(t *testing.T) {
	iconTag := core.NewTag(0x0088, 0x0200)
	topLevel := []byte{0x01, 0x02}
	icon := []byte{0xAA, 0xBB}
	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, topLevel),
		dicomtest.NewSequenceElement(
			iconTag,
			core.DataSet{Elements: []core.Element{
				dicomtest.NewOBElement(core.TagPixelData, icon),
			}},
		),
	}, nil)

	got, err := ExtractAll(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ExtractAll() returned %d item(s), want 2", len(got))
	}
	if got[0].Context != PixelDataContextTopLevel {
		t.Fatalf("first context = %q, want top-level", got[0].Context)
	}
	if got[1].Context != PixelDataContextIconImageSequence {
		t.Fatalf("second context = %q, want icon-image-sequence", got[1].Context)
	}
	if path := got[1].Path.String(); path != "(0088,0200)[0]/(7FE0,0010)" {
		t.Fatalf("icon path = %q, want (0088,0200)[0]/(7FE0,0010)", path)
	}
	if !bytes.Equal(got[1].Data.Raw, icon) {
		t.Fatalf("icon raw = % X, want % X", got[1].Data.Raw, icon)
	}
}

func TestExtractAllReturnsGenericNestedSequencePath(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	nested := []byte{0x05, 0x06}
	obj := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(
			sequenceTag,
			core.DataSet{Elements: []core.Element{dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "NOPIXEL")}},
			core.DataSet{Elements: []core.Element{dicomtest.NewOBElement(core.TagPixelData, nested)}},
		),
	}, nil)

	got, err := ExtractAll(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("ExtractAll() returned %d item(s), want 1", len(got))
	}
	if got[0].Context != PixelDataContextSequence {
		t.Fatalf("context = %q, want %q", got[0].Context, PixelDataContextSequence)
	}
	if path := got[0].Path.String(); path != "(0008,1111)[1]/(7FE0,0010)" {
		t.Fatalf("path = %q, want (0008,1111)[1]/(7FE0,0010)", path)
	}
}

func TestExtractAllReturnsIndependentNestedPaths(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	obj := object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(
			sequenceTag,
			core.DataSet{Elements: []core.Element{dicomtest.NewOBElement(core.TagPixelData, []byte{0x01})}},
			core.DataSet{Elements: []core.Element{dicomtest.NewOBElement(core.TagPixelData, []byte{0x02})}},
		),
	}, nil)

	got, err := ExtractAll(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("ExtractAll() returned %d item(s), want 2", len(got))
	}
	wantPaths := []string{
		"(0008,1111)[0]/(7FE0,0010)",
		"(0008,1111)[1]/(7FE0,0010)",
	}
	gotPaths := []string{got[0].Path.String(), got[1].Path.String()}
	if !reflect.DeepEqual(gotPaths, wantPaths) {
		t.Fatalf("paths = %#v, want %#v", gotPaths, wantPaths)
	}

	got[0].Path[0].ItemIndex = 99
	if got[1].Path[0].ItemIndex != 1 {
		t.Fatalf("second path item index = %d, want retained independent copy", got[1].Path[0].ItemIndex)
	}
}

func TestExtractAllReusesTraversalStackWithoutPixelData(t *testing.T) {
	obj := extractAllWideSequenceFixtureObject(128)

	allocs := testing.AllocsPerRun(100, func() {
		got, err := ExtractAll(obj)
		if err != nil {
			panic(err)
		}
		if len(got) != 0 {
			panic("unexpected pixel data")
		}
	})
	if allocs > 4 {
		t.Fatalf("ExtractAll() allocations/run = %.1f, want <= 4", allocs)
	}
}

func TestPixelDataPathCloneReturnsIndependentCopy(t *testing.T) {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	path := PixelDataPath{
		{Tag: sequenceTag, ItemIndex: 0},
		{Tag: core.TagPixelData, ItemIndex: PixelDataPathNoItem},
	}

	clone := path.Clone()
	path[0].ItemIndex = 7
	path[1].Tag = core.NewTag(0x7FE0, 0x0011)

	want := PixelDataPath{
		{Tag: sequenceTag, ItemIndex: 0},
		{Tag: core.TagPixelData, ItemIndex: PixelDataPathNoItem},
	}
	if !reflect.DeepEqual(clone, want) {
		t.Fatalf("Clone() = %#v, want %#v", clone, want)
	}
}

func TestExtractRemainsTopLevelOnly(t *testing.T) {
	iconTag := core.NewTag(0x0088, 0x0200)
	topLevel := []byte{0x01, 0x02}
	icon := []byte{0xAA, 0xBB}
	obj := object.FromElements([]core.Element{
		dicomtest.NewOBElement(core.TagPixelData, topLevel),
		dicomtest.NewSequenceElement(
			iconTag,
			core.DataSet{Elements: []core.Element{
				dicomtest.NewOBElement(core.TagPixelData, icon),
			}},
		),
	}, nil)

	got, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Raw, topLevel) {
		t.Fatalf("Extract() raw = % X, want top-level % X", got.Raw, topLevel)
	}
}

func TestExtractNativeFrames8BitMonochrome2SingleFrame(t *testing.T) {
	raw := sequentialBytes(64)
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames == nil {
		t.Fatal("ExtractNativeFrames() = nil, want non-nil")
	}

	assertMetadata(t, frames.Metadata, Metadata{
		Rows:                      8,
		Columns:                   8,
		SamplesPerPixel:           1,
		BitsAllocated:             8,
		BitsStored:                8,
		HighBit:                   7,
		PixelRepresentation:       0,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	})
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	if len(frames.Data[0]) != 64 {
		t.Fatalf("frame size = %d, want 64", len(frames.Data[0]))
	}
	if !bytes.Equal(frames.Data[0], raw) {
		t.Fatalf("frame data = %v, want %v", frames.Data[0], raw)
	}
}

func TestExtractNativeFramesViewAliasesObjectPixelData(t *testing.T) {
	obj := object.FromElements(append(
		pixelMetadataElements(1, 4, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3, 4}),
	), nil)

	frames, err := ExtractNativeFramesView(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], []byte{1, 2, 3, 4}) {
		t.Fatalf("frames = %v, want one native frame", frames.Data)
	}
	frames.Data[0][0] = 0xEE
	elem, _ := obj.Get(core.TagPixelData)
	raw, _ := elem.RawBytes()
	if raw[0] != 0xEE {
		t.Fatalf("native frame view does not alias object: first byte = %#x", raw[0])
	}
}

func TestExtractNativeFramesAcceptsMandatoryEvenLengthPadByte(t *testing.T) {
	raw := []byte{1, 2, 3, 0}
	obj := object.FromElements(append(
		pixelMetadataElements(1, 3, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	want := []byte{1, 2, 3}
	if !bytes.Equal(frames.Data[0], want) {
		t.Fatalf("frame data = %v, want %v", frames.Data[0], want)
	}
}

func TestExtractNativeFrames16BitMonochrome2SingleFrame(t *testing.T) {
	raw := encodeWordsLE(
		0x001, 0x123, 0x234, 0x345,
		0x456, 0x567, 0x678, 0x789,
		0x89A, 0x9AB, 0xABC, 0xBCD,
		0xCDE, 0xDEF, 0xEEE, 0xFFF,
	)
	obj := object.FromElements(append(
		pixelMetadataElements(4, 4, 1, 16, 12, 11, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.BytesElement(core.TagPixelData, core.VROW, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames == nil {
		t.Fatal("ExtractNativeFrames() = nil, want non-nil")
	}

	assertMetadata(t, frames.Metadata, Metadata{
		Rows:                      4,
		Columns:                   4,
		SamplesPerPixel:           1,
		BitsAllocated:             16,
		BitsStored:                12,
		HighBit:                   11,
		PixelRepresentation:       0,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	})
	if frames.Metadata.BytesPerSample() != 2 {
		t.Fatalf("BytesPerSample = %d, want 2", frames.Metadata.BytesPerSample())
	}
	if frames.Metadata.FrameSize() != 32 {
		t.Fatalf("FrameSize = %d, want 32", frames.Metadata.FrameSize())
	}
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	if !bytes.Equal(frames.Data[0], raw) {
		t.Fatalf("frame data = %v, want %v", frames.Data[0], raw)
	}
}

func TestExtractNativeFramesOneBitUsesPackedFrameSize(t *testing.T) {
	raw := []byte{0b10101010, 0b10000000}
	obj := object.FromElements(append(
		pixelMetadataElements(1, 9, 1, 1, 1, 0, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := frames.Metadata.FrameSize(), int64(2); got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	if !bytes.Equal(frames.Data[0], raw) {
		t.Fatalf("frame data = %08b, want %08b", frames.Data[0], raw)
	}
}

func TestExtractNativeFramesYBRFull422UsesSubsampledFrameSize(t *testing.T) {
	raw := []byte{10, 20, 30, 40, 128, 128, 128, 128}
	obj := object.FromElements(append(
		pixelMetadataElements(2, 2, 3, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "YBR_FULL_422"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := frames.Metadata.FrameSize(), int64(len(raw)); got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], raw) {
		t.Fatalf("frames = %#v, want one YBR_FULL_422 frame %v", frames.Data, raw)
	}
}

func TestExtractNativeFramesMultiFrame(t *testing.T) {
	raw := sequentialBytes(192)
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, stringPtr("3"),
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames == nil {
		t.Fatal("ExtractNativeFrames() = nil, want non-nil")
	}
	if len(frames.Data) != 3 {
		t.Fatalf("frame count = %d, want 3", len(frames.Data))
	}
	for i := range frames.Data {
		if len(frames.Data[i]) != 64 {
			t.Fatalf("frame %d size = %d, want 64", i, len(frames.Data[i]))
		}
		start := i * 64
		if !bytes.Equal(frames.Data[i], raw[start:start+64]) {
			t.Fatalf("frame %d data mismatch", i)
		}
	}
	if uintptr(unsafe.Pointer(&frames.Data[1][0]))-uintptr(unsafe.Pointer(&frames.Data[0][0])) != 64 {
		t.Fatalf("frame 1 offset mismatch")
	}
	if uintptr(unsafe.Pointer(&frames.Data[2][0]))-uintptr(unsafe.Pointer(&frames.Data[1][0])) != 64 {
		t.Fatalf("frame 2 offset mismatch")
	}
}

func TestExtractNativeFramesErrPixelDataSizeMismatch(t *testing.T) {
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, sequentialBytes(60)),
	), nil)

	_, err := ExtractNativeFrames(obj)
	if err == nil {
		t.Fatal("ExtractNativeFrames() error = nil, want ErrPixelDataSizeMismatch")
	}
	if !errors.Is(err, ErrPixelDataSizeMismatch) {
		t.Fatalf("error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestExtractNativeFramesErrMissingMetadata(t *testing.T) {
	elements := pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
	)
	elements = removeElement(elements, tagRows)
	elements = append(elements, dicomtest.NewOBElement(core.TagPixelData, sequentialBytes(64)))
	obj := object.FromElements(elements, nil)

	_, err := ExtractNativeFrames(obj)
	if err == nil {
		t.Fatal("ExtractNativeFrames() error = nil, want ErrMissingMetadata")
	}
	if !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("error = %v, want ErrMissingMetadata", err)
	}
}

func TestExtractNativeFramesErrEncapsulatedPixelData(t *testing.T) {
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, []byte{0x00, 0x00, 0x00, 0x00}, sequentialBytes(64)),
	), nil)

	_, err := ExtractNativeFrames(obj)
	if err == nil {
		t.Fatal("ExtractNativeFrames() error = nil, want ErrEncapsulatedPixelData")
	}
	if !errors.Is(err, ErrEncapsulatedPixelData) {
		t.Fatalf("error = %v, want ErrEncapsulatedPixelData", err)
	}
}

func TestExtractMetadataTrimsNumberOfFramesBeforeParsing(t *testing.T) {
	obj := object.FromElements(pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, stringPtr(" 3 "),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
	), nil)

	metadata, err := ExtractMetadata(obj)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.NumberOfFrames != 3 {
		t.Fatalf("NumberOfFrames = %d, want 3", metadata.NumberOfFrames)
	}
}

func TestExtractMetadataUsesObjectValueByteOrder(t *testing.T) {
	elements := pixelMetadataElementsWithOrder(binary.BigEndian, 512, 257, 3, 16, 12, 11, 1, nil,
		dicomtest.Uint16Element(tagPlanarConfiguration, core.VRUS, binary.BigEndian, 1),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "RGB"),
	)
	data, err := dicomtest.Part10File(transfer.ExplicitVRBigEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	metadata, err := ExtractMetadata(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Rows != 512 || metadata.Columns != 257 {
		t.Fatalf("geometry = %dx%d, want 512x257", metadata.Rows, metadata.Columns)
	}
	if metadata.SamplesPerPixel != 3 || metadata.BitsAllocated != 16 || metadata.BitsStored != 12 || metadata.HighBit != 11 || metadata.PixelRepresentation != 1 {
		t.Fatalf("metadata = %+v, want big-endian US fields decoded", metadata)
	}
	if !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 1 {
		t.Fatalf("PlanarConfiguration = %d present=%v, want 1 true", metadata.PlanarConfiguration, metadata.PlanarConfigurationPresent)
	}
}

func BenchmarkExtractMetadata(b *testing.B) {
	obj := object.FromElements(pixelMetadataElements(512, 512, 1, 16, 12, 11, 1, stringPtr("120"),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
	), nil)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var err error
		benchmarkExtractedMetadata, err = ExtractMetadata(obj)
		if err != nil {
			b.Fatal(err)
		}
	}
}

var benchmarkExtractedMetadata Metadata

func TestMetadataFrameSizeUsesInt64Arithmetic(t *testing.T) {
	metadata := Metadata{
		Rows:            65535,
		Columns:         65535,
		SamplesPerPixel: 65535,
		BitsAllocated:   16,
		NumberOfFrames:  2,
	}
	if metadata.FrameSize() <= math.MaxInt32 {
		t.Fatalf("FrameSize = %d, want value above MaxInt32", metadata.FrameSize())
	}
	if metadata.TotalSize() != metadata.FrameSize()*2 {
		t.Fatalf("TotalSize = %d, want %d", metadata.TotalSize(), metadata.FrameSize()*2)
	}
}

func TestMetadataFrameSizeUsesPackedBitsBelowOneByte(t *testing.T) {
	metadata := Metadata{
		Rows:            1,
		Columns:         9,
		SamplesPerPixel: 1,
		BitsAllocated:   1,
		NumberOfFrames:  2,
	}
	if got, want := metadata.FrameSize(), int64(2); got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	if got, want := metadata.TotalSize(), int64(4); got != want {
		t.Fatalf("TotalSize = %d, want %d", got, want)
	}
}

func TestMetadataTotalSizeRejectsOverflow(t *testing.T) {
	metadata := Metadata{
		Rows:            math.MaxUint16,
		Columns:         math.MaxUint16,
		SamplesPerPixel: math.MaxUint16,
		BitsAllocated:   16,
		NumberOfFrames:  math.MaxInt32,
	}
	if got := metadata.TotalSize(); got != 0 {
		t.Fatalf("TotalSize = %d, want 0 for overflow", got)
	}
}

func TestExtractMetadataRejectsExcessiveNumberOfFrames(t *testing.T) {
	frames := "2147483648"
	obj := object.FromElements(pixelMetadataElements(1, 1, 1, 8, 8, 7, 0, &frames,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
	), nil)

	_, err := ExtractMetadata(obj)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("ExtractMetadata() error = %v, want ErrInvalidMetadata", err)
	}
}

func TestMemoryRegistryRegisterAndGetCodec(t *testing.T) {
	r := NewMemoryRegistry()
	first := &fakeCodec{}
	second := &fakeCodec{}

	if got, ok := r.GetCodec("1.2.3"); ok || got != nil {
		t.Fatalf("GetCodec() = %#v ok=%v, want <nil> false", got, ok)
	}

	if err := r.RegisterCodec("1.2.3 \x00", first); err != nil {
		t.Fatalf("RegisterCodec() error = %v, want nil", err)
	}

	got, ok := r.GetCodec("1.2.3 \x00")
	if !ok {
		t.Fatal("GetCodec() ok = false, want true")
	}
	if got != first {
		t.Fatalf("GetCodec() = %#v, want %#v", got, first)
	}

	if err := r.RegisterCodec("1.2.3", second); err != nil {
		t.Fatalf("RegisterCodec() replacement error = %v, want nil", err)
	}

	got, ok = r.GetCodec("1.2.3")
	if !ok {
		t.Fatal("GetCodec() ok = false after replacement, want true")
	}
	if got != second {
		t.Fatalf("GetCodec() after replacement = %#v, want %#v", got, second)
	}
}

func TestMemoryRegistryRegisterCodecReturnsValidationErrors(t *testing.T) {
	t.Run("nil receiver", func(t *testing.T) {
		var r *MemoryRegistry
		err := r.RegisterCodec("1.2.3", &fakeCodec{})
		if !errors.Is(err, ErrCodecRegistryNil) {
			t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
		}
	})

	t.Run("nil codec", func(t *testing.T) {
		r := NewMemoryRegistry()
		err := r.RegisterCodec("1.2.3", nil)
		if !errors.Is(err, ErrCodecNil) {
			t.Fatalf("error = %v, want ErrCodecNil", err)
		}
	})

	t.Run("empty normalized uid", func(t *testing.T) {
		r := NewMemoryRegistry()
		err := r.RegisterCodec(" \x00", &fakeCodec{})
		if !errors.Is(err, ErrCodecUIDInvalid) {
			t.Fatalf("error = %v, want ErrCodecUIDInvalid", err)
		}
	})
}

func TestMemoryRegistryDecodeFramesNativeWithoutCodec(t *testing.T) {
	raw := sequentialBytes(64)
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)

	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}

	frames, err := NewMemoryRegistry().DecodeFrames(transfer.ExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 8 || frames.Columns != 8 {
		t.Fatalf("frame geometry = %dx%d, want 8x8", frames.Rows, frames.Columns)
	}
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	if !bytes.Equal(frames.Data[0], raw) {
		t.Fatalf("frame data = %v, want %v", frames.Data[0], raw)
	}
}

func TestDecodeFramesEncapsulatedUncompressedSingleFragmentPerFrame(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, stringPtr("2"),
		[]byte{0, 1, 2, 3},
		[]byte{4, 5, 6, 7},
	)

	frames, err := NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 2 || frames.Columns != 2 {
		t.Fatalf("frame geometry = %dx%d, want 2x2", frames.Rows, frames.Columns)
	}
	if !reflect.DeepEqual(frames.Data, [][]byte{{0, 1, 2, 3}, {4, 5, 6, 7}}) {
		t.Fatalf("frames = %v, want two native frames", frames.Data)
	}
}

func TestDecodeFramesEncapsulatedUncompressedSingleFrameMultiFragment(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, nil,
		[]byte{0, 1},
		[]byte{2, 3},
	)

	frames, err := NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames.Data, [][]byte{{0, 1, 2, 3}}) {
		t.Fatalf("frames = %v, want one assembled native frame", frames.Data)
	}
}

func TestDecodeFramesEncapsulatedUncompressedAcceptsMandatoryEvenLengthPadByte(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 1, 3, nil,
		[]byte{1, 2},
		[]byte{3, 0},
	)

	frames, err := NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{1, 2, 3}}
	if !reflect.DeepEqual(frames.Data, want) {
		t.Fatalf("frames = %v, want %v without trailing pad byte", frames.Data, want)
	}
}

func TestDecodeFramesEncapsulatedUncompressedAcceptsPadBytePerOddFrame(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 1, 3, stringPtr("3"),
		[]byte{1, 2, 3, 0},
		[]byte{4, 5, 6, 0},
		[]byte{7, 8, 9, 0},
	)

	frames, err := DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]byte{{1, 2, 3}, {4, 5, 6}, {7, 8, 9}}
	if !reflect.DeepEqual(frames.Data, want) {
		t.Fatalf("frames = %v, want %v without per-frame pad bytes", frames.Data, want)
	}
}

func TestDecodeFramesEncapsulatedUncompressedMultiFrameAcrossFragments(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, stringPtr("2"),
		[]byte{0, 1},
		[]byte{2, 3, 4},
		[]byte{5, 6, 7},
	)

	frames, err := DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames.Data, [][]byte{{0, 1, 2, 3}, {4, 5, 6, 7}}) {
		t.Fatalf("frames = %v, want two frames split by metadata size", frames.Data)
	}
}

func TestDecodeFramesEncapsulatedUncompressedDoesNotRequireDefaultRegistry(t *testing.T) {
	previous := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = previous
	})
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, nil,
		[]byte{0, 1},
		[]byte{2, 3},
	)

	frames, err := DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(frames.Data, [][]byte{{0, 1, 2, 3}}) {
		t.Fatalf("frames = %v, want one assembled native frame", frames.Data)
	}
}

func TestDecodeFramesEncapsulatedUncompressedRejectsNoFragments(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, nil)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, ErrPixelDataSizeMismatch) {
		t.Fatalf("DecodeFrames() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeFramesEncapsulatedUncompressedRejectsSizeMismatch(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, stringPtr("2"),
		[]byte{0, 1, 2, 3},
		[]byte{4, 5, 6},
	)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, ErrPixelDataSizeMismatch) {
		t.Fatalf("DecodeFrames() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeFramesEncapsulatedUncompressedRejectsMissingMetadata(t *testing.T) {
	elements := pixelMetadataElements(2, 2, 1, 8, 8, 7, 0, nil,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
	)
	elements = removeElement(elements, tagRows)
	elements = append(elements, dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, []byte{0, 1, 2, 3}))
	obj := object.FromElements(elements, nil)
	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewMemoryRegistry().DecodeFrames(transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, ErrMissingMetadata) {
		t.Fatalf("DecodeFrames() error = %v, want ErrMissingMetadata", err)
	}
}

func TestDecodeFramesWithRegisteredCodec(t *testing.T) {
	registry := NewMemoryRegistry()
	obj, pixel := testEncapsulatedPixelObject(t)
	codec := &fakeCodec{
		frames: Frames{
			Rows:    8,
			Columns: 8,
			Data:    [][]byte{{0xAA, 0xBB, 0xCC}},
		},
	}

	if err := registry.RegisterCodec("1.2.840.10008.9999.9999.2", codec); err != nil {
		t.Fatalf("RegisterCodec() error = %v, want nil", err)
	}

	got, err := registry.DecodeFrames("1.2.840.10008.9999.9999.2 \x00", pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, codec.frames) {
		t.Fatalf("DecodeFrames() = %#v, want %#v", got, codec.frames)
	}
	if !reflect.DeepEqual(codec.pixel, pixel) {
		t.Fatalf("codec pixel = %#v, want %#v", codec.pixel, pixel)
	}
	if codec.obj != obj {
		t.Fatalf("codec obj = %p, want %p", codec.obj, obj)
	}
}

func TestDecodeFramesWrapsCodecFailureWithTransferSyntaxContext(t *testing.T) {
	registry := NewMemoryRegistry()
	obj, pixel := testEncapsulatedPixelObject(t)
	codec := &fakeCodec{err: ErrInvalidMetadata}
	if err := registry.RegisterCodec(transfer.JPEGExtended.UID, codec); err != nil {
		t.Fatalf("RegisterCodec() error = %v, want nil", err)
	}

	_, err := registry.DecodeFrames(transfer.JPEGExtended.UID, pixel, obj)
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("DecodeFrames() error = %v, want ErrInvalidMetadata", err)
	}
	var decodeErr *CodecDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("DecodeFrames() error = %T, want CodecDecodeError", err)
	}
	if decodeErr.TransferSyntaxUID != transfer.JPEGExtended.UID || decodeErr.TransferSyntaxName != transfer.JPEGExtended.Name {
		t.Fatalf("CodecDecodeError transfer syntax = %q/%q, want %q/%q", decodeErr.TransferSyntaxUID, decodeErr.TransferSyntaxName, transfer.JPEGExtended.UID, transfer.JPEGExtended.Name)
	}
	for _, want := range []string{transfer.JPEGExtended.UID, transfer.JPEGExtended.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
	if strings.Contains(err.Error(), ErrInvalidMetadata.Error()) || !errors.Is(err, ErrCodecDecodeFailed) {
		t.Fatalf("error = %q, want redacted typed codec failure", err)
	}
}

func TestDecodeFramesReturnsErrCodecNotFound(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := NewMemoryRegistry().DecodeFrames("1.2.840.10008.9999.9999.404", pixel, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrCodecNotFound")
	}
	if !errors.Is(err, ErrCodecNotFound) {
		t.Fatalf("error = %v, want ErrCodecNotFound", err)
	}
}

func TestDecodeFramesKnownMissingCodecErrorIncludesSyntaxAndRegistrationHint(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.JPEGBaseline.UID, pixel, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrCodecNotFound")
	}
	if !errors.Is(err, ErrCodecNotFound) {
		t.Fatalf("error = %v, want ErrCodecNotFound", err)
	}
	for _, want := range []string{
		transfer.JPEGBaseline.UID,
		transfer.JPEGBaseline.Name,
		"pixeldata/jpeg",
		"RegisterDefault",
		"registered codecs: none",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestDecodeFramesKnownJPEGXLErrorIncludesUnsupportedBoundaryHint(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.JPEGXL.UID, pixel, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrCodecNotFound")
	}
	if !errors.Is(err, ErrCodecNotFound) {
		t.Fatalf("error = %v, want ErrCodecNotFound", err)
	}
	var availability *CodecAvailabilityError
	if !errors.As(err, &availability) {
		t.Fatalf("error = %T, want CodecAvailabilityError", err)
	}
	if availability.TransferSyntaxUID != transfer.JPEGXL.UID {
		t.Fatalf("TransferSyntaxUID = %q, want %q", availability.TransferSyntaxUID, transfer.JPEGXL.UID)
	}
	for _, want := range []string{
		transfer.JPEGXL.UID,
		transfer.JPEGXL.Name,
		"JPEG XL",
		"no default decoder adapter",
		"metadata and encapsulated Pixel Data can be preserved",
		"registered codecs: none",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestDecodeFramesKnownJPEG2000ErrorIncludesOptionalAdapterHint(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	tests := []transfer.Syntax{
		transfer.JPEG2000,
		transfer.HTJ2KLossless,
	}

	for _, syntax := range tests {
		t.Run(syntax.Name, func(t *testing.T) {
			_, err := NewMemoryRegistry().DecodeFrames(syntax.UID, pixel, obj)
			if err == nil {
				t.Fatal("DecodeFrames() error = nil, want ErrCodecNotFound")
			}
			if !errors.Is(err, ErrCodecNotFound) {
				t.Fatalf("error = %v, want ErrCodecNotFound", err)
			}
			var availability *CodecAvailabilityError
			if !errors.As(err, &availability) {
				t.Fatalf("error = %T, want CodecAvailabilityError", err)
			}
			if availability.TransferSyntaxUID != syntax.UID {
				t.Fatalf("TransferSyntaxUID = %q, want %q", availability.TransferSyntaxUID, syntax.UID)
			}
			for _, want := range []string{
				syntax.UID,
				syntax.Name,
				"JPEG 2000 / HTJ2K",
				"no default decoder adapter",
				"examples/codec-adapters/jpeg2000",
				"RegisterDefault",
				"registered codecs: none",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestDecodeFramesKnownJPEGLSErrorIncludesOptionalAdapterHint(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	tests := []transfer.Syntax{
		transfer.JPEGLSLossless,
		transfer.JPEGLSNearLossless,
	}

	for _, syntax := range tests {
		t.Run(syntax.Name, func(t *testing.T) {
			_, err := NewMemoryRegistry().DecodeFrames(syntax.UID, pixel, obj)
			if err == nil {
				t.Fatal("DecodeFrames() error = nil, want ErrCodecNotFound")
			}
			if !errors.Is(err, ErrCodecNotFound) {
				t.Fatalf("error = %v, want ErrCodecNotFound", err)
			}
			var availability *CodecAvailabilityError
			if !errors.As(err, &availability) {
				t.Fatalf("error = %T, want CodecAvailabilityError", err)
			}
			if availability.TransferSyntaxUID != syntax.UID {
				t.Fatalf("TransferSyntaxUID = %q, want %q", availability.TransferSyntaxUID, syntax.UID)
			}
			for _, want := range []string{
				syntax.UID,
				syntax.Name,
				"JPEG-LS",
				"no default decoder adapter",
				"examples/codec-adapters/jpegls",
				"RegisterDefault",
				"registered codecs: none",
			} {
				if !strings.Contains(err.Error(), want) {
					t.Fatalf("error = %q, want substring %q", err, want)
				}
			}
		})
	}
}

func TestDecodeFramesKnownVideoSyntaxErrorIncludesVideoPolicyHint(t *testing.T) {
	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.MPEG2MPML.UID, pixel, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrMediaPayloadNotRenderable")
	}
	if !errors.Is(err, ErrMediaPayloadNotRenderable) {
		t.Fatalf("error = %v, want ErrMediaPayloadNotRenderable", err)
	}
	var mediaErr *MediaPayloadNotRenderableError
	if !errors.As(err, &mediaErr) {
		t.Fatalf("error = %T, want MediaPayloadNotRenderableError", err)
	}
	if mediaErr.TransferSyntaxUID != transfer.MPEG2MPML.UID || mediaErr.MediaKind != "video" {
		t.Fatalf("media error = %#v, want MPEG2 video transfer metadata", mediaErr)
	}
	if mediaErr.FragmentCount != len(pixel.Sequence.Fragments) {
		t.Fatalf("FragmentCount = %d, want %d", mediaErr.FragmentCount, len(pixel.Sequence.Fragments))
	}
	for _, want := range []string{
		transfer.MPEG2MPML.UID,
		transfer.MPEG2MPML.Name,
		"video media payload",
		"not renderable as still-image frames",
		"external media pipeline",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "RegisterCodec") {
		t.Fatalf("error = %q, want video policy hint instead of generic registration hint", err)
	}
}

func TestExtractJPIPReferenceReturnsProviderURLAndSyntaxMetadata(t *testing.T) {
	obj := object.FromElements(pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "17"),
		dicomtest.NewStringElement(tagPixelDataProviderURL, core.VRUR, "https://jpip.example/images/abc.jp2?target=abc"),
	), nil)

	ref, err := ExtractJPIPReference(transfer.JPIPHTJ2KReferenced.UID, obj)
	if err != nil {
		t.Fatalf("ExtractJPIPReference: %v", err)
	}
	if ref.PixelDataProviderURL != "https://jpip.example/images/abc.jp2?target=abc" {
		t.Fatalf("PixelDataProviderURL = %q", ref.PixelDataProviderURL)
	}
	if ref.TransferSyntaxUID != transfer.JPIPHTJ2KReferenced.UID || ref.TransferSyntaxName != transfer.JPIPHTJ2KReferenced.Name {
		t.Fatalf("reference syntax = %#v, want JPIP HTJ2K", ref)
	}
	if !ref.HTJ2K || ref.Deflated {
		t.Fatalf("reference flags = HTJ2K %v Deflated %v, want true/false", ref.HTJ2K, ref.Deflated)
	}
	if ref.NumberOfFrames != 17 {
		t.Fatalf("NumberOfFrames = %d, want 17", ref.NumberOfFrames)
	}
}

func TestDecodeFramesJPIPReferencedRequiresExternalRetrieval(t *testing.T) {
	obj := object.FromElements(pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(tagPixelDataProviderURL, core.VRUR, "https://jpip.example/images/abc.jp2"),
	), nil)

	_, err := NewMemoryRegistry().DecodeFrames(transfer.JPIPReferenced.UID, PixelData{}, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrJPIPRetrievalRequired")
	}
	if !errors.Is(err, ErrJPIPRetrievalRequired) {
		t.Fatalf("error = %v, want ErrJPIPRetrievalRequired", err)
	}
	var retrievalErr *JPIPRetrievalRequiredError
	if !errors.As(err, &retrievalErr) {
		t.Fatalf("error = %T, want JPIPRetrievalRequiredError", err)
	}
	if retrievalErr.Reference.PixelDataProviderURL != "https://jpip.example/images/abc.jp2" {
		t.Fatalf("retrieval reference = %#v", retrievalErr.Reference)
	}
	for _, want := range []string{
		transfer.JPIPReferenced.UID,
		transfer.JPIPReferenced.Name,
		"JPIP referenced pixel stream",
		"external streaming retrieval",
		"Pixel Data Provider URL present (redacted)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
	if strings.Contains(err.Error(), "jpip.example") {
		t.Fatalf("error leaked provider URL: %q", err)
	}
}

func TestDecodeFramesPropagatesCodecError(t *testing.T) {
	registry := NewMemoryRegistry()
	obj, pixel := testEncapsulatedPixelObject(t)
	wantErr := errors.New("decode failed")
	codec := &fakeCodec{err: wantErr}

	if err := registry.RegisterCodec("1.2.840.10008.9999.9999.2", codec); err != nil {
		t.Fatalf("RegisterCodec() error = %v, want nil", err)
	}

	_, err := registry.DecodeFrames("1.2.840.10008.9999.9999.2", pixel, obj)
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !reflect.DeepEqual(codec.pixel, pixel) {
		t.Fatalf("codec pixel = %#v, want %#v", codec.pixel, pixel)
	}
	if codec.obj != obj {
		t.Fatalf("codec obj = %p, want %p", codec.obj, obj)
	}
}

func TestMemoryRegistryDecodeFramesReturnsErrIncompatiblePixelData(t *testing.T) {
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, sequentialBytes(64)),
	), nil)

	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}

	_, err = NewMemoryRegistry().DecodeFrames(transfer.JPEGBaseline.UID, pixel, obj)
	if err == nil {
		t.Fatal("DecodeFrames() error = nil, want ErrIncompatiblePixelData")
	}
	if !errors.Is(err, ErrIncompatiblePixelData) {
		t.Fatalf("error = %v, want ErrIncompatiblePixelData", err)
	}
}

func TestMemoryRegistryDecodeFramesNilReceiverReturnsErrCodecRegistryNilForEncapsulatedData(t *testing.T) {
	var registry *MemoryRegistry
	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := registry.DecodeFrames(transfer.JPEGBaseline.UID, pixel, obj)
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
	}
	for _, want := range []string{transfer.JPEGBaseline.UID, transfer.JPEGBaseline.Name} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error = %q, want substring %q", err, want)
		}
	}
}

func TestMemoryRegistryRegisteredCodecUIDsReturnsSortedNormalizedUIDs(t *testing.T) {
	registry := NewMemoryRegistry()
	if err := registry.RegisterCodec("1.2.840.10008.1.2.4.50 \x00", &fakeCodec{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterCodec("1.2.840.10008.1.2.5", &fakeCodec{}); err != nil {
		t.Fatal(err)
	}

	got := registry.RegisteredCodecUIDs()
	want := []string{"1.2.840.10008.1.2.4.50", "1.2.840.10008.1.2.5"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("RegisteredCodecUIDs() = %#v, want %#v", got, want)
	}
}

func TestPackageLevelRegisterCodecAndDecodeFrames(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = NewMemoryRegistry()
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	obj, pixel := testEncapsulatedPixelObject(t)
	codec := &fakeCodec{
		frames: Frames{
			Rows:    8,
			Columns: 8,
			Data:    [][]byte{{0xAA, 0xBB, 0xCC}},
		},
	}

	if err := RegisterCodec("1.2.840.10008.9999.9999.2 \x00", codec); err != nil {
		t.Fatalf("RegisterCodec() error = %v, want nil", err)
	}

	gotCodec, ok, err := GetCodec("1.2.840.10008.9999.9999.2")
	if err != nil {
		t.Fatalf("GetCodec() error = %v, want nil", err)
	}
	if !ok {
		t.Fatal("GetCodec() ok = false, want true")
	}
	if gotCodec != codec {
		t.Fatalf("GetCodec() = %#v, want %#v", gotCodec, codec)
	}

	got, err := DecodeFrames("1.2.840.10008.9999.9999.2 \x00", pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, codec.frames) {
		t.Fatalf("DecodeFrames() = %#v, want %#v", got, codec.frames)
	}
	if !reflect.DeepEqual(codec.pixel, pixel) {
		t.Fatalf("codec pixel = %#v, want %#v", codec.pixel, pixel)
	}
	if codec.obj != obj {
		t.Fatalf("codec obj = %p, want %p", codec.obj, obj)
	}
}

func TestPackageLevelRegisterCodecReturnsErrCodecRegistryNil(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	err := RegisterCodec("1.2.3", &fakeCodec{})
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
	}
}

func TestPackageLevelGetCodecReturnsErrCodecRegistryNil(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	got, ok, err := GetCodec("1.2.3")
	if got != nil || ok {
		t.Fatalf("GetCodec() = %#v ok=%v, want <nil> false", got, ok)
	}
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
	}
}

func TestCheckCodecAvailabilityDoesNotDecodePixelData(t *testing.T) {
	registry := NewMemoryRegistry()
	codec := &fakeCodec{}
	if err := registry.RegisterCodec(transfer.JPEGBaseline.UID, codec); err != nil {
		t.Fatal(err)
	}

	if err := CheckCodecAvailability(registry, transfer.JPEGBaseline.UID); err != nil {
		t.Fatalf("CheckCodecAvailability() error = %v, want nil", err)
	}
	if codec.calls != 0 {
		t.Fatalf("codec was invoked during availability check: %#v", codec)
	}
}

func TestCheckCodecAvailabilityReportsMissingCodecWithRegistrationHint(t *testing.T) {
	registry := NewMemoryRegistry()
	if err := registry.RegisterCodec(transfer.RLELossless.UID, &fakeCodec{}); err != nil {
		t.Fatal(err)
	}

	err := CheckCodecAvailability(registry, transfer.JPEG2000LosslessOnly.UID)
	if !errors.Is(err, ErrCodecNotFound) {
		t.Fatalf("CheckCodecAvailability() error = %v, want ErrCodecNotFound", err)
	}
	var availabilityErr *CodecAvailabilityError
	if !errors.As(err, &availabilityErr) {
		t.Fatalf("CheckCodecAvailability() error type = %T, want *CodecAvailabilityError", err)
	}
	if !strings.Contains(err.Error(), "JPEG 2000 / HTJ2K") || !strings.Contains(err.Error(), "optional adapter") {
		t.Fatalf("CheckCodecAvailability() error = %q, want optional adapter guidance", err)
	}
	if got, want := availabilityErr.RegisteredCodecUIDs, []string{transfer.RLELossless.UID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("RegisteredCodecUIDs = %#v, want %#v", got, want)
	}
}

func TestCheckCodecAvailabilityReportsNilRegistry(t *testing.T) {
	err := CheckCodecAvailability(nil, transfer.JPEGBaseline.UID)
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("CheckCodecAvailability() error = %v, want ErrCodecRegistryNil", err)
	}
}

func TestPackageLevelDecodeFramesReturnsErrCodecRegistryNil(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := DecodeFrames("1.2.3", pixel, obj)
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
	}
}

func TestPackageLevelDecodeFramesJPIPRequiresExternalRetrievalWhenRegistryNil(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	obj := object.FromElements(pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(tagPixelDataProviderURL, core.VRUR, "https://jpip.example/images/abc.jp2"),
	), nil)

	_, err := DecodeFrames(transfer.JPIPReferenced.UID, PixelData{}, obj)
	if !errors.Is(err, ErrJPIPRetrievalRequired) {
		t.Fatalf("error = %v, want ErrJPIPRetrievalRequired", err)
	}
}

func TestPackageLevelDecodeFramesVideoMediaNotRenderableWhenRegistryNil(t *testing.T) {
	prev := DefaultRegistry
	DefaultRegistry = nil
	t.Cleanup(func() {
		DefaultRegistry = prev
	})

	obj, pixel := testEncapsulatedPixelObject(t)

	_, err := DecodeFrames(transfer.HEVCMP51.UID, pixel, obj)
	if !errors.Is(err, ErrMediaPayloadNotRenderable) {
		t.Fatalf("error = %v, want ErrMediaPayloadNotRenderable", err)
	}
}

type fakeCodec struct {
	pixel  PixelData
	obj    *object.Object
	frames Frames
	err    error
	calls  int
}

func (c *fakeCodec) Decode(pixel PixelData, obj *object.Object) (Frames, error) {
	c.calls++
	c.pixel = pixel
	c.obj = obj
	if c.err != nil {
		return Frames{}, c.err
	}
	return c.frames, nil
}

func testEncapsulatedPixelObject(t *testing.T) (*object.Object, PixelData) {
	t.Helper()
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, []byte{0x00, 0x00, 0x00, 0x00}, []byte{0xAA, 0xBB, 0xCC}),
	), nil)
	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func encapsulatedUncompressedPixelObject(t *testing.T, rows, columns uint16, numberOfFrames *string, fragments ...[]byte) (*object.Object, PixelData) {
	t.Helper()
	obj := object.FromElements(append(
		pixelMetadataElements(rows, columns, 1, 8, 8, 7, 0, numberOfFrames,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	), nil)
	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func pixelMetadataElements(rows, columns, samplesPerPixel, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, numberOfFrames *string, extra ...core.Element) []core.Element {
	return pixelMetadataElementsWithOrder(nil, rows, columns, samplesPerPixel, bitsAllocated, bitsStored, highBit, pixelRepresentation, numberOfFrames, extra...)
}

func pixelMetadataElementsWithOrder(order binary.ByteOrder, rows, columns, samplesPerPixel, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, numberOfFrames *string, extra ...core.Element) []core.Element {
	elements := []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, order, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, order, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, order, samplesPerPixel),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, order, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, order, bitsStored),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, order, highBit),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, order, pixelRepresentation),
	}
	if numberOfFrames != nil {
		elements = append(elements, dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, *numberOfFrames))
	}
	return append(elements, extra...)
}

func extractAllWideSequenceFixtureObject(items int) *object.Object {
	sequenceTag := core.NewTag(0x0008, 0x1111)
	dataSets := make([]core.DataSet, items)
	for i := range dataSets {
		dataSets[i] = core.DataSet{
			Elements: []core.Element{
				dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "NO^PIXEL"),
			},
		}
	}
	return object.FromElements([]core.Element{
		dicomtest.NewSequenceElement(sequenceTag, dataSets...),
	}, nil)
}

func BenchmarkExtractAllWideSequenceWithoutPixelData(b *testing.B) {
	obj := extractAllWideSequenceFixtureObject(128)

	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		got, err := ExtractAll(obj)
		if err != nil {
			b.Fatal(err)
		}
		if len(got) != 0 {
			b.Fatalf("ExtractAll() returned %d item(s), want 0", len(got))
		}
	}
}

func assertMetadata(t *testing.T, got, want Metadata) {
	t.Helper()
	if got != want {
		t.Fatalf("metadata = %#v, want %#v", got, want)
	}
}

func sequentialBytes(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = byte(i)
	}
	return data
}

func stringPtr(s string) *string {
	return &s
}

func removeElement(elements []core.Element, tag core.Tag) []core.Element {
	filtered := make([]core.Element, 0, len(elements))
	for _, elem := range elements {
		if elem.Tag() == tag {
			continue
		}
		filtered = append(filtered, elem)
	}
	return filtered
}

func encodeWordsLE(values ...uint16) []byte {
	buf := make([]byte, 2*len(values))
	for i, value := range values {
		binary.LittleEndian.PutUint16(buf[i*2:], value)
	}
	return buf
}
