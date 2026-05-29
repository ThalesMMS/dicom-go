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
		HTJ2KLossless,
		HTJ2KLosslessRPCL,
		HTJ2K,
		JPEGXLLossless,
		JPEGXLJPEGRecompression,
		JPEGXL,
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
		{syntax: DeflatedExplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRBigEndian, want: true},
		{syntax: EncapsulatedUncompressedExplicitVRLittleEndian, want: false},
		{syntax: JPEGBaseline, want: true},
		{syntax: JPEGExtended, want: true},
		{syntax: JPEGLosslessNonHierarchical, want: true},
		{syntax: JPEGLosslessSV1, want: true},
		{syntax: JPEG2000, want: false},
		{syntax: RLELossless, want: true},
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
		{syntax: DeflatedExplicitVRLittleEndian, want: true},
		{syntax: ExplicitVRBigEndian, want: true},
		{syntax: JPEGBaseline, want: true},
		{syntax: JPEGExtended, want: true},
		{syntax: JPEGLosslessNonHierarchical, want: true},
		{syntax: JPEGLosslessSV1, want: true},
		{syntax: EncapsulatedUncompressedExplicitVRLittleEndian, want: true},
		{syntax: JPEGLSLossless, want: true},
		{syntax: JPEGLSNearLossless, want: true},
		{syntax: JPEG2000LosslessOnly, want: true},
		{syntax: JPEG2000, want: true},
		{syntax: JPEG2000Part2Lossless, want: true},
		{syntax: JPEG2000Part2, want: true},
		{syntax: HTJ2KLossless, want: true},
		{syntax: HTJ2KLosslessRPCL, want: true},
		{syntax: HTJ2K, want: true},
		{syntax: RLELossless, want: true},
		{syntax: JPIPReferenced, want: true},
		{syntax: JPIPReferencedDeflate, want: true},
		{syntax: JPIPHTJ2KReferenced, want: true},
		{syntax: JPIPHTJ2KReferencedDeflate, want: true},
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
	tests := []struct {
		syntax        Syntax
		wantSupported bool
		wantHasCodec  bool
	}{
		{syntax: JPEGBaseline, wantSupported: true, wantHasCodec: true},
		{syntax: JPEGExtended, wantSupported: true, wantHasCodec: true},
		{syntax: JPEGLosslessNonHierarchical, wantSupported: true, wantHasCodec: true},
		{syntax: JPEGLosslessSV1, wantSupported: true, wantHasCodec: true},
		{syntax: RLELossless, wantSupported: true, wantHasCodec: true},
	}

	for _, tt := range tests {
		syntax := tt.syntax
		if !syntax.ExplicitVR {
			t.Fatalf("expected syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected syntax %q to be encapsulated", syntax.UID)
		}
		if syntax.Supported != tt.wantSupported {
			t.Fatalf("Supported for %q = %v, want %v", syntax.UID, syntax.Supported, tt.wantSupported)
		}
		if syntax.CodecAvailable != tt.wantHasCodec {
			t.Fatalf("CodecAvailable for %q = %v, want %v", syntax.UID, syntax.CodecAvailable, tt.wantHasCodec)
		}
	}
}

func TestJPEGLSStillImageSyntaxFieldValues(t *testing.T) {
	for _, syntax := range []Syntax{JPEGLSLossless, JPEGLSNearLossless} {
		if !IsJPEGLSTransferSyntax(syntax.UID) {
			t.Fatalf("expected syntax %q to be classified as JPEG-LS", syntax.UID)
		}
		if !syntax.ExplicitVR {
			t.Fatalf("expected JPEG-LS syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected JPEG-LS syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected JPEG-LS syntax %q to preserve encapsulated Pixel Data", syntax.UID)
		}
		if !syntax.Supported {
			t.Fatalf("expected JPEG-LS syntax %q to be metadata/payload-readable", syntax.UID)
		}
		if syntax.MediaPayload {
			t.Fatalf("expected JPEG-LS syntax %q to not be classified as media payload", syntax.UID)
		}
		if syntax.CodecAvailable {
			t.Fatalf("expected JPEG-LS syntax %q to have no default decoder adapter", syntax.UID)
		}
		if !syntax.RequiresCodec() {
			t.Fatalf("expected JPEG-LS syntax %q to remain a still-image codec target", syntax.UID)
		}
	}
	if IsJPEGLSTransferSyntax(JPEGBaseline.UID) {
		t.Fatalf("expected legacy JPEG syntax %q to not be classified as JPEG-LS", JPEGBaseline.UID)
	}
	if IsJPEGLSTransferSyntax(JPEG2000.UID) {
		t.Fatalf("expected JPEG 2000 syntax %q to not be classified as JPEG-LS", JPEG2000.UID)
	}
	if IsJPEGLSTransferSyntax(JPEGXL.UID) {
		t.Fatalf("expected JPEG XL syntax %q to not be classified as JPEG-LS", JPEGXL.UID)
	}
}

