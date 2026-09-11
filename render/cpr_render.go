package render

import (
	"context"
	"errors"
	"image"
	"math"
	"sync/atomic"
)

var (
	// ErrCPRSuperseded reports that a newer render generation began before this
	// render finished, so its result is discarded before reaching UI state.
	ErrCPRSuperseded = errors.New("render: CPR render superseded by a newer generation")
)

// CPRMode selects which curved-planar-reformation view to render.
type CPRMode int

const (
	// CPRStraightened unrolls the centerline into a straight horizontal strip; the
	// cross-section follows the rotation-minimizing frame normal.
	CPRStraightened CPRMode = iota
	// CPRStretched keeps the cross-section aligned with a fixed reference
	// direction (re-projected per tangent) rather than the rotating frame.
	CPRStretched
	// CPRTransverse renders a single cross-sectional slice perpendicular to the
	// centerline at a chosen arc length.
	CPRTransverse
	// CPRSlab is a straightened CPR whose pixels are slab projections through the
	// binormal direction.
	CPRSlab
)

// CPRRequest parameterizes a CPR render.
type CPRRequest struct {
	Mode   CPRMode
	Volume *Volume
	Path   *CPRPath
	Window WindowLevel

	// Width is the cross-section width in pixels (and the side length for the
	// transverse view).
	Width int
	// ArcSpacing is the along-path spacing (mm) per output column for the
	// longitudinal views; defaults to the volume's in-plane spacing.
	ArcSpacing float64
	// CrossSpacing is mm per cross-section pixel; defaults to the volume's
	// in-plane spacing.
	CrossSpacing float64

	// ArcLength selects the cross-section position for the transverse view.
	ArcLength float64

	// ThicknessMM and SlabMode control the slab projection in patient-space
	// millimetres. Thickness is retained for source compatibility with older
	// callers and is interpreted as a sample count only when ThicknessMM is not
	// positive.
	ThicknessMM float64
	Thickness   int
	SlabMode    SlabMode

	// StretchDir is the fixed cross-section reference for the stretched view; a
	// zero value uses the first frame's normal.
	StretchDir Vec3
	// RotationDegrees rotates the sampling basis around the centerline tangent.
	// It is independent of the straightened/stretched reformation type.
	RotationDegrees float64

	// Generation, when non-zero, ties the render to a CPRRenderer generation so
	// stale renders are discarded.
	Generation uint64
}

func (r CPRRequest) width() int {
	if r.Width > 0 {
		return r.Width
	}
	return 64
}

func (r CPRRequest) arcSpacing() float64 {
	if r.ArcSpacing > 0 {
		return r.ArcSpacing
	}
	if r.Volume != nil && r.Volume.RowSpacing > 0 {
		return r.Volume.RowSpacing
	}
	return 1
}

func (r CPRRequest) crossSpacing() float64 {
	if r.CrossSpacing > 0 {
		return r.CrossSpacing
	}
	if r.Volume != nil && r.Volume.ColSpacing > 0 {
		return r.Volume.ColSpacing
	}
	return 1
}

