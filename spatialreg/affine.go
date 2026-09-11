package spatialreg

import (
	"errors"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/render"
)

// SpatialRegistrationStorage is the DICOM Spatial Registration Storage SOP Class.
const SpatialRegistrationStorage = "1.2.840.10008.5.1.4.1.1.66.1"

// TransformType is the DICOM Frame of Reference Transformation Matrix Type.
type TransformType string

const (
	TransformRigid      TransformType = "RIGID"
	TransformRigidScale TransformType = "RIGID_SCALE"
	TransformAffine     TransformType = "AFFINE"
)

var (
	ErrInvalidMatrix     = errors.New("dicom/spatialreg: invalid matrix")
	ErrSingularMatrix    = errors.New("dicom/spatialreg: singular matrix")
	ErrIllConditioned    = errors.New("dicom/spatialreg: ill-conditioned matrix")
	ErrUnsupportedMatrix = errors.New("dicom/spatialreg: unsupported matrix type")
)

// MatrixError identifies the matrix and validation category that made a
// Spatial Registration object unusable. Index is zero-based within Matrix
// Sequence.
type MatrixError struct {
	Index     int
	Type      TransformType
	Condition float64
	Err       error
}

func (e *MatrixError) Error() string {
	if e == nil {
		return ErrInvalidMatrix.Error()
	}
	if e.Condition > 0 {
		return fmt.Sprintf("matrix %d (%s): %v (condition %.6g)", e.Index, e.Type, e.Err, e.Condition)
	}
	return fmt.Sprintf("matrix %d (%s): %v", e.Index, e.Type, e.Err)
}

func (e *MatrixError) Unwrap() error {
	if e == nil || e.Err == nil {
		return ErrInvalidMatrix
	}
	return e.Err
}

// AffineRegistration describes one validated Source-to-Registered mapping.
// RegisteredToSource is the explicitly computed inverse used when resampling
// source data into the registered coordinate system.
type AffineRegistration struct {
	SourceFrameOfReferenceUID string
	ReferencedSOPInstanceUIDs []string
	Matrices                  []Matrix
	Type                      TransformType
	SourceToRegistered        render.GeometryAffine
	RegisteredToSource        render.GeometryAffine
}

// AffineObject is one validated Spatial Registration Storage instance.
type AffineObject struct {
	SOPInstanceUID                string
	RegisteredFrameOfReferenceUID string
	Registrations                 []AffineRegistration
}

// MapSourceToRegistered maps a finite patient point from the source RCS into
// the registered RCS.
func (r *AffineRegistration) MapSourceToRegistered(point render.Vec3) (render.Vec3, error) {
	if r == nil || !finiteVec(point) {
		return render.Vec3{}, fmt.Errorf("%w: non-finite source point", ErrInvalidObject)
	}
	return checkedTransformPoint(r.SourceToRegistered, point)
}

// MapRegisteredToSource maps a finite patient point from the registered RCS
// back into the source RCS.
func (r *AffineRegistration) MapRegisteredToSource(point render.Vec3) (render.Vec3, error) {
	if r == nil || !finiteVec(point) {
		return render.Vec3{}, fmt.Errorf("%w: non-finite registered point", ErrInvalidObject)
	}
	return checkedTransformPoint(r.RegisteredToSource, point)
}

func checkedTransformPoint(matrix render.GeometryAffine, point render.Vec3) (render.Vec3, error) {
	if !matrix.Finite() {
		return render.Vec3{}, fmt.Errorf("%w: transform is invalid", ErrInvalidMatrix)
	}
	out := matrix.TransformPoint(point)
	if !finiteVec(out) {
		return render.Vec3{}, fmt.Errorf("%w: transform produced a non-finite point", ErrInvalidMatrix)
	}
	return out, nil
}

func identityAffine() render.GeometryAffine {
	return render.GeometryAffine{
		1, 0, 0, 0,
		0, 1, 0, 0,
		0, 0, 1, 0,
		0, 0, 0, 1,
	}
}

