package render

import (
	"fmt"
	"math"
)

// CPRPathSample is one arc-length sample along a CPR centerline together with a
// stable local frame. The frame is rotation-minimizing (parallel transport),
// not Frenet, so {Tangent, Normal, Binormal} stays continuous through straight
// and low-curvature regions instead of flipping near zero curvature.
type CPRPathSample struct {
	ArcLength float64
	Position  Vec3
	Tangent   Vec3 // unit direction of travel
	Normal    Vec3 // unit, perpendicular to Tangent
	Binormal  Vec3 // unit, Tangent × Normal
}

// CPRPath is a patient-space centerline (a polyline of control points) for
// curved planar reformation. It produces deterministic arc-length samples and
// rotation-minimizing local frames along the curve.
type CPRPath struct {
	points []Vec3
	cum    []float64 // cumulative arc length at each control point
	length float64
}

// NewCPRPath builds a bounded path from patient-space control points. It is the
// compatibility form of NewCPRPathChecked and returns nil for invalid or
// excessive input.
func NewCPRPath(controlPoints []Vec3) *CPRPath {
	path, _ := NewCPRPathChecked(controlPoints)
	return path
}

// NewCPRPathChecked validates coordinates, point count, degeneracy and total
// length before allocating path-owned buffers.
func NewCPRPathChecked(controlPoints []Vec3) (*CPRPath, error) {
	if len(controlPoints) > MaxCPRControlPoints {
		return nil, &CPRLimitError{Resource: "control_points", Value: uint64(len(controlPoints)), Limit: MaxCPRControlPoints}
	}
	pts := make([]Vec3, 0, len(controlPoints))
	for index, p := range controlPoints {
		if !finiteVec3(p) {
			return nil, &CPRInputError{Field: fmt.Sprintf("control_points[%d]", index), Reason: "coordinate is not finite"}
		}
		if len(pts) > 0 && p == pts[len(pts)-1] {
			continue
		}
		pts = append(pts, p)
	}
	if len(pts) < 2 {
		return nil, &CPRInputError{Field: "control_points", Reason: "at least two distinct points are required"}
	}
	cum := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		segment := pts[i].Sub(pts[i-1]).Length()
		if !positiveFinite(segment) {
			return nil, &CPRInputError{Field: fmt.Sprintf("segment[%d]", i-1), Reason: "length is zero or not finite"}
		}
		cum[i] = cum[i-1] + segment
		if !positiveFinite(cum[i]) {
			return nil, &CPRInputError{Field: "path_length", Reason: "length is not finite"}
		}
		if cum[i] > MaxCPRPathLengthMM {
			return nil, &CPRLimitError{Resource: "path_length_mm", Value: uint64(math.Ceil(cum[i])), Limit: MaxCPRPathLengthMM}
		}
	}
	return &CPRPath{points: pts, cum: cum, length: cum[len(cum)-1]}, nil
}

// Length is the total arc length (mm) of the centerline.
func (p *CPRPath) Length() float64 {
	if p == nil {
		return 0
	}
	return p.length
}

// ControlPointCount is the number of distinct control points retained.
func (p *CPRPath) ControlPointCount() int {
	if p == nil {
		return 0
	}
	return len(p.points)
}

// PointAt returns the patient-space point at arc length s, clamped to
// [0, Length].
func (p *CPRPath) PointAt(s float64) Vec3 {
	i, frac := p.locate(s)
	return p.points[i].Add(p.points[i+1].Sub(p.points[i]).Scale(frac))
}

// TangentAt returns the unit tangent at arc length s: the direction of the
// segment containing s.
func (p *CPRPath) TangentAt(s float64) Vec3 {
	i, _ := p.locate(s)
	return p.points[i+1].Sub(p.points[i]).Normalize()
}

// locate returns the segment index and interpolation fraction for arc length s.
func (p *CPRPath) locate(s float64) (int, float64) {
	if s <= 0 || math.IsNaN(s) {
		return 0, 0
	}
	if s >= p.length {
		return len(p.points) - 2, 1
	}
	lo, hi := 0, len(p.cum)-1
	for hi-lo > 1 {
		mid := (lo + hi) / 2
		if p.cum[mid] <= s {
			lo = mid
		} else {
			hi = mid
		}
	}
	seg := p.cum[hi] - p.cum[lo]
	if seg == 0 {
		return lo, 0
	}
	return lo, (s - p.cum[lo]) / seg
}

// Resample returns frames spaced at most `spacing` mm apart along the arc
// length, starting at s=0 and always including the path endpoint. The final
// interval may be shorter than spacing. Including both endpoints keeps the
// longitudinal physical field of view independent of the requested sampling
// resolution. Each sample carries a rotation-minimizing normal transported
// from the previous frame. It returns nil for a non-positive or non-finite
// spacing.
func (p *CPRPath) Resample(spacing float64) []CPRPathSample {
	samples, _ := p.ResampleChecked(spacing)
	return samples
}

