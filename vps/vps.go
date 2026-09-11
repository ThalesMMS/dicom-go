package vps

import (
	"errors"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

const (
	GrayscalePlanarMPRVolumetricPresentationStateStorage       = "1.2.840.10008.5.1.4.1.1.11.6"
	CompositingPlanarMPRVolumetricPresentationStateStorage     = "1.2.840.10008.5.1.4.1.1.11.7"
	VolumeRenderingVolumetricPresentationStateStorage          = "1.2.840.10008.5.1.4.1.1.11.9"
	SegmentedVolumeRenderingVolumetricPresentationStateStorage = "1.2.840.10008.5.1.4.1.1.11.10"
	MultipleVolumeRenderingVolumetricPresentationStateStorage  = "1.2.840.10008.5.1.4.1.1.11.11"
	privateCreator                                             = "THALESMMS_VPS"
)

var (
	ErrUnsupportedSOPClass = errors.New("dicom/vps: unsupported SOP class")
	ErrUnsupportedPayload  = errors.New("dicom/vps: unsupported standard payload")
	ErrInvalidObject       = errors.New("dicom/vps: invalid object")
	ErrMissingReference    = errors.New("dicom/vps: missing reference")
)

var (
	tagVPSInputSequence        = core.NewTag(0x0070, 0x1201)
	tagPresentationInputType   = core.NewTag(0x0070, 0x1202)
	tagCrop                    = core.NewTag(0x0070, 0x1204)
	tagVPSInputNumber          = core.NewTag(0x0070, 0x1207)
	tagVPSInputSetUID          = core.NewTag(0x0070, 0x1209)
	tagVPSInputSetSequence     = core.NewTag(0x0070, 0x120A)
	tagGlobalCrop              = core.NewTag(0x0070, 0x120B)
	tagReferencedImageSequence = core.NewTag(0x0008, 0x1140)
	tagPrivateCreator          = core.NewTag(0x0071, 0x0010)
	tagRenderPresetName        = core.NewTag(0x0071, 0x1001)
	tagCameraTarget            = core.NewTag(0x0071, 0x1002)
	tagCameraAngles            = core.NewTag(0x0071, 0x1003)
	tagCameraDistanceFov       = core.NewTag(0x0071, 0x1004)
)

type ReferencedInstance struct {
	SOPClassUID    string
	SOPInstanceUID string
}

type Input struct {
	Number                   int
	InputSetUID              string
	ReferencedInstances      []ReferencedInstance
	CropSpecificationNumbers []int
	VOI                      *VOI
	RenderingMethod          string
}

// VOI describes the Window Center/Width transform attached to one VPS input.
// LUT-based VOI is deliberately not represented because applying it as WC/WW
// would change the presentation.
type VOI struct {
	WindowCenter float64
	WindowWidth  float64
}

// CroppingSpecification is one standard Volume Cropping Sequence item. The
// supported method is BOUNDING_BOX; other standard methods fail closed on read.
type CroppingSpecification struct {
	Number      int
	Method      string
	BoundingBox [6]float64
}

// MPRGeometry is the standard PLANAR MPR geometry in VPS-RCS coordinates.
type MPRGeometry struct {
	ThicknessType   string
	SlabThickness   float64
	TopLeft         render.Vec3
	WidthDirection  render.Vec3
	Width           float64
	HeightDirection render.Vec3
	Height          float64
}

// VolumeRenderGeometry is the standard viewpoint and field-of-view geometry.
type VolumeRenderGeometry struct {
	Projection  string
	Position    render.Vec3
	LookAt      render.Vec3
	Up          render.Vec3
	FieldOfView [6]float64
}

// ClassificationComponent is the supported one-input scalar-to-RGBA
// classification. Palette data is retained as 16-bit table entries.
type ClassificationComponent struct {
	InputNumbers          []int
	RGBTransferFunction   string
	AlphaTransferFunction string
	// PaletteDescriptor contains entry count, first mapped value, and bits per
	// entry. VPS TABLE classifications require a first mapped value of zero.
	PaletteDescriptor [3]uint16
	RedPalette        []uint16
	GreenPalette      []uint16
	BluePalette       []uint16
	AlphaPalette      []uint16
	Description       string
}

// Display describes the standard presentation output pipeline.
type Display struct {
	PixelPresentation    string
	PresentationLUTShape string
	InputSetUID          string
	Classification       []ClassificationComponent
	ICCProfile           []byte
	ColorSpace           string
}

// Shading describes the standard Render Shading module. The current viewer
// applies its enabled state; numeric Phong parameters are retained losslessly.
type Shading struct {
	Style          string
	Ambient        float64
	LightDirection render.Vec3
	Diffuse        float64
	Specular       float64
	Shininess      float64
}

type Camera struct {
	Target   render.Vec3
	Yaw      float64
	Pitch    float64
	Roll     float64
	Distance float64
	FovY     float64
}

type State struct {
	SOPClassUID       string
	SOPInstanceUID    string
	StudyInstanceUID  string
	SeriesInstanceUID string
	// FrameOfReferenceUID identifies the patient coordinate system used by the
	// authored standard VPS geometry and all of its referenced inputs.
	FrameOfReferenceUID            string
	Inputs                         []Input
	GlobalCropSpecificationNumbers []int
	CroppingSpecifications         []CroppingSpecification
	MPRGeometry                    *MPRGeometry
	VolumeRenderGeometry           *VolumeRenderGeometry
	RenderingMethod                string
	Display                        *Display
	Shading                        *Shading
	RenderPresetName               string
	Camera                         Camera
}

type AppliedState struct {
	Inputs                         []Input
	Preset                         render.VRPreset
	Camera                         Camera
	GlobalCropSpecificationNumbers []int
	CroppingSpecifications         []CroppingSpecification
	MPRGeometry                    *MPRGeometry
	VolumeRenderGeometry           *VolumeRenderGeometry
	VOI                            *VOI
	Shading                        *Shading
}

// Write encodes the volumetric presentation state into a DICOM file suitable for storage or transmission. It returns ErrInvalidObject if state is nil, or ErrUnsupportedSOPClass if the SOP class UID is not supported.
func Write(state *State) (*object.File, error) {
	if state == nil {
		return nil, fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	if !supportedSOPClass(state.SOPClassUID) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, state.SOPClassUID)
	}
	if hasStandardState(state) {
		return writeStandard(state)
	}
	for i, input := range state.Inputs {
		if input.Number <= 0 || input.Number > 1<<16-1 {
			return nil, fmt.Errorf("%w: input %d number %d is outside US", ErrInvalidObject, i, input.Number)
		}
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, state.SOPClassUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, state.SOPInstanceUID),
		derivedio.CS(derivedio.TagModality, "PR"),
		derivedio.UI(derivedio.TagStudyInstanceUID, state.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, state.SeriesInstanceUID),
	}
	if state.FrameOfReferenceUID != "" {
		elements = append(elements, derivedio.UI(derivedio.TagFrameOfReferenceUID, state.FrameOfReferenceUID))
	}
	elements = append(elements,
		inputSetSequence(state.Inputs),
		inputSequence(state.Inputs),
		derivedio.CS(tagGlobalCrop, "NO"),
		derivedio.LO(tagPrivateCreator, privateCreator),
		derivedio.LO(tagRenderPresetName, presetName(state.RenderPresetName)),
		derivedio.DS(tagCameraTarget, state.Camera.Target.X, state.Camera.Target.Y, state.Camera.Target.Z),
		derivedio.DS(tagCameraAngles, state.Camera.Yaw, state.Camera.Pitch, state.Camera.Roll),
		derivedio.DS(tagCameraDistanceFov, state.Camera.Distance, state.Camera.FovY),
	)
	dataset := derivedio.Object(elements...)
	return derivedio.File(state.SOPClassUID, state.SOPInstanceUID, dataset)
}

