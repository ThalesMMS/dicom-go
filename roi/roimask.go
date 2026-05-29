package roi

// MaskRun is a half-open horizontal span [Start, End) of set pixels within one
// row of a RasterMask.
type MaskRun struct {
	Start, End int
}

// RasterMask is a 2D binary mask stored as per-row run lists (a compact
// run-length encoding). Segmentation masks are mostly empty with a few
// contiguous spans per row, so storing sorted, non-overlapping runs per row is
// far smaller than a dense bitmap and iterates set pixels cheaply.
//
// A RasterMask is the raster counterpart of a VectorROI (see VectorROI.Rasterize)
// and the per-slice payload of a Segmentation3D — the three layers (vector ROI,
// 2D raster mask, 3D segmentation) stay distinct with explicit conversions.
type RasterMask struct {
	Columns int
	Rows    int
	runs    [][]MaskRun // runs[y] is the sorted, merged run list of row y
}

// NewRasterMask returns an empty mask of the given size.
func NewRasterMask(columns, rows int) *RasterMask {
	if columns < 0 {
		columns = 0
	}
	if rows < 0 {
		rows = 0
	}
	return &RasterMask{Columns: columns, Rows: rows, runs: make([][]MaskRun, rows)}
}

func (m *RasterMask) inBounds(x, y int) bool {
	return m != nil && x >= 0 && y >= 0 && x < m.Columns && y < m.Rows
}

// Get reports whether pixel (x,y) is set.
func (m *RasterMask) Get(x, y int) bool {
	if !m.inBounds(x, y) {
		return false
	}
	for _, r := range m.runs[y] {
		if x < r.Start {
			return false
		}
		if x < r.End {
			return true
		}
	}
	return false
}

// Set turns pixel (x,y) on or off.
func (m *RasterMask) Set(x, y int, on bool) {
	if !m.inBounds(x, y) {
		return
	}
	if on {
		m.SetRun(y, x, x+1)
	} else {
		m.ClearRun(y, x, x+1)
	}
}

// SetRun sets the half-open column span [x0,x1) in row y, merging with existing
// runs. Out-of-range coordinates are clamped to the mask bounds.
func (m *RasterMask) SetRun(y, x0, x1 int) {
	if m == nil || y < 0 || y >= m.Rows {
		return
	}
	x0, x1 = clampSpan(x0, x1, m.Columns)
	if x0 >= x1 {
		return
	}
	m.runs[y] = addRun(m.runs[y], x0, x1)
}

// ClearRun clears the half-open column span [x0,x1) in row y.
func (m *RasterMask) ClearRun(y, x0, x1 int) {
	if m == nil || y < 0 || y >= m.Rows {
		return
	}
	x0, x1 = clampSpan(x0, x1, m.Columns)
	if x0 >= x1 {
		return
	}
	m.runs[y] = removeRun(m.runs[y], x0, x1)
}

// Runs returns the run list of row y (do not mutate).
func (m *RasterMask) Runs(y int) []MaskRun {
	if m == nil || y < 0 || y >= m.Rows {
		return nil
	}
	return m.runs[y]
}

// ForEachRun invokes fn for every run in row order.
func (m *RasterMask) ForEachRun(fn func(y int, run MaskRun)) {
	if m == nil || fn == nil {
		return
	}
	for y, rs := range m.runs {
		for _, r := range rs {
			fn(y, r)
		}
	}
}

// ForEachPixel invokes fn for every set pixel in row-major order.
func (m *RasterMask) ForEachPixel(fn func(x, y int)) {
	if m == nil || fn == nil {
		return
	}
	for y, rs := range m.runs {
		for _, r := range rs {
			for x := r.Start; x < r.End; x++ {
				fn(x, y)
			}
		}
	}
}

// Count returns the number of set pixels.
func (m *RasterMask) Count() int {
	if m == nil {
		return 0
	}
	n := 0
	for _, rs := range m.runs {
		for _, r := range rs {
			n += r.End - r.Start
		}
	}
	return n
}

// Empty reports whether no pixels are set.
func (m *RasterMask) Empty() bool { return m.Count() == 0 }

// Clone returns a deep copy of the mask.
func (m *RasterMask) Clone() *RasterMask {
	if m == nil {
		return nil
	}
	out := NewRasterMask(m.Columns, m.Rows)
	for y, rs := range m.runs {
		if len(rs) == 0 {
			continue
		}
		out.runs[y] = append([]MaskRun(nil), rs...)
	}
	return out
}

// Union sets every pixel that is set in other (same dimensions assumed; pixels
// outside this mask are ignored).
func (m *RasterMask) Union(other *RasterMask) {
	if m == nil || other == nil {
		return
	}
	if m == other {
		return
	}
	rows := min(min(m.Rows, len(m.runs)), min(other.Rows, len(other.runs)))
	for y := 0; y < rows; y++ {
		if len(other.runs[y]) == 0 {
			continue
		}
		m.runs[y] = unionRunLists(m.runs[y], other.runs[y], m.Columns)
	}
}

// Intersect keeps only pixels present in both masks. A nil or mismatched mask
// produces an empty result rather than attempting an ambiguous resample.
func (m *RasterMask) Intersect(other *RasterMask) {
	if m == nil {
		return
	}
	if other == nil || m.Columns != other.Columns || m.Rows != other.Rows {
		for y := range m.runs {
			m.runs[y] = nil
		}
		return
	}
	for y := range m.runs {
		left, right := m.runs[y], other.runs[y]
		out := make([]MaskRun, 0, min(len(left), len(right)))
		for i, j := 0, 0; i < len(left) && j < len(right); {
			start := max(left[i].Start, right[j].Start)
			end := min(left[i].End, right[j].End)
			if start < end {
				out = append(out, MaskRun{Start: start, End: end})
			}
			if left[i].End < right[j].End {
				i++
			} else {
				j++
			}
		}
		m.runs[y] = out
	}
}

