package transfer

import (
	"encoding/binary"
	"testing"
)

func TestSyntaxRequiresCodec(t *testing.T) {
	nativeSyntaxes := []Syntax{
		ImplicitVRLittleEndian,
		ExplicitVRLittleEndian,
		ExplicitVRBigEndian,
	}
	for _, syntax := range nativeSyntaxes {
		if syntax.RequiresCodec() {
			t.Fatalf("expected native syntax %q to not require a codec", syntax.UID)
		}
	}

	codecSyntaxes := []Syntax{
		DeflatedExplicitVRLittleEndian,
		JPEGBaseline,
		JPEGExtended,
		JPEGLosslessNonHierarchical,
		JPEGLosslessSV1,
		JPEGLSLossless,
		JPEGLSNearLossless,
		JPEG2000LosslessOnly,
		JPEG2000,
		JPEG2000Part2Lossless,
		JPEG2000Part2,
		RLELossless,
	}
	for _, syntax := range codecSyntaxes {
		if !syntax.RequiresCodec() {
			t.Fatalf("expected syntax %q to require a codec", syntax.UID)
		}
	}
	if EncapsulatedUncompressedExplicitVRLittleEndian.RequiresCodec() {
		t.Fatalf("expected fragment-only syntax %q to not require a codec", EncapsulatedUncompressedExplicitVRLittleEndian.UID)
	}
}

func TestSyntaxHasCodec(t *testing.T) {
	tests := []struct {
		syntax Syntax
		want   bool
	}{
		{syntax: ImplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRLittleEndian, want: true},
		{syntax: DeflatedExplicitVRLittleEndian, want: false},
		{syntax: ExplicitVRBigEndian, want: true},
		{syntax: EncapsulatedUncompressedExplicitVRLittleEndian, want: false},
		{syntax: JPEGBaseline, want: false},
		{syntax: JPEG2000, want: false},
		{syntax: RLELossless, want: false},
	}

	for _, tt := range tests {
		if got := tt.syntax.HasCodec(); got != tt.want {
			t.Fatalf("HasCodec() for %q = %v, want %v", tt.syntax.UID, got, tt.want)
		}
	}
}

func TestSyntaxSupportedFlag(t *testing.T) {
	tests := []struct {
		syntax Syntax
		want   bool
	}{
		{syntax: ImplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRLittleEndian, want: true},
		{syntax: DeflatedExplicitVRLittleEndian, want: false},
		{syntax: ExplicitVRBigEndian, want: true},
		{syntax: JPEGBaseline, want: false},
		{syntax: EncapsulatedUncompressedExplicitVRLittleEndian, want: true},
	}

	for _, tt := range tests {
		if got := tt.syntax.Supported; got != tt.want {
			t.Fatalf("Supported for %q = %v, want %v", tt.syntax.UID, got, tt.want)
		}
	}
}

func TestSyntaxIsLittleEndianForRegisteredSyntaxes(t *testing.T) {
	tests := []struct {
		syntax Syntax
		want   bool
	}{
		{syntax: ImplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRLittleEndian, want: true},
		{syntax: DeflatedExplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRBigEndian, want: false},
		{syntax: JPEGBaseline, want: true},
		{syntax: JPEGExtended, want: true},
		{syntax: JPEGLosslessNonHierarchical, want: true},
		{syntax: JPEGLosslessSV1, want: true},
		{syntax: JPEGLSLossless, want: true},
		{syntax: JPEGLSNearLossless, want: true},
		{syntax: JPEG2000LosslessOnly, want: true},
		{syntax: JPEG2000, want: true},
		{syntax: JPEG2000Part2Lossless, want: true},
		{syntax: JPEG2000Part2, want: true},
		{syntax: RLELossless, want: true},
		{syntax: EncapsulatedUncompressedExplicitVRLittleEndian, want: true},
	}

	for _, tt := range tests {
		if got := tt.syntax.IsLittleEndian(); got != tt.want {
			t.Fatalf("IsLittleEndian() for %q = %v, want %v", tt.syntax.UID, got, tt.want)
		}
	}
}

func TestEncapsulatedSyntaxFieldValues(t *testing.T) {
	tests := []Syntax{
		JPEGBaseline,
		JPEGExtended,
		JPEGLosslessNonHierarchical,
		JPEGLosslessSV1,
		JPEGLSLossless,
		JPEGLSNearLossless,
		JPEG2000LosslessOnly,
		JPEG2000,
		JPEG2000Part2Lossless,
		JPEG2000Part2,
		RLELossless,
	}

	for _, syntax := range tests {
		if !syntax.ExplicitVR {
			t.Fatalf("expected syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected syntax %q to be encapsulated", syntax.UID)
		}
		if syntax.Supported {
			t.Fatalf("expected encapsulated syntax %q to be unsupported without codec support", syntax.UID)
		}
	}
}

func TestFragmentOnlySyntaxFieldValues(t *testing.T) {
	syntax := EncapsulatedUncompressedExplicitVRLittleEndian

	if !syntax.ExplicitVR {
		t.Fatalf("expected syntax %q to use explicit VR", syntax.UID)
	}
	if syntax.ByteOrder != binary.LittleEndian {
		t.Fatalf("expected syntax %q to be little endian", syntax.UID)
	}
	if !syntax.Encapsulated {
		t.Fatalf("expected syntax %q to be encapsulated", syntax.UID)
	}
	if !syntax.Supported {
		t.Fatalf("expected syntax %q to be supported", syntax.UID)
	}
	if syntax.CodecAvailable {
		t.Fatalf("expected syntax %q to be fragment-only without codec availability", syntax.UID)
	}
}

func TestNewlyRegisteredUnsupportedSyntaxes(t *testing.T) {
	tests := []Syntax{
		MPEG2MPML,
		MPEG4HP41,
		JPEGXLLossless,
		HTJ2KLossless,
		JPIPHTJ2KReferenced,
		SMPTEST211020UncompressedProgressiveActiveVideo,
		DeflatedImageFrameCompression,
	}

	for _, syntax := range tests {
		if syntax.Supported {
			t.Fatalf("expected syntax %q to be unsupported", syntax.UID)
		}
		if !syntax.ExplicitVR {
			t.Fatalf("expected syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected syntax %q to be little endian", syntax.UID)
		}
	}
}

func TestST2110SyntaxesAreNotEncapsulated(t *testing.T) {
	tests := []Syntax{
		SMPTEST211020UncompressedProgressiveActiveVideo,
		SMPTEST211020UncompressedInterlacedActiveVideo,
		SMPTEST211030PCMDigitalAudio,
	}

	for _, syntax := range tests {
		if syntax.Encapsulated {
			t.Fatalf("expected ST 2110 syntax %q to not be marked encapsulated", syntax.UID)
		}
		if syntax.RequiresCodec() {
			t.Fatalf("expected ST 2110 syntax %q to not be treated as encapsulated/deflated codec syntax", syntax.UID)
		}
	}
}
