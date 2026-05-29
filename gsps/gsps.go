package gsps

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

const (
	GrayscaleSoftcopyPresentationStateStorage = "1.2.840.10008.5.1.4.1.1.11.1"
	PresentationLUTIdentity                   = "IDENTITY"
	PresentationLUTInverse                    = "INVERSE"

	GraphicTypePoint        = "POINT"
	GraphicTypePolyline     = "POLYLINE"
	GraphicTypeCircle       = "CIRCLE"
	GraphicTypeEllipse      = "ELLIPSE"
	GraphicTypeInterpolated = "INTERPOLATED"

	AnnotationUnitsPixel   = "PIXEL"
	AnnotationUnitsDisplay = "DISPLAY"

	PresentationSizeScaleToFit = "SCALE TO FIT"
	PresentationSizeTrueSize   = "TRUE SIZE"
	PresentationSizeMagnify    = "MAGNIFY"
)

var (
	ErrUnsupportedSOPClass = errors.New("dicom/gsps: unsupported SOP class")
	ErrUnsupportedUnits    = errors.New("dicom/gsps: unsupported annotation units")
	ErrInvalidObject       = errors.New("dicom/gsps: invalid object")
	ErrMissingReference    = errors.New("dicom/gsps: missing reference")
)

var (
	tagReferencedSeriesSequence     = core.NewTag(0x0008, 0x1115)
	tagReferencedImageSequence      = core.NewTag(0x0008, 0x1140)
	tagDisplayedAreaSelectionSeq    = core.NewTag(0x0070, 0x005A)
	tagDisplayedAreaTopLeft         = core.NewTag(0x0070, 0x0052)
	tagDisplayedAreaBottomRight     = core.NewTag(0x0070, 0x0053)
	tagPresentationSizeMode         = core.NewTag(0x0070, 0x0100)
	tagPresentationPixelSpacing     = core.NewTag(0x0070, 0x0101)
	tagPresentationPixelAspectRatio = core.NewTag(0x0070, 0x0102)
	tagPresentationPixelMagnify     = core.NewTag(0x0070, 0x0103)
	tagImageRotation                = core.NewTag(0x0070, 0x0042)
	tagImageHorizontalFlip          = core.NewTag(0x0070, 0x0041)
	tagShutterShape                 = core.NewTag(0x0018, 0x1600)
	tagShutterLeft                  = core.NewTag(0x0018, 0x1602)
	tagShutterRight                 = core.NewTag(0x0018, 0x1604)
	tagShutterUpper                 = core.NewTag(0x0018, 0x1606)
	tagShutterLower                 = core.NewTag(0x0018, 0x1608)
	tagShutterCircleCenter          = core.NewTag(0x0018, 0x1610)
	tagShutterCircleRadius          = core.NewTag(0x0018, 0x1612)
	tagShutterPolygonVertices       = core.NewTag(0x0018, 0x1620)
	tagShutterPresentationValue     = core.NewTag(0x0018, 0x1622)
	tagShutterOverlayGroup          = core.NewTag(0x0018, 0x1623)
	tagShutterPresentationColor     = core.NewTag(0x0018, 0x1624)
	tagSoftcopyVOILUTSequence       = core.NewTag(0x0028, 0x3110)
	tagWindowCenter                 = core.NewTag(0x0028, 0x1050)
	tagWindowWidth                  = core.NewTag(0x0028, 0x1051)
	tagWindowCenterWidthExplanation = core.NewTag(0x0028, 0x1055)
	tagVOILUTFunction               = core.NewTag(0x0028, 0x1056)
	tagVOILUTSequence               = core.NewTag(0x0028, 0x3010)
	tagLUTDescriptor                = core.NewTag(0x0028, 0x3002)
	tagLUTExplanation               = core.NewTag(0x0028, 0x3003)
	tagLUTData                      = core.NewTag(0x0028, 0x3006)
	tagPresentationLUTShape         = core.NewTag(0x2050, 0x0020)
	tagGraphicAnnotationSequence    = core.NewTag(0x0070, 0x0001)
	tagGraphicLayer                 = core.NewTag(0x0070, 0x0002)
	tagAnchorPointAnnotationUnits   = core.NewTag(0x0070, 0x0004)
	tagGraphicAnnotationUnits       = core.NewTag(0x0070, 0x0005)
	tagTextObjectSequence           = core.NewTag(0x0070, 0x0008)
	tagUnformattedTextValue         = core.NewTag(0x0070, 0x0006)
	tagAnchorPoint                  = core.NewTag(0x0070, 0x0014)
	tagAnchorPointVisibility        = core.NewTag(0x0070, 0x0015)
	tagGraphicObjectSequence        = core.NewTag(0x0070, 0x0009)
	tagGraphicDimensions            = core.NewTag(0x0070, 0x0020)
	tagGraphicData                  = core.NewTag(0x0070, 0x0022)
	tagGraphicType                  = core.NewTag(0x0070, 0x0023)
	tagGraphicFilled                = core.NewTag(0x0070, 0x0024)
	tagNumberOfGraphicPoints        = core.NewTag(0x0070, 0x0021)
	tagGraphicLayerSequence         = core.NewTag(0x0070, 0x0060)
	tagGraphicLayerOrder            = core.NewTag(0x0070, 0x0062)
)

type ReferencedImage struct {
	SeriesInstanceUID string
	SOPClassUID       string
	SOPInstanceUID    string
	Frames            []int
}

type DisplayedArea struct {
	// Defined distinguishes an explicitly encoded all-zero area from an absent
	// Displayed Area Selection Sequence. Non-zero coordinates imply presence for
	// backward compatibility with existing callers.
	Defined      bool
	TopLeftX     int
	TopLeftY     int
	BottomRightX int
	BottomRightY int
	// PresentationSizeMode is SCALE TO FIT, TRUE SIZE, or MAGNIFY. Empty means
	// SCALE TO FIT for backward compatibility.
	PresentationSizeMode string
	PixelSpacing         []float64
	PixelAspectRatio     []int
	MagnificationRatio   float64
	ReferencedImages     []ReferencedImage
}

type SpatialTransform struct {
	RotationDegrees int
	FlipHorizontal  bool
}

type SoftcopyVOI struct {
	WindowCenter     float64
	WindowWidth      float64
	Explanation      string
	Function         display.VOIFunction
	LUT              *display.LUT
	ReferencedImages []ReferencedImage
}

type Point2D struct {
	X float64
	Y float64
}

