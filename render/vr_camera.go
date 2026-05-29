package render

import (
	"math"
	"strings"
)

// VRCamera is a turntable orbit camera for the 3D volume renderer
// (3d-toolbar-plan.md §5.3). It orbits Target at Distance, with Yaw/Pitch/Roll in
// radians and a vertical field of view. The renderer generates rays directly from
// the camera basis (Forward/Right/Up), which is equivalent to (and simpler than)
// unprojecting through inverse(proj·view).
type VRCamera struct {
	Target     Vec3
	Yaw        float64
	Pitch      float64
	Roll       float64
	Distance   float64
	FovY       float64 // radians
	Radius     float64 // scene bounding radius (mm), set by Fit; bounds zoom
	Projection VRCameraProjection
}

// worldUp is the turntable axis (patient superior/inferior in box space).
var worldUp = Vec3{Z: 1}

const (
	vrPitchLimit = 1.35
	vrDefaultFov = 45 * math.Pi / 180
)

// VRCameraProjection selects the camera ray geometry.
type VRCameraProjection int

const (
	VRProjectionPerspective VRCameraProjection = iota
	VRProjectionParallel
	VRProjectionEndoscopy
)

func (p VRCameraProjection) normalized() VRCameraProjection {
	switch p {
	case VRProjectionParallel, VRProjectionEndoscopy:
		return p
	default:
		return VRProjectionPerspective
	}
}

func (p VRCameraProjection) Label() string {
	switch p.normalized() {
	case VRProjectionParallel:
		return "Parallel"
	case VRProjectionEndoscopy:
		return "Endoscopy"
	default:
		return "Perspective"
	}
}

func VRCameraProjectionLabels() []string {
	return []string{
		VRProjectionPerspective.Label(),
		VRProjectionParallel.Label(),
		VRProjectionEndoscopy.Label(),
	}
}

func ParseVRCameraProjection(label string) (VRCameraProjection, bool) {
	switch strings.ToLower(strings.TrimSpace(label)) {
	case "perspective", "orbit":
		return VRProjectionPerspective, true
	case "parallel", "orthographic", "ortho":
		return VRProjectionParallel, true
	case "endoscopy", "endoscopic":
		return VRProjectionEndoscopy, true
	default:
		return VRProjectionPerspective, false
	}
}

// NewVRCamera returns a camera fitted to a bounding radius, looking along +Y with
// a slight downward pitch.
func NewVRCamera(radius float64) VRCamera {
	c := VRCamera{Target: Vec3{}, FovY: vrDefaultFov, Yaw: 0, Pitch: 0.3}
	c.Fit(radius)
	return c
}

// dir is the unit camera→target direction for the current yaw/pitch.
func (c VRCamera) dir() Vec3 {
	cp := math.Cos(c.Pitch)
	return Vec3{
		X: cp * math.Sin(c.Yaw),
		Y: cp * math.Cos(c.Yaw),
		Z: math.Sin(c.Pitch),
	}
}

// Forward is the unit direction from the camera toward the target.
func (c VRCamera) Forward() Vec3 { return c.dir().Normalize() }

// Position is the camera location in world (box-centred mm) space.
func (c VRCamera) Position() Vec3 {
	return c.Target.Sub(c.Forward().Scale(c.Distance))
}

// basis returns the orthonormal (right, up, forward) frame including roll.
func (c VRCamera) basis() (right, up, forward Vec3) {
	forward = c.Forward()
	right = forward.Cross(worldUp)
	if right.Length() < 1e-6 {
		right = Vec3{X: 1}
	}
	right = right.Normalize()
	up = right.Cross(forward).Normalize()
	if c.Roll != 0 {
		right = right.Rotate(forward, c.Roll)
		up = up.Rotate(forward, c.Roll)
	}
	return right, up, forward
}

func (c VRCamera) Right() Vec3 { r, _, _ := c.basis(); return r }
func (c VRCamera) Up() Vec3    { _, u, _ := c.basis(); return u }

func (c VRCamera) rayNDC(px, py, w, h int) (ndcx, ndcy float64) {
	aspect := 1.0
	if h > 0 {
		aspect = float64(w) / float64(h)
	}
	tan := math.Tan(c.FovY / 2)
	ndcx = (2*(float64(px)+0.5)/float64(w) - 1) * aspect * tan
	ndcy = (1 - 2*(float64(py)+0.5)/float64(h)) * tan
	return ndcx, ndcy
}

