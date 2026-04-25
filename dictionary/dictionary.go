// Package dictionary defines the minimal data dictionary contract used by
// implicit-VR decoding.
//
// The Go implementation intentionally keeps this surface small: exact tag and
// keyword lookup only. Repeating tag ranges and virtual VRs are resolved inside
// generated dictionaries before they reach callers.
package dictionary

import "github.com/ThalesMMS/dicom-go/core"

// Entry describes one DICOM data element dictionary record.
type Entry struct {
	Tag     core.Tag
	VR      core.VR
	Keyword string
	Name    string
	VM      string
	Retired bool
}

// DataDictionary resolves DICOM attributes by tag or keyword.
//
// Keyword lookup is case-insensitive by contract so callers do not need to
// preserve the standard DICOM keyword casing.
type DataDictionary interface {
	ByTag(tag core.Tag) (Entry, bool)
	ByKeyword(keyword string) (Entry, bool)
}

// Empty is a dictionary implementation with no registered entries.
type Empty struct{}

func (Empty) ByTag(core.Tag) (Entry, bool)   { return Entry{}, false }
func (Empty) ByKeyword(string) (Entry, bool) { return Entry{}, false }

// LookupVR resolves the VR for an element header in implicit VR transfer
// syntaxes.
//
// It is the primary dictionary resolution path used by the parser's implicit
// VR reader after transfer-syntax-specific special cases such as Pixel Data and
// Overlay Data have been handled.
//
// Unknown tags, private tags without dictionary coverage, missing dictionaries,
// and entries without an exact VR all fall back to UN. In practice this means
// implicit-VR parsing preserves defined-length values as opaque raw bytes
// instead of guessing a VR heuristically. Private dictionary support is
// intentionally out of scope for the standard dictionary package, so odd-group
// private tags typically reach this fallback unless callers inject a custom
// DataDictionary.
func LookupVR(dict DataDictionary, tag core.Tag) core.VR {
	if dict == nil {
		return core.VRUN
	}
	if entry, ok := dict.ByTag(tag); ok && entry.VR != "" {
		return entry.VR
	}
	return core.VRUN
}
