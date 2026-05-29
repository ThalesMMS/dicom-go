package render

import (
	"context"
	"fmt"
	"image"
)

// ScalarPlane is an immutable MPR geometry result in modality-value space.
// It retains only source fallback metadata needed by the standalone
// presentation API; explicit Window/Level and VOI LUT state remain external so
// callers may reuse the sampled values across presentation changes.
type ScalarPlane struct {
	width       int
	height      int
	values      []float64
	valid       []byte
	photometric string
	defaultVOI  WindowLevel
}

// Dimensions returns the immutable output dimensions.
func (p *ScalarPlane) Dimensions() (int, int) {
	if p == nil {
		return 0, 0
	}
	return p.width, p.height
}

// Bytes reports the retained payload size used by bounded viewer caches.
func (p *ScalarPlane) Bytes() int64 {
	if p == nil {
		return 0
	}
	return int64(len(p.values))*8 + int64(len(p.valid))
}

// ApplyWindow applies VOI/LUT presentation without recomputing MPR geometry.
func (p *ScalarPlane) ApplyWindow(window WindowLevel) image.Image {
	img, _ := p.ApplyWindowContext(context.Background(), window)
	return img
}

// ApplyWindowContext is the cancellable presentation pass for a scalar plane.
func (p *ScalarPlane) ApplyWindowContext(ctx context.Context, window WindowLevel) (image.Image, error) {
	if p == nil || p.width <= 0 || p.height <= 0 ||
		len(p.values) < p.width*p.height || len(p.valid) < p.width*p.height {
		return blankImage(512, 512), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	mapper := prepareWindow(normalizeWindow(window, p.defaultVOI))
	img := image.NewGray(image.Rect(0, 0, p.width, p.height))
	err := parallelRowsContext(ctx, p.height, func(y int) {
		offset := y * p.width
		for x := 0; x < p.width; x++ {
			if ctx.Err() != nil {
				return
			}
			index := offset + x
			if p.valid[index] == 0 {
				continue
			}
			img.Pix[index] = displayGrayMapped(float64(p.values[index]), mapper, p.photometric)
		}
	})
	return img, err
}

// RenderScalarSlabWithOptionsContext renders an orthogonal MPR plane in
// modality-value space. Cancellation is checked between rows, pixels, and slab
// samples so interactive supersession does not wait for a whole projection.
func RenderScalarSlabWithOptionsContext(
	ctx context.Context,
	series *Stack,
	plane MPRPlane,
	center, thickness int,
	mode SlabMode,
	options MPRRenderOptions,
) (*ScalarPlane, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if series == nil || len(series.Frames) == 0 {
		return nil, fmt.Errorf("render: no series selected")
	}
	first := firstRenderableSlice(series)
	if first == nil {
		return nil, fmt.Errorf("render: series has no displayable images")
	}
	rows := int(first.Metadata.Rows)
	cols := int(first.Metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("render: invalid slice dimensions")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	correctTilt := options.GantryTiltMode != GantryTiltSourceGeometry
	defaultVOI := normalizeWindow(
		series.DefaultWindow,
		WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth},
	)
	// The legacy CPU oracle renders a plain axial plane directly from its
	// source frame (float64 rescale), while projected slabs use the cached
	// float32 volume. Preserve that distinction exactly.
	if plane == MPRPlaneAxial && mode == SlabNone {
		return renderFallbackScalarSlabContext(
			ctx, series, plane, center, thickness, mode, rows, cols, defaultVOI,
		)
	}
	if vol, sampler, err := stackVolumeSampler(series, correctTilt); err == nil {
		defer sampler.Close()
		if scalar, rendered, renderErr := renderCachedScalarSlabContext(
			ctx, vol, sampler, plane, center, thickness, mode, correctTilt, defaultVOI,
		); rendered || renderErr != nil {
			return scalar, renderErr
		}
	} else if isNonCorrectableGeometry(err) {
		return nil, err
	}
	return renderFallbackScalarSlabContext(ctx, series, plane, center, thickness, mode, rows, cols, defaultVOI)
}

func renderCachedScalarSlabContext(
	ctx context.Context,
	vol *Volume,
	sampler *volumeSampler,
	plane MPRPlane,
	center, thickness int,
	mode SlabMode,
	correctGantryTilt bool,
	defaultVOI WindowLevel,
) (*ScalarPlane, bool, error) {
	if vol == nil || sampler == nil || sampler.rows <= 0 || sampler.cols <= 0 || sampler.depth <= 0 {
		return nil, false, nil
	}
	if correctGantryTilt {
		if corrected, width, height, ok := vol.correctedOrthogonalPlane(plane, center); ok {
			if mode == SlabNone || thickness <= 1 {
				scalar, err := resliceScalarWithSamplerContext(ctx, sampler, corrected, width, height, defaultVOI)
				return scalar, true, err
			}
			slabAxis, step, sampleCount := vol.correctedSlabAxis(plane, vol.Rows, vol.Cols)
			clampedCenter := clampIndex(center, sampleCount)
			start, end := mipProjectionRange(clampedCenter, thickness, sampleCount)
			scalar, err := resliceScalarSlabWithSamplerContext(
				ctx, sampler, corrected, slabAxis, width, height,
				float64(start-clampedCenter), end-start+1, step, mode, defaultVOI,
			)
			return scalar, true, err
		}
	}

	rows, cols, depth := sampler.rows, sampler.cols, sampler.depth
	switch plane {
	case MPRPlaneAxial:
		start, end := scalarProjectionRange(center, thickness, depth, mode)
		scalar := newScalarPlane(cols, rows, sampler.photometric, defaultVOI)
		err := parallelRowsContext(ctx, rows, func(y int) {
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(z int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(x, y, value, ok)
			}
		})
		return scalar, true, err
	case MPRPlaneCoronal:
		start, end := scalarProjectionRange(center, thickness, rows, mode)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.ColSpacing)
		scalar := newScalarPlane(cols, height, sampler.photometric, defaultVOI)
		err := parallelRowsContext(ctx, height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(y int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(x, outZ, value, ok)
			}
		})
		return scalar, true, err
	case MPRPlaneSagittal:
		start, end := scalarProjectionRange(center, thickness, cols, mode)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.RowSpacing)
		scalar := newScalarPlane(rows, height, sampler.photometric, defaultVOI)
		err := parallelRowsContext(ctx, height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			for y := 0; y < rows; y++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(x int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(y, outZ, value, ok)
			}
		})
		return scalar, true, err
	default:
		return nil, false, nil
	}
}

