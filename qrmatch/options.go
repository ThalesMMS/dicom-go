package qrmatch

import (
	"context"
	"errors"

	"github.com/ThalesMMS/dicom-go/core"
)

var (
	ErrResourceLimit = errors.New("dicom qrmatch: resource limit exceeded")
	ErrCanceled      = errors.New("dicom qrmatch: canceled")
	ErrInvalidQuery  = errors.New("dicom qrmatch: invalid query")
)

// Limits bound query matching so a remote Identifier cannot induce unbounded
// work. Zero fields are replaced by defaults.
type Limits struct {
	MaxValues        int
	MaxSequenceItems int
	MaxSequenceDepth int
	MaxWildcardSteps int
	MaxValueBytes    int
}

func DefaultLimits() Limits {
	return Limits{
		MaxValues:        128,
		MaxSequenceItems: 64,
		MaxSequenceDepth: 8,
		MaxWildcardSteps: 250_000,
		MaxValueBytes:    16_384,
	}
}

func (limits Limits) normalized() Limits {
	defaults := DefaultLimits()
	if limits.MaxValues <= 0 {
		limits.MaxValues = defaults.MaxValues
	}
	if limits.MaxSequenceItems <= 0 {
		limits.MaxSequenceItems = defaults.MaxSequenceItems
	}
	if limits.MaxSequenceDepth <= 0 {
		limits.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if limits.MaxWildcardSteps <= 0 {
		limits.MaxWildcardSteps = defaults.MaxWildcardSteps
	}
	if limits.MaxValueBytes <= 0 {
		limits.MaxValueBytes = defaults.MaxValueBytes
	}
	return limits
}

// Options configure Match. SupportedMatching, when non-empty, lists tags that
// may constrain candidates. Present non-universal keys outside that set are
// reported as UnsupportedOptional and do not filter the archive.
type Options struct {
	Context           context.Context
	Limits            Limits
	SupportedMatching []core.Tag
}
