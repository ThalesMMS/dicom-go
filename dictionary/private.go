package dictionary

import (
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
)

var (
	ErrPrivateDefinition        = errors.New("dicom: invalid private dictionary definition")
	ErrPrivateCreator           = errors.New("dicom: invalid private creator reservation")
	ErrPrivateCreatorConflict   = errors.New("dicom: conflicting private creator reservations")
	ErrPrivateCreatorMissing    = errors.New("dicom: private creator reservation missing")
	ErrPrivateDefinitionMissing = errors.New("dicom: private attribute definition missing")
	ErrPrivateResourceLimit     = errors.New("dicom: private reservation resource limit exceeded")
	ErrPrivateBlocksExhausted   = errors.New("dicom: no unused private block available")
)

// PrivateDataDictionary optionally extends DataDictionary with creator-aware
// definitions. Its entries describe a block-relative offset, not a fixed block.
// Implementations are shared read-only; reservations belong to each dataset.
type PrivateDataDictionary interface {
	DataDictionary
	ByPrivate(creator string, group uint16, offset uint8) (Entry, bool)
}

// PrivateEntry describes a caller-supplied private attribute. This package does
// not distribute a vendor catalog or classify any attribute as safe to retain.
type PrivateEntry struct {
	Creator           string
	Group             uint16
	Offset            uint8
	VR                core.VR
	Keyword, Name, VM string
	Retired           bool
}
type privateKey struct {
	creator string
	group   uint16
	offset  uint8
}

// PrivateCatalog is immutable after construction. It has no absolute tag or
// keyword entries: use ByPrivate or resolve an actual dataset's reservations.
// Compose it with ordinary dictionaries using Chain in the desired precedence.
type PrivateCatalog struct {
	entries []PrivateEntry
	byKey   map[privateKey]Entry
}

func NewPrivateCatalog(entries []PrivateEntry) (*PrivateCatalog, error) {
	c := &PrivateCatalog{byKey: make(map[privateKey]Entry, len(entries))}
	for i, e := range entries {
		creator, err := PrivateCreatorID(e.Creator)
		vr, vrErr := core.ParseVR(string(e.VR))
		if err != nil || !IsPrivateGroup(e.Group) || vrErr != nil || vr == core.VRUN {
			return nil, fmt.Errorf("%w: entry %d", ErrPrivateDefinition, i)
		}
		key := privateKey{creator, e.Group, e.Offset}
		if _, ok := c.byKey[key]; ok {
			return nil, fmt.Errorf("%w: duplicate entry %d", ErrPrivateDefinition, i)
		}
		e.Creator = creator
		c.entries = append(c.entries, e)
		c.byKey[key] = Entry{Tag: core.NewTag(e.Group, uint16(e.Offset)), VR: e.VR, Keyword: e.Keyword, Name: e.Name, VM: e.VM, Retired: e.Retired}
	}
	return c, nil
}
func (*PrivateCatalog) ByTag(core.Tag) (Entry, bool)   { return Entry{}, false }
func (*PrivateCatalog) ByKeyword(string) (Entry, bool) { return Entry{}, false }
func (c *PrivateCatalog) ByPrivate(creator string, group uint16, offset uint8) (Entry, bool) {
	if c == nil {
		return Entry{}, false
	}
	creator, err := PrivateCreatorID(creator)
	if err != nil {
		return Entry{}, false
	}
	e, ok := c.byKey[privateKey{creator, group, offset}]
	return e, ok
}
func (c *PrivateCatalog) Entries() []PrivateEntry {
	if c == nil {
		return nil
	}
	return append([]PrivateEntry(nil), c.entries...)
}

// PrivateCreatorID removes only the ASCII SPACE padding permitted for LO.
// Case, internal whitespace and punctuation are significant. Private creators
// use the Default Character Repertoire regardless of Specific Character Set.
// Backslash, controls, escape sequences, NUL and non-ASCII bytes are invalid.
func PrivateCreatorID(value string) (string, error) {
	if len(value) > 64 {
		return "", ErrPrivateCreator
	}
	for _, b := range []byte(value) {
		if b < 0x20 || b > 0x7e || b == '\\' {
			return "", ErrPrivateCreator
		}
	}
	value = strings.Trim(value, " ")
	if value == "" {
		return "", ErrPrivateCreator
	}
	return value, nil
}

func IsPrivateGroup(group uint16) bool { return group >= 0x0009 && group != 0xffff && group&1 != 0 }
func IsPrivateCreatorTag(tag core.Tag) bool {
	return IsPrivateGroup(tag.Group) && tag.Element >= 0x0010 && tag.Element <= 0x00ff
}
func IsPrivateDataTag(tag core.Tag) bool { return IsPrivateGroup(tag.Group) && tag.Element >= 0x1000 }

// HasPrivateDictionary avoids activating creator resolution for a legacy Chain
// containing only ordinary absolute-tag overlays.
func HasPrivateDictionary(dict DataDictionary) bool {
	if chain, ok := dict.(*Chain); ok {
		return chain != nil && HasPrivateDictionary(*chain)
	}
	if chain, ok := dict.(Chain); ok {
		for _, d := range chain {
			if HasPrivateDictionary(d) {
				return true
			}
		}
		return false
	}
	_, ok := dict.(PrivateDataDictionary)
	return ok
}

func (c Chain) ByPrivate(creator string, group uint16, offset uint8) (Entry, bool) {
	for _, d := range c {
		if p, ok := d.(PrivateDataDictionary); ok {
			if e, found := p.ByPrivate(creator, group, offset); found {
				return e, true
			}
		}
	}
	return Entry{}, false
}

// LookupScopedEntry preserves Chain order across absolute-tag overlays and
// creator-aware definitions. A matching absolute entry (even UN) owns its VR.
// Missing/invalid creators must be passed as empty; no private VR is guessed.
func LookupScopedEntry(dict DataDictionary, tag core.Tag, creator string) (Entry, bool) {
	if dict == nil {
		return Entry{}, false
	}
	if chain, ok := dict.(*Chain); ok {
		if chain == nil {
			return Entry{}, false
		}
		return LookupScopedEntry(*chain, tag, creator)
	}
	if chain, ok := dict.(Chain); ok {
		for _, d := range chain {
			if e, found := LookupScopedEntry(d, tag, creator); found {
				return e, true
			}
		}
		return Entry{}, false
	}
	if e, ok := dict.ByTag(tag); ok {
		return e, true
	}
	if creator != "" && IsPrivateDataTag(tag) {
		if p, ok := dict.(PrivateDataDictionary); ok {
			if e, found := p.ByPrivate(creator, tag.Group, uint8(tag.Element)); found {
				e.Tag = tag
				return e, true
			}
		}
	}
	return Entry{}, false
}
