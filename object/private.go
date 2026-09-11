package object

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/parser"
)

type PrivateDiagnostic = parser.PrivateDiagnostic

// ParsedPrivateDiagnostics returns the read operation's bounded diagnostic
// snapshot. Item offsets distinguish nested scopes; subsequent object edits do
// not rewrite this historical parse report. Values/creator strings are omitted.
func (o *Object) ParsedPrivateDiagnostics() ([]PrivateDiagnostic, bool) {
	if o == nil {
		return nil, false
	}
	return append([]PrivateDiagnostic(nil), o.privateDiagnostics...), o.privateDiagnosticsTruncated
}

func (o *Object) capturePrivateScope(elements []core.Element) {
	// Retain only duplicate creator slots even without an opted-in catalog:
	// a later inspection catalog must not hide ambiguity behind last-wins.
	var seen map[core.Tag]bool
	for _, e := range elements {
		if !dictionary.IsPrivateCreatorTag(e.Tag()) {
			continue
		}
		if seen[e.Tag()] {
			if o.privateDuplicateSlots == nil {
				o.privateDuplicateSlots = map[core.Tag]bool{}
			}
			o.privateDuplicateSlots[e.Tag()] = true
		}
		if seen == nil {
			seen = map[core.Tag]bool{}
		}
		seen[e.Tag()] = true
	}
	if !dictionary.HasPrivateDictionary(o.dict) {
		return
	}
	// The elements already exist in this materialized object. The streaming
	// reader enforces its separate per-scope budget before object construction.
	o.privateScope, _, o.privateScopeError = dictionary.PrivateReservationsFromElements(elements, len(elements))
}
func (o *Object) privateReservations() (*dictionary.PrivateReservations, error) {
	if o == nil {
		return nil, dictionary.ErrPrivateCreatorMissing
	}
	if o.privateScope == nil && o.privateScopeError == nil {
		o.privateScope, _, o.privateScopeError = dictionary.PrivateReservationsFromElements(o.Elements(), len(o.elements))
		if o.privateScope != nil {
			for tag := range o.privateDuplicateSlots {
				// Observing the surviving slot a second time marks its group
				// ambiguous without retaining earlier creator values.
				_ = o.privateScope.Observe(o.elements[tag])
			}
		}
	}
	return o.privateScope, o.privateScopeError
}

// ResolvePrivateEntry resolves the actual reserved block in this Object only.
// Explicit VRs in its elements are never modified. An unknown catalog entry or
// invalid/ambiguous reservation produces a typed error, not a heuristic VR.
func (o *Object) ResolvePrivateEntry(tag core.Tag) (dictionary.Entry, error) {
	s, err := o.privateReservations()
	if err != nil {
		return dictionary.Entry{}, err
	}
	creator, err := s.Creator(tag)
	if err != nil {
		return dictionary.Entry{}, err
	}
	e, ok := dictionary.LookupScopedEntry(o.dict, tag, creator)
	if !ok {
		return dictionary.Entry{}, dictionary.ErrPrivateDefinitionMissing
	}
	return e, nil
}

// PrivateTag finds an already reserved logical attribute. It does not reserve
// blocks, move attributes or inherit a parent dataset's reservation.
func (o *Object) PrivateTag(group uint16, creator string, offset uint8) (core.Tag, error) {
	s, err := o.privateReservations()
	if err != nil {
		return core.Tag{}, err
	}
	block, ok, err := s.Block(group, creator)
	if err != nil {
		return core.Tag{}, err
	}
	if !ok {
		return core.Tag{}, dictionary.ErrPrivateCreatorMissing
	}
	return core.NewTag(group, uint16(block)<<8|uint16(offset)), nil
}

// ReservePrivateCreator reuses an unambiguous existing reservation or inserts
// an LO/VM1 creator in the first unused block (10..FF). A block with orphaned
// private attributes is occupied, even when its creator element is absent.
// No attribute is remapped. The Object is changed only after all checks pass.
func (o *Object) ReservePrivateCreator(group uint16, creator string) (uint8, error) {
	if o == nil || !dictionary.IsPrivateGroup(group) {
		return 0, dictionary.ErrPrivateCreator
	}
	id, err := dictionary.PrivateCreatorID(creator)
	if err != nil {
		return 0, err
	}
	s, err := o.privateReservations()
	if err != nil {
		return 0, err
	}
	block, ok, err := s.Block(group, id)
	if err != nil {
		return 0, err
	}
	if ok {
		return block, nil
	}
	var occupied [256]bool
	for tag, e := range o.elements {
		if tag.Group != group {
			continue
		}
		if dictionary.IsPrivateCreatorTag(tag) {
			occupied[tag.Element] = true
			// Refuse authorship in a group with malformed reservations.
			probe, _ := dictionary.NewPrivateReservations(1)
			if err := probe.Observe(e); err != nil {
				return 0, err
			}
		} else if dictionary.IsPrivateDataTag(tag) {
			occupied[tag.Element>>8] = true
		}
	}
	for candidate := 16; candidate <= 255; candidate++ {
		if !occupied[candidate] {
			o.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(group, uint16(candidate)), VR: core.VRLO}, Value: core.StringValue{id}})
			return uint8(candidate), nil
		}
	}
	return 0, dictionary.ErrPrivateBlocksExhausted
}

func (o *Object) scopedSummaryEntry(dict dictionary.DataDictionary, tag core.Tag) (dictionary.Entry, bool) {
	if !dictionary.HasPrivateDictionary(dict) {
		return dict.ByTag(tag)
	}
	creator := ""
	if dictionary.IsPrivateDataTag(tag) {
		if scope, err := o.privateReservations(); err == nil {
			creator, _ = scope.Creator(tag)
		}
	}
	return dictionary.LookupScopedEntry(dict, tag, creator)
}

// ValidatePrivateReservations reports reservation errors for authorship without
// inferring whether private values are identifying or safe for de-identification.
func (o *Object) ValidatePrivateReservations() error {
	if o == nil {
		return dictionary.ErrPrivateCreator
	}
	_, issues, err := dictionary.PrivateReservationsFromElements(o.Elements(), len(o.elements))
	if err != nil {
		return err
	}
	var result error
	for _, issue := range issues {
		result = errors.Join(result, fmt.Errorf("%s: %w", issue.Tag, issue.Err))
	}
	// Preserve duplicate-slot ambiguity retained from the original dataset,
	// even though Object's ordinary value map uses last-wins semantics.
	if scope, err := o.privateReservations(); err != nil {
		return errors.Join(result, err)
	} else {
		for tag := range o.elements {
			if dictionary.IsPrivateCreatorTag(tag) {
				if _, err := scope.Creator(core.NewTag(tag.Group, tag.Element<<8)); errors.Is(err, dictionary.ErrPrivateCreatorConflict) {
					return errors.Join(result, err)
				}
			}
		}
	}
	return result
}