// Read reconstructs either the supported standard VPS modules or the legacy
// private renderer extension used by this package. Unsupported standard
// pipelines fail closed with ErrUnsupportedPayload.
func Read(obj *object.Object) (*State, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClassUID := derivedio.CleanUID(obj, derivedio.TagSOPClassUID)
	if !supportedSOPClass(sopClassUID) {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	creator := derivedio.CleanString(obj, tagPrivateCreator)
	hasRendererPayload := hasPrivateRendererPayload(obj)
	if creator == privateCreator && !hasRendererPayload {
		return nil, fmt.Errorf("%w: private creator is present without renderer payload", ErrInvalidObject)
	}
	legacyPrivatePayload := creator == "" && hasRendererPayload
	if hasStandardGeometry(obj, sopClassUID) {
		return readStandard(obj, sopClassUID)
	}
	if creator != privateCreator && !legacyPrivatePayload {
		return nil, fmt.Errorf("%w: SOP class %s requires standard VPS geometry, crop, VOI, and display modules", ErrUnsupportedPayload, sopClassUID)
	}
	return &State{
		SOPClassUID:         sopClassUID,
		SOPInstanceUID:      derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:    derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID),
		Inputs:              readInputs(obj),
		RenderPresetName:    derivedio.CleanString(obj, tagRenderPresetName),
		Camera:              readCamera(obj),
	}, nil
}

