package render

import (
	"container/heap"
	"context"
	"errors"
	"fmt"
	"math"
)

var (
	// ErrCPRPathAssistantInput reports an invalid volume or waypoint set. The
	// interactive CPR workflow accepts two through five patient-space points.
	ErrCPRPathAssistantInput = errors.New("render: invalid CPR path assistant input")
	// ErrCPRPathAssistantMemory reports that a bounded search grid could not be
	// produced within the configured memory budget.
	ErrCPRPathAssistantMemory = errors.New("render: CPR path assistant search exceeds memory budget")
	// ErrCPRPathAssistantNotFound reports that no connected image-space route
	// could be reconstructed between two consecutive waypoints.
	ErrCPRPathAssistantNotFound = errors.New("render: CPR path assistant route not found")
)

// CPRPathAssistantOptions controls the image-derived centerline search. The
// search uses an adaptively downsampled, volume-axis-aligned grid and therefore
// stays bounded for large clinical series.
type CPRPathAssistantOptions struct {
	GridSpacingMM   float64
	MarginMM        float64
	OutputSpacingMM float64
	IntensityWeight float64
	MaxGridVoxels   int
}

// CPRPathAssistantSegmentError identifies the waypoint pair that could not be
// connected. From and To are one-based so callers can present A/B/C-style
// feedback without parsing an error string.
type CPRPathAssistantSegmentError struct {
	From int
	To   int
	Err  error
}

func (e *CPRPathAssistantSegmentError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("waypoints %d-%d: %v", e.From, e.To, e.Err)
}

func (e *CPRPathAssistantSegmentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

const (
	defaultCPRAssistantGridSpacingMM   = 2.0
	defaultCPRAssistantMarginMM        = 30.0
	defaultCPRAssistantOutputSpacingMM = 3.0
	defaultCPRAssistantIntensityWeight = 64.0
	defaultCPRAssistantMaxGridVoxels   = 1_000_000
)

// FindCPRAssistedPath finds an image-derived patient-space centerline through
// each consecutive pair of waypoints. Traversal favors voxels whose intensity
// resembles the endpoint seeds, while physical edge lengths keep the result
// spacing-aware. Each segment is re-sampled independently so intermediate
// user waypoints remain exact and the final output is reversible in the UI.
func FindCPRAssistedPath(ctx context.Context, volume *Volume, waypoints []Vec3, options CPRPathAssistantOptions) ([]Vec3, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if volume == nil || volume.Cols <= 0 || volume.Rows <= 0 || volume.Depth <= 0 || len(waypoints) < 2 || len(waypoints) > 5 {
		return nil, ErrCPRPathAssistantInput
	}
	for i, point := range waypoints {
		if !finiteVec3(point) || !voxelInsideVolume(volume, volume.PatientToVoxel(point)) {
			return nil, fmt.Errorf("%w: waypoint %d lies outside the volume", ErrCPRPathAssistantInput, i+1)
		}
	}

	options = normalizedCPRPathAssistantOptions(options)
	centerline := make([]Vec3, 0, len(waypoints)*16)
	for i := 1; i < len(waypoints); i++ {
		segment, err := findCPRAssistedSegment(ctx, volume, waypoints[i-1], waypoints[i], options)
		if err != nil {
			return nil, &CPRPathAssistantSegmentError{From: i, To: i + 1, Err: err}
		}
		segment = resampleCPRAssistedSegment(segment, options.OutputSpacingMM)
		if len(segment) < 2 {
			return nil, &CPRPathAssistantSegmentError{From: i, To: i + 1, Err: ErrCPRPathAssistantNotFound}
		}
		if len(centerline) > 0 && segment[0].Sub(centerline[len(centerline)-1]).Length() < 1e-9 {
			segment = segment[1:]
		}
		centerline = append(centerline, segment...)
	}
	return centerline, nil
}

func normalizedCPRPathAssistantOptions(options CPRPathAssistantOptions) CPRPathAssistantOptions {
	if !positiveFinite(options.GridSpacingMM) {
		options.GridSpacingMM = defaultCPRAssistantGridSpacingMM
	}
	if !positiveFinite(options.MarginMM) {
		options.MarginMM = defaultCPRAssistantMarginMM
	}
	if !positiveFinite(options.OutputSpacingMM) {
		options.OutputSpacingMM = defaultCPRAssistantOutputSpacingMM
	}
	if !positiveFinite(options.IntensityWeight) {
		options.IntensityWeight = defaultCPRAssistantIntensityWeight
	}
	if options.MaxGridVoxels <= 0 {
		options.MaxGridVoxels = defaultCPRAssistantMaxGridVoxels
	}
	return options
}

type cprAssistantGrid struct {
	volume     *Volume
	min        Vec3
	step       Vec3
	nx, ny, nz int
	values     []float64
	valid      []bool
	valueMin   float64
	valueMax   float64
}

func newCPRAssistantGrid(ctx context.Context, volume *Volume, start, end Vec3, options CPRPathAssistantOptions) (*cprAssistantGrid, error) {
	startVoxel := volume.PatientToVoxel(start)
	endVoxel := volume.PatientToVoxel(end)
	spacing := Vec3{X: volume.ColSpacing, Y: volume.RowSpacing, Z: volume.SliceSpacing}
	for _, value := range []*float64{&spacing.X, &spacing.Y, &spacing.Z} {
		if !positiveFinite(*value) {
			*value = 1
		}
	}

	gridSpacing := options.GridSpacingMM
	for attempt := 0; attempt < 8; attempt++ {
		margin := Vec3{X: options.MarginMM / spacing.X, Y: options.MarginMM / spacing.Y, Z: options.MarginMM / spacing.Z}
		minVoxel := Vec3{
			X: math.Max(0, math.Min(startVoxel.X, endVoxel.X)-margin.X),
			Y: math.Max(0, math.Min(startVoxel.Y, endVoxel.Y)-margin.Y),
			Z: math.Max(0, math.Min(startVoxel.Z, endVoxel.Z)-margin.Z),
		}
		maxVoxel := Vec3{
			X: math.Min(float64(volume.Cols-1), math.Max(startVoxel.X, endVoxel.X)+margin.X),
			Y: math.Min(float64(volume.Rows-1), math.Max(startVoxel.Y, endVoxel.Y)+margin.Y),
			Z: math.Min(float64(volume.Depth-1), math.Max(startVoxel.Z, endVoxel.Z)+margin.Z),
		}
		step := Vec3{
			X: math.Max(1, gridSpacing/spacing.X),
			Y: math.Max(1, gridSpacing/spacing.Y),
			Z: math.Max(1, gridSpacing/spacing.Z),
		}
		nx := int(math.Ceil((maxVoxel.X-minVoxel.X)/step.X)) + 1
		ny := int(math.Ceil((maxVoxel.Y-minVoxel.Y)/step.Y)) + 1
		nz := int(math.Ceil((maxVoxel.Z-minVoxel.Z)/step.Z)) + 1
		count64 := int64(nx) * int64(ny) * int64(nz)
		if nx > 0 && ny > 0 && nz > 0 && count64 > 0 && count64 <= int64(options.MaxGridVoxels) {
			grid := &cprAssistantGrid{
				volume: volume, min: minVoxel, step: step,
				nx: nx, ny: ny, nz: nz,
				values: make([]float64, int(count64)), valid: make([]bool, int(count64)),
				valueMin: math.Inf(1), valueMax: math.Inf(-1),
			}
			if err := grid.sampleValues(ctx); err != nil {
				return nil, err
			}
			return grid, nil
		}
		if count64 <= 0 {
			break
		}
		ratio := math.Cbrt(float64(count64) / float64(options.MaxGridVoxels))
		if ratio < 1.15 {
			ratio = 1.15
		}
		gridSpacing *= ratio * 1.02
	}
	return nil, ErrCPRPathAssistantMemory
}

func (g *cprAssistantGrid) sampleValues(ctx context.Context) error {
	sampler, ok := newVolumeSampler(g.volume)
	if !ok {
		return ErrCPRInput
	}
	defer sampler.Close()
	for index := range g.values {
		if index&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		voxel := g.voxelAt(index)
		patient := g.volume.VoxelToPatient(voxel)
		value, sampleOK := sampler.trilinearAt(sampler.vol.PatientToVoxel(patient))
		if !sampleOK || math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		g.values[index] = value
		g.valid[index] = true
		g.valueMin = math.Min(g.valueMin, value)
		g.valueMax = math.Max(g.valueMax, value)
	}
	return nil
}

func (g *cprAssistantGrid) index(x, y, z int) int { return (z*g.ny+y)*g.nx + x }

func (g *cprAssistantGrid) xyz(index int) (int, int, int) {
	x := index % g.nx
	rest := index / g.nx
	return x, rest % g.ny, rest / g.ny
}

func (g *cprAssistantGrid) voxelAt(index int) Vec3 {
	x, y, z := g.xyz(index)
	return Vec3{
		X: math.Min(float64(g.volume.Cols-1), g.min.X+float64(x)*g.step.X),
		Y: math.Min(float64(g.volume.Rows-1), g.min.Y+float64(y)*g.step.Y),
		Z: math.Min(float64(g.volume.Depth-1), g.min.Z+float64(z)*g.step.Z),
	}
}

func (g *cprAssistantGrid) nearestIndex(voxel Vec3) int {
	x := clampInt(int(math.Round((voxel.X-g.min.X)/g.step.X)), 0, g.nx-1)
	y := clampInt(int(math.Round((voxel.Y-g.min.Y)/g.step.Y)), 0, g.ny-1)
	z := clampInt(int(math.Round((voxel.Z-g.min.Z)/g.step.Z)), 0, g.nz-1)
	return g.index(x, y, z)
}

func findCPRAssistedSegment(ctx context.Context, volume *Volume, start, end Vec3, options CPRPathAssistantOptions) ([]Vec3, error) {
	grid, err := newCPRAssistantGrid(ctx, volume, start, end, options)
	if err != nil {
		return nil, err
	}
	startIndex := grid.nearestIndex(volume.PatientToVoxel(start))
	endIndex := grid.nearestIndex(volume.PatientToVoxel(end))
	if !grid.valid[startIndex] || !grid.valid[endIndex] {
		return nil, ErrCPRPathAssistantNotFound
	}
	if startIndex == endIndex {
		return []Vec3{start, end}, nil
	}

	seedValue := (grid.values[startIndex] + grid.values[endIndex]) / 2
	valueRange := grid.valueMax - grid.valueMin
	if !positiveFinite(valueRange) {
		valueRange = 1
	}
	penaltyAt := func(index int) float64 {
		deviation := math.Abs(grid.values[index]-seedValue) / valueRange
		return 1 + options.IntensityWeight*deviation*deviation
	}

	count := len(grid.values)
	distances := make([]float64, count)
	previous := make([]int32, count)
	visited := make([]bool, count)
	for i := range distances {
		distances[i] = math.Inf(1)
		previous[i] = -1
	}
	distances[startIndex] = 0
	queue := cprAssistantPriorityQueue{{index: startIndex, cost: 0}}
	heap.Init(&queue)

	for pops := 0; queue.Len() > 0; pops++ {
		if pops&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		current := heap.Pop(&queue).(cprAssistantQueueItem)
		if visited[current.index] || current.cost != distances[current.index] {
			continue
		}
		visited[current.index] = true
		if current.index == endIndex {
			break
		}
		x, y, z := grid.xyz(current.index)
		currentVoxel := grid.voxelAt(current.index)
		currentPenalty := penaltyAt(current.index)
		for dz := -1; dz <= 1; dz++ {
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					if dx == 0 && dy == 0 && dz == 0 {
						continue
					}
					nx, ny, nz := x+dx, y+dy, z+dz
					if nx < 0 || ny < 0 || nz < 0 || nx >= grid.nx || ny >= grid.ny || nz >= grid.nz {
						continue
					}
					nextIndex := grid.index(nx, ny, nz)
					if visited[nextIndex] || !grid.valid[nextIndex] {
						continue
					}
					edgeLength := volume.VoxelToPatient(grid.voxelAt(nextIndex)).Sub(volume.VoxelToPatient(currentVoxel)).Length()
					candidate := current.cost + edgeLength*(currentPenalty+penaltyAt(nextIndex))/2
					if candidate < distances[nextIndex] {
						distances[nextIndex] = candidate
						previous[nextIndex] = int32(current.index)
						heap.Push(&queue, cprAssistantQueueItem{index: nextIndex, cost: candidate})
					}
				}
			}
		}
	}
	if !visited[endIndex] {
		return nil, ErrCPRPathAssistantNotFound
	}

	indices := make([]int, 0, 64)
	for index := endIndex; index >= 0; index = int(previous[index]) {
		indices = append(indices, index)
		if index == startIndex {
			break
		}
	}
	if len(indices) == 0 || indices[len(indices)-1] != startIndex {
		return nil, ErrCPRPathAssistantNotFound
	}
	path := make([]Vec3, 0, len(indices)+2)
	path = append(path, start)
	for i := len(indices) - 2; i > 0; i-- {
		path = append(path, volume.VoxelToPatient(grid.voxelAt(indices[i])))
	}
	path = append(path, end)
	return path, nil
}

