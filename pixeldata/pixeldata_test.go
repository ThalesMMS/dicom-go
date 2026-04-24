package pixeldata

import (
	"bytes"
	"encoding/binary"
	"errors"
	"math"
	"reflect"
	"testing"
	"unsafe"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

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

	_, err := registry.DecodeFrames("1.2.840.10008.9999.9999.2", pixel, obj)
	if !errors.Is(err, ErrCodecRegistryNil) {
		t.Fatalf("error = %v, want ErrCodecRegistryNil", err)
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

type fakeCodec struct {
	pixel  PixelData
	obj    *object.Object
	frames Frames
	err    error
}

func (c *fakeCodec) Decode(pixel PixelData, obj *object.Object) (Frames, error) {
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

func pixelMetadataElements(rows, columns, samplesPerPixel, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16, numberOfFrames *string, extra ...core.Element) []core.Element {
	elements := []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, samplesPerPixel),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsStored),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, highBit),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, pixelRepresentation),
	}
	if numberOfFrames != nil {
		elements = append(elements, dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, *numberOfFrames))
	}
	return append(elements, extra...)
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