// Validate verifies that state can be written without silently omitting any
// represented standard VPS module.
func Validate(state *State) error {
	if state == nil {
		return fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	if !supportedSOPClass(state.SOPClassUID) {
		return fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, state.SOPClassUID)
	}
	if hasStandardState(state) {
		return validateStandardState(state)
	}
	return nil
}

func hasPrivateRendererPayload(obj *object.Object) bool {
	return obj.Has(tagRenderPresetName) || obj.Has(tagCameraTarget) || obj.Has(tagCameraAngles) || obj.Has(tagCameraDistanceFov)
}

// Apply resolves the render preset for the given state, returning an AppliedState containing the resolved preset, a shallow copy of the inputs, and the original camera. It returns an error if state is nil.
func Apply(state *State) (AppliedState, error) {
	if state == nil {
		return AppliedState{}, fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	if hasStandardState(state) {
		if err := validateStandardState(state); err != nil {
			return AppliedState{}, err
		}
	}
	preset, err := appliedPreset(state)
	if err != nil {
		return AppliedState{}, err
	}
	var voi *VOI
	for _, input := range state.Inputs {
		if input.VOI != nil {
			copyVOI := *input.VOI
			voi = &copyVOI
			break
		}
	}
	return AppliedState{
		Inputs:                         append([]Input(nil), state.Inputs...),
		Preset:                         preset,
		Camera:                         state.Camera,
		GlobalCropSpecificationNumbers: append([]int(nil), state.GlobalCropSpecificationNumbers...),
		CroppingSpecifications:         append([]CroppingSpecification(nil), state.CroppingSpecifications...),
		MPRGeometry:                    cloneMPRGeometry(state.MPRGeometry),
		VolumeRenderGeometry:           cloneVolumeRenderGeometry(state.VolumeRenderGeometry),
		VOI:                            voi,
		Shading:                        cloneShading(state.Shading),
	}, nil
}

// inputSetSequence groups the referenced instances for each input set.
func inputSetSequence(inputs []Input) core.Element {
	type inputSet struct {
		uid  string
		refs []ReferencedInstance
		seen map[string]bool
	}
	sets := make([]inputSet, 0, len(inputs))
	indexes := map[string]int{}
	for _, input := range inputs {
		index, ok := indexes[input.InputSetUID]
		if !ok {
			index = len(sets)
			indexes[input.InputSetUID] = index
			sets = append(sets, inputSet{uid: input.InputSetUID, seen: map[string]bool{}})
		}
		for _, ref := range input.ReferencedInstances {
			key := ref.SOPClassUID + "\x00" + ref.SOPInstanceUID
			if sets[index].seen[key] {
				continue
			}
			sets[index].seen[key] = true
			sets[index].refs = append(sets[index].refs, ref)
		}
	}
	items := make([]core.DataSet, 0, len(sets))
	for _, set := range sets {
		refItems := make([]core.DataSet, 0, len(set.refs))
		for _, ref := range set.refs {
			refItems = append(refItems, derivedio.DataSet(
				derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID),
				derivedio.UI(derivedio.TagRefSOPInstanceUID, ref.SOPInstanceUID),
			))
		}
		items = append(items, derivedio.DataSet(
			derivedio.UI(tagVPSInputSetUID, set.uid),
			derivedio.CS(tagPresentationInputType, "VOLUME"),
			derivedio.Seq(tagReferencedImageSequence, refItems...),
		))
	}
	return derivedio.Seq(tagVPSInputSetSequence, items...)
}

