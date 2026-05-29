package render

import (
	"fmt"
	"image"
	"image/color"
)

// SlabMode selects how a thick slab is projected onto the plane
// (mpr-toolbar-plan.md §5.5).
type SlabMode int

const (
	SlabNone    SlabMode = iota // single slice (thickness 1) = plain MPR
	SlabMIP                     // maximum intensity projection
	SlabMinIP                   // minimum intensity projection
	SlabAverage                 // mean of the in-bounds samples
)

func RenderMIP(series *Stack, window WindowLevel) (image.Image, error) {
	return RenderMIPPlane(series, MPRPlaneAxial, window)
}

func RenderMIPPlane(series *Stack, plane MPRPlane, window WindowLevel) (image.Image, error) {
	return RenderMIPSlab(series, plane, 0, 0, window)
}

// RenderMIPSlab is the max-only convenience wrapper retained for callers; it
// delegates to RenderSlab with SlabMIP.
func RenderMIPSlab(series *Stack, plane MPRPlane, center, thickness int, window WindowLevel) (image.Image, error) {
	return RenderSlab(series, plane, center, thickness, SlabMIP, window)
}

// RenderSlab renders a thick-slab projection of the plane with the given mode.
// SlabNone is a single slice (plain MPR); SlabMIP/MinIP/Average reduce the slab
// samples that straddle the centre symmetrically (mipProjectionRange).
func RenderSlab(series *Stack, plane MPRPlane, center, thickness int, mode SlabMode, window WindowLevel) (image.Image, error) {
	return RenderSlabWithOptions(series, plane, center, thickness, mode, window, DefaultMPRRenderOptions())
}

// RenderSlabWithOptions is RenderSlab with explicit gantry-tilt geometry
// selection for corrected viewing or source-grid inspection.
func RenderSlabWithOptions(series *Stack, plane MPRPlane, center, thickness int, mode SlabMode, window WindowLevel, options MPRRenderOptions) (image.Image, error) {
	if mode == SlabNone {
		return RenderMPRPlaneWithOptions(series, plane, center, window, options)
	}
	if series == nil || len(series.Frames) == 0 {
		return blankImage(512, 512), fmt.Errorf("render: no series selected")
	}
	first := firstRenderableSlice(series)
	if first == nil {
		return blankImage(512, 512), fmt.Errorf("render: series has no displayable images")
	}
	rows := int(first.Metadata.Rows)
	cols := int(first.Metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return blankImage(512, 512), fmt.Errorf("render: invalid slice dimensions")
	}
	window = normalizeWindow(window, series.DefaultWindow)
	mapper := prepareWindow(window)
	correctTilt := options.GantryTiltMode != GantryTiltSourceGeometry
	if vol, sampler, err := stackVolumeSampler(series, correctTilt); err == nil {
		img, rendered := renderCachedSlab(vol, sampler, plane, center, thickness, mode, mapper, correctTilt)
		sampler.Close()
		if rendered {
			return img, nil
		}
	} else if isNonCorrectableGeometry(err) {
		return blankImage(512, 512), err
	}

	switch plane {
	case MPRPlaneAxial:
		start, end := mipProjectionRange(center, thickness, len(series.Frames))
		img := image.NewGray(image.Rect(0, 0, cols, rows))
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				value, photometric, ok := reduceSlab(mode, start, end, func(z int) (float64, string, bool) {
					return sampleVoxel(series.Frames[z], x, y)
				})
				if ok {
					img.SetGray(x, y, color.Gray{Y: displayGrayMapped(value, mapper, photometric)})
				}
			}
		}
		return img, nil
	case MPRPlaneCoronal:
		start, end := mipProjectionRange(center, thickness, rows)
		height := orthogonalMPRHeight(series, seriesColumnSpacing(series))
		img := image.NewGray(image.Rect(0, 0, cols, height))
		for outZ := 0; outZ < height; outZ++ {
			slice := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for x := 0; x < cols; x++ {
				value, photometric, ok := reduceSlab(mode, start, end, func(y int) (float64, string, bool) {
					return sampleVoxel(slice, x, y)
				})
				if ok {
					img.SetGray(x, outZ, color.Gray{Y: displayGrayMapped(value, mapper, photometric)})
				}
			}
		}
		return img, nil
	case MPRPlaneSagittal:
		start, end := mipProjectionRange(center, thickness, cols)
		height := orthogonalMPRHeight(series, seriesRowSpacing(series))
		img := image.NewGray(image.Rect(0, 0, rows, height))
		for outZ := 0; outZ < height; outZ++ {
			slice := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for y := 0; y < rows; y++ {
				value, photometric, ok := reduceSlab(mode, start, end, func(x int) (float64, string, bool) {
					return sampleVoxel(slice, x, y)
				})
				if ok {
					img.SetGray(y, outZ, color.Gray{Y: displayGrayMapped(value, mapper, photometric)})
				}
			}
		}
		return img, nil
	default:
		return blankImage(512, 512), fmt.Errorf("render: unsupported slab plane %q", plane)
	}
}

