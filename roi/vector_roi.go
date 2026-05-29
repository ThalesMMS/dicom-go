package roi

import (
	"image"
	"math"
	"sort"
)

// ROIShape identifies a vector ROI's geometry.
type ROIShape int

const (
	// ROIRectangle is defined by two opposite corner points.
	ROIRectangle ROIShape = iota
	// ROIEllipse is the ellipse inscribed in the bounding box of two corners.
	ROIEllipse
	// ROIPolygon is the closed polygon through its vertices.
	ROIPolygon
)

// VectorROI is a geometric region of interest in image-pixel coordinates. It is
// the vector layer of the ROI model; Rasterize converts it to a RasterMask (the
// raster layer) which can then be placed in a Segmentation3D (the volumetric
// layer).
type VectorROI struct {
	Shape  ROIShape
	Points []image.Point
}

// Contains reports whether pixel (x,y) lies inside the ROI.
func (r VectorROI) Contains(x, y int) bool {
	switch r.Shape {
	case ROIEllipse:
		return r.ellipseContains(x, y)
	case ROIPolygon:
		return roiPointInPolygon(x, y, r.Points)
	default:
		return r.rectContains(x, y)
	}
}

func (r VectorROI) bbox() (minX, minY, maxX, maxY int, ok bool) {
	if len(r.Points) < 2 {
		return 0, 0, 0, 0, false
	}
	minX, maxX = r.Points[0].X, r.Points[0].X
	minY, maxY = r.Points[0].Y, r.Points[0].Y
	for _, p := range r.Points {
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return minX, minY, maxX, maxY, true
}

func (r VectorROI) rectContains(x, y int) bool {
	minX, minY, maxX, maxY, ok := r.bbox()
	if !ok {
		return false
	}
	return x >= minX && x <= maxX && y >= minY && y <= maxY
}

func (r VectorROI) ellipseContains(x, y int) bool {
	minX, minY, maxX, maxY, ok := r.bbox()
	if !ok {
		return false
	}
	cx := float64(minX+maxX) / 2
	cy := float64(minY+maxY) / 2
	rx := float64(maxX-minX) / 2
	ry := float64(maxY-minY) / 2
	if rx <= 0 || ry <= 0 {
		return false
	}
	dx := (float64(x) - cx) / rx
	dy := (float64(y) - cy) / ry
	return dx*dx+dy*dy <= 1
}

func roiPointInPolygon(x, y int, pts []image.Point) bool {
	if len(pts) < 3 {
		return false
	}
	px, py := float64(x), float64(y)
	in := false
	n := len(pts)
	j := n - 1
	for i := 0; i < n; i++ {
		xi, yi := float64(pts[i].X), float64(pts[i].Y)
		xj, yj := float64(pts[j].X), float64(pts[j].Y)
		if (yi > py) != (yj > py) {
			cross := (xj-xi)*(py-yi)/(yj-yi) + xi
			if px < cross {
				in = !in
			}
		}
		j = i
	}
	return in
}

// Rasterize converts the vector ROI into a RasterMask of the given size by
// scanning each row for contiguous inside spans.
func (r VectorROI) Rasterize(columns, rows int) *RasterMask {
	mask := NewRasterMask(columns, rows)
	if r.Shape == ROIPolygon {
		r.rasterizePolygonScanlines(mask)
		return mask
	}
	for y := 0; y < rows; y++ {
		x := 0
		for x < columns {
			if !r.Contains(x, y) {
				x++
				continue
			}
			start := x
			for x < columns && r.Contains(x, y) {
				x++
			}
			mask.SetRun(y, start, x)
		}
	}
	return mask
}

// Statistics rasterizes the vector ROI and computes its rescaled pixel-value
// distribution. The callback is invoked only for pixels inside the image-sized
// mask; bins follows Stats2DWithHistogram semantics.
func (r VectorROI) Statistics(columns, rows int, valueAt func(x, y int) (float64, bool), bins int) Stats {
	return Stats2DWithHistogram(r.Rasterize(columns, rows), valueAt, bins)
}

// StatisticsWithHistogramBounds is Statistics with an explicit display range
// for histogram bins. Values outside the range are retained in the edge bins.
func (r VectorROI) StatisticsWithHistogramBounds(columns, rows int, valueAt func(x, y int) (float64, bool), bins int, histogramMin, histogramMax float64) Stats {
	return Stats2DWithHistogramBounds(r.Rasterize(columns, rows), valueAt, bins, histogramMin, histogramMax)
}

// rasterizePolygonScanlines fills an even-odd polygon directly as horizontal
// runs. Its half-open rule matches roiPointInPolygon exactly for integer pixel
// centers: a sorted intersection pair [left,right] contributes integer x where
// x >= left and x < right, i.e. [ceil(left), ceil(right)).
func (r VectorROI) rasterizePolygonScanlines(mask *RasterMask) {
	if mask == nil || len(r.Points) < 3 || mask.Columns <= 0 || mask.Rows <= 0 {
		return
	}
	_, minY, _, maxY, ok := r.bbox()
	if !ok || maxY < 0 || minY >= mask.Rows {
		return
	}
	if minY < 0 {
		minY = 0
	}
	if maxY >= mask.Rows {
		maxY = mask.Rows - 1
	}
	intersections := make([]float64, 0, len(r.Points))
	for y := minY; y <= maxY; y++ {
		intersections = intersections[:0]
		py := float64(y)
		previous := len(r.Points) - 1
		for current := 0; current < len(r.Points); current++ {
			xi, yi := float64(r.Points[current].X), float64(r.Points[current].Y)
			xj, yj := float64(r.Points[previous].X), float64(r.Points[previous].Y)
			if (yi > py) != (yj > py) {
				intersections = append(intersections, (xj-xi)*(py-yi)/(yj-yi)+xi)
			}
			previous = current
		}
		sort.Float64s(intersections)
		runs := make([]MaskRun, 0, len(intersections)/2)
		for i := 0; i+1 < len(intersections); i += 2 {
			start := polygonScanlineBoundary(intersections[i], mask.Columns)
			end := polygonScanlineBoundary(intersections[i+1], mask.Columns)
			if start >= end {
				continue
			}
			if len(runs) > 0 && runs[len(runs)-1].End >= start {
				if end > runs[len(runs)-1].End {
					runs[len(runs)-1].End = end
				}
				continue
			}
			runs = append(runs, MaskRun{Start: start, End: end})
		}
		if len(runs) > 0 {
			mask.runs[y] = runs
		}
	}
}

func polygonScanlineBoundary(intersection float64, columns int) int {
	if intersection <= 0 {
		return 0
	}
	if intersection >= float64(columns) {
		return columns
	}
	return int(math.Ceil(intersection))
}
