package render

import (
	"context"
	"image"
	"math"
)

// Plane is an output reslice plane in patient space (mm): the image origin
// (output pixel 0,0) and the U / V edge vectors spanning the plane's width and
// height. Output pixel (i,j) samples Origin + (i/(w-1))·U + (j/(h-1))·V.
type Plane struct {
	Origin Vec3
	U      Vec3
	V      Vec3
}

// At returns the patient-space point at fractional plane coordinates (s,t) in
// [0,1]², where (0,0)=Origin, (1,0)=Origin+U, (0,1)=Origin+V.
func (p Plane) At(s, t float64) Vec3 {
	return p.Origin.Add(p.U.Scale(s)).Add(p.V.Scale(t))
}

// PixelSpacingMM is the effective in-plane mm-per-pixel of a reslice rendered at
// outW×outH: |U|/(outW-1) horizontally and |V|/(outH-1) vertically. For an
// orthogonal OrthogonalPlane this reduces to the remapped axis spacings; for an
// oblique plane it is the true projected spacing along U/V
// (mpr-toolbar-plan.md §5.6).
func (p Plane) PixelSpacingMM(outW, outH int) MeasureSpacing {
	dw := float64(outW - 1)
	dh := float64(outH - 1)
	if dw <= 0 {
		dw = 1
	}
	if dh <= 0 {
		dh = 1
	}
	return MeasureSpacing{X: p.U.Length() / dw, Y: p.V.Length() / dh}
}

// Photometric returns the photometric interpretation of the volume's slices
// (assumed uniform across the series).
func (v *Volume) Photometric() string {
	if v == nil || len(v.slices) == 0 || v.slices[0] == nil {
		return ""
	}
	return v.slices[0].Metadata.PhotometricInterpretation
}

// TrilinearAt samples the volume at fractional voxel coordinates by trilinear
// interpolation of the eight neighbours. Out-of-bounds neighbours contribute 0.
// ok is false only when the sample point is fully outside the volume (so callers
// can paint the background).
func (v *Volume) TrilinearAt(p Vec3) (float64, bool) {
	reader, err := v.AcquireReader()
	if err != nil {
		return 0, false
	}
	defer reader.Close()
	return reader.TrilinearAt(p)
}

// ResliceOblique renders an arbitrary-angle plane through the volume by
// trilinear sampling (mpr-toolbar-plan.md §5.2), producing an outW by outH
// grayscale image. Out-of-bounds samples stay black. RenderMPRPlane remains the
// fast path for axis-aligned planes.
func ResliceOblique(vol *Volume, plane Plane, outW, outH int, window WindowLevel) image.Image {
	img, _ := ResliceObliqueContext(context.Background(), vol, plane, outW, outH, window)
	return img
}

// ResliceObliqueContext is the cancellable form of ResliceOblique. It stops
// between output rows and returns the partially rendered image with ctx.Err().
func ResliceObliqueContext(ctx context.Context, vol *Volume, plane Plane, outW, outH int, window WindowLevel) (image.Image, error) {
	if vol == nil || outW <= 0 || outH <= 0 {
		return blankImage(512, 512), nil
	}
	window = normalizeWindow(window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return blankImage(outW, outH), nil
	}
	defer sampler.Close()
	return resliceObliqueWithSamplerContext(ctx, vol, sampler, plane, outW, outH, mapper)
}

func resliceObliqueWithSampler(vol *Volume, sampler *volumeSampler, plane Plane, outW, outH int, mapper preparedVOI) image.Image {
	img, _ := resliceObliqueWithSamplerContext(context.Background(), vol, sampler, plane, outW, outH, mapper)
	return img
}

func resliceObliqueWithSamplerContext(ctx context.Context, vol *Volume, sampler *volumeSampler, plane Plane, outW, outH int, mapper preparedVOI) (image.Image, error) {
	img := image.NewGray(image.Rect(0, 0, outW, outH))

	denomW := float64(outW - 1)
	if denomW <= 0 {
		denomW = 1
	}
	denomH := float64(outH - 1)
	if denomH <= 0 {
		denomH = 1
	}
	err := parallelRowsContext(ctx, outH, func(j int) {
		t := float64(j) / denomH
		rowBase := plane.Origin.Add(plane.V.Scale(t))
		rowOffset := img.PixOffset(0, j)
		for i := 0; i < outW; i++ {
			s := float64(i) / denomW
			p := rowBase.Add(plane.U.Scale(s))
			voxel := sampler.vol.PatientToVoxel(p)
			if val, ok := sampler.trilinearAt(voxel); ok {
				img.Pix[rowOffset+i] = sampler.displayGrayMapped(val, mapper)
			}
			// Out-of-bounds samples leave the background (0 / black).
		}
	})
	return img, err
}