type GraphicAnnotation struct {
	LayerName string
	// GroupIndex identifies objects that came from the same Graphic Annotation
	// Sequence item. A zero value keeps programmatically-created annotations as
	// independent items for backward compatibility.
	GroupIndex   int
	Text         string
	Anchor       Point2D
	AnchorUnits  string
	GraphicType  string
	GraphicUnits string
	Polyline     []Point2D
	// ReferencedImages scopes this Graphic Annotation Sequence item to images
	// or frames. Empty means the annotation applies to every referenced image.
	ReferencedImages []ReferencedImage
}

type State struct {
	SOPClassUID          string
	SOPInstanceUID       string
	StudyInstanceUID     string
	SeriesInstanceUID    string
	ReferencedImages     []ReferencedImage
	DisplayedArea        DisplayedArea
	DisplayedAreas       []DisplayedArea
	Shutter              *display.Shutter
	SpatialTransform     SpatialTransform
	SoftcopyVOI          SoftcopyVOI
	SoftcopyVOIs         []SoftcopyVOI
	PresentationLUTShape string
	GraphicAnnotations   []GraphicAnnotation
}

type ImageState struct {
	SOPInstanceUID string
	FrameNumber    int
	Rows           int
	Columns        int
}

type AppliedState struct {
	DisplayedArea      DisplayedArea
	Shutter            *display.Shutter
	RotationDegrees    int
	FlipHorizontal     bool
	WindowCenter       float64
	WindowWidth        float64
	VOI                display.VOILUT
	PresentationLUT    string
	GraphicAnnotations []GraphicAnnotation
}

// Write creates a DICOM file containing a grayscale softcopy presentation state.
// If the state's SOPClassUID is not specified, it defaults to the standard GSPS
// SOP class UID. Returns ErrInvalidObject if state is nil, or ErrUnsupportedSOPClass
// if the SOP class UID does not match the supported GSPS class.
func Write(state *State) (*object.File, error) {
	if state == nil {
		return nil, fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	sopClassUID := state.SOPClassUID
	if sopClassUID == "" {
		sopClassUID = GrayscaleSoftcopyPresentationStateStorage
	}
	if sopClassUID != GrayscaleSoftcopyPresentationStateStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	if err := validateReferencedSeries(state.ReferencedImages); err != nil {
		return nil, err
	}
	if err := validateDisplayedAreas(state); err != nil {
		return nil, err
	}
	if err := display.ValidateShutter(state.Shutter); err != nil {
		return nil, err
	}
	if err := validateSoftcopyVOIs(state); err != nil {
		return nil, err
	}
	if err := validateGraphicAnnotations(state.GraphicAnnotations); err != nil {
		return nil, err
	}
	if state.SpatialTransform.RotationDegrees < 0 || state.SpatialTransform.RotationDegrees > 1<<16-1 {
		return nil, fmt.Errorf("%w: image rotation %d is outside US", ErrInvalidObject, state.SpatialTransform.RotationDegrees)
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, sopClassUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, state.SOPInstanceUID),
		derivedio.CS(derivedio.TagModality, "PR"),
		derivedio.UI(derivedio.TagStudyInstanceUID, state.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, state.SeriesInstanceUID),
		referencedSeriesSequence(state.ReferencedImages),
		displayedAreaSequence(state),
		derivedio.US(tagImageRotation, uint16(state.SpatialTransform.RotationDegrees)),
		derivedio.CS(tagImageHorizontalFlip, flipValue(state.SpatialTransform.FlipHorizontal)),
	}
	elements = append(elements, shutterElements(state.Shutter)...)
	if voiSequence, ok := softcopyVOISequence(state); ok {
		elements = append(elements, voiSequence)
	}
	elements = append(elements,
		derivedio.CS(tagPresentationLUTShape, presentationLUTShape(state.PresentationLUTShape)),
		graphicLayerSequence(state.GraphicAnnotations),
		graphicAnnotationSequence(state.GraphicAnnotations),
	)
	dataset := derivedio.Object(elements...)
	return derivedio.File(sopClassUID, state.SOPInstanceUID, dataset)
}

// Read extracts GSPS presentation state from a DICOM object.
// It returns ErrInvalidObject if obj is nil, or ErrUnsupportedSOPClass if the
// object's SOP Class UID does not match the supported GSPS SOP class.
func Read(obj *object.Object) (*State, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClassUID := derivedio.CleanUID(obj, derivedio.TagSOPClassUID)
	if sopClassUID != GrayscaleSoftcopyPresentationStateStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	vois, err := readSoftcopyVOIs(obj)
	if err != nil {
		return nil, err
	}
	displayedAreas, err := readDisplayedAreas(obj)
	if err != nil {
		return nil, err
	}
	shutter, err := display.ShutterFromObject(obj)
	if err != nil {
		return nil, fmt.Errorf("dicom/gsps: %w", err)
	}
	state := &State{
		SOPClassUID:          sopClassUID,
		SOPInstanceUID:       derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:     derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:    derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		ReferencedImages:     readReferences(obj),
		DisplayedAreas:       displayedAreas,
		Shutter:              shutter,
		SpatialTransform:     readSpatialTransform(obj),
		SoftcopyVOIs:         vois,
		PresentationLUTShape: derivedio.CleanString(obj, tagPresentationLUTShape),
		GraphicAnnotations:   readGraphicAnnotations(obj),
	}
	if len(displayedAreas) > 0 {
		state.DisplayedArea = displayedAreas[0]
	}
	if len(vois) > 0 {
		state.SoftcopyVOI = vois[0]
		for _, voi := range vois {
			if len(voi.ReferencedImages) == 0 {
				state.SoftcopyVOI = voi
				break
			}
		}
	}
	return state, nil
}

