package vps

import (
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

const (
	CropBoundingBox             = "BOUNDING_BOX"
	MPRThicknessThin            = "THIN"
	MPRThicknessSlab            = "SLAB"
	ProjectionOrthographic      = "ORTHOGRAPHIC"
	ProjectionPerspective       = "PERSPECTIVE"
	RenderingMaximumIP          = "MAXIMUM_IP"
	RenderingMinimumIP          = "MINIMUM_IP"
	RenderingAverageIP          = "AVERAGE_IP"
	RenderingVolumeRendered     = "VOLUME_RENDERED"
	PixelPresentationMonochrome = "MONOCHROME"
	PixelPresentationTrueColor  = "TRUE_COLOR"
	PresentationLUTIdentity     = "IDENTITY"
	PresentationLUTInverse      = "INVERSE"
	RGBTransferEqual            = "EQUAL_RGB"
	RGBTransferTable            = "TABLE"
	AlphaTransferNone           = "NONE"
	AlphaTransferIdentity       = "IDENTITY"
	AlphaTransferTable          = "TABLE"
	ShadingSingleSided          = "SINGLESIDED"
	ShadingDoubleSided          = "DOUBLESIDED"
)

var (
	tagCroppingSpecificationIndex            = core.NewTag(0x0070, 0x1205)
	tagRenderingMethod                       = core.NewTag(0x0070, 0x120D)
	tagGlobalCroppingSpecificationIndex      = core.NewTag(0x0070, 0x120C)
	tagVolumeCroppingSequence                = core.NewTag(0x0070, 0x1301)
	tagVolumeCroppingMethod                  = core.NewTag(0x0070, 0x1302)
	tagBoundingBoxCrop                       = core.NewTag(0x0070, 0x1303)
	tagObliqueCroppingPlaneSequence          = core.NewTag(0x0070, 0x1304)
	tagCroppingSpecificationNumber           = core.NewTag(0x0070, 0x1309)
	tagMPRStyle                              = core.NewTag(0x0070, 0x1501)
	tagMPRThicknessType                      = core.NewTag(0x0070, 0x1502)
	tagMPRSlabThickness                      = core.NewTag(0x0070, 0x1503)
	tagMPRTopLeft                            = core.NewTag(0x0070, 0x1505)
	tagMPRWidthDirection                     = core.NewTag(0x0070, 0x1507)
	tagMPRWidth                              = core.NewTag(0x0070, 0x1508)
	tagMPRHeightDirection                    = core.NewTag(0x0070, 0x1511)
	tagMPRHeight                             = core.NewTag(0x0070, 0x1512)
	tagRenderProjection                      = core.NewTag(0x0070, 0x1602)
	tagViewpointPosition                     = core.NewTag(0x0070, 0x1603)
	tagViewpointLookAt                       = core.NewTag(0x0070, 0x1604)
	tagViewpointUp                           = core.NewTag(0x0070, 0x1605)
	tagRenderFieldOfView                     = core.NewTag(0x0070, 0x1606)
	tagShadingStyle                          = core.NewTag(0x0070, 0x1701)
	tagAmbientReflection                     = core.NewTag(0x0070, 0x1702)
	tagLightDirection                        = core.NewTag(0x0070, 0x1703)
	tagDiffuseReflection                     = core.NewTag(0x0070, 0x1704)
	tagSpecularReflection                    = core.NewTag(0x0070, 0x1705)
	tagShininess                             = core.NewTag(0x0070, 0x1706)
	tagClassificationSequence                = core.NewTag(0x0070, 0x1801)
	tagComponentType                         = core.NewTag(0x0070, 0x1802)
	tagComponentInputSequence                = core.NewTag(0x0070, 0x1803)
	tagPresentationInputIndex                = core.NewTag(0x0070, 0x1804)
	tagCompositorSequence                    = core.NewTag(0x0070, 0x1805)
	tagVolumeStreamSequence                  = core.NewTag(0x0070, 0x1A08)
	tagRGBATransferFunctionDescription       = core.NewTag(0x0070, 0x1A09)
	tagWindowCenter                          = core.NewTag(0x0028, 0x1050)
	tagWindowWidth                           = core.NewTag(0x0028, 0x1051)
	tagVOILUTSequence                        = core.NewTag(0x0028, 0x3010)
	tagPixelPresentation                     = core.NewTag(0x0008, 0x9205)
	tagPresentationLUTShape                  = core.NewTag(0x2050, 0x0020)
	tagRGBTransferFunction                   = core.NewTag(0x0028, 0x140F)
	tagAlphaTransferFunction                 = core.NewTag(0x0028, 0x1410)
	tagRedPaletteDescriptor                  = core.NewTag(0x0028, 0x1101)
	tagGreenPaletteDescriptor                = core.NewTag(0x0028, 0x1102)
	tagBluePaletteDescriptor                 = core.NewTag(0x0028, 0x1103)
	tagRedPaletteData                        = core.NewTag(0x0028, 0x1201)
	tagGreenPaletteData                      = core.NewTag(0x0028, 0x1202)
	tagBluePaletteData                       = core.NewTag(0x0028, 0x1203)
	tagAlphaPaletteData                      = core.NewTag(0x0028, 0x1204)
	tagICCProfile                            = core.NewTag(0x0028, 0x2000)
	tagColorSpace                            = core.NewTag(0x0028, 0x2002)
	tagReferencedSpatialRegistrationSequence = core.NewTag(0x0070, 0x0404)
)

func hasStandardState(state *State) bool {
	return state != nil && (state.MPRGeometry != nil || state.VolumeRenderGeometry != nil)
}

func hasStandardGeometry(obj *object.Object, sopClassUID string) bool {
	if obj == nil {
		return false
	}
	if isMPRSOPClass(sopClassUID) {
		return obj.Has(tagMPRStyle) || obj.Has(tagMPRTopLeft) || obj.Has(tagMPRWidthDirection)
	}
	return obj.Has(tagRenderProjection) || obj.Has(tagViewpointPosition) || obj.Has(tagViewpointLookAt)
}

func isMPRSOPClass(uid string) bool {
	return uid == GrayscalePlanarMPRVolumetricPresentationStateStorage || uid == CompositingPlanarMPRVolumetricPresentationStateStorage
}

func writeStandard(state *State) (*object.File, error) {
	if err := validateStandardState(state); err != nil {
		return nil, err
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, state.SOPClassUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, state.SOPInstanceUID),
		derivedio.CS(derivedio.TagModality, "PR"),
		derivedio.UI(derivedio.TagStudyInstanceUID, state.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, state.SeriesInstanceUID),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, state.FrameOfReferenceUID),
		inputSetSequence(state.Inputs),
		standardInputSequence(state.Inputs),
	}
	if len(state.GlobalCropSpecificationNumbers) == 0 {
		elements = append(elements, derivedio.CS(tagGlobalCrop, "NO"))
	} else {
		elements = append(elements,
			derivedio.CS(tagGlobalCrop, "YES"),
			usInts(tagGlobalCroppingSpecificationIndex, state.GlobalCropSpecificationNumbers),
		)
	}
	if len(state.CroppingSpecifications) > 0 {
		elements = append(elements, croppingSequence(state.CroppingSpecifications))
	}
	if state.MPRGeometry != nil {
		elements = append(elements, encodeMPRGeometry(state.MPRGeometry)...)
	} else {
		elements = append(elements, encodeVolumeGeometry(state.VolumeRenderGeometry)...)
		elements = append(elements, derivedio.CS(tagRenderingMethod, state.RenderingMethod))
	}
	elements = append(elements, encodeDisplay(state.Display, state.SOPClassUID)...)
	if state.Shading != nil {
		elements = append(elements, encodeShading(state.Shading)...)
	}
	dataset := derivedio.Object(elements...)
	return derivedio.File(state.SOPClassUID, state.SOPInstanceUID, dataset)
}