// Subtract clears every pixel present in other. Nil and grid-mismatched masks
// are fail-closed no-ops; higher-level geometry-aware callers validate grids
// before invoking this primitive.
func (m *RasterMask) Subtract(other *RasterMask) {
	if m == nil || other == nil || m.Columns != other.Columns || m.Rows != other.Rows {
		return
	}
	for y := range m.runs {
		for _, run := range other.runs[y] {
			m.runs[y] = removeRun(m.runs[y], run.Start, run.End)
		}
	}
}

// XOR replaces this mask with its symmetric difference with other: pixels set
// in exactly one mask remain set. Pixels outside this mask's dimensions are
// ignored.
func (m *RasterMask) XOR(other *RasterMask) {
	if m == nil || other == nil {
		return
	}
	rows := min(min(m.Rows, len(m.runs)), min(other.Rows, len(other.runs)))
	for y := 0; y < rows; y++ {
		otherRuns := make([]MaskRun, 0, len(other.runs[y]))
		for _, run := range other.runs[y] {
			x0, x1 := clampSpan(run.Start, run.End, m.Columns)
			if x0 < x1 {
				otherRuns = addRun(otherRuns, x0, x1)
			}
		}
		m.runs[y] = xorRuns(m.runs[y], otherRuns)
	}
}

func clampSpan(x0, x1, columns int) (int, int) {
	if x0 < 0 {
		x0 = 0
	}
	if x1 > columns {
		x1 = columns
	}
	return x0, x1
}

// addRun unions [x0,x1) into a sorted, merged run list (touching runs merge).
func addRun(runs []MaskRun, x0, x1 int) []MaskRun {
	out := make([]MaskRun, 0, len(runs)+1)
	i := 0
	for i < len(runs) && runs[i].End < x0 {
		out = append(out, runs[i])
		i++
	}
	ns, ne := x0, x1
	for i < len(runs) && runs[i].Start <= x1 {
		if runs[i].Start < ns {
			ns = runs[i].Start
		}
		if runs[i].End > ne {
			ne = runs[i].End
		}
		i++
	}
	out = append(out, MaskRun{Start: ns, End: ne})
	for i < len(runs) {
		out = append(out, runs[i])
		i++
	}
	return out
}

// unionRunLists merges two sorted run lists in one pass. Runs from second are
// clamped to columns so Union retains its behavior for differently sized masks.
func unionRunLists(first, second []MaskRun, columns int) []MaskRun {
	out := make([]MaskRun, 0, len(first)+len(second))
	firstIndex, secondIndex := 0, 0
	for firstIndex < len(first) || secondIndex < len(second) {
		var run MaskRun
		if secondIndex >= len(second) || (firstIndex < len(first) && first[firstIndex].Start <= second[secondIndex].Start) {
			run = first[firstIndex]
			firstIndex++
		} else {
			run = second[secondIndex]
			secondIndex++
		}
		x0, x1 := clampSpan(run.Start, run.End, columns)
		if x0 >= x1 {
			continue
		}
		if len(out) > 0 && x0 <= out[len(out)-1].End {
			if x1 > out[len(out)-1].End {
				out[len(out)-1].End = x1
			}
			continue
		}
		out = append(out, MaskRun{Start: x0, End: x1})
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func intersectRunLists(first, second []MaskRun) []MaskRun {
	out := make([]MaskRun, 0, min(len(first), len(second)))
	firstIndex, secondIndex := 0, 0
	for firstIndex < len(first) && secondIndex < len(second) {
		start := max(first[firstIndex].Start, second[secondIndex].Start)
		end := min(first[firstIndex].End, second[secondIndex].End)
		if start < end {
			out = append(out, MaskRun{Start: start, End: end})
		}
		if first[firstIndex].End < second[secondIndex].End {
			firstIndex++
		} else {
			secondIndex++
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// removeRun subtracts [x0,x1) from a sorted run list, splitting runs as needed.
func removeRun(runs []MaskRun, x0, x1 int) []MaskRun {
	out := make([]MaskRun, 0, len(runs)+1)
	for _, r := range runs {
		if r.End <= x0 || r.Start >= x1 {
			out = append(out, r)
			continue
		}
		if r.Start < x0 {
			out = append(out, MaskRun{Start: r.Start, End: x0})
		}
		if r.End > x1 {
			out = append(out, MaskRun{Start: x1, End: r.End})
		}
	}
	return out
}

func xorRuns(a, b []MaskRun) []MaskRun {
	const maxInt = int(^uint(0) >> 1)
	ia, ib := 0, 0
	inA, inB := false, false
	start := 0
	out := make([]MaskRun, 0, len(a)+len(b))
	for {
		nextA := maxInt
		if ia < len(a) {
			if inA {
				nextA = a[ia].End
			} else {
				nextA = a[ia].Start
			}
		}
		nextB := maxInt
		if ib < len(b) {
			if inB {
				nextB = b[ib].End
			} else {
				nextB = b[ib].Start
			}
		}
		x := min(nextA, nextB)
		if x == maxInt {
			break
		}
		wasSet := inA != inB
		if nextA == x {
			if inA {
				inA = false
				ia++
			} else {
				inA = true
			}
		}
		if nextB == x {
			if inB {
				inB = false
				ib++
			} else {
				inB = true
			}
		}
		isSet := inA != inB
		if !wasSet && isSet {
			start = x
		}
		if wasSet && !isSet {
			out = append(out, MaskRun{Start: start, End: x})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