func renderFallbackScalarSlabContext(
	ctx context.Context,
	series *Stack,
	plane MPRPlane,
	center, thickness int,
	mode SlabMode,
	rows, cols int,
	defaultVOI WindowLevel,
) (*ScalarPlane, error) {
	photometric := firstRenderableSlice(series).Metadata.PhotometricInterpretation
	switch plane {
	case MPRPlaneAxial:
		start, end := scalarProjectionRange(center, thickness, len(series.Frames), mode)
		scalar := newScalarPlane(cols, rows, photometric, defaultVOI)
		err := parallelRowsContext(ctx, rows, func(y int) {
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(z int) (float64, bool) {
					value, _, ok := sampleVoxel(series.Frames[z], x, y)
					return value, ok
				})
				scalar.set(x, y, value, ok)
			}
		})
		return scalar, err
	case MPRPlaneCoronal:
		start, end := scalarProjectionRange(center, thickness, rows, mode)
		height := orthogonalMPRHeight(series, seriesColumnSpacing(series))
		scalar := newScalarPlane(cols, height, photometric, defaultVOI)
		err := parallelRowsContext(ctx, height, func(outZ int) {
			frame := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(y int) (float64, bool) {
					value, _, ok := sampleVoxel(frame, x, y)
					return value, ok
				})
				scalar.set(x, outZ, value, ok)
			}
		})
		return scalar, err
	case MPRPlaneSagittal:
		start, end := scalarProjectionRange(center, thickness, cols, mode)
		height := orthogonalMPRHeight(series, seriesRowSpacing(series))
		scalar := newScalarPlane(rows, height, photometric, defaultVOI)
		err := parallelRowsContext(ctx, height, func(outZ int) {
			frame := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for y := 0; y < rows; y++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(x int) (float64, bool) {
					value, _, ok := sampleVoxel(frame, x, y)
					return value, ok
				})
				scalar.set(y, outZ, value, ok)
			}
		})
		return scalar, err
	default:
		return nil, fmt.Errorf("render: unsupported slab plane %q", plane)
	}
}

// ResliceObliqueScalarContext renders one arbitrary plane without VOI.
func ResliceObliqueScalarContext(ctx context.Context, vol *Volume, plane Plane, outW, outH int) (*ScalarPlane, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if vol == nil || outW <= 0 || outH <= 0 {
		return newScalarPlane(maxInt(outW, 1), maxInt(outH, 1), "", defaultScalarVOI()), nil
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return newScalarPlane(outW, outH, vol.Photometric(), defaultScalarVOI()), nil
	}
	defer sampler.Close()
	return resliceScalarWithSamplerContext(ctx, sampler, plane, outW, outH, defaultScalarVOI())
}

