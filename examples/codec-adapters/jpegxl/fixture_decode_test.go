//go:build jpegxl_djxl || codecfull

package jpegxladapter

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestDecodeJPEGXLFixtureFrames(t *testing.T) {
	decoder := newDjxlDecoder()
	if _, err := decoder.resolveExecutable(); err != nil {
		t.Skipf("djxl runtime unavailable: %v", err)
	}

	manifest := loadJPEGXLFixtureManifest(t)
	root := skipIfJPEGXLFixturesUnavailable(t, manifest)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	for groupName, group := range manifest.Groups {
		if len(group.Files) == 0 {
			continue
		}
		t.Run(groupName, func(t *testing.T) {
			for _, fixture := range sampleJPEGXLFixtureFiles(group) {
				t.Run(fixture.Path, func(t *testing.T) {
					assertDecodeJPEGXLFixture(t, registry, root, fixture)
				})
			}
		})
	}
}

func TestDecodeMalformedJPEGXLPayloadsReturnCodecDecodeError(t *testing.T) {
	decoder := newDjxlDecoder()
	if _, err := decoder.resolveExecutable(); err != nil {
		t.Skipf("djxl runtime unavailable: %v", err)
	}

	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name     string
		fragment []byte
	}{
		{name: "empty", fragment: nil},
		{name: "invalid magic", fragment: []byte("not-a-jxl-codestream")},
		{name: "truncated container marker", fragment: []byte{0xff, 0x0a, 0x20, 0x01}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj, pixel := jpegxlObject(t, jpegxlMetadataOptions{}, tc.fragment)
			_, err := registry.DecodeFrames(transfer.JPEGXL.UID, pixel, obj)
			if !errors.Is(err, ErrMalformedCodestream) {
				t.Fatalf("DecodeFrames() error = %v, want ErrMalformedCodestream", err)
			}
			var decodeErr *pixeldata.CodecDecodeError
			if !errors.As(err, &decodeErr) {
				t.Fatalf("DecodeFrames() error = %T %[1]v, want CodecDecodeError", err)
			}
			if decodeErr.TransferSyntaxUID != transfer.JPEGXL.UID || decodeErr.TransferSyntaxName != transfer.JPEGXL.Name {
				t.Fatalf("CodecDecodeError transfer syntax = %q/%q, want %q/%q",
					decodeErr.TransferSyntaxUID,
					decodeErr.TransferSyntaxName,
					transfer.JPEGXL.UID,
					transfer.JPEGXL.Name,
				)
			}
		})
	}
}

