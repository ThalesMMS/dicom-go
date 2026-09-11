package roi

import (
	"context"
	"errors"
	"fmt"
	"math"
)

var (
	// ErrGrowCutInvalidDimensions reports a zero or negative image/volume size.
	ErrGrowCutInvalidDimensions = errors.New("roi: invalid GrowCut dimensions")
	// ErrGrowCutDimensionOverflow reports dimensions whose voxel count cannot be
	// represented by an int without wrapping.
	ErrGrowCutDimensionOverflow = errors.New("roi: GrowCut dimensions overflow")
	// ErrGrowCutResourceLimit reports a voxel, byte, or pending-queue limit.
	ErrGrowCutResourceLimit = errors.New("roi: GrowCut resource limit exceeded")
	// ErrGrowCutInvalidSeeds reports nil, mismatched, out-of-range, or malformed
	// seed masks.
	ErrGrowCutInvalidSeeds = errors.New("roi: invalid GrowCut seeds")
	// ErrGrowCutMissingSeeds reports a missing foreground or background seed.
	ErrGrowCutMissingSeeds = errors.New("roi: GrowCut requires foreground and background seeds")
	// ErrGrowCutNonFiniteValue reports a valueAt callback that returned NaN or an
	// infinity as a valid voxel value.
	ErrGrowCutNonFiniteValue = errors.New("roi: GrowCut value is not finite")
	// ErrGrowCutCancelled reports cancellation while GrowCut was running. The
	// returned error also wraps context.Canceled or context.DeadlineExceeded.
	ErrGrowCutCancelled = errors.New("roi: GrowCut cancelled")
	// ErrGrowCutInvalidValueSource reports a missing voxel callback.
	ErrGrowCutInvalidValueSource = errors.New("roi: GrowCut value source is nil")
	// ErrGrowCutInvalidLimits reports a negative resource limit.
	ErrGrowCutInvalidLimits = errors.New("roi: invalid GrowCut resource limits")
)

const (
	defaultGrowCutMaxVoxels     = 32 * 1024 * 1024
	defaultGrowCutMaxBytes      = int64(768 * 1024 * 1024)
	defaultGrowCutMaxQueueItems = 4 * 1024 * 1024
	// These are conservative working-set estimates for the dense value/label
	// arrays and a queued item. The queue is separately capped at runtime.
	growCutDenseBytesPerVoxel = int64(18)
	growCutQueueItemBytes     = int64(32)
)

// GrowCutLimits bounds the allocations and work queue used by the checked
// GrowCut entry points. A zero field uses the corresponding default; negative
// fields are rejected.
type GrowCutLimits struct {
	MaxVoxels     int
	MaxBytes      int64
	MaxQueueItems int
}

// DefaultGrowCutLimits returns conservative limits suitable for interactive
// segmentation without allowing unchecked dense allocations.
func DefaultGrowCutLimits() GrowCutLimits {
	return GrowCutLimits{
		MaxVoxels:     defaultGrowCutMaxVoxels,
		MaxBytes:      defaultGrowCutMaxBytes,
		MaxQueueItems: defaultGrowCutMaxQueueItems,
	}
}

func normalizeGrowCutLimits(limits GrowCutLimits) (GrowCutLimits, error) {
	if limits.MaxVoxels < 0 || limits.MaxBytes < 0 || limits.MaxQueueItems < 0 {
		return GrowCutLimits{}, ErrGrowCutInvalidLimits
	}
	defaults := DefaultGrowCutLimits()
	if limits.MaxVoxels == 0 {
		limits.MaxVoxels = defaults.MaxVoxels
	}
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxQueueItems == 0 {
		limits.MaxQueueItems = defaults.MaxQueueItems
	}
	return limits, nil
}

// GrowCut2DContext is the resource-bounded, cancellable form of GrowCut2D.
// It preserves the legacy algorithm on valid inputs while validating all
// dimensions and seeds before allocating dense working buffers.
func GrowCut2DContext(ctx context.Context, columns, rows int, foreground, background *RasterMask, valueAt func(x, y int) (float64, bool), limits GrowCutLimits) (*RasterMask, error) {
	if valueAt == nil {
		return nil, ErrGrowCutInvalidValueSource
	}
	foregroundSeeds := map[int]*RasterMask{}
	if foreground != nil {
		foregroundSeeds[0] = foreground
	}
	backgroundSeeds := map[int]*RasterMask{}
	if background != nil {
		backgroundSeeds[0] = background
	}
	result, err := growCutLabelsContext(ctx, columns, rows, 1,
		foregroundSeeds, backgroundSeeds, false,
		func(x, y, _ int) (float64, bool) {
			if valueAt == nil {
				return 0, false
			}
			return valueAt(x, y)
		}, limits, nil)
	if err != nil {
		return nil, err
	}
	if mask := result[0]; mask != nil {
		return mask, nil
	}
	return NewRasterMask(columns, rows), nil
}

