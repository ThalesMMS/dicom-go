package render

import "math"

// First-hit picking for the 3D renderer (3d-toolbar-plan.md §5.11). PickVR casts
// a ray through screen pixel (sx,sy) using the same step / clip / window gate as
// the renderer and returns the patient-space (mm) position of the first sample
// whose composited alpha reaches the threshold. ROI length is the Euclidean
// distance between two picked points.

const vrPickAlphaThreshold = 0.5

// PickVR returns the patient-space surface point under pixel (sx,sy), or ok=false
// if the ray misses any opaque sample.
func PickVR(vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, clip *VRClip, sx, sy, w, h int) (WorldPoint, bool) {
	if vol == nil || w <= 0 || h <= 0 {
		return WorldPoint{}, false
	}
	size := vol.PhysicalSize()
	if size.X <= 0 || size.Y <= 0 || size.Z <= 0 {
		return WorldPoint{}, false
	}
	boxMin := size.Scale(-0.5)
	boxMax := size.Scale(0.5)
	o, d := cam.Ray(sx, sy, w, h)
	t0, t1, hit := intersectAABB(o, d, boxMin, boxMax)
	if !hit {
		return WorldPoint{}, false
	}

	dataMin, dataMax, ok := vol.HURange()
	if !ok {
		dataMin, dataMax = 0, 1
	}
	lut := preset.TF.BakeLUT(dataMin, dataMax, 1024)
	window = normalizeWindow(window, vol.RecommendedWindow())
	steps := 256
	stepLen := size.Length() / float64(steps)

	accA := 0.0
	for t := t0; t <= t1; t += stepLen {
		pw := o.Add(d.Scale(t))
		tex := Vec3{
			X: (pw.X - boxMin.X) / size.X,
			Y: (pw.Y - boxMin.Y) / size.Y,
			Z: (pw.Z - boxMin.Z) / size.Z,
		}
		if !clip.keep(tex) {
			continue
		}
		hu, sok := vol.SampleHUTexture(tex)
		if !sok {
			continue
		}
		densityWindow := windowedUnit(hu, window)
		densityData := clampUnit((hu - dataMin) / (dataMax - dataMin))
		alpha := lut.Lookup(densityData).A * densityWindow
		accA += (1 - accA) * alpha
		if accA >= vrPickAlphaThreshold {
			patient := vol.VoxelToPatient(vol.TextureToVoxel(tex))
			return WorldPoint{X: patient.X, Y: patient.Y, Z: patient.Z}, true
		}
	}
	return WorldPoint{}, false
}

// VRLengthMM returns the Euclidean distance (mm) between two picked world points.
func VRLengthMM(a, b WorldPoint) float64 {
	dx := a.X - b.X
	dy := a.Y - b.Y
	dz := a.Z - b.Z
	return math.Sqrt(dx*dx + dy*dy + dz*dz)
}