// RenderCPR renders the requested CPR view deterministically from the volume and
// path. It honors ctx cancellation between output rows, returning ctx.Err().
func RenderCPR(ctx context.Context, req CPRRequest) (image.Image, error) {
	if req.Volume == nil || req.Path == nil {
		return nil, ErrCPRInput
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateCPRRequest(req); err != nil {
		return nil, err
	}
	switch req.Mode {
	case CPRTransverse:
		return renderTransverseCPR(ctx, req)
	case CPRStretched:
		return renderLongitudinalCPR(ctx, req, true)
	case CPRSlab:
		// CPRSlab remains a compatibility alias. New callers select a
		// reformation type and configure its projection independently.
		req.Mode = CPRStraightened
		return renderLongitudinalCPR(ctx, req, false)
	default:
		return renderLongitudinalCPR(ctx, req, false)
	}
}

func validateCPRRequest(req CPRRequest) error {
	if req.Width < 0 {
		return &CPRInputError{Field: "width", Reason: "width cannot be negative"}
	}
	if req.width() > MaxCPROutputDimension {
		return &CPRLimitError{Resource: "output_dimension", Value: uint64(req.width()), Limit: MaxCPROutputDimension}
	}
	for _, field := range []struct {
		name  string
		value float64
	}{
		{"arc_spacing", req.ArcSpacing},
		{"cross_spacing", req.CrossSpacing},
		{"thickness_mm", req.ThicknessMM},
	} {
		if field.value < 0 || math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return &CPRInputError{Field: field.name, Reason: "value must be zero or positive and finite"}
		}
	}
	for _, field := range []struct {
		name  string
		value float64
	}{
		{"arc_length", req.ArcLength},
		{"rotation_degrees", req.RotationDegrees},
		{"window_center", req.Window.Center},
		{"window_width", req.Window.Width},
	} {
		if math.IsNaN(field.value) || math.IsInf(field.value, 0) {
			return &CPRInputError{Field: field.name, Reason: "value is not finite"}
		}
	}
	if req.StretchDir != (Vec3{}) && !finiteVec3(req.StretchDir) {
		return &CPRInputError{Field: "stretch_direction", Reason: "coordinate is not finite"}
	}
	if req.Thickness < 0 {
		return &CPRInputError{Field: "thickness", Reason: "sample count cannot be negative"}
	}
	if req.Thickness > MaxCPRSlabSamples {
		return &CPRLimitError{Resource: "slab_samples", Value: uint64(req.Thickness), Limit: MaxCPRSlabSamples}
	}
	if req.SlabMode < SlabNone || req.SlabMode > SlabAverage {
		return &CPRInputError{Field: "slab_mode", Reason: "mode is not supported"}
	}
	if req.Mode < CPRStraightened || req.Mode > CPRSlab {
		return &CPRInputError{Field: "mode", Reason: "mode is not supported"}
	}
	return nil
}

func validateCPROutput(width, height, slabSamples int) error {
	if width <= 0 || height <= 0 {
		return &CPRInputError{Field: "output_dimensions", Reason: "dimensions must be positive"}
	}
	if width > MaxCPROutputDimension || height > MaxCPROutputDimension {
		value := width
		if height > value {
			value = height
		}
		return &CPRLimitError{Resource: "output_dimension", Value: uint64(value), Limit: MaxCPROutputDimension}
	}
	pixels, err := checkedCPRProduct("output_pixels", MaxCPROutputPixels, uint64(width), uint64(height))
	if err != nil {
		return err
	}
	if slabSamples < 1 {
		slabSamples = 1
	}
	_, err = checkedCPRProduct("sample_operations", MaxCPRSampleOperations, pixels, uint64(slabSamples))
	return err
}

