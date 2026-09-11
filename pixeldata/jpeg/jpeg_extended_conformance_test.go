package jpeg

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/internal/jpegfixture"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

const process4FixtureBase64 = jpegfixture.Process4Base64

var process4SourceSamples = jpegfixture.SourceSamples()
var process4DjpegReference = jpegfixture.ReferenceSamples()

func TestJPEGExtendedProcess4IndependentFixture(t *testing.T) {
	frames, err := decodeProcess4Fixture(process4Fixture(t), 8, 8, 12)
	if err != nil {
		t.Fatalf("Decode() independent Process 4 fixture error = %v", err)
	}
	assertProcess4Samples(t, frames, process4DjpegReference, 8)
}

func TestJPEGExtendedProcess4DICOMFragmentPadding(t *testing.T) {
	fixture := process4Fixture(t)
	frames, err := decodeProcess4Fixture(append(fixture, 0xa2), 8, 8, 12)
	if err != nil {
		t.Fatalf("Decode() one-byte DICOM fragment padding = %v", err)
	}
	assertProcess4Samples(t, frames, process4DjpegReference, 8)

	if _, err := decodeProcess4Fixture(append(fixture, 0, 0), 8, 8, 12); err == nil {
		t.Fatal("Decode() two trailing padding bytes error = nil")
	}
}

func TestJPEGExtendedProcess4RejectsMalformedHeaders(t *testing.T) {
	fixture := process4Fixture(t)
	tests := []struct {
		name       string
		mutate     func(*testing.T, []byte) []byte
		bitsStored uint16
		rows       uint16
		columns    uint16
	}{
		{
			name: "DQT length below header",
			mutate: func(t *testing.T, data []byte) []byte {
				segment := jpegSegment(t, data, 0xdb)
				binary.BigEndian.PutUint16(data[segment-2:segment], 1)
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "DQT reserved precision",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xdb)] = 0x20
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "DQT zero coefficient",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xdb)+1] = 0
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "DHT symbol count exceeds segment",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xc4)+1] = 0xff
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOF zero height",
			mutate: func(t *testing.T, data []byte) []byte {
				segment := jpegSegment(t, data, 0xc1)
				data[segment+1], data[segment+2] = 0, 0
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOF zero width",
			mutate: func(t *testing.T, data []byte) []byte {
				segment := jpegSegment(t, data, 0xc1)
				data[segment+3], data[segment+4] = 0, 0
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOF unsupported precision",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xc1)] = 13
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOF component count exceeds segment",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xc1)+5] = 2
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOF dimensions disagree with DICOM",
			mutate: func(t *testing.T, data []byte) []byte {
				segment := jpegSegment(t, data, 0xc1)
				binary.BigEndian.PutUint16(data[segment+3:segment+5], 9)
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
		{
			name: "SOS references absent Huffman tables",
			mutate: func(t *testing.T, data []byte) []byte {
				data[jpegSegment(t, data, 0xda)+2] = 0x11
				return data
			},
			bitsStored: 12, rows: 8, columns: 8,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			data := test.mutate(t, append([]byte(nil), fixture...))
			if _, err := decodeProcess4Fixture(data, test.rows, test.columns, test.bitsStored); err == nil {
				t.Fatal("Decode() malformed Process 4 stream error = nil")
			}
		})
	}
}

func TestJPEGExtendedProcess4RejectsSOFDecodedByteLimit(t *testing.T) {
	data := append([]byte(nil), process4Fixture(t)...)
	segment := jpegSegment(t, data, 0xc1)
	binary.BigEndian.PutUint16(data[segment+1:segment+3], 0xffff)
	binary.BigEndian.PutUint16(data[segment+3:segment+5], 0xffff)

	err := (&extendedProcess4Decoder{}).parseSOF1(data[segment:segment+9], pixeldata.Metadata{
		Rows: 0xffff, Columns: 0xffff,
	})
	const want = "JPEG Extended decoded frame exceeds resource limit"
	if err == nil || err.Error() != want {
		t.Fatalf("parseSOF1() error = %q, want %q", err, want)
	}
}

