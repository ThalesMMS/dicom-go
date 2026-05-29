package roi

import (
	"math"

	"github.com/ThalesMMS/dicom-go/render"
)

// Segmentation operations on RasterMask / Segmentation3D. Everything here is
// pure Go: brush/fill, thresholding, connected components, and basic morphology
// run without any ITK, Python, or cgo dependency, keeping the default build
// lightweight.

// Brush sets or clears a filled disc of the given radius (pixels) centered at
// (cx,cy) in the mask.
func Brush(mask *RasterMask, cx, cy, radius int, on bool) {
	if mask == nil || radius < 0 {
		return
	}
	r2 := radius * radius
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if dx*dx+dy*dy <= r2 {
				mask.Set(cx+dx, cy+dy, on)
			}
		}
	}
}

// ThresholdMask returns a 2D mask of pixels whose value (from valueAt) lies in
// the inclusive range [lo, hi].
func ThresholdMask(columns, rows int, lo, hi float64, valueAt func(x, y int) (float64, bool)) *RasterMask {
	mask := NewRasterMask(columns, rows)
	if valueAt == nil {
		return mask
	}
	for y := 0; y < rows; y++ {
		x := 0
		for x < columns {
			v, ok := valueAt(x, y)
			if !ok || v < lo || v > hi {
				x++
				continue
			}
			start := x
			for x < columns {
				v, ok := valueAt(x, y)
				if !ok || v < lo || v > hi {
					break
				}
				x++
			}
			mask.SetRun(y, start, x)
		}
	}
	return mask
}

// ThresholdSegmentation thresholds every slice of a volume into a Segmentation3D,
// carrying the supplied geometry. valueAt reads a slice's rescaled value.
func ThresholdSegmentation(geometry render.VolumeGeometry, columns, rows int, lo, hi float64, valueAt func(x, y, slice int) (float64, bool)) *Segmentation3D {
	seg := NewSegmentation3D(geometry, columns, rows)
	if valueAt == nil {
		return seg
	}
	for z := 0; z < len(geometry.Slices); z++ {
		slice := z
		mask := ThresholdMask(columns, rows, lo, hi, func(x, y int) (float64, bool) {
			return valueAt(x, y, slice)
		})
		seg.SetMask(slice, mask)
	}
	return seg
}

// InterpolateMask blends two same-sized binary masks at fraction t in [0,1].
// It first maps each endpoint into an interpolated bounding box, then blends
// signed distance fields inside that common frame. That keeps translated or
// resized painted masks moving between slices instead of copying one endpoint.
func InterpolateMask(first, last *RasterMask, t float64) *RasterMask {
	if first == nil || last == nil {
		return nil
	}
	if first.Columns != last.Columns || first.Rows != last.Rows {
		return nil
	}
	if t <= 0 {
		return first.Clone()
	}
	if t >= 1 {
		return last.Clone()
	}
	out := NewRasterMask(first.Columns, first.Rows)
	if first.Columns == 0 || first.Rows == 0 {
		return out
	}
	firstBounds, firstOK := maskBounds(first)
	lastBounds, lastOK := maskBounds(last)
	switch {
	case !firstOK && !lastOK:
		return out
	case !firstOK:
		return last.Clone()
	case !lastOK:
		return first.Clone()
	}
	targetBounds := interpolateBounds(firstBounds, lastBounds, t)
	firstAligned := remapMaskToBounds(first, firstBounds, targetBounds)
	lastAligned := remapMaskToBounds(last, lastBounds, targetBounds)
	firstField := signedDistanceField(firstAligned)
	lastField := signedDistanceField(lastAligned)
	for idx := range firstField {
		if (1-t)*firstField[idx]+t*lastField[idx] >= 0 {
			out.Set(idx%first.Columns, idx/first.Columns, true)
		}
	}
	return out
}

