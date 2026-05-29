package transfer

import (
	"encoding/binary"
	"sort"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	uiddict "github.com/ThalesMMS/dicom-go/dictionary/uid"
)

type Registry interface {
	Get(uid string) (Syntax, bool)
	Register(syntax Syntax)
	All() []Syntax
}

type MemoryRegistry struct {
	byUID map[string]Syntax
}

func NewRegistry(syntaxes ...Syntax) *MemoryRegistry {
	r := &MemoryRegistry{byUID: map[string]Syntax{}}
	for _, syntax := range syntaxes {
		r.Register(syntax)
	}
	return r
}

func (r *MemoryRegistry) Get(uid string) (Syntax, bool) {
	if r == nil {
		return Syntax{}, false
	}
	uid = NormalizeUID(uid)
	syntax, ok := r.byUID[uid]
	return syntax, ok
}

func (r *MemoryRegistry) Register(syntax Syntax) {
	if r == nil {
		return
	}
	if r.byUID == nil {
		r.byUID = map[string]Syntax{}
	}
	normalizedUID := NormalizeUID(syntax.UID)
	if normalizedUID == "" {
		return
	}
	syntax.UID = normalizedUID
	r.byUID[normalizedUID] = syntax
}

// All returns a UID-sorted snapshot of the registered transfer syntaxes.
func (r *MemoryRegistry) All() []Syntax {
	if r == nil {
		return nil
	}
	out := make([]Syntax, 0, len(r.byUID))
	for _, syntax := range r.byUID {
		out = append(out, syntax)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].UID < out[j].UID
	})
	return out
}

var DefaultRegistry Registry = newDefaultRegistry()

// NormalizeUID trims trailing spaces and NUL bytes from a transfer syntax UID.
func NormalizeUID(uid string) string {
	return core.NormalizeUID(uid)
}

func newDefaultRegistry() Registry {
	r := NewRegistry()
	for _, entry := range uiddict.EntriesByType(uiddict.TransferSyntax) {
		r.Register(knownTransferSyntax(entry))
	}
	for _, syntax := range []Syntax{
		ImplicitVRLittleEndian,
		ExplicitVRLittleEndian,
		DeflatedExplicitVRLittleEndian,
		ExplicitVRBigEndian,
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
		RLELossless,
		JPIPReferenced,
		JPIPReferencedDeflate,
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
		JPEGXLLossless,
		JPEGXLJPEGRecompression,
		JPEGXL,
		HTJ2KLossless,
		HTJ2KLosslessRPCL,
		HTJ2K,
		JPIPHTJ2KReferenced,
		JPIPHTJ2KReferencedDeflate,
		RFC2557MIMEEncapsulation,
		XMLEncoding,
		SMPTEST211020UncompressedProgressiveActiveVideo,
		SMPTEST211020UncompressedInterlacedActiveVideo,
		SMPTEST211030PCMDigitalAudio,
		DeflatedImageFrameCompression,
	} {
		r.Register(syntax)
	}
	return r
}

func knownTransferSyntax(entry uiddict.Entry) Syntax {
	switch entry.UID {
	case ImplicitVRLittleEndian.UID:
		return ImplicitVRLittleEndian
	case ExplicitVRLittleEndian.UID:
		return ExplicitVRLittleEndian
	case DeflatedExplicitVRLittleEndian.UID:
		return DeflatedExplicitVRLittleEndian
	case ExplicitVRBigEndian.UID:
		return ExplicitVRBigEndian
	case EncapsulatedUncompressedExplicitVRLittleEndian.UID:
		return EncapsulatedUncompressedExplicitVRLittleEndian
	case JPEGBaseline.UID:
		return JPEGBaseline
	case JPEGExtended.UID:
		return JPEGExtended
	case JPEGLosslessNonHierarchical.UID:
		return JPEGLosslessNonHierarchical
	case JPEGLosslessSV1.UID:
		return JPEGLosslessSV1
	case JPEGLSLossless.UID:
		return JPEGLSLossless
	case JPEGLSNearLossless.UID:
		return JPEGLSNearLossless
	case JPEG2000LosslessOnly.UID:
		return JPEG2000LosslessOnly
	case JPEG2000.UID:
		return JPEG2000
	case JPEG2000Part2Lossless.UID:
		return JPEG2000Part2Lossless
	case JPEG2000Part2.UID:
		return JPEG2000Part2
	case RLELossless.UID:
		return RLELossless
	case JPIPReferenced.UID:
		return JPIPReferenced
	case JPIPReferencedDeflate.UID:
		return JPIPReferencedDeflate
	case JPIPHTJ2KReferenced.UID:
		return JPIPHTJ2KReferenced
	case JPIPHTJ2KReferencedDeflate.UID:
		return JPIPHTJ2KReferencedDeflate
	}
	if IsJPEGXLTransferSyntax(entry.UID) {
		return newJPEGXLSyntax(entry.UID, entry.Name)
	}
	if IsVideoTransferSyntax(entry.UID) {
		return newVideoMediaSyntax(entry.UID, entry.Name)
	}

	syntax := Syntax{
		UID:        entry.UID,
		Name:       entry.Name,
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Supported:  false,
	}

	switch entry.UID {
	case "1.2.840.10008.1.20":
		syntax.ExplicitVR = false
		return syntax
	case "1.2.840.10008.1.2.4.95", "1.2.840.10008.1.2.4.205", "1.2.840.10008.1.2.8.1":
		syntax.Deflated = true
		return syntax
	}

	if isEncapsulatedTransferSyntaxUID(entry.UID) {
		syntax.Encapsulated = true
		return syntax
	}
	return syntax
}

func isEncapsulatedTransferSyntaxUID(uid string) bool {
	switch {
	case uid == EncapsulatedUncompressedExplicitVRLittleEndian.UID:
		return true
	case strings.HasPrefix(uid, "1.2.840.10008.1.2.4."):
		return uid != "1.2.840.10008.1.2.4.95" && uid != "1.2.840.10008.1.2.4.205"
	case uid == "1.2.840.10008.1.2.5":
		return true
	case strings.HasPrefix(uid, "1.2.840.10008.1.2.7."):
		return true
	case uid == "1.2.840.10008.1.2.8.1":
		return true
	default:
		return false
	}
}
