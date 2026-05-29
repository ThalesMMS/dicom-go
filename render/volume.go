package render

import (
	"errors"
	"fmt"
	"image"
	"math"
)

func RenderVolumeProjection(series *Stack, window WindowLevel) (image.Image, error) {
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

	if volume, volumeErr := series.Volume(); volumeErr == nil {
		sampler, ok := newVolumeSampler(volume)
		if !ok {
			return blankImage(512, 512), fmt.Errorf("render: volume contains no decodable voxels")
		}
		defer sampler.Close()
		rows, cols = sampler.rows, sampler.cols
		img := image.NewGray(image.Rect(0, 0, cols, rows))
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				gray, ok := volumeRayGraySampler(sampler, x, y, mapper)
				if ok {
					img.Pix[img.PixOffset(x, y)] = gray
				}
			}
		}
		return img, nil
	} else if isNonCorrectableGeometry(volumeErr) || errors.Is(volumeErr, ErrVolumeStoreClosed) {
		return blankImage(512, 512), volumeErr
	}

	// Geometry-free legacy stacks remain independently projectable for safe 2D
	// fallback. They cannot satisfy VolumeSnapshot's regular patient affine and
	// therefore do not enter the canonical 3D store.
	voxels, err := prepareVolumeVoxels(series)
	if err != nil {
		return blankImage(512, 512), err
	}
	img := image.NewGray(image.Rect(0, 0, cols, rows))
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			gray, ok := volumeRayGrayVoxels(voxels, x, y, mapper)
			if ok {
				img.Pix[img.PixOffset(x, y)] = gray
			}
		}
	}
	return img, nil
}

func volumeRayGraySampler(sampler *volumeSampler, x, y int, mapper preparedVOI) (uint8, bool) {
	accumulatedColor := 0.0
	accumulatedAlpha := 0.0
	found := false
	for frameIndex := 0; frameIndex < sampler.depth; frameIndex++ {
		value, ok := sampler.valueAt(x, y, frameIndex)
		if !ok {
			continue
		}
		found = true
		gray := float64(sampler.displayGrayMapped(value, mapper)) / 255
		opacity := gray
		accumulatedColor += (1 - accumulatedAlpha) * opacity * gray
		accumulatedAlpha += (1 - accumulatedAlpha) * opacity
		if accumulatedAlpha >= 0.995 {
			break
		}
	}
	if !found {
		return 0, false
	}
	return uint8(math.Round(clampUnit(accumulatedColor) * 255)), true
}

type volumeVoxels struct {
	rows   int
	cols   int
	frames []*decodedFrame
}

func prepareVolumeVoxels(series *Stack) (*volumeVoxels, error) {
	if series == nil {
		return nil, fmt.Errorf("render: no series selected")
	}
	series.volumeMu.Lock()
	if series.volumeVoxels != nil {
		voxels := series.volumeVoxels
		series.volumeMu.Unlock()
		return voxels, nil
	}
	series.volumeMu.Unlock()

	first := firstRenderableSlice(series)
	if first == nil {
		return nil, fmt.Errorf("render: series has no displayable images")
	}
	rows := int(first.Metadata.Rows)
	cols := int(first.Metadata.Columns)
	if rows <= 0 || cols <= 0 {
		return nil, fmt.Errorf("render: invalid slice dimensions")
	}
	voxels := &volumeVoxels{rows: rows, cols: cols}
	for _, slice := range series.Frames {
		if slice == nil || slice.DecodeErr != nil {
			continue
		}
		decoded, err := decodeSliceFrame(slice)
		if err != nil || decoded == nil || len(decoded.grayscale) == 0 {
			continue
		}
		voxels.frames = append(voxels.frames, decoded)
	}
	series.volumeMu.Lock()
	if series.volumeVoxels == nil {
		series.volumeVoxels = voxels
	} else {
		voxels = series.volumeVoxels
	}
	series.volumeMu.Unlock()
	return voxels, nil
}

func (v *volumeVoxels) valueAt(frameIndex, x, y int) (float64, bool) {
	if v == nil || frameIndex < 0 || frameIndex >= len(v.frames) || x < 0 || y < 0 {
		return 0, false
	}
	frame := v.frames[frameIndex]
	if frame == nil || x >= frame.cols || y >= frame.rows {
		return 0, false
	}
	offset := y*frame.cols + x
	if offset < 0 || offset >= len(frame.grayscale) {
		return 0, false
	}
	return float64(frame.grayscale[offset]), true
}

func volumeRayGrayVoxels(voxels *volumeVoxels, x, y int, mapper preparedVOI) (uint8, bool) {
	accumulatedColor := 0.0
	accumulatedAlpha := 0.0
	found := false
	for frameIndex := range voxels.frames {
		value, ok := voxels.valueAt(frameIndex, x, y)
		if !ok {
			continue
		}
		found = true
		gray := float64(displayGrayMapped(value, mapper, voxels.photometricAt(frameIndex))) / 255
		opacity := gray
		accumulatedColor += (1 - accumulatedAlpha) * opacity * gray
		accumulatedAlpha += (1 - accumulatedAlpha) * opacity
		if accumulatedAlpha >= 0.995 {
			break
		}
	}
	if !found {
		return 0, false
	}
	return uint8(math.Round(clampUnit(accumulatedColor) * 255)), true
}

func (v *volumeVoxels) photometricAt(frameIndex int) string {
	if v == nil || frameIndex < 0 || frameIndex >= len(v.frames) {
		return ""
	}
	frame := v.frames[frameIndex]
	if frame == nil {
		return ""
	}
	return frame.photometric
}

func clampUnit(value float64) float64 {
	if value < 0 || math.IsNaN(value) {
		return 0
	}
	if value > 1 || math.IsInf(value, 0) {
		return 1
	}
	return value
}
