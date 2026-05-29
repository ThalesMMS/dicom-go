package render

import (
	"fmt"
	"image"
	"math"
)

// SlabOpacityMode selects the scalar-opacity curve used by MPR volume slabs.
// The names mirror the four tables exposed by OsiriX's 3D MPR toolbar.
type SlabOpacityMode int

const (
	SlabOpacityLinear SlabOpacityMode = iota
	SlabOpacityLogarithmicInverse
	SlabOpacityLogarithmic
	SlabOpacitySmooth
)

// VolumeSlabOptions controls front-to-back compositing and optional
// Blinn-Phong shading for a thick MPR slab.
type VolumeSlabOptions struct {
	Opacity       SlabOpacityMode
	Shading       bool
	Ambient       float64
	Diffuse       float64
	Specular      float64
	SpecularPower float64
	// GantryTiltMode defaults to corrected patient-space rendering. The source
	// mode is an explicit inspection view of the acquired sheared grid.
	GantryTiltMode GantryTiltRenderMode
}

// DefaultVolumeSlabOptions matches the default values shown by OsiriX's MPR
// Shading editor.
func DefaultVolumeSlabOptions() VolumeSlabOptions {
	return VolumeSlabOptions{
		Opacity:       SlabOpacityLinear,
		Ambient:       0.15,
		Diffuse:       0.90,
		Specular:      0.30,
		SpecularPower: 15,
	}
}

// RenderVolumeSlab renders an orthogonal thick slab through front-to-back
// scalar-opacity compositing. Unlike MIP/MinIP/Mean, every sample can
// contribute to the result, and optional shading uses the volume gradient at
// the contributing sample.
func RenderVolumeSlab(series *Stack, plane MPRPlane, center, thickness int, window WindowLevel, options VolumeSlabOptions) (image.Image, error) {
	if series == nil || len(series.Frames) == 0 {
		return blankImage(512, 512), fmt.Errorf("render: no series selected")
	}
	correctTilt := options.GantryTiltMode != GantryTiltSourceGeometry
	vol, sampler, err := stackVolumeSampler(series, correctTilt)
	if err != nil {
		return blankImage(512, 512), fmt.Errorf("render: series has no displayable volume: %w", err)
	}
	defer sampler.Close()
	window = normalizeWindow(window, series.DefaultWindow)
	mapper := prepareWindow(window)
	options = normalizeVolumeSlabOptions(options)

	rows, cols, depth := sampler.rows, sampler.cols, sampler.depth
	if correctTilt {
		if corrected, width, height, ok := vol.correctedOrthogonalPlane(plane, center); ok {
			slabAxis, step, sampleCount := vol.correctedSlabAxis(plane, vol.Rows, vol.Cols)
			viewDir := Vec3{Y: 1}
			if plane == MPRPlaneSagittal {
				viewDir = Vec3{X: 1}
			}
			clampedCenter := clampIndex(center, sampleCount)
			start, end := mipProjectionRange(clampedCenter, thickness, sampleCount)
			return renderCorrectedVolumeSlab(
				vol, sampler, mapper, options, corrected, slabAxis, viewDir,
				width, height, clampedCenter, start, end, step,
			), nil
		}
	}
	switch plane {
	case MPRPlaneAxial:
		start, end := mipProjectionRange(center, thickness, depth)
		img := image.NewGray(image.Rect(0, 0, cols, rows))
		parallelRows(rows, func(y int) {
			offset := img.PixOffset(0, y)
			for x := 0; x < cols; x++ {
				img.Pix[offset+x] = compositeVolumeSlabRay(sampler, mapper, options, start, end, Vec3{Z: 1}, func(sample int) (int, int, int) {
					return x, y, sample
				})
			}
		})
		return img, nil
	case MPRPlaneCoronal:
		start, end := mipProjectionRange(center, thickness, rows)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.ColSpacing)
		img := image.NewGray(image.Rect(0, 0, cols, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for x := 0; x < cols; x++ {
				img.Pix[offset+x] = compositeVolumeSlabRay(sampler, mapper, options, start, end, Vec3{Y: 1}, func(sample int) (int, int, int) {
					return x, sample, z
				})
			}
		})
		return img, nil
	case MPRPlaneSagittal:
		start, end := mipProjectionRange(center, thickness, cols)
		height := orthogonalVolumeHeight(depth, vol.SliceSpacing, vol.RowSpacing)
		img := image.NewGray(image.Rect(0, 0, rows, height))
		parallelRows(height, func(outZ int) {
			z := orthogonalDisplaySliceIndex(outZ, height, depth)
			offset := img.PixOffset(0, outZ)
			for y := 0; y < rows; y++ {
				img.Pix[offset+y] = compositeVolumeSlabRay(sampler, mapper, options, start, end, Vec3{X: 1}, func(sample int) (int, int, int) {
					return sample, y, z
				})
			}
		})
		return img, nil
	default:
		return blankImage(512, 512), fmt.Errorf("render: unsupported slab plane %q", plane)
	}
}

func renderCorrectedVolumeSlab(vol *Volume, sampler *volumeSampler, mapper preparedVOI, options VolumeSlabOptions, plane Plane, slabAxis, viewDir Vec3, width, height, center, start, end int, step float64) image.Image {
	img := image.NewGray(image.Rect(0, 0, width, height))
	slabAxis = slabAxis.Normalize()
	denomW := float64(width - 1)
	if denomW <= 0 {
		denomW = 1
	}
	denomH := float64(height - 1)
	if denomH <= 0 {
		denomH = 1
	}
	parallelRows(height, func(j int) {
		t := float64(j) / denomH
		rowBase := plane.Origin.Add(plane.V.Scale(t))
		rowOffset := img.PixOffset(0, j)
		for i := 0; i < width; i++ {
			s := float64(i) / denomW
			base := rowBase.Add(plane.U.Scale(s))
			img.Pix[rowOffset+i] = compositeCorrectedVolumeSlabRay(
				vol, sampler, mapper, options, base, slabAxis, viewDir,
				center, start, end, step,
			)
		}
	})
	return img
}