// Apply derives image-specific display parameters from a presentation state,
// optionally verifying that the image appears in the state's referenced images.
// Returns AppliedState with the derived parameters, ErrInvalidObject if state is nil,
// or ErrMissingReference if the image is not found in references.
func Apply(state *State, image ImageState) (AppliedState, error) {
	if state == nil {
		return AppliedState{}, fmt.Errorf("%w: state is nil", ErrInvalidObject)
	}
	if image.SOPInstanceUID != "" && len(state.ReferencedImages) > 0 {
		if !referencesApply(state.ReferencedImages, image) {
			return AppliedState{}, fmt.Errorf("%w: image %s", ErrMissingReference, image.SOPInstanceUID)
		}
	}
	displayedArea := selectDisplayedArea(state, image)
	annotations, err := applyGraphicAnnotations(state, displayedArea, image)
	if err != nil {
		return AppliedState{}, err
	}
	softcopyVOI := selectSoftcopyVOI(state, image)
	voi := display.VOILUT{
		Center: softcopyVOI.WindowCenter, Width: softcopyVOI.WindowWidth,
		Function: softcopyVOI.Function, LUT: softcopyVOI.LUT,
	}
	return AppliedState{
		DisplayedArea:      displayedArea,
		Shutter:            cloneShutter(state.Shutter),
		RotationDegrees:    state.SpatialTransform.RotationDegrees,
		FlipHorizontal:     state.SpatialTransform.FlipHorizontal,
		WindowCenter:       softcopyVOI.WindowCenter,
		WindowWidth:        softcopyVOI.WindowWidth,
		VOI:                voi,
		PresentationLUT:    presentationLUTShape(state.PresentationLUTShape),
		GraphicAnnotations: annotations,
	}, nil
}

func selectSoftcopyVOI(state *State, image ImageState) SoftcopyVOI {
	items := state.SoftcopyVOIs
	if len(items) == 0 {
		return state.SoftcopyVOI
	}
	defaultIndex := -1
	for i, item := range items {
		if len(item.ReferencedImages) == 0 {
			if defaultIndex < 0 {
				defaultIndex = i
			}
			continue
		}
		if softcopyVOIApplies(item, image) {
			return item
		}
	}
	if defaultIndex >= 0 {
		return items[defaultIndex]
	}
	return SoftcopyVOI{}
}

func selectDisplayedArea(state *State, image ImageState) DisplayedArea {
	areas := configuredDisplayedAreas(state)
	defaultIndex := -1
	for i, area := range areas {
		if len(area.ReferencedImages) == 0 {
			if defaultIndex < 0 {
				defaultIndex = i
			}
			continue
		}
		if referencesApply(area.ReferencedImages, image) {
			return area
		}
	}
	if defaultIndex >= 0 {
		return areas[defaultIndex]
	}
	return DisplayedArea{}
}

func softcopyVOIApplies(item SoftcopyVOI, image ImageState) bool {
	return referencesApply(item.ReferencedImages, image)
}

func referencesApply(refs []ReferencedImage, image ImageState) bool {
	for _, ref := range refs {
		if ref.SOPInstanceUID != image.SOPInstanceUID {
			continue
		}
		if len(ref.Frames) == 0 {
			return true
		}
		if image.FrameNumber <= 0 {
			continue
		}
		for _, frame := range ref.Frames {
			if frame == image.FrameNumber {
				return true
			}
		}
	}
	return false
}

func applyGraphicAnnotations(state *State, displayedArea DisplayedArea, image ImageState) ([]GraphicAnnotation, error) {
	annotations := make([]GraphicAnnotation, 0, len(state.GraphicAnnotations))
	for i, source := range state.GraphicAnnotations {
		if len(source.ReferencedImages) > 0 && !referencesApply(source.ReferencedImages, image) {
			continue
		}
		annotation := source
		annotation.Polyline = append([]Point2D(nil), source.Polyline...)
		if source.Text != "" {
			anchor, err := annotationPointInPixelUnits(source.Anchor, source.AnchorUnits, displayedArea, image)
			if err != nil {
				return nil, fmt.Errorf("annotation %d anchor: %w", i, err)
			}
			annotation.Anchor = anchor
			annotation.AnchorUnits = AnnotationUnitsPixel
		}
		if len(source.Polyline) > 0 {
			for pointIndex, point := range source.Polyline {
				converted, err := annotationPointInPixelUnits(point, source.GraphicUnits, displayedArea, image)
				if err != nil {
					return nil, fmt.Errorf("annotation %d graphic point %d: %w", i, pointIndex, err)
				}
				annotation.Polyline[pointIndex] = converted
			}
			annotation.GraphicUnits = AnnotationUnitsPixel
		}
		annotations = append(annotations, annotation)
	}
	return annotations, nil
}

func annotationPointInPixelUnits(point Point2D, units string, area DisplayedArea, image ImageState) (Point2D, error) {
	switch normalizedAnnotationUnits(units) {
	case AnnotationUnitsPixel:
		return point, nil
	case AnnotationUnitsDisplay:
		if !unitInterval(point.X) || !unitInterval(point.Y) {
			return Point2D{}, fmt.Errorf("%w: DISPLAY point %g\\%g is outside 0..1", ErrInvalidObject, point.X, point.Y)
		}
		left, top, right, bottom, err := displayedAreaPixelEdges(area, image)
		if err != nil {
			return Point2D{}, err
		}
		return Point2D{
			X: left + point.X*(right-left),
			Y: top + point.Y*(bottom-top),
		}, nil
	default:
		return Point2D{}, fmt.Errorf("%w: %q", ErrUnsupportedUnits, units)
	}
}

func displayedAreaPixelEdges(area DisplayedArea, image ImageState) (left, top, right, bottom float64, err error) {
	if !displayedAreaDefined(area) {
		if image.Columns <= 0 || image.Rows <= 0 {
			return 0, 0, 0, 0, fmt.Errorf("%w: DISPLAY units require positive image dimensions", ErrInvalidObject)
		}
		return 0, 0, float64(image.Columns), float64(image.Rows), nil
	}
	if area.BottomRightX < area.TopLeftX || area.BottomRightY < area.TopLeftY {
		return 0, 0, 0, 0, fmt.Errorf("%w: invalid displayed area %+v", ErrInvalidObject, area)
	}
	return float64(area.TopLeftX - 1), float64(area.TopLeftY - 1), float64(area.BottomRightX), float64(area.BottomRightY), nil
}

func unitInterval(value float64) bool {
	return value >= 0 && value <= 1 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

// referencedSeriesSequence creates a DICOM ReferencedSeriesSequence element containing referenced images grouped by series.
func referencedSeriesSequence(refs []ReferencedImage) core.Element {
	type seriesGroup struct {
		seriesUID string
		images    []core.DataSet
	}
	var groups []seriesGroup
	index := map[string]int{}
	for _, ref := range refs {
		elements := []core.Element{
			derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID),
			derivedio.UI(derivedio.TagRefSOPInstanceUID, ref.SOPInstanceUID),
		}
		if len(ref.Frames) > 0 {
			elements = append(elements, derivedio.IS(derivedio.TagRefFrameNumber, ref.Frames...))
		}
		ds := derivedio.DataSet(elements...)
		if i, ok := index[ref.SeriesInstanceUID]; ok {
			groups[i].images = append(groups[i].images, ds)
			continue
		}
		index[ref.SeriesInstanceUID] = len(groups)
		groups = append(groups, seriesGroup{seriesUID: ref.SeriesInstanceUID, images: []core.DataSet{ds}})
	}
	items := make([]core.DataSet, 0, len(groups))
	for _, group := range groups {
		items = append(items, derivedio.DataSet(
			derivedio.UI(derivedio.TagSeriesInstanceUID, group.seriesUID),
			derivedio.Seq(tagReferencedImageSequence, group.images...),
		))
	}
	return derivedio.Seq(tagReferencedSeriesSequence, items...)
}

