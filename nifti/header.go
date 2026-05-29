package nifti

import (
	"encoding/binary"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/render"
)

const (
	headerSize        = 348
	headerDescription = "dicom-go NIfTI-1 export"
)

type headerSpec struct {
	Dimensions  [4]int
	Datatype    int16
	BitPix      int16
	Spacing     [4]float64
	VoxelOffset int64
	Slope       float64
	Intercept   float64
	AffineRAS   render.GeometryAffine
}

func buildHeader(spec headerSpec) ([]byte, error) {
	if err := validateHeaderSpec(spec); err != nil {
		return nil, err
	}
	header := make([]byte, headerSize)
	putInt16 := func(offset int, value int16) {
		binary.LittleEndian.PutUint16(header[offset:offset+2], uint16(value))
	}
	putInt32 := func(offset int, value int32) {
		binary.LittleEndian.PutUint32(header[offset:offset+4], uint32(value))
	}
	putFloat32 := func(offset int, value float64) {
		binary.LittleEndian.PutUint32(header[offset:offset+4], math.Float32bits(float32(value)))
	}

	putInt32(0, headerSize)
	dimensionCount := int16(3)
	if spec.Dimensions[3] > 1 {
		dimensionCount = 4
	}
	putInt16(40, dimensionCount)
	for index, dimension := range spec.Dimensions {
		putInt16(42+2*index, int16(dimension))
	}
	for index := 5; index < 8; index++ {
		putInt16(40+2*index, 1)
	}
	putInt16(70, spec.Datatype)
	putInt16(72, spec.BitPix)
	quaternion, qfac := affineQuaternion(spec.AffineRAS, spec.Spacing)
	putFloat32(76, qfac)
	for index, spacing := range spec.Spacing {
		putFloat32(80+4*index, spacing)
	}
	putFloat32(108, float64(spec.VoxelOffset))
	putFloat32(112, spec.Slope)
	putFloat32(116, spec.Intercept)
	header[123] = 2
	if spec.Dimensions[3] > 1 {
		header[123] |= 8
	}
	copy(header[148:228], headerDescription)
	putInt16(252, 1)
	putInt16(254, 1)
	putFloat32(256, quaternion[0])
	putFloat32(260, quaternion[1])
	putFloat32(264, quaternion[2])
	putFloat32(268, spec.AffineRAS[3])
	putFloat32(272, spec.AffineRAS[7])
	putFloat32(276, spec.AffineRAS[11])
	for row, offset := range []int{280, 296, 312} {
		for column := 0; column < 4; column++ {
			putFloat32(offset+4*column, spec.AffineRAS[4*row+column])
		}
	}
	copy(header[344:348], "n+1\x00")
	return header, nil
}

func validateHeaderSpec(spec headerSpec) error {
	for index, dimension := range spec.Dimensions {
		if dimension < 1 || dimension > math.MaxInt16 {
			return unsupportedHeader(fmt.Sprintf("dimension %d is %d, want 1..32767", index+1, dimension))
		}
	}
	wantBitPix, ok := supportedDatatypeBitPix(spec.Datatype)
	if !ok {
		return unsupportedHeader(fmt.Sprintf("unsupported datatype %d", spec.Datatype))
	}
	if spec.BitPix != wantBitPix {
		return unsupportedHeader(fmt.Sprintf("datatype %d requires bitpix %d, got %d", spec.Datatype, wantBitPix, spec.BitPix))
	}
	for index, spacing := range spec.Spacing {
		if spacing <= 0 || float32(spacing) <= 0 || !fitsFiniteFloat32(spacing) {
			return unsupportedHeader(fmt.Sprintf("spacing %d is not a positive finite float32", index+1))
		}
	}
	if spec.VoxelOffset < 352 || spec.VoxelOffset%16 != 0 {
		return unsupportedHeader(fmt.Sprintf("voxel offset %d is not a 16-byte-aligned offset at or after 352", spec.VoxelOffset))
	}
	floatOffset := float32(spec.VoxelOffset)
	if math.IsInf(float64(floatOffset), 0) || int64(floatOffset) != spec.VoxelOffset {
		return unsupportedHeader(fmt.Sprintf("voxel offset %d is not exactly representable as float32", spec.VoxelOffset))
	}
	if spec.Slope == 0 || float32(spec.Slope) == 0 || !fitsFiniteFloat32(spec.Slope) {
		return unsupportedHeader("scaling slope is zero, non-finite, or outside float32")
	}
	if !fitsFiniteFloat32(spec.Intercept) {
		return unsupportedHeader("scaling intercept is non-finite or outside float32")
	}
	if !spec.AffineRAS.Finite() {
		return unsupportedHeader("affine is non-finite or has an invalid homogeneous row")
	}
	for _, value := range spec.AffineRAS {
		if !fitsFiniteFloat32(value) {
			return unsupportedHeader("affine coefficient is outside float32")
		}
	}

	columns := [3][3]float64{
		{spec.AffineRAS[0], spec.AffineRAS[4], spec.AffineRAS[8]},
		{spec.AffineRAS[1], spec.AffineRAS[5], spec.AffineRAS[9]},
		{spec.AffineRAS[2], spec.AffineRAS[6], spec.AffineRAS[10]},
	}
	norms := [3]float64{}
	for index, column := range columns {
		norms[index] = math.Sqrt(dot3(column, column))
		if !nearlyEqual(norms[index], spec.Spacing[index], 1e-5) {
			return unsupportedHeader(fmt.Sprintf("affine column %d norm does not match spacing", index+1))
		}
	}
	for first := 0; first < len(columns); first++ {
		for second := first + 1; second < len(columns); second++ {
			cosine := dot3(columns[first], columns[second]) / (norms[first] * norms[second])
			if math.Abs(cosine) > 1e-5 {
				return unsupportedHeader(fmt.Sprintf("affine columns %d and %d are not orthogonal", first+1, second+1))
			}
		}
	}
	return nil
}