func readStandard(obj *object.Object, sopClassUID string) (*State, error) {
	if obj.Has(tagReferencedSpatialRegistrationSequence) {
		return nil, unsupported("Referenced Spatial Registration Sequence")
	}
	state := &State{
		SOPClassUID:         sopClassUID,
		SOPInstanceUID:      derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:    derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID),
	}
	var err error
	state.Inputs, err = readStandardInputs(obj)
	if err != nil {
		return nil, err
	}
	state.GlobalCropSpecificationNumbers, err = readCropSelection(obj, tagGlobalCrop, tagGlobalCroppingSpecificationIndex, "Global Crop")
	if err != nil {
		return nil, err
	}
	state.CroppingSpecifications, err = readCroppingSpecifications(obj)
	if err != nil {
		return nil, err
	}
	if isMPRSOPClass(sopClassUID) {
		state.MPRGeometry, err = readMPRGeometry(obj)
	} else {
		state.VolumeRenderGeometry, err = readVolumeGeometry(obj)
		state.RenderingMethod = cleanCS(obj, tagRenderingMethod)
	}
	if err != nil {
		return nil, err
	}
	state.Display, err = readDisplay(obj, sopClassUID, state.Inputs)
	if err != nil {
		return nil, err
	}
	state.Shading, err = readShading(obj)
	if err != nil {
		return nil, err
	}
	if err := validateStandardStateForRead(state); err != nil {
		return nil, err
	}
	state.Camera = cameraFromVolumeGeometry(state.VolumeRenderGeometry)
	return state, nil
}

func validateStandardState(state *State) error {
	return validateStandardStateWithFrameRequirement(state, true)
}

func validateStandardStateForRead(state *State) error {
	return validateStandardStateWithFrameRequirement(state, false)
}

func validateStandardStateWithFrameRequirement(state *State, requireFrameOfReference bool) error {
	fail := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidObject, fmt.Sprintf(format, args...))
	}
	if state == nil {
		return fail("state is nil")
	}
	if strings.TrimSpace(state.SOPInstanceUID) == "" || strings.TrimSpace(state.StudyInstanceUID) == "" || strings.TrimSpace(state.SeriesInstanceUID) == "" {
		return fail("standard VPS is missing an SOP, Study, or Series Instance UID")
	}
	if requireFrameOfReference && strings.TrimSpace(state.FrameOfReferenceUID) == "" {
		return fail("standard VPS is missing Frame of Reference UID")
	}
	if isMPRSOPClass(state.SOPClassUID) {
		if state.MPRGeometry == nil || state.VolumeRenderGeometry != nil {
			return fail("SOP class %s requires exactly one PLANAR MPR geometry", state.SOPClassUID)
		}
		if err := validateMPRGeometry(state.MPRGeometry); err != nil {
			return err
		}
	} else {
		if state.VolumeRenderGeometry == nil || state.MPRGeometry != nil {
			return fail("SOP class %s requires exactly one volume render geometry", state.SOPClassUID)
		}
		if err := validateVolumeGeometry(state.VolumeRenderGeometry); err != nil {
			return err
		}
		if !oneOf(state.RenderingMethod, RenderingVolumeRendered, RenderingMaximumIP, RenderingMinimumIP) {
			return unsupported("Rendering Method %q", state.RenderingMethod)
		}
	}
	if len(state.Inputs) == 0 {
		return fail("Volumetric Presentation State Input Sequence is empty")
	}
	if len(state.Inputs) != 1 {
		return unsupported("%d presentation inputs; the supported standard subset requires exactly one", len(state.Inputs))
	}
	sets := map[string]bool{}
	inputNumbers := map[int]bool{}
	for i, input := range state.Inputs {
		if input.Number != i+1 || input.Number > math.MaxUint16 {
			return fail("input numbers must be contiguous from 1; item %d is %d", i+1, input.Number)
		}
		if strings.TrimSpace(input.InputSetUID) == "" {
			return fail("input %d is missing Input Set UID", input.Number)
		}
		sets[input.InputSetUID] = true
		inputNumbers[input.Number] = true
		if len(input.ReferencedInstances) == 0 {
			return fail("input set %s has no referenced instances", input.InputSetUID)
		}
		for _, ref := range input.ReferencedInstances {
			if strings.TrimSpace(ref.SOPClassUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
				return fail("input %d has an incomplete SOP instance reference", input.Number)
			}
		}
		if input.VOI != nil && (!finite(input.VOI.WindowCenter) || !finite(input.VOI.WindowWidth) || input.VOI.WindowWidth <= 0) {
			return fail("input %d has invalid Window Center/Width", input.Number)
		}
		if isMPRSOPClass(state.SOPClassUID) && state.MPRGeometry.ThicknessType == MPRThicknessSlab &&
			!oneOf(input.RenderingMethod, RenderingAverageIP, RenderingMaximumIP, RenderingMinimumIP) {
			return unsupported("input %d Rendering Method %q", input.Number, input.RenderingMethod)
		}
		if isMPRSOPClass(state.SOPClassUID) && state.MPRGeometry.ThicknessType == MPRThicknessThin && input.RenderingMethod != "" {
			return unsupported("Rendering Method on THIN MPR input %d", input.Number)
		}
		if !isMPRSOPClass(state.SOPClassUID) && input.RenderingMethod != "" {
			return unsupported("input-level Rendering Method on volume render input %d", input.Number)
		}
	}
	if err := validateCrop(state); err != nil {
		return err
	}
	if err := validateDisplay(state.Display, state.SOPClassUID, sets, inputNumbers); err != nil {
		return err
	}
	if state.Shading != nil {
		if !oneOf(state.Shading.Style, ShadingSingleSided, ShadingDoubleSided) {
			return unsupported("Shading Style %q", state.Shading.Style)
		}
		for name, value := range map[string]float64{"ambient": state.Shading.Ambient, "diffuse": state.Shading.Diffuse, "specular": state.Shading.Specular, "shininess": state.Shading.Shininess} {
			if !finite(value) || value < 0 || value > 1 {
				return fail("%s shading intensity %g is outside [0,1]", name, value)
			}
		}
		if (state.Shading.Diffuse > 0 || state.Shading.Specular > 0) && !unitVec(state.Shading.LightDirection) {
			return fail("Light Direction must be a unit vector when diffuse or specular shading is enabled")
		}
	}
	return nil
}