func resampleCPRAssistedSegment(points []Vec3, spacing float64) []Vec3 {
	path := NewCPRPath(points)
	if path == nil {
		return nil
	}
	count := int(math.Floor(path.Length() / spacing))
	result := make([]Vec3, 0, count+2)
	for i := 0; i <= count; i++ {
		result = append(result, path.PointAt(float64(i)*spacing))
	}
	end := path.PointAt(path.Length())
	if result[len(result)-1].Sub(end).Length() > 1e-9 {
		result = append(result, end)
	} else {
		result[len(result)-1] = end
	}
	return result
}

type cprAssistantQueueItem struct {
	index int
	cost  float64
}

type cprAssistantPriorityQueue []cprAssistantQueueItem

func (q cprAssistantPriorityQueue) Len() int           { return len(q) }
func (q cprAssistantPriorityQueue) Less(i, j int) bool { return q[i].cost < q[j].cost }
func (q cprAssistantPriorityQueue) Swap(i, j int)      { q[i], q[j] = q[j], q[i] }
func (q *cprAssistantPriorityQueue) Push(value any)    { *q = append(*q, value.(cprAssistantQueueItem)) }
func (q *cprAssistantPriorityQueue) Pop() any {
	old := *q
	last := old[len(old)-1]
	*q = old[:len(old)-1]
	return last
}

func voxelInsideVolume(volume *Volume, point Vec3) bool {
	return point.X >= 0 && point.Y >= 0 && point.Z >= 0 &&
		point.X <= float64(volume.Cols-1) && point.Y <= float64(volume.Rows-1) && point.Z <= float64(volume.Depth-1)
}

func finiteVec3(point Vec3) bool {
	return !math.IsNaN(point.X) && !math.IsNaN(point.Y) && !math.IsNaN(point.Z) &&
		!math.IsInf(point.X, 0) && !math.IsInf(point.Y, 0) && !math.IsInf(point.Z, 0)
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func clampInt(value, minValue, maxValue int) int {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}
