package render

import "math"

// WorldPoint is a patient-coordinate (mm) position used by the 3D cursor to link
// panels (toolbar-plan.md §8.3).
type WorldPoint struct{ X, Y, Z float64 }

// PanelClickToWorld maps a click at image pixel (px,py) on the slice at index to a
// patient-coordinate world point, using the slice's ImagePosition/Orientation and
// the series PixelSpacing. It is computed from geometry for axial slices; planes
// or slices without the geometry tags return ok=false (the 3D cursor then no-ops
// for that panel). Cross-plane reslice geometry is a follow-up.
func PanelClickToWorld(s *Stack, plane MPRPlane, index, px, py int) (WorldPoint, bool) {
	if s == nil || plane != MPRPlaneAxial || index < 0 || index >= len(s.Frames) {
		return WorldPoint{}, false
	}
	sl := s.Frames[index]
	if sl == nil || len(sl.ImagePosition) < 3 || len(sl.ImageOrientation) < 6 || len(s.PixelSpacing) < 2 {
		return WorldPoint{}, false
	}
	rowSp, colSp := s.PixelSpacing[0], s.PixelSpacing[1]
	o := sl.ImagePosition
	col := sl.ImageOrientation[0:3] // direction of increasing column (x)
	row := sl.ImageOrientation[3:6] // direction of increasing row (y)
	return WorldPoint{
		X: o[0] + col[0]*float64(px)*colSp + row[0]*float64(py)*rowSp,
		Y: o[1] + col[1]*float64(px)*colSp + row[1]*float64(py)*rowSp,
		Z: o[2] + col[2]*float64(px)*colSp + row[2]*float64(py)*rowSp,
	}, true
}

// WorldToPanelIndex returns the slice index in the (axial) series nearest the
// world point along the slice axis. Returns ok=false when the series lacks the
// geometry to resolve it.
func WorldToPanelIndex(s *Stack, plane MPRPlane, w WorldPoint) (int, bool) {
	if s == nil || plane != MPRPlaneAxial || len(s.Frames) == 0 {
		return 0, false
	}
	best, bestDist := -1, math.Inf(1)
	for i, sl := range s.Frames {
		if sl == nil {
			continue
		}
		var z float64
		switch {
		case len(sl.ImagePosition) >= 3:
			z = sl.ImagePosition[2]
		case sl.SliceLocationOK:
			z = sl.SliceLocation
		default:
			continue
		}
		if d := math.Abs(z - w.Z); d < bestDist {
			bestDist, best = d, i
		}
	}
	if best < 0 {
		return 0, false
	}
	return best, true
}
