package rle

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

var (
	tagRows                      = core.NewTag(0x0028, 0x0010)
	tagColumns                   = core.NewTag(0x0028, 0x0011)
	tagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	tagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	tagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	tagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	tagBitsStored                = core.NewTag(0x0028, 0x0101)
	tagHighBit                   = core.NewTag(0x0028, 0x0102)
	tagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
)

func TestParseHeaderValidSegmentCounts(t *testing.T) {
	for _, count := range []int{1, 2, 3, 6} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			segments := make([][]byte, count)
			for i := range segments {
				segments[i] = []byte{byte(i)}
			}

			offsets, err := parseHeader(rleFragmentEncoded(segments...))
			if err != nil {
				t.Fatal(err)
			}
			if len(offsets) != count+1 {
				t.Fatalf("offset count = %d, want %d", len(offsets), count+1)
			}
			if offsets[0] != 64 {
				t.Fatalf("first offset = %d, want 64", offsets[0])
			}
		})
	}
}

func TestParseHeaderValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "short", data: make([]byte, 63), want: ErrInvalidHeader},
		{name: "zero segments", data: rleFragment(), want: ErrInvalidSegmentCount},
		{name: "too many segments", data: rleHeaderOnly(16, 64), want: ErrInvalidSegmentCount},
		{name: "offset before header", data: rleHeaderOnly(1, 63), want: ErrInvalidSegmentOffset},
		{name: "offset out of bounds", data: rleHeaderOnly(1, 65), want: ErrInvalidSegmentOffset},
		{name: "offsets decrease", data: rleHeaderOnly(2, 64, 63), want: ErrInvalidSegmentOffset},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseHeader(tt.data)
			if !errors.Is(err, tt.want) {
				t.Fatalf("parseHeader() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecodePackBits(t *testing.T) {
	tests := []struct {
		name    string
		segment []byte
		want    []byte
	}{
		{name: "literal", segment: []byte{0x02, 0x10, 0x20, 0x30}, want: []byte{0x10, 0x20, 0x30}},
		{name: "repeat", segment: []byte{0xFE, 0xAA}, want: []byte{0xAA, 0xAA, 0xAA}},
		{name: "empty", segment: nil, want: nil},
		{name: "noop", segment: []byte{0x80, 0x00, 0x7F}, want: []byte{0x7F}},
		{
			name: "mixed ops reference vector",
			segment: []byte{
				0xFE, 0xAA, 0x02, 0x80, 0x00, 0x2A, 0xFD, 0xAA, 0x03, 0x80, 0x00, 0x2A, 0x22, 0xF7,
				0xAA,
			},
			want: []byte{
				0xAA, 0xAA, 0xAA, 0x80, 0x00, 0x2A, 0xAA, 0xAA, 0xAA, 0xAA, 0x80, 0x00, 0x2A, 0x22,
				0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA, 0xAA,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := decodePackBits(tt.segment)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, tt.want) {
				t.Fatalf("decodePackBits() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDecodePackBitsMalformedReturnsError(t *testing.T) {
	tests := []struct {
		name    string
		segment []byte
	}{
		{name: "truncated literal", segment: []byte{0x02, 0xAA}},
		{name: "truncated repeat", segment: []byte{0xFF}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := decodePackBits(tt.segment); !errors.Is(err, ErrSegmentDecodeFailed) {
				t.Fatalf("decodePackBits() error = %v, want ErrSegmentDecodeFailed", err)
			}
		})
	}
}

func TestDecodeFragment8BitMonochrome(t *testing.T) {
	got, err := decodeFragment(rleFragment([]byte{1, 2, 3, 4, 5, 6}), 2, 3, 1, 8)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 4, 5, 6}
	if !bytes.Equal(got, want) {
		t.Fatalf("decodeFragment() = %v, want %v", got, want)
	}
}

func TestDecodeFragment16BitMonochrome(t *testing.T) {
	got, err := decodeFragment(rleFragment(
		[]byte{0x01, 0x03, 0xAB, 0x12},
		[]byte{0x02, 0x04, 0xCD, 0x00},
	), 1, 4, 1, 16)
	if err != nil {
		t.Fatal(err)
	}
	want := wordsLE(0x0102, 0x0304, 0xABCD, 0x1200)
	if !bytes.Equal(got, want) {
		t.Fatalf("decodeFragment() = %v, want %v", got, want)
	}
}

func TestDecodeFragmentSegmentCountMismatch(t *testing.T) {
	_, err := decodeFragment(rleFragment([]byte{1, 2}), 1, 2, 1, 16)
	if !errors.Is(err, ErrInvalidSegmentCount) {
		t.Fatalf("decodeFragment() error = %v, want ErrInvalidSegmentCount", err)
	}
}

func TestDecodeFramesRLE4x4_8BitGrayscale(t *testing.T) {
	wantFrame := []byte{
		0, 1, 2, 3,
		4, 5, 6, 7,
		8, 9, 10, 11,
		12, 13, 14, 15,
	}
	obj, pixel := rleObjectWithFragment(t, 4, 4, 1, 8, "MONOCHROME2", rleFragment(wantFrame))

	frames := decodeWithRegisteredCodec(t, obj, pixel)
	if frames.Rows != 4 || frames.Columns != 4 || !equalFrames(frames.Data, [][]byte{wantFrame}) {
		t.Fatalf("DecodeFrames() = %#v, want rows=4 columns=4 data=%v", frames, wantFrame)
	}
}

func TestDecodeFramesRLE16BitGrayscale(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 4, 1, 16, "MONOCHROME2", rleFragment(
		[]byte{0x01, 0x03, 0xAB, 0x12},
		[]byte{0x02, 0x04, 0xCD, 0x00},
	))

	frames := decodeWithRegisteredCodec(t, obj, pixel)
	want := [][]byte{wordsLE(0x0102, 0x0304, 0xABCD, 0x1200)}
	if !equalFrames(frames.Data, want) {
		t.Fatalf("DecodeFrames() data = %v, want %v", frames.Data, want)
	}
}

func TestDecodeFramesRLEMultiFrame(t *testing.T) {
	first := []byte{
		0, 1, 2, 3,
		4, 5, 6, 7,
		8, 9, 10, 11,
		12, 13, 14, 15,
	}
	second := []byte{
		15, 14, 13, 12,
		11, 10, 9, 8,
		7, 6, 5, 4,
		3, 2, 1, 0,
	}
	obj, pixel := rleObjectWithFragments(t, 4, 4, 1, 8, "MONOCHROME2", rleFragment(first), rleFragment(second))

	frames := decodeWithRegisteredCodec(t, obj, pixel)
	want := [][]byte{first, second}
	if frames.Rows != 4 || frames.Columns != 4 || !equalFrames(frames.Data, want) {
		t.Fatalf("DecodeFrames() = %#v, want rows=4 columns=4 data=%v", frames, want)
	}
}

func TestDecodeFramesRLE8BitRGB(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 2, 3, 8, "RGB", rleFragment(
		[]byte{1, 4},
		[]byte{2, 5},
		[]byte{3, 6},
	))

	frames := decodeWithRegisteredCodec(t, obj, pixel)
	want := [][]byte{{1, 2, 3, 4, 5, 6}}
	if !equalFrames(frames.Data, want) {
		t.Fatalf("DecodeFrames() data = %v, want %v", frames.Data, want)
	}
}

func TestDecodeFramesRLE16BitRGB(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 2, 3, 16, "RGB", rleFragment(
		[]byte{0x01, 0x07},
		[]byte{0x02, 0x08},
		[]byte{0x03, 0x09},
		[]byte{0x04, 0x0A},
		[]byte{0x05, 0x0B},
		[]byte{0x06, 0x0C},
	))

	frames := decodeWithRegisteredCodec(t, obj, pixel)
	want := [][]byte{wordsLE(0x0102, 0x0304, 0x0506, 0x0708, 0x090A, 0x0B0C)}
	if !equalFrames(frames.Data, want) {
		t.Fatalf("DecodeFrames() data = %v, want %v", frames.Data, want)
	}
}

