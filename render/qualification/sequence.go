package qualification

import (
	"fmt"
	"math"

	dicomrender "github.com/ThalesMMS/dicom-go/render"
)

type Operation string

const (
	Operation2D  Operation = "2d"
	OperationMPR Operation = "mpr"
	OperationVR  Operation = "vr"
)

// StateStep is one frozen, PHI-free qualification transition. View is nil only
// for 2D steps; MPR and VR steps carry a fully validated canonical ViewState.
type StateStep struct {
	ID                     string
	Operation              Operation
	VolumeGeneration       uint64
	ViewGeneration         uint64
	PresentationGeneration uint64
	SliceIndex             uint32
	View                   *dicomrender.ViewState
}

func FrozenMPRSequence() ([]StateStep, error) {
	sequence, err := buildMPRSequence(1, 1)
	if err != nil {
		return nil, err
	}
	return freezeSequence(sequence)
}

func FrozenVRSequence() ([]StateStep, error) {
	sequence, err := buildVRSequence(1, 101)
	if err != nil {
		return nil, err
	}
	return freezeSequence(sequence)
}

func FrozenMixedSequence() ([]StateStep, error) {
	mpr, err := buildMPRSequence(1, 201)
	if err != nil {
		return nil, err
	}
	vr, err := buildVRSequence(1, 301)
	if err != nil {
		return nil, err
	}
	replacementMPR, err := buildMPRSequence(2, 401)
	if err != nil {
		return nil, err
	}
	mprAxial, err := stateStepByID(mpr, "mpr-01-axial")
	if err != nil {
		return nil, err
	}
	vrFit, err := stateStepByID(vr, "vr-01-fit")
	if err != nil {
		return nil, err
	}
	mprOblique, err := stateStepByID(mpr, "mpr-04-oblique")
	if err != nil {
		return nil, err
	}
	vrTransfer, err := stateStepByID(vr, "vr-03-transfer")
	if err != nil {
		return nil, err
	}
	replacementAxial, err := stateStepByID(replacementMPR, "mpr-01-axial")
	if err != nil {
		return nil, err
	}
	mprAxial.ID = "mixed-02-mpr-axial"
	vrFit.ID = "mixed-03-vr-fit"
	mprOblique.ID = "mixed-05-mpr-oblique"
	vrTransfer.ID = "mixed-06-vr-transfer"
	replacementAxial.ID = "mixed-08-mpr-replacement"
	return freezeSequence([]StateStep{
		{ID: "mixed-01-2d-center", Operation: Operation2D, VolumeGeneration: 1, ViewGeneration: 1, SliceIndex: 150},
		mprAxial,
		vrFit,
		{ID: "mixed-04-2d-jump", Operation: Operation2D, VolumeGeneration: 1, ViewGeneration: 2, SliceIndex: 37},
		mprOblique,
		vrTransfer,
		{ID: "mixed-07-2d-replacement", Operation: Operation2D, VolumeGeneration: 2, ViewGeneration: 1, SliceIndex: 149},
		replacementAxial,
	})
}

func stateStepByID(sequence []StateStep, id string) (StateStep, error) {
	for _, step := range sequence {
		if step.ID == id {
			return step, nil
		}
	}
	return StateStep{}, fmt.Errorf("qualification: state step %q not found", id)
}

func ValidateStateSequence(sequence []StateStep) error {
	if len(sequence) == 0 {
		return fmt.Errorf("qualification: empty state sequence")
	}
	seen := make(map[string]struct{}, len(sequence))
	for index, step := range sequence {
		if step.ID == "" {
			return fmt.Errorf("qualification: state step %d has empty ID", index)
		}
		if _, exists := seen[step.ID]; exists {
			return fmt.Errorf("qualification: duplicate state step ID %q", step.ID)
		}
		seen[step.ID] = struct{}{}
		if step.VolumeGeneration == 0 || step.ViewGeneration == 0 {
			return fmt.Errorf("qualification: state step %q has zero generation", step.ID)
		}
		switch step.Operation {
		case Operation2D:
			if step.View != nil {
				return fmt.Errorf("qualification: 2D step %q unexpectedly has ViewState", step.ID)
			}
		case OperationMPR, OperationVR:
			if step.View == nil {
				return fmt.Errorf("qualification: render step %q lacks ViewState", step.ID)
			}
			if err := dicomrender.ValidateViewState(*step.View); err != nil {
				return fmt.Errorf("qualification: state step %q: %w", step.ID, err)
			}
			if step.View.VolumeGeneration != step.VolumeGeneration ||
				step.View.ViewGeneration != step.ViewGeneration ||
				step.View.PresentationGeneration != step.PresentationGeneration {
				return fmt.Errorf("qualification: state step %q generation mismatch", step.ID)
			}
			if step.Operation == OperationMPR && step.View.Kind != dicomrender.ViewKindMPR {
				return fmt.Errorf("qualification: MPR step %q has wrong view kind", step.ID)
			}
			if step.Operation == OperationVR && step.View.Kind != dicomrender.ViewKindVR {
				return fmt.Errorf("qualification: VR step %q has wrong view kind", step.ID)
			}
		default:
			return fmt.Errorf("qualification: state step %q operation %q", step.ID, step.Operation)
		}
	}
	return nil
}