// InterpolateBetweenSlices fills the slices strictly between two manually edited
// endpoint slices. Existing intermediate masks are replaced with interpolated
// masks, while the endpoint masks are left untouched. It returns the number of
// intermediate slice masks written.
func InterpolateBetweenSlices(seg *Segmentation3D, firstSlice, lastSlice int) int {
	if seg == nil || firstSlice == lastSlice {
		return 0
	}
	if firstSlice > lastSlice {
		firstSlice, lastSlice = lastSlice, firstSlice
	}
	if lastSlice-firstSlice < 2 {
		return 0
	}
	first, ok := seg.MaskAt(firstSlice)
	if !ok || first == nil || first.Empty() {
		return 0
	}
	last, ok := seg.MaskAt(lastSlice)
	if !ok || last == nil || last.Empty() {
		return 0
	}
	if first.Columns != last.Columns || first.Rows != last.Rows {
		return 0
	}
	written := 0
	span := float64(lastSlice - firstSlice)
	for slice := firstSlice + 1; slice < lastSlice; slice++ {
		mask := InterpolateMask(first, last, float64(slice-firstSlice)/span)
		if mask == nil || mask.Empty() {
			continue
		}
		seg.SetMask(slice, mask)
		written++
	}
	return written
}

type rasterMaskBounds struct {
	minX int
	minY int
	maxX int
	maxY int
}

func maskBounds(mask *RasterMask) (rasterMaskBounds, bool) {
	if mask == nil || mask.Empty() {
		return rasterMaskBounds{}, false
	}
	bounds := rasterMaskBounds{
		minX: mask.Columns,
		minY: mask.Rows,
		maxX: -1,
		maxY: -1,
	}
	mask.ForEachRun(func(y int, run MaskRun) {
		if run.Start < bounds.minX {
			bounds.minX = run.Start
		}
		if run.End-1 > bounds.maxX {
			bounds.maxX = run.End - 1
		}
		if y < bounds.minY {
			bounds.minY = y
		}
		if y > bounds.maxY {
			bounds.maxY = y
		}
	})
	return bounds, bounds.maxX >= bounds.minX && bounds.maxY >= bounds.minY
}

func interpolateBounds(first, last rasterMaskBounds, t float64) rasterMaskBounds {
	return rasterMaskBounds{
		minX: int(math.Round(lerpFloat(float64(first.minX), float64(last.minX), t))),
		minY: int(math.Round(lerpFloat(float64(first.minY), float64(last.minY), t))),
		maxX: int(math.Round(lerpFloat(float64(first.maxX), float64(last.maxX), t))),
		maxY: int(math.Round(lerpFloat(float64(first.maxY), float64(last.maxY), t))),
	}
}

func remapMaskToBounds(mask *RasterMask, from, to rasterMaskBounds) *RasterMask {
	out := NewRasterMask(mask.Columns, mask.Rows)
	mask.ForEachRun(func(y int, run MaskRun) {
		mappedY := remapMaskCoordinate(y, from.minY, from.maxY, to.minY, to.maxY)
		mappedStart := remapMaskCoordinate(run.Start, from.minX, from.maxX, to.minX, to.maxX)
		mappedEnd := remapMaskCoordinate(run.End-1, from.minX, from.maxX, to.minX, to.maxX)
		if mappedStart > mappedEnd {
			mappedStart, mappedEnd = mappedEnd, mappedStart
		}
		out.SetRun(mappedY, mappedStart, mappedEnd+1)
	})
	return out
}

func remapMaskCoordinate(value, fromMin, fromMax, toMin, toMax int) int {
	if fromMax == fromMin {
		return int(math.Round((float64(toMin) + float64(toMax)) / 2))
	}
	position := float64(value-fromMin) / float64(fromMax-fromMin)
	return int(math.Round(lerpFloat(float64(toMin), float64(toMax), position)))
}

func lerpFloat(a, b, t float64) float64 {
	return a + (b-a)*t
}

func signedDistanceField(mask *RasterMask) []float64 {
	total := mask.Columns * mask.Rows
	insideFeatures := make([]bool, total)
	outsideFeatures := make([]bool, total)
	for y := 0; y < mask.Rows; y++ {
		for x := 0; x < mask.Columns; x++ {
			idx := y*mask.Columns + x
			inside := mask.Get(x, y)
			insideFeatures[idx] = inside
			outsideFeatures[idx] = !inside
		}
	}
	maxDistance := math.Hypot(float64(mask.Columns), float64(mask.Rows))
	distanceToInside := euclideanDistanceToFeatures(insideFeatures, mask.Columns, mask.Rows, maxDistance)
	distanceToOutside := euclideanDistanceToFeatures(outsideFeatures, mask.Columns, mask.Rows, maxDistance)
	field := make([]float64, total)
	for idx := range field {
		if insideFeatures[idx] {
			field[idx] = distanceToOutside[idx]
		} else {
			field[idx] = -distanceToInside[idx]
		}
	}
	return field
}

