package jpegxladapter

import (
	"encoding/binary"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagPlanarConfiguration       = core.NewTag(0x0028, 0x0006)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
)

func TestRegisterRegistersOnlyJPEGXLStillImageSyntaxes(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()

	if err := Register(registry); err != nil {
		t.Fatal(err)
	}
	for _, uid := range supportedUIDs() {
		if _, ok := registry.GetCodec(uid); !ok {
			t.Fatalf("codec for %s not registered", uid)
		}
	}
	if _, ok := registry.GetCodec(transfer.JPEGBaseline.UID); ok {
		t.Fatal("baseline JPEG codec registered, want JPEG XL syntaxes only")
	}
	if _, ok := registry.GetCodec(transfer.HTJ2K.UID); ok {
		t.Fatal("HTJ2K codec registered, want JPEG XL syntaxes only")
	}
}

func TestDecodeFramesJPEGXLDelegatesOneFragmentPerFrame(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{numberOfFrames: 2}, []byte("frame-1"), []byte("frame-2"))
	decoder := &fakeDecoder{outputs: [][]byte{{1, 2}, {3, 4}}}

	frames, err := NewWithDecoder(decoder).Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 || !equalFrames(frames.Data, [][]byte{{1, 2}, {3, 4}}) {
		t.Fatalf("Decode() = %#v, want rows=1 columns=2 data=[[1 2] [3 4]]", frames)
	}
	if len(decoder.fragments) != 2 ||
		!slices.Equal(decoder.fragments[0], []byte("frame-1")) ||
		!slices.Equal(decoder.fragments[1], []byte("frame-2")) {
		t.Fatalf("decoder fragments = %#v, want copied frame fragments", decoder.fragments)
	}
	if len(decoder.metadata) != 2 || decoder.metadata[0].Rows != 1 || decoder.metadata[0].Columns != 2 {
		t.Fatalf("decoder metadata = %#v, want extracted DICOM metadata", decoder.metadata)
	}
}

func TestDecodeFramesJPEGXLJoinsFragmentsForSingleFrame(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{}, []byte("part-1"), []byte("part-2"))
	decoder := &fakeDecoder{outputs: [][]byte{{1, 2}}}

	if _, err := NewWithDecoder(decoder).Decode(pixel, obj); err != nil {
		t.Fatal(err)
	}
	if len(decoder.fragments) != 1 || !slices.Equal(decoder.fragments[0], []byte("part-1part-2")) {
		t.Fatalf("decoder fragments = %#v, want joined single-frame payload", decoder.fragments)
	}
}

func TestDecodeFramesJPEGXLUsesBasicOffsetTableForMultiFragmentFrames(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{numberOfFrames: 2}, []byte("aa"), []byte("b"), []byte("cc"), []byte("d"))
	pixel.Sequence.OffsetTable = make([]byte, 8)
	binary.LittleEndian.PutUint32(pixel.Sequence.OffsetTable[4:], 20)
	decoder := &fakeDecoder{outputs: [][]byte{{1, 2}, {3, 4}}}

	if _, err := NewWithDecoder(decoder).Decode(pixel, obj); err != nil {
		t.Fatal(err)
	}
	if len(decoder.fragments) != 2 ||
		!slices.Equal(decoder.fragments[0], []byte("aab")) ||
		!slices.Equal(decoder.fragments[1], []byte("ccd")) {
		t.Fatalf("decoder fragments = %#v, want BOT-grouped frames", decoder.fragments)
	}
}

