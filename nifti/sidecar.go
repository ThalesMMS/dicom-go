// Package nifti provides NIfTI export primitives.
package nifti

import (
	"encoding/json"
	"errors"
	"fmt"
)

// SidecarSchema identifies the closed, non-PHI NIfTI sidecar schema.
const SidecarSchema = "dicom-go.nifti.sidecar/v1"

// ErrInvalidSidecarLimit reports a non-positive sidecar byte limit.
var ErrInvalidSidecarLimit = errors.New("nifti: sidecar maxBytes must be positive")

// ErrSidecarTooLarge identifies sidecar output that exceeds its byte limit.
var ErrSidecarTooLarge = errors.New("nifti: sidecar exceeds byte limit")

// SidecarSizeError reports the encoded size and configured maximum without
// including any sidecar content in the error.
type SidecarSizeError struct {
	ActualBytes int
	MaxBytes    int
}

func (e *SidecarSizeError) Error() string {
	return fmt.Sprintf("%v: encoded=%d max=%d", ErrSidecarTooLarge, e.ActualBytes, e.MaxBytes)
}

// Unwrap permits callers to classify the error with errors.Is.
func (e *SidecarSizeError) Unwrap() error { return ErrSidecarTooLarge }

// CodedUnits identifies measurement units without a free-text meaning.
type CodedUnits struct {
	Code   string `json:"code"`
	Scheme string `json:"scheme"`
}

// Sidecar contains the allowlisted metadata emitted beside a NIfTI file.
// It intentionally has no fields for DICOM identifiers, paths, patient data,
// dates, or descriptions. Warnings are machine-readable codes, not messages.
type Sidecar struct {
	Dimensions               [4]int
	Datatype                 string
	ScalingPolicy            string
	Reordered                bool
	Resampled                bool
	Interpolation            string
	Units                    CodedUnits
	Quantity                 CodedUnits
	TemporalOffsetsSeconds   []float64
	TemporalDurationsSeconds []float64
	Warnings                 []string
}

// Marshal returns the deterministic JSON representation of s, provided it is
// no larger than maxBytes.
func (s Sidecar) Marshal(maxBytes int) ([]byte, error) {
	if maxBytes <= 0 {
		return nil, ErrInvalidSidecarLimit
	}

	encoded, err := json.Marshal(struct {
		Schema                   string     `json:"schema"`
		Dimensions               [4]int     `json:"dimensions"`
		Datatype                 string     `json:"datatype"`
		ScalingPolicy            string     `json:"scaling_policy"`
		Reordered                bool       `json:"reordered"`
		Resampled                bool       `json:"resampled"`
		Interpolation            string     `json:"interpolation"`
		Units                    CodedUnits `json:"units"`
		Quantity                 CodedUnits `json:"quantity"`
		TemporalOffsetsSeconds   []float64  `json:"temporal_offsets_seconds"`
		TemporalDurationsSeconds []float64  `json:"temporal_durations_seconds"`
		Warnings                 []string   `json:"warnings"`
	}{
		Schema:                   SidecarSchema,
		Dimensions:               s.Dimensions,
		Datatype:                 s.Datatype,
		ScalingPolicy:            s.ScalingPolicy,
		Reordered:                s.Reordered,
		Resampled:                s.Resampled,
		Interpolation:            s.Interpolation,
		Units:                    s.Units,
		Quantity:                 s.Quantity,
		TemporalOffsetsSeconds:   s.TemporalOffsetsSeconds,
		TemporalDurationsSeconds: s.TemporalDurationsSeconds,
		Warnings:                 s.Warnings,
	})
	if err != nil {
		return nil, err
	}
	if len(encoded) > maxBytes {
		return nil, &SidecarSizeError{ActualBytes: len(encoded), MaxBytes: maxBytes}
	}
	return encoded, nil
}
