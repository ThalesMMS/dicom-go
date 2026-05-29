//go:build codecfull

package codecfull

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	dicomframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	codecfullGrayWidth   = 64
	codecfullGrayHeight  = 64
	codecfullColorWidth  = 64
	codecfullColorHeight = 48
)

func TestIndependentLosslessAndLossyPairs(t *testing.T) {
	registry := mustRegistry(t)
	tests := []struct {
		name       string
		compressed string
		reference  string
		tolerance  int
	}{
		{"jpeg2000-signed-ct", "693_J2KR.dcm", "693_UNCR.dcm", 0},
		{"jpeg2000-multiframe-mr", "emri_small_jpeg_2k_lossless.dcm", "emri_small.dcm", 0},
		{"jpeg2000-lossy-color-us", "US1_J2KI.dcm", "US1_UNCI.dcm", 1},
		{"jpegls-multiframe-mr", "emri_small_jpeg_ls_lossless.dcm", "emri_small.dcm", 0},
		{"jpegls-signed-mr", "MR_small_jpeg_ls_lossless.dcm", "MR_small.dcm", 0},
		{"rle-multiframe-mr", "emri_small_RLE.dcm", "emri_small.dcm", 0},
		{"rle-signed-mr", "MR_small_RLE.dcm", "MR_small.dcm", 0},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compressed, compressedMetadata := decodeDICOM(t, registry, pydicomFixture(test.compressed))
			reference, referenceMetadata := decodeDICOM(t, registry, pydicomFixture(test.reference))
			if compressedMetadata.Rows != referenceMetadata.Rows ||
				compressedMetadata.Columns != referenceMetadata.Columns ||
				compressedMetadata.SamplesPerPixel != referenceMetadata.SamplesPerPixel {
				t.Fatalf("metadata mismatch: compressed=%+v reference=%+v", compressedMetadata, referenceMetadata)
			}
			if got := framesMaxAbsoluteError(compressed, reference, referenceMetadata.BitsAllocated); got > test.tolerance {
				t.Fatalf("maximum absolute pixel error = %d, want <= %d", got, test.tolerance)
			}
		})
	}
}

func TestYBRJPEG2000ExtractFramesRendersRGB(t *testing.T) {
	registry := mustRegistry(t)
	previousRegistry := pixeldata.DefaultRegistry
	pixeldata.DefaultRegistry = registry
	t.Cleanup(func() {
		pixeldata.DefaultRegistry = previousRegistry
	})

	compressed := readDICOMFile(t, pydicomFixture("US1_J2KI.dcm"))
	reference := readDICOMFile(t, pydicomFixture("US1_UNCI.dcm"))
	compressedFrames, err := dicomframe.ExtractFrames(compressed)
	if err != nil {
		t.Fatalf("extract JPEG 2000 frames: %v", err)
	}
	referenceFrames, err := dicomframe.ExtractFrames(reference)
	if err != nil {
		t.Fatalf("extract reference frames: %v", err)
	}
	if len(compressedFrames) == 0 || len(referenceFrames) == 0 {
		t.Fatalf("empty frame set: compressed=%d reference=%d", len(compressedFrames), len(referenceFrames))
	}
	decoded, err := compressedFrames[0].GetEncapsulatedFrame()
	if err != nil {
		t.Fatal(err)
	}
	if got := decoded.Metadata.PhotometricInterpretation; got != "RGB" {
		t.Fatalf("decoded photometric interpretation = %q, want RGB", got)
	}
	compressedImage, err := compressedFrames[0].GetImage()
	if err != nil {
		t.Fatalf("render JPEG 2000 frame: %v", err)
	}
	referenceImage, err := referenceFrames[0].GetImage()
	if err != nil {
		t.Fatalf("render reference frame: %v", err)
	}
	if compressedImage.Bounds() != referenceImage.Bounds() {
		t.Fatalf("image bounds mismatch: compressed=%v reference=%v", compressedImage.Bounds(), referenceImage.Bounds())
	}
	if got := imagesMaxAbsoluteError(compressedImage, referenceImage); got > 1 {
		t.Fatalf("rendered maximum absolute channel error = %d, want <= 1", got)
	}
}