func buildMPRSequence(volumeGeneration, firstViewGeneration uint64) ([]StateStep, error) {
	descriptor, err := syntheticDescriptor(volumeGeneration)
	if err != nil {
		return nil, err
	}
	affine := descriptor.IndexToPatientLPS
	xStep := affineVector(affine, 1, 0, 0)
	yStep := affineVector(affine, 0, 1, 0)
	zStep := affineVector(affine, 0, 0, 1)
	center := affinePoint(
		affine,
		float64(descriptor.Dimensions[0]-1)/2,
		float64(descriptor.Dimensions[1]-1)/2,
		float64(descriptor.Dimensions[2]-1)/2,
	)
	const output = uint32(256)
	originFor := func(u, v [3]float64) [3]float64 {
		return subtract(
			subtract(center, scale(u, float64(output-1)/2)),
			scale(v, float64(output-1)/2),
		)
	}
	makeState := func(
		generation uint64,
		origin, u, v [3]float64,
		interpolation dicomrender.Interpolation,
		slab dicomrender.SlabMode,
		slabAxis [3]float64,
		thickness, spacing float64,
		presentation uint64,
	) (*dicomrender.ViewState, error) {
		state := dicomrender.ViewState{
			ContractVersion:        dicomrender.ViewStateContractVersion,
			StructSize:             dicomrender.ViewStateHeaderSizeV1,
			Kind:                   dicomrender.ViewKindMPR,
			VolumeGeneration:       volumeGeneration,
			ViewGeneration:         generation,
			PresentationGeneration: presentation,
			OutputWidth:            output,
			OutputHeight:           output,
			MPR: dicomrender.MPRViewState{
				PixelOriginLPS:      origin,
				PixelStepULPS:       u,
				PixelStepVLPS:       v,
				Interpolation:       interpolation,
				SlabMode:            slab,
				SlabAxisLPS:         slabAxis,
				SlabThicknessMM:     thickness,
				SlabSampleSpacingMM: spacing,
				OutsideValue:        -1024,
			},
		}
		frozen, err := dicomrender.FreezeViewState(state)
		if err != nil {
			return nil, err
		}
		return &frozen, nil
	}
	obliqueU := add(scale(xStep, math.Cos(math.Pi/6)), scale(zStep, math.Sin(math.Pi/6)))
	obliqueV := yStep
	obliqueNormal, err := unitCross(obliqueU, obliqueV)
	if err != nil {
		return nil, err
	}
	stateSpecs := []struct {
		generation         uint64
		origin, u, v       [3]float64
		interpolation      dicomrender.Interpolation
		slab               dicomrender.SlabMode
		slabAxis           [3]float64
		thickness, spacing float64
		presentation       uint64
	}{
		{firstViewGeneration, originFor(xStep, yStep), xStep, yStep, dicomrender.InterpolationLinear, dicomrender.SlabNone, [3]float64{}, 0, 0, 1},
		{firstViewGeneration + 1, originFor(yStep, zStep), yStep, zStep, dicomrender.InterpolationLinear, dicomrender.SlabNone, [3]float64{}, 0, 0, 1},
		{firstViewGeneration + 2, originFor(xStep, zStep), xStep, zStep, dicomrender.InterpolationLinear, dicomrender.SlabNone, [3]float64{}, 0, 0, 1},
		{firstViewGeneration + 3, originFor(obliqueU, obliqueV), obliqueU, obliqueV, dicomrender.InterpolationCubic, dicomrender.SlabNone, [3]float64{}, 0, 0, 2},
		{firstViewGeneration + 4, originFor(obliqueU, obliqueV), obliqueU, obliqueV, dicomrender.InterpolationLinear, dicomrender.SlabMIP, obliqueNormal, 12, 0.5, 2},
	}
	states := make([]*dicomrender.ViewState, 0, len(stateSpecs))
	for _, spec := range stateSpecs {
		state, err := makeState(
			spec.generation,
			spec.origin,
			spec.u,
			spec.v,
			spec.interpolation,
			spec.slab,
			spec.slabAxis,
			spec.thickness,
			spec.spacing,
			spec.presentation,
		)
		if err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	ids := []string{"mpr-01-axial", "mpr-02-sagittal", "mpr-03-coronal", "mpr-04-oblique", "mpr-05-slab-mip"}
	result := make([]StateStep, len(states))
	for index, state := range states {
		result[index] = StateStep{
			ID:                     ids[index],
			Operation:              OperationMPR,
			VolumeGeneration:       state.VolumeGeneration,
			ViewGeneration:         state.ViewGeneration,
			PresentationGeneration: state.PresentationGeneration,
			View:                   state,
		}
	}
	return result, nil
}

func buildVRSequence(volumeGeneration, firstViewGeneration uint64) ([]StateStep, error) {
	descriptor, err := syntheticDescriptor(volumeGeneration)
	if err != nil {
		return nil, err
	}
	affine := descriptor.IndexToPatientLPS
	center := affinePoint(
		affine,
		float64(descriptor.Dimensions[0]-1)/2,
		float64(descriptor.Dimensions[1]-1)/2,
		float64(descriptor.Dimensions[2]-1)/2,
	)
	radius := 320.0
	cameras := [][3]float64{
		{center[0], center[1], center[2] + radius*2.5},
		{center[0] + radius*1.25, center[1], center[2] + radius*2.1},
		{center[0] - radius*0.8, center[1] + radius*0.6, center[2] + radius*2.2},
		{center[0], center[1], center[2] + radius*2.5},
	}
	ids := []string{"vr-01-fit", "vr-02-orbit", "vr-03-transfer", "vr-04-settle"}
	result := make([]StateStep, len(cameras))
	for index, position := range cameras {
		presentation := uint64(1)
		if index >= 2 {
			presentation = 2
		}
		state := dicomrender.ViewState{
			ContractVersion:        dicomrender.ViewStateContractVersion,
			StructSize:             dicomrender.ViewStateHeaderSizeV1,
			Kind:                   dicomrender.ViewKindVR,
			VolumeGeneration:       volumeGeneration,
			ViewGeneration:         firstViewGeneration + uint64(index),
			PresentationGeneration: presentation,
			OutputWidth:            384,
			OutputHeight:           384,
			VR: dicomrender.VRViewState{
				PositionLPS:          position,
				FocalPointLPS:        center,
				ViewUpLPS:            [3]float64{0, 1, 0},
				Projection:           dicomrender.ProjectionPerspective,
				VerticalFOVRadians:   math.Pi / 4,
				NearMM:               0.1,
				FarMM:                5000,
				Mode:                 dicomrender.VRModeDVR,
				SampleDistanceMM:     0.7,
				ImageSampleDistance:  1,
				ShadingEnabled:       1,
				Ambient:              0.2,
				Diffuse:              0.7,
				Specular:             0.1,
				SpecularPower:        10,
				GradientOpacityScale: 220,
				TransferLUT: dicomrender.TransferLUT{
					DomainMin: -1024,
					DomainMax: 3071,
					Samples: []dicomrender.TransferSample{
						{R: 0, G: 0, B: 0, A: 0},
						{R: 0.18, G: 0.08, B: 0.04, A: 0.08},
						{R: 0.72, G: 0.49, B: 0.32, A: 0.34},
						{R: 1, G: 0.92, B: 0.82, A: 0.82},
					},
				},
			},
		}
		frozen, err := dicomrender.FreezeViewState(state)
		if err != nil {
			return nil, err
		}
		result[index] = StateStep{
			ID:                     ids[index],
			Operation:              OperationVR,
			VolumeGeneration:       frozen.VolumeGeneration,
			ViewGeneration:         frozen.ViewGeneration,
			PresentationGeneration: frozen.PresentationGeneration,
			View:                   &frozen,
		}
	}
	return result, nil
}

func syntheticDescriptor(generation uint64) (dicomrender.VolumeDescriptor, error) {
	descriptor, err := syntheticCTDescriptor(referenceSyntheticCTDimensions, generation)
	if err != nil {
		return dicomrender.VolumeDescriptor{}, err
	}
	return descriptor, nil
}

func freezeSequence(source []StateStep) ([]StateStep, error) {
	result := make([]StateStep, len(source))
	for index, step := range source {
		result[index] = step
		if step.View != nil {
			view, err := dicomrender.FreezeViewState(*step.View)
			if err != nil {
				return nil, err
			}
			result[index].View = &view
		}
	}
	return result, nil
}

func affinePoint(affine dicomrender.GeometryAffine, x, y, z float64) [3]float64 {
	return [3]float64{
		affine[0]*x + affine[1]*y + affine[2]*z + affine[3],
		affine[4]*x + affine[5]*y + affine[6]*z + affine[7],
		affine[8]*x + affine[9]*y + affine[10]*z + affine[11],
	}
}

func affineVector(affine dicomrender.GeometryAffine, x, y, z float64) [3]float64 {
	return [3]float64{
		affine[0]*x + affine[1]*y + affine[2]*z,
		affine[4]*x + affine[5]*y + affine[6]*z,
		affine[8]*x + affine[9]*y + affine[10]*z,
	}
}

func add(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] + b[0], a[1] + b[1], a[2] + b[2]}
}

func subtract(a, b [3]float64) [3]float64 {
	return [3]float64{a[0] - b[0], a[1] - b[1], a[2] - b[2]}
}

func scale(value [3]float64, factor float64) [3]float64 {
	return [3]float64{value[0] * factor, value[1] * factor, value[2] * factor}
}

func unitCross(a, b [3]float64) ([3]float64, error) {
	cross := [3]float64{
		a[1]*b[2] - a[2]*b[1],
		a[2]*b[0] - a[0]*b[2],
		a[0]*b[1] - a[1]*b[0],
	}
	length := math.Hypot(math.Hypot(cross[0], cross[1]), cross[2])
	if length == 0 || math.IsNaN(length) || math.IsInf(length, 0) {
		return [3]float64{}, fmt.Errorf("qualification: cannot normalize degenerate cross product")
	}
	return scale(cross, 1/length), nil
}
