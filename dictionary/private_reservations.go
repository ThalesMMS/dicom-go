package dictionary

import "github.com/ThalesMMS/dicom-go/core"

const DefaultMaxPrivateCreators = 4096
const DefaultMaxPrivateIssues = 128

type privateReservation struct {
	creator string
	valid   bool
}
type creatorGroup struct {
	creator string
	group   uint16
}

// PrivateReservations holds one dataset/item's reservation state. Never share
// it with a child item or another parse. After construction/Observe, concurrent
// reads are safe provided no caller mutates it. Catalogs contain no such state.
type PrivateReservations struct {
	blocks    map[core.Tag]privateReservation
	creators  map[creatorGroup]core.Tag
	conflicts map[uint16]bool
	max       int
}

func NewPrivateReservations(maxCreators int) (*PrivateReservations, error) {
	if maxCreators < 0 {
		return nil, ErrPrivateResourceLimit
	}
	if maxCreators == 0 {
		maxCreators = DefaultMaxPrivateCreators
	}
	return &PrivateReservations{max: maxCreators}, nil
}

// Observe records a creator element. Invalid reservations stay unusable. A
// duplicate slot or creator in one group makes that group's dynamic resolution
// ambiguous and fail-closed, including previously observed slots in the group.
// Error strings contain no creator values.
func (s *PrivateReservations) Observe(e core.Element) error {
	if !IsPrivateCreatorTag(e.Tag()) {
		return ErrPrivateCreator
	}
	if _, ok := s.blocks[e.Tag()]; ok {
		if s.conflicts == nil {
			s.conflicts = map[uint16]bool{}
		}
		s.conflicts[e.Tag().Group] = true
		return ErrPrivateCreatorConflict
	}
	if len(s.blocks) >= s.max {
		return ErrPrivateResourceLimit
	}
	if s.blocks == nil {
		s.blocks = map[core.Tag]privateReservation{}
	}
	s.blocks[e.Tag()] = privateReservation{}
	if e.VR() != core.VRLO {
		return ErrPrivateCreator
	}
	var raw string
	switch v := e.Value.(type) {
	case core.RawValue:
		raw = string(v)
	case core.StringValue:
		if len(v) != 1 {
			return ErrPrivateCreator
		}
		raw = v[0]
	default:
		return ErrPrivateCreator
	}
	creator, err := PrivateCreatorID(raw)
	if err != nil {
		return err
	}
	key := creatorGroup{creator, e.Tag().Group}
	if _, ok := s.creators[key]; ok {
		if s.conflicts == nil {
			s.conflicts = map[uint16]bool{}
		}
		s.conflicts[e.Tag().Group] = true
		return ErrPrivateCreatorConflict
	}
	if s.creators == nil {
		s.creators = map[creatorGroup]core.Tag{}
	}
	s.creators[key] = e.Tag()
	s.blocks[e.Tag()] = privateReservation{creator: creator, valid: true}
	return nil
}

// Creator resolves the actual high-byte block used by a private data tag.
func (s *PrivateReservations) Creator(tag core.Tag) (string, error) {
	if !IsPrivateDataTag(tag) {
		return "", ErrPrivateCreator
	}
	if s == nil {
		return "", ErrPrivateCreatorMissing
	}
	if s.conflicts[tag.Group] {
		return "", ErrPrivateCreatorConflict
	}
	r, ok := s.blocks[core.NewTag(tag.Group, tag.Element>>8)]
	if !ok {
		return "", ErrPrivateCreatorMissing
	}
	if !r.valid {
		return "", ErrPrivateCreator
	}
	return r.creator, nil
}

func (s *PrivateReservations) Block(group uint16, creator string) (uint8, bool, error) {
	if !IsPrivateGroup(group) {
		return 0, false, ErrPrivateCreator
	}
	id, err := PrivateCreatorID(creator)
	if err != nil {
		return 0, false, err
	}
	if s == nil {
		return 0, false, nil
	}
	if s.conflicts[group] {
		return 0, false, ErrPrivateCreatorConflict
	}
	tag, ok := s.creators[creatorGroup{id, group}]
	return uint8(tag.Element), ok, nil
}

// PrivateReservationsFromElements builds only this dataset's scope, never
// descending into sequence items. It retains invalid/conflicting reservations
// so callers cannot accidentally resolve them after ignoring a reported issue.
// The returned issue list is capped at DefaultMaxPrivateIssues; all reservations
// are still checked. A resource-limit error aborts construction and returns nil.
func PrivateReservationsFromElements(elements []core.Element, maxCreators int) (*PrivateReservations, []PrivateIssue, error) {
	s, err := NewPrivateReservations(maxCreators)
	if err != nil {
		return nil, nil, err
	}
	var issues []PrivateIssue
	for _, e := range elements {
		if IsPrivateCreatorTag(e.Tag()) {
			if err := s.Observe(e); err != nil {
				if err == ErrPrivateResourceLimit {
					return nil, issues, err
				}
				if len(issues) < DefaultMaxPrivateIssues {
					issues = append(issues, PrivateIssue{Tag: e.Tag(), Err: err})
				}
			}
		}
	}
	return s, issues, nil
}

type PrivateIssue struct {
	Tag core.Tag
	Err error
}
