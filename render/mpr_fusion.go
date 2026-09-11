package render

import (
	"context"
	"errors"
	"fmt"
	"image"
	"math"
	"sync"
	"sync/atomic"

	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

var (
	// ErrFusionMapping reports that a target-to-source mapper failed or
	// produced non-finite patient coordinates. No partial result is returned.
	ErrFusionMapping = errors.New("dicom/render: fused MPR mapping failed")
	// ErrFusionNoOverlap reports that every mapped overlay sample fell outside
	// the secondary volume. The returned scalar planes remain usable so callers
	// can display the base layer with an explicit diagnostic.
	ErrFusionNoOverlap = errors.New("dicom/render: fused MPR overlay has no sampled overlap")
)

// PatientPointMapper maps a point on the target output geometry into the
// secondary source volume's patient coordinate system. A nil mapper is the
// identity mapping for volumes already expressed in the same Frame of
// Reference.
type PatientPointMapper func(Vec3) (Vec3, error)

// FusedMPRSlab defines one patient-space slab shared by both layers.
// ThicknessMM is the full extent centered on TargetPlane. SampleSpacingMM zero
// uses the smallest positive source spacing. Axis zero uses the plane normal.
type FusedMPRSlab struct {
	Mode            SlabMode
	Axis            Vec3
	ThicknessMM     float64
	SampleSpacingMM float64
}

// FusedMPRRequest describes scalar sampling before either layer's presentation
// is applied.
type FusedMPRRequest struct {
	Base           *Volume
	Overlay        *Volume
	TargetPlane    Plane
	Width          int
	Height         int
	TargetToSource PatientPointMapper
	Slab           FusedMPRSlab
	Limits         MPRLimits
}

// FusedMPRSamplingReport makes partial volume-domain coverage explicit. Counts
// are slab samples, not output pixels.
type FusedMPRSamplingReport struct {
	TargetSamples   int64
	BaseInBounds    int64
	OverlayInBounds int64
}

// FusedScalarPlanes retains independently sampled modality values for base and
// overlay on exactly the same target grid and slab extent.
type FusedScalarPlanes struct {
	Base    *ScalarPlane
	Overlay *ScalarPlane
	Report  FusedMPRSamplingReport
}

// Bytes reports the combined retained scalar payload.
func (p *FusedScalarPlanes) Bytes() int64 {
	if p == nil {
		return 0
	}
	return p.Base.Bytes() + p.Overlay.Bytes()
}

// FusionLayerPresentation controls one layer after scalar sampling. Alpha is
// clamped to [0,1]. A nil palette produces grayscale.
type FusionLayerPresentation struct {
	Window  WindowLevel
	Palette *display.PaletteColorLUT
	Alpha   float64
}

// DefaultFusionPresentation returns an opaque grayscale base and a 50% gray
// overlay.
func DefaultFusionPresentation() (base, overlay FusionLayerPresentation) {
	return FusionLayerPresentation{Alpha: 1}, FusionLayerPresentation{Alpha: 0.5}
}

// ResliceFusedMPRScalarContext samples both volumes at the same patient-space
// locations and reduces the same physical slab extent before VOI or palette
// application. Mapper failure is atomic: no partially mapped planes escape.
func ResliceFusedMPRScalarContext(ctx context.Context, request FusedMPRRequest) (*FusedScalarPlanes, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.Base == nil {
		return nil, invalidMPRField("BaseVolume")
	}
	if request.Overlay == nil {
		return nil, invalidMPRField("OverlayVolume")
	}
	axis, firstOffset, step, sampleCount, err := fusedSlabSampling(request)
	if err != nil {
		return nil, err
	}
	limits, err := validateMPRReslice(request.TargetPlane, request.Width, request.Height, sampleCount, 18, request.Limits)
	if err != nil {
		return nil, err
	}
	baseReader, err := request.Base.AcquireReader()
	if err != nil {
		return nil, err
	}
	defer baseReader.Close()
	overlayReader, err := request.Overlay.AcquireReader()
	if err != nil {
		return nil, err
	}
	defer overlayReader.Close()

	basePlane, err := newScalarPlane(request.Width, request.Height, sampleCount, request.Base.Photometric(), defaultScalarVOI(), limits)
	if err != nil {
		return nil, err
	}
	overlayPlane, err := newScalarPlane(request.Width, request.Height, sampleCount, request.Overlay.Photometric(), defaultScalarVOI(), limits)
	if err != nil {
		return nil, err
	}

	renderCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mappingOnce sync.Once
	var mappingErr error
	var baseInBounds atomic.Int64
	var overlayInBounds atomic.Int64
	denomW := float64(maxInt(request.Width-1, 1))
	denomH := float64(maxInt(request.Height-1, 1))
	err = parallelRowsContext(renderCtx, request.Height, func(y int) {
		t := float64(y) / denomH
		for x := 0; x < request.Width; x++ {
			if renderCtx.Err() != nil {
				return
			}
			center := request.TargetPlane.At(float64(x)/denomW, t)
			var baseAcc, overlayAcc float64
			baseCount, overlayCount := 0, 0
			for sample := 0; sample < sampleCount; sample++ {
				if renderCtx.Err() != nil {
					return
				}
				target := center.Add(axis.Scale(firstOffset + float64(sample)*step))
				if value, ok := baseReader.SamplePatient(target); ok {
					baseAcc, baseCount = accumulateSlabValue(request.Slab.Mode, baseAcc, baseCount, value)
					baseInBounds.Add(1)
				}
				source := target
				if request.TargetToSource != nil {
					mapped, mapErr := request.TargetToSource(target)
					if mapErr != nil || !finiteMPRVec3(mapped) {
						mappingOnce.Do(func() {
							if mapErr == nil {
								mapErr = invalidMPRField("TargetToSource")
							}
							mappingErr = &FusionMappingError{Err: mapErr}
							cancel()
						})
						return
					}
					source = mapped
				}
				if value, ok := overlayReader.SamplePatient(source); ok {
					overlayAcc, overlayCount = accumulateSlabValue(request.Slab.Mode, overlayAcc, overlayCount, value)
					overlayInBounds.Add(1)
				}
			}
			baseValue, baseOK := finishSlabValue(request.Slab.Mode, baseAcc, baseCount)
			overlayValue, overlayOK := finishSlabValue(request.Slab.Mode, overlayAcc, overlayCount)
			basePlane.set(x, y, baseValue, baseOK)
			overlayPlane.set(x, y, overlayValue, overlayOK)
		}
	})
	if mappingErr != nil {
		return nil, mappingErr
	}
	if err != nil {
		return nil, err
	}
	targetSamples := int64(request.Width) * int64(request.Height) * int64(sampleCount)
	result := &FusedScalarPlanes{
		Base:    basePlane,
		Overlay: overlayPlane,
		Report: FusedMPRSamplingReport{
			TargetSamples:   targetSamples,
			BaseInBounds:    baseInBounds.Load(),
			OverlayInBounds: overlayInBounds.Load(),
		},
	}
	if result.Report.OverlayInBounds == 0 {
		return result, ErrFusionNoOverlap
	}
	return result, nil
}

// FusionMappingError preserves the mapper's cause while also matching
// ErrFusionMapping through errors.Is.
type FusionMappingError struct {
	Err error
}

func (e *FusionMappingError) Error() string {
	if e == nil || e.Err == nil {
		return ErrFusionMapping.Error()
	}
	return fmt.Sprintf("%v: %v", ErrFusionMapping, e.Err)
}

func (e *FusionMappingError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *FusionMappingError) Is(target error) bool {
	return target == ErrFusionMapping || (e != nil && errors.Is(e.Err, target))
}

func fusedSlabSampling(request FusedMPRRequest) (axis Vec3, firstOffset, step float64, sampleCount int, err error) {
	mode := request.Slab.Mode
	if mode < SlabNone || mode > SlabAverage {
		return Vec3{}, 0, 0, 0, invalidMPRField("SlabMode")
	}
	thickness := request.Slab.ThicknessMM
	spacing := request.Slab.SampleSpacingMM
	if math.IsNaN(thickness) || math.IsInf(thickness, 0) || thickness < 0 {
		return Vec3{}, 0, 0, 0, invalidMPRField("SlabThicknessMM")
	}
	if math.IsNaN(spacing) || math.IsInf(spacing, 0) || spacing < 0 {
		return Vec3{}, 0, 0, 0, invalidMPRField("SlabSampleSpacingMM")
	}
	axis = request.Slab.Axis
	if axis.Length() == 0 {
		axis = request.TargetPlane.U.Cross(request.TargetPlane.V)
	}
	if !finiteMPRVec3(axis) || axis.Length() == 0 {
		return Vec3{}, 0, 0, 0, invalidMPRField("SlabAxis")
	}
	axis = axis.Normalize()
	if mode == SlabNone || thickness == 0 {
		return axis, 0, 0, 1, nil
	}
	if spacing == 0 {
		spacing = smallestPositiveVolumeSpacing(request.Base, request.Overlay)
	}
	if spacing <= 0 {
		return Vec3{}, 0, 0, 0, invalidMPRField("SlabSampleSpacingMM")
	}
	intervalsFloat := math.Ceil(thickness / spacing)
	if math.IsInf(intervalsFloat, 0) || intervalsFloat >= float64(MaxObliqueSlabSamples) {
		return Vec3{}, 0, 0, 0, limitedMPRField("SlabSamples")
	}
	intervals := maxInt(1, int(intervalsFloat))
	sampleCount = intervals + 1
	return axis, -thickness / 2, thickness / float64(intervals), sampleCount, nil
}

func smallestPositiveVolumeSpacing(volumes ...*Volume) float64 {
	spacing := math.Inf(1)
	for _, volume := range volumes {
		if volume == nil {
			continue
		}
		for _, candidate := range []float64{volume.ColSpacing, volume.RowSpacing, volume.SliceSpacing} {
			if finitePositiveSpacing(candidate) && candidate < spacing {
				spacing = candidate
			}
		}
	}
	if math.IsInf(spacing, 1) {
		return 0
	}
	return spacing
}

// Composite applies VOI, optional palettes, and source-over alpha independently
// to the retained base and overlay planes.
func (p *FusedScalarPlanes) Composite(ctx context.Context, base, overlay FusionLayerPresentation) (image.Image, error) {
	if p == nil || p.Base == nil || p.Overlay == nil {
		return nil, invalidMPRField("FusedScalarPlanes")
	}
	width, height := p.Base.Dimensions()
	if overlayWidth, overlayHeight := p.Overlay.Dimensions(); overlayWidth != width || overlayHeight != height {
		return nil, invalidMPRField("FusedScalarPlaneDimensions")
	}
	if math.IsNaN(base.Alpha) || math.IsInf(base.Alpha, 0) || math.IsNaN(overlay.Alpha) || math.IsInf(overlay.Alpha, 0) {
		return nil, invalidMPRField("LayerAlpha")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	base.Alpha = clampUnit(base.Alpha)
	overlay.Alpha = clampUnit(overlay.Alpha)
	baseMapper := prepareWindow(normalizeWindow(base.Window, p.Base.defaultVOI))
	overlayMapper := prepareWindow(normalizeWindow(overlay.Window, p.Overlay.defaultVOI))
	out := image.NewRGBA(image.Rect(0, 0, width, height))
	err := parallelRowsContext(ctx, height, func(y int) {
		for x := 0; x < width; x++ {
			if ctx.Err() != nil {
				return
			}
			var red, green, blue, alpha float64
			if value, ok := p.Base.ValueAt(x, y); ok && base.Alpha > 0 {
				r, g, b := fusionLayerColor(value, baseMapper, p.Base.photometric, base.Palette)
				red, green, blue, alpha = compositeFusionLayer(red, green, blue, alpha, r, g, b, base.Alpha)
			}
			if value, ok := p.Overlay.ValueAt(x, y); ok && overlay.Alpha > 0 {
				r, g, b := fusionLayerColor(value, overlayMapper, p.Overlay.photometric, overlay.Palette)
				red, green, blue, alpha = compositeFusionLayer(red, green, blue, alpha, r, g, b, overlay.Alpha)
			}
			index := (y*width + x) * 4
			if alpha > 0 {
				out.Pix[index+0] = byte(math.Round(clampUnit(red/alpha) * 255))
				out.Pix[index+1] = byte(math.Round(clampUnit(green/alpha) * 255))
				out.Pix[index+2] = byte(math.Round(clampUnit(blue/alpha) * 255))
				out.Pix[index+3] = byte(math.Round(clampUnit(alpha) * 255))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func fusionLayerColor(value float64, mapper preparedVOI, photometric string, palette *display.PaletteColorLUT) (uint8, uint8, uint8) {
	gray := displayGrayMapped(value, mapper, photometric)
	if palette == nil {
		return gray, gray, gray
	}
	return palette.At(int(gray))
}

func compositeFusionLayer(dstR, dstG, dstB, dstA float64, sourceR, sourceG, sourceB uint8, sourceA float64) (float64, float64, float64, float64) {
	sourceA = clampUnit(sourceA)
	oneMinusSource := 1 - sourceA
	return float64(sourceR)/255*sourceA + dstR*oneMinusSource,
		float64(sourceG)/255*sourceA + dstG*oneMinusSource,
		float64(sourceB)/255*sourceA + dstB*oneMinusSource,
		sourceA + dstA*oneMinusSource
}