// ResampleChecked is Resample with typed validation and sample-count errors.
func (p *CPRPath) ResampleChecked(spacing float64) ([]CPRPathSample, error) {
	sampleCount, err := p.SampleCountChecked(spacing)
	if err != nil {
		return nil, err
	}
	regularCount := int(math.Floor(p.length/spacing)) + 1

	t0 := p.TangentAt(0)
	prevTangent := t0
	prevNormal := initialNormal(t0)

	samples := make([]CPRPathSample, 0, sampleCount)
	for k := 0; k < regularCount; k++ {
		s := float64(k) * spacing
		tangent := p.TangentAt(s)
		normal := parallelTransport(prevNormal, prevTangent, tangent)
		// Re-orthonormalize against the tangent so the frame stays exactly
		// orthonormal as floating-point error accumulates.
		binormal := tangent.Cross(normal).Normalize()
		normal = binormal.Cross(tangent).Normalize()
		samples = append(samples, CPRPathSample{
			ArcLength: s,
			Position:  p.PointAt(s),
			Tangent:   tangent,
			Normal:    normal,
			Binormal:  binormal,
		})
		prevTangent = tangent
		prevNormal = normal
	}
	if len(samples) > 0 && p.length-samples[len(samples)-1].ArcLength > 1e-9 {
		tangent := p.TangentAt(p.length)
		normal := parallelTransport(prevNormal, prevTangent, tangent)
		binormal := tangent.Cross(normal).Normalize()
		normal = binormal.Cross(tangent).Normalize()
		samples = append(samples, CPRPathSample{
			ArcLength: p.length,
			Position:  p.PointAt(p.length),
			Tangent:   tangent,
			Normal:    normal,
			Binormal:  binormal,
		})
	}
	return samples, nil
}

// SampleCountChecked returns the exact number of frames ResampleChecked will
// produce, including a shorter endpoint interval, without allocating them.
func (p *CPRPath) SampleCountChecked(spacing float64) (int, error) {
	if p == nil {
		return 0, &CPRInputError{Field: "path", Reason: "path is nil"}
	}
	if !positiveFinite(spacing) {
		return 0, &CPRInputError{Field: "spacing", Reason: "spacing must be positive and finite"}
	}
	regularCount := math.Floor(p.length/spacing) + 1
	if !positiveFinite(regularCount) {
		return 0, &CPRInputError{Field: "sample_count", Reason: "sample count is not finite"}
	}
	count := regularCount
	last := (regularCount - 1) * spacing
	if p.length-last > 1e-9 {
		count++
	}
	if count > MaxCPRPathSamples {
		return 0, &CPRLimitError{Resource: "path_samples", Value: MaxCPRPathSamples + 1, Limit: MaxCPRPathSamples}
	}
	if count < 1 {
		return 0, &CPRInputError{Field: "sample_count", Reason: "sample count is less than one"}
	}
	return int(count), nil
}

// FrameAt returns the rotation-minimizing frame at arc length s, transporting
// the seed normal through every segment junction up to s. It is consistent with
// the frames produced by Resample at the same arc length.
func (p *CPRPath) FrameAt(s float64) CPRPathSample {
	if s < 0 {
		s = 0
	}
	if s > p.length {
		s = p.length
	}
	prevTangent := p.points[1].Sub(p.points[0]).Normalize()
	normal := initialNormal(prevTangent)
	segIdx, _ := p.locate(s)
	for i := 1; i <= segIdx; i++ {
		ti := p.points[i+1].Sub(p.points[i]).Normalize()
		normal = parallelTransport(normal, prevTangent, ti)
		prevTangent = ti
	}
	tangent := p.TangentAt(s)
	binormal := tangent.Cross(normal).Normalize()
	normal = binormal.Cross(tangent).Normalize()
	return CPRPathSample{
		ArcLength: s,
		Position:  p.PointAt(s),
		Tangent:   tangent,
		Normal:    normal,
		Binormal:  binormal,
	}
}

// initialNormal picks a deterministic unit vector perpendicular to the tangent
// to seed the rotation-minimizing frame.
func initialNormal(tangent Vec3) Vec3 {
	ref := Vec3{X: 1}
	if math.Abs(tangent.X) > 0.9 {
		ref = Vec3{Y: 1}
	}
	n := ref.Sub(tangent.Scale(ref.Dot(tangent))).Normalize()
	if n == (Vec3{}) {
		ref = Vec3{Z: 1}
		n = ref.Sub(tangent.Scale(ref.Dot(tangent))).Normalize()
	}
	return n
}

// parallelTransport rotates a normal from the previous tangent frame onto the
// new tangent by the minimal rotation between the two tangents. When the
// tangents are (anti)parallel the normal is carried forward unchanged, which is
// what keeps frames stable through straight and low-curvature segments.
func parallelTransport(normal, prevTangent, tangent Vec3) Vec3 {
	axis := prevTangent.Cross(tangent)
	axisLen := axis.Length()
	if axisLen < 1e-9 {
		return normal
	}
	axis = axis.Scale(1 / axisLen)
	angle := math.Atan2(axisLen, prevTangent.Dot(tangent))
	return normal.Rotate(axis, angle)
}