func validateMPRGeometry(g *MPRGeometry) error {
	if g == nil {
		return fmt.Errorf("%w: MPR geometry is nil", ErrInvalidObject)
	}
	if !oneOf(g.ThicknessType, MPRThicknessThin, MPRThicknessSlab) {
		return unsupported("MPR Thickness Type %q", g.ThicknessType)
	}
	if g.ThicknessType == MPRThicknessSlab && (!finite(g.SlabThickness) || g.SlabThickness <= 0) {
		return fmt.Errorf("%w: MPR slab thickness must be positive", ErrInvalidObject)
	}
	if !finiteVec(g.TopLeft) || !unitVec(g.WidthDirection) || !unitVec(g.HeightDirection) ||
		math.Abs(g.WidthDirection.Dot(g.HeightDirection)) > 1e-4 || !finite(g.Width) || g.Width <= 0 || !finite(g.Height) || g.Height <= 0 {
		return fmt.Errorf("%w: invalid MPR plane geometry", ErrInvalidObject)
	}
	return nil
}

func validateVolumeGeometry(g *VolumeRenderGeometry) error {
	if g == nil {
		return fmt.Errorf("%w: volume render geometry is nil", ErrInvalidObject)
	}
	if !oneOf(g.Projection, ProjectionOrthographic, ProjectionPerspective) {
		return unsupported("Render Projection %q", g.Projection)
	}
	forward := g.LookAt.Sub(g.Position)
	if !finiteVec(g.Position) || !finiteVec(g.LookAt) || !unitVec(g.Up) || forward.Length() <= 1e-9 || math.Abs(forward.Normalize().Dot(g.Up)) > 1-1e-6 {
		return fmt.Errorf("%w: invalid viewpoint position/look-at/up geometry", ErrInvalidObject)
	}
	for _, value := range g.FieldOfView {
		if !finite(value) {
			return fmt.Errorf("%w: Render Field of View contains a non-finite value", ErrInvalidObject)
		}
	}
	if !(g.FieldOfView[0] < g.FieldOfView[1] && g.FieldOfView[3] < g.FieldOfView[2] && g.FieldOfView[4] > 0 && g.FieldOfView[4] < g.FieldOfView[5]) {
		return fmt.Errorf("%w: invalid Render Field of View bounds", ErrInvalidObject)
	}
	return nil
}

func validateCrop(state *State) error {
	specs := map[int]bool{}
	for _, spec := range state.CroppingSpecifications {
		if spec.Number <= 0 || spec.Number > math.MaxUint16 || specs[spec.Number] {
			return fmt.Errorf("%w: invalid or duplicate cropping specification number %d", ErrInvalidObject, spec.Number)
		}
		specs[spec.Number] = true
		if spec.Method != CropBoundingBox {
			return unsupported("Volume Cropping Method %q", spec.Method)
		}
		b := spec.BoundingBox
		for _, value := range b {
			if !finite(value) {
				return fmt.Errorf("%w: crop %d contains a non-finite bound", ErrInvalidObject, spec.Number)
			}
		}
		if !(b[0] < b[1] && b[2] < b[3] && b[4] < b[5]) {
			return fmt.Errorf("%w: crop %d bounding box is not ordered", ErrInvalidObject, spec.Number)
		}
	}
	check := func(scope string, values []int) error {
		for _, number := range values {
			if !specs[number] {
				return fmt.Errorf("%w: %s references missing crop %d", ErrInvalidObject, scope, number)
			}
		}
		return nil
	}
	if err := check("Global Crop", state.GlobalCropSpecificationNumbers); err != nil {
		return err
	}
	for _, input := range state.Inputs {
		if err := check(fmt.Sprintf("input %d", input.Number), input.CropSpecificationNumbers); err != nil {
			return err
		}
	}
	return nil
}