func renderCachedSlab(vol *Volume, sampler *volumeSampler, plane MPRPlane, center, thickness int, mode SlabMode, mapper preparedVOI, correctGantryTilt bool) (image.Image, bool) {
	if vol == nil || sampler == nil {
		return nil, false
	}
	rows, cols, depth := sampler.rows, sampler.cols, sampler.depth
	if rows <= 0 || cols <= 0 || depth <= 0 {
		return nil, false
	}
	if correctGantryTilt {
		if corrected, width, height, ok := vol.correctedOrthogonalPlane(plane, center); ok {
			slabAxis, step, sampleCount := vol.correctedSlabAxis(plane, vol.Rows, vol.Cols)
			clampedCenter := clampIndex(center, sampleCount)
			start, end := mipProjectionRange(clampedCenter, thickness, sampleCount)
			return resliceObliqueSlabRangeWithSampler(
				vol, sampler, corrected, slabAxis, width, height,
				float64(start-clampedCenter), end-start+1, step, mode, mapper,
			), true
		}
	}
	switch plane {
	case MPRPlaneAxial:
		start, end := mipProjectionRange(center, thickness, depth)
		img := image.NewGray(image.Rect(0, 0, cols, rows))
		parallelRows(rows, func(y int) {
			offset := img.PixOffset(0, y)
			for x := 0; x < cols; x++ {
				if value, ok := reduceCachedAxialSlab(sampler, mode, start, end, x, y); ok {
					img.Pix[offset+x] = sampler.displayGrayMapped(value, mapper)
				}
			}
		})
		return img, true
	case MPRPlaneCoronal:
		start, end := mipProjectionRange(center, thickness, rows)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.ColSpacing)
		img := image.NewGray(image.Rect(0, 0, cols, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for x := 0; x < cols; x++ {
				if value, ok := reduceCachedCoronalSlab(sampler, mode, start, end, x, z); ok {
					img.Pix[offset+x] = sampler.displayGrayMapped(value, mapper)
				}
			}
		})
		return img, true
	case MPRPlaneSagittal:
		start, end := mipProjectionRange(center, thickness, cols)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.RowSpacing)
		img := image.NewGray(image.Rect(0, 0, rows, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for y := 0; y < rows; y++ {
				if value, ok := reduceCachedSagittalSlab(sampler, mode, start, end, y, z); ok {
					img.Pix[offset+y] = sampler.displayGrayMapped(value, mapper)
				}
			}
		})
		return img, true
	default:
		return nil, false
	}
}

func reduceCachedAxialSlab(sampler *volumeSampler, mode SlabMode, start, end, x, y int) (float64, bool) {
	var acc float64
	count := 0
	for z := start; z <= end; z++ {
		value, ok := sampler.valueAt(x, y, z)
		if !ok {
			continue
		}
		acc, count = accumulateSlabValue(mode, acc, count, value)
	}
	return finishSlabValue(mode, acc, count)
}

func reduceCachedCoronalSlab(sampler *volumeSampler, mode SlabMode, start, end, x, z int) (float64, bool) {
	var acc float64
	count := 0
	for y := start; y <= end; y++ {
		value, ok := sampler.valueAt(x, y, z)
		if !ok {
			continue
		}
		acc, count = accumulateSlabValue(mode, acc, count, value)
	}
	return finishSlabValue(mode, acc, count)
}

func reduceCachedSagittalSlab(sampler *volumeSampler, mode SlabMode, start, end, y, z int) (float64, bool) {
	var acc float64
	count := 0
	for x := start; x <= end; x++ {
		value, ok := sampler.valueAt(x, y, z)
		if !ok {
			continue
		}
		acc, count = accumulateSlabValue(mode, acc, count, value)
	}
	return finishSlabValue(mode, acc, count)
}

func accumulateSlabValue(mode SlabMode, acc float64, count int, value float64) (float64, int) {
	switch mode {
	case SlabMinIP:
		if count == 0 || value < acc {
			acc = value
		}
	case SlabAverage:
		acc += value
	default:
		if count == 0 || value > acc {
			acc = value
		}
	}
	return acc, count + 1
}

func finishSlabValue(mode SlabMode, acc float64, count int) (float64, bool) {
	if count == 0 {
		return 0, false
	}
	if mode == SlabAverage {
		return acc / float64(count), true
	}
	return acc, true
}

func mipProjectionRange(center, thickness, count int) (int, int) {
	if count <= 1 {
		return 0, 0
	}
	if thickness <= 0 || thickness > count {
		thickness = count
	}
	if center < 0 {
		center = 0
	}
	if center >= count {
		center = count - 1
	}
	before := (thickness - 1) / 2
	after := thickness - 1 - before
	start := center - before
	end := center + after
	if start < 0 {
		end -= start
		start = 0
	}
	if end >= count {
		start -= end - count + 1
		end = count - 1
	}
	if start < 0 {
		start = 0
	}
	return start, end
}

// sampleVoxel returns the rescaled value and photometric at (x,y) on a slice.
func sampleVoxel(slice *Frame, x, y int) (float64, string, bool) {
	value, ok := PixelValueAt(slice, x, y)
	if !ok {
		return 0, "", false
	}
	return value.Rescaled, slice.Metadata.PhotometricInterpretation, true
}

// reduceSlab reduces samples i∈[start,end] under the given mode, skipping
// out-of-bounds samples. It returns the projected value, the last seen
// photometric, and whether any sample contributed.
func reduceSlab(mode SlabMode, start, end int, sample func(i int) (float64, string, bool)) (float64, string, bool) {
	var acc float64
	var photometric string
	count := 0
	for i := start; i <= end; i++ {
		v, ph, ok := sample(i)
		if !ok {
			continue
		}
		switch mode {
		case SlabMinIP:
			if count == 0 || v < acc {
				acc = v
			}
		case SlabAverage:
			acc += v
		default: // SlabMIP
			if count == 0 || v > acc {
				acc = v
			}
		}
		photometric = ph
		count++
	}
	if count == 0 {
		return 0, "", false
	}
	if mode == SlabAverage {
		return acc / float64(count), photometric, true
	}
	return acc, photometric, true
}

func displayGrayMapped(value float64, mapper preparedVOI, photometric string) uint8 {
	gray := windowedGrayMapped(value, mapper)
	if normalizedPhotometric(photometric) == "MONOCHROME1" {
		return 255 - gray
	}
	return gray
}