func TestJPEGExtendedProcess4RejectsNonConformantDICOMMetadata(t *testing.T) {
	fixture := process4Fixture(t)
	tests := []struct {
		name                string
		bitsAllocated       uint16
		bitsStored          uint16
		highBit             uint16
		pixelRepresentation uint16
	}{
		{name: "nine bits stored", bitsAllocated: 16, bitsStored: 9, highBit: 8},
		{name: "ten bits stored", bitsAllocated: 16, bitsStored: 10, highBit: 9},
		{name: "eleven bits stored", bitsAllocated: 16, bitsStored: 11, highBit: 10},
		{name: "wrong high bit", bitsAllocated: 16, bitsStored: 12, highBit: 10},
		{name: "signed", bitsAllocated: 16, bitsStored: 12, highBit: 11, pixelRepresentation: 1},
		{name: "twelve allocated", bitsAllocated: 12, bitsStored: 12, highBit: 11},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			obj, pixel := process4Object(t, fixture, 8, 8, test.bitsAllocated, test.bitsStored, test.highBit, test.pixelRepresentation)
			if _, err := New().Decode(pixel, obj); err == nil {
				t.Fatal("Decode() non-conformant DICOM metadata error = nil")
			}
		})
	}
}

func TestJPEGExtendedProcess4SharedCodecConcurrentDecode(t *testing.T) {
	obj, pixel := process4Object(t, process4Fixture(t), 8, 8, 16, 12, 11, 0)
	codec := New()

	const workers = 32
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frames, err := codec.Decode(pixel, obj)
			if err == nil {
				err = validateProcess4Samples(frames, process4DjpegReference, 8)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Decode() = %v", err)
		}
	}
}

func FuzzJPEGExtendedProcess4Decode(f *testing.F) {
	fixture, err := base64.StdEncoding.DecodeString(process4FixtureBase64)
	if err != nil {
		f.Fatal(err)
	}
	f.Add(fixture)
	f.Add([]byte{0xff, 0xd8, 0xff, 0xc1, 0x00, 0x0b, 12, 0, 8, 0, 8, 1, 1, 0x11, 0, 0xff, 0xd9})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1<<20 {
			t.Skip()
		}
		obj, pixel := process4Object(t, data, 8, 8, 16, 12, 11, 0)
		_, _ = New().Decode(pixel, obj)
	})
}