func validateReferencedSeries(refs []ReferencedImage) error {
	for _, ref := range refs {
		if strings.TrimSpace(ref.SeriesInstanceUID) == "" ||
			strings.TrimSpace(ref.SOPClassUID) == "" ||
			strings.TrimSpace(ref.SOPInstanceUID) == "" {
			return fmt.Errorf("%w: referenced image requires series, SOP class, and SOP instance UIDs", ErrMissingReference)
		}
	}
	return nil
}

func validateDisplayedArea(area DisplayedArea) error {
	if !displayedAreaDefined(area) {
		return fmt.Errorf("%w: Displayed Area is required", ErrInvalidObject)
	}
	if area.BottomRightX < area.TopLeftX || area.BottomRightY < area.TopLeftY {
		return fmt.Errorf("%w: invalid Displayed Area %+v", ErrInvalidObject, area)
	}
	for _, value := range []int{area.TopLeftX, area.TopLeftY, area.BottomRightX, area.BottomRightY} {
		if int64(value) < -(1<<31) || int64(value) > 1<<31-1 {
			return fmt.Errorf("%w: Displayed Area coordinate %d is outside SL", ErrInvalidObject, value)
		}
	}
	mode := normalizedPresentationSizeMode(area.PresentationSizeMode)
	switch mode {
	case PresentationSizeScaleToFit:
	case PresentationSizeTrueSize:
		if len(area.PixelSpacing) != 2 {
			return fmt.Errorf("%w: TRUE SIZE Displayed Area requires Presentation Pixel Spacing", ErrInvalidObject)
		}
	case PresentationSizeMagnify:
		if area.MagnificationRatio <= 0 || math.IsNaN(area.MagnificationRatio) || math.IsInf(area.MagnificationRatio, 0) {
			return fmt.Errorf("%w: MAGNIFY Displayed Area requires a positive magnification ratio", ErrInvalidObject)
		}
	default:
		return fmt.Errorf("%w: unsupported Presentation Size Mode %q", ErrInvalidObject, area.PresentationSizeMode)
	}
	if area.MagnificationRatio != 0 &&
		(area.MagnificationRatio < 0 || math.IsNaN(area.MagnificationRatio) || math.IsInf(area.MagnificationRatio, 0)) {
		return fmt.Errorf("%w: invalid magnification ratio %g", ErrInvalidObject, area.MagnificationRatio)
	}
	if len(area.PixelSpacing) != 0 && len(area.PixelSpacing) != 2 {
		return fmt.Errorf("%w: Presentation Pixel Spacing requires two values", ErrInvalidObject)
	}
	for _, value := range area.PixelSpacing {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%w: Presentation Pixel Spacing contains invalid value %g", ErrInvalidObject, value)
		}
	}
	if len(area.PixelAspectRatio) != 0 && len(area.PixelAspectRatio) != 2 {
		return fmt.Errorf("%w: Presentation Pixel Aspect Ratio requires two values", ErrInvalidObject)
	}
	if len(area.PixelSpacing) > 0 && len(area.PixelAspectRatio) > 0 {
		return fmt.Errorf("%w: Displayed Area cannot encode both Presentation Pixel Spacing and Aspect Ratio", ErrInvalidObject)
	}
	for _, value := range area.PixelAspectRatio {
		if value <= 0 {
			return fmt.Errorf("%w: Presentation Pixel Aspect Ratio contains invalid value %d", ErrInvalidObject, value)
		}
	}
	for _, ref := range area.ReferencedImages {
		if strings.TrimSpace(ref.SOPClassUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
			return fmt.Errorf("%w: Displayed Area reference requires SOP class and instance UIDs", ErrMissingReference)
		}
	}
	return nil
}

func validateDisplayedAreas(state *State) error {
	areas := configuredDisplayedAreas(state)
	if len(areas) == 0 {
		return fmt.Errorf("%w: Displayed Area is required", ErrInvalidObject)
	}
	for i, area := range areas {
		if err := validateDisplayedArea(area); err != nil {
			return fmt.Errorf("dicom/gsps: Displayed Area item %d: %w", i, err)
		}
	}
	return nil
}

func configuredDisplayedAreas(state *State) []DisplayedArea {
	if state != nil && len(state.DisplayedAreas) > 0 {
		return state.DisplayedAreas
	}
	if state != nil && displayedAreaDefined(state.DisplayedArea) {
		return []DisplayedArea{state.DisplayedArea}
	}
	return nil
}

func normalizedPresentationSizeMode(mode string) string {
	mode = strings.ToUpper(strings.TrimSpace(mode))
	if mode == "" {
		return PresentationSizeScaleToFit
	}
	return mode
}

