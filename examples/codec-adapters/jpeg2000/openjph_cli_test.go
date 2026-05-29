//go:build codecfull

package jpeg2000

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestValidateOpenJPHBinaryRequiresQualificationMarker(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ojph_expand")
	if err := os.WriteFile(path, []byte("stripped OpenJPH executable"), 0o700); err != nil {
		t.Fatal(err)
	}
	markerPath := path + QualifiedOpenJPHMarkerSuffix
	if err := os.WriteFile(markerPath, []byte(QualifiedOpenJPHMarker+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenJPHBinary(path); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, []byte("OpenJPH 0.30.0\ncommit c1c2c3\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenJPHBinary(path); !errors.Is(err, ErrOpenJPHUnavailable) {
		t.Fatalf("wrong version error = %v, want ErrOpenJPHUnavailable", err)
	}
	if err := os.Remove(markerPath); err != nil {
		t.Fatal(err)
	}
	if err := validateOpenJPHBinary(path); !errors.Is(err, ErrOpenJPHUnavailable) {
		t.Fatalf("missing marker error = %v, want ErrOpenJPHUnavailable", err)
	}
}

func TestOpenJPHPNMRejectsSignedAndPrecisionMismatch(t *testing.T) {
	metadata := pixeldata.Metadata{
		Rows:                      1,
		Columns:                   1,
		SamplesPerPixel:           1,
		BitsAllocated:             8,
		BitsStored:                8,
		HighBit:                   7,
		NumberOfFrames:            1,
		PhotometricInterpretation: "MONOCHROME2",
	}
	signed := metadata
	signed.PixelRepresentation = 1
	if _, err := (openJPHDecoder{}).DecodeFrame([]byte{1}, signed); !errors.Is(err, ErrUnsupportedMetadata) {
		t.Fatalf("signed error = %v, want ErrUnsupportedMetadata", err)
	}

	if _, err := openJPHPNMToFrame([]byte("P5\n1 1\n65535\n\x00\x01"), metadata); !errors.Is(err, ErrUnsupportedMetadata) {
		t.Fatalf("precision mismatch error = %v, want ErrUnsupportedMetadata", err)
	}

	unsupportedAllocation := metadata
	unsupportedAllocation.BitsAllocated = 12
	unsupportedAllocation.BitsStored = 12
	unsupportedAllocation.HighBit = 11
	if _, err := openJPHPNMToFrame([]byte("P5\n1 1\n4095\n\x00\x01"), unsupportedAllocation); !errors.Is(err, ErrUnsupportedMetadata) {
		t.Fatalf("BitsAllocated error = %v, want ErrUnsupportedMetadata", err)
	}
}
