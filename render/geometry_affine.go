package render

import "math"

// GeometryAffine is a finite row-major 4x4 affine operating on homogeneous
// column vectors. Translation occupies indexes 3, 7, and 11. The final row is
// always [0,0,0,1].
type GeometryAffine [16]float64

// TransformPoint applies the affine to a patient- or voxel-space point.
func (a GeometryAffine) TransformPoint(point Vec3) Vec3 {
	return Vec3{
		X: a[0]*point.X + a[1]*point.Y + a[2]*point.Z + a[3],
		Y: a[4]*point.X + a[5]*point.Y + a[6]*point.Z + a[7],
		Z: a[8]*point.X + a[9]*point.Y + a[10]*point.Z + a[11],
	}
}

// Finite reports whether every coefficient is finite and the homogeneous final
// row matches [0,0,0,1] within the VolumeSnapshot v1 1e-12 tolerance.
func (a GeometryAffine) Finite() bool {
	for _, value := range a {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return false
		}
	}
	return math.Abs(a[12]) <= 1e-12 &&
		math.Abs(a[13]) <= 1e-12 &&
		math.Abs(a[14]) <= 1e-12 &&
		math.Abs(a[15]-1) <= 1e-12
}

func geometryAffinePair(origin, xStep, yStep, zStep Vec3) (indexToPatient, patientToIndex GeometryAffine, ok bool) {
	if !finiteVec3(origin) || !finiteVec3(xStep) || !finiteVec3(yStep) || !finiteVec3(zStep) {
		return GeometryAffine{}, GeometryAffine{}, false
	}
	indexToPatient = GeometryAffine{
		xStep.X, yStep.X, zStep.X, origin.X,
		xStep.Y, yStep.Y, zStep.Y, origin.Y,
		xStep.Z, yStep.Z, zStep.Z, origin.Z,
		0, 0, 0, 1,
	}

	a, b, c := xStep.X, yStep.X, zStep.X
	d, e, f := xStep.Y, yStep.Y, zStep.Y
	g, h, i := xStep.Z, yStep.Z, zStep.Z
	determinant := a*(e*i-f*h) - b*(d*i-f*g) + c*(d*h-e*g)
	scale := math.Max(xStep.Length(), math.Max(yStep.Length(), zStep.Length()))
	if !finitePositiveSpacing(scale) || math.Abs(determinant) <= 1e-15*scale*scale*scale {
		return GeometryAffine{}, GeometryAffine{}, false
	}
	invDet := 1 / determinant
	r00 := (e*i - f*h) * invDet
	r01 := (c*h - b*i) * invDet
	r02 := (b*f - c*e) * invDet
	r10 := (f*g - d*i) * invDet
	r11 := (a*i - c*g) * invDet
	r12 := (c*d - a*f) * invDet
	r20 := (d*h - e*g) * invDet
	r21 := (b*g - a*h) * invDet
	r22 := (a*e - b*d) * invDet
	patientToIndex = GeometryAffine{
		r00, r01, r02, -(r00*origin.X + r01*origin.Y + r02*origin.Z),
		r10, r11, r12, -(r10*origin.X + r11*origin.Y + r12*origin.Z),
		r20, r21, r22, -(r20*origin.X + r21*origin.Y + r22*origin.Z),
		0, 0, 0, 1,
	}
	return indexToPatient, patientToIndex, indexToPatient.Finite() && patientToIndex.Finite()
}