func validateDisplay(display *Display, sopClassUID string, sets map[string]bool, inputNumbers map[int]bool) error {
	if display == nil {
		return fmt.Errorf("%w: standard VPS is missing its display module", ErrInvalidObject)
	}
	if sopClassUID == GrayscalePlanarMPRVolumetricPresentationStateStorage {
		if display.PixelPresentation != PixelPresentationMonochrome {
			return unsupported("grayscale MPR Pixel Presentation %q", display.PixelPresentation)
		}
		if !oneOf(display.PresentationLUTShape, PresentationLUTIdentity, PresentationLUTInverse) {
			return unsupported("Presentation LUT Shape %q", display.PresentationLUTShape)
		}
		if len(display.Classification) != 0 {
			return unsupported("classification in MONOCHROME MPR")
		}
		return nil
	}
	if display.PixelPresentation != PixelPresentationTrueColor {
		return unsupported("Pixel Presentation %q", display.PixelPresentation)
	}
	if len(display.ICCProfile) == 0 || strings.ToUpper(strings.TrimSpace(display.ColorSpace)) != "SRGB" {
		return unsupported("TRUE_COLOR display without an embedded sRGB ICC profile")
	}
	if !validRGBICCProfile(display.ICCProfile) {
		return fmt.Errorf("%w: embedded ICC profile is not an RGB profile", ErrInvalidObject)
	}
	if len(display.Classification) != 1 {
		return unsupported("%d classification components", len(display.Classification))
	}
	component := display.Classification[0]
	if len(component.InputNumbers) != 1 {
		return unsupported("classification component with %d inputs", len(component.InputNumbers))
	}
	if err := validateClassificationTransfer(component, isMPRSOPClass(sopClassUID)); err != nil {
		return err
	}
	if !inputNumbers[component.InputNumbers[0]] {
		return fmt.Errorf("%w: classification references unknown input %d", ErrInvalidObject, component.InputNumbers[0])
	}
	if !isMPRSOPClass(sopClassUID) && (display.InputSetUID == "" || !sets[display.InputSetUID]) {
		return fmt.Errorf("%w: volume stream references unknown input set %s", ErrInvalidObject, display.InputSetUID)
	}
	return nil
}

const maxClassificationPaletteEntries = 4096

func validateClassificationTransfer(component ClassificationComponent, mpr bool) error {
	hasRGBPalette := len(component.RedPalette) != 0 || len(component.GreenPalette) != 0 || len(component.BluePalette) != 0
	switch component.RGBTransferFunction {
	case RGBTransferEqual:
		if component.PaletteDescriptor != [3]uint16{} || hasRGBPalette {
			return fmt.Errorf("%w: EQUAL_RGB classification carries palette data", ErrInvalidObject)
		}
	case RGBTransferTable:
		count, err := classificationPaletteCount(component.PaletteDescriptor)
		if err != nil {
			return err
		}
		if len(component.RedPalette) != count || len(component.GreenPalette) != count || len(component.BluePalette) != count {
			return fmt.Errorf("%w: RGB palette lengths %d/%d/%d do not match descriptor count %d", ErrInvalidObject, len(component.RedPalette), len(component.GreenPalette), len(component.BluePalette), count)
		}
	default:
		return unsupported("RGB LUT Transfer Function %q", component.RGBTransferFunction)
	}

	switch component.AlphaTransferFunction {
	case "":
		if !mpr {
			return fmt.Errorf("%w: volume classification is missing Alpha LUT Transfer Function", ErrInvalidObject)
		}
	case AlphaTransferNone, AlphaTransferIdentity:
		if len(component.AlphaPalette) != 0 {
			return fmt.Errorf("%w: non-TABLE alpha classification carries palette data", ErrInvalidObject)
		}
	case AlphaTransferTable:
		if component.RGBTransferFunction != RGBTransferTable {
			return unsupported("TABLE alpha without a TABLE RGB descriptor")
		}
		count, err := classificationPaletteCount(component.PaletteDescriptor)
		if err != nil {
			return err
		}
		if len(component.AlphaPalette) != count {
			return fmt.Errorf("%w: alpha palette length %d does not match descriptor count %d", ErrInvalidObject, len(component.AlphaPalette), count)
		}
	default:
		return unsupported("Alpha LUT Transfer Function %q", component.AlphaTransferFunction)
	}
	return nil
}

func classificationPaletteCount(descriptor [3]uint16) (int, error) {
	count := int(descriptor[0])
	if count == 0 {
		return 0, unsupported("65536-entry classification palette")
	}
	if count < 2 || count > maxClassificationPaletteEntries || descriptor[1] != 0 {
		return 0, fmt.Errorf("%w: invalid classification palette descriptor %v", ErrInvalidObject, descriptor)
	}
	if descriptor[2] == 8 {
		return 0, unsupported("8-bit classification palette")
	}
	if descriptor[2] != 16 {
		return 0, fmt.Errorf("%w: invalid classification palette descriptor %v", ErrInvalidObject, descriptor)
	}
	return count, nil
}