// inputSequence builds the ordered VPS inputs that refer to root input sets.
func inputSequence(inputs []Input) core.Element {
	items := make([]core.DataSet, 0, len(inputs))
	for _, input := range inputs {
		items = append(items, derivedio.DataSet(
			derivedio.US(tagVPSInputNumber, uint16(input.Number)),
			derivedio.UI(tagVPSInputSetUID, input.InputSetUID),
			derivedio.CS(tagCrop, "NO"),
		))
	}
	return derivedio.Seq(tagVPSInputSequence, items...)
}

// readInputs parses VPS inputs from a DICOM object.
func readInputs(obj *object.Object) []Input {
	inputSets := map[string][]ReferencedInstance{}
	for _, set := range derivedio.Sequence(obj, tagVPSInputSetSequence) {
		uid := derivedio.CleanUID(set, tagVPSInputSetUID)
		for _, refObj := range derivedio.Sequence(set, tagReferencedImageSequence) {
			inputSets[uid] = append(inputSets[uid], ReferencedInstance{
				SOPClassUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
				SOPInstanceUID: derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
			})
		}
	}
	items := derivedio.Sequence(obj, tagVPSInputSequence)
	out := make([]Input, 0, len(items))
	for _, item := range items {
		input := Input{
			Number:      derivedio.Int(item, tagVPSInputNumber),
			InputSetUID: derivedio.CleanUID(item, tagVPSInputSetUID),
		}
		input.ReferencedInstances = append(input.ReferencedInstances, inputSets[input.InputSetUID]...)
		if len(input.ReferencedInstances) == 0 {
			for _, inputSet := range derivedio.Sequence(item, tagVPSInputSetSequence) {
				for _, refObj := range derivedio.Sequence(inputSet, tagReferencedImageSequence) {
					input.ReferencedInstances = append(input.ReferencedInstances, ReferencedInstance{
						SOPClassUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
						SOPInstanceUID: derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
					})
				}
			}
		}
		out = append(out, input)
	}
	return out
}

// readCamera reads camera target, rotation angles, and distance/FOV parameters from a DICOM object.
func readCamera(obj *object.Object) Camera {
	target := derivedio.Floats(obj, tagCameraTarget)
	angles := derivedio.Floats(obj, tagCameraAngles)
	distanceFov := derivedio.Floats(obj, tagCameraDistanceFov)
	var camera Camera
	if len(target) >= 3 {
		camera.Target = render.Vec3{X: target[0], Y: target[1], Z: target[2]}
	}
	if len(angles) >= 3 {
		camera.Yaw = angles[0]
		camera.Pitch = angles[1]
		camera.Roll = angles[2]
	}
	if len(distanceFov) >= 2 {
		camera.Distance = distanceFov[0]
		camera.FovY = distanceFov[1]
	}
	return camera
}

// supportedSOPClass reports whether uid is a supported VPS SOP class UID.
func supportedSOPClass(uid string) bool {
	switch uid {
	case GrayscalePlanarMPRVolumetricPresentationStateStorage,
		CompositingPlanarMPRVolumetricPresentationStateStorage,
		VolumeRenderingVolumetricPresentationStateStorage,
		SegmentedVolumeRenderingVolumetricPresentationStateStorage,
		MultipleVolumeRenderingVolumetricPresentationStateStorage:
		return true
	default:
		return false
	}
}

// presetName returns the provided value, or the default VR preset name if the value is empty.
func presetName(value string) string {
	if value == "" {
		return render.DefaultVRPreset().Name
	}
	return value
}