func TestJPEGExtendedProcess4LibjpegTurboInterop(t *testing.T) {
	if os.Getenv("DICOMGO_JPEG12_INTEROP") != "1" {
		t.Skip("set DICOMGO_JPEG12_INTEROP=1 to verify against libjpeg-turbo cjpeg/djpeg")
	}
	cjpeg, err := exec.LookPath("cjpeg")
	if err != nil {
		t.Skipf("cjpeg unavailable: %v", err)
	}
	djpeg, err := exec.LookPath("djpeg")
	if err != nil {
		t.Skipf("djpeg unavailable: %v", err)
	}
	version, err := runJPEGInteropCommand(cjpeg, "-version")
	if err != nil {
		t.Fatalf("cjpeg -version: %v: %s", err, version)
	}
	if !strings.Contains(strings.ToLower(string(version)), "libjpeg-turbo") {
		t.Fatalf("cjpeg is not the audited libjpeg-turbo implementation: %s", version)
	}
	t.Logf("independent implementation: %s", strings.TrimSpace(string(version)))

	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.pgm")
	jpegPath := filepath.Join(dir, "process4.jpg")
	decodedPath := filepath.Join(dir, "reference.pgm")
	if err := os.WriteFile(inputPath, process4PGM(8, 8, process4SourceSamples), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runJPEGInteropCommand(cjpeg, "-precision", "12", "-quality", "95", "-optimize", "-grayscale", "-outfile", jpegPath, inputPath)
	if err != nil {
		t.Fatalf("cjpeg Process 4: %v: %s", err, output)
	}
	output, err = runJPEGInteropCommand(djpeg, "-strict", "-pnm", "-outfile", decodedPath, jpegPath)
	if err != nil {
		t.Fatalf("djpeg Process 4: %v: %s", err, output)
	}
	stream, err := os.ReadFile(jpegPath)
	if err != nil {
		t.Fatal(err)
	}
	referencePGM, err := os.ReadFile(decodedPath)
	if err != nil {
		t.Fatal(err)
	}
	width, height, max, reference, err := parseBinaryPGM(referencePGM)
	if err != nil {
		t.Fatal(err)
	}
	if width != 8 || height != 8 || max != 4095 {
		t.Fatalf("djpeg PGM metadata = %dx%d max=%d, want 8x8 max=4095", width, height, max)
	}
	frames, err := decodeProcess4Fixture(stream, 8, 8, 12)
	if err != nil {
		t.Fatalf("Decode() libjpeg-turbo Process 4 stream = %v", err)
	}
	assertProcess4Samples(t, frames, reference, 8)
}

func TestJPEGExtendedProcess4LibjpegTurboRestartInterop(t *testing.T) {
	if os.Getenv("DICOMGO_JPEG12_INTEROP") != "1" {
		t.Skip("set DICOMGO_JPEG12_INTEROP=1 to verify restart markers against libjpeg-turbo")
	}
	cjpeg, err := exec.LookPath("cjpeg")
	if err != nil {
		t.Skipf("cjpeg unavailable: %v", err)
	}
	djpeg, err := exec.LookPath("djpeg")
	if err != nil {
		t.Skipf("djpeg unavailable: %v", err)
	}
	const width, height = 16, 16
	source := make([]uint16, width*height)
	for i := range source {
		source[i] = uint16((i*37 + (i/width)*211) & 0x0fff)
	}
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.pgm")
	jpegPath := filepath.Join(dir, "restart.jpg")
	decodedPath := filepath.Join(dir, "reference.pgm")
	if err := os.WriteFile(inputPath, process4PGM(width, height, source), 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runJPEGInteropCommand(cjpeg, "-precision", "12", "-quality", "95", "-optimize", "-grayscale", "-restart", "1B", "-outfile", jpegPath, inputPath)
	if err != nil {
		t.Fatalf("cjpeg Process 4 restart stream: %v: %s", err, output)
	}
	stream, err := os.ReadFile(jpegPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(stream, []byte{0xff, 0xdd}) || !bytes.Contains(stream, []byte{0xff, 0xd0}) {
		t.Fatal("cjpeg restart stream lacks DRI/RST0 markers")
	}
	output, err = runJPEGInteropCommand(djpeg, "-strict", "-pnm", "-outfile", decodedPath, jpegPath)
	if err != nil {
		t.Fatalf("djpeg Process 4 restart stream: %v: %s", err, output)
	}
	pgm, err := os.ReadFile(decodedPath)
	if err != nil {
		t.Fatal(err)
	}
	gotWidth, gotHeight, max, reference, err := parseBinaryPGM(pgm)
	if err != nil {
		t.Fatal(err)
	}
	if gotWidth != width || gotHeight != height || max != 4095 {
		t.Fatalf("djpeg restart PGM = %dx%d max=%d", gotWidth, gotHeight, max)
	}
	frames, err := decodeProcess4Fixture(stream, height, width, 12)
	if err != nil {
		t.Fatalf("Decode() libjpeg-turbo restart stream = %v", err)
	}
	assertProcess4GeometryAndSamples(t, frames, width, height, reference, 8)
}

// TestJPEGExtendedProcess4GDCMDataInterop exercises independently authored
// vendor streams without copying third-party fixtures into this repository.
// GDCMData does not publish clear repository-level fixture licensing, so this
// test is deliberately opt-in and consumes a separately obtained checkout.
func TestJPEGExtendedProcess4GDCMDataInterop(t *testing.T) {
	root := os.Getenv("DICOMGO_GDCMDATA_ROOT")
	if root == "" {
		t.Skip("set DICOMGO_GDCMDATA_ROOT to a separately obtained GDCMData checkout")
	}
	djpeg, err := exec.LookPath("djpeg")
	if err != nil {
		t.Skipf("djpeg unavailable: %v", err)
	}
	for _, name := range []string{
		"SIEMENS-12-Jpeg_Process_2_4-Lossy-a.dcm",
		"gdcm-JPEG-Extended.dcm",
	} {
		t.Run(name, func(t *testing.T) {
			input, err := os.Open(filepath.Join(root, name))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = input.Close() }()
			file, err := object.ReadFile(input)
			if err != nil {
				t.Fatal(err)
			}
			if file.TransferSyntax.UID != UIDExtended {
				t.Fatalf("Transfer Syntax = %s, want %s", file.TransferSyntax.UID, UIDExtended)
			}
			pixel, err := pixeldata.Extract(file.Dataset)
			if err != nil {
				t.Fatal(err)
			}
			if len(pixel.Sequence.Fragments) != 1 {
				t.Fatalf("fixture fragments = %d, want one", len(pixel.Sequence.Fragments))
			}
			frames, err := New().Decode(pixel, file.Dataset)
			if err != nil {
				t.Fatalf("Decode() vendor Process 4 fixture = %v", err)
			}

			dir := t.TempDir()
			jpegPath := filepath.Join(dir, "frame.jpg")
			pgmPath := filepath.Join(dir, "reference.pgm")
			if err := os.WriteFile(jpegPath, pixel.Sequence.Fragments[0], 0o600); err != nil {
				t.Fatal(err)
			}
			output, err := runJPEGInteropCommand(djpeg, "-strict", "-pnm", "-outfile", pgmPath, jpegPath)
			if err != nil {
				t.Fatalf("djpeg vendor Process 4 fixture: %v: %s", err, output)
			}
			pgm, err := os.ReadFile(pgmPath)
			if err != nil {
				t.Fatal(err)
			}
			width, height, max, reference, err := parseBinaryPGM(pgm)
			if err != nil {
				t.Fatal(err)
			}
			if width != frames.Columns || height != frames.Rows || max != 4095 {
				t.Fatalf("djpeg PGM = %dx%d max=%d, codec=%dx%d", width, height, max, frames.Columns, frames.Rows)
			}
			if len(frames.Data) != 1 || len(frames.Data[0]) != len(reference)*2 {
				t.Fatalf("codec frame layout = %d frames/%d bytes, want one/%d", len(frames.Data), len(frames.Data[0]), len(reference)*2)
			}
			for i, expected := range reference {
				actual := binary.LittleEndian.Uint16(frames.Data[0][i*2:])
				var delta uint16
				if actual >= expected {
					delta = actual - expected
				} else {
					delta = expected - actual
				}
				if delta > 8 {
					t.Fatalf("sample %d = %d, independent djpeg=%d (delta %d > 8)", i, actual, expected, delta)
				}
			}
		})
	}
}

func TestJPEGExtendedProcess4GDCMDataRejectsNoncanonicalVendorHuffmanTable(t *testing.T) {
	root := os.Getenv("DICOMGO_GDCMDATA_ROOT")
	if root == "" {
		t.Skip("set DICOMGO_GDCMDATA_ROOT to a separately obtained GDCMData checkout")
	}
	djpeg, err := exec.LookPath("djpeg")
	if err != nil {
		t.Skipf("djpeg unavailable: %v", err)
	}
	input, err := os.Open(filepath.Join(root, "PHILIPS_Gyroscan-12-Jpeg_Extended_Process_2_4.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	file, err := object.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New().Decode(pixel, file.Dataset); err == nil {
		t.Fatal("Decode() vendor stream with out-of-range lossy DC symbols error = nil")
	}

	// Remove the single DICOM Item padding byte so the independent failure can
	// only be attributed to the JPEG stream. libjpeg-turbo rejects the DC DHT,
	// which contains the invalid lossy symbols 0x10 and 0x11.
	stream := pixel.Sequence.Fragments[0]
	eoi := bytes.LastIndex(stream, []byte{0xff, 0xd9})
	if eoi < 0 {
		t.Fatal("vendor fixture has no JPEG EOI")
	}
	dir := t.TempDir()
	jpegPath := filepath.Join(dir, "noncanonical.jpg")
	if err := os.WriteFile(jpegPath, stream[:eoi+2], 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := runJPEGInteropCommand(djpeg, "-strict", "-pnm", jpegPath)
	if err == nil || !strings.Contains(string(output), "Bogus Huffman table definition") {
		t.Fatalf("djpeg malformed DHT result = %v: %s", err, output)
	}
}

func TestJPEGExtendedProcess4GDCMDataRejectsLegacyTwelveBitsAllocatedFixture(t *testing.T) {
	root := os.Getenv("DICOMGO_GDCMDATA_ROOT")
	if root == "" {
		t.Skip("set DICOMGO_GDCMDATA_ROOT to a separately obtained GDCMData checkout")
	}
	input, err := os.Open(filepath.Join(root, "DCMTK_JPEGExt_12Bits.dcm"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	file, err := object.ReadFile(input)
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := New().Decode(pixel, file.Dataset); err == nil {
		t.Fatal("Decode() legacy BitsAllocated=12 fixture error = nil")
	}
}

func process4Fixture(t testing.TB) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(process4FixtureBase64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func runJPEGInteropCommand(name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, name, args...).CombinedOutput()
}

func decodeProcess4Fixture(stream []byte, rows, columns, bitsStored uint16) (pixeldata.Frames, error) {
	obj, pixel := process4ObjectNoTest(stream, rows, columns, 16, bitsStored, bitsStored-1, 0)
	return New().Decode(pixel, obj)
}

func process4Object(t testing.TB, stream []byte, rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	obj, pixel := process4ObjectNoTest(stream, rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation)
	if !pixel.Encapsulated {
		t.Fatal("test Process 4 Pixel Data is not encapsulated")
	}
	return obj, pixel
}

func process4ObjectNoTest(stream []byte, rows, columns, bitsAllocated, bitsStored, highBit, pixelRepresentation uint16) (*object.Object, pixeldata.PixelData) {
	obj := object.FromElements([]core.Element{
		dicomtest.Uint16Element(tagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(tagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(tagSamplesPerPixel, core.VRUS, nil, 1),
		dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.NewStringElement(tagNumberOfFrames, core.VRIS, "1"),
		dicomtest.Uint16Element(tagBitsAllocated, core.VRUS, nil, bitsAllocated),
		dicomtest.Uint16Element(tagBitsStored, core.VRUS, nil, bitsStored),
		dicomtest.Uint16Element(tagHighBit, core.VRUS, nil, highBit),
		dicomtest.Uint16Element(tagPixelRepresentation, core.VRUS, nil, pixelRepresentation),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, stream),
	}, nil)
	pixel, _ := pixeldata.Extract(obj)
	return obj, pixel
}

func jpegSegment(t testing.TB, data []byte, marker byte) int {
	t.Helper()
	for offset := 0; offset+3 < len(data); offset++ {
		if data[offset] == 0xff && data[offset+1] == marker {
			length := int(binary.BigEndian.Uint16(data[offset+2 : offset+4]))
			if length < 2 || offset+2+length > len(data) {
				t.Fatalf("JPEG marker ff%02x has invalid length %d", marker, length)
			}
			return offset + 4
		}
	}
	t.Fatalf("JPEG marker ff%02x not found", marker)
	return 0
}

func assertProcess4Samples(t testing.TB, frames pixeldata.Frames, want []uint16, tolerance uint16) {
	t.Helper()
	if err := validateProcess4Samples(frames, want, tolerance); err != nil {
		t.Fatal(err)
	}
}

func validateProcess4Samples(frames pixeldata.Frames, want []uint16, tolerance uint16) error {
	return validateProcess4GeometryAndSamples(frames, 8, 8, want, tolerance)
}

func assertProcess4GeometryAndSamples(t testing.TB, frames pixeldata.Frames, width, height int, want []uint16, tolerance uint16) {
	t.Helper()
	if err := validateProcess4GeometryAndSamples(frames, width, height, want, tolerance); err != nil {
		t.Fatal(err)
	}
}

func validateProcess4GeometryAndSamples(frames pixeldata.Frames, width, height int, want []uint16, tolerance uint16) error {
	if frames.Rows != height || frames.Columns != width || len(frames.Data) != 1 {
		return fmt.Errorf("Decode() geometry = %dx%d frames=%d, want %dx%d frames=1", frames.Columns, frames.Rows, len(frames.Data), width, height)
	}
	if len(frames.Data[0]) != len(want)*2 {
		return fmt.Errorf("Decode() frame bytes = %d, want %d", len(frames.Data[0]), len(want)*2)
	}
	for i, expected := range want {
		actual := binary.LittleEndian.Uint16(frames.Data[0][i*2:])
		var delta uint16
		if actual >= expected {
			delta = actual - expected
		} else {
			delta = expected - actual
		}
		if delta > tolerance {
			return fmt.Errorf("Decode() sample %d = %d, independent djpeg=%d (delta %d > %d)", i, actual, expected, delta, tolerance)
		}
	}
	return nil
}

func process4PGM(width, height int, samples []uint16) []byte {
	var out bytes.Buffer
	fmt.Fprintf(&out, "P5\n%d %d\n4095\n", width, height)
	for _, sample := range samples {
		var encoded [2]byte
		binary.BigEndian.PutUint16(encoded[:], sample)
		out.Write(encoded[:])
	}
	return out.Bytes()
}

func parseBinaryPGM(data []byte) (width, height, max int, samples []uint16, err error) {
	offset := 0
	nextToken := func() (string, error) {
		for offset < len(data) {
			if data[offset] == '#' {
				for offset < len(data) && data[offset] != '\n' {
					offset++
				}
				continue
			}
			if data[offset] > ' ' {
				break
			}
			offset++
		}
		start := offset
		for offset < len(data) && data[offset] > ' ' {
			offset++
		}
		if start == offset {
			return "", fmt.Errorf("truncated PGM header")
		}
		return string(data[start:offset]), nil
	}
	magic, err := nextToken()
	if err != nil || magic != "P5" {
		return 0, 0, 0, nil, fmt.Errorf("PGM magic = %q, want P5", magic)
	}
	values := make([]int, 3)
	for i := range values {
		token, tokenErr := nextToken()
		if tokenErr != nil {
			return 0, 0, 0, nil, tokenErr
		}
		values[i], err = strconv.Atoi(token)
		if err != nil {
			return 0, 0, 0, nil, fmt.Errorf("invalid PGM header token %q: %w", token, err)
		}
	}
	width, height, max = values[0], values[1], values[2]
	if offset >= len(data) || data[offset] > ' ' {
		return 0, 0, 0, nil, fmt.Errorf("PGM header lacks raster separator")
	}
	offset++
	if width <= 0 || height <= 0 || max <= 255 || max > 65535 || width > int(^uint(0)>>1)/height {
		return 0, 0, 0, nil, fmt.Errorf("unsupported PGM dimensions/max: %dx%d max=%d", width, height, max)
	}
	pixelCount := width * height
	if len(data)-offset != pixelCount*2 {
		return 0, 0, 0, nil, fmt.Errorf("PGM raster bytes = %d, want %d", len(data)-offset, pixelCount*2)
	}
	samples = make([]uint16, pixelCount)
	for i := range samples {
		samples[i] = binary.BigEndian.Uint16(data[offset+i*2:])
	}
	return width, height, max, samples, nil
}