func standardInputSequence(inputs []Input) core.Element {
	items := make([]core.DataSet, 0, len(inputs))
	for _, input := range inputs {
		elements := []core.Element{
			derivedio.US(tagVPSInputNumber, uint16(input.Number)),
			derivedio.UI(tagVPSInputSetUID, input.InputSetUID),
		}
		if len(input.CropSpecificationNumbers) == 0 {
			elements = append(elements, derivedio.CS(tagCrop, "NO"))
		} else {
			elements = append(elements, derivedio.CS(tagCrop, "YES"), usInts(tagCroppingSpecificationIndex, input.CropSpecificationNumbers))
		}
		if input.VOI != nil {
			elements = append(elements,
				derivedio.DS(tagWindowCenter, input.VOI.WindowCenter),
				derivedio.DS(tagWindowWidth, input.VOI.WindowWidth),
			)
		}
		if input.RenderingMethod != "" {
			elements = append(elements, derivedio.CS(tagRenderingMethod, input.RenderingMethod))
		}
		items = append(items, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagVPSInputSequence, items...)
}

func croppingSequence(specs []CroppingSpecification) core.Element {
	items := make([]core.DataSet, 0, len(specs))
	for _, spec := range specs {
		items = append(items, derivedio.DataSet(
			derivedio.US(tagCroppingSpecificationNumber, uint16(spec.Number)),
			derivedio.CS(tagVolumeCroppingMethod, spec.Method),
			derivedio.FD(tagBoundingBoxCrop, spec.BoundingBox[:]...),
		))
	}
	return derivedio.Seq(tagVolumeCroppingSequence, items...)
}

func encodeMPRGeometry(g *MPRGeometry) []core.Element {
	elements := []core.Element{
		derivedio.CS(tagMPRStyle, "PLANAR"),
		derivedio.CS(tagMPRThicknessType, g.ThicknessType),
		derivedio.FD(tagMPRTopLeft, vecValues(g.TopLeft)...),
		derivedio.FD(tagMPRWidthDirection, vecValues(g.WidthDirection)...),
		derivedio.FD(tagMPRWidth, g.Width),
		derivedio.FD(tagMPRHeightDirection, vecValues(g.HeightDirection)...),
		derivedio.FD(tagMPRHeight, g.Height),
	}
	if g.ThicknessType == MPRThicknessSlab {
		elements = append(elements, derivedio.FD(tagMPRSlabThickness, g.SlabThickness))
	}
	return elements
}

func encodeVolumeGeometry(g *VolumeRenderGeometry) []core.Element {
	return []core.Element{
		derivedio.CS(tagRenderProjection, g.Projection),
		derivedio.FD(tagViewpointPosition, vecValues(g.Position)...),
		derivedio.FD(tagViewpointLookAt, vecValues(g.LookAt)...),
		derivedio.FD(tagViewpointUp, vecValues(g.Up)...),
		derivedio.FD(tagRenderFieldOfView, g.FieldOfView[:]...),
	}
}

func encodeDisplay(display *Display, sopClassUID string) []core.Element {
	elements := []core.Element{derivedio.CS(tagPixelPresentation, display.PixelPresentation)}
	if sopClassUID == GrayscalePlanarMPRVolumetricPresentationStateStorage {
		return append(elements, derivedio.CS(tagPresentationLUTShape, display.PresentationLUTShape))
	}
	componentItems := make([]core.DataSet, 0, len(display.Classification))
	for _, component := range display.Classification {
		inputItems := make([]core.DataSet, 0, len(component.InputNumbers))
		for _, input := range component.InputNumbers {
			inputItems = append(inputItems, derivedio.DataSet(derivedio.US(tagPresentationInputIndex, uint16(input))))
		}
		componentElements := []core.Element{
			derivedio.CS(tagComponentType, "ONE_TO_RGBA"),
			derivedio.Seq(tagComponentInputSequence, inputItems...),
			derivedio.CS(tagRGBTransferFunction, component.RGBTransferFunction),
		}
		if component.Description != "" {
			componentElements = append(componentElements, derivedio.LO(tagRGBATransferFunctionDescription, component.Description))
		}
		if component.AlphaTransferFunction != "" {
			componentElements = append(componentElements, derivedio.CS(tagAlphaTransferFunction, component.AlphaTransferFunction))
		}
		if component.RGBTransferFunction == RGBTransferTable {
			descriptor := component.PaletteDescriptor[:]
			componentElements = append(componentElements,
				derivedio.US(tagRedPaletteDescriptor, descriptor...),
				derivedio.US(tagGreenPaletteDescriptor, descriptor...),
				derivedio.US(tagBluePaletteDescriptor, descriptor...),
				wordPalette(tagRedPaletteData, component.RedPalette),
				wordPalette(tagGreenPaletteData, component.GreenPalette),
				wordPalette(tagBluePaletteData, component.BluePalette),
			)
		}
		if component.AlphaTransferFunction == AlphaTransferTable {
			componentElements = append(componentElements,
				wordPalette(tagAlphaPaletteData, component.AlphaPalette),
			)
		}
		componentItems = append(componentItems, derivedio.DataSet(componentElements...))
	}
	classification := derivedio.Seq(tagClassificationSequence, componentItems...)
	if isMPRSOPClass(sopClassUID) {
		elements = append(elements, classification)
	} else {
		elements = append(elements, derivedio.Seq(tagVolumeStreamSequence, derivedio.DataSet(
			derivedio.UI(tagVPSInputSetUID, display.InputSetUID),
			classification,
		)))
	}
	elements = append(elements,
		derivedio.Raw(tagICCProfile, core.VROB, append([]byte(nil), display.ICCProfile...)),
		derivedio.CS(tagColorSpace, display.ColorSpace),
	)
	return elements
}

func wordPalette(tag core.Tag, values []uint16) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VROW},
		Value:  core.Uint16Value(append([]uint16(nil), values...)),
	}
}

func encodeShading(s *Shading) []core.Element {
	elements := []core.Element{
		derivedio.CS(tagShadingStyle, s.Style),
		derivedio.FD(tagAmbientReflection, s.Ambient),
	}
	if s.Diffuse > 0 || s.Specular > 0 {
		elements = append(elements, derivedio.FD(tagLightDirection, vecValues(s.LightDirection)...))
	}
	if s.Diffuse > 0 {
		elements = append(elements, derivedio.FD(tagDiffuseReflection, s.Diffuse))
	}
	if s.Specular > 0 {
		elements = append(elements, derivedio.FD(tagSpecularReflection, s.Specular))
	}
	if s.Shininess > 0 {
		elements = append(elements, derivedio.FD(tagShininess, s.Shininess))
	}
	return elements
}