func euclideanDistanceToFeatures(features []bool, columns, rows int, maxDistance float64) []float64 {
	total := columns * rows
	out := make([]float64, total)
	if columns <= 0 || rows <= 0 {
		return out
	}
	const largeDistance = 1e12
	tmp := make([]float64, total)
	for y := 0; y < rows; y++ {
		row := make([]float64, columns)
		for x := 0; x < columns; x++ {
			if features[y*columns+x] {
				row[x] = 0
			} else {
				row[x] = largeDistance
			}
		}
		transformed := distanceTransform1D(row)
		copy(tmp[y*columns:(y+1)*columns], transformed)
	}
	for x := 0; x < columns; x++ {
		column := make([]float64, rows)
		for y := 0; y < rows; y++ {
			column[y] = tmp[y*columns+x]
		}
		transformed := distanceTransform1D(column)
		for y := 0; y < rows; y++ {
			distance := math.Sqrt(transformed[y])
			if distance > maxDistance {
				distance = maxDistance
			}
			out[y*columns+x] = distance
		}
	}
	return out
}

func distanceTransform1D(values []float64) []float64 {
	n := len(values)
	out := make([]float64, n)
	if n == 0 {
		return out
	}
	locations := make([]int, n)
	boundaries := make([]float64, n+1)
	k := 0
	locations[0] = 0
	boundaries[0] = math.Inf(-1)
	boundaries[1] = math.Inf(1)
	for q := 1; q < n; q++ {
		s := distanceParabolaIntersection(values, q, locations[k])
		for s <= boundaries[k] {
			k--
			s = distanceParabolaIntersection(values, q, locations[k])
		}
		k++
		locations[k] = q
		boundaries[k] = s
		boundaries[k+1] = math.Inf(1)
	}
	k = 0
	for q := 0; q < n; q++ {
		for boundaries[k+1] < float64(q) {
			k++
		}
		delta := float64(q - locations[k])
		out[q] = delta*delta + values[locations[k]]
	}
	return out
}

func distanceParabolaIntersection(values []float64, q, p int) float64 {
	qf := float64(q)
	pf := float64(p)
	return ((values[q] + qf*qf) - (values[p] + pf*pf)) / (2*qf - 2*pf)
}

// FloodFill returns the 4-connected region of pixels reachable from (seedX,seedY)
// for which inside reports true (region growing). It is the basis of the fill
// tool and seeded threshold growing.
func FloodFill(columns, rows, seedX, seedY int, inside func(x, y int) bool) *RasterMask {
	mask := NewRasterMask(columns, rows)
	if inside == nil || seedX < 0 || seedY < 0 || seedX >= columns || seedY >= rows || !inside(seedX, seedY) {
		return mask
	}
	visited := make([]bool, columns*rows)
	idx := func(x, y int) int { return y*columns + x }
	queue := [][2]int{{seedX, seedY}}
	visited[idx(seedX, seedY)] = true
	for len(queue) > 0 {
		p := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		mask.Set(p[0], p[1], true)
		for _, nb := range [][2]int{{p[0] - 1, p[1]}, {p[0] + 1, p[1]}, {p[0], p[1] - 1}, {p[0], p[1] + 1}} {
			nx, ny := nb[0], nb[1]
			if nx < 0 || ny < 0 || nx >= columns || ny >= rows || visited[idx(nx, ny)] {
				continue
			}
			if inside(nx, ny) {
				visited[idx(nx, ny)] = true
				queue = append(queue, [2]int{nx, ny})
			}
		}
	}
	return mask
}

// ConnectedComponents labels the set pixels of a mask into connected regions
// (8-connected when eightConnected, else 4-connected), returned in row-major
// discovery order.
func ConnectedComponents(mask *RasterMask, eightConnected bool) []*RasterMask {
	if mask == nil {
		return nil
	}
	visited := make([]bool, mask.Columns*mask.Rows)
	idx := func(x, y int) int { return y*mask.Columns + x }
	var comps []*RasterMask
	mask.ForEachPixel(func(sx, sy int) {
		if visited[idx(sx, sy)] {
			return
		}
		comp := NewRasterMask(mask.Columns, mask.Rows)
		queue := [][2]int{{sx, sy}}
		visited[idx(sx, sy)] = true
		for len(queue) > 0 {
			p := queue[len(queue)-1]
			queue = queue[:len(queue)-1]
			comp.Set(p[0], p[1], true)
			for _, nb := range neighbors2D(p[0], p[1], eightConnected) {
				nx, ny := nb[0], nb[1]
				if nx < 0 || ny < 0 || nx >= mask.Columns || ny >= mask.Rows || visited[idx(nx, ny)] {
					continue
				}
				if mask.Get(nx, ny) {
					visited[idx(nx, ny)] = true
					queue = append(queue, [2]int{nx, ny})
				}
			}
		}
		comps = append(comps, comp)
	})
	return comps
}

