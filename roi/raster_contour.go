package roi

import (
	"image"
	"math"
)

type rasterBoundaryEdge struct {
	from image.Point
	to   image.Point
	used bool
}

// Contours traces every closed pixel-edge contour in the mask. Outer contours
// are clockwise in image coordinates (where Y grows down); holes run in the
// opposite direction. The closing point is not duplicated.
func (m *RasterMask) Contours() [][]image.Point {
	if m == nil || m.Empty() {
		return nil
	}
	edges := make([]rasterBoundaryEdge, 0)
	add := func(from, to image.Point) {
		edges = append(edges, rasterBoundaryEdge{from: from, to: to})
	}
	m.ForEachPixel(func(x, y int) {
		if !m.Get(x, y-1) {
			add(image.Pt(x, y), image.Pt(x+1, y))
		}
		if !m.Get(x+1, y) {
			add(image.Pt(x+1, y), image.Pt(x+1, y+1))
		}
		if !m.Get(x, y+1) {
			add(image.Pt(x+1, y+1), image.Pt(x, y+1))
		}
		if !m.Get(x-1, y) {
			add(image.Pt(x, y+1), image.Pt(x, y))
		}
	})
	byStart := make(map[image.Point][]int, len(edges))
	for index := range edges {
		byStart[edges[index].from] = append(byStart[edges[index].from], index)
	}
	contours := make([][]image.Point, 0)
	for startIndex := range edges {
		if edges[startIndex].used {
			continue
		}
		start := edges[startIndex].from
		contour := []image.Point{start}
		currentIndex := startIndex
		closed := false
		for steps := 0; steps <= len(edges); steps++ {
			current := &edges[currentIndex]
			if current.used {
				break
			}
			current.used = true
			if current.to == start {
				closed = true
				break
			}
			contour = append(contour, current.to)
			next := nextRasterBoundaryEdge(edges, byStart[current.to], current.from, current.to)
			if next < 0 {
				break
			}
			currentIndex = next
		}
		contour = removeCollinearClosedContourPoints(contour)
		if closed && len(contour) >= 3 {
			contours = append(contours, contour)
		}
	}
	return contours
}

// LargestContour returns the contour with the greatest absolute enclosed area.
// This is the outer outline expected when a connected raster region is turned
// into one editable polygon; holes and stray islands are intentionally omitted.
func (m *RasterMask) LargestContour() []image.Point {
	var largest []image.Point
	maxArea := 0.0
	for _, contour := range m.Contours() {
		area := math.Abs(closedContourSignedArea(contour))
		if area > maxArea {
			maxArea = area
			largest = contour
		}
	}
	return append([]image.Point(nil), largest...)
}

// SimplifyClosedContour applies a closed-loop Ramer-Douglas-Peucker pass while
// preserving at least three vertices. A non-positive tolerance only removes
// exactly collinear points.
func SimplifyClosedContour(points []image.Point, tolerance float64) []image.Point {
	points = removeCollinearClosedContourPoints(append([]image.Point(nil), points...))
	if len(points) < 4 || tolerance <= 0 || math.IsNaN(tolerance) || math.IsInf(tolerance, 0) {
		return points
	}
	first, second := closedContourSplitPair(points)
	if first == second {
		return points
	}
	forward := closedContourArc(points, first, second)
	backward := closedContourArc(points, second, first)
	forward = simplifyOpenContour(forward, tolerance)
	backward = simplifyOpenContour(backward, tolerance)
	out := append([]image.Point(nil), forward...)
	if len(backward) > 2 {
		out = append(out, backward[1:len(backward)-1]...)
	}
	out = removeCollinearClosedContourPoints(out)
	if len(out) < 3 {
		return points
	}
	return out
}

func nextRasterBoundaryEdge(edges []rasterBoundaryEdge, candidates []int, previous, current image.Point) int {
	best := -1
	bestRank := 5
	incoming := rasterEdgeHeading(previous, current)
	for _, index := range candidates {
		if index < 0 || index >= len(edges) || edges[index].used {
			continue
		}
		outgoing := rasterEdgeHeading(current, edges[index].to)
		delta := (outgoing - incoming + 4) % 4
		rank := 3
		switch delta {
		case 1: // right turn keeps the filled region on the right
			rank = 0
		case 0:
			rank = 1
		case 3:
			rank = 2
		}
		if rank < bestRank {
			best, bestRank = index, rank
		}
	}
	return best
}

