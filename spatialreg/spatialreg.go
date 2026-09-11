// Package spatialreg parses and applies DICOM Spatial Registration objects.
// Affine transforms expose their validated forward and inverse directions.
// Deformable transforms are exposed only in the registered-to-source direction
// defined by DICOM. The package does not infer an inverse deformation field.
package spatialreg

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/render"
)

const DeformableSpatialRegistrationStorage = "1.2.840.10008.5.1.4.1.1.66.3"

const (
	DefaultMaxGridBytes     uint64 = 128 << 20
	DefaultMaxGridVoxels    uint64 = 8 * 1024 * 1024
	DefaultMaxRegistrations        = 64
)

var (
	ErrUnsupportedSOPClass = errors.New("dicom/spatialreg: unsupported SOP class")
	ErrInvalidObject       = errors.New("dicom/spatialreg: invalid object")
	ErrMemoryLimit         = errors.New("dicom/spatialreg: memory limit exceeded")
	ErrOutsideDomain       = errors.New("dicom/spatialreg: point outside deformation domain")
	ErrUndefinedDomain     = errors.New("dicom/spatialreg: deformation undefined at point")
)

type Limit string

const (
	LimitGridBytes     Limit = "grid-bytes"
	LimitGridVoxels    Limit = "grid-voxels"
	LimitRegistrations Limit = "registrations"
)

// LimitError identifies the configured resource budget that rejected an
// object. It unwraps to ErrMemoryLimit for coarse error handling.
type LimitError struct {
	Limit   Limit
	Value   uint64
	Maximum uint64
}

func (e *LimitError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%v: %s value %d exceeds maximum %d", ErrMemoryLimit, e.Limit, e.Value, e.Maximum)
}

func (e *LimitError) Unwrap() error { return ErrMemoryLimit }

// Options bounds allocations performed while decoding a registration object.
// Zero fields use the package defaults, so parsing is bounded by default.
type Options struct {
	MaxGridBytes     uint64
	MaxGridVoxels    uint64
	MaxRegistrations int
}

func (o Options) normalized() Options {
	if o.MaxGridBytes == 0 {
		o.MaxGridBytes = DefaultMaxGridBytes
	}
	if o.MaxGridVoxels == 0 {
		o.MaxGridVoxels = DefaultMaxGridVoxels
	}
	if o.MaxRegistrations == 0 {
		o.MaxRegistrations = DefaultMaxRegistrations
	}
	return o
}

// Matrix is a row-major homogeneous transform applied to column-vector points.
type Matrix struct {
	Type   string
	Values render.GeometryAffine
}

func identityMatrix() Matrix {
	return Matrix{Values: render.GeometryAffine{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}}
}

// Grid is a validated deformation field. Dimensions are X, Y, and Z; vectors
// are stored with X varying fastest, followed by Y and Z.
type Grid struct {
	Origin          render.Vec3
	RowDirection    render.Vec3
	ColumnDirection render.Vec3
	NormalDirection render.Vec3
	Spacing         render.Vec3
	Dimensions      [3]uint32
	vectors         []vector3f
}

type vector3f struct {
	X float32
	Y float32
	Z float32
}

// VectorAt returns the displacement at an integer grid coordinate. ok is false
// for an out-of-range coordinate or the DICOM undefined triple NaN sentinel.
func (g *Grid) VectorAt(x, y, z int) (render.Vec3, bool) {
	if g == nil || x < 0 || y < 0 || z < 0 ||
		x >= int(g.Dimensions[0]) || y >= int(g.Dimensions[1]) || z >= int(g.Dimensions[2]) {
		return render.Vec3{}, false
	}
	index := (z*int(g.Dimensions[1])+y)*int(g.Dimensions[0]) + x
	if index < 0 || index >= len(g.vectors) {
		return render.Vec3{}, false
	}
	value := g.vectors[index]
	if undefinedVector(value) {
		return render.Vec3{}, false
	}
	return render.Vec3{X: float64(value.X), Y: float64(value.Y), Z: float64(value.Z)}, true
}

// Registration maps coordinates in the registered Frame of Reference to
// sampling coordinates in SourceFrameOfReferenceUID.
type Registration struct {
	SourceFrameOfReferenceUID string
	ReferencedSOPInstanceUIDs []string
	Pre                       Matrix
	Post                      Matrix
	Grid                      *Grid
}

// Object is one validated Deformable Spatial Registration instance.
type Object struct {
	SOPInstanceUID                string
	RegisteredFrameOfReferenceUID string
	Registrations                 []Registration
}

// MapRegisteredToSource applies pre-matrix, interpolated displacement, then
// post-matrix. It never attempts to invert a deformation field.
func (r *Registration) MapRegisteredToSource(point render.Vec3) (render.Vec3, error) {
	if r == nil || !finiteVec(point) {
		return render.Vec3{}, fmt.Errorf("%w: non-finite registered point", ErrInvalidObject)
	}
	if !r.Pre.Values.Finite() || !r.Post.Values.Finite() {
		return render.Vec3{}, fmt.Errorf("%w: pre or post matrix is invalid", ErrInvalidObject)
	}
	transformed := r.Pre.Values.TransformPoint(point)
	if r.Grid != nil {
		displacement, err := r.Grid.Sample(point)
		if err != nil {
			return render.Vec3{}, err
		}
		transformed = transformed.Add(displacement)
	}
	source := r.Post.Values.TransformPoint(transformed)
	if !finiteVec(source) {
		return render.Vec3{}, fmt.Errorf("%w: transform produced a non-finite source point", ErrInvalidObject)
	}
	return source, nil
}
