package render

import (
	"context"
	"image"
	"image/color"
	"math"
	"sync"
)

// RenderVR renders a 3D volume to a 2D image using the specified camera,
// transfer function, rendering mode, and clipping parameters. Non-positive
// quality dimensions default to 256×256. Shading is optional. A nil or empty
// volume returns an empty image.
func RenderVR(vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, shading bool, clip *VRClip, quality VRQuality) image.Image {
	img, _ := RenderVRContext(context.Background(), vol, cam, preset, window, shading, clip, quality)
	return img
}

// RenderVRContext is the cancellable form of RenderVR. Cancellation is checked
// between rows, pixels, and ray samples; the returned image may be partial.
func RenderVRContext(ctx context.Context, vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, shading bool, clip *VRClip, quality VRQuality) (image.Image, error) {
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
	boxMin := size.Scale(-0.5)
	boxMax := size.Scale(0.5)

	dataMin, dataMax, ok := vol.huRangeFromSampler(sampler)
	if !ok {
		dataMin, dataMax = 0, 1
	}
	lut := preset.TF.BakeLUT(dataMin, dataMax, 1024)

	window = normalizeWindow(window, vol.RecommendedWindow())

	steps := quality.MaxSteps
	if steps < 8 {
		steps = 8
	}
	diag := size.Length()
	stepLen := diag / float64(steps)
	rayGen := newVRRayGenerator(cam, w, h)

	// Render rows in parallel; each goroutine writes disjoint scanlines, so the
	// shared image needs no synchronization.
	renderRow := func(py int, gradientCell *volumeGradientCell) {
		rowBase := rayGen.rowBase(py)
		for px := 0; px < w; px++ {
			if ctx.Err() != nil {
				return
			}
			o, d := rayGen.ray(px, rowBase)
			t0, t1, hit := intersectAABB(o, d, boxMin, boxMax)
			if preset.Mode != VRModeDVR {
				v := 0.0
				if hit {
					v, _ = marchProjectionContext(ctx, sampler, o, d, t0, t1, stepLen, boxMin, size, window, preset.Mode, clip)
				}
				if preset.Inverse {
					v = 1 - v
				}
				gy := to8(v)
				img.SetNRGBA(px, py, color.NRGBA{R: gy, G: gy, B: gy, A: 255})
				continue
			}
			if !hit {
				continue
			}
			r, g, b, a := marchDVRContext(ctx, sampler, gradientCell, lut, o, d, t0, t1, stepLen, boxMin, size, window, dataMin, dataMax, preset, shading, clip)
			if a <= 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{R: to8(linearToSRGB(r)), G: to8(linearToSRGB(g)), B: to8(linearToSRGB(b)), A: 255})
		}
	}

	var cells sync.Pool
	cells.New = func() any { return new(volumeGradientCell) }
	err = parallelRowsContext(ctx, h, func(py int) {
		cell := cells.Get().(*volumeGradientCell)
		renderRow(py, cell)
		cells.Put(cell)
	})
	return img, err
}

// RenderVRPreview renders a cheaper interaction frame. It preserves projection
// mode, transfer function, window and clipping, but disables shading and gradient
// opacity so drag/orbit does not pay the gradient path.
func RenderVRPreview(vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, clip *VRClip, quality VRQuality) image.Image {
	img, _ := RenderVRPreviewContext(context.Background(), vol, cam, preset, window, clip, quality)
	return img
}

// RenderVRPreviewContext is the cancellable form of RenderVRPreview.
func RenderVRPreviewContext(ctx context.Context, vol *Volume, cam VRCamera, preset VRPreset, window WindowLevel, clip *VRClip, quality VRQuality) (image.Image, error) {
	preset.GradientOpacityScale = 0
	return RenderVRContext(ctx, vol, cam, preset, window, false, clip, quality)
}

type vrRayGenerator struct {
	origin     Vec3
	right      Vec3
	up         Vec3
	forward    Vec3
	projection VRCameraProjection
	distance   float64
	aspectTan  float64
	tanHalfFov float64
	invWidth   float64
	invHeight  float64
}

type vrRayRow struct {
	originBase Vec3
	dirBase    Vec3
}

