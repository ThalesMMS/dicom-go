package render

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

type MPRPlane string

const (
	MPRPlaneAxial    MPRPlane = "axial"
	MPRPlaneCoronal  MPRPlane = "coronal"
	MPRPlaneSagittal MPRPlane = "sagittal"
)

// GantryTiltRenderMode selects whether MPR views apply the patient-space
// correction for a stable gantry-tilted stack or expose the acquired source
// grid for explicit inspection. Corrected is the safe/default clinical view.
type GantryTiltRenderMode int

const (
	GantryTiltCorrected GantryTiltRenderMode = iota
	GantryTiltSourceGeometry
)

// MPRRenderOptions controls geometry choices shared by single-plane and
// projection MPR rendering. The zero value applies gantry-tilt correction.
type MPRRenderOptions struct {
	GantryTiltMode GantryTiltRenderMode
}

func DefaultMPRRenderOptions() MPRRenderOptions {
	return MPRRenderOptions{GantryTiltMode: GantryTiltCorrected}
}

func RenderMPRPlane(series *Stack, plane MPRPlane, index int, window WindowLevel) (image.Image, error) {
	return RenderMPRPlaneWithOptions(series, plane, index, window, DefaultMPRRenderOptions())
}

// RenderMPRPlaneWithOptions renders one orthogonal MPR plane while allowing an
// explicit source-grid inspection of stable gantry-tilted acquisitions.
func RenderMPRPlaneWithOptions(series *Stack, plane MPRPlane, index int, window WindowLevel, options MPRRenderOptions) (image.Image, error) {
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
	if plane == MPRPlaneCoronal || plane == MPRPlaneSagittal {
		correctTilt := options.GantryTiltMode != GantryTiltSourceGeometry
		if vol, sampler, err := stackVolumeSampler(series, correctTilt); err == nil {
			img, rendered := renderCachedMPRPlane(vol, sampler, plane, index, mapper, correctTilt)
			sampler.Close()
			if rendered {
				return img, nil
			}
		} else if isNonCorrectableGeometry(err) {
			return blankImage(512, 512), err
		}
	}
	switch plane {
	case MPRPlaneAxial:
		if index < 0 {
			index = 0
		}
		if index >= len(series.Frames) {
			index = len(series.Frames) - 1
		}
		return RenderFrame(series.Frames[index], window)
	case MPRPlaneCoronal:
		if index < 0 {
			index = 0
		}
		if index >= rows {
			index = rows - 1
		}
		height := orthogonalMPRHeight(series, seriesColumnSpacing(series))
		img := image.NewGray(image.Rect(0, 0, cols, height))
		for outZ := 0; outZ < height; outZ++ {
			slice := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for x := 0; x < cols; x++ {
				img.SetGray(x, outZ, color.Gray{Y: mprVoxelGray(slice, x, index, mapper)})
			}
		}
		return img, nil
	case MPRPlaneSagittal:
		if index < 0 {
			index = 0
		}
		if index >= cols {
			index = cols - 1
		}
		height := orthogonalMPRHeight(series, seriesRowSpacing(series))
		img := image.NewGray(image.Rect(0, 0, rows, height))
		for outZ := 0; outZ < height; outZ++ {
			slice := series.Frames[orthogonalDisplaySliceIndex(outZ, height, len(series.Frames))]
			for y := 0; y < rows; y++ {
				img.SetGray(y, outZ, color.Gray{Y: mprVoxelGray(slice, index, y, mapper)})
			}
		}
		return img, nil
	default:
		return blankImage(512, 512), fmt.Errorf("render: unsupported MPR plane %q", plane)
	}
}

func stackVolumeSampler(series *Stack, regularize bool) (*Volume, *volumeSampler, error) {
	vol, err := series.Volume()
	if err != nil {
		return nil, nil, err
	}
	var sampler *volumeSampler
	var ok bool
	if regularize {
		sampler, ok = newVolumeSampler(vol)
	} else {
		sampler, ok = newDecodedVolumeSampler(vol)
	}
	if !ok {
		return vol, nil, fmt.Errorf("render: volume contains no decodable voxels")
	}
	return vol, sampler, nil
}

func renderCachedMPRPlane(vol *Volume, sampler *volumeSampler, plane MPRPlane, index int, mapper preparedVOI, correctGantryTilt bool) (image.Image, bool) {
	if vol == nil || sampler == nil {
		return nil, false
	}
	rows, cols, depth := sampler.rows, sampler.cols, sampler.depth
	if rows <= 0 || cols <= 0 || depth <= 0 {
		return nil, false
	}
	if correctGantryTilt {
		if corrected, width, height, ok := vol.correctedOrthogonalPlane(plane, index); ok {
			return resliceObliqueWithSampler(vol, sampler, corrected, width, height, mapper), true
		}
	}
	switch plane {
	case MPRPlaneCoronal:
		if index < 0 {
			index = 0
		}
		if index >= rows {
			index = rows - 1
		}
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.ColSpacing)
		img := image.NewGray(image.Rect(0, 0, cols, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for x := 0; x < cols; x++ {
				if value, ok := sampler.valueAt(x, index, z); ok {
					img.Pix[offset+x] = sampler.displayGrayMapped(value, mapper)
				}
			}
		})
		return img, true
	case MPRPlaneSagittal:
		if index < 0 {
			index = 0
		}
		if index >= cols {
			index = cols - 1
		}
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.RowSpacing)
		img := image.NewGray(image.Rect(0, 0, rows, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for y := 0; y < rows; y++ {
				if value, ok := sampler.valueAt(index, y, z); ok {
					img.Pix[offset+y] = sampler.displayGrayMapped(value, mapper)
				}
			}
		})
		return img, true
	default:
		return nil, false
	}
}