// ResliceObliqueSlabScalarContext renders an arbitrary thick slab without VOI.
func ResliceObliqueSlabScalarContext(
	ctx context.Context,
	vol *Volume,
	plane Plane,
	outW, outH, thickness int,
	mode SlabMode,
) (*ScalarPlane, error) {
	if thickness <= 1 || mode == SlabNone {
		return ResliceObliqueScalarContext(ctx, vol, plane, outW, outH)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if vol == nil || outW <= 0 || outH <= 0 {
		return newScalarPlane(maxInt(outW, 1), maxInt(outH, 1), "", defaultScalarVOI()), nil
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return newScalarPlane(outW, outH, vol.Photometric(), defaultScalarVOI()), nil
	}
	defer sampler.Close()
	half := float64(thickness-1) / 2
	return resliceScalarSlabWithSamplerContext(
		ctx, sampler, plane, plane.U.Cross(plane.V), outW, outH,
		-half, thickness, vol.SliceSpacing, mode, defaultScalarVOI(),
	)
}

func resliceScalarWithSamplerContext(
	ctx context.Context,
	sampler *volumeSampler,
	plane Plane,
	outW, outH int,
	defaultVOI WindowLevel,
) (*ScalarPlane, error) {
	scalar := newScalarPlane(outW, outH, sampler.photometric, defaultVOI)
	denomW := float64(maxInt(outW-1, 1))
	denomH := float64(maxInt(outH-1, 1))
	err := parallelRowsContext(ctx, outH, func(y int) {
		t := float64(y) / denomH
		rowBase := plane.Origin.Add(plane.V.Scale(t))
		for x := 0; x < outW; x++ {
			if ctx.Err() != nil {
				return
			}
			point := rowBase.Add(plane.U.Scale(float64(x) / denomW))
			value, ok := sampler.trilinearAt(sampler.vol.PatientToVoxel(point))
			scalar.set(x, y, value, ok)
		}
	})
	return scalar, err
}

func resliceScalarSlabWithSamplerContext(
	ctx context.Context,
	sampler *volumeSampler,
	plane Plane,
	slabAxis Vec3,
	outW, outH int,
	firstOffset float64,
	sampleCount int,
	step float64,
	mode SlabMode,
	defaultVOI WindowLevel,
) (*ScalarPlane, error) {
	if step <= 0 {
		step = 1
	}
	normal := slabAxis.Normalize()
	scalar := newScalarPlane(outW, outH, sampler.photometric, defaultVOI)
	denomW := float64(maxInt(outW-1, 1))
	denomH := float64(maxInt(outH-1, 1))
	err := parallelRowsContext(ctx, outH, func(y int) {
		t := float64(y) / denomH
		for x := 0; x < outW; x++ {
			if ctx.Err() != nil {
				return
			}
			base := plane.At(float64(x)/denomW, t)
			value, ok := reduceCachedScalarContext(ctx, mode, 0, sampleCount-1, func(sample int) (float64, bool) {
				point := base.Add(normal.Scale((firstOffset + float64(sample)) * step))
				return sampler.trilinearAt(sampler.vol.PatientToVoxel(point))
			})
			scalar.set(x, y, value, ok)
		}
	})
	return scalar, err
}

func newScalarPlane(width, height int, photometric string, defaultVOI WindowLevel) *ScalarPlane {
	count := maxInt(width, 0) * maxInt(height, 0)
	return &ScalarPlane{
		width:       width,
		height:      height,
		values:      make([]float64, count),
		valid:       make([]byte, count),
		photometric: photometric,
		defaultVOI:  normalizeWindow(defaultVOI, defaultScalarVOI()),
	}
}

func defaultScalarVOI() WindowLevel {
	return WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth}
}

func (p *ScalarPlane) set(x, y int, value float64, ok bool) {
	if p == nil || !ok || x < 0 || y < 0 || x >= p.width || y >= p.height {
		return
	}
	index := y*p.width + x
	p.values[index] = value
	p.valid[index] = 1
}

func scalarProjectionRange(center, thickness, count int, mode SlabMode) (int, int) {
	if mode == SlabNone {
		thickness = 1
	}
	return mipProjectionRange(center, thickness, count)
}

func reduceCachedScalarContext(
	ctx context.Context,
	mode SlabMode,
	start, end int,
	sample func(int) (float64, bool),
) (float64, bool) {
	var acc float64
	count := 0
	for index := start; index <= end; index++ {
		if ctx != nil && ctx.Err() != nil {
			return 0, false
		}
		value, ok := sample(index)
		if !ok {
			continue
		}
		acc, count = accumulateSlabValue(mode, acc, count, value)
	}
	return finishSlabValue(mode, acc, count)
}
