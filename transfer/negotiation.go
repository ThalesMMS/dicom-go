package transfer

// NativeStorePreference selects how native little-endian transfer syntaxes are
// proposed for C-STORE presentation contexts.
type NativeStorePreference int

const (
	NativeStoreSourceFirst NativeStorePreference = iota
	NativeStoreExplicitLittleEndianFirst
	NativeStoreImplicitLittleEndianFirst
)

var preservableCompressedReceiveSyntaxes = []Syntax{
	EncapsulatedUncompressedExplicitVRLittleEndian,
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
}

// ProposedStoreTransferSyntaxUIDs returns normalized transfer syntax UIDs for a
// Storage SCU presentation context. Compressed sources are proposed unchanged,
// because this helper does not imply transcoding.
func ProposedStoreTransferSyntaxUIDs(sourceUID string, pref NativeStorePreference) []string {
	if !isNativeStoreTransferSyntax(sourceUID) {
		return normalizedUniqueUIDs(sourceUID)
	}
	switch pref {
	case NativeStoreExplicitLittleEndianFirst:
		return normalizedUniqueUIDs(ExplicitVRLittleEndian.UID, sourceUID, ImplicitVRLittleEndian.UID)
	case NativeStoreImplicitLittleEndianFirst:
		return normalizedUniqueUIDs(ImplicitVRLittleEndian.UID, sourceUID, ExplicitVRLittleEndian.UID)
	default:
		return normalizedUniqueUIDs(sourceUID, ExplicitVRLittleEndian.UID, ImplicitVRLittleEndian.UID)
	}
}

// ReceiveTransferSyntaxUIDs combines native receive order with compressed
// syntaxes that dicom-go can preserve without claiming renderability.
func ReceiveTransferSyntaxUIDs(nativeOrder ...Syntax) []string {
	uids := make([]string, 0, len(nativeOrder)+len(preservableCompressedReceiveSyntaxes))
	for _, syntax := range nativeOrder {
		uids = append(uids, syntax.UID)
	}
	for _, syntax := range preservableCompressedReceiveSyntaxes {
		uids = append(uids, syntax.UID)
	}
	return normalizedUniqueUIDs(uids...)
}

// PreservableCompressedReceiveSyntaxes returns compressed syntaxes that the
// default receive profile accepts for metadata/payload preservation.
func PreservableCompressedReceiveSyntaxes() []Syntax {
	return append([]Syntax(nil), preservableCompressedReceiveSyntaxes...)
}

func isNativeStoreTransferSyntax(uid string) bool {
	switch NormalizeUID(uid) {
	case ExplicitVRLittleEndian.UID, ImplicitVRLittleEndian.UID:
		return true
	default:
		return false
	}
}

func normalizedUniqueUIDs(uids ...string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(uids))
	for _, uid := range uids {
		uid = NormalizeUID(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		out = append(out, uid)
	}
	return out
}