func TestDecodeRejectsUnsupportedFragmentLayout(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{numberOfFrames: 2}, []byte("a"), []byte("b"), []byte("c"))

	_, err := NewWithDecoder(&fakeDecoder{outputs: [][]byte{{1, 2}}}).Decode(pixel, obj)
	if !errors.Is(err, ErrUnsupportedFragmentLayout) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedFragmentLayout", err)
	}
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeRejectsUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name          string
		opts          jpegxlMetadataOptions
		want          error
		wantSubstring string
	}{
		{
			name:          "bits allocated",
			opts:          jpegxlMetadataOptions{bitsAllocated: 12},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "BitsAllocated=12",
		},
		{
			name:          "bits stored",
			opts:          jpegxlMetadataOptions{bitsAllocated: 16, bitsStored: 17},
			want:          pixeldata.ErrInvalidMetadata,
			wantSubstring: "BitsStored=17 BitsAllocated=16",
		},
		{
			name:          "pixel representation",
			opts:          jpegxlMetadataOptions{pixelRepresentation: 2},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "PixelRepresentation=2",
		},
		{
			name: "planar configuration",
			opts: jpegxlMetadataOptions{
				samplesPerPixel:     3,
				photometric:         "RGB",
				planarConfiguration: uint16Ptr(1),
			},
			want:          pixeldata.ErrUnsupportedPlanarConfiguration,
			wantSubstring: "PlanarConfiguration=1",
		},
		{
			name:          "photometric interpretation",
			opts:          jpegxlMetadataOptions{photometric: "PALETTE COLOR"},
			want:          pixeldata.ErrUnsupportedPhotometricInterpretation,
			wantSubstring: "PhotometricInterpretation=PALETTE COLOR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, pixel := jpegxlObject(t, tt.opts, []byte("encoded"))
			_, err := NewWithDecoder(&fakeDecoder{outputs: [][]byte{{1, 2}}}).Decode(pixel, obj)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode() error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Fatalf("Decode() error = %q, want substring %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestDecodeAllowsSignedMonochromePixels(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{pixelRepresentation: 1}, []byte("encoded"))
	decoder := &fakeDecoder{outputs: [][]byte{{0xfe, 0xff}}}

	frames, err := NewWithDecoder(decoder).Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !equalFrames(frames.Data, [][]byte{{0xfe, 0xff}}) {
		t.Fatalf("Decode() frames = %#v, want signed monochrome bytes", frames.Data)
	}
	if len(decoder.metadata) != 1 || decoder.metadata[0].PixelRepresentation != 1 {
		t.Fatalf("decoder metadata = %#v, want PixelRepresentation=1", decoder.metadata)
	}
}

func TestDecodeAllowsYBRColorPhotometricInterpretation(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{
		columns:         2,
		samplesPerPixel: 3,
		photometric:     "YBR_FULL_422",
	}, []byte("encoded"))
	decoder := &fakeDecoder{outputs: [][]byte{{0, 128, 255, 1, 129, 254}}}

	frames, err := NewWithDecoder(decoder).Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !equalFrames(frames.Data, [][]byte{{0, 128, 255, 1, 129, 254}}) {
		t.Fatalf("Decode() frames = %#v, want color bytes", frames.Data)
	}
	if len(decoder.metadata) != 1 || strings.TrimSpace(decoder.metadata[0].PhotometricInterpretation) != "YBR_FULL_422" {
		t.Fatalf("decoder metadata = %#v, want YBR_FULL_422", decoder.metadata)
	}
}

func TestDecodeRejectsDecodedFrameSizeMismatch(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{}, []byte("encoded"))

	_, err := NewWithDecoder(&fakeDecoder{outputs: [][]byte{{1}}}).Decode(pixel, obj)
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodePreservesDjxlUnavailableError(t *testing.T) {
	obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{}, []byte("encoded"))

	_, err := NewWithDecoder(&fakeDecoder{err: ErrDjxlUnavailable}).Decode(pixel, obj)
	if !errors.Is(err, ErrDjxlUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrDjxlUnavailable", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("Decode() error = %v, should not classify missing djxl as malformed codestream", err)
	}
}

type fakeDecoder struct {
	outputs   [][]byte
	err       error
	metadata  []pixeldata.Metadata
	fragments [][]byte
}

func (d *fakeDecoder) DecodeFrame(fragment []byte, metadata pixeldata.Metadata) ([]byte, error) {
	d.metadata = append(d.metadata, metadata)
	d.fragments = append(d.fragments, append([]byte(nil), fragment...))
	if d.err != nil {
		return nil, d.err
	}
	if len(d.outputs) == 0 {
		return nil, nil
	}
	output := d.outputs[0]
	d.outputs = d.outputs[1:]
	return append([]byte(nil), output...), nil
}

type jpegxlMetadataOptions struct {
	rows                uint16
	columns             uint16
	samplesPerPixel     uint16
	bitsAllocated       uint16
	bitsStored          uint16
	pixelRepresentation uint16
	photometric         string
	numberOfFrames      int
	planarConfiguration *uint16
}

func jpegxlObject(t *testing.T, opts jpegxlMetadataOptions, fragments ...[]byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj := object.FromElements(append(jpegxlMetadataElements(opts), fragmentElement(fragments...)), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func jpegxlMetadataElements(opts jpegxlMetadataOptions) []core.Element {
	rows := opts.rows
	if rows == 0 {
		rows = 1
	}
	columns := opts.columns
	if columns == 0 {
		columns = 2
	}
	samplesPerPixel := opts.samplesPerPixel
	if samplesPerPixel == 0 {
		samplesPerPixel = 1
	}
	bitsAllocated := opts.bitsAllocated
	if bitsAllocated == 0 {
		bitsAllocated = 8
	}
	bitsStored := opts.bitsStored
	if bitsStored == 0 {
		bitsStored = bitsAllocated
	}
	photometric := opts.photometric
	if photometric == "" {
		photometric = "MONOCHROME2"
	}
	numberOfFrames := opts.numberOfFrames
	if numberOfFrames == 0 {
		numberOfFrames = 1
	}

	elements := []core.Element{
		uint16Element(tagRows, rows),
		uint16Element(tagColumns, columns),
		uint16Element(tagSamplesPerPixel, samplesPerPixel),
		stringElement(tagPhotometricInterpretation, core.VRCS, photometric),
		stringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(numberOfFrames)),
		uint16Element(tagBitsAllocated, bitsAllocated),
		uint16Element(tagBitsStored, bitsStored),
		uint16Element(tagHighBit, bitsStored-1),
		uint16Element(tagPixelRepresentation, opts.pixelRepresentation),
	}
	if opts.planarConfiguration != nil {
		elements = append(elements, uint16Element(tagPlanarConfiguration, *opts.planarConfiguration))
	}
	return elements
}

func uint16Element(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.NewRawElement(tag, vr, []byte(value))
}

func fragmentElement(fragments ...[]byte) core.Element {
	cloned := make([][]byte, len(fragments))
	for i := range fragments {
		cloned[i] = append([]byte(nil), fragments[i]...)
	}
	return core.Element{
		Header: core.ElementHeader{
			Tag:       core.TagPixelData,
			VR:        core.VROB,
			Length:    core.UndefinedLength,
			LengthSet: true,
		},
		Value: core.FragmentSequence{Fragments: cloned},
	}
}

func uint16Ptr(value uint16) *uint16 {
	return &value
}

func equalFrames(got, want [][]byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !slices.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}