func assertDecodeJPEGXLFixture(t testing.TB, registry pixeldata.Registry, root string, fixture jpegxlFixtureFile) {
	t.Helper()
	file := loadFixtureFile(t, root, fixture)
	if file.TransferSyntax.UID != fixture.TransferSyntaxUID {
		t.Fatalf("fixture TransferSyntaxUID = %q, manifest %q", file.TransferSyntax.UID, fixture.TransferSyntaxUID)
	}
	metadata, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	assertFixtureMetadata(t, fixture, metadata)

	pixel, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if !pixel.Encapsulated {
		t.Fatal("fixture Pixel Data is native, want encapsulated JPEG XL")
	}
	if got, want := len(pixel.Sequence.Fragments), fixture.Encapsulation.FragmentCount; got != want {
		t.Fatalf("fragment count=%d, manifest %d", got, want)
	}

	frames, err := registry.DecodeFrames(file.TransferSyntax.UID, pixel, file.Dataset)
	if !fixtureSupportedByAdapter(fixture) {
		assertUnsupportedFixtureError(t, err, fixture)
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != int(metadata.Rows) || frames.Columns != int(metadata.Columns) {
		t.Fatalf("decoded dimensions = %dx%d, want %dx%d", frames.Columns, frames.Rows, metadata.Columns, metadata.Rows)
	}
	if len(frames.Data) != metadata.NumberOfFrames {
		t.Fatalf("decoded frames=%d, want %d", len(frames.Data), metadata.NumberOfFrames)
	}
	for i, frame := range frames.Data {
		if got, want := int64(len(frame)), metadata.FrameSize(); got != want {
			t.Fatalf("frame %d bytes=%d, want %d", i, got, want)
		}
	}
}

func assertFixtureMetadata(t testing.TB, fixture jpegxlFixtureFile, metadata pixeldata.Metadata) {
	t.Helper()
	checks := []struct {
		name string
		got  any
		want any
	}{
		{name: "Rows", got: metadata.Rows, want: fixture.Rows},
		{name: "Columns", got: metadata.Columns, want: fixture.Columns},
		{name: "SamplesPerPixel", got: metadata.SamplesPerPixel, want: fixture.SamplesPerPixel},
		{name: "PhotometricInterpretation", got: strings.TrimSpace(metadata.PhotometricInterpretation), want: strings.TrimSpace(fixture.PhotometricInterpretation)},
		{name: "BitsAllocated", got: metadata.BitsAllocated, want: fixture.BitsAllocated},
		{name: "BitsStored", got: metadata.BitsStored, want: fixture.BitsStored},
		{name: "PixelRepresentation", got: metadata.PixelRepresentation, want: fixture.PixelRepresentation},
		{name: "NumberOfFrames", got: metadata.NumberOfFrames, want: fixture.FrameCount},
	}
	for _, check := range checks {
		if check.got != check.want {
			t.Fatalf("%s = %v, manifest %v", check.name, check.got, check.want)
		}
	}
}

func fixtureSupportedByAdapter(fixture jpegxlFixtureFile) bool {
	if fixture.PixelRepresentation != 0 && fixture.PixelRepresentation != 1 {
		return false
	}
	if fixture.BitsAllocated != 8 && fixture.BitsAllocated != 16 {
		return false
	}
	if fixture.SamplesPerPixel != 1 && fixture.SamplesPerPixel != 3 {
		return false
	}
	return supportedPhotometricInterpretation(pixeldata.Metadata{
		SamplesPerPixel:           fixture.SamplesPerPixel,
		PhotometricInterpretation: fixture.PhotometricInterpretation,
	})
}

func assertUnsupportedFixtureError(t testing.TB, err error, fixture jpegxlFixtureFile) {
	t.Helper()
	if err == nil {
		t.Fatalf("DecodeFrames() succeeded for unsupported fixture %+v", fixture)
	}
	if !errors.Is(err, ErrUnsupportedMetadata) &&
		!errors.Is(err, pixeldata.ErrUnsupportedPhotometricInterpretation) &&
		!errors.Is(err, pixeldata.ErrUnsupportedPlanarConfiguration) {
		t.Fatalf("DecodeFrames() error = %v, want typed unsupported metadata error", err)
	}
	wantSubstrings := []string{
		fixture.TransferSyntaxUID,
		unsupportedFixtureCondition(fixture),
	}
	for _, want := range wantSubstrings {
		if want != "" && !strings.Contains(err.Error(), want) {
			t.Fatalf("DecodeFrames() error = %q, want substring %q", err, want)
		}
	}
}

func unsupportedFixtureCondition(fixture jpegxlFixtureFile) string {
	switch {
	case fixture.PixelRepresentation != 0 && fixture.PixelRepresentation != 1:
		return fmt.Sprintf("PixelRepresentation=%d", fixture.PixelRepresentation)
	case fixture.BitsAllocated != 8 && fixture.BitsAllocated != 16:
		return fmt.Sprintf("BitsAllocated=%d", fixture.BitsAllocated)
	case fixture.SamplesPerPixel != 1 && fixture.SamplesPerPixel != 3:
		return fmt.Sprintf("SamplesPerPixel=%d", fixture.SamplesPerPixel)
	case strings.TrimSpace(fixture.PhotometricInterpretation) != "":
		return strings.TrimSpace(fixture.PhotometricInterpretation)
	default:
		return ""
	}
}