func TestIndependentReferencePoints(t *testing.T) {
	registry := mustRegistry(t)

	t.Run("jpeg-lossless", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("JPGLosslessP14SV1_1s_1f_8b.dcm"))
		assertSamples(t, frames, metadata, 0, 300, 512, 0, []int{26, 26, 25, 22, 19, 16, 14, 15})
		assertSamples(t, frames, metadata, 0, 600, 512, 0, []int{45, 43, 41, 38, 33, 30, 26, 21})
	})

	t.Run("jpegls-near-lossless-8", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("JPEGLSNearLossless_08.dcm"))
		for row, want := range map[int]int{0: 255, 5: 125, 10: 65, 15: 30, 20: 15, 25: 5, 30: 5, 35: 0, 40: 0} {
			assertSamples(t, frames, metadata, 0, row, 0, 0, []int{want})
		}
	})

	t.Run("jpegls-near-lossless-16", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("JPEGLSNearLossless_16.dcm"))
		for row, want := range map[int]int{0: 65535, 5: 32765, 10: 16385, 15: 4095, 20: 1025, 25: 255, 30: 65, 35: 15, 40: 5} {
			assertSamples(t, frames, metadata, 0, row, 0, 0, []int{want})
		}
	})

	t.Run("rle-16bit-rgb-multiframe", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("SC_rgb_rle_16bit_2frame.dcm"))
		assertSamples(t, frames, metadata, 0, 5, 50, 0, []int{65535, 0, 0})
		assertSamples(t, frames, metadata, 0, 75, 50, 0, []int{16448, 16448, 16448})
		assertSamples(t, frames, metadata, 1, 5, 50, 0, []int{0, 65535, 65535})
		assertSamples(t, frames, metadata, 1, 75, 50, 0, []int{49087, 49087, 49087})
	})

	t.Run("htj2k-lossless-rgb", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("HTJ2KLossless_08_RGB.dcm"))
		assertSamples(t, frames, metadata, 0, 160, 295, 0, []int{90, 38, 1, 94, 40, 1, 97, 42, 5})
		assertSamples(t, frames, metadata, 0, 275, 635, 0, []int{208, 193, 172})
	})

	t.Run("htj2k-lossy-rgb", func(t *testing.T) {
		frames, metadata := decodeDICOM(t, registry, pydicomFixture("HTJ2K_08_RGB.dcm"))
		assertSamples(t, frames, metadata, 0, 160, 295, 0, []int{91, 37, 2, 94, 40, 1, 97, 42, 5})
		assertSamples(t, frames, metadata, 0, 275, 635, 0, []int{207, 193, 171})
	})
}

func TestSyntheticQualifiedCoverage(t *testing.T) {
	registry := mustRegistry(t)
	if err := codecfixture.ValidateCase(registry, codecfixture.JPEGBaselineSmall()); err != nil {
		t.Fatalf("JPEG Baseline SOF0: %v", err)
	}
	if err := codecfixture.ValidateCase(registry, codecfixture.JPEGExtendedSmall()); err != nil {
		t.Fatalf("JPEG Extended SOF1: %v", err)
	}

	source := syntheticJ2KGray8()
	for _, test := range []struct {
		name    string
		fixture codecfixture.Case
	}{
		{
			name: "jpeg2000-part2-lossless",
			fixture: codecfixture.JPEG2000Part2Lossless(
				codecfullGrayHeight, codecfullGrayWidth,
				readFixture(t, jpeg2000Fixture("part2-lossless.j2k")),
				source,
			),
		},
		{
			name: "jpeg2000-part2-lossy",
			fixture: codecfixture.JPEG2000Part2Lossy(
				codecfullGrayHeight, codecfullGrayWidth,
				readFixture(t, jpeg2000Fixture("part2-lossy.j2k")),
				source,
				32,
			),
		},
		{
			name: "htj2k-rpcl",
			fixture: codecfixture.HTJ2KLosslessRPCL(
				codecfullGrayHeight, codecfullGrayWidth,
				readFixture(t, jpeg2000Fixture("htj2k-rpcl-lossless.j2c")),
				source,
			),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := codecfixture.ValidateCase(registry, test.fixture); err != nil {
				t.Fatal(err)
			}
		})
	}

	t.Run("encapsulated-uncompressed-multiframe", func(t *testing.T) {
		obj, pixel := testPixelObject(
			t, 1, 2, 1, 8, 8, "MONOCHROME2",
			[][]byte{{1, 2}, {3, 4}},
			2,
		)
		frames, err := registry.DecodeFrames(
			transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID,
			pixel,
			obj,
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(frames.Data) != 2 ||
			!bytes.Equal(frames.Data[0], []byte{1, 2}) ||
			!bytes.Equal(frames.Data[1], []byte{3, 4}) {
			t.Fatalf("frames = %#v, want [[1 2] [3 4]]", frames.Data)
		}
	})
}

func readFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestJPEGXLQualifiedFixtures(t *testing.T) {
	registry := mustRegistry(t)

	t.Run("lossless-gray16", func(t *testing.T) {
		got := decodeJXLFixture(t, registry, "gray16-lossless.jxl", transfer.JPEGXLLossless, codecfullGrayHeight, codecfullGrayWidth, 1, 16, "MONOCHROME2")
		want := syntheticGray16()
		if errorValue := framesMaxAbsoluteError(got, [][]byte{want}, 16); errorValue != 0 {
			t.Fatalf("maximum absolute pixel error = %d, want 0", errorValue)
		}
	})

	t.Run("lossy-rgb8", func(t *testing.T) {
		got := decodeJXLFixture(t, registry, "rgb8-lossy.jxl", transfer.JPEGXL, codecfullColorHeight, codecfullColorWidth, 3, 8, "RGB")
		want := syntheticRGB8()
		if errorValue := framesMaxAbsoluteError(got, [][]byte{want}, 8); errorValue > 80 {
			t.Fatalf("maximum absolute pixel error = %d, want <= 80", errorValue)
		}
	})

	t.Run("jpeg-recompression", func(t *testing.T) {
		got := decodeJXLFixture(t, registry, "jpeg-recompression.jxl", transfer.JPEGXLJPEGRecompression, codecfullColorHeight, codecfullColorWidth, 3, 8, "RGB")
		want := syntheticJPEGReference(t)
		if errorValue := framesMaxAbsoluteError(got, [][]byte{want}, 8); errorValue > 80 {
			t.Fatalf("maximum absolute pixel error = %d, want <= 80 across independent JPEG pixel decoders", errorValue)
		}
	})
}

func TestJPEGXLJPEGRecompressionRestoresOriginalJPEG(t *testing.T) {
	djxl := os.Getenv("DICOM_GO_DJXL")
	if djxl == "" {
		t.Fatal("DICOM_GO_DJXL is required")
	}
	output := filepath.Join(t.TempDir(), "restored.jpg")
	command := exec.Command(djxl, jxlFixture("jpeg-recompression.jxl"), output, "--quiet")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("djxl JPEG reconstruction: %v: %s", err, combined)
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	want := syntheticJPEGBytes(t)
	if !bytes.Equal(got, want) {
		t.Fatalf("reconstructed JPEG differs: got %d bytes, want %d bytes", len(got), len(want))
	}
}