func neighbors2D(x, y int, eight bool) [][2]int {
	out := [][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}}
	if eight {
		out = append(out, [2]int{x - 1, y - 1}, [2]int{x + 1, y - 1}, [2]int{x - 1, y + 1}, [2]int{x + 1, y + 1})
	}
	return out
}

// ConnectedComponents3D labels the set voxels of a segmentation into 6-connected
// components, each returned as its own Segmentation3D carrying the same geometry.
func ConnectedComponents3D(seg *Segmentation3D) []*Segmentation3D {
	if seg == nil {
		return nil
	}
	type voxel struct{ x, y, z int }
	visited := map[voxel]bool{}
	var comps []*Segmentation3D
	for _, z := range seg.Slices() {
		mask := seg.masks[z]
		zCopy := z
		mask.ForEachPixel(func(sx, sy int) {
			seed := voxel{sx, sy, zCopy}
			if visited[seed] {
				return
			}
			comp := NewSegmentation3D(seg.Geometry, seg.Columns, seg.Rows)
			queue := []voxel{seed}
			visited[seed] = true
			for len(queue) > 0 {
				p := queue[len(queue)-1]
				queue = queue[:len(queue)-1]
				comp.SetVoxel(p.x, p.y, p.z, true)
				for _, nb := range []voxel{
					{p.x - 1, p.y, p.z}, {p.x + 1, p.y, p.z},
					{p.x, p.y - 1, p.z}, {p.x, p.y + 1, p.z},
					{p.x, p.y, p.z - 1}, {p.x, p.y, p.z + 1},
				} {
					if visited[nb] {
						continue
					}
					if seg.Voxel(nb.x, nb.y, nb.z) {
						visited[nb] = true
						queue = append(queue, nb)
					}
				}
			}
			comps = append(comps, comp)
		})
	}
	return comps
}

// Dilate grows a mask by one pixel using a 4-connected structuring element.
func Dilate(mask *RasterMask) *RasterMask {
	if mask == nil {
		return nil
	}
	out := NewRasterMask(mask.Columns, mask.Rows)
	for y, runs := range mask.runs {
		expanded := expandRunList(runs, mask.Columns)
		if y > 0 {
			expanded = unionRunLists(expanded, mask.runs[y-1], mask.Columns)
		}
		if y+1 < mask.Rows {
			expanded = unionRunLists(expanded, mask.runs[y+1], mask.Columns)
		}
		out.runs[y] = expanded
	}
	return out
}

func expandRunList(runs []MaskRun, columns int) []MaskRun {
	out := make([]MaskRun, 0, len(runs))
	for _, run := range runs {
		start, end := clampSpan(run.Start-1, run.End+1, columns)
		if start >= end {
			continue
		}
		if len(out) > 0 && start <= out[len(out)-1].End {
			if end > out[len(out)-1].End {
				out[len(out)-1].End = end
			}
			continue
		}
		out = append(out, MaskRun{Start: start, End: end})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// Erode shrinks a mask by one pixel: a pixel survives only if all four of its
// edge neighbors are also set.
func Erode(mask *RasterMask) *RasterMask {
	if mask == nil {
		return nil
	}
	out := NewRasterMask(mask.Columns, mask.Rows)
	for y := 1; y+1 < mask.Rows; y++ {
		horizontal := make([]MaskRun, 0, len(mask.runs[y]))
		for _, run := range mask.runs[y] {
			if run.Start+1 < run.End-1 {
				horizontal = append(horizontal, MaskRun{Start: run.Start + 1, End: run.End - 1})
			}
		}
		horizontal = intersectRunLists(horizontal, mask.runs[y-1])
		out.runs[y] = intersectRunLists(horizontal, mask.runs[y+1])
	}
	return out
}