func shutterElements(shutter *display.Shutter) []core.Element {
	if shutter == nil {
		return nil
	}
	shapes := make([]string, 0, 4)
	elements := make([]core.Element, 0, 16)
	if rect := shutter.Rectangle; rect != nil {
		shapes = append(shapes, "RECTANGULAR")
		elements = append(elements,
			derivedio.IS(tagShutterLeft, rect.Left),
			derivedio.IS(tagShutterRight, rect.Right),
			derivedio.IS(tagShutterUpper, rect.Upper),
			derivedio.IS(tagShutterLower, rect.Lower),
		)
	}
	if circle := shutter.Circle; circle != nil {
		shapes = append(shapes, "CIRCULAR")
		elements = append(elements,
			derivedio.IS(tagShutterCircleCenter, circle.CenterRow, circle.CenterCol),
			derivedio.IS(tagShutterCircleRadius, circle.Radius),
		)
	}
	if polygon := shutter.Polygon; polygon != nil {
		shapes = append(shapes, "POLYGONAL")
		values := make([]int, 0, len(polygon.Vertices)*2)
		for _, point := range polygon.Vertices {
			values = append(values, point.Row, point.Col)
		}
		elements = append(elements, derivedio.IS(tagShutterPolygonVertices, values...))
	}
	if bitmap := shutter.Bitmap; bitmap != nil {
		shapes = append(shapes, "BITMAP")
		group := bitmap.Group
		overlay := bitmap.Overlay
		elements = append(elements,
			derivedio.US(tagShutterOverlayGroup, group),
			derivedio.US(core.NewTag(group, 0x0010), uint16(overlay.Rows)),
			derivedio.US(core.NewTag(group, 0x0011), uint16(overlay.Columns)),
			derivedio.CS(core.NewTag(group, 0x0040), "G"),
			signedShorts(core.NewTag(group, 0x0050), int16(overlay.OriginRow), int16(overlay.OriginCol)),
			derivedio.US(core.NewTag(group, 0x0100), 1),
			derivedio.US(core.NewTag(group, 0x0102), 0),
			derivedio.Raw(core.NewTag(group, 0x3000), core.VROW, append([]byte(nil), overlay.Data...)),
		)
	}
	pvalue := uint16(shutter.PresentationValue) * 257
	if shutter.PresentationPValueDefined {
		pvalue = shutter.PresentationPValue
	}
	head := []core.Element{
		derivedio.Strings(tagShutterShape, core.VRCS, shapes),
		derivedio.US(tagShutterPresentationValue, pvalue),
	}
	if shutter.PresentationColorCIELabDefined {
		color := shutter.PresentationColorCIELab
		head = append(head, derivedio.US(tagShutterPresentationColor, color[0], color[1], color[2]))
	}
	return append(head, elements...)
}

func signedShorts(tag core.Tag, values ...int16) core.Element {
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		binary.LittleEndian.PutUint16(raw[i*2:], uint16(value))
	}
	return derivedio.Raw(tag, core.VRSS, raw)
}

func cloneShutter(source *display.Shutter) *display.Shutter {
	return source.Clone()
}

func validateGraphicAnnotations(items []GraphicAnnotation) error {
	type groupKey struct {
		index int
		layer string
	}
	groupRefs := map[groupKey][]ReferencedImage{}
	for i, item := range items {
		for _, ref := range item.ReferencedImages {
			if strings.TrimSpace(ref.SOPClassUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
				return fmt.Errorf("%w: annotation %d reference requires SOP class and instance UIDs", ErrMissingReference, i)
			}
		}
		if item.GroupIndex > 0 {
			key := groupKey{index: item.GroupIndex, layer: graphicLayerName(item.LayerName)}
			if previous, ok := groupRefs[key]; ok && !referencedImagesEqual(previous, item.ReferencedImages) {
				return fmt.Errorf("%w: annotation group %d on layer %q has inconsistent references", ErrInvalidObject, item.GroupIndex, item.LayerName)
			}
			groupRefs[key] = item.ReferencedImages
		}
		if item.Text != "" {
			if err := validateAnnotationPoint(item.Anchor, item.AnchorUnits); err != nil {
				return fmt.Errorf("dicom/gsps: annotation %d anchor: %w", i, err)
			}
		}
		if len(item.Polyline) > 1<<16-1 {
			return fmt.Errorf("%w: annotation %d has %d graphic points, maximum is 65535", ErrInvalidObject, i, len(item.Polyline))
		}
		for pointIndex, point := range item.Polyline {
			if err := validateAnnotationPoint(point, item.GraphicUnits); err != nil {
				return fmt.Errorf("dicom/gsps: annotation %d graphic point %d: %w", i, pointIndex, err)
			}
		}
	}
	return nil
}

func referencedImagesEqual(left, right []ReferencedImage) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].SeriesInstanceUID != right[i].SeriesInstanceUID ||
			left[i].SOPClassUID != right[i].SOPClassUID ||
			left[i].SOPInstanceUID != right[i].SOPInstanceUID ||
			len(left[i].Frames) != len(right[i].Frames) {
			return false
		}
		for frameIndex := range left[i].Frames {
			if left[i].Frames[frameIndex] != right[i].Frames[frameIndex] {
				return false
			}
		}
	}
	return true
}

func validateAnnotationPoint(point Point2D, units string) error {
	units = normalizedAnnotationUnits(units)
	if units != AnnotationUnitsPixel && units != AnnotationUnitsDisplay {
		return fmt.Errorf("%w: %q", ErrUnsupportedUnits, units)
	}
	if math.IsNaN(point.X) || math.IsInf(point.X, 0) || math.IsNaN(point.Y) || math.IsInf(point.Y, 0) {
		return fmt.Errorf("%w: annotation point contains a non-finite value", ErrInvalidObject)
	}
	if units == AnnotationUnitsDisplay && (!unitInterval(point.X) || !unitInterval(point.Y)) {
		return fmt.Errorf("%w: DISPLAY point %g\\%g is outside 0..1", ErrInvalidObject, point.X, point.Y)
	}
	return nil
}

func validateSoftcopyVOIs(state *State) error {
	items := configuredSoftcopyVOIs(state)
	for i, item := range items {
		for _, ref := range item.ReferencedImages {
			if strings.TrimSpace(ref.SOPClassUID) == "" || strings.TrimSpace(ref.SOPInstanceUID) == "" {
				return fmt.Errorf("%w: Softcopy VOI item %d reference requires SOP class and instance UIDs", ErrMissingReference, i)
			}
		}
		if item.LUT == nil {
			if math.IsNaN(item.WindowCenter) || math.IsInf(item.WindowCenter, 0) ||
				math.IsNaN(item.WindowWidth) || math.IsInf(item.WindowWidth, 0) {
				return fmt.Errorf("%w: Softcopy VOI item %d window contains a non-finite value", ErrInvalidObject, i)
			}
			if item.Function == display.VOILinear && item.WindowWidth < 1 {
				return fmt.Errorf("%w: Softcopy VOI item %d LINEAR window width %g is below 1", ErrInvalidObject, i, item.WindowWidth)
			}
			if item.Function != display.VOILinear && item.WindowWidth <= 0 {
				return fmt.Errorf("%w: Softcopy VOI item %d window width %g is not positive", ErrInvalidObject, i, item.WindowWidth)
			}
			continue
		}
		if item.LUT.FirstMapped < -(1<<15) || item.LUT.FirstMapped > 1<<16-1 {
			return fmt.Errorf("%w: Softcopy VOI item %d LUT first mapped value %d is outside SS/US", ErrInvalidObject, i, item.LUT.FirstMapped)
		}
		if len(item.LUT.Entries) > 1<<16 {
			return fmt.Errorf("%w: Softcopy VOI item %d LUT has %d entries, maximum is 65536", ErrInvalidObject, i, len(item.LUT.Entries))
		}
		encodedCount := len(item.LUT.Entries)
		if encodedCount == 1<<16 {
			encodedCount = 0
		}
		if _, err := display.NewLUT([]int{encodedCount, item.LUT.FirstMapped, item.LUT.BitsPerEntry}, item.LUT.Entries); err != nil {
			return fmt.Errorf("dicom/gsps: Softcopy VOI item %d: %w", i, err)
		}
	}
	return nil
}

