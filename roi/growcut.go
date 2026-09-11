package roi

import (
	"math"
)

const (
	growCutUnlabeled  uint8 = 0
	growCutForeground uint8 = 1
	growCutBackground uint8 = 2
)

// GrowCut2D performs seeded competing region growing over one grayscale image.
// Foreground and background are sparse Brush masks drawn by the user. The
// returned mask contains the pixels won by foreground; seed masks are never
// mutated. Invalid pixels reported by valueAt are excluded from propagation.
func GrowCut2D(columns, rows int, foreground, background *RasterMask, valueAt func(x, y int) (float64, bool)) *RasterMask {
	if _, _, err := validateGrowCutDimensions(columns, rows, 1, DefaultGrowCutLimits()); err != nil {
		return NewRasterMask(0, 0)
	}
	result := growCutLabels(columns, rows, 1, map[int]*RasterMask{0: foreground}, map[int]*RasterMask{0: background}, false, func(x, y, _ int) (float64, bool) {
		if valueAt == nil {
			return 0, false
		}
		return valueAt(x, y)
	})
	if mask := result[0]; mask != nil {
		return mask
	}
	return NewRasterMask(columns, rows)
}

// GrowCut3D performs seeded competing region growing over a grayscale volume.
// Six-connected neighbours compete across slices. The result contains one
// foreground mask per non-empty slice; seed maps and masks are never mutated.
func GrowCut3D(columns, rows, slices int, foreground, background map[int]*RasterMask, valueAt func(x, y, slice int) (float64, bool)) map[int]*RasterMask {
	if _, _, err := validateGrowCutDimensions(columns, rows, slices, DefaultGrowCutLimits()); err != nil {
		return map[int]*RasterMask{}
	}
	return growCutLabels(columns, rows, slices, foreground, background, true, valueAt)
}

type growCutQueueItem struct {
	index    int
	strength float64
	label    uint8
	order    int
}

type growCutPriorityQueue []growCutQueueItem

func (q growCutPriorityQueue) Len() int { return len(q) }
func (q growCutPriorityQueue) Less(i, j int) bool {
	if q[i].strength != q[j].strength {
		return q[i].strength > q[j].strength
	}
	return q[i].order < q[j].order
}
func (q growCutPriorityQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *growCutPriorityQueue) push(item growCutQueueItem) {
	*q = append(*q, item)
	q.up(q.Len() - 1)
}
func (q *growCutPriorityQueue) pop() growCutQueueItem {
	last := q.Len() - 1
	q.Swap(0, last)
	q.down(0, last)
	old := *q
	item := old[last]
	*q = old[:last]
	return item
}

// up and down mirror container/heap's binary-heap operations. Keeping the
// exact comparison and swap order preserves GrowCut's stable tie behavior
// without boxing every queue item through heap.Interface.
func (q *growCutPriorityQueue) up(index int) {
	for {
		parent := (index - 1) / 2
		if parent == index || !q.Less(index, parent) {
			break
		}
		q.Swap(parent, index)
		index = parent
	}
}

func (q *growCutPriorityQueue) down(index, length int) {
	for {
		left := 2*index + 1
		if left >= length || left < 0 {
			break
		}
		smallest := left
		if right := left + 1; right < length && q.Less(right, left) {
			smallest = right
		}
		if !q.Less(smallest, index) {
			break
		}
		q.Swap(index, smallest)
		index = smallest
	}
}

