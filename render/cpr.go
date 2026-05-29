package render

import "math"

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

// NewCPRPath builds a path from patient-space control points. Consecutive
// duplicate points are collapsed; it returns nil when fewer than two distinct
// points remain.
func NewCPRPath(controlPoints []Vec3) *CPRPath {
	pts := make([]Vec3, 0, len(controlPoints))
	for _, p := range controlPoints {
		if len(pts) > 0 && p.Sub(pts[len(pts)-1]).Length() == 0 {
			continue
		}
		pts = append(pts, p)
	}
	if len(pts) < 2 {
		return nil
	}
	cum := make([]float64, len(pts))
	for i := 1; i < len(pts); i++ {
		cum[i] = cum[i-1] + pts[i].Sub(pts[i-1]).Length()
	}
	return &CPRPath{points: pts, cum: cum, length: cum[len(cum)-1]}
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
	if p == nil || spacing <= 0 || math.IsNaN(spacing) || math.IsInf(spacing, 0) {
		return nil
	}
	count := int(math.Floor(p.length/spacing)) + 1

	t0 := p.TangentAt(0)
	prevTangent := t0
	prevNormal := initialNormal(t0)

	samples := make([]CPRPathSample, 0, count+1)
	for k := 0; k <= count; k++ {
		s := float64(k) * spacing
		if s > p.length {
			break
		}
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
	return samples
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