func TestJPEG2000StillImageSyntaxFieldValues(t *testing.T) {
	for _, syntax := range []Syntax{
		JPEG2000LosslessOnly,
		JPEG2000,
		JPEG2000Part2Lossless,
		JPEG2000Part2,
		HTJ2KLossless,
		HTJ2KLosslessRPCL,
		HTJ2K,
	} {
		if !IsJPEG2000TransferSyntax(syntax.UID) {
			t.Fatalf("expected syntax %q to be classified as JPEG 2000 / HTJ2K", syntax.UID)
		}
		if !syntax.ExplicitVR {
			t.Fatalf("expected JPEG 2000 syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected JPEG 2000 syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected JPEG 2000 syntax %q to preserve encapsulated Pixel Data", syntax.UID)
		}
		if !syntax.Supported {
			t.Fatalf("expected JPEG 2000 syntax %q to be metadata/payload-readable", syntax.UID)
		}
		if syntax.MediaPayload {
			t.Fatalf("expected JPEG 2000 syntax %q to not be classified as media payload", syntax.UID)
		}
		if syntax.CodecAvailable {
			t.Fatalf("expected JPEG 2000 syntax %q to have no default decoder adapter", syntax.UID)
		}
		if !syntax.RequiresCodec() {
			t.Fatalf("expected JPEG 2000 syntax %q to remain a still-image codec target", syntax.UID)
		}
	}
	if IsJPEG2000TransferSyntax(JPEGBaseline.UID) {
		t.Fatalf("expected legacy JPEG syntax %q to not be classified as JPEG 2000 / HTJ2K", JPEGBaseline.UID)
	}
	if IsJPEG2000TransferSyntax(JPEGXL.UID) {
		t.Fatalf("expected JPEG XL syntax %q to not be classified as JPEG 2000 / HTJ2K", JPEGXL.UID)
	}
	if IsJPEG2000TransferSyntax(JPIPHTJ2KReferenced.UID) {
		t.Fatalf("expected JPIP HTJ2K reference syntax %q to require external retrieval instead of local JPEG 2000 decode", JPIPHTJ2KReferenced.UID)
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

func TestVideoMediaSyntaxFieldValues(t *testing.T) {
	for _, syntax := range videoMediaSyntaxesForTest() {
		if !IsVideoTransferSyntax(syntax.UID) {
			t.Fatalf("expected syntax %q to be classified as video media", syntax.UID)
		}
		if !syntax.ExplicitVR {
			t.Fatalf("expected video syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected video syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected video syntax %q to preserve encapsulated media payloads", syntax.UID)
		}
		if !syntax.Supported {
			t.Fatalf("expected video syntax %q to be metadata/payload-readable", syntax.UID)
		}
		if syntax.CodecAvailable {
			t.Fatalf("expected video syntax %q to avoid still-image codec availability", syntax.UID)
		}
		if syntax.RequiresCodec() {
			t.Fatalf("expected video syntax %q to not be classified as a still-image codec target", syntax.UID)
		}
	}
	if IsVideoTransferSyntax(JPEGBaseline.UID) {
		t.Fatalf("expected still-image syntax %q to not be video media", JPEGBaseline.UID)
	}
	if IsVideoTransferSyntax(JPEGXL.UID) {
		t.Fatalf("expected JPEG XL syntax %q to stay a still-image codec family", JPEGXL.UID)
	}
}

func videoMediaSyntaxesForTest() []Syntax {
	return []Syntax{
		MPEG2MPML,
		MPEG2MPMLF,
		MPEG2MPHL,
		MPEG2MPHLF,
		MPEG4HP41,
		MPEG4HP41F,
		MPEG4HP41BD,
		MPEG4HP41BDF,
		MPEG4HP422D,
		MPEG4HP422DF,
		MPEG4HP423D,
		MPEG4HP423DF,
		MPEG4HP42STEREO,
		MPEG4HP42STEREOF,
		HEVCMP51,
		HEVCM10P51,
	}
}

func TestJPIPReferencedSyntaxFieldValues(t *testing.T) {
	tests := []struct {
		syntax       Syntax
		wantDeflated bool
	}{
		{syntax: JPIPReferenced},
		{syntax: JPIPReferencedDeflate, wantDeflated: true},
		{syntax: JPIPHTJ2KReferenced},
		{syntax: JPIPHTJ2KReferencedDeflate, wantDeflated: true},
	}

	for _, tt := range tests {
		syntax := tt.syntax
		if !syntax.ExplicitVR {
			t.Fatalf("expected JPIP syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected JPIP syntax %q to be little endian", syntax.UID)
		}
		if syntax.Encapsulated {
			t.Fatalf("expected JPIP syntax %q to preserve URL metadata without encapsulated Pixel Data", syntax.UID)
		}
		if !syntax.Supported {
			t.Fatalf("expected JPIP syntax %q to be metadata-readable", syntax.UID)
		}
		if syntax.CodecAvailable {
			t.Fatalf("expected JPIP syntax %q to require an external retriever, not a local codec", syntax.UID)
		}
		if syntax.Deflated != tt.wantDeflated {
			t.Fatalf("Deflated for JPIP syntax %q = %v, want %v", syntax.UID, syntax.Deflated, tt.wantDeflated)
		}
	}
}

func TestJPEGXLStillImageSyntaxFieldValues(t *testing.T) {
	for _, syntax := range []Syntax{JPEGXLLossless, JPEGXLJPEGRecompression, JPEGXL} {
		if !IsJPEGXLTransferSyntax(syntax.UID) {
			t.Fatalf("expected syntax %q to be classified as JPEG XL", syntax.UID)
		}
		if !syntax.ExplicitVR {
			t.Fatalf("expected JPEG XL syntax %q to use explicit VR", syntax.UID)
		}
		if syntax.ByteOrder != binary.LittleEndian {
			t.Fatalf("expected JPEG XL syntax %q to be little endian", syntax.UID)
		}
		if !syntax.Encapsulated {
			t.Fatalf("expected JPEG XL syntax %q to preserve encapsulated Pixel Data", syntax.UID)
		}
		if !syntax.Supported {
			t.Fatalf("expected JPEG XL syntax %q to be metadata/payload-readable", syntax.UID)
		}
		if syntax.MediaPayload {
			t.Fatalf("expected JPEG XL syntax %q to not be classified as media payload", syntax.UID)
		}
		if syntax.CodecAvailable {
			t.Fatalf("expected JPEG XL syntax %q to have no default decoder adapter", syntax.UID)
		}
		if !syntax.RequiresCodec() {
			t.Fatalf("expected JPEG XL syntax %q to remain a still-image codec target", syntax.UID)
		}
	}
	if IsJPEGXLTransferSyntax(JPEGBaseline.UID) {
		t.Fatalf("expected legacy JPEG syntax %q to not be classified as JPEG XL", JPEGBaseline.UID)
	}
	if IsJPEGXLTransferSyntax(JPEG2000.UID) {
		t.Fatalf("expected JPEG 2000 syntax %q to not be classified as JPEG XL", JPEG2000.UID)
	}
	if IsJPEGXLTransferSyntax(MPEG4HP41.UID) {
		t.Fatalf("expected video syntax %q to not be classified as JPEG XL", MPEG4HP41.UID)
	}
}

func TestNewlyRegisteredUnsupportedSyntaxes(t *testing.T) {
	tests := []Syntax{
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