// Ray returns the world-space origin and unit direction for output pixel (px,py)
// of a (w×h) image.
func (c VRCamera) Ray(px, py, w, h int) (origin, dir Vec3) {
	right, up, forward := c.basis()
	ndcx, ndcy := c.rayNDC(px, py, w, h)
	switch c.Projection.normalized() {
	case VRProjectionParallel:
		origin = c.Position().Add(right.Scale(ndcx * c.Distance)).Add(up.Scale(ndcy * c.Distance))
		return origin, forward
	case VRProjectionEndoscopy:
		dir = forward.Add(right.Scale(ndcx)).Add(up.Scale(ndcy)).Normalize()
		return c.Target, dir
	default:
		dir = forward.Add(right.Scale(ndcx)).Add(up.Scale(ndcy)).Normalize()
		return c.Position(), dir
	}
}

// Rotate orbits the camera (drag): yaw/pitch decrease with dx/dy; pitch clamped.
func (c *VRCamera) Rotate(dx, dy float64) {
	const k = 0.008
	c.Yaw -= dx * k
	c.Pitch -= dy * k
	c.Pitch = clampFloat(c.Pitch, -vrPitchLimit, vrPitchLimit)
}

// RollBy rotates the image about the view axis.
func (c *VRCamera) RollBy(theta float64) { c.Roll += theta }

// Zoom dollies the camera; scale>1 zooms in. Distance clamps to [r·0.25, r·12].
func (c *VRCamera) Zoom(scale float64) {
	if scale <= 0 {
		return
	}
	if c.Projection.normalized() == VRProjectionEndoscopy {
		stepBase := c.Radius
		if stepBase <= 0 {
			stepBase = c.Distance
		}
		if stepBase <= 0 {
			stepBase = 1
		}
		c.Target = c.Target.Add(c.Forward().Scale(math.Log(scale) * stepBase * 0.25))
		return
	}
	c.Distance /= scale
	if c.Radius > 0 {
		c.Distance = clampFloat(c.Distance, c.Radius*0.25, c.Radius*12)
	}
}

// Pan translates the target along the camera right/up axes (cursor-tracking).
func (c *VRCamera) Pan(dx, dy float64) {
	right, up, _ := c.basis()
	scale := c.Distance * 0.0015
	c.Target = c.Target.Add(right.Scale(-dx * scale)).Add(up.Scale(dy * scale))
}

// Fit centres on the origin and sets the distance from the bounding (sphere)
// radius so the whole circumscribing sphere is visible at any orbit angle.
func (c *VRCamera) Fit(radius float64) {
	if radius <= 0 {
		radius = 1
	}
	c.Radius = radius
	c.Target = Vec3{}
	s := math.Sin(c.FovY / 2)
	if s < 1e-3 {
		s = 1e-3
	}
	c.Distance = radius/s + radius
	c.Roll = 0
}

// vrFitFill is the fraction of the half-frame the framed half-extent occupies at
// the default fit — so the anatomy fills most of the viewport (reference 16)
// instead of the looser circumscribing-sphere fit.
const vrFitFill = 0.92

// FitExtent frames a scene tightly: boundingRadius sets the zoom-clamp Radius and
// recentres the target, while frameExtent (a half-extent in mm) is sized to fill
// most of the viewport. frameExtent <= 0 falls back to boundingRadius (the loose
// fit). Yaw/Pitch are left untouched so a Fit command keeps the current view.
func (c *VRCamera) FitExtent(boundingRadius, frameExtent float64) {
	if boundingRadius <= 0 {
		boundingRadius = 1
	}
	c.Radius = boundingRadius
	c.Target = Vec3{}
	if frameExtent <= 0 {
		frameExtent = boundingRadius
	}
	t := math.Tan(c.FovY / 2)
	if t < 1e-3 {
		t = 1e-3
	}
	c.Distance = frameExtent / (t * vrFitFill)
	c.Roll = 0
}

// FitVolume frames a volume so its longest side fills most of the viewport while
// zoom stays clamped to the bounding-sphere radius (reference 16).
func (c *VRCamera) FitVolume(v *Volume) {
	if v == nil {
		c.Fit(1)
		return
	}
	c.FitExtent(v.BoundingRadiusMM(), v.FrameRadiusMM())
}

// SetOrientation aims the camera so it looks along -normal (the plane normal
// faces the viewer), snapping yaw/pitch and clearing roll (anatomical snaps).
func (c *VRCamera) SetOrientation(normal Vec3) {
	f := normal.Scale(-1).Normalize() // forward = -normal (look toward target)
	if f.Length() == 0 {
		return
	}
	c.Pitch = math.Asin(clampFloat(f.Z, -1, 1))
	c.Yaw = math.Atan2(f.X, f.Y)
	c.Roll = 0
}
