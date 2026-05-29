//go:build jpegxl_djxl || codecfull

package jpegxladapter

import (
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestDjxlDecoderResolvesEnvironmentExecutableFirst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable mode assertion does not apply on Windows")
	}
	path := writeExecutable(t, "custom-djxl", "#!/bin/sh\nexit 0\n")
	t.Setenv("DICOM_GO_DJXL", path)

	got, err := newDjxlDecoder().resolveExecutable()
	if err != nil {
		t.Fatal(err)
	}
	if got != path {
		t.Fatalf("resolveExecutable() = %q, want environment executable %q", got, path)
	}
}

func TestValidateDjxlVersionOutput(t *testing.T) {
	if err := validateDjxlVersionOutput("djxl v0.11.2 build"); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{
		"Microsoft Windows command processor",
		"djxl v0.11.1 build",
		"djxl v0.11.20 build",
		"not-djxl v0.11.2 build",
		"prefix djxl v0.11.2",
	} {
		if err := validateDjxlVersionOutput(output); !errors.Is(err, ErrDjxlUnavailable) {
			t.Fatalf("validateDjxlVersionOutput(%q) error = %v, want ErrDjxlUnavailable", output, err)
		}
	}
}

func TestDjxlDecoderReportsNonExecutableDependencyUnavailable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX executable mode assertion does not apply on Windows")
	}
	path := filepath.Join(t.TempDir(), "djxl")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DICOM_GO_DJXL", path)

	_, err := newDjxlDecoder().DecodeFrame([]byte("encoded"), grayMetadata())
	if !errors.Is(err, ErrDjxlUnavailable) {
		t.Fatalf("DecodeFrame() error = %v, want ErrDjxlUnavailable", err)
	}
}

func TestDjxlDecoderRunsConfiguredExecutableAndParsesPGM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script probe is POSIX-specific")
	}
	path := writeExecutable(t, "fake-djxl", `#!/bin/sh
out="${2}"
printf 'P5\n2 1\n255\n\x00\xff' > "$out"
`)

	frame, err := NewDjxlDecoder(DjxlExecutable(path)).DecodeFrame([]byte("encoded"), grayMetadata())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(frame, []byte{0, 255}) {
		t.Fatalf("DecodeFrame() = %v, want [0 255]", frame)
	}
}

func TestDjxlDecoderTimesOutExternalProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script timeout probe is POSIX-specific")
	}
	path := writeExecutable(t, "slow-djxl", "#!/bin/sh\nwhile :; do :; done\n")

	_, err := NewDjxlDecoder(DjxlExecutable(path), DjxlTimeout(50*time.Millisecond)).DecodeFrame([]byte("encoded"), grayMetadata())
	if !errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("DecodeFrame() error = %v, want ErrMalformedCodestream timeout", err)
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("DecodeFrame() error = %q, want timeout detail", err)
	}
}

func TestPPMToFrameBytesConvertsBigEndian16BitToDICOMLittleEndian(t *testing.T) {
	var input []byte
	input = append(input, []byte("P5\n2 1\n65535\n")...)
	input = binary.BigEndian.AppendUint16(input, 0x1234)
	input = binary.BigEndian.AppendUint16(input, 0xabcd)

	got, err := ppmToFrameBytes(input, pixeldata.Metadata{
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
	want := []byte{0x34, 0x12, 0xcd, 0xab}
	if !slices.Equal(got, want) {
		t.Fatalf("ppmToFrameBytes() = %v, want %v", got, want)
	}
}

func TestPPMToFrameBytesRejectsImageSizeMismatch(t *testing.T) {
	_, err := ppmToFrameBytes([]byte("P5\n3 1\n255\n\x00\x01\x02"), grayMetadata())
	if !errors.Is(err, ErrImageSizeMismatch) {
		t.Fatalf("ppmToFrameBytes() error = %v, want ErrImageSizeMismatch", err)
	}
}

func TestPPMToFrameBytesPreservesLowerPrecisionIn16BitContainer(t *testing.T) {
	got, err := ppmToFrameBytes([]byte("P5\n2 1\n255\n\x00\xff"), pixeldata.Metadata{
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
	want := []byte{0, 0, 255, 0}
	if !slices.Equal(got, want) {
		t.Fatalf("ppmToFrameBytes() = %v, want %v", got, want)
	}
}

func TestPPMToFrameBytesPreservesWiderPrecisionFor12BitData(t *testing.T) {
	var input []byte
	input = append(input, []byte("P5\n2 1\n65535\n")...)
	input = binary.BigEndian.AppendUint16(input, 0)
	input = binary.BigEndian.AppendUint16(input, 4095)

	got, err := ppmToFrameBytes(input, pixeldata.Metadata{
		Rows:                      1,
		Columns:                   2,
		SamplesPerPixel:           1,
		BitsAllocated:             16,
		BitsStored:                12,
		HighBit:                   11,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 0xff, 0x0f}
	if !slices.Equal(got, want) {
		t.Fatalf("ppmToFrameBytes() = %v, want %v", got, want)
	}
}

func grayMetadata() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                      1,
		Columns:                   2,
		SamplesPerPixel:           1,
		BitsAllocated:             8,
		BitsStored:                8,
		HighBit:                   7,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	}
}

func writeExecutable(t testing.TB, name string, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}