func supportedDatatypeBitPix(datatype int16) (int16, bool) {
	switch datatype {
	case 2, 256:
		return 8, true
	case 4, 512:
		return 16, true
	case 8, 16, 768:
		return 32, true
	case 64:
		return 64, true
	default:
		return 0, false
	}
}

func fitsFiniteFloat32(value float64) bool {
	converted := float32(value)
	return !math.IsNaN(value) && !math.IsInf(value, 0) &&
		!math.IsNaN(float64(converted)) && !math.IsInf(float64(converted), 0)
}

func dot3(a, b [3]float64) float64 {
	return a[0]*b[0] + a[1]*b[1] + a[2]*b[2]
}

func nearlyEqual(a, b, relativeTolerance float64) bool {
	scale := math.Max(math.Abs(a), math.Abs(b))
	if scale == 0 {
		return true
	}
	return math.Abs(a-b) <= relativeTolerance*scale
}

func affineQuaternion(affine render.GeometryAffine, spacing [4]float64) (quaternion [3]float64, qfac float64) {
	r := [9]float64{
		affine[0] / spacing[0], affine[1] / spacing[1], affine[2] / spacing[2],
		affine[4] / spacing[0], affine[5] / spacing[1], affine[6] / spacing[2],
		affine[8] / spacing[0], affine[9] / spacing[1], affine[10] / spacing[2],
	}
	determinant := r[0]*(r[4]*r[8]-r[5]*r[7]) -
		r[1]*(r[3]*r[8]-r[5]*r[6]) +
		r[2]*(r[3]*r[7]-r[4]*r[6])
	qfac = 1
	if determinant < 0 {
		qfac = -1
		r[2] = -r[2]
		r[5] = -r[5]
		r[8] = -r[8]
	}

	var a, b, c, d float64
	trace := r[0] + r[4] + r[8]
	switch {
	case trace > 0:
		s := 2 * math.Sqrt(trace+1)
		a = s / 4
		b = (r[7] - r[5]) / s
		c = (r[2] - r[6]) / s
		d = (r[3] - r[1]) / s
	case r[0] > r[4] && r[0] > r[8]:
		s := 2 * math.Sqrt(1+r[0]-r[4]-r[8])
		a = (r[7] - r[5]) / s
		b = s / 4
		c = (r[1] + r[3]) / s
		d = (r[2] + r[6]) / s
	case r[4] > r[8]:
		s := 2 * math.Sqrt(1+r[4]-r[0]-r[8])
		a = (r[2] - r[6]) / s
		b = (r[1] + r[3]) / s
		c = s / 4
		d = (r[5] + r[7]) / s
	default:
		s := 2 * math.Sqrt(1+r[8]-r[0]-r[4])
		a = (r[3] - r[1]) / s
		b = (r[2] + r[6]) / s
		c = (r[5] + r[7]) / s
		d = s / 4
	}
	length := math.Sqrt(a*a + b*b + c*c + d*d)
	if length != 0 {
		a, b, c, d = a/length, b/length, c/length, d/length
	}
	if a < 0 {
		b, c, d = -b, -c, -d
	}
	return [3]float64{b, c, d}, qfac
}

func affineLPS2RAS(affine render.GeometryAffine) render.GeometryAffine {
	for column := 0; column < 4; column++ {
		affine[column] = -affine[column]
		affine[4+column] = -affine[4+column]
	}
	return affine
}

func unsupportedHeader(reason string) error {
	return fmt.Errorf("nifti: invalid header: %s", reason)
}
