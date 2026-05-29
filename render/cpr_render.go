package render

import (
	"context"
	"errors"
	"image"
	"math"
	"sync/atomic"
)

var (
	// ErrCPRInput reports a render request missing its volume or path.
	ErrCPRInput = errors.New("render: CPR render requires a volume and a path")
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

// renderLongitudinalCPR renders straightened (stretched=false) or stretched
// (stretched=true) CPR: columns run along the centerline arc length, rows across
// the cross-section. This orientation matches curved-MPR workstations where
// transverse section markers are vertical lines on the longitudinal output.
func renderLongitudinalCPR(ctx context.Context, req CPRRequest, stretched bool) (image.Image, error) {
	vol := req.Volume
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return nil, ErrCPRInput
	}
	defer sampler.Close()
	samples := req.Path.Resample(req.arcSpacing())
	if len(samples) == 0 {
		return blankImage(req.width(), 1), nil
	}
	width := req.width()
	cross := req.crossSpacing()
	half := float64(width-1) / 2
	photometric := vol.Photometric()
	window := normalizeWindow(req.Window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)

	ref := req.StretchDir
	if stretched && ref == (Vec3{}) {
		ref = samples[0].Normal
	}

	img := image.NewGray(image.Rect(0, 0, len(samples), width))
	err := parallelRowsContext(ctx, width, func(r int) {
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
			if offsets := cprSlabSampleOffsets(req, cross); len(offsets) > 0 {
				value, ph, ok := reduceSlab(req.SlabMode, 0, len(offsets)-1, func(k int) (float64, string, bool) {
					p := base.Add(slabDir.Scale(offsets[k]))
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
	sampler, ok := newVolumeSampler(vol)
	if !ok {
		return nil, ErrCPRInput
	}
	defer sampler.Close()
	frame := req.Path.FrameAt(req.ArcLength)
	normal, binormal := rotateCPRBasis(frame.Normal, frame.Tangent, req.RotationDegrees)
	size := req.width()
	cross := req.crossSpacing()
	half := float64(size-1) / 2
	photometric := vol.Photometric()
	window := normalizeWindow(req.Window, WindowLevel{Center: defaultWindowCenter, Width: defaultWindowWidth})
	mapper := prepareWindow(window)

	img := image.NewGray(image.Rect(0, 0, size, size))
	err := parallelRowsContext(ctx, size, func(j int) {
		voff := (float64(j) - half) * cross
		rowOffset := img.PixOffset(0, j)
		for i := 0; i < size; i++ {
			uoff := (float64(i) - half) * cross
			base := frame.Position.Add(normal.Scale(uoff)).Add(binormal.Scale(voff))
			if offsets := cprSlabSampleOffsets(req, cross); len(offsets) > 0 {
				value, ph, ok := reduceSlab(req.SlabMode, 0, len(offsets)-1, func(k int) (float64, string, bool) {
					p := base.Add(frame.Tangent.Scale(offsets[k]))
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
	if req.SlabMode == SlabNone {
		return nil
	}
	if thickness := req.ThicknessMM; positiveFinite(thickness) {
		step := cprPhysicalSamplingStep(req.Volume)
		count := int(math.Ceil(thickness/step)) + 1
		if count < 2 {
			count = 2
		}
		// Bound adversarial requests while still supporting slabs far larger than
		// normal clinical use.
		if count > 4097 {
			count = 4097
		}
		offsets := make([]float64, count)
		for i := range offsets {
			offsets[i] = -thickness/2 + thickness*float64(i)/float64(count-1)
		}
		return offsets
	}
	if req.Thickness <= 1 {
		return nil
	}
	if !positiveFinite(legacyStep) {
		legacyStep = 1
	}
	offsets := make([]float64, req.Thickness)
	half := float64(req.Thickness-1) / 2
	for i := range offsets {
		offsets[i] = (float64(i) - half) * legacyStep
	}
	return offsets
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