func readStandardInputs(obj *object.Object) ([]Input, error) {
	sets := map[string][]ReferencedInstance{}
	setItems := derivedio.Sequence(obj, tagVPSInputSetSequence)
	if len(setItems) == 0 {
		return nil, fmt.Errorf("%w: Volumetric Presentation Input Set Sequence is missing or empty", ErrInvalidObject)
	}
	for index, set := range setItems {
		uid := derivedio.CleanUID(set, tagVPSInputSetUID)
		if uid == "" || sets[uid] != nil {
			return nil, fmt.Errorf("%w: input set item %d has missing or duplicate UID", ErrInvalidObject, index+1)
		}
		if cleanCS(set, tagPresentationInputType) != "VOLUME" {
			return nil, unsupported("Presentation Input Type %q", cleanCS(set, tagPresentationInputType))
		}
		if set.Has(tagReferencedSpatialRegistrationSequence) {
			return nil, unsupported("Referenced Spatial Registration Sequence in input set %s", uid)
		}
		refs := derivedio.Sequence(set, tagReferencedImageSequence)
		if len(refs) == 0 {
			return nil, fmt.Errorf("%w: input set %s has no Referenced Image Sequence", ErrInvalidObject, uid)
		}
		for _, ref := range refs {
			sets[uid] = append(sets[uid], ReferencedInstance{
				SOPClassUID:    derivedio.CleanUID(ref, derivedio.TagRefSOPClassUID),
				SOPInstanceUID: derivedio.CleanUID(ref, derivedio.TagRefSOPInstanceUID),
			})
		}
	}
	items := derivedio.Sequence(obj, tagVPSInputSequence)
	if len(items) == 0 {
		return nil, fmt.Errorf("%w: Volumetric Presentation State Input Sequence is missing or empty", ErrInvalidObject)
	}
	out := make([]Input, 0, len(items))
	for _, item := range items {
		input := Input{Number: derivedio.Int(item, tagVPSInputNumber), InputSetUID: derivedio.CleanUID(item, tagVPSInputSetUID)}
		input.ReferencedInstances = append(input.ReferencedInstances, sets[input.InputSetUID]...)
		var err error
		input.CropSpecificationNumbers, err = readCropSelection(item, tagCrop, tagCroppingSpecificationIndex, fmt.Sprintf("input %d Crop", input.Number))
		if err != nil {
			return nil, err
		}
		if item.Has(tagVOILUTSequence) {
			return nil, unsupported("VOI LUT Sequence on input %d", input.Number)
		}
		centers := derivedio.Floats(item, tagWindowCenter)
		widths := derivedio.Floats(item, tagWindowWidth)
		if len(centers) > 0 || len(widths) > 0 {
			if len(centers) != 1 || len(widths) != 1 {
				return nil, unsupported("multi-valued or incomplete Window Center/Width on input %d", input.Number)
			}
			input.VOI = &VOI{WindowCenter: centers[0], WindowWidth: widths[0]}
		}
		input.RenderingMethod = cleanCS(item, tagRenderingMethod)
		out = append(out, input)
	}
	return out, nil
}

func readCropSelection(obj *object.Object, flagTag, indexTag core.Tag, label string) ([]int, error) {
	flag := cleanCS(obj, flagTag)
	switch flag {
	case "NO":
		if obj.Has(indexTag) {
			return nil, fmt.Errorf("%w: %s is NO but has crop indexes", ErrInvalidObject, label)
		}
		return nil, nil
	case "YES":
		values := derivedio.Ints(obj, indexTag)
		if len(values) == 0 {
			return nil, fmt.Errorf("%w: %s is YES without crop indexes", ErrInvalidObject, label)
		}
		out := make([]int, len(values))
		for i, value := range values {
			out[i] = int(value)
		}
		return out, nil
	default:
		return nil, fmt.Errorf("%w: %s must be YES or NO", ErrInvalidObject, label)
	}
}

func readCroppingSpecifications(obj *object.Object) ([]CroppingSpecification, error) {
	items := derivedio.Sequence(obj, tagVolumeCroppingSequence)
	out := make([]CroppingSpecification, 0, len(items))
	for _, item := range items {
		method := cleanCS(item, tagVolumeCroppingMethod)
		if method != CropBoundingBox {
			if item.Has(tagObliqueCroppingPlaneSequence) {
				return nil, unsupported("Volume Cropping Method %q with oblique planes", method)
			}
			return nil, unsupported("Volume Cropping Method %q", method)
		}
		values := derivedio.Floats(item, tagBoundingBoxCrop)
		if len(values) != 6 {
			return nil, fmt.Errorf("%w: BOUNDING_BOX crop must contain six values", ErrInvalidObject)
		}
		spec := CroppingSpecification{Number: derivedio.Int(item, tagCroppingSpecificationNumber), Method: method}
		copy(spec.BoundingBox[:], values)
		out = append(out, spec)
	}
	return out, nil
}

func readMPRGeometry(obj *object.Object) (*MPRGeometry, error) {
	if cleanCS(obj, tagMPRStyle) != "PLANAR" {
		return nil, unsupported("Multi-Planar Reconstruction Style %q", cleanCS(obj, tagMPRStyle))
	}
	g := &MPRGeometry{ThicknessType: cleanCS(obj, tagMPRThicknessType)}
	var err error
	if g.TopLeft, err = requiredVec(obj, tagMPRTopLeft, "MPR Top Left Hand Corner"); err != nil {
		return nil, err
	}
	if g.WidthDirection, err = requiredVec(obj, tagMPRWidthDirection, "MPR View Width Direction"); err != nil {
		return nil, err
	}
	if g.Width, err = requiredScalar(obj, tagMPRWidth, "MPR View Width"); err != nil {
		return nil, err
	}
	if g.HeightDirection, err = requiredVec(obj, tagMPRHeightDirection, "MPR View Height Direction"); err != nil {
		return nil, err
	}
	if g.Height, err = requiredScalar(obj, tagMPRHeight, "MPR View Height"); err != nil {
		return nil, err
	}
	if g.ThicknessType == MPRThicknessSlab {
		if g.SlabThickness, err = requiredScalar(obj, tagMPRSlabThickness, "MPR Slab Thickness"); err != nil {
			return nil, err
		}
	}
	return g, nil
}

func readVolumeGeometry(obj *object.Object) (*VolumeRenderGeometry, error) {
	g := &VolumeRenderGeometry{Projection: cleanCS(obj, tagRenderProjection)}
	var err error
	if g.Position, err = requiredVec(obj, tagViewpointPosition, "Viewpoint Position"); err != nil {
		return nil, err
	}
	if g.LookAt, err = requiredVec(obj, tagViewpointLookAt, "Viewpoint LookAt Point"); err != nil {
		return nil, err
	}
	if g.Up, err = requiredVec(obj, tagViewpointUp, "Viewpoint Up Direction"); err != nil {
		return nil, err
	}
	values := derivedio.Floats(obj, tagRenderFieldOfView)
	if len(values) != 6 {
		return nil, fmt.Errorf("%w: Render Field of View must contain six values", ErrInvalidObject)
	}
	copy(g.FieldOfView[:], values)
	return g, nil
}

