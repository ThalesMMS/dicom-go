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

// ValueAt returns one retained modality value. ok is false outside the plane
// or where patient-space sampling found no source voxel.
func (p *ScalarPlane) ValueAt(x, y int) (float64, bool) {
	if p == nil || x < 0 || y < 0 || x >= p.width || y >= p.height {
		return 0, false
	}
	index := y*p.width + x
	if index >= len(p.values) || index >= len(p.valid) || p.valid[index] == 0 {
		return 0, false
	}
	return p.values[index], true
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
	if err != nil {
		return nil, err
	}
	return img, nil
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
	limits, err := normalizeMPRLimits(options.Limits)
	if err != nil {
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
			ctx, series, plane, center, thickness, mode, rows, cols, defaultVOI, limits,
		)
	}
	if vol, sampler, err := stackVolumeSampler(series, correctTilt); err == nil {
		defer sampler.Close()
		if scalar, rendered, renderErr := renderCachedScalarSlabContext(
			ctx, vol, sampler, plane, center, thickness, mode, correctTilt, defaultVOI, limits,
		); rendered || renderErr != nil {
			return scalar, renderErr
		}
	} else if isNonCorrectableGeometry(err) {
		return nil, err
	}
	return renderFallbackScalarSlabContext(ctx, series, plane, center, thickness, mode, rows, cols, defaultVOI, limits)
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
	limits MPRLimits,
) (*ScalarPlane, bool, error) {
	if vol == nil || sampler == nil || sampler.rows <= 0 || sampler.cols <= 0 || sampler.depth <= 0 {
		return nil, false, nil
	}
	if correctGantryTilt {
		if corrected, width, height, ok := vol.correctedOrthogonalPlane(plane, center); ok {
			if mode == SlabNone || thickness <= 1 {
				scalar, err := resliceScalarWithSamplerContext(ctx, sampler, corrected, width, height, defaultVOI, limits)
				return scalar, true, err
			}
			slabAxis, step, sampleCount := vol.correctedSlabAxis(plane, vol.Rows, vol.Cols)
			clampedCenter := clampIndex(center, sampleCount)
			start, end := mipProjectionRange(clampedCenter, thickness, sampleCount)
			scalar, err := resliceScalarSlabWithSamplerContext(
				ctx, sampler, corrected, slabAxis, width, height,
				float64(start-clampedCenter), end-start+1, step, mode, defaultVOI, limits,
			)
			return scalar, true, err
		}
	}

	rows, cols, depth := sampler.rows, sampler.cols, sampler.depth
	switch plane {
	case MPRPlaneAxial:
		start, end := scalarProjectionRange(center, thickness, depth, mode)
		scalar, allocationErr := newScalarPlane(cols, rows, end-start+1, sampler.photometric, defaultVOI, limits)
		if allocationErr != nil {
			return nil, true, allocationErr
		}
		err := parallelRowsContext(ctx, rows, func(y int) {
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(z int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(x, y, value, ok)
			}
		})
		if err != nil {
			return nil, true, err
		}
		return scalar, true, nil
	case MPRPlaneCoronal:
		start, end := scalarProjectionRange(center, thickness, rows, mode)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.ColSpacing)
		scalar, allocationErr := newScalarPlane(cols, height, end-start+1, sampler.photometric, defaultVOI, limits)
		if allocationErr != nil {
			return nil, true, allocationErr
		}
		err := parallelRowsContext(ctx, height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(y int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(x, outZ, value, ok)
			}
		})
		if err != nil {
			return nil, true, err
		}
		return scalar, true, nil
	case MPRPlaneSagittal:
		start, end := scalarProjectionRange(center, thickness, cols, mode)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.RowSpacing)
		scalar, allocationErr := newScalarPlane(rows, height, end-start+1, sampler.photometric, defaultVOI, limits)
		if allocationErr != nil {
			return nil, true, allocationErr
		}
		err := parallelRowsContext(ctx, height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			for y := 0; y < rows; y++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(x int) (float64, bool) {
					return sampler.valueAt(x, y, z)
				})
				scalar.set(y, outZ, value, ok)
			}
		})
		if err != nil {
			return nil, true, err
		}
		return scalar, true, nil
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
	limits MPRLimits,
) (*ScalarPlane, error) {
	photometric := firstRenderableSlice(series).Metadata.PhotometricInterpretation
	switch plane {
	case MPRPlaneAxial:
		start, end := scalarProjectionRange(center, thickness, len(series.Frames), mode)
		scalar, err := newScalarPlane(cols, rows, end-start+1, photometric, defaultVOI, limits)
		if err != nil {
			return nil, err
		}
		err = parallelRowsContext(ctx, rows, func(y int) {
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(z int) (float64, bool) {
					value, _, ok := sampleVoxel(series.Frames[z], x, y)
					return value, ok
				})
				scalar.set(x, y, value, ok)
			}
		})
		if err != nil {
			return nil, err
		}
		return scalar, nil
	case MPRPlaneCoronal:
		start, end := scalarProjectionRange(center, thickness, rows, mode)
		height := orthogonalMPRHeight(series, seriesColumnSpacing(series))
		scalar, err := newScalarPlane(cols, height, end-start+1, photometric, defaultVOI, limits)
		if err != nil {
			return nil, err
		}
		err = parallelRowsContext(ctx, height, func(outZ int) {
			frame := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for x := 0; x < cols; x++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(y int) (float64, bool) {
					value, _, ok := sampleVoxel(frame, x, y)
					return value, ok
				})
				scalar.set(x, outZ, value, ok)
			}
		})
		if err != nil {
			return nil, err
		}
		return scalar, nil
	case MPRPlaneSagittal:
		start, end := scalarProjectionRange(center, thickness, cols, mode)
		height := orthogonalMPRHeight(series, seriesRowSpacing(series))
		scalar, err := newScalarPlane(rows, height, end-start+1, photometric, defaultVOI, limits)
		if err != nil {
			return nil, err
		}
		err = parallelRowsContext(ctx, height, func(outZ int) {
			frame := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for y := 0; y < rows; y++ {
				value, ok := reduceCachedScalarContext(ctx, mode, start, end, func(x int) (float64, bool) {
					value, _, ok := sampleVoxel(frame, x, y)
					return value, ok
				})
				scalar.set(y, outZ, value, ok)
			}
		})
		if err != nil {
			return nil, err
		}
		return scalar, nil
	default:
		return nil, fmt.Errorf("render: unsupported slab plane %q", plane)
	}
}