func mustRegistry(t *testing.T) *pixeldata.MemoryRegistry {
	t.Helper()
	registry, err := NewRegistry()
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func decodeDICOM(t *testing.T, registry pixeldata.Registry, path string) ([][]byte, pixeldata.Metadata) {
	t.Helper()
	file := readDICOMFile(t, path)
	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := registry.DecodeFrames(file.TransferSyntax.UID, pixel, file.Dataset)
	if err != nil {
		t.Fatalf("decode %s: %v", filepath.Base(path), err)
	}
	return frames.Data, metadata
}

func readDICOMFile(t *testing.T, path string) *object.File {
	t.Helper()
	reader, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	file, err := object.ReadFile(reader)
	if err != nil {
		t.Fatalf("read %s: %v", filepath.Base(path), err)
	}
	return file
}

func decodeJXLFixture(t *testing.T, registry pixeldata.Registry, name string, syntax transfer.Syntax, rows, columns, samples, bits int, photometric string) [][]byte {
	t.Helper()
	fragment, err := os.ReadFile(jxlFixture(name))
	if err != nil {
		t.Fatal(err)
	}
	obj, pixel := testPixelObject(t, rows, columns, samples, bits, bits, photometric, [][]byte{fragment}, 1)
	frames, err := registry.DecodeFrames(syntax.UID, pixel, obj)
	if err != nil {
		t.Fatalf("decode %s: %v", name, err)
	}
	return frames.Data
}

func testPixelObject(t *testing.T, rows, columns, samples, bitsAllocated, bitsStored int, photometric string, fragments [][]byte, numberOfFrames int) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	uint16Element := func(tag core.Tag, value int) core.Element {
		raw := make([]byte, 2)
		binary.LittleEndian.PutUint16(raw, uint16(value))
		return core.NewRawElement(tag, core.VRUS, raw)
	}
	stringElement := func(tag core.Tag, vr core.VR, value string) core.Element {
		return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
	}
	elements := []core.Element{
		uint16Element(core.NewTag(0x0028, 0x0002), samples),
		stringElement(core.NewTag(0x0028, 0x0004), core.VRCS, photometric),
		stringElement(core.NewTag(0x0028, 0x0008), core.VRIS, fmt.Sprint(numberOfFrames)),
		uint16Element(core.NewTag(0x0028, 0x0010), rows),
		uint16Element(core.NewTag(0x0028, 0x0011), columns),
		uint16Element(core.NewTag(0x0028, 0x0100), bitsAllocated),
		uint16Element(core.NewTag(0x0028, 0x0101), bitsStored),
		uint16Element(core.NewTag(0x0028, 0x0102), bitsStored-1),
		uint16Element(core.NewTag(0x0028, 0x0103), 0),
		{
			Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
			Value:  core.FragmentSequence{Fragments: fragments},
		},
	}
	if samples > 1 {
		elements = append(elements, uint16Element(core.NewTag(0x0028, 0x0006), 0))
	}
	obj := object.FromElements(elements, std.Dictionary)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func framesMaxAbsoluteError(got, want [][]byte, bitsAllocated uint16) int {
	if len(got) != len(want) {
		return math.MaxInt
	}
	maximum := 0
	for frame := range got {
		if len(got[frame]) != len(want[frame]) {
			return math.MaxInt
		}
		step := 1
		if bitsAllocated > 8 {
			step = 2
		}
		for offset := 0; offset < len(got[frame]); offset += step {
			gotValue := int(got[frame][offset])
			wantValue := int(want[frame][offset])
			if step == 2 {
				gotValue = int(binary.LittleEndian.Uint16(got[frame][offset:]))
				wantValue = int(binary.LittleEndian.Uint16(want[frame][offset:]))
			}
			delta := gotValue - wantValue
			if delta < 0 {
				delta = -delta
			}
			if delta > maximum {
				maximum = delta
			}
		}
	}
	return maximum
}

func imagesMaxAbsoluteError(got, want image.Image) int {
	maximum := 0
	bounds := got.Bounds()
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			gotR, gotG, gotB, _ := got.At(x, y).RGBA()
			wantR, wantG, wantB, _ := want.At(x, y).RGBA()
			for _, pair := range [][2]uint32{{gotR, wantR}, {gotG, wantG}, {gotB, wantB}} {
				delta := int(pair[0]>>8) - int(pair[1]>>8)
				if delta < 0 {
					delta = -delta
				}
				if delta > maximum {
					maximum = delta
				}
			}
		}
	}
	return maximum
}

