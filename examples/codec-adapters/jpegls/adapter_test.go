package jpegls

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

func TestRegisterRegistersJPEGLSSyntaxes(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()

	if err := Register(registry, &fakeDecoder{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.GetCodec(transfer.JPEGLSLossless.UID); !ok {
		t.Fatal("lossless codec not registered")
	}
	if _, ok := registry.GetCodec(transfer.JPEGLSNearLossless.UID); !ok {
		t.Fatal("near-lossless codec not registered")
	}
}

func TestDecodeFramesJPEGLSLossless(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))
	decoder := &fakeDecoder{outputs: [][]byte{{1, 2}}}

	frames, err := NewLossless(decoder).Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 1 || frames.Columns != 2 || !equalFrames(frames.Data, [][]byte{{1, 2}}) {
		t.Fatalf("Decode() = %#v, want rows=1 columns=2 data=[[1 2]]", frames)
	}
	if len(decoder.inputs) != 1 || decoder.inputs[0].NearLossless {
		t.Fatalf("decoder inputs = %#v, want one lossless input", decoder.inputs)
	}
	if !slices.Equal(decoder.fragments[0], []byte("encoded")) {
		t.Fatalf("decoder fragment = %q, want encoded", decoder.fragments[0])
	}
}

func TestDecodeFramesJPEGLSNearLossless(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))
	decoder := &fakeDecoder{outputs: [][]byte{{1, 2}}}
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, decoder); err != nil {
		t.Fatal(err)
	}

	if _, err := registry.DecodeFrames(transfer.JPEGLSNearLossless.UID, pixel, obj); err != nil {
		t.Fatal(err)
	}
	if len(decoder.inputs) != 1 || !decoder.inputs[0].NearLossless {
		t.Fatalf("decoder inputs = %#v, want one near-lossless input", decoder.inputs)
	}
}

func TestDecodeRejectsUnavailableDecoder(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))

	_, err := NewLossless(nil).Decode(pixel, obj)
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrDecoderUnavailable", err)
	}
}

func TestDecodePreservesUnavailableBackendError(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))

	_, err := NewLossless(&fakeDecoder{err: ErrDecoderUnavailable}).Decode(pixel, obj)
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrDecoderUnavailable", err)
	}
	if errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("Decode() error = %v, should not classify missing backend as malformed frame", err)
	}
}

func TestRegistryDecodeAddsTransferSyntaxContext(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, &fakeDecoder{err: ErrDecoderUnavailable}); err != nil {
		t.Fatal(err)
	}

	_, err := registry.DecodeFrames(transfer.JPEGLSLossless.UID, pixel, obj)
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("DecodeFrames() error = %v, want ErrDecoderUnavailable", err)
	}
	var decodeErr *pixeldata.CodecDecodeError
	if !errors.As(err, &decodeErr) {
		t.Fatalf("DecodeFrames() error = %T %[1]v, want CodecDecodeError", err)
	}
	if decodeErr.TransferSyntaxUID != transfer.JPEGLSLossless.UID || decodeErr.TransferSyntaxName == "" {
		t.Fatalf("transfer syntax context = %#v, want JPEG-LS lossless UID/name", decodeErr)
	}
}

func TestDecodeRejectsMalformedFrameWithTypedError(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("malformed"))

	_, err := NewLossless(&fakeDecoder{err: errors.New("bad marker")}).Decode(pixel, obj)
	if !errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("Decode() error = %v, want ErrMalformedFrame", err)
	}
	if !strings.Contains(err.Error(), "bad marker") {
		t.Fatalf("Decode() error = %q, want decoder context", err)
	}
	if !strings.Contains(err.Error(), "frame 0") {
		t.Fatalf("Decode() error = %q, want frame context", err)
	}
}

func TestDecodeRejectsUnsupportedMetadata(t *testing.T) {
	tests := []struct {
		name          string
		opts          jpeglsMetadataOptions
		want          error
		wantSubstring string
	}{
		{
			name:          "bits allocated",
			opts:          jpeglsMetadataOptions{bitsAllocated: 12},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "BitsAllocated=12",
		},
		{
			name:          "samples per pixel",
			opts:          jpeglsMetadataOptions{samplesPerPixel: 2},
			want:          ErrUnsupportedMetadata,
			wantSubstring: "SamplesPerPixel=2",
		},
		{
			name:          "pixel representation",
			opts:          jpeglsMetadataOptions{pixelRepresentation: 2},
			want:          pixeldata.ErrUnsupportedPixelRepresentation,
			wantSubstring: "PixelRepresentation=2",
		},
		{
			name: "planar configuration",
			opts: jpeglsMetadataOptions{
				samplesPerPixel:     3,
				photometric:         "RGB",
				planarConfiguration: uint16Ptr(1),
			},
			want:          pixeldata.ErrUnsupportedPlanarConfiguration,
			wantSubstring: "PlanarConfiguration=1",
		},
		{
			name:          "photometric interpretation",
			opts:          jpeglsMetadataOptions{photometric: "PALETTE COLOR"},
			want:          pixeldata.ErrUnsupportedPhotometricInterpretation,
			wantSubstring: "PhotometricInterpretation=PALETTE COLOR",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			obj, pixel := jpeglsObject(t, tt.opts, []byte("encoded"))
			_, err := NewLossless(&fakeDecoder{outputs: [][]byte{{1, 2}}}).Decode(pixel, obj)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Decode() error = %v, want %v", err, tt.want)
			}
			if !strings.Contains(err.Error(), tt.wantSubstring) {
				t.Fatalf("Decode() error = %q, want substring %q", err, tt.wantSubstring)
			}
		})
	}
}

func TestDecodeRejectsUnsupportedFragmentLayout(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{numberOfFrames: 2}, []byte("single-fragment"))

	_, err := NewLossless(&fakeDecoder{outputs: [][]byte{{1, 2}}}).Decode(pixel, obj)
	if !errors.Is(err, ErrUnsupportedFragmentLayout) {
		t.Fatalf("Decode() error = %v, want ErrUnsupportedFragmentLayout", err)
	}
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestDecodeRejectsDecodedFrameSizeMismatch(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))

	_, err := NewLossless(&fakeDecoder{outputs: [][]byte{{1}}}).Decode(pixel, obj)
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

type fakeDecoder struct {
	outputs   [][]byte
	err       error
	inputs    []DecoderInput
	fragments [][]byte
}

func (d *fakeDecoder) DecodeJPEGLS(fragment []byte, input DecoderInput) ([]byte, error) {
	d.inputs = append(d.inputs, input)
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

type jpeglsMetadataOptions struct {
	rows                uint16
	columns             uint16
	samplesPerPixel     uint16
	bitsAllocated       uint16
	pixelRepresentation uint16
	photometric         string
	numberOfFrames      int
	planarConfiguration *uint16
}

func jpeglsObject(t *testing.T, opts jpeglsMetadataOptions, fragments ...[]byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj := object.FromElements(append(jpeglsMetadataElements(opts), fragmentElement(fragments...)), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func jpeglsMetadataElements(opts jpeglsMetadataOptions) []core.Element {
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
		uint16Element(tagBitsStored, bitsAllocated),
		uint16Element(tagHighBit, bitsAllocated-1),
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
