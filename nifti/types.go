// Package nifti exports scalar DICOM image series as geometry-aware NIfTI-1
// volumes. It deliberately excludes presentation transforms: VOI, windowing,
// MONOCHROME1 inversion, GSPS, and display state never alter quantitative
// voxel values.
package nifti

import (
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/render"
)

const (
	DatatypeUint8   int16 = 2
	DatatypeInt16   int16 = 4
	DatatypeInt32   int16 = 8
	DatatypeFloat32 int16 = 16
	DatatypeFloat64 int16 = 64
	DatatypeInt8    int16 = 256
	DatatypeUint16  int16 = 512
	DatatypeUint32  int16 = 768
)

var (
	ErrInvalidSource       = errors.New("dicom/nifti: invalid source")
	ErrInvalidGeometry     = errors.New("dicom/nifti: invalid or unsupported geometry")
	ErrMixedIdentity       = errors.New("dicom/nifti: mixed series or frame of reference")
	ErrUnsupportedPixels   = errors.New("dicom/nifti: unsupported pixel representation")
	ErrUnsupportedScaling  = errors.New("dicom/nifti: unsupported scaling")
	ErrUnsupportedTemporal = errors.New("dicom/nifti: unsupported temporal dimension")
	ErrLimitExceeded       = errors.New("dicom/nifti: resource limit exceeded")
	ErrNIfTI2Required      = errors.New("dicom/nifti: NIfTI-2 required")
)

// ErrorCode is a stable, PHI-free failure classification.
type ErrorCode string

const (
	CodeSource              ErrorCode = "source"
	CodeIdentity            ErrorCode = "identity"
	CodeGeometry            ErrorCode = "geometry"
	CodeTemporal            ErrorCode = "temporal"
	CodePixels              ErrorCode = "pixels"
	CodeScaling             ErrorCode = "scaling"
	CodeLimit               ErrorCode = "limit"
	CodeNIfTI2              ErrorCode = "nifti2-required"
	CodeWrite               ErrorCode = "write"
	CodeCanceled            ErrorCode = "canceled"
	CodeQuadruped           ErrorCode = "quadruped-coordinate-system"
	CodeMultipleStacks      ErrorCode = "multiple-stacks"
	CodeIrregularTime       ErrorCode = "irregular-time"
	CodeMissingTiming       ErrorCode = "missing-time-spacing"
	CodeSingleSlice         ErrorCode = "single-slice"
	CodeUnsupportedModality ErrorCode = "unsupported-modality-transform"
)

// ExportError identifies a source/frame ordinal without echoing DICOM values,
// paths, UIDs, or arbitrary decoder text in Error(). The wrapped error remains
// available through errors.Is/errors.As for trusted callers.
type ExportError struct {
	Code        ErrorCode
	SourceIndex int // zero-based; -1 when not source-specific
	FrameIndex  int // zero-based; -1 when not frame-specific
	// GeometryIssue is a stable, non-PHI render guardrail reason when Code is
	// CodeGeometry. It is empty for non-geometric failures.
	GeometryIssue render.GeometryIssue
	Err           error
}

func (e *ExportError) Error() string {
	if e == nil {
		return "dicom/nifti: export failed"
	}
	location := ""
	if e.SourceIndex >= 0 {
		location = fmt.Sprintf(" at source %d", e.SourceIndex+1)
		if e.FrameIndex >= 0 {
			location += fmt.Sprintf(" frame %d", e.FrameIndex+1)
		}
	}
	code := strings.TrimSpace(string(e.Code))
	if code == "" {
		code = "export"
	}
	if e.GeometryIssue != render.GeometryIssueNone {
		code += " (" + string(e.GeometryIssue) + ")"
	}
	return "dicom/nifti: " + code + location
}

