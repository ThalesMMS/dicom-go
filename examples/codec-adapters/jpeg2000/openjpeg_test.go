//go:build jpeg2000_openjpeg || codecfull

package jpeg2000

import (
	"bytes"
	"errors"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
)

func TestValidateOpenJPEGVersionOutput(t *testing.T) {
	if err := validateOpenJPEGVersionOutput("compiled against openjp2 library v2.5.4."); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenJPEGVersionOutput("Microsoft Windows command processor"); !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("wrong product error = %v, want ErrOpenJPEGUnavailable", err)
	}
	if err := validateOpenJPEGVersionOutput("compiled against openjp2 library v2.5.3."); !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("wrong version error = %v, want ErrOpenJPEGUnavailable", err)
	}
}

func TestOpenJPEGPNMConversionPreservesCodestreamPrecisionWithinAllocation(t *testing.T) {
	got, err := pnmToFrameBytes([]byte("P5\n2 1\n255\n\x00\xff"), pixeldata.Metadata{
		Rows:                      1,
		Columns:                   2,
		SamplesPerPixel:           1,
		BitsAllocated:             16,
		BitsStored:                16,
		HighBit:                   15,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, []byte{0, 0, 255, 0}) {
		t.Fatalf("pnmToFrameBytes() = %v, want preserved 8-bit samples in 16-bit allocation", got)
	}
}

func TestOpenJPEGPNMConversionRejectsInvalidPackingMetadata(t *testing.T) {
	tests := []struct {
		name string
		pnm  []byte
		meta pixeldata.Metadata
		want string
	}{
		{
			name: "zero bits allocated",
			pnm:  []byte("P5\n1 1\n255\n\x00"),
			meta: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsStored: 8},
			want: "BitsAllocated=0",
		},
		{
			name: "too many bits allocated",
			pnm:  []byte("P5\n1 1\n255\n\x00"),
			meta: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 32, BitsStored: 8},
			want: "BitsAllocated=32",
		},
		{
			name: "bits stored exceeds allocated",
			pnm:  []byte("P5\n1 1\n65535\n\x00\x01"),
			meta: pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, BitsAllocated: 8, BitsStored: 16},
			want: "BitsStored=16 BitsAllocated=8",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := pnmToFrameBytes(tt.pnm, tt.meta)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("pnmToFrameBytes() error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestOpenJPEGDecoderReportsNonExecutableDependencyUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable mode assertion does not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "opj_decompress")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte{0xff, 0x4f})

	_, err := NewOpenJPEGCodec(OpenJPEGExecutable(path)).Decode(pixel, obj)
	if !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("Decode() error = %v, want ErrOpenJPEGUnavailable", err)
	}
}

func TestOpenJPEGDecoderTimesOutExternalProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script timeout probe is POSIX-specific")
	}
	path := filepath.Join(t.TempDir(), "slow-opj-decompress")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nwhile :; do :; done\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte{0xff, 0x4f})

	_, err := NewOpenJPEGCodec(OpenJPEGExecutable(path), OpenJPEGTimeout(50*time.Millisecond)).Decode(pixel, obj)
	if !errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("Decode() error = %v, want ErrMalformedCodestream timeout", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("Decode() error = %q, want timeout detail", err)
	}
}

func TestOpenJPEGDecoderRejectsOversizedOutputBeforeRead(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script output probe is POSIX-specific")
	}
	path := filepath.Join(t.TempDir(), "oversized-opj-decompress")
	script := `#!/bin/sh
out=""
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    shift
    out="$1"
  fi
  shift
done
{ printf 'P5\n2 1\n255\n'; dd if=/dev/zero bs=1 count=5000 2>/dev/null; } > "$out"
`
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte{0xff, 0x4f})

	_, err := NewOpenJPEGCodec(OpenJPEGExecutable(path)).Decode(pixel, obj)
	if !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("Decode() error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

func TestOpenJPEGDecoderRunsSharedFixtureHarness(t *testing.T) {
	if _, err := exec.LookPath("opj_decompress"); err != nil {
		t.Skipf("opj_decompress not found in PATH: %v", err)
	}
	if _, err := exec.LookPath("opj_compress"); err != nil {
		t.Skipf("opj_compress not found in PATH: %v", err)
	}

	img := image.NewGray(image.Rect(0, 0, 2, 2))
	copy(img.Pix, []byte{0, 64, 128, 255})
	cases := []codecfixture.Case{
		codecfixture.JPEG2000Lossless(2, 2, encodeOpenJPEGPGM(t, img.Pix, false), []byte{0, 64, 128, 255}),
		codecfixture.JPEG2000Lossy(2, 2, encodeOpenJPEGPGM(t, img.Pix, true), []byte{0, 64, 128, 255}, 64),
	}

	pureGoRegistry := pixeldata.NewMemoryRegistry()
	if err := Register(pureGoRegistry); err != nil {
		t.Fatal(err)
	}
	openJPEGRegistry := pixeldata.NewMemoryRegistry()
	if err := RegisterOpenJPEG(openJPEGRegistry); err != nil {
		t.Fatal(err)
	}

	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			if err := codecfixture.ValidateCase(openJPEGRegistry, tc); err != nil {
				t.Fatalf("OpenJPEG ValidateCase() error = %v", err)
			}

			pureGoResult := codecfixture.RunCase(pureGoRegistry, tc)
			openJPEGResult := codecfixture.RunCase(openJPEGRegistry, tc)
			if openJPEGResult.Err != nil {
				t.Fatalf("OpenJPEG RunCase() error = %v", openJPEGResult.Err)
			}
			if pureGoResult.Err == nil &&
				len(pureGoResult.Frames.Data) == 1 &&
				bytesWithinTolerance(pureGoResult.Frames.Data[0], tc.ExpectedFrames[0], maxByteTolerance(1, tc.Tolerance)) {
				t.Fatalf("pure-Go unexpectedly matched OpenJPEG-generated fixture; reassess fallback necessity")
			}
		})
	}
}

func encodeOpenJPEGPGM(t testing.TB, pixels []byte, lossy bool) []byte {
	t.Helper()
	dir := t.TempDir()
	inputPath := filepath.Join(dir, "input.pgm")
	outputPath := filepath.Join(dir, "output.j2k")
	var input bytes.Buffer
	input.WriteString("P5\n2 2\n255\n")
	input.Write(pixels)
	if err := os.WriteFile(inputPath, input.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-i", inputPath, "-o", outputPath, "-n", "1"}
	if lossy {
		args = append(args, "-r", "20")
	}
	output, err := exec.Command("opj_compress", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("opj_compress failed: %v: %s", err, bytes.TrimSpace(output))
	}
	encoded, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func maxByteTolerance(a byte, b byte) byte {
	if a > b {
		return a
	}
	return b
}
