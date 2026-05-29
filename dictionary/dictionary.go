// Package dictionary defines the minimal data dictionary contract used by
// implicit-VR decoding.
//
// The implementation intentionally keeps this surface small: exact tag and
// keyword lookup, plus an optional immutable description of contextual VRs.
// Repeating tag ranges are resolved inside generated dictionaries before they
// reach callers.
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

// ContextualVR identifies a DICOM dictionary VR whose concrete representation
// depends on other attributes or the transfer syntax.
type ContextualVR string

const (
	ContextualVRXS ContextualVR = "xs" // US or SS
	ContextualVROX ContextualVR = "ox" // OB or OW
	ContextualVRPX ContextualVR = "px" // Pixel Data: OB or OW
	ContextualVRLT ContextualVR = "lt" // LUT Data: US, SS, or OW
	ContextualVRUP ContextualVR = "up" // directory offset: UL
)

// VRSpec is an immutable, comparable set of allowed VRs. The zero value is
// empty. Values returns a caller-owned copy so generated dictionary storage
// cannot be mutated through a lookup result.
type VRSpec struct {
	values  [34]core.VR
	count   uint8
	context ContextualVR
}

// NewVRSpec constructs an exact or alternative VR specification. Empty and
// duplicate values are omitted while preserving order.
func NewVRSpec(values ...core.VR) VRSpec {
	var spec VRSpec
	for _, value := range values {
		if value == "" || spec.Contains(value) || int(spec.count) == len(spec.values) {
			continue
		}
		spec.values[spec.count] = value
		spec.count++
	}
	return spec
}

// NewContextualVRSpec constructs the alternatives defined by a contextual VR
// token in the standard dictionary source.
func NewContextualVRSpec(context ContextualVR) VRSpec {
	var spec VRSpec
	switch context {
	case ContextualVRXS:
		spec = NewVRSpec(core.VRUS, core.VRSS)
	case ContextualVROX, ContextualVRPX:
		spec = NewVRSpec(core.VROB, core.VROW)
	case ContextualVRLT:
		spec = NewVRSpec(core.VRUS, core.VRSS, core.VROW)
	case ContextualVRUP:
		spec = NewVRSpec(core.VRUL)
	default:
		return VRSpec{}
	}
	spec.context = context
	return spec
}

func (s VRSpec) Contains(vr core.VR) bool {
	for i := 0; i < int(s.count); i++ {
		if s.values[i] == vr {
			return true
		}
	}
	return false
}

func (s VRSpec) Values() []core.VR {
	if s.count == 0 {
		return nil
	}
	return append([]core.VR(nil), s.values[:s.count]...)
}

func (s VRSpec) Context() ContextualVR { return s.context }

// VRSpecDictionary optionally preserves the alternatives behind contextual
// VR tokens. DataDictionary remains unchanged for source compatibility.
type VRSpecDictionary interface {
	DataDictionary
	VRSpecByTag(tag core.Tag) (VRSpec, bool)
}

// Chain composes dictionaries by trying each dictionary in order. It is useful
// for private or site-specific overlays:
//
//	dictionary.Chain{privateDictionary, std.Dictionary}
//
// The first dictionary that returns a match wins. Nil dictionaries are skipped.
type Chain []DataDictionary

func (c Chain) ByTag(tag core.Tag) (Entry, bool) {
	for _, dict := range c {
		if dict == nil {
			continue
		}
		if entry, ok := dict.ByTag(tag); ok {
			return entry, true
		}
	}
	return Entry{}, false
}

func (c Chain) ByKeyword(keyword string) (Entry, bool) {
	for _, dict := range c {
		if dict == nil {
			continue
		}
		if entry, ok := dict.ByKeyword(keyword); ok {
			return entry, true
		}
	}
	return Entry{}, false
}

// VRSpecByTag follows the same first-match precedence as ByTag. A dictionary
// that wins the entry lookup also owns its VR specification; alternatives from
// a later fallback dictionary must not leak through a private overlay.
func (c Chain) VRSpecByTag(tag core.Tag) (VRSpec, bool) {
	for _, dict := range c {
		if dict == nil {
			continue
		}
		entry, ok := dict.ByTag(tag)
		if !ok {
			continue
		}
		if contextual, ok := dict.(VRSpecDictionary); ok {
			if spec, found := contextual.VRSpecByTag(tag); found {
				return spec, true
			}
		}
		if entry.VR == "" {
			return VRSpec{}, false
		}
		return NewVRSpec(entry.VR), true
	}
	return VRSpec{}, false
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
// DataDictionary. Use Chain to layer private dictionaries before the standard
// dictionary without losing standard fallback coverage.
func LookupVR(dict DataDictionary, tag core.Tag) core.VR {
	if dict == nil {
		return core.VRUN
	}
	if entry, ok := dict.ByTag(tag); ok && entry.VR != "" {
		return entry.VR
	}
	return core.VRUN
}

// LookupVRSpec resolves every VR allowed by a dictionary entry. Dictionaries
// that do not implement VRSpecDictionary retain their historical exact Entry
// behavior.
func LookupVRSpec(dict DataDictionary, tag core.Tag) (VRSpec, bool) {
	if dict == nil {
		return VRSpec{}, false
	}
	if contextual, ok := dict.(VRSpecDictionary); ok {
		if spec, found := contextual.VRSpecByTag(tag); found {
			return spec, true
		}
	}
	entry, ok := dict.ByTag(tag)
	if !ok || entry.VR == "" {
		return VRSpec{}, false
	}
	return NewVRSpec(entry.VR), true
}
