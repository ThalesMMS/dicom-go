package roi

import (
	"container/heap"
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
func (q *growCutPriorityQueue) Push(value any) {
	*q = append(*q, value.(growCutQueueItem))
}
func (q *growCutPriorityQueue) Pop() any {
	old := *q
	last := len(old) - 1
	item := old[last]
	*q = old[:last]
	return item
}

func growCutLabels(columns, rows, slices int, foreground, background map[int]*RasterMask, includeSliceNeighbours bool, valueAt func(x, y, slice int) (float64, bool)) map[int]*RasterMask {
	result := make(map[int]*RasterMask)
	if columns <= 0 || rows <= 0 || slices <= 0 || valueAt == nil {
		return result
	}
	plane := columns * rows
	total := plane * slices
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
	heap.Init(queue)
	nextOrder := 0
	seed := func(masks map[int]*RasterMask, label uint8) {
		for z, mask := range masks {
			if z < 0 || z >= slices || mask == nil {
				continue
			}
			mask.ForEachPixel(func(x, y int) {
				if x < 0 || x >= columns || y < 0 || y >= rows {
					return
				}
				index := z*plane + y*columns + x
				if !valid[index] || strengths[index] >= 1 {
					return
				}
				labels[index] = label
				strengths[index] = 1
				heap.Push(queue, growCutQueueItem{index: index, strength: 1, label: label, order: nextOrder})
				nextOrder++
			})
		}
	}
	// Foreground wins overlapping seed pixels, matching the visible ROI result.
	seed(foreground, growCutForeground)
	seed(background, growCutBackground)
	if queue.Len() == 0 {
		return result
	}

	for queue.Len() > 0 {
		item := heap.Pop(queue).(growCutQueueItem)
		if labels[item.index] != item.label || math.Abs(strengths[item.index]-item.strength) > 1e-12 {
			continue
		}
		z := item.index / plane
		remainder := item.index % plane
		y := remainder / columns
		x := remainder % columns
		visit := func(nx, ny, nz int) {
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
			heap.Push(queue, growCutQueueItem{index: neighbour, strength: attack, label: item.label, order: nextOrder})
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
	}

	for index, label := range labels {
		if label != growCutForeground {
			continue
		}
		z := index / plane
		remainder := index % plane
		y := remainder / columns
		x := remainder % columns
		mask := result[z]
		if mask == nil {
			mask = NewRasterMask(columns, rows)
			result[z] = mask
		}
		mask.Set(x, y, true)
	}
	return result
}