func (e *ExportError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type Compression uint8

const (
	CompressionNone Compression = iota
	CompressionGZIP
)

func (c Compression) String() string {
	if c == CompressionGZIP {
		return "gzip"
	}
	return "none"
}

type ScalingPolicy uint8

const (
	// ScalingPreserveUniform writes normalized stored integers and preserves one
	// uniform linear transform in scl_slope/scl_inter.
	ScalingPreserveUniform ScalingPolicy = iota
	// ScalingApplyFloat32 applies every frame's slope/intercept and writes F32.
	ScalingApplyFloat32
	// ScalingApplyFloat64 applies every frame's slope/intercept and writes F64.
	ScalingApplyFloat64
)

func (p ScalingPolicy) String() string {
	switch p {
	case ScalingApplyFloat32:
		return "apply-float32"
	case ScalingApplyFloat64:
		return "apply-float64"
	default:
		return "preserve-uniform"
	}
}

type GeometryPolicy uint8

const (
	GeometryStrict GeometryPolicy = iota
	// GeometryResampleLinear permits only geometry classified as regularizable
	// by render. Unsupported/mixed geometry still fails closed.
	GeometryResampleLinear
)

type Limits struct {
	MaxInstances         int
	MaxFrames            int
	MaxVoxels            uint64
	MaxUncompressedBytes uint64
	MaxSpoolBytes        uint64
	MaxResampleVoxels    uint64
	// MaxInMemorySourceBytes bounds the fallback used by sources/codecs that
	// cannot stream one decoded frame at a time. Native path sources use a
	// FrameSink and remain O(one frame); oversized compressed sources fail
	// explicitly instead of materializing without a ceiling.
	MaxInMemorySourceBytes uint64
}

func DefaultLimits() Limits {
	return Limits{
		MaxInstances:           100_000,
		MaxFrames:              1_000_000,
		MaxVoxels:              1 << 32,
		MaxUncompressedBytes:   16 << 30,
		MaxSpoolBytes:          16 << 30,
		MaxResampleVoxels:      256 << 20,
		MaxInMemorySourceBytes: 512 << 20,
	}
}

// StrictGeometryTolerances are intentionally tighter than the viewer's
// display-oriented defaults. NIfTI encodes one regular affine grid.
func StrictGeometryTolerances() render.GeometryTolerances {
	return render.GeometryTolerances{
		SpacingRel:      0.001,
		SpacingAbs:      0.001,
		OrientationCos:  math.Cos(0.1 * math.Pi / 180),
		TiltCos:         math.Cos(0.1 * math.Pi / 180),
		PositionAbs:     1e-6,
		ShearAbs:        math.Tan(0.1 * math.Pi / 180),
		AffineRoundTrip: 1e-9,
	}
}

type Options struct {
	Compression Compression
	Scaling     ScalingPolicy
	Geometry    GeometryPolicy
	// TemporalSpacingSeconds explicitly supplies uniform spacing for a resolved
	// temporal dimension whose DICOM metadata identifies order but not timing.
	// It is ignored for 3D exports and must be finite and positive when used.
	TemporalSpacingSeconds float64
	Tolerances             render.GeometryTolerances
	Limits                 Limits
	TempDir                string
}

func DefaultOptions() Options {
	return Options{
		Tolerances: StrictGeometryTolerances(),
		Limits:     DefaultLimits(),
	}
}

type Report struct {
	Dimensions       [4]int
	Datatype         int16
	BitPix           int16
	VoxelOffset      int64
	IndexToRAS       render.GeometryAffine
	ScalingPolicy    ScalingPolicy
	ScalingSlope     float64
	ScalingIntercept float64
	Compression      Compression
	InputReordered   bool
	Resampled        bool
	Interpolation    string
	Warnings         []string
	BytesWritten     int64
	Sidecar          Sidecar
}

func normalizedOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	if options.Compression > CompressionGZIP {
		return Options{}, &ExportError{Code: CodeWrite, SourceIndex: -1, FrameIndex: -1, Err: ErrInvalidSource}
	}
	if options.Scaling > ScalingApplyFloat64 {
		return Options{}, &ExportError{Code: CodeScaling, SourceIndex: -1, FrameIndex: -1, Err: ErrUnsupportedScaling}
	}
	if options.Geometry > GeometryResampleLinear {
		return Options{}, &ExportError{Code: CodeGeometry, SourceIndex: -1, FrameIndex: -1, Err: ErrInvalidGeometry}
	}
	if options.Tolerances == (render.GeometryTolerances{}) {
		options.Tolerances = defaults.Tolerances
	}
	if options.Limits.MaxInstances == 0 {
		options.Limits.MaxInstances = defaults.Limits.MaxInstances
	}
	if options.Limits.MaxFrames == 0 {
		options.Limits.MaxFrames = defaults.Limits.MaxFrames
	}
	if options.Limits.MaxVoxels == 0 {
		options.Limits.MaxVoxels = defaults.Limits.MaxVoxels
	}
	if options.Limits.MaxUncompressedBytes == 0 {
		options.Limits.MaxUncompressedBytes = defaults.Limits.MaxUncompressedBytes
	}
	if options.Limits.MaxSpoolBytes == 0 {
		options.Limits.MaxSpoolBytes = defaults.Limits.MaxSpoolBytes
	}
	if options.Limits.MaxResampleVoxels == 0 {
		options.Limits.MaxResampleVoxels = defaults.Limits.MaxResampleVoxels
	}
	if options.Limits.MaxInMemorySourceBytes == 0 {
		options.Limits.MaxInMemorySourceBytes = defaults.Limits.MaxInMemorySourceBytes
	}
	if options.Limits.MaxInstances < 0 || options.Limits.MaxFrames < 0 {
		return Options{}, &ExportError{Code: CodeLimit, SourceIndex: -1, FrameIndex: -1, Err: ErrLimitExceeded}
	}
	return options, nil
}