// correctedOrthogonalPlane returns a rectilinear patient-space plane for a
// tilted coronal or sagittal reconstruction. Its V axis follows the reference
// slice normal rather than the sheared first-to-last origin vector; the inverse
// volume transform supplies the per-slice lateral correction.
func (v *Volume) correctedOrthogonalPlane(plane MPRPlane, index int) (Plane, int, int, bool) {
	if v == nil || !v.tilt.Applied || v.Depth < 2 {
		return Plane{}, 0, 0, false
	}
	normalSpan := v.indexToPosition(float64(v.Depth - 1))
	if normalSpan <= 0 || math.IsNaN(normalSpan) || math.IsInf(normalSpan, 0) {
		return Plane{}, 0, 0, false
	}
	top := v.Origin.Add(v.Normal.Scale(normalSpan))
	switch plane {
	case MPRPlaneCoronal:
		index = clampIndex(index, v.Rows)
		origin := top.Add(v.AxisY.Scale(float64(index) * v.RowSpacing))
		return Plane{
			Origin: origin,
			U:      v.AxisX.Scale(float64(v.Cols-1) * v.ColSpacing),
			V:      v.Normal.Scale(-normalSpan),
		}, v.Cols, correctedOrthogonalHeight(normalSpan, v.ColSpacing), true
	case MPRPlaneSagittal:
		index = clampIndex(index, v.Cols)
		origin := top.Add(v.AxisX.Scale(float64(index) * v.ColSpacing))
		return Plane{
			Origin: origin,
			U:      v.AxisY.Scale(float64(v.Rows-1) * v.RowSpacing),
			V:      v.Normal.Scale(-normalSpan),
		}, v.Rows, correctedOrthogonalHeight(normalSpan, v.RowSpacing), true
	default:
		return Plane{}, 0, 0, false
	}
}

// correctedSlabAxis returns the signed patient-space sampling axis, spacing,
// and source sample count for corrected coronal and sagittal slabs.
func (v *Volume) correctedSlabAxis(plane MPRPlane, rows, cols int) (axis Vec3, step float64, count int) {
	switch plane {
	case MPRPlaneCoronal:
		return v.AxisY, v.RowSpacing, rows
	case MPRPlaneSagittal:
		return v.AxisX, v.ColSpacing, cols
	default:
		return Vec3{}, 0, 0
	}
}

func correctedOrthogonalHeight(normalSpan, inPlaneSpacing float64) int {
	if normalSpan <= 0 || inPlaneSpacing <= 0 ||
		math.IsNaN(normalSpan) || math.IsInf(normalSpan, 0) ||
		math.IsNaN(inPlaneSpacing) || math.IsInf(inPlaneSpacing, 0) {
		return 1
	}
	height := int(math.Round(normalSpan/inPlaneSpacing)) + 1
	if height < 1 {
		return 1
	}
	return height
}

func clampIndex(index, count int) int {
	if count <= 0 || index < 0 {
		return 0
	}
	if index >= count {
		return count - 1
	}
	return index
}

// ResliceObliqueSlab renders a thick-slab projection of an oblique plane
// (mpr-toolbar-plan.md §5.5): for each pixel it samples `thickness` steps along
// the plane normal (spaced by the volume's through-plane spacing) and reduces
// ResliceObliqueSlab renders a thick-slab projection through an oblique plane by sampling multiple points along the plane normal and combining them using the specified reduction mode.
func ResliceObliqueSlab(vol *Volume, plane Plane, outW, outH, thickness int, mode SlabMode, window WindowLevel) image.Image {
	img, _ := ResliceObliqueSlabContext(context.Background(), vol, plane, outW, outH, thickness, mode, window)
	return img
}