// renderLongitudinalCPR renders straightened (stretched=false) or stretched
// (stretched=true) CPR: columns run along the centerline arc length, rows across
// the cross-section. This orientation matches curved-MPR workstations where
// transverse section markers are vertical lines on the longitudinal output.
func renderLongitudinalCPR(ctx context.Context, req CPRRequest, stretched bool) (image.Image, error) {
	vol := req.Volume
	sampleCount, err := req.Path.SampleCountChecked(req.arcSpacing())
	if err != nil {
		return nil, err
	}
	width := req.width()
	cross := req.crossSpacing()
	slabSamples, err := cprSlabSampleCountChecked(req)
	if err != nil {
		return nil, err
	}
	if err := validateCPROutput(sampleCount, width, slabSamples); err != nil {
		return nil, err
	}
	samples, err := req.Path.ResampleChecked(req.arcSpacing())
	if err != nil {
		return nil, err
	}
	slabOffsets, err := cprSlabSampleOffsetsChecked(req, cross)
	if err != nil {
		return nil, err
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return nil, ErrCPRInput
	}
	defer sampler.Close()
	half := float64(width-1) / 2
	photometric := vol.Photometric()
	window := normalizeWindow(req.Window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)

	ref := req.StretchDir
	if stretched && ref == (Vec3{}) {
		ref = samples[0].Normal
	}

	img := image.NewGray(image.Rect(0, 0, len(samples), width))
	err = parallelRowsContext(ctx, width, func(r int) {
		rowOffset := img.PixOffset(0, r)
		offset := (float64(r) - half) * cross
		for c, s := range samples {
			crossDir := s.Normal
			if stretched {
				crossDir = ref.Sub(s.Tangent.Scale(ref.Dot(s.Tangent))).Normalize()
				if crossDir == (Vec3{}) {
					crossDir = s.Normal
				}
			}
			crossDir, slabDir := rotateCPRBasis(crossDir, s.Tangent, req.RotationDegrees)
			base := s.Position.Add(crossDir.Scale(offset))
			if len(slabOffsets) > 0 {
				value, ph, ok := reduceSlab(req.SlabMode, 0, len(slabOffsets)-1, func(k int) (float64, string, bool) {
					p := base.Add(slabDir.Scale(slabOffsets[k]))
					v, sampleOK := sampler.trilinearAt(sampler.vol.PatientToVoxel(p))
					return v, photometric, sampleOK
				})
				if ok {
					img.Pix[rowOffset+c] = displayGrayMapped(value, mapper, ph)
				}
				continue
			}
			if val, ok := sampler.trilinearAt(sampler.vol.PatientToVoxel(base)); ok {
				img.Pix[rowOffset+c] = displayGrayMapped(val, mapper, photometric)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return img, nil
}

// renderSlabCPR renders a straightened CPR whose pixels are slab projections
// (MIP/MinIP/Average) through the binormal direction, matching the MPR slab
// reduction semantics. Thickness ≤ 1 or SlabNone falls back to straightened CPR.
func renderSlabCPR(ctx context.Context, req CPRRequest) (image.Image, error) {
	req.Mode = CPRStraightened
	return renderLongitudinalCPR(ctx, req, false)
}

// renderTransverseCPR renders one cross-sectional slice perpendicular to the
// centerline at req.ArcLength, spanned by the frame's normal and binormal.
func renderTransverseCPR(ctx context.Context, req CPRRequest) (image.Image, error) {
	vol := req.Volume
	frame := req.Path.FrameAt(req.ArcLength)
	normal, binormal := rotateCPRBasis(frame.Normal, frame.Tangent, req.RotationDegrees)
	size := req.width()
	cross := req.crossSpacing()
	slabSamples, err := cprSlabSampleCountChecked(req)
	if err != nil {
		return nil, err
	}
	if err := validateCPROutput(size, size, slabSamples); err != nil {
		return nil, err
	}
	slabOffsets, err := cprSlabSampleOffsetsChecked(req, cross)
	if err != nil {
		return nil, err
	}
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return nil, ErrCPRInput
	}
	defer sampler.Close()
	half := float64(size-1) / 2
	photometric := vol.Photometric()
	window := normalizeWindow(req.Window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)

	img := image.NewGray(image.Rect(0, 0, size, size))
	err = parallelRowsContext(ctx, size, func(j int) {
		voff := (float64(j) - half) * cross
		rowOffset := img.PixOffset(0, j)
		for i := 0; i < size; i++ {
			uoff := (float64(i) - half) * cross
			base := frame.Position.Add(normal.Scale(uoff)).Add(binormal.Scale(voff))
			if len(slabOffsets) > 0 {
				value, ph, ok := reduceSlab(req.SlabMode, 0, len(slabOffsets)-1, func(k int) (float64, string, bool) {
					p := base.Add(frame.Tangent.Scale(slabOffsets[k]))
					v, sampleOK := sampler.trilinearAt(sampler.vol.PatientToVoxel(p))
					return v, photometric, sampleOK
				})
				if ok {
					img.Pix[rowOffset+i] = displayGrayMapped(value, mapper, ph)
				}
				continue
			}
			if val, ok := sampler.trilinearAt(sampler.vol.PatientToVoxel(base)); ok {
				img.Pix[rowOffset+i] = displayGrayMapped(val, mapper, photometric)
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return img, nil
}

// cprSlabSampleOffsets returns symmetric patient-space offsets spanning the
// requested full slab thickness. Physical thickness is deliberately sampled
// independently of output pixel size, so Standard and High-Res reformations
// retain the same field of view and slab extent. The legacy sample-count path
// preserves the behavior of existing callers that only set Thickness.
func cprSlabSampleOffsets(req CPRRequest, legacyStep float64) []float64 {
	offsets, _ := cprSlabSampleOffsetsChecked(req, legacyStep)
	return offsets
}

func cprSlabSampleOffsetsChecked(req CPRRequest, legacyStep float64) ([]float64, error) {
	count, err := cprSlabSampleCountChecked(req)
	if err != nil || count == 0 {
		return nil, err
	}
	if !positiveFinite(legacyStep) {
		legacyStep = 1
	}
	offsets := make([]float64, count)
	if thickness := req.ThicknessMM; positiveFinite(thickness) {
		for i := range offsets {
			offsets[i] = -thickness/2 + thickness*float64(i)/float64(count-1)
		}
		return offsets, nil
	}
	half := float64(count-1) / 2
	for i := range offsets {
		offsets[i] = (float64(i) - half) * legacyStep
	}
	return offsets, nil
}

func cprSlabSampleCountChecked(req CPRRequest) (int, error) {
	if req.SlabMode == SlabNone {
		return 0, nil
	}
	if thickness := req.ThicknessMM; positiveFinite(thickness) {
		step := cprPhysicalSamplingStep(req.Volume)
		countFloat := math.Ceil(thickness/step) + 1
		if !positiveFinite(countFloat) {
			return 0, &CPRInputError{Field: "slab_samples", Reason: "sample count is not finite"}
		}
		if countFloat > MaxCPRSlabSamples {
			return 0, &CPRLimitError{Resource: "slab_samples", Value: MaxCPRSlabSamples + 1, Limit: MaxCPRSlabSamples}
		}
		count := int(countFloat)
		if count < 2 {
			count = 2
		}
		return count, nil
	}
	if req.Thickness <= 1 {
		return 0, nil
	}
	if req.Thickness > MaxCPRSlabSamples {
		return 0, &CPRLimitError{Resource: "slab_samples", Value: uint64(req.Thickness), Limit: MaxCPRSlabSamples}
	}
	return req.Thickness, nil
}

func cprPhysicalSamplingStep(volume *Volume) float64 {
	step := math.Inf(1)
	if volume != nil {
		for _, spacing := range []float64{volume.RowSpacing, volume.ColSpacing, volume.SliceSpacing} {
			if positiveFinite(spacing) && spacing < step {
				step = spacing
			}
		}
	}
	if !positiveFinite(step) {
		return 1
	}
	return step
}

func rotateCPRBasis(normal, tangent Vec3, degrees float64) (Vec3, Vec3) {
	if math.IsNaN(degrees) || math.IsInf(degrees, 0) {
		degrees = 0
	}
	normal = normal.Rotate(tangent, degrees*math.Pi/180).Normalize()
	binormal := tangent.Cross(normal).Normalize()
	normal = binormal.Cross(tangent).Normalize()
	return normal, binormal
}

// CPRRenderer renders CPR views while tracking a monotonic generation so that
// renders for stale navigation state are discarded before reaching UI state.
type CPRRenderer struct {
	gen atomic.Uint64
}

// NextGeneration advances and returns the current render generation. Callers
// invoke it when the navigation state changes and pass the returned id in
// CPRRequest.Generation; older in-flight renders then fail the generation check.
func (r *CPRRenderer) NextGeneration() uint64 {
	return r.gen.Add(1)
}

// CurrentGeneration returns the latest generation id.
func (r *CPRRenderer) CurrentGeneration() uint64 {
	return r.gen.Load()
}

// Render renders req under ctx, discarding the result with ErrCPRSuperseded if a
// newer generation began before (or completes during) the render. A zero
// Generation skips the generation check.
func (r *CPRRenderer) Render(ctx context.Context, req CPRRequest) (image.Image, error) {
	if req.Generation != 0 && req.Generation != r.gen.Load() {
		return nil, ErrCPRSuperseded
	}
	img, err := RenderCPR(ctx, req)
	if err != nil {
		return nil, err
	}
	if req.Generation != 0 && req.Generation != r.gen.Load() {
		return nil, ErrCPRSuperseded
	}
	return img, nil
}