func readDisplay(obj *object.Object, sopClassUID string, inputs []Input) (*Display, error) {
	display := &Display{PixelPresentation: cleanCS(obj, tagPixelPresentation)}
	if sopClassUID == GrayscalePlanarMPRVolumetricPresentationStateStorage {
		display.PresentationLUTShape = cleanCS(obj, tagPresentationLUTShape)
		return display, nil
	}
	var classificationOwner *object.Object = obj
	if !isMPRSOPClass(sopClassUID) {
		streams := derivedio.Sequence(obj, tagVolumeStreamSequence)
		if len(streams) != 1 {
			return nil, unsupported("%d Volume Stream Sequence items", len(streams))
		}
		classificationOwner = streams[0]
		display.InputSetUID = derivedio.CleanUID(streams[0], tagVPSInputSetUID)
	}
	if obj.Has(tagCompositorSequence) {
		return nil, unsupported("Presentation State Compositor Component Sequence")
	}
	components := derivedio.Sequence(classificationOwner, tagClassificationSequence)
	for _, item := range components {
		if cleanCS(item, tagComponentType) != "ONE_TO_RGBA" {
			return nil, unsupported("classification Component Type %q", cleanCS(item, tagComponentType))
		}
		component := ClassificationComponent{
			RGBTransferFunction:   cleanCS(item, tagRGBTransferFunction),
			AlphaTransferFunction: cleanCS(item, tagAlphaTransferFunction),
			Description:           derivedio.CleanString(item, tagRGBATransferFunctionDescription),
		}
		for _, input := range derivedio.Sequence(item, tagComponentInputSequence) {
			component.InputNumbers = append(component.InputNumbers, derivedio.Int(input, tagPresentationInputIndex))
		}
		if component.RGBTransferFunction == RGBTransferTable {
			var err error
			component.PaletteDescriptor, err = readClassificationPaletteDescriptor(item)
			if err != nil {
				return nil, err
			}
			if component.RedPalette, err = readClassificationPalette(item, tagRedPaletteData, "red"); err != nil {
				return nil, err
			}
			if component.GreenPalette, err = readClassificationPalette(item, tagGreenPaletteData, "green"); err != nil {
				return nil, err
			}
			if component.BluePalette, err = readClassificationPalette(item, tagBluePaletteData, "blue"); err != nil {
				return nil, err
			}
		}
		if component.AlphaTransferFunction == AlphaTransferTable {
			var err error
			component.AlphaPalette, err = readClassificationPalette(item, tagAlphaPaletteData, "alpha")
			if err != nil {
				return nil, err
			}
		}
		display.Classification = append(display.Classification, component)
	}
	if element, ok := obj.Get(tagICCProfile); ok {
		if raw, ok := element.RawBytes(); ok {
			display.ICCProfile = append([]byte(nil), raw...)
		}
	}
	display.ColorSpace = cleanCS(obj, tagColorSpace)
	_ = inputs
	return display, nil
}

func readClassificationPaletteDescriptor(obj *object.Object) ([3]uint16, error) {
	red := derivedio.Ints(obj, tagRedPaletteDescriptor)
	green := derivedio.Ints(obj, tagGreenPaletteDescriptor)
	blue := derivedio.Ints(obj, tagBluePaletteDescriptor)
	if len(red) != 3 || len(green) != 3 || len(blue) != 3 {
		return [3]uint16{}, fmt.Errorf("%w: TABLE classification requires three RGB palette descriptors", ErrInvalidObject)
	}
	var descriptor [3]uint16
	for index := range descriptor {
		if red[index] < 0 || red[index] > math.MaxUint16 || green[index] != red[index] || blue[index] != red[index] {
			return [3]uint16{}, fmt.Errorf("%w: RGB palette descriptors differ or overflow", ErrInvalidObject)
		}
		descriptor[index] = uint16(red[index])
	}
	return descriptor, nil
}

func readClassificationPalette(obj *object.Object, tag core.Tag, name string) ([]uint16, error) {
	element, ok := obj.Get(tag)
	if !ok {
		return nil, fmt.Errorf("%w: TABLE classification is missing %s palette data", ErrInvalidObject, name)
	}
	if values, ok := element.Value.(core.Uint16Value); ok {
		return append([]uint16(nil), values...), nil
	}
	raw, ok := element.RawBytes()
	if !ok || len(raw)%2 != 0 {
		return nil, fmt.Errorf("%w: %s palette data is not 16-bit OW", ErrInvalidObject, name)
	}
	values := make([]uint16, len(raw)/2)
	order := obj.ValueByteOrder()
	for index := range values {
		values[index] = order.Uint16(raw[index*2:])
	}
	return values, nil
}

func readShading(obj *object.Object) (*Shading, error) {
	if !obj.Has(tagShadingStyle) {
		if obj.Has(tagAmbientReflection) || obj.Has(tagLightDirection) || obj.Has(tagDiffuseReflection) || obj.Has(tagSpecularReflection) || obj.Has(tagShininess) {
			return nil, fmt.Errorf("%w: incomplete Render Shading module", ErrInvalidObject)
		}
		return nil, nil
	}
	s := &Shading{Style: cleanCS(obj, tagShadingStyle)}
	var err error
	if s.Ambient, err = requiredScalar(obj, tagAmbientReflection, "Ambient Reflection Intensity"); err != nil {
		return nil, err
	}
	var hasDiffuse, hasSpecular bool
	if s.Diffuse, hasDiffuse, err = optionalScalar(obj, tagDiffuseReflection, "Diffuse Reflection Intensity"); err != nil {
		return nil, err
	}
	if s.Specular, hasSpecular, err = optionalScalar(obj, tagSpecularReflection, "Specular Reflection Intensity"); err != nil {
		return nil, err
	}
	if s.Shininess, _, err = optionalScalar(obj, tagShininess, "Shininess"); err != nil {
		return nil, err
	}
	if hasDiffuse || hasSpecular {
		if s.LightDirection, err = requiredVec(obj, tagLightDirection, "Light Direction"); err != nil {
			return nil, err
		}
	}
	return s, nil
}