func newVRRayGenerator(cam VRCamera, w, h int) vrRayGenerator {
	right, up, forward := cam.basis()
	aspect := 1.0
	if h > 0 {
		aspect = float64(w) / float64(h)
	}
	invW, invH := 1.0, 1.0
	if w > 0 {
		invW = 1 / float64(w)
	}
	if h > 0 {
		invH = 1 / float64(h)
	}
	tanHalfFov := math.Tan(cam.FovY / 2)
	projection := cam.Projection.normalized()
	origin := cam.Position()
	if projection == VRProjectionEndoscopy {
		origin = cam.Target
	}
	return vrRayGenerator{
		origin:     origin,
		right:      right,
		up:         up,
		forward:    forward,
		projection: projection,
		distance:   cam.Distance,
		aspectTan:  aspect * tanHalfFov,
		tanHalfFov: tanHalfFov,
		invWidth:   invW,
		invHeight:  invH,
	}
}

func (g vrRayGenerator) rowBase(py int) vrRayRow {
	ndcy := (1 - 2*(float64(py)+0.5)*g.invHeight) * g.tanHalfFov
	if g.projection == VRProjectionParallel {
		return vrRayRow{
			originBase: g.origin.Add(g.up.Scale(ndcy * g.distance)),
			dirBase:    g.forward,
		}
	}
	return vrRayRow{
		originBase: g.origin,
		dirBase:    g.forward.Add(g.up.Scale(ndcy)),
	}
}

func (g vrRayGenerator) ray(px int, rowBase vrRayRow) (origin, dir Vec3) {
	ndcx := (2*(float64(px)+0.5)*g.invWidth - 1) * g.aspectTan
	if g.projection == VRProjectionParallel {
		return rowBase.originBase.Add(g.right.Scale(ndcx * g.distance)), g.forward
	}
	return rowBase.originBase, rowBase.dirBase.Add(g.right.Scale(ndcx)).Normalize()
}

// marchProjection accumulates a MIP / MinIP / Average of the windowed density
// marchProjection accumulates projection values along a ray segment by sampling the volume at regular intervals.
// It applies windowing and clipping to samples and accumulates them according to the specified mode: minimum intensity projection (MinIP), maximum intensity projection (MIP), or average density.
// The boolean return indicates whether any valid samples were encountered; the float64 is the accumulated projection value (or zero if no samples were valid).
func marchProjection(sampler *volumeSampler, o, d Vec3, t0, t1, stepLen float64, boxMin, size Vec3, window WindowLevel, mode VRMode, clip *VRClip) (float64, bool) {
	return marchProjectionContext(context.Background(), sampler, o, d, t0, t1, stepLen, boxMin, size, window, mode, clip)
}

func marchProjectionContext(ctx context.Context, sampler *volumeSampler, o, d Vec3, t0, t1, stepLen float64, boxMin, size Vec3, window WindowLevel, mode VRMode, clip *VRClip) (float64, bool) {
	var acc float64
	count := 0
	sampleIndex := 0
	switch mode {
	case VRModeMinIP:
		acc = 1
	}
	for t := t0; t <= t1; t += stepLen {
		if sampleIndex%vrCancellationCheckStride == 0 && ctx != nil && ctx.Err() != nil {
			return 0, false
		}
		sampleIndex++
		pw := o.Add(d.Scale(t))
		tex := Vec3{
			X: (pw.X - boxMin.X) / size.X,
			Y: (pw.Y - boxMin.Y) / size.Y,
			Z: (pw.Z - boxMin.Z) / size.Z,
		}
		if !clip.keep(tex) {
			continue
		}
		hu, ok := sampler.textureAt(tex)
		if !ok {
			continue
		}
		dv := windowedUnit(hu, window)
		switch mode {
		case VRModeMinIP:
			if count == 0 || dv < acc {
				acc = dv
			}
		case VRModeAverage:
			acc += dv
		default: // VRModeMIP
			if count == 0 || dv > acc {
				acc = dv
			}
		}
		count++
	}
	if count == 0 {
		return 0, false
	}
	if mode == VRModeAverage {
		return acc / float64(count), true
	}
	return acc, true
}

// marchDVR composites samples front-to-back along [t0,t1].
func marchDVR(sampler *volumeSampler, gradientCell *volumeGradientCell, lut VRLUT, o, d Vec3, t0, t1, stepLen float64, boxMin, size Vec3, window WindowLevel, dataMin, dataMax float64, preset VRPreset, shading bool, clip *VRClip) (accR, accG, accB, accA float64) {
	return marchDVRContext(context.Background(), sampler, gradientCell, lut, o, d, t0, t1, stepLen, boxMin, size, window, dataMin, dataMax, preset, shading, clip)
}