// ResliceObliqueSlabContext is the cancellable form of ResliceObliqueSlab.
func ResliceObliqueSlabContext(ctx context.Context, vol *Volume, plane Plane, outW, outH, thickness int, mode SlabMode, window WindowLevel) (image.Image, error) {
	if vol == nil || outW <= 0 || outH <= 0 {
		return blankImage(512, 512), nil
	}
	if thickness <= 1 || mode == SlabNone {
		return ResliceObliqueContext(ctx, vol, plane, outW, outH, window)
	}
	window = normalizeWindow(window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return blankImage(outW, outH), nil
	}
	defer sampler.Close()
	half := float64(thickness-1) / 2
	slabAxis := plane.U.Cross(plane.V)
	return resliceObliqueSlabRangeWithSamplerContext(ctx, vol, sampler, plane, slabAxis, outW, outH, -half, thickness, vol.SliceSpacing, mode, mapper)
}

func resliceObliqueSlabRangeWithSampler(vol *Volume, sampler *volumeSampler, plane Plane, slabAxis Vec3, outW, outH int, firstOffset float64, sampleCount int, step float64, mode SlabMode, mapper preparedVOI) image.Image {
	img, _ := resliceObliqueSlabRangeWithSamplerContext(context.Background(), vol, sampler, plane, slabAxis, outW, outH, firstOffset, sampleCount, step, mode, mapper)
	return img
}

func resliceObliqueSlabRangeWithSamplerContext(ctx context.Context, vol *Volume, sampler *volumeSampler, plane Plane, slabAxis Vec3, outW, outH int, firstOffset float64, sampleCount int, step float64, mode SlabMode, mapper preparedVOI) (image.Image, error) {
	normal := slabAxis.Normalize()
	if step <= 0 || math.IsNaN(step) || math.IsInf(step, 0) {
		step = 1
	}

	img := image.NewGray(image.Rect(0, 0, outW, outH))
	denomW := float64(outW - 1)
	if denomW <= 0 {
		denomW = 1
	}
	denomH := float64(outH - 1)
	if denomH <= 0 {
		denomH = 1
	}
	err := parallelRowsContext(ctx, outH, func(j int) {
		t := float64(j) / denomH
		rowOffset := img.PixOffset(0, j)
		for i := 0; i < outW; i++ {
			if ctx != nil && ctx.Err() != nil {
				return
			}
			s := float64(i) / denomW
			base := plane.At(s, t)
			value, _, ok := reduceSlab(mode, 0, sampleCount-1, func(k int) (float64, string, bool) {
				off := (firstOffset + float64(k)) * step
				p := base.Add(normal.Scale(off))
				v, ok := sampler.trilinearAt(sampler.vol.PatientToVoxel(p))
				return v, sampler.photometric, ok
			})
			if ok {
				img.Pix[rowOffset+i] = sampler.displayGrayMapped(value, mapper)
			}
		}
	})
	return img, err
}

// OrthogonalPlane builds the reslice Plane for one of the three orthogonal views
// spanning the whole volume, with the crosshair centre fixing the through-plane
// position. It is a convenience for callers that want ResliceOblique to render an
// axis-aligned view.
func (v *Volume) OrthogonalPlane(plane MPRPlane, center Vec3) Plane {
	if v == nil {
		return Plane{}
	}
	cv := v.PatientToVoxel(center)
	lastSlice := v.Depth - 1
	if lastSlice < 0 {
		lastSlice = 0
	}
	switch plane {
	case MPRPlaneSagittal:
		// Plane spans rows (Y) × slices (Z) at column cv.X.
		origin := v.VoxelToPatient(Vec3{X: cv.X, Y: 0, Z: float64(lastSlice)})
		return Plane{
			Origin: origin,
			U:      v.AxisY.Scale(float64(v.Rows-1) * v.RowSpacing),
			V:      v.VoxelToPatient(Vec3{X: cv.X, Y: 0, Z: 0}).Sub(origin),
		}
	case MPRPlaneCoronal:
		// Plane spans columns (X) × slices (Z) at row cv.Y.
		origin := v.VoxelToPatient(Vec3{X: 0, Y: cv.Y, Z: float64(lastSlice)})
		return Plane{
			Origin: origin,
			U:      v.AxisX.Scale(float64(v.Cols-1) * v.ColSpacing),
			V:      v.VoxelToPatient(Vec3{X: 0, Y: cv.Y, Z: 0}).Sub(origin),
		}
	default: // axial
		// Plane spans columns (X) × rows (Y) at slice cv.Z.
		origin := v.VoxelToPatient(Vec3{X: 0, Y: 0, Z: cv.Z})
		return Plane{
			Origin: origin,
			U:      v.AxisX.Scale(float64(v.Cols-1) * v.ColSpacing),
			V:      v.AxisY.Scale(float64(v.Rows-1) * v.RowSpacing),
		}
	}
}