func growCutLabels(columns, rows, slices int, foreground, background map[int]*RasterMask, includeSliceNeighbours bool, valueAt func(x, y, slice int) (float64, bool)) map[int]*RasterMask {
	result := make(map[int]*RasterMask)
	if columns <= 0 || rows <= 0 || slices <= 0 || valueAt == nil {
		return result
	}
	plane, total, err := validateGrowCutDimensions(columns, rows, slices, DefaultGrowCutLimits())
	if err != nil {
		return result
	}
	values := make([]float64, total)
	valid := make([]bool, total)
	minimum := math.Inf(1)
	maximum := math.Inf(-1)
	for z := 0; z < slices; z++ {
		for y := 0; y < rows; y++ {
			for x := 0; x < columns; x++ {
				value, ok := valueAt(x, y, z)
				if !ok || math.IsNaN(value) || math.IsInf(value, 0) {
					continue
				}
				index := z*plane + y*columns + x
				values[index] = value
				valid[index] = true
				minimum = min(minimum, value)
				maximum = max(maximum, value)
			}
		}
	}
	if math.IsInf(minimum, 1) {
		return result
	}
	contrast := maximum - minimum
	if contrast <= 0 || math.IsNaN(contrast) || math.IsInf(contrast, 0) {
		contrast = 1
	}

	labels := make([]uint8, total)
	strengths := make([]float64, total)
	queue := &growCutPriorityQueue{}
	queueLimit := DefaultGrowCutLimits().MaxQueueItems
	nextOrder := 0
	seed := func(masks map[int]*RasterMask, label uint8) bool {
		queueLimitHit := false
		for z, mask := range masks {
			if z < 0 || z >= slices || mask == nil {
				continue
			}
			mask.ForEachPixel(func(x, y int) {
				if queueLimitHit {
					return
				}
				if x < 0 || x >= columns || y < 0 || y >= rows {
					return
				}
				index := z*plane + y*columns + x
				if !valid[index] || strengths[index] >= 1 {
					return
				}
				labels[index] = label
				strengths[index] = 1
				if queue.Len() >= queueLimit {
					queueLimitHit = true
					return
				}
				queue.push(growCutQueueItem{index: index, strength: 1, label: label, order: nextOrder})
				nextOrder++
			})
		}
		return !queueLimitHit
	}
	// Foreground wins overlapping seed pixels, matching the visible ROI result.
	if !seed(foreground, growCutForeground) || !seed(background, growCutBackground) {
		return result
	}
	if queue.Len() == 0 {
		return result
	}

	for queue.Len() > 0 {
		item := queue.pop()
		if labels[item.index] != item.label || math.Abs(strengths[item.index]-item.strength) > 1e-12 {
			continue
		}
		z := item.index / plane
		remainder := item.index % plane
		y := remainder / columns
		x := remainder % columns
		queueLimitHit := false
		visit := func(nx, ny, nz int) {
			if queueLimitHit {
				return
			}
			if nx < 0 || nx >= columns || ny < 0 || ny >= rows || nz < 0 || nz >= slices {
				return
			}
			neighbour := nz*plane + ny*columns + nx
			if !valid[neighbour] || strengths[neighbour] >= 1 {
				return
			}
			affinity := 1 - math.Abs(values[item.index]-values[neighbour])/contrast
			if affinity < 0 {
				affinity = 0
			}
			attack := item.strength * affinity
			if attack <= strengths[neighbour]+1e-12 {
				return
			}
			labels[neighbour] = item.label
			strengths[neighbour] = attack
			if queue.Len() >= queueLimit {
				queueLimitHit = true
				return
			}
			queue.push(growCutQueueItem{index: neighbour, strength: attack, label: item.label, order: nextOrder})
			nextOrder++
		}
		visit(x-1, y, z)
		visit(x+1, y, z)
		visit(x, y-1, z)
		visit(x, y+1, z)
		if includeSliceNeighbours {
			visit(x, y, z-1)
			visit(x, y, z+1)
		}
		if queueLimitHit {
			return result
		}
	}

	growCutAppendResultRuns(result, labels, columns, rows, slices, plane)
	return result
}

func growCutAppendResultRuns(result map[int]*RasterMask, labels []uint8, columns, rows, slices, plane int) {
	for z := 0; z < slices; z++ {
		for y := 0; y < rows; y++ {
			rowStart := z*plane + y*columns
			for x := 0; x < columns; {
				if labels[rowStart+x] != growCutForeground {
					x++
					continue
				}
				start := x
				for x < columns && labels[rowStart+x] == growCutForeground {
					x++
				}
				mask := result[z]
				if mask == nil {
					mask = NewRasterMask(columns, rows)
					result[z] = mask
				}
				mask.SetRun(y, start, x)
			}
		}
	}
}