// multiplyAffine returns left*right for column-vector transforms.
func multiplyAffine(left, right render.GeometryAffine) render.GeometryAffine {
	var out render.GeometryAffine
	for row := 0; row < 4; row++ {
		for column := 0; column < 4; column++ {
			for k := 0; k < 4; k++ {
				out[row*4+column] += left[row*4+k] * right[k*4+column]
			}
		}
	}
	return out
}

func inverseAffine(matrix render.GeometryAffine) (render.GeometryAffine, float64, error) {
	if !matrix.Finite() {
		return render.GeometryAffine{}, 0, ErrInvalidMatrix
	}
	a, b, c := matrix[0], matrix[1], matrix[2]
	d, e, f := matrix[4], matrix[5], matrix[6]
	g, h, i := matrix[8], matrix[9], matrix[10]
	determinant := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	norm := matrix3InfinityNorm(matrix)
	if !finite(norm) || norm == 0 || math.Abs(determinant) <= 1e-15*norm*norm*norm {
		return render.GeometryAffine{}, math.Inf(1), ErrSingularMatrix
	}
	invDet := 1 / determinant
	inverse := render.GeometryAffine{
		(e*i - f*h) * invDet, (c*h - b*i) * invDet, (b*f - c*e) * invDet, 0,
		(f*g - d*i) * invDet, (a*i - c*g) * invDet, (c*d - a*f) * invDet, 0,
		(d*h - e*g) * invDet, (b*g - a*h) * invDet, (a*e - b*d) * invDet, 0,
		0, 0, 0, 1,
	}
	inverse[3] = -(inverse[0]*matrix[3] + inverse[1]*matrix[7] + inverse[2]*matrix[11])
	inverse[7] = -(inverse[4]*matrix[3] + inverse[5]*matrix[7] + inverse[6]*matrix[11])
	inverse[11] = -(inverse[8]*matrix[3] + inverse[9]*matrix[7] + inverse[10]*matrix[11])
	condition := norm * matrix3InfinityNorm(inverse)
	if !finite(condition) || condition > 1e10 {
		return render.GeometryAffine{}, condition, ErrIllConditioned
	}
	return inverse, condition, nil
}

func matrix3InfinityNorm(matrix render.GeometryAffine) float64 {
	maximum := 0.0
	for row := 0; row < 3; row++ {
		sum := math.Abs(matrix[row*4]) + math.Abs(matrix[row*4+1]) + math.Abs(matrix[row*4+2])
		maximum = math.Max(maximum, sum)
	}
	return maximum
}

func validateMatrixType(matrix render.GeometryAffine, kind TransformType) error {
	if !matrix.Finite() {
		return ErrInvalidMatrix
	}
	columns := [3]render.Vec3{
		{X: matrix[0], Y: matrix[4], Z: matrix[8]},
		{X: matrix[1], Y: matrix[5], Z: matrix[9]},
		{X: matrix[2], Y: matrix[6], Z: matrix[10]},
	}
	const tolerance = 1e-6
	scales := [3]float64{columns[0].Length(), columns[1].Length(), columns[2].Length()}
	for _, scale := range scales {
		if !finite(scale) || scale <= 1e-12 {
			return ErrSingularMatrix
		}
	}
	determinant := columns[0].Dot(columns[1].Cross(columns[2]))
	switch kind {
	case TransformRigid:
		for _, scale := range scales {
			if math.Abs(scale-1) > tolerance {
				return ErrInvalidMatrix
			}
		}
		if math.Abs(determinant-1) > tolerance {
			return ErrInvalidMatrix
		}
	case TransformRigidScale:
		for left := 0; left < 3; left++ {
			for right := left + 1; right < 3; right++ {
				if math.Abs(columns[left].Dot(columns[right])) > tolerance*scales[left]*scales[right] {
					return ErrInvalidMatrix
				}
			}
		}
		if determinant <= 0 {
			return ErrInvalidMatrix
		}
	case TransformAffine:
		// Invertibility and conditioning are checked below.
	default:
		return ErrUnsupportedMatrix
	}
	_, _, err := inverseAffine(matrix)
	return err
}