func TestDecodeFramesRequiresExplicitRegistration(t *testing.T) {
	prev := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = pixeldata.NewMemoryRegistry()
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = prev
	})

	obj, pixel := rleObjectWithFragment(t, 1, 1, 1, 8, "MONOCHROME2", rleFragment([]byte{0x7F}))
	if _, err := pixeldata.DecodeFrames(UID, pixel, obj); !errors.Is(err, pixeldata.ErrCodecNotFound) {
		t.Fatalf("DecodeFrames() error before registration = %v, want ErrCodecNotFound", err)
	}

	if err := RegisterDefault(); err != nil {
		t.Fatalf("RegisterDefault() error = %v, want nil", err)
	}
	frames, err := pixeldata.DecodeFrames(UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if !equalFrames(frames.Data, [][]byte{{0x7F}}) {
		t.Fatalf("DecodeFrames() data = %v, want [[0x7F]]", frames.Data)
	}
}

func TestDecodeFramesMalformedHeaderReturnsError(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 1, 1, 8, "MONOCHROME2", rleHeaderOnly(1, 65))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("DecodeFrames() panicked for malformed RLE header: %v", recovered)
			}
		}()

		_, err := registry.DecodeFrames(UID, pixel, obj)
		if !errors.Is(err, ErrInvalidSegmentOffset) {
			t.Fatalf("DecodeFrames() error = %v, want ErrInvalidSegmentOffset", err)
		}
	}()
}