func assertSamples(t *testing.T, frames [][]byte, metadata pixeldata.Metadata, frame, row, column, component int, want []int) {
	t.Helper()
	if frame < 0 || frame >= len(frames) {
		t.Fatalf("frame index %d outside %d frame(s)", frame, len(frames))
	}
	bytesPerSample := int(metadata.BitsAllocated / 8)
	startSample := ((row*int(metadata.Columns)+column)*int(metadata.SamplesPerPixel) + component)
	for index, wantValue := range want {
		offset := (startSample + index) * bytesPerSample
		if offset+bytesPerSample > len(frames[frame]) {
			t.Fatalf("sample offset %d outside %d-byte frame", offset, len(frames[frame]))
		}
		got := int(frames[frame][offset])
		if bytesPerSample == 2 {
			got = int(binary.LittleEndian.Uint16(frames[frame][offset:]))
		}
		if got != wantValue {
			t.Fatalf("sample %d at frame=%d row=%d column=%d component=%d = %d, want %d", index, frame, row, column, component, got, wantValue)
		}
	}
}

func syntheticGray16() []byte {
	out := make([]byte, codecfullGrayWidth*codecfullGrayHeight*2)
	for y := 0; y < codecfullGrayHeight; y++ {
		for x := 0; x < codecfullGrayWidth; x++ {
			value := uint16((x*997 + y*313 + x*y*17) & 0xffff)
			binary.LittleEndian.PutUint16(out[(y*codecfullGrayWidth+x)*2:], value)
		}
	}
	return out
}

func syntheticRGB8() []byte {
	out := make([]byte, codecfullColorWidth*codecfullColorHeight*3)
	for y := 0; y < codecfullColorHeight; y++ {
		for x := 0; x < codecfullColorWidth; x++ {
			offset := (y*codecfullColorWidth + x) * 3
			out[offset] = byte(x*4 + y)
			out[offset+1] = byte(y*5 + x/2)
			out[offset+2] = byte((x*3 + y*7) ^ 0x5a)
		}
	}
	return out
}

func syntheticJ2KGray8() []byte {
	out := make([]byte, codecfullGrayWidth*codecfullGrayHeight)
	for y := 0; y < codecfullGrayHeight; y++ {
		for x := 0; x < codecfullGrayWidth; x++ {
			out[y*codecfullGrayWidth+x] = byte((x*7 + y*13 + x*y*3) & 0xff)
		}
	}
	return out
}

func syntheticJPEGReference(t *testing.T) []byte {
	t.Helper()
	encoded := syntheticJPEGBytes(t)
	decoded, err := jpeg.Decode(bytes.NewReader(encoded))
	if err != nil {
		t.Fatal(err)
	}
	out := make([]byte, codecfullColorWidth*codecfullColorHeight*3)
	for y := 0; y < codecfullColorHeight; y++ {
		for x := 0; x < codecfullColorWidth; x++ {
			r, g, b, _ := decoded.At(x, y).RGBA()
			offset := (y*codecfullColorWidth + x) * 3
			out[offset] = byte(r >> 8)
			out[offset+1] = byte(g >> 8)
			out[offset+2] = byte(b >> 8)
		}
	}
	return out
}

func syntheticJPEGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, codecfullColorWidth, codecfullColorHeight))
	pixels := syntheticRGB8()
	for y := 0; y < codecfullColorHeight; y++ {
		for x := 0; x < codecfullColorWidth; x++ {
			offset := (y*codecfullColorWidth + x) * 3
			img.SetRGBA(x, y, color.RGBA{R: pixels[offset], G: pixels[offset+1], B: pixels[offset+2], A: 255})
		}
	}
	var encoded bytes.Buffer
	if err := jpeg.Encode(&encoded, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return encoded.Bytes()
}

func pydicomFixture(name string) string {
	return filepath.Join("..", "..", "pixeldata", "codecfixture", "testdata", "codecfull", "pydicom", name)
}

func jxlFixture(name string) string {
	return filepath.Join("..", "..", "pixeldata", "codecfixture", "testdata", "codecfull", "jxl", name)
}

func jpeg2000Fixture(name string) string {
	return filepath.Join("..", "..", "pixeldata", "codecfixture", "testdata", "codecfull", "jpeg2000", name)
}