func rasterEdgeHeading(from, to image.Point) int {
	switch {
	case to.X > from.X:
		return 0 // east
	case to.Y > from.Y:
		return 1 // south
	case to.X < from.X:
		return 2 // west
	default:
		return 3 // north
	}
}

func removeCollinearClosedContourPoints(points []image.Point) []image.Point {
	for len(points) > 3 {
		removed := false
		out := make([]image.Point, 0, len(points))
		for index, point := range points {
			previous := points[(index+len(points)-1)%len(points)]
			next := points[(index+1)%len(points)]
			first := point.Sub(previous)
			second := next.Sub(point)
			if first.X*second.Y-first.Y*second.X == 0 && first.X*second.X+first.Y*second.Y >= 0 {
				removed = true
				continue
			}
			out = append(out, point)
		}
		points = out
		if !removed {
			break
		}
	}
	return points
}

func closedContourSignedArea(points []image.Point) float64 {
	if len(points) < 3 {
		return 0
	}
	doubleArea := int64(0)
	for index, point := range points {
		next := points[(index+1)%len(points)]
		doubleArea += int64(point.X)*int64(next.Y) - int64(next.X)*int64(point.Y)
	}
	return float64(doubleArea) / 2
}

func closedContourSplitPair(points []image.Point) (int, int) {
	if len(points) < 2 {
		return 0, 0
	}
	extremes := []int{0, 0, 0, 0}
	for index := 1; index < len(points); index++ {
		if points[index].X < points[extremes[0]].X {
			extremes[0] = index
		}
		if points[index].X > points[extremes[1]].X {
			extremes[1] = index
		}
		if points[index].Y < points[extremes[2]].Y {
			extremes[2] = index
		}
		if points[index].Y > points[extremes[3]].Y {
			extremes[3] = index
		}
	}
	first, second := extremes[0], extremes[1]
	maxDistance := -1
	for _, a := range extremes {
		for _, b := range extremes {
			if a == b {
				continue
			}
			delta := points[b].Sub(points[a])
			distance := delta.X*delta.X + delta.Y*delta.Y
			if distance > maxDistance {
				first, second, maxDistance = a, b, distance
			}
		}
	}
	return first, second
}

func closedContourArc(points []image.Point, first, last int) []image.Point {
	out := []image.Point{points[first]}
	for index := first; index != last; {
		index = (index + 1) % len(points)
		out = append(out, points[index])
	}
	return out
}

func simplifyOpenContour(points []image.Point, tolerance float64) []image.Point {
	if len(points) <= 2 {
		return append([]image.Point(nil), points...)
	}
	maxDistance := 0.0
	maxIndex := 0
	for index := 1; index < len(points)-1; index++ {
		distance := pointSegmentDistanceFloat(points[index], points[0], points[len(points)-1])
		if distance > maxDistance {
			maxDistance, maxIndex = distance, index
		}
	}
	if maxDistance <= tolerance {
		return []image.Point{points[0], points[len(points)-1]}
	}
	left := simplifyOpenContour(points[:maxIndex+1], tolerance)
	right := simplifyOpenContour(points[maxIndex:], tolerance)
	return append(left[:len(left)-1], right...)
}

func pointSegmentDistanceFloat(point, start, end image.Point) float64 {
	dx := float64(end.X - start.X)
	dy := float64(end.Y - start.Y)
	if dx == 0 && dy == 0 {
		return math.Hypot(float64(point.X-start.X), float64(point.Y-start.Y))
	}
	t := (float64(point.X-start.X)*dx + float64(point.Y-start.Y)*dy) / (dx*dx + dy*dy)
	t = math.Max(0, math.Min(1, t))
	projectionX := float64(start.X) + t*dx
	projectionY := float64(start.Y) + t*dy
	return math.Hypot(float64(point.X)-projectionX, float64(point.Y)-projectionY)
}