func TestDecodeFramesTruncatedFragmentReturnsError(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 2, 1, 8, "MONOCHROME2", rleFragmentEncoded([]byte{0x02, 0xAA}))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	_, err := registry.DecodeFrames(UID, pixel, obj)
	if !errors.Is(err, ErrSegmentDecodeFailed) {
		t.Fatalf("DecodeFrames() error = %v, want ErrSegmentDecodeFailed", err)
	}
}

func TestDecodeFramesUnsupportedBitsAllocated(t *testing.T) {
	obj, pixel := rleObjectWithFragment(t, 1, 1, 1, 12, "MONOCHROME2", rleFragment([]byte{0x00}))
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	_, err := registry.DecodeFrames(UID, pixel, obj)
	if !errors.Is(err, ErrUnsupportedBitsAllocated) {
		t.Fatalf("DecodeFrames() error = %v, want ErrUnsupportedBitsAllocated", err)
	}
}

func TestDecodeRejectsNativePixelData(t *testing.T) {
	obj := object.FromElements(rleMetadataElements(1, 1, 1, 8, "MONOCHROME2", 1), nil)
	_, err := New().Decode(pixeldata.PixelData{Raw: []byte{0x01}}, obj)
	if !errors.Is(err, pixeldata.ErrIncompatiblePixelData) {
		t.Fatalf("Decode() error = %v, want ErrIncompatiblePixelData", err)
	}
}

func TestPixelDataPackageDoesNotImportRLE(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "list", "-deps", "github.com/ThalesMMS/dicom-go/pixeldata")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps ./pixeldata failed: %v\n%s", err, out)
	}
	if strings.Contains(string(out), "github.com/ThalesMMS/dicom-go/pixeldata/rle") {
		t.Fatalf("pixeldata package imports pixeldata/rle:\n%s", out)
	}
}

func decodeWithRegisteredCodec(t *testing.T, obj *object.Object, pixel pixeldata.PixelData) pixeldata.Frames {
	t.Helper()
	prev := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = pixeldata.NewMemoryRegistry()
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = prev
	})

	if err := RegisterDefault(); err != nil {
		t.Fatal(err)
	}
	frames, err := pixeldata.DecodeFrames(UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	return frames
}

func rleObjectWithFragment(t *testing.T, rows, columns, samplesPerPixel, bitsAllocated uint16, photometricInterpretation string, fragment []byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	return rleObjectWithFragments(t, rows, columns, samplesPerPixel, bitsAllocated, photometricInterpretation, fragment)
}

func rleObjectWithFragments(t *testing.T, rows, columns, samplesPerPixel, bitsAllocated uint16, photometricInterpretation string, fragments ...[]byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj := object.FromElements(append(
		rleMetadataElements(rows, columns, samplesPerPixel, bitsAllocated, photometricInterpretation, len(fragments)),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	), nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func rleMetadataElements(rows, columns, samplesPerPixel, bitsAllocated uint16, photometricInterpretation string, numberOfFrames int) []core.Element {
	return []core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, samplesPerPixel),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, photometricInterpretation),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, strconv.Itoa(numberOfFrames)),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, bitsAllocated-1),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, 0),
	}
}

func rleFragment(segments ...[]byte) []byte {
	encodedSegments := make([][]byte, len(segments))
	for i, segment := range segments {
		encodedSegments[i] = packLiteral(segment)
	}
	return rleFragmentEncoded(encodedSegments...)
}

func rleFragmentEncoded(segments ...[]byte) []byte {
	fragment := make([]byte, 64)
	binary.LittleEndian.PutUint32(fragment[:4], uint32(len(segments)))
	offset := uint32(64)
	for i, segment := range segments {
		binary.LittleEndian.PutUint32(fragment[4+i*4:], offset)
		offset += uint32(len(segment))
	}
	for _, segment := range segments {
		fragment = append(fragment, segment...)
	}
	return fragment
}

func rleHeaderOnly(count uint32, offsets ...uint32) []byte {
	header := make([]byte, 64)
	binary.LittleEndian.PutUint32(header[:4], count)
	for i, offset := range offsets {
		binary.LittleEndian.PutUint32(header[4+i*4:], offset)
	}
	return header
}

func packLiteral(data []byte) []byte {
	if len(data) == 0 {
		return nil
	}
	var out []byte
	for len(data) > 0 {
		n := len(data)
		if n > 128 {
			n = 128
		}
		out = append(out, byte(n-1))
		out = append(out, data[:n]...)
		data = data[n:]
	}
	return out
}

func wordsLE(values ...uint16) []byte {
	buf := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(buf[i*2:], value)
	}
	return buf
}

func equalFrames(got, want [][]byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if !bytes.Equal(got[i], want[i]) {
			return false
		}
	}
	return true
}
