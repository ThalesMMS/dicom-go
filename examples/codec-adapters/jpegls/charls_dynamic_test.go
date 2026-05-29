//go:build (jpegls_charls || codecfull) && (darwin || linux || windows)

package jpegls

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCharLSDecoderUnavailableWhenLibraryMissing(t *testing.T) {
	t.Setenv("DICOM_GO_CHARLS_LIBRARY", filepath.Join(t.TempDir(), "missing-libcharls"))

	_, err := openCharLSAPI()
	if !errors.Is(err, ErrDecoderUnavailable) {
		t.Fatalf("openCharLSAPI() error = %v, want ErrDecoderUnavailable", err)
	}
}

func TestValidateCharLSAPIVersion(t *testing.T) {
	tests := []struct {
		name    string
		major   int32
		minor   int32
		patch   int32
		wantErr error
	}{
		{name: "packaged version", major: 2, minor: 4, patch: 2},
		{name: "newer compatible minor", major: 2, minor: 5},
		{name: "old major", major: 1, minor: 4, wantErr: ErrDecoderUnavailable},
		{name: "old minor", major: 2, minor: 3, wantErr: ErrDecoderUnavailable},
		{name: "unvalidated future major", major: 3, wantErr: ErrDecoderUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &charlsAPI{
				versionNumber: func(major, minor, patch *int32) {
					*major = tt.major
					*minor = tt.minor
					*patch = tt.patch
				},
			}
			err := validateCharLSAPI(api)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateCharLSAPI() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateQualifiedCharLSAPIVersion(t *testing.T) {
	tests := []struct {
		name    string
		major   int32
		minor   int32
		patch   int32
		wantErr error
	}{
		{name: "qualified release", major: 2, minor: 4, patch: 2},
		{name: "different patch", major: 2, minor: 4, patch: 1, wantErr: ErrDecoderUnavailable},
		{name: "newer compatible release", major: 2, minor: 5, patch: 0, wantErr: ErrDecoderUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := &charlsAPI{
				versionNumber: func(major, minor, patch *int32) {
					*major = tt.major
					*minor = tt.minor
					*patch = tt.patch
				},
			}
			err := validateQualifiedCharLSAPI(api)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("validateQualifiedCharLSAPI() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCharLSDecoderConformanceLossless8Bit(t *testing.T) {
	frame := []byte{0, 64, 128, 255}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 1,
	}, frame)

	tc := charlsFixtureCase("charls-jpegls-lossless-8bit", transfer.JPEGLSLossless, jpeglsMetadataOptions{
		rows: 2, columns: 2, bitsAllocated: 8,
	}, encoded, frame)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, NewCharLSDecoder()); err != nil {
		t.Fatal(err)
	}
	if err := codecfixture.ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}

func TestCharLSDecoderConformanceLossless16Bit(t *testing.T) {
	frame := []byte{0, 0, 1, 0, 255, 0, 255, 255}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 16, samplesPerPixel: 1,
	}, frame)

	tc := charlsFixtureCase("charls-jpegls-lossless-16bit", transfer.JPEGLSLossless, jpeglsMetadataOptions{
		rows: 2, columns: 2, bitsAllocated: 16,
	}, encoded, frame)
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, NewCharLSDecoder()); err != nil {
		t.Fatal(err)
	}
	if err := codecfixture.ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}

func TestCharLSDecoderConformanceNearLossless(t *testing.T) {
	frame := []byte{10, 20, 30, 40}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 1, nearLossless: 1,
	}, frame)

	tc := charlsFixtureCase("charls-jpegls-near-lossless", transfer.JPEGLSNearLossless, jpeglsMetadataOptions{
		rows: 2, columns: 2, bitsAllocated: 8,
	}, encoded, frame)
	tc.Tolerance = 1
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, NewCharLSDecoder()); err != nil {
		t.Fatal(err)
	}
	if err := codecfixture.ValidateCase(registry, tc); err != nil {
		t.Fatal(err)
	}
}

