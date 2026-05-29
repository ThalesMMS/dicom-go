package render

// Clipping primitives for the 3D renderer (3d-toolbar-plan.md §5.9): an
// axis-aligned crop box plus up to three half-space clip planes, both in
// texture [0,1]³ space. Samples failing the test are skipped in the march; this
// is also the foundation the scalpel writes into (#162). VRClip.keep is defined
// alongside the renderer.

// VRClip carries the optional geometric clipping (crop box + planes, #143) and
// the scalpel cut mask (#162). A nil *VRClip keeps every sample.
type VRClip struct {
	hasCrop          bool
	cropMin, cropMax Vec3 // texture-space [0,1]³
	planes           []clipPlane
	cut              *VRCutMask
}

type clipPlane struct {
	normal Vec3 // texture space
	d      float64
}

// keep reports whether a texture-space sample survives the clipping.
func (c *VRClip) keep(tex Vec3) bool {
	if c == nil {
		return true
	}
	if c.hasCrop {
		if tex.X < c.cropMin.X || tex.Y < c.cropMin.Y || tex.Z < c.cropMin.Z ||
			tex.X > c.cropMax.X || tex.Y > c.cropMax.Y || tex.Z > c.cropMax.Z {
			return false
		}
	}
	for _, p := range c.planes {
		if tex.Dot(p.normal)-p.d > 0 {
			return false
		}
	}
	if c.cut != nil && c.cut.cut(tex) {
		return false
	}
	return true
}

// NewVRClip returns an empty clip that keeps every sample.
func NewVRClip() *VRClip { return &VRClip{} }

// SetCropBox restricts rendering to the texture-space box [min,max].
func (c *VRClip) SetCropBox(min, max Vec3) {
	if c == nil {
		return
	}
	c.cropMin, c.cropMax = min, max
	c.hasCrop = true
}

// ClearCropBox removes the crop box.
func (c *VRClip) ClearCropBox() {
	if c == nil {
		return
	}
	c.hasCrop = false
}

// AddClipPlane adds a half-space clip: samples where dot(tex,normal) - d > 0 are
// removed. Up to three planes are kept.
func (c *VRClip) AddClipPlane(normal Vec3, d float64) {
	if c == nil || len(c.planes) >= 3 {
		return
	}
	c.planes = append(c.planes, clipPlane{normal: normal, d: d})
}

// ClearPlanes removes all clip planes.
func (c *VRClip) ClearPlanes() {
	if c == nil {
		return
	}
	c.planes = nil
}

// SetCut installs the scalpel cut mask (#162).
func (c *VRClip) SetCut(m *VRCutMask) {
	if c == nil {
		return
	}
	c.cut = m
}

// CutMask returns the current scalpel cut mask (may be nil).
func (c *VRClip) CutMask() *VRCutMask {
	if c == nil {
		return nil
	}
	return c.cut
}

// Empty reports whether the clip removes nothing.
func (c *VRClip) Empty() bool {
	return c == nil || (!c.hasCrop && len(c.planes) == 0 && c.cut == nil)
}
