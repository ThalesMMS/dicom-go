package render

import (
	"context"
	"image"
	"image/color"
)

// RenderVRSurface renders the first iso-value crossing along each camera ray.
// It is the interactive counterpart of ISOSurfaceTriangles: the STL path keeps
// exact voxel faces for export, while this path samples the scalar field for a
// responsive clinical preview that follows the VR camera, crop, and clip state.
func RenderVRSurface(vol *Volume, cam VRCamera, iso float64, surfaceColor color.NRGBA, shading bool, clip *VRClip, quality VRQuality) image.Image {
	img, _ := RenderVRSurfaceContext(context.Background(), vol, cam, iso, surfaceColor, shading, clip, quality)
	return img
}

// RenderVRSurfaceContext is the cancellable form of RenderVRSurface.
func RenderVRSurfaceContext(ctx context.Context, vol *Volume, cam VRCamera, iso float64, surfaceColor color.NRGBA, shading bool, clip *VRClip, quality VRQuality) (image.Image, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	w, h := quality.Width, quality.Height
	if w <= 0 || h <= 0 {
		w, h = 256, 256
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	if vol == nil {
		return img, nil
	}
	if err := ctx.Err(); err != nil {
		return img, err
	}
	var err error
	vol, err = vol.RegularGrid()
	if err != nil {
		return img, err
	}
	if err := ctx.Err(); err != nil {
		return img, err
	}
	size := vol.PhysicalSize()
	if size.X <= 0 || size.Y <= 0 || size.Z <= 0 {
		return img, nil
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return img, nil
	}
	defer sampler.Close()
	if surfaceColor.A == 0 {
		surfaceColor.A = 255
	}
	boxMin := size.Scale(-0.5)
	boxMax := size.Scale(0.5)
	steps := quality.MaxSteps
	if steps < 8 {
		steps = 8
	}
	stepLen := size.Length() / float64(steps)
	rayGen := newVRRayGenerator(cam, w, h)

	renderRow := func(py int) {
		rowBase := rayGen.rowBase(py)
		for px := 0; px < w; px++ {
			if ctx.Err() != nil {
				return
			}
			o, d := rayGen.ray(px, rowBase)
			t0, t1, hit := intersectAABB(o, d, boxMin, boxMax)
			if !hit {
				continue
			}
			tex, ok := firstVRSurfaceHitContext(ctx, sampler, o, d, t0, t1, stepLen, boxMin, size, iso, clip)
			if !ok {
				continue
			}
			light := 1.0
			if shading {
				light = vol.shadeWithGradient(sampler.gradientAt(tex), d)
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: to8(float64(surfaceColor.R) / 255 * light),
				G: to8(float64(surfaceColor.G) / 255 * light),
				B: to8(float64(surfaceColor.B) / 255 * light),
				A: surfaceColor.A,
			})
		}
	}
	err = parallelRowsContext(ctx, h, renderRow)
	return img, err
}

func firstVRSurfaceHitContext(ctx context.Context, sampler *volumeSampler, origin, direction Vec3, t0, t1, stepLen float64, boxMin, size Vec3, iso float64, clip *VRClip) (Vec3, bool) {
	if sampler == nil || stepLen <= 0 {
		return Vec3{}, false
	}
	sampleIndex := 0
	for t := t0; t <= t1; t += stepLen {
		if sampleIndex%vrCancellationCheckStride == 0 && ctx.Err() != nil {
			return Vec3{}, false
		}
		sampleIndex++
		point := origin.Add(direction.Scale(t))
		tex := Vec3{
			X: (point.X - boxMin.X) / size.X,
			Y: (point.Y - boxMin.Y) / size.Y,
			Z: (point.Z - boxMin.Z) / size.Z,
		}
		if clip != nil && !clip.keep(tex) {
			continue
		}
		value, ok := sampler.textureAt(tex)
		if ok && value >= iso {
			return tex, true
		}
	}
	return Vec3{}, false
}