// ResliceObliqueScalarContext renders one arbitrary plane without VOI.
func ResliceObliqueScalarContext(ctx context.Context, vol *Volume, plane Plane, outW, outH int) (*ScalarPlane, error) {
	return ResliceObliqueScalarWithLimitsContext(ctx, vol, plane, outW, outH, MPRLimits{})
}

// ResliceObliqueScalarWithLimitsContext validates resource ceilings before
// retaining float64 modality values and the validity mask.
func ResliceObliqueScalarWithLimitsContext(ctx context.Context, vol *Volume, plane Plane, outW, outH int, limits MPRLimits) (*ScalarPlane, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	limits, err := validateMPRReslice(plane, outW, outH, 1, 9, limits)
	if err != nil {
		return nil, err
	}
	if vol == nil {
		return newScalarPlane(outW, outH, 1, "", defaultScalarVOI(), limits)
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return newScalarPlane(outW, outH, 1, vol.Photometric(), defaultScalarVOI(), limits)
	}
	defer sampler.Close()
	return resliceScalarWithSamplerContext(ctx, sampler, plane, outW, outH, defaultScalarVOI(), limits)
}

// ResliceObliqueSlabScalarContext renders an arbitrary thick slab without VOI.
func ResliceObliqueSlabScalarContext(
	ctx context.Context,
	vol *Volume,
	plane Plane,
	outW, outH, thickness int,
	mode SlabMode,
) (*ScalarPlane, error) {
	return ResliceObliqueSlabScalarWithLimitsContext(ctx, vol, plane, outW, outH, thickness, mode, MPRLimits{})
}

// ResliceObliqueSlabScalarWithLimitsContext is the bounded scalar thick-slab
// variant. Dimension, retained bytes, and sample work are checked up front.
func ResliceObliqueSlabScalarWithLimitsContext(
	ctx context.Context,
	vol *Volume,
	plane Plane,
	outW, outH, thickness int,
	mode SlabMode,
	limits MPRLimits,
) (*ScalarPlane, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if mode < SlabNone || mode > SlabAverage {
		return nil, invalidMPRField("SlabMode")
	}
	slabSamples := 1
	if thickness > 1 && mode != SlabNone {
		slabSamples = thickness
	}
	limits, err := validateMPRReslice(plane, outW, outH, slabSamples, 9, limits)
	if err != nil {
		return nil, err
	}
	if thickness <= 1 || mode == SlabNone {
		return ResliceObliqueScalarWithLimitsContext(ctx, vol, plane, outW, outH, limits)
	}
	if vol == nil {
		return newScalarPlane(outW, outH, slabSamples, "", defaultScalarVOI(), limits)
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return newScalarPlane(outW, outH, slabSamples, vol.Photometric(), defaultScalarVOI(), limits)
	}
	defer sampler.Close()
	half := float64(thickness-1) / 2
	return resliceScalarSlabWithSamplerContext(
		ctx, sampler, plane, plane.U.Cross(plane.V), outW, outH,
		-half, thickness, vol.SliceSpacing, mode, defaultScalarVOI(), limits,
	)
}

func resliceScalarWithSamplerContext(
	ctx context.Context,
	sampler *volumeSampler,
	plane Plane,
	outW, outH int,
	defaultVOI WindowLevel,
	limits MPRLimits,
) (*ScalarPlane, error) {
	scalar, err := newScalarPlane(outW, outH, 1, sampler.photometric, defaultVOI, limits)
	if err != nil {
		return nil, err
	}
	denomW := float64(maxInt(outW-1, 1))
	denomH := float64(maxInt(outH-1, 1))
	err = parallelRowsContext(ctx, outH, func(y int) {
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
	if err != nil {
		return nil, err
	}
	return scalar, nil
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
	limits MPRLimits,
) (*ScalarPlane, error) {
	if step <= 0 {
		step = 1
	}
	normal := slabAxis.Normalize()
	scalar, err := newScalarPlane(outW, outH, sampleCount, sampler.photometric, defaultVOI, limits)
	if err != nil {
		return nil, err
	}
	denomW := float64(maxInt(outW-1, 1))
	denomH := float64(maxInt(outH-1, 1))
	err = parallelRowsContext(ctx, outH, func(y int) {
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
	if err != nil {
		return nil, err
	}
	return scalar, nil
}

func newScalarPlane(width, height, slabSamples int, photometric string, defaultVOI WindowLevel, limits MPRLimits) (*ScalarPlane, error) {
	limits, err := validateMPROutput(width, height, slabSamples, 9, limits)
	if err != nil {
		return nil, err
	}
	count := int64(width) * int64(height)
	return &ScalarPlane{
		width:       width,
		height:      height,
		values:      make([]float64, int(count)),
		valid:       make([]byte, int(count)),
		photometric: photometric,
		defaultVOI:  normalizeWindow(defaultVOI, defaultScalarVOI()),
	}, nil
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