func firstRenderableSlice(series *Stack) *Frame {
	if series == nil {
		return nil
	}
	for _, slice := range series.Frames {
		if slice != nil && slice.DecodeErr == nil && slice.Metadata.Rows > 0 && slice.Metadata.Columns > 0 {
			return slice
		}
	}
	return nil
}

func mprVoxelGray(slice *Frame, x, y int, mapper preparedVOI) uint8 {
	value, ok := PixelValueAt(slice, x, y)
	if !ok {
		return 0
	}
	return displayGrayMapped(value.Rescaled, mapper, slice.Metadata.PhotometricInterpretation)
}

func orthogonalMPRHeight(series *Stack, inPlaneAxisSpacing float64) int {
	if series == nil || len(series.Frames) == 0 {
		return 0
	}
	// Resample the through-plane axis by the actual inter-slice SPACING, not
	// SliceThickness — the slab thickness can differ from the slice-to-slice
	// distance (overlapping or gapped acquisitions), which would squash or stretch
	// the coronal/sagittal reformat. This matches BuildVolume.SliceSpacing.
	sliceSpacing := seriesSliceSpacing(series)
	if sliceSpacing <= 0 || inPlaneAxisSpacing <= 0 || math.IsNaN(sliceSpacing) || math.IsNaN(inPlaneAxisSpacing) || math.IsInf(sliceSpacing, 0) || math.IsInf(inPlaneAxisSpacing, 0) {
		return len(series.Frames)
	}
	height := int(math.Round(float64(len(series.Frames)) * sliceSpacing / inPlaneAxisSpacing))
	if height < 1 {
		return 1
	}
	return height
}

func orthogonalVolumeHeight(depth int, sliceSpacing, inPlaneAxisSpacing float64) int {
	if depth <= 0 {
		return 0
	}
	if sliceSpacing <= 0 || inPlaneAxisSpacing <= 0 || math.IsNaN(sliceSpacing) || math.IsNaN(inPlaneAxisSpacing) || math.IsInf(sliceSpacing, 0) || math.IsInf(inPlaneAxisSpacing, 0) {
		return depth
	}
	height := int(math.Round(float64(depth) * sliceSpacing / inPlaneAxisSpacing))
	if height < 1 {
		return 1
	}
	return height
}

// seriesSliceSpacing is the mean through-plane spacing between slices in mm,
// derived from their spatial positions — NOT SliceThickness, which is the slab
// thickness and may differ from the inter-slice distance. Falls back to
// SliceThickness, then 1 mm, when positions are unavailable. This mirrors
// BuildVolume.SliceSpacing so the orthogonal reformats and the volume-based
// reslice agree.
func seriesSliceSpacing(series *Stack) float64 {
	if series == nil {
		return 1
	}
	lo, hi := math.Inf(1), math.Inf(-1)
	count := 0
	for _, sl := range series.Frames {
		if sl == nil {
			continue
		}
		v := spatialSortValue(sl)
		if v < lo {
			lo = v
		}
		if v > hi {
			hi = v
		}
		count++
	}
	if count > 1 && hi > lo {
		if sp := (hi - lo) / float64(count-1); sp > 0 && !math.IsNaN(sp) && !math.IsInf(sp, 0) {
			return sp
		}
	}
	if thickness := seriesSliceThickness(series); thickness > 0 {
		return thickness
	}
	return 1
}

func seriesRowSpacing(series *Stack) float64 {
	if series != nil && len(series.PixelSpacing) >= 1 && finitePositiveSpacing(series.PixelSpacing[0]) {
		return series.PixelSpacing[0]
	}
	if series != nil {
		for _, frame := range series.Frames {
			if frame != nil && len(frame.PixelSpacing) >= 1 && finitePositiveSpacing(frame.PixelSpacing[0]) {
				return frame.PixelSpacing[0]
			}
		}
	}
	return 0
}

func seriesColumnSpacing(series *Stack) float64 {
	if series != nil && len(series.PixelSpacing) >= 2 && finitePositiveSpacing(series.PixelSpacing[1]) {
		return series.PixelSpacing[1]
	}
	if series != nil {
		for _, frame := range series.Frames {
			if frame != nil && len(frame.PixelSpacing) >= 2 && finitePositiveSpacing(frame.PixelSpacing[1]) {
				return frame.PixelSpacing[1]
			}
		}
	}
	return 0
}

func seriesSliceThickness(series *Stack) float64 {
	if series == nil {
		return 0
	}
	if finitePositiveSpacing(series.SliceThickness) {
		return series.SliceThickness
	}
	for _, frame := range series.Frames {
		if frame != nil && finitePositiveSpacing(frame.SliceThickness) {
			return frame.SliceThickness
		}
	}
	return 0
}

func finitePositiveSpacing(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func resampledSliceIndex(outZ, outputHeight, sliceCount int) int {
	if sliceCount <= 1 || outputHeight <= 1 {
		return 0
	}
	index := int(math.Floor((float64(outZ) + 0.5) * float64(sliceCount) / float64(outputHeight)))
	if index < 0 {
		return 0
	}
	if index >= sliceCount {
		return sliceCount - 1
	}
	return index
}

func orthogonalDisplaySliceIndex(outZ, outputHeight, sliceCount int) int {
	return resampledSliceIndex(outputHeight-1-outZ, outputHeight, sliceCount)
}