func appliedPreset(state *State) (render.VRPreset, error) {
	if state == nil {
		return render.VRPreset{}, fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	if !hasStandardState(state) {
		preset, ok := render.VRPresetByName(presetName(state.RenderPresetName))
		if !ok {
			preset = render.DefaultVRPreset()
		}
		return preset, nil
	}
	method := state.RenderingMethod
	if state.MPRGeometry != nil && len(state.Inputs) > 0 {
		method = state.Inputs[0].RenderingMethod
	}
	preset := render.DefaultVRPreset()
	preset.Name = "DICOM VPS"
	if state.Display != nil && len(state.Display.Classification) == 1 {
		transfer, err := classificationTransferFunction(state.Display.Classification[0], isMPRSOPClass(state.SOPClassUID))
		if err != nil {
			return render.VRPreset{}, err
		}
		preset.TF = transfer
		if description := strings.TrimSpace(state.Display.Classification[0].Description); description != "" {
			preset.Name = description
		}
		preset.GradientOpacityScale = 0
	}
	switch method {
	case "", RenderingVolumeRendered:
		preset.Mode = render.VRModeDVR
	case RenderingMaximumIP:
		preset.Mode = render.VRModeMIP
	case RenderingMinimumIP:
		preset.Mode = render.VRModeMinIP
	case RenderingAverageIP:
		preset.Mode = render.VRModeAverage
	default:
		return render.VRPreset{}, unsupported("Rendering Method %q", method)
	}
	if state.Display != nil && state.Display.PresentationLUTShape == PresentationLUTInverse {
		preset.Inverse = true
	}
	if state.Shading != nil {
		preset.ShadingDefault = true
	}
	return preset, nil
}

func classificationTransferFunction(component ClassificationComponent, mpr bool) (render.VRTransferFunction, error) {
	if err := validateClassificationTransfer(component, mpr); err != nil {
		return render.VRTransferFunction{}, err
	}
	count := 2
	if component.RGBTransferFunction == RGBTransferTable {
		count = int(component.PaletteDescriptor[0])
	}
	denominator := float64(uint16(0xffff))
	colors := make([]render.TFColorStop, count)
	alpha := make([]render.TFAlphaStop, count)
	for index := 0; index < count; index++ {
		position := float64(index) / float64(count-1)
		colors[index].HU = position
		alpha[index].HU = position
		if component.RGBTransferFunction == RGBTransferTable {
			colors[index].R = float64(component.RedPalette[index]) / denominator
			colors[index].G = float64(component.GreenPalette[index]) / denominator
			colors[index].B = float64(component.BluePalette[index]) / denominator
		} else {
			colors[index].R, colors[index].G, colors[index].B = position, position, position
		}
		switch component.AlphaTransferFunction {
		case "", AlphaTransferNone:
			alpha[index].A = 1
		case AlphaTransferIdentity:
			alpha[index].A = position
		case AlphaTransferTable:
			alpha[index].A = float64(component.AlphaPalette[index]) / denominator
		}
	}
	return render.NewNormalizedVRTransferFunction(colors, alpha, render.DefaultVROpacityUnitDistanceMM), nil
}

func cameraFromVolumeGeometry(g *VolumeRenderGeometry) Camera {
	if g == nil {
		return Camera{}
	}
	forward := g.LookAt.Sub(g.Position).Normalize()
	up := g.Up.Normalize()
	baseRight := forward.Cross(render.Vec3{Z: 1})
	if baseRight.Length() < 1e-6 {
		baseRight = render.Vec3{X: 1}
	}
	baseRight = baseRight.Normalize()
	baseUp := baseRight.Cross(forward).Normalize()
	roll := math.Atan2(baseRight.Dot(up), baseUp.Dot(up))
	vertical := g.FieldOfView[2] - g.FieldOfView[3]
	fov := 0.0
	if g.Projection == ProjectionPerspective && g.FieldOfView[5] > 0 {
		fov = 2 * math.Atan(vertical/(2*g.FieldOfView[5]))
	}
	return Camera{
		Target:   g.LookAt,
		Yaw:      math.Atan2(forward.X, forward.Y),
		Pitch:    math.Asin(math.Max(-1, math.Min(1, forward.Z))),
		Roll:     roll,
		Distance: g.LookAt.Sub(g.Position).Length(),
		FovY:     fov,
	}
}

func requiredVec(obj *object.Object, tag core.Tag, name string) (render.Vec3, error) {
	values := derivedio.Floats(obj, tag)
	if len(values) != 3 {
		return render.Vec3{}, fmt.Errorf("%w: %s must contain three values", ErrInvalidObject, name)
	}
	return render.Vec3{X: values[0], Y: values[1], Z: values[2]}, nil
}

func requiredScalar(obj *object.Object, tag core.Tag, name string) (float64, error) {
	values := derivedio.Floats(obj, tag)
	if len(values) != 1 {
		return 0, fmt.Errorf("%w: %s must contain one value", ErrInvalidObject, name)
	}
	return values[0], nil
}

func optionalScalar(obj *object.Object, tag core.Tag, name string) (float64, bool, error) {
	if !obj.Has(tag) {
		return 0, false, nil
	}
	values, err := derivedio.LookupFloats(obj, tag)
	if err != nil || len(values) != 1 {
		return 0, true, fmt.Errorf("%w: %s must contain one numeric value", ErrInvalidObject, name)
	}
	return values[0], true, nil
}

func usInts(tag core.Tag, values []int) core.Element {
	out := make([]uint16, len(values))
	for i, value := range values {
		out[i] = uint16(value)
	}
	return derivedio.US(tag, out...)
}

func vecValues(v render.Vec3) []float64 { return []float64{v.X, v.Y, v.Z} }
func cleanCS(obj *object.Object, tag core.Tag) string {
	return strings.ToUpper(strings.TrimSpace(derivedio.CleanString(obj, tag)))
}
func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}
func finite(value float64) bool    { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func finiteVec(v render.Vec3) bool { return finite(v.X) && finite(v.Y) && finite(v.Z) }
func unitVec(v render.Vec3) bool   { return finiteVec(v) && math.Abs(v.Length()-1) <= 1e-4 }
func validRGBICCProfile(profile []byte) bool {
	return len(profile) >= 128 && string(profile[16:20]) == "RGB " && string(profile[36:40]) == "acsp"
}
func unsupported(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedPayload, fmt.Sprintf(format, args...))
}

func cloneMPRGeometry(value *MPRGeometry) *MPRGeometry {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
func cloneVolumeRenderGeometry(value *VolumeRenderGeometry) *VolumeRenderGeometry {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
func cloneShading(value *Shading) *Shading {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