func marchDVRContext(ctx context.Context, sampler *volumeSampler, gradientCell *volumeGradientCell, lut VRLUT, o, d Vec3, t0, t1, stepLen float64, boxMin, size Vec3, window WindowLevel, dataMin, dataMax float64, preset VRPreset, shading bool, clip *VRClip) (accR, accG, accB, accA float64) {
	needGradient := shading || preset.GradientOpacityScale > 0
	// The cached, interpolated stencil is a measured win for gradient-opacity
	// presets. Shading-only presets retain the exact central-difference path,
	// which is cheaper for their sparse opaque samples.
	useGradientCell := gradientCell != nil && preset.GradientOpacityScale > 0
	sampleIndex := 0
	for t := t0; t <= t1; t += stepLen {
		if sampleIndex%vrCancellationCheckStride == 0 && ctx != nil && ctx.Err() != nil {
			return
		}
		sampleIndex++
		pw := o.Add(d.Scale(t))
		tex := Vec3{
			X: (pw.X - boxMin.X) / size.X,
			Y: (pw.Y - boxMin.Y) / size.Y,
			Z: (pw.Z - boxMin.Z) / size.Z,
		}
		if !clip.keep(tex) {
			continue
		}
		var hu float64
		var textureSample volumeTextureSample
		var ok bool
		if useGradientCell {
			textureSample, ok = sampler.textureSample(tex)
			hu = textureSample.value()
		} else {
			hu, ok = sampler.textureAt(tex)
		}
		if !ok {
			continue
		}
		densityWindow := windowedUnit(hu, window)
		densityData := clampUnit((hu - dataMin) / (dataMax - dataMin))
		rgba := lut.Lookup(densityData)
		alpha := rgba.A * densityWindow
		if alpha <= 0 {
			continue
		}
		cr, cg, cb := rgba.R, rgba.G, rgba.B
		// The gradient drives both opacity modulation and shading, so compute it
		// once per opaque sample and reuse it.
		if needGradient {
			var grad Vec3
			if useGradientCell {
				grad = gradientCell.gradient(sampler, textureSample)
			} else {
				grad = sampler.gradientAt(tex)
			}
			if preset.GradientOpacityScale > 0 {
				alpha *= clampUnit(grad.Length() / preset.GradientOpacityScale)
				if alpha <= 0 {
					continue
				}
			}
			if shading {
				s := sampler.vol.shadeWithGradient(grad, d)
				cr, cg, cb = cr*s, cg*s, cb*s
			}
		}
		inv := 1 - accA
		accR += inv * alpha * cr
		accG += inv * alpha * cg
		accB += inv * alpha * cb
		accA += inv * alpha
		if accA > 0.95 {
			break
		}
	}
	return accR, accG, accB, accA
}

const vrCancellationCheckStride = 32

// intersectAABB returns the [t0,t1] entry/exit parameters of the ray with the
// axis-aligned box (slab method), or hit=false when it misses.
func intersectAABB(o, d, bmin, bmax Vec3) (t0, t1 float64, hit bool) {
	t0, t1 = math.Inf(-1), math.Inf(1)
	axis := func(oa, da, mn, mx float64) bool {
		if math.Abs(da) < 1e-12 {
			return oa >= mn && oa <= mx
		}
		inv := 1 / da
		ta := (mn - oa) * inv
		tb := (mx - oa) * inv
		if ta > tb {
			ta, tb = tb, ta
		}
		if ta > t0 {
			t0 = ta
		}
		if tb < t1 {
			t1 = tb
		}
		return t0 <= t1
	}
	if !axis(o.X, d.X, bmin.X, bmax.X) || !axis(o.Y, d.Y, bmin.Y, bmax.Y) || !axis(o.Z, d.Z, bmin.Z, bmax.Z) {
		return 0, 0, false
	}
	if t1 < 0 {
		return 0, 0, false
	}
	if t0 < 0 {
		t0 = 0
	}
	return t0, t1, true
}

// AdjustVRWindow applies a smooth windowing drag (3d-toolbar-plan.md §5.8): dx
// widens the window, dy raises the level. The window multiplies sample opacity
// (densityWindow) in the march; it does not rescale the TF color domain.
func AdjustVRWindow(wl WindowLevel, dx, dy float64) WindowLevel {
	wl.Center -= dy
	wl.Width += dx * 4
	if wl.Width < 1 {
		wl.Width = 1
	}
	return wl
}

func to8(v float64) uint8 {
	if v <= 0 {
		return 0
	}
	if v >= 1 {
		return 255
	}
	return uint8(v*255 + 0.5)
}