// GrowCut3DContext is the resource-bounded, cancellable form of GrowCut3D.
// Six-connected neighbours compete across slices and the input masks are
// never mutated.
func GrowCut3DContext(ctx context.Context, columns, rows, slices int, foreground, background map[int]*RasterMask, valueAt func(x, y, slice int) (float64, bool), limits GrowCutLimits) (map[int]*RasterMask, error) {
	return growCutLabelsContext(ctx, columns, rows, slices, foreground, background, true, valueAt, limits, nil)
}

// growCutRunMetrics is optional benchmark instrumentation. Keeping it internal
// avoids adding diagnostic state to the clinical API while making queue growth
// and stale work directly measurable.
type growCutRunMetrics struct {
	queuePushes    int
	queuePops      int
	stalePops      int
	peakQueueItems int
}

func growCutContextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", ErrGrowCutCancelled, err)
	}
	return nil
}

func validateGrowCutDimensions(columns, rows, slices int, limits GrowCutLimits) (int, int, error) {
	if columns <= 0 || rows <= 0 || slices <= 0 {
		return 0, 0, ErrGrowCutInvalidDimensions
	}
	maxInt := int(^uint(0) >> 1)
	if columns > maxInt/rows {
		return 0, 0, ErrGrowCutDimensionOverflow
	}
	plane := columns * rows
	if plane > maxInt/slices {
		return 0, 0, ErrGrowCutDimensionOverflow
	}
	total := plane * slices
	if total > limits.MaxVoxels {
		return 0, 0, fmt.Errorf("%w: %d voxels exceeds %d", ErrGrowCutResourceLimit, total, limits.MaxVoxels)
	}
	estimated, ok := growCutEstimatedBytes(total, limits.MaxQueueItems)
	if !ok || estimated > limits.MaxBytes {
		return 0, 0, fmt.Errorf("%w: estimated working set %d bytes exceeds %d", ErrGrowCutResourceLimit, estimated, limits.MaxBytes)
	}
	return plane, total, nil
}

func growCutEstimatedBytes(total, queueItems int) (int64, bool) {
	if total < 0 || queueItems < 0 {
		return 0, false
	}
	total64, queue64 := int64(total), int64(queueItems)
	maxInt64 := int64(1<<63 - 1)
	if queue64 > maxInt64/growCutQueueItemBytes {
		return 0, false
	}
	queueBytes := queue64 * growCutQueueItemBytes
	if total64 > (maxInt64-queueBytes)/growCutDenseBytesPerVoxel {
		return 0, false
	}
	return total64*growCutDenseBytesPerVoxel + queueBytes, true
}

func validateGrowCutSeedMaps(columns, rows, slices int, foreground, background map[int]*RasterMask) error {
	foregroundCount, err := validateGrowCutSeedMap(columns, rows, slices, foreground)
	if err != nil {
		return err
	}
	backgroundCount, err := validateGrowCutSeedMap(columns, rows, slices, background)
	if err != nil {
		return err
	}
	if foregroundCount == 0 || backgroundCount == 0 {
		return ErrGrowCutMissingSeeds
	}
	return nil
}

func validateGrowCutSeedMap(columns, rows, slices int, masks map[int]*RasterMask) (int, error) {
	count := 0
	for slice, mask := range masks {
		if slice < 0 || slice >= slices || mask == nil || mask.Columns != columns || mask.Rows != rows {
			return 0, fmt.Errorf("%w: slice %d has incompatible dimensions", ErrGrowCutInvalidSeeds, slice)
		}
		lastY, lastEnd := -1, 0
		invalid := false
		mask.ForEachRun(func(y int, run MaskRun) {
			if invalid {
				return
			}
			if y < 0 || y >= rows || run.Start < 0 || run.Start >= run.End || run.End > columns {
				invalid = true
				return
			}
			if y != lastY {
				lastY, lastEnd = y, 0
			}
			if run.Start < lastEnd {
				invalid = true
				return
			}
			lastEnd = run.End
			count += run.End - run.Start
		})
		if invalid {
			return 0, fmt.Errorf("%w: malformed run in slice %d", ErrGrowCutInvalidSeeds, slice)
		}
	}
	return count, nil
}