func compositeCorrectedVolumeSlabRay(vol *Volume, sampler *volumeSampler, mapper preparedVOI, options VolumeSlabOptions, base, slabAxis, viewDir Vec3, center, start, end int, step float64) uint8 {
	return compositeVolumeSlabSamples(sampler, mapper, options, start, end, viewDir, func(sample int, needTexture bool) (float64, Vec3, bool) {
		offset := float64(sample-center) * step
		voxel := sampler.vol.PatientToVoxel(base.Add(slabAxis.Scale(offset)))
		value, ok := sampler.trilinearAt(voxel)
		if !ok {
			return 0, Vec3{}, false
		}
		var texture Vec3
		if needTexture {
			texture = Vec3{
				X: normalizedVoxelPosition(voxel.X, sampler.cols),
				Y: normalizedVoxelPosition(voxel.Y, sampler.rows),
				Z: normalizedVoxelPosition(voxel.Z, sampler.depth),
			}
		}
		return value, texture, true
	})
}

func compositeVolumeSlabRay(sampler *volumeSampler, mapper preparedVOI, options VolumeSlabOptions, start, end int, viewDir Vec3, coordinate func(int) (int, int, int)) uint8 {
	return compositeVolumeSlabSamples(sampler, mapper, options, start, end, viewDir, func(sample int, needTexture bool) (float64, Vec3, bool) {
		x, y, z := coordinate(sample)
		value, ok := sampler.valueAt(x, y, z)
		if !ok {
			return 0, Vec3{}, false
		}
		var texture Vec3
		if needTexture {
			texture = Vec3{
				X: normalizedVoxelCoordinate(x, sampler.cols),
				Y: normalizedVoxelCoordinate(y, sampler.rows),
				Z: normalizedVoxelCoordinate(z, sampler.depth),
			}
		}
		return value, texture, true
	})
}

type volumeSlabSampleProvider func(sample int, needTexture bool) (value float64, texture Vec3, ok bool)

func compositeVolumeSlabSamples(sampler *volumeSampler, mapper preparedVOI, options VolumeSlabOptions, start, end int, viewDir Vec3, sampleAt volumeSlabSampleProvider) uint8 {
	accColor, accAlpha := 0.0, 0.0
	stride := 1
	if count := end - start + 1; count > 64 {
		stride = (count + 63) / 64
	}
	for sample := start; sample <= end && accAlpha < 0.995; sample += stride {
		value, texture, ok := sampleAt(sample, options.Shading)
		if !ok {
			continue
		}
		intensity := float64(sampler.displayGrayMapped(value, mapper)) / 255
		alpha := volumeSlabOpacity(options.Opacity, intensity)
		if alpha <= 0 {
			continue
		}
		// Correct the tabulated opacity for sample density so a thicker slab
		// does not become completely opaque after only a few low-alpha voxels.
		alpha = 1 - math.Pow(1-alpha, 0.25*float64(stride))
		shade := 1.0
		if options.Shading {
			shade = volumeSlabShade(sampler.gradientAt(texture), viewDir, options)
		}
		weight := (1 - accAlpha) * alpha
		accColor += weight * intensity * shade
		accAlpha += weight
	}
	return uint8(math.Round(255 * clamp01(accColor)))
}

func normalizedVoxelCoordinate(index, count int) float64 {
	return normalizedVoxelPosition(float64(index), count)
}

func normalizedVoxelPosition(position float64, count int) float64 {
	if count <= 1 {
		return 0
	}
	return clamp01(position / float64(count-1))
}

func volumeSlabOpacity(mode SlabOpacityMode, value float64) float64 {
	value = clamp01(value)
	switch mode {
	case SlabOpacityLogarithmicInverse:
		return 1 - math.Log1p(9*(1-value))/math.Log(10)
	case SlabOpacityLogarithmic:
		return math.Log1p(9*value) / math.Log(10)
	case SlabOpacitySmooth:
		return value * value * (3 - 2*value)
	default:
		return value
	}
}

func volumeSlabShade(gradient, viewDir Vec3, options VolumeSlabOptions) float64 {
	length := gradient.Length()
	if length < 1e-6 {
		return options.Ambient
	}
	normal := gradient.Scale(-1 / length)
	eye := viewDir.Scale(-1).Normalize()
	diffuse := math.Max(normal.Dot(eye), 0)
	specular := math.Pow(math.Max(normal.Dot(eye), 0), options.SpecularPower)
	return clamp01(options.Ambient + options.Diffuse*diffuse + options.Specular*specular)
}

func normalizeVolumeSlabOptions(options VolumeSlabOptions) VolumeSlabOptions {
	if options.Opacity < SlabOpacityLinear || options.Opacity > SlabOpacitySmooth {
		options.Opacity = SlabOpacityLinear
	}
	options.Ambient = clamp01(options.Ambient)
	options.Diffuse = clamp01(options.Diffuse)
	options.Specular = clamp01(options.Specular)
	if math.IsNaN(options.SpecularPower) || math.IsInf(options.SpecularPower, 0) || options.SpecularPower < 1 {
		options.SpecularPower = 1
	}
	if options.SpecularPower > 128 {
		options.SpecularPower = 128
	}
	return options
}

func clamp01(value float64) float64 {
	if math.IsNaN(value) || value < 0 {
		return 0
	}
	if value > 1 || math.IsInf(value, 1) {
		return 1
	}
	return value
}