func TestCharLSDecoderRejectsMalformedData(t *testing.T) {
	tc := charlsFixtureCase("charls-jpegls-malformed", transfer.JPEGLSLossless, jpeglsMetadataOptions{}, []byte{0xff, 0xd8, 0x00, 0x01}, nil)

	result := runCharLSFixtureCase(t, tc)
	if !errors.Is(result.Err, ErrMalformedFrame) {
		t.Fatalf("RunCase() error = %v, want ErrMalformedFrame", result.Err)
	}
}

func TestCharLSDecoderRejectsMetadataMismatch(t *testing.T) {
	frame := []byte{0, 64, 128, 255}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 1,
	}, frame)
	tc := charlsFixtureCase("charls-jpegls-metadata-mismatch", transfer.JPEGLSLossless, jpeglsMetadataOptions{rows: 1, columns: 2}, encoded, nil)

	result := runCharLSFixtureCase(t, tc)
	if !errors.Is(result.Err, pixeldata.ErrPixelDataSizeMismatch) {
		t.Fatalf("RunCase() error = %v, want ErrPixelDataSizeMismatch", result.Err)
	}
}

func TestCharLSDecoderRejectsUnsupportedRGBInterleave(t *testing.T) {
	frame := []byte{
		255, 0, 0,
		0, 255, 0,
		0, 0, 255,
		255, 255, 255,
	}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 3,
	}, frame)
	tc := charlsFixtureCase("charls-jpegls-unsupported-rgb-interleave", transfer.JPEGLSLossless, jpeglsMetadataOptions{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 3, photometric: "RGB",
	}, encoded, nil)

	result := runCharLSFixtureCase(t, tc)
	if !errors.Is(result.Err, ErrUnsupportedMetadata) {
		t.Fatalf("RunCase() error = %v, want ErrUnsupportedMetadata", result.Err)
	}
}

func TestCharLSDecoderRejectsNearLosslessDataForLosslessSyntax(t *testing.T) {
	frame := []byte{10, 20, 30, 40}
	encoded := encodeCharLSTestFrame(t, charlsFrameSpec{
		rows: 2, columns: 2, bitsAllocated: 8, samplesPerPixel: 1, nearLossless: 1,
	}, frame)
	tc := charlsFixtureCase("charls-jpegls-near-data-lossless-syntax", transfer.JPEGLSLossless, jpeglsMetadataOptions{
		rows: 2, columns: 2, bitsAllocated: 8,
	}, encoded, nil)

	result := runCharLSFixtureCase(t, tc)
	if !errors.Is(result.Err, ErrUnsupportedMetadata) {
		t.Fatalf("RunCase() error = %v, want ErrUnsupportedMetadata", result.Err)
	}
}

func charlsFixtureCase(name string, syntax transfer.Syntax, opts jpeglsMetadataOptions, fragment, expected []byte) codecfixture.Case {
	return codecfixture.Case{
		Name:           name,
		Description:    "synthetic CharLS-backed JPEG-LS conformance case",
		Syntax:         syntax,
		Size:           codecfixture.SizeSmall,
		Provenance:     codecfixture.Provenance{Source: "generated by CharLS test encoder", Synthetic: true, License: "repository test fixture", Permission: "generated in-tree", NoPHI: true},
		Elements:       append(jpeglsMetadataElements(opts), fragmentElement(fragment)),
		ExpectedFrames: [][]byte{append([]byte(nil), expected...)},
	}
}

func encodeCharLSTestFrame(t *testing.T, info charlsFrameSpec, frame []byte) []byte {
	t.Helper()
	encoded, err := encodeCharLSFrameForTest(info, frame)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func runCharLSFixtureCase(t *testing.T, tc codecfixture.Case) codecfixture.Result {
	t.Helper()
	registry := pixeldata.NewMemoryRegistry()
	if err := Register(registry, NewCharLSDecoder()); err != nil {
		t.Fatal(err)
	}
	return codecfixture.RunCase(registry, tc)
}