func growCutLabelsContext(ctx context.Context, columns, rows, slices int, foreground, background map[int]*RasterMask, includeSliceNeighbours bool, valueAt func(x, y, slice int) (float64, bool), limits GrowCutLimits, metrics *growCutRunMetrics) (map[int]*RasterMask, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := growCutContextError(ctx); err != nil {
		return nil, err
	}
	if valueAt == nil {
		return nil, ErrGrowCutInvalidValueSource
	}
	normalized, err := normalizeGrowCutLimits(limits)
	if err != nil {
		return nil, err
	}
	plane, total, err := validateGrowCutDimensions(columns, rows, slices, normalized)
	if err != nil {
		return nil, err
	}
	if err := validateGrowCutSeedMaps(columns, rows, slices, foreground, background); err != nil {
		return nil, err
	}

	values := make([]float64, total)
	valid := make([]bool, total)
	minimum := math.Inf(1)
	maximum := math.Inf(-1)
	for z := 0; z < slices; z++ {
		for y := 0; y < rows; y++ {
			if err := growCutContextError(ctx); err != nil {
				return nil, err
			}
			for x := 0; x < columns; x++ {
				value, ok := valueAt(x, y, z)
				if !ok {
					continue
				}
				if math.IsNaN(value) || math.IsInf(value, 0) {
					return nil, fmt.Errorf("%w at (%d,%d,%d)", ErrGrowCutNonFiniteValue, x, y, z)
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
		return map[int]*RasterMask{}, nil
	}
	contrast := maximum - minimum
	if contrast <= 0 || math.IsNaN(contrast) || math.IsInf(contrast, 0) {
		contrast = 1
	}

	labels := make([]uint8, total)
	strengths := make([]float64, total)
	queue := &growCutPriorityQueue{}
	nextOrder := 0
	push := func(item growCutQueueItem) error {
		if queue.Len() >= normalized.MaxQueueItems {
			return fmt.Errorf("%w: queue reached %d items", ErrGrowCutResourceLimit, normalized.MaxQueueItems)
		}
		queue.push(item)
		if metrics != nil {
			metrics.queuePushes++
			metrics.peakQueueItems = max(metrics.peakQueueItems, queue.Len())
		}
		return nil
	}
	seed := func(masks map[int]*RasterMask, label uint8) error {
		for z, mask := range masks {
			if err := growCutContextError(ctx); err != nil {
				return err
			}
			var seedErr error
			mask.ForEachRun(func(y int, run MaskRun) {
				if seedErr != nil || y < 0 || y >= rows {
					return
				}
				for x := run.Start; x < run.End; x++ {
					if seedErr = growCutContextError(ctx); seedErr != nil {
						return
					}
					if !valid[z*plane+y*columns+x] || strengths[z*plane+y*columns+x] >= 1 {
						continue
					}
					index := z*plane + y*columns + x
					labels[index] = label
					strengths[index] = 1
					if pushErr := push(growCutQueueItem{index: index, strength: 1, label: label, order: nextOrder}); pushErr != nil {
						seedErr = pushErr
						return
					}
					nextOrder++
				}
			})
			if seedErr != nil {
				return seedErr
			}
		}
		return nil
	}
	// Foreground wins overlapping seed pixels, matching the legacy result.
	if err := seed(foreground, growCutForeground); err != nil {
		return nil, err
	}
	if err := seed(background, growCutBackground); err != nil {
		return nil, err
	}
	if queue.Len() == 0 {
		return map[int]*RasterMask{}, nil
	}

	for queue.Len() > 0 {
		if err := growCutContextError(ctx); err != nil {
			return nil, err
		}
		item := queue.pop()
		if metrics != nil {
			metrics.queuePops++
		}
		if labels[item.index] != item.label || math.Abs(strengths[item.index]-item.strength) > 1e-12 {
			if metrics != nil {
				metrics.stalePops++
			}
			continue
		}
		z := item.index / plane
		remainder := item.index % plane
		y := remainder / columns
		x := remainder % columns
		var visitErr error
		visit := func(nx, ny, nz int) {
			if visitErr != nil || nx < 0 || nx >= columns || ny < 0 || ny >= rows || nz < 0 || nz >= slices {
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
			if err := push(growCutQueueItem{index: neighbour, strength: attack, label: item.label, order: nextOrder}); err != nil {
				visitErr = err
				return
			}
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
		if visitErr != nil {
			return nil, visitErr
		}
	}

	result := make(map[int]*RasterMask)
	growCutAppendResultRuns(result, labels, columns, rows, slices, plane)
	return result, nil
}