func displayedAreaDefined(area DisplayedArea) bool {
	return area.Defined || area.TopLeftX != 0 || area.TopLeftY != 0 || area.BottomRightX != 0 || area.BottomRightY != 0
}

func configuredSoftcopyVOIs(state *State) []SoftcopyVOI {
	if len(state.SoftcopyVOIs) > 0 {
		return state.SoftcopyVOIs
	}
	if softcopyVOIDefined(state.SoftcopyVOI) {
		return []SoftcopyVOI{state.SoftcopyVOI}
	}
	return nil
}

func softcopyVOIDefined(voi SoftcopyVOI) bool {
	return voi.WindowCenter != 0 || voi.WindowWidth != 0 || voi.Explanation != "" ||
		voi.Function != display.VOILinear || voi.LUT != nil || len(voi.ReferencedImages) > 0
}

// displayedAreaSequence creates image-specific or default Displayed Area items.
func displayedAreaSequence(state *State) core.Element {
	areas := configuredDisplayedAreas(state)
	items := make([]core.DataSet, 0, len(areas))
	for _, area := range areas {
		elements := []core.Element{
			derivedio.SL(tagDisplayedAreaTopLeft, int32(area.TopLeftX), int32(area.TopLeftY)),
			derivedio.SL(tagDisplayedAreaBottomRight, int32(area.BottomRightX), int32(area.BottomRightY)),
			derivedio.CS(tagPresentationSizeMode, normalizedPresentationSizeMode(area.PresentationSizeMode)),
		}
		if len(area.ReferencedImages) > 0 {
			elements = append(elements, voiReferencedImageSequence(area.ReferencedImages))
		}
		if len(area.PixelSpacing) == 2 {
			elements = append(elements, derivedio.DS(tagPresentationPixelSpacing, area.PixelSpacing...))
		}
		if len(area.PixelAspectRatio) == 2 {
			elements = append(elements, derivedio.IS(tagPresentationPixelAspectRatio, area.PixelAspectRatio...))
		}
		if area.MagnificationRatio != 0 {
			elements = append(elements, derivedio.DS(tagPresentationPixelMagnify, area.MagnificationRatio))
		}
		items = append(items, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagDisplayedAreaSelectionSeq, items...)
}

// softcopyVOISequence creates image-specific or default Softcopy VOI items.
func softcopyVOISequence(state *State) (core.Element, bool) {
	items := configuredSoftcopyVOIs(state)
	if len(items) == 0 {
		return core.Element{}, false
	}
	sets := make([]core.DataSet, 0, len(items))
	for _, voi := range items {
		elements := make([]core.Element, 0, 5)
		if len(voi.ReferencedImages) > 0 {
			elements = append(elements, voiReferencedImageSequence(voi.ReferencedImages))
		}
		if voi.LUT != nil {
			elements = append(elements, voiLUTSequence(voi))
		} else {
			elements = append(elements,
				derivedio.DS(tagWindowCenter, voi.WindowCenter),
				derivedio.DS(tagWindowWidth, voi.WindowWidth),
				derivedio.LO(tagWindowCenterWidthExplanation, voi.Explanation),
				derivedio.CS(tagVOILUTFunction, voiFunctionValue(voi.Function)),
			)
		}
		sets = append(sets, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagSoftcopyVOILUTSequence, sets...), true
}

func voiReferencedImageSequence(refs []ReferencedImage) core.Element {
	items := make([]core.DataSet, 0, len(refs))
	for _, ref := range refs {
		elements := []core.Element{
			derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID),
			derivedio.UI(derivedio.TagRefSOPInstanceUID, ref.SOPInstanceUID),
		}
		if len(ref.Frames) > 0 {
			elements = append(elements, derivedio.IS(derivedio.TagRefFrameNumber, ref.Frames...))
		}
		items = append(items, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagReferencedImageSequence, items...)
}

func voiLUTSequence(voi SoftcopyVOI) core.Element {
	lut := voi.LUT
	count := len(lut.Entries)
	encodedCount := count
	if count == 1<<16 {
		encodedCount = 0
	}
	descriptorVR := core.VRUS
	if lut.FirstMapped < 0 {
		descriptorVR = core.VRSS
	}
	var descriptor [6]byte
	binary.LittleEndian.PutUint16(descriptor[0:2], uint16(encodedCount))
	binary.LittleEndian.PutUint16(descriptor[2:4], uint16(lut.FirstMapped))
	binary.LittleEndian.PutUint16(descriptor[4:6], uint16(lut.BitsPerEntry))
	return derivedio.Seq(tagVOILUTSequence, derivedio.DataSet(
		core.NewRawElement(tagLUTDescriptor, descriptorVR, descriptor[:]),
		derivedio.LO(tagLUTExplanation, voi.Explanation),
		derivedio.Raw(tagLUTData, core.VROW, derivedio.Uint16Bytes(lut.Entries)),
	))
}

func voiFunctionValue(function display.VOIFunction) string {
	switch function {
	case display.VOILinearExact:
		return "LINEAR_EXACT"
	case display.VOISigmoid:
		return "SIGMOID"
	default:
		return "LINEAR"
	}
}

// graphicAnnotationSequence creates a DICOM GraphicAnnotationSequence element
// while preserving objects that were grouped in the same source item.
func graphicAnnotationSequence(items []GraphicAnnotation) core.Element {
	type groupKey struct {
		index int
		layer string
	}
	type annotationGroup struct {
		layer string
		items []GraphicAnnotation
		refs  []ReferencedImage
	}

	groups := make([]annotationGroup, 0, len(items))
	groupIndexes := map[groupKey]int{}
	for _, item := range items {
		layer := graphicLayerName(item.LayerName)
		if item.GroupIndex <= 0 {
			groups = append(groups, annotationGroup{layer: layer, items: []GraphicAnnotation{item}, refs: item.ReferencedImages})
			continue
		}
		key := groupKey{index: item.GroupIndex, layer: layer}
		if groupIndex, ok := groupIndexes[key]; ok {
			groups[groupIndex].items = append(groups[groupIndex].items, item)
			continue
		}
		groupIndexes[key] = len(groups)
		groups = append(groups, annotationGroup{layer: layer, items: []GraphicAnnotation{item}, refs: item.ReferencedImages})
	}

	sets := make([]core.DataSet, 0, len(groups))
	for _, group := range groups {
		elements := []core.Element{derivedio.SH(tagGraphicLayer, group.layer)}
		if len(group.refs) > 0 {
			elements = append(elements, voiReferencedImageSequence(group.refs))
		}
		texts := make([]core.DataSet, 0, len(group.items))
		graphics := make([]core.DataSet, 0, len(group.items))
		for _, item := range group.items {
			if item.Text != "" {
				texts = append(texts, derivedio.DataSet(
					derivedio.CS(tagAnchorPointAnnotationUnits, normalizedAnnotationUnits(item.AnchorUnits)),
					derivedio.Str(tagUnformattedTextValue, core.VRUT, item.Text),
					derivedio.FL(tagAnchorPoint, item.Anchor.X, item.Anchor.Y),
					derivedio.CS(tagAnchorPointVisibility, "N"),
				))
			}
			if len(item.Polyline) > 0 {
				graphicType := normalizedGraphicType(item.GraphicType)
				if graphicType == "" {
					graphicType = GraphicTypePolyline
				}
				graphicElements := []core.Element{
					derivedio.CS(tagGraphicAnnotationUnits, normalizedAnnotationUnits(item.GraphicUnits)),
					derivedio.US(tagGraphicDimensions, 2),
					derivedio.US(tagNumberOfGraphicPoints, uint16(len(item.Polyline))),
					derivedio.FL(tagGraphicData, flattenPoints(item.Polyline)...),
					derivedio.CS(tagGraphicType, graphicType),
				}
				if graphicNeedsFilledAttribute(graphicType, item.Polyline) {
					graphicElements = append(graphicElements, derivedio.CS(tagGraphicFilled, "N"))
				}
				graphics = append(graphics, derivedio.DataSet(graphicElements...))
			}
		}
		if len(texts) > 0 {
			elements = append(elements, derivedio.Seq(tagTextObjectSequence, texts...))
		}
		if len(graphics) > 0 {
			elements = append(elements, derivedio.Seq(tagGraphicObjectSequence, graphics...))
		}
		sets = append(sets, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagGraphicAnnotationSequence, sets...)
}

func graphicLayerSequence(items []GraphicAnnotation) core.Element {
	seen := map[string]bool{}
	layers := make([]core.DataSet, 0, len(items))
	for _, item := range items {
		name := graphicLayerName(item.LayerName)
		if seen[name] {
			continue
		}
		seen[name] = true
		layers = append(layers, derivedio.DataSet(
			derivedio.SH(tagGraphicLayer, name),
			derivedio.IS(tagGraphicLayerOrder, len(layers)+1),
		))
	}
	return derivedio.Seq(tagGraphicLayerSequence, layers...)
}

// readReferences extracts referenced image identifiers and frame numbers from a DICOM object.
func readReferences(obj *object.Object) []ReferencedImage {
	var out []ReferencedImage
	for _, series := range derivedio.Sequence(obj, tagReferencedSeriesSequence) {
		seriesUID := derivedio.CleanUID(series, derivedio.TagSeriesInstanceUID)
		for _, refObj := range derivedio.Sequence(series, tagReferencedImageSequence) {
			ref := ReferencedImage{
				SeriesInstanceUID: seriesUID,
				SOPClassUID:       derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
				SOPInstanceUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
			}
			if frames, err := refObj.GetInts(derivedio.TagRefFrameNumber); err == nil {
				for _, frame := range frames {
					ref.Frames = append(ref.Frames, int(frame))
				}
			}
			out = append(out, ref)
		}
	}
	return out
}

func readDirectImageReferences(obj *object.Object) []ReferencedImage {
	var out []ReferencedImage
	for _, refObj := range derivedio.Sequence(obj, tagReferencedImageSequence) {
		ref := ReferencedImage{
			SOPClassUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
			SOPInstanceUID: derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
		}
		if frames, err := refObj.GetInts(derivedio.TagRefFrameNumber); err == nil {
			for _, frame := range frames {
				ref.Frames = append(ref.Frames, int(frame))
			}
		}
		out = append(out, ref)
	}
	return out
}

// readDisplayedAreas extracts every image-specific or default displayed area.
func readDisplayedAreas(obj *object.Object) ([]DisplayedArea, error) {
	items := derivedio.Sequence(obj, tagDisplayedAreaSelectionSeq)
	if len(items) == 0 {
		return nil, nil
	}
	areas := make([]DisplayedArea, 0, len(items))
	for i, item := range items {
		tl := derivedio.Ints(item, tagDisplayedAreaTopLeft)
		br := derivedio.Ints(item, tagDisplayedAreaBottomRight)
		if len(tl) != 2 || len(br) != 2 {
			return nil, fmt.Errorf("%w: Displayed Area item %d requires two corners", ErrInvalidObject, i)
		}
		area := DisplayedArea{
			Defined:              true,
			TopLeftX:             int(tl[0]),
			TopLeftY:             int(tl[1]),
			BottomRightX:         int(br[0]),
			BottomRightY:         int(br[1]),
			PresentationSizeMode: derivedio.CleanString(item, tagPresentationSizeMode),
			PixelSpacing:         derivedio.Floats(item, tagPresentationPixelSpacing),
			ReferencedImages:     readDirectImageReferences(item),
		}
		for _, value := range derivedio.Ints(item, tagPresentationPixelAspectRatio) {
			area.PixelAspectRatio = append(area.PixelAspectRatio, int(value))
		}
		if values := derivedio.Floats(item, tagPresentationPixelMagnify); len(values) > 0 {
			area.MagnificationRatio = values[0]
		}
		if err := validateDisplayedArea(area); err != nil {
			return nil, fmt.Errorf("dicom/gsps: Displayed Area item %d: %w", i, err)
		}
		areas = append(areas, area)
	}
	return areas, nil
}

// readSpatialTransform extracts spatial transform settings from the given DICOM object.
func readSpatialTransform(obj *object.Object) SpatialTransform {
	return SpatialTransform{
		RotationDegrees: derivedio.Int(obj, tagImageRotation),
		FlipHorizontal:  derivedio.CleanString(obj, tagImageHorizontalFlip) == "Y",
	}
}

// readSoftcopyVOIs extracts every image-specific or default Softcopy VOI item.
func readSoftcopyVOIs(obj *object.Object) ([]SoftcopyVOI, error) {
	items := derivedio.Sequence(obj, tagSoftcopyVOILUTSequence)
	out := make([]SoftcopyVOI, 0, len(items))
	for i, item := range items {
		transform, err := display.VOIFromObject(item)
		if err != nil {
			return nil, fmt.Errorf("dicom/gsps: Softcopy VOI item %d: %w", i, err)
		}
		explanation := derivedio.CleanString(item, tagWindowCenterWidthExplanation)
		if transform.LUT != nil {
			if lutItems := derivedio.Sequence(item, tagVOILUTSequence); len(lutItems) > 0 {
				explanation = derivedio.CleanString(lutItems[0], tagLUTExplanation)
			}
		}
		out = append(out, SoftcopyVOI{
			WindowCenter:     transform.Center,
			WindowWidth:      transform.Width,
			Explanation:      explanation,
			Function:         transform.Function,
			LUT:              transform.LUT,
			ReferencedImages: readVOIReferences(item),
		})
	}
	return out, nil
}

func readVOIReferences(item *object.Object) []ReferencedImage {
	var out []ReferencedImage
	for _, refObj := range derivedio.Sequence(item, tagReferencedImageSequence) {
		ref := ReferencedImage{
			SOPClassUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
			SOPInstanceUID: derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
		}
		if frames, err := refObj.GetInts(derivedio.TagRefFrameNumber); err == nil {
			for _, frame := range frames {
				ref.Frames = append(ref.Frames, int(frame))
			}
		}
		out = append(out, ref)
	}
	return out
}

// readGraphicAnnotations extracts graphic annotations from a DICOM object's GraphicAnnotationSequence.
func readGraphicAnnotations(obj *object.Object) []GraphicAnnotation {
	var out []GraphicAnnotation
	for groupIndex, item := range derivedio.Sequence(obj, tagGraphicAnnotationSequence) {
		layer := derivedio.CleanString(item, tagGraphicLayer)
		references := readDirectImageReferences(item)
		texts := readTextObjects(item, layer)
		graphics := readGraphicObjects(item, layer)
		for i := range texts {
			texts[i].GroupIndex = groupIndex + 1
			texts[i].ReferencedImages = cloneReferences(references)
		}
		for i := range graphics {
			graphics[i].GroupIndex = groupIndex + 1
			graphics[i].ReferencedImages = cloneReferences(references)
		}
		out = append(out, texts...)
		out = append(out, graphics...)
		if len(texts) == 0 && len(graphics) == 0 {
			out = append(out, GraphicAnnotation{
				LayerName: layer, GroupIndex: groupIndex + 1,
				ReferencedImages: cloneReferences(references),
			})
		}
	}
	return out
}

func cloneReferences(source []ReferencedImage) []ReferencedImage {
	out := make([]ReferencedImage, len(source))
	for i := range source {
		out[i] = source[i]
		out[i].Frames = append([]int(nil), source[i].Frames...)
	}
	return out
}

func readTextObjects(item *object.Object, layer string) []GraphicAnnotation {
	var out []GraphicAnnotation
	for _, textItem := range derivedio.Sequence(item, tagTextObjectSequence) {
		annotation := GraphicAnnotation{
			LayerName:   layer,
			Text:        derivedio.CleanString(textItem, tagUnformattedTextValue),
			AnchorUnits: normalizedAnnotationUnits(derivedio.CleanString(textItem, tagAnchorPointAnnotationUnits)),
		}
		anchor := derivedio.Floats(textItem, tagAnchorPoint)
		if len(anchor) >= 2 {
			annotation.Anchor = Point2D{X: anchor[0], Y: anchor[1]}
		}
		out = append(out, annotation)
	}
	return out
}

func readGraphicObjects(item *object.Object, layer string) []GraphicAnnotation {
	var out []GraphicAnnotation
	for _, graphicItem := range derivedio.Sequence(item, tagGraphicObjectSequence) {
		annotation := GraphicAnnotation{
			LayerName:    layer,
			GraphicType:  normalizedGraphicType(derivedio.CleanString(graphicItem, tagGraphicType)),
			GraphicUnits: normalizedAnnotationUnits(derivedio.CleanString(graphicItem, tagGraphicAnnotationUnits)),
		}
		if annotation.GraphicType == "" {
			annotation.GraphicType = GraphicTypePolyline
		}
		values := derivedio.Floats(graphicItem, tagGraphicData)
		for i := 0; i+1 < len(values); i += 2 {
			annotation.Polyline = append(annotation.Polyline, Point2D{X: values[i], Y: values[i+1]})
		}
		out = append(out, annotation)
	}
	return out
}

func normalizedGraphicType(value string) string {
	return strings.ToUpper(strings.TrimSpace(value))
}

func normalizedAnnotationUnits(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if value == "" {
		return AnnotationUnitsPixel
	}
	return value
}

// flattenPoints converts a sequence of 2D points into a flat float64 slice with alternating X and Y coordinates.
func flattenPoints(points []Point2D) []float64 {
	values := make([]float64, 0, len(points)*2)
	for _, point := range points {
		values = append(values, point.X, point.Y)
	}
	return values
}

func graphicLayerName(value string) string {
	if value == "" {
		return "ANNOTATIONS"
	}
	return value
}

func graphicNeedsFilledAttribute(graphicType string, points []Point2D) bool {
	switch graphicType {
	case GraphicTypeCircle, GraphicTypeEllipse:
		return len(points) > 0
	case GraphicTypePolyline, GraphicTypeInterpolated:
		return closedPolyline(points)
	default:
		return false
	}
}

func closedPolyline(points []Point2D) bool {
	if len(points) < 2 {
		return false
	}
	first := points[0]
	last := points[len(points)-1]
	return first.X == last.X && first.Y == last.Y
}

// flipValue returns "Y" if enabled is true, otherwise "N".
func flipValue(enabled bool) string {
	if enabled {
		return "Y"
	}
	return "N"
}

// presentationLUTShape returns value if non-empty, otherwise returns the identity presentation LUT shape.
func presentationLUTShape(value string) string {
	if value == "" {
		return PresentationLUTIdentity
	}
	return value
}
