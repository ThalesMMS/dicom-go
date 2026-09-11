package render

import (
	"errors"
	"fmt"
)

var (
	// ErrCPRInput reports an invalid CPR path or render request.
	ErrCPRInput = errors.New("render: invalid CPR input")
	// ErrCPRLimit reports a CPR request that exceeds a hard resource bound.
	ErrCPRLimit = errors.New("render: CPR resource limit exceeded")
)

const (
	MaxCPRControlPoints    = 65_536
	MaxCPRPathLengthMM     = 1_000_000_000
	MaxCPRPathSamples      = 1_000_000
	MaxCPROutputDimension  = 8_192
	MaxCPROutputPixels     = 32 * 1024 * 1024
	MaxCPRSlabSamples      = 4_097
	MaxCPRSampleOperations = 128 * 1024 * 1024
)

// CPRInputError identifies the invalid field without requiring callers to
// parse an error string. It matches ErrCPRInput through errors.Is.
type CPRInputError struct {
	Field  string
	Reason string
}

func (e *CPRInputError) Error() string {
	if e == nil {
		return ""
	}
	if e.Field == "" {
		return fmt.Sprintf("%v: %s", ErrCPRInput, e.Reason)
	}
	return fmt.Sprintf("%v: %s: %s", ErrCPRInput, e.Field, e.Reason)
}

func (e *CPRInputError) Unwrap() error { return ErrCPRInput }

// CPRLimitError identifies which bounded CPR resource exceeded its limit. It
// matches ErrCPRLimit through errors.Is.
type CPRLimitError struct {
	Resource string
	Value    uint64
	Limit    uint64
}

func (e *CPRLimitError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%v: %s=%d, limit=%d", ErrCPRLimit, e.Resource, e.Value, e.Limit)
}

func (e *CPRLimitError) Unwrap() error { return ErrCPRLimit }

func checkedCPRProduct(resource string, limit uint64, values ...uint64) (uint64, error) {
	product := uint64(1)
	for _, value := range values {
		if value != 0 && product > limit/value {
			return 0, &CPRLimitError{Resource: resource, Value: limit + 1, Limit: limit}
		}
		product *= value
		if product > limit {
			return 0, &CPRLimitError{Resource: resource, Value: product, Limit: limit}
		}
	}
	return product, nil
}
