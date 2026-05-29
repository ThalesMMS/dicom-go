package render

import "math"

// HU / texture-space accessors on the shared Volume for the 3D volume
// renderer (3d-toolbar-plan.md §5.1). ValueAt already returns rescaled HU (slope/
// intercept applied in PixelValueAt) and TrilinearAt samples it; this adds the
// dataset HU range, a recommended window, a SampleHU alias, and the physical
// extent the camera/raycaster use so anisotropic spacing is not squashed.

// SampleHU returns the trilinearly-interpolated HU value at fractional voxel
// coordinates. ok is false only when fully outside the volume.
func (v *Volume) SampleHU(p Vec3) (float64, bool) { return v.TrilinearAt(p) }

// HURange returns the dataset HU min/max. It is computed once (strided for large
// volumes to bound cost) and cached. ok is false for an empty volume.
func (v *Volume) HURange() (huMin, huMax float64, ok bool) {
	return v.huRangeFromSampler(nil)
}

func (v *Volume) huRangeFromSampler(sampler *volumeSampler) (huMin, huMax float64, ok bool) {
	if v == nil || v.Depth == 0 {
		return 0, 0, false
	}
	v.huMu.Lock()
	defer v.huMu.Unlock()
	if v.huRangeOK {
		return v.huMin, v.huMax, true
	}
	ownsSampler := false
	if sampler == nil || sampler.vol != v {
		var samplerOK bool
		sampler, samplerOK = newVolumeSampler(v)
		if !samplerOK {
			return 0, 0, false
		}
		ownsSampler = true
	}
	if ownsSampler {
		defer sampler.Close()
	}
	const target = 2_000_000 // cap samples scanned
	total := sampler.cols * sampler.rows * sampler.depth
	stride := 1
	if total > target {
		stride = total / target
	}
	huMin, huMax = math.Inf(1), math.Inf(-1)
	found := false
	i := 0
	for z := 0; z < sampler.depth; z++ {
		for y := 0; y < sampler.rows; y++ {
			for x := 0; x < sampler.cols; x++ {
				if i%stride == 0 {
					if val, valid := sampler.valueAt(x, y, z); valid {
						if val < huMin {
							huMin = val
						}
						if val > huMax {
							huMax = val
						}
						found = true
					}
				}
				i++
			}
		}
	}
	if !found || huMax <= huMin {
		return 0, 0, false
	}
	v.huMin, v.huMax, v.huRangeOK = huMin, huMax, true
	return huMin, huMax, true
}

// RecommendedWindow returns a sensible default window: the series default when
// valid, otherwise spanning the dataset HU range.
func (v *Volume) RecommendedWindow() WindowLevel {
	if v != nil && len(v.slices) > 0 {
		dw := v.slices[0].DefaultWindow
		if dw.Width > 0 {
			return dw
		}
	}
	if lo, hi, ok := v.HURange(); ok {
		return WindowLevel{Center: (lo + hi) / 2, Width: hi - lo}
	}
	return WindowLevel{Center: 40, Width: 400}
}

// PhysicalSize is the volume's extent in mm along (X=col, Y=row, Z=slice),
// measured between the outermost voxel centres.
func (v *Volume) PhysicalSize() Vec3 {
	if v == nil {
		return Vec3{}
	}
	return Vec3{
		X: float64(v.Cols-1) * v.ColSpacing,
		Y: float64(v.Rows-1) * v.RowSpacing,
		Z: float64(v.Depth-1) * v.SliceSpacing,
	}
}

// BoundingRadiusMM is half the physical diagonal — the camera fit distance basis.
func (v *Volume) BoundingRadiusMM() float64 {
	s := v.PhysicalSize()
	return 0.5 * s.Length()
}

// FrameRadiusMM is the largest physical half-extent of the volume box (half its
// longest side). Fitting the camera to this — instead of the circumscribing
// BoundingRadiusMM — makes the anatomy fill most of the viewport (reference 16);
// the box corners may extend past the frame on oblique orbits, which matches a
// clinical VR view rather than the looser bounding-sphere fit.
func (v *Volume) FrameRadiusMM() float64 {
	if v == nil {
		return 1
	}
	s := v.PhysicalSize()
	m := s.X
	if s.Y > m {
		m = s.Y
	}
	if s.Z > m {
		m = s.Z
	}
	return 0.5 * m
}

// TextureToVoxel maps a [0,1]³ texture coordinate to fractional voxel coordinates
// (component i → i*(dim-1)).
func (v *Volume) TextureToVoxel(tex Vec3) Vec3 {
	return Vec3{
		X: tex.X * float64(v.Cols-1),
		Y: tex.Y * float64(v.Rows-1),
		Z: tex.Z * float64(v.Depth-1),
	}
}

// SampleHUTexture samples HU at a [0,1]³ texture coordinate.
func (v *Volume) SampleHUTexture(tex Vec3) (float64, bool) {
	return v.TrilinearAt(v.TextureToVoxel(tex))
}

// shadeAt returns a Blinn-Phong shading multiplier for the sample at texture
// coordinate tex viewed along viewDir (3d-toolbar-plan.md §5.6). The surface
// normal is the central-difference HU gradient (six trilinear samples); a
// head-light (light = eye) drives diffuse + specular over an ambient term. Flat
// regions (tiny gradient) return ambient only.
func (v *Volume) shadeAt(tex, viewDir Vec3) float64 {
	return v.shadeWithGradient(v.gradientAt(tex), viewDir)
}

// gradientAt returns the central-difference HU gradient at texture coordinate tex
// (six trilinear samples one voxel apart). It drives both Blinn-Phong shading and
// gradient-opacity modulation, so the renderer computes it once per opaque sample.
func (v *Volume) gradientAt(tex Vec3) Vec3 {
	dx := 1.0 / float64(maxInt(v.Cols, 2))
	dy := 1.0 / float64(maxInt(v.Rows, 2))
	dz := 1.0 / float64(maxInt(v.Depth, 2))
	hx1, _ := v.SampleHUTexture(Vec3{tex.X + dx, tex.Y, tex.Z})
	hx0, _ := v.SampleHUTexture(Vec3{tex.X - dx, tex.Y, tex.Z})
	hy1, _ := v.SampleHUTexture(Vec3{tex.X, tex.Y + dy, tex.Z})
	hy0, _ := v.SampleHUTexture(Vec3{tex.X, tex.Y - dy, tex.Z})
	hz1, _ := v.SampleHUTexture(Vec3{tex.X, tex.Y, tex.Z + dz})
	hz0, _ := v.SampleHUTexture(Vec3{tex.X, tex.Y, tex.Z - dz})
	return Vec3{X: hx1 - hx0, Y: hy1 - hy0, Z: hz1 - hz0}
}

// shadeWithGradient is the Blinn-Phong shading multiplier for a sample with the
// given HU gradient, viewed along viewDir, under a head-light (light = eye). Flat
// regions (tiny gradient) return ambient only.
func (v *Volume) shadeWithGradient(grad, viewDir Vec3) float64 {
	const ambient, kd, ks, shininess = 0.3, 0.7, 0.3, 20.0
	g := grad.Length()
	if g < 1e-6 {
		return ambient
	}
	n := grad.Scale(-1 / g) // outward (toward lower density)
	eye := viewDir.Scale(-1).Normalize()
	diffuse := math.Max(n.Dot(eye), 0)
	half := eye // light == eye (headlight) → half == eye
	spec := math.Pow(math.Max(n.Dot(half), 0), shininess)
	out := ambient + kd*diffuse + ks*spec
	if out < 0 {
		out = 0
	}
	return out
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
