package gsps

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata/display"
)

func TestGSPSRoundTripsAndAppliesDisplayState(t *testing.T) {
	// Given: a grayscale softcopy presentation state with VOI and annotation.
	state := State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.10",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.11",
		ReferencedImages: []ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.3.4.1",
			Frames:            []int{1},
		}},
		DisplayedArea:        DisplayedArea{TopLeftX: 1, TopLeftY: 2, BottomRightX: 511, BottomRightY: 512},
		SpatialTransform:     SpatialTransform{RotationDegrees: 90, FlipHorizontal: true},
		SoftcopyVOI:          SoftcopyVOI{WindowCenter: 45, WindowWidth: 350, Explanation: "Liver", Function: display.VOISigmoid},
		PresentationLUTShape: PresentationLUTIdentity,
		GraphicAnnotations: []GraphicAnnotation{{
			LayerName: "MEASUREMENTS",
			Text:      "Lesion A",
			Anchor:    Point2D{X: 100, Y: 120},
			Polyline:  []Point2D{{X: 100, Y: 120}, {X: 130, Y: 145}},
		}},
	}
	file, err := Write(&state)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}

	// When: the state is read and applied to its referenced image.
	readFile, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := Read(readFile.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	applied, err := Apply(roundTrip, ImageState{SOPInstanceUID: "1.2.3.4.1", FrameNumber: 1, Rows: 512, Columns: 512})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// Then: display transforms, VOI, and graphics are available to a viewer.
	if roundTrip.SOPClassUID != GrayscaleSoftcopyPresentationStateStorage {
		t.Fatalf("SOPClassUID = %q, want GSPS", roundTrip.SOPClassUID)
	}
	if len(roundTrip.ReferencedImages) != 1 || roundTrip.ReferencedImages[0].SeriesInstanceUID != "1.2.826.0.1.3680043.9.7433.11.20.1" {
		t.Fatalf("ReferencedImages = %+v, want referenced series UID", roundTrip.ReferencedImages)
	}
	if applied.WindowCenter != 45 || applied.WindowWidth != 350 {
		t.Fatalf("window = %.1f/%.1f, want 45/350", applied.WindowCenter, applied.WindowWidth)
	}
	if roundTrip.SoftcopyVOI.Function != display.VOISigmoid || applied.VOI.Function != display.VOISigmoid {
		t.Fatalf("VOI function = read %v applied %v, want SIGMOID", roundTrip.SoftcopyVOI.Function, applied.VOI.Function)
	}
	if applied.RotationDegrees != 90 || !applied.FlipHorizontal {
		t.Fatalf("transform = %+v, want rotation 90 and flip", applied)
	}
	if len(applied.GraphicAnnotations) != 2 || applied.GraphicAnnotations[0].Text != "Lesion A" || len(applied.GraphicAnnotations[1].Polyline) != 2 {
		t.Fatalf("annotations = %+v, want separate Lesion A text and polyline in one group", applied.GraphicAnnotations)
	}
}

func TestGSPSRoundTripsDisplayedAreasAndEveryShutterShape(t *testing.T) {
	first := ReferencedImage{
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.568.10",
		SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.568.11",
		Frames:            []int{1},
	}
	second := first
	second.SOPInstanceUID = "1.2.826.0.1.3680043.9.7433.568.12"
	state := &State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.568.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.568.2",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.568.3",
		ReferencedImages:  []ReferencedImage{first, second},
		DisplayedAreas: []DisplayedArea{
			{
				Defined: true, TopLeftX: 1, TopLeftY: 1, BottomRightX: 8, BottomRightY: 8,
				PresentationSizeMode: PresentationSizeScaleToFit,
			},
			{
				Defined: true, TopLeftX: 2, TopLeftY: 3, BottomRightX: 7, BottomRightY: 6,
				PresentationSizeMode: PresentationSizeMagnify, MagnificationRatio: 2.5,
				ReferencedImages: []ReferencedImage{second},
			},
		},
		Shutter: &display.Shutter{
			PresentationValue:              128,
			PresentationPValue:             32768,
			PresentationPValueDefined:      true,
			PresentationColorCIELab:        [3]uint16{1000, 2000, 3000},
			PresentationColorCIELabDefined: true,
			Rectangle:                      &display.RectShutter{Left: 1, Right: 8, Upper: 1, Lower: 8},
			Circle:                         &display.CircleShutter{CenterRow: 4, CenterCol: 4, Radius: 3},
			Polygon: &display.PolygonShutter{Vertices: []display.ShutterPoint{
				{Row: 1, Col: 1}, {Row: 1, Col: 8}, {Row: 8, Col: 1},
			}},
		},
		SoftcopyVOI:          SoftcopyVOI{WindowCenter: 40, WindowWidth: 400},
		PresentationLUTShape: PresentationLUTIdentity,
		GraphicAnnotations: []GraphicAnnotation{
			{LayerName: "FIRST", Text: "first", Anchor: Point2D{X: 1, Y: 1}, ReferencedImages: []ReferencedImage{first}},
			{LayerName: "SECOND", Text: "second", Anchor: Point2D{X: 2, Y: 2}, ReferencedImages: []ReferencedImage{second}},
		},
	}
	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(roundTrip.DisplayedAreas) != 2 {
		t.Fatalf("DisplayedAreas = %d, want 2", len(roundTrip.DisplayedAreas))
	}
	applied, err := Apply(roundTrip, ImageState{SOPInstanceUID: second.SOPInstanceUID, FrameNumber: 1, Rows: 8, Columns: 8})
	if err != nil {
		t.Fatalf("Apply second image: %v", err)
	}
	if applied.DisplayedArea.TopLeftX != 2 || applied.DisplayedArea.TopLeftY != 3 ||
		applied.DisplayedArea.MagnificationRatio != 2.5 {
		t.Fatalf("image-specific DisplayedArea = %+v", applied.DisplayedArea)
	}
	if len(applied.GraphicAnnotations) != 1 || applied.GraphicAnnotations[0].Text != "second" ||
		len(applied.GraphicAnnotations[0].ReferencedImages) != 1 {
		t.Fatalf("image-specific annotations = %+v, want only second", applied.GraphicAnnotations)
	}
	shutter := applied.Shutter
	if shutter == nil || shutter.Rectangle == nil || shutter.Circle == nil || shutter.Polygon == nil {
		t.Fatalf("round-trip Shutter = %+v, want all geometric shapes", shutter)
	}
	if !shutter.PresentationPValueDefined || shutter.PresentationPValue != 32768 ||
		!shutter.PresentationColorCIELabDefined || shutter.PresentationColorCIELab != ([3]uint16{1000, 2000, 3000}) {
		t.Fatalf("round-trip Shutter presentation = %+v", shutter)
	}
	state.Shutter = &display.Shutter{
		PresentationValue: 9,
		Bitmap: &display.BitmapShutter{
			Group: 0x6000,
			Overlay: display.Overlay{
				Rows: 2, Columns: 4, OriginRow: -1, OriginCol: 2,
				Data: []byte{0b00111100},
			},
		},
	}
	bitmapFile, err := Write(state)
	if err != nil {
		t.Fatalf("Write bitmap: %v", err)
	}
	var encodedBitmap bytes.Buffer
	if err := object.WriteFile(&encodedBitmap, bitmapFile); err != nil {
		t.Fatalf("object.WriteFile bitmap: %v", err)
	}
	decodedBitmap, err := object.ReadFile(bytes.NewReader(encodedBitmap.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile bitmap: %v", err)
	}
	bitmapState, err := Read(decodedBitmap.Dataset)
	if err != nil {
		t.Fatalf("Read bitmap: %v", err)
	}
	bitmap := bitmapState.Shutter.Bitmap
	if bitmap == nil || bitmap.Group != 0x6000 || bitmap.Overlay.OriginRow != -1 ||
		bitmap.Overlay.OriginCol != 2 || !bytes.Equal(bitmap.Overlay.Data, []byte{0b00111100}) {
		t.Fatalf("round-trip bitmap shutter = %+v", bitmap)
	}
}

func TestGSPSReadRejectsMalformedDisplayedAreaAndUnknownShutter(t *testing.T) {
	base := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.Seq(tagDisplayedAreaSelectionSeq, derivedio.DataSet(
			derivedio.SL(tagDisplayedAreaTopLeft, 1, 1),
			derivedio.SL(tagDisplayedAreaBottomRight, 8, 8),
			derivedio.CS(tagPresentationSizeMode, "NOT A MODE"),
		)),
	}
	if _, err := Read(object.FromElements(base, std.Dictionary)); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("invalid Presentation Size Mode error = %v, want ErrInvalidObject", err)
	}

	base[1] = derivedio.Seq(tagDisplayedAreaSelectionSeq, derivedio.DataSet(
		derivedio.SL(tagDisplayedAreaTopLeft, 1, 1),
		derivedio.SL(tagDisplayedAreaBottomRight, 8, 8),
	))
	base = append(base, derivedio.CS(tagShutterShape, "TRAPEZOID"))
	if _, err := Read(object.FromElements(base, std.Dictionary)); !errors.Is(err, display.ErrUnsupportedShutter) {
		t.Fatalf("unknown shutter error = %v, want ErrUnsupportedShutter", err)
	}
}

func TestValidateDisplayedAreaRejectsInvalidOptionalMagnificationRatio(t *testing.T) {
	for _, ratio := range []float64{-1, math.NaN(), math.Inf(1), math.Inf(-1)} {
		t.Run(fmt.Sprintf("%g", ratio), func(t *testing.T) {
			err := validateDisplayedArea(DisplayedArea{
				Defined:              true,
				PresentationSizeMode: PresentationSizeScaleToFit,
				MagnificationRatio:   ratio,
			})
			if !errors.Is(err, ErrInvalidObject) {
				t.Fatalf("validateDisplayedArea(%g) error = %v, want ErrInvalidObject", ratio, err)
			}
		})
	}
}

func TestGSPSWriteRejectsNilState(t *testing.T) {
	_, err := Write(nil)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Write(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestGSPSWriteRejectsWrongSopClass(t *testing.T) {
	state := &State{
		SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2", // CT, not GSPS
		SOPInstanceUID: "1.2.3.4.1",
	}
	_, err := Write(state)
	if !errors.Is(err, ErrUnsupportedSOPClass) {
		t.Fatalf("Write(wrong SOP class) error = %v, want ErrUnsupportedSOPClass", err)
	}
}

func TestGSPSReadRejectsNilDataset(t *testing.T) {
	_, err := Read(nil)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Read(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestGSPSReadRejectsWrongSopClass(t *testing.T) {
	// Build a dataset that has a CT SOP class UID.
	dataset := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.2"}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.3.4.9"}},
	}, std.Dictionary)
	_, err := Read(dataset)
	if !errors.Is(err, ErrUnsupportedSOPClass) {
		t.Fatalf("Read(wrong SOP class) error = %v, want ErrUnsupportedSOPClass", err)
	}
}

func TestGSPSReadPreservesMultipleTextObjects(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.Seq(tagGraphicAnnotationSequence, derivedio.DataSet(
			derivedio.SH(tagGraphicLayer, "LAYER1"),
			derivedio.Seq(tagTextObjectSequence,
				derivedio.DataSet(
					derivedio.Str(tagUnformattedTextValue, core.VRUT, "First"),
					derivedio.DS(tagAnchorPoint, 1, 2),
				),
				derivedio.DataSet(
					derivedio.Str(tagUnformattedTextValue, core.VRUT, "Second"),
					derivedio.DS(tagAnchorPoint, 3, 4),
				),
			),
		)),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(state.GraphicAnnotations) != 2 {
		t.Fatalf("GraphicAnnotations = %d, want 2", len(state.GraphicAnnotations))
	}
	if got := state.GraphicAnnotations[0]; got.Text != "First" || got.Anchor != (Point2D{X: 1, Y: 2}) || got.LayerName != "LAYER1" {
		t.Fatalf("first annotation = %+v, want First at 1,2 on LAYER1", got)
	}
	if got := state.GraphicAnnotations[1]; got.Text != "Second" || got.Anchor != (Point2D{X: 3, Y: 4}) || got.LayerName != "LAYER1" {
		t.Fatalf("second annotation = %+v, want Second at 3,4 on LAYER1", got)
	}
}

func TestGSPSReadPreservesMultipleGraphicObjectsAndType(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.Seq(tagGraphicAnnotationSequence, derivedio.DataSet(
			derivedio.SH(tagGraphicLayer, "LAYER1"),
			derivedio.Seq(tagGraphicObjectSequence,
				derivedio.DataSet(
					derivedio.CS(tagGraphicType, "POLYLINE"),
					derivedio.DS(tagGraphicData, 1, 1, 2, 2),
				),
				derivedio.DataSet(
					derivedio.CS(tagGraphicType, "CIRCLE"),
					derivedio.DS(tagGraphicData, 10, 10, 12, 10),
				),
			),
		)),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(state.GraphicAnnotations) != 2 {
		t.Fatalf("GraphicAnnotations = %d, want 2", len(state.GraphicAnnotations))
	}
	if got := state.GraphicAnnotations[0]; got.GraphicType != GraphicTypePolyline || !pointsEqual(got.Polyline, []Point2D{{X: 1, Y: 1}, {X: 2, Y: 2}}) {
		t.Fatalf("first graphic = %+v, want POLYLINE with two points", got)
	}
	if got := state.GraphicAnnotations[1]; got.GraphicType != GraphicTypeCircle || !pointsEqual(got.Polyline, []Point2D{{X: 10, Y: 10}, {X: 12, Y: 10}}) {
		t.Fatalf("second graphic = %+v, want CIRCLE with center and edge points", got)
	}
}

func TestGSPSReadWritePreservesGraphicAnnotationItemGrouping(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.308.1"),
		derivedio.UI(derivedio.TagStudyInstanceUID, "1.2.826.0.1.3680043.9.7433.308.2"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, "1.2.826.0.1.3680043.9.7433.308.3"),
		derivedio.Seq(tagDisplayedAreaSelectionSeq, derivedio.DataSet(
			derivedio.IS(tagDisplayedAreaTopLeft, 1, 1),
			derivedio.IS(tagDisplayedAreaBottomRight, 512, 512),
		)),
		derivedio.Seq(tagGraphicAnnotationSequence, derivedio.DataSet(
			derivedio.SH(tagGraphicLayer, "LAYER1"),
			derivedio.Seq(tagTextObjectSequence,
				derivedio.DataSet(derivedio.Str(tagUnformattedTextValue, core.VRUT, "First")),
				derivedio.DataSet(derivedio.Str(tagUnformattedTextValue, core.VRUT, "Second")),
			),
			derivedio.Seq(tagGraphicObjectSequence,
				derivedio.DataSet(
					derivedio.CS(tagGraphicType, GraphicTypePolyline),
					derivedio.DS(tagGraphicData, 1, 1, 2, 2),
				),
				derivedio.DataSet(
					derivedio.CS(tagGraphicType, GraphicTypeCircle),
					derivedio.DS(tagGraphicData, 10, 10, 12, 10),
				),
			),
		)),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(state.GraphicAnnotations) != 4 {
		t.Fatalf("GraphicAnnotations = %d, want four independent text/graphic objects", len(state.GraphicAnnotations))
	}
	for i, annotation := range state.GraphicAnnotations {
		if annotation.GroupIndex != 1 {
			t.Fatalf("GraphicAnnotations[%d].GroupIndex = %d, want 1", i, annotation.GroupIndex)
		}
	}

	written, err := Write(state)
	if err != nil {
		t.Fatalf("Write after Read: %v", err)
	}
	annotations, ok := written.Dataset.GetSequence(tagGraphicAnnotationSequence)
	if !ok || len(annotations) != 1 {
		t.Fatalf("GraphicAnnotationSequence ok=%v len=%d, want original single item", ok, len(annotations))
	}
	texts, ok := annotations[0].GetSequence(tagTextObjectSequence)
	if !ok || len(texts) != 2 {
		t.Fatalf("TextObjectSequence ok=%v len=%d, want two items", ok, len(texts))
	}
	graphics, ok := annotations[0].GetSequence(tagGraphicObjectSequence)
	if !ok || len(graphics) != 2 {
		t.Fatalf("GraphicObjectSequence ok=%v len=%d, want two items", ok, len(graphics))
	}
}

func TestGSPSApplyConvertsDisplayAnnotationUnitsToPixelSpace(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.306.1"),
		derivedio.UI(derivedio.TagStudyInstanceUID, "1.2.826.0.1.3680043.9.7433.306.2"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, "1.2.826.0.1.3680043.9.7433.306.3"),
		derivedio.Seq(tagDisplayedAreaSelectionSeq, derivedio.DataSet(
			derivedio.IS(tagDisplayedAreaTopLeft, 1, 1),
			derivedio.IS(tagDisplayedAreaBottomRight, 200, 100),
		)),
		derivedio.Seq(tagGraphicAnnotationSequence, derivedio.DataSet(
			derivedio.SH(tagGraphicLayer, "DISPLAY_UNITS"),
			derivedio.Seq(tagTextObjectSequence, derivedio.DataSet(
				derivedio.Str(tagUnformattedTextValue, core.VRUT, "Normalized"),
				derivedio.CS(tagAnchorPointAnnotationUnits, "DISPLAY"),
				derivedio.DS(tagAnchorPoint, 0.25, 0.75),
			)),
			derivedio.Seq(tagGraphicObjectSequence, derivedio.DataSet(
				derivedio.CS(tagGraphicAnnotationUnits, "DISPLAY"),
				derivedio.CS(tagGraphicType, GraphicTypePolyline),
				derivedio.DS(tagGraphicData, 0, 0, 1, 1),
			)),
		)),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(state.GraphicAnnotations) != 2 {
		t.Fatalf("GraphicAnnotations = %d, want separate text and graphic objects", len(state.GraphicAnnotations))
	}
	if state.GraphicAnnotations[0].AnchorUnits != AnnotationUnitsDisplay || state.GraphicAnnotations[1].GraphicUnits != AnnotationUnitsDisplay {
		t.Fatalf("read units = anchor %q graphic %q, want DISPLAY/DISPLAY", state.GraphicAnnotations[0].AnchorUnits, state.GraphicAnnotations[1].GraphicUnits)
	}
	written, err := Write(state)
	if err != nil {
		t.Fatalf("Write after Read: %v", err)
	}
	writtenAnnotations, _ := written.Dataset.GetSequence(tagGraphicAnnotationSequence)
	writtenTexts, _ := writtenAnnotations[0].GetSequence(tagTextObjectSequence)
	writtenGraphics, _ := writtenAnnotations[0].GetSequence(tagGraphicObjectSequence)
	if got, _ := writtenTexts[0].GetString(tagAnchorPointAnnotationUnits); got != AnnotationUnitsDisplay {
		t.Fatalf("round-trip anchor units = %q, want DISPLAY", got)
	}
	if got, _ := writtenGraphics[0].GetString(tagGraphicAnnotationUnits); got != AnnotationUnitsDisplay {
		t.Fatalf("round-trip graphic units = %q, want DISPLAY", got)
	}
	applied, err := Apply(state, ImageState{Rows: 100, Columns: 200})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(applied.GraphicAnnotations) != 2 {
		t.Fatalf("applied annotations = %d, want separate text and graphic objects", len(applied.GraphicAnnotations))
	}
	textAnnotation := applied.GraphicAnnotations[0]
	graphicAnnotation := applied.GraphicAnnotations[1]
	if textAnnotation.Anchor != (Point2D{X: 50, Y: 75}) {
		t.Fatalf("DISPLAY anchor = %+v, want pixel 50,75", textAnnotation.Anchor)
	}
	if !pointsEqual(graphicAnnotation.Polyline, []Point2D{{X: 0, Y: 0}, {X: 200, Y: 100}}) {
		t.Fatalf("DISPLAY polyline = %+v, want image-edge pixel coordinates", graphicAnnotation.Polyline)
	}
	if textAnnotation.AnchorUnits != AnnotationUnitsPixel || graphicAnnotation.GraphicUnits != AnnotationUnitsPixel {
		t.Fatalf("applied units = anchor %q graphic %q, want PIXEL/PIXEL", textAnnotation.AnchorUnits, graphicAnnotation.GraphicUnits)
	}
	if state.GraphicAnnotations[0].Anchor != (Point2D{X: 0.25, Y: 0.75}) {
		t.Fatalf("Apply mutated source annotation: %+v", state.GraphicAnnotations[0])
	}
	if !pointsEqual(state.GraphicAnnotations[1].Polyline, []Point2D{{X: 0, Y: 0}, {X: 1, Y: 1}}) {
		t.Fatalf("Apply mutated source polyline: %+v", state.GraphicAnnotations[1].Polyline)
	}
}

func TestGSPSApplyMapsDisplayUnitsThroughSpecifiedDisplayedArea(t *testing.T) {
	state := &State{
		DisplayedArea: DisplayedArea{TopLeftX: 11, TopLeftY: 21, BottomRightX: 110, BottomRightY: 70},
		GraphicAnnotations: []GraphicAnnotation{{
			Text:        "Crop center",
			Anchor:      Point2D{X: 0.5, Y: 0.5},
			AnchorUnits: AnnotationUnitsDisplay,
		}},
	}

	applied, err := Apply(state, ImageState{Rows: 100, Columns: 200})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if got := applied.GraphicAnnotations[0].Anchor; got != (Point2D{X: 60, Y: 45}) {
		t.Fatalf("cropped DISPLAY center = %+v, want pixel 60,45", got)
	}
}

func TestGSPSApplySelectsSoftcopyVoiForReferencedImage(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.Seq(tagSoftcopyVOILUTSequence,
			derivedio.DataSet(
				derivedio.Seq(tagReferencedImageSequence, derivedio.DataSet(
					derivedio.UI(derivedio.TagRefSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"),
					derivedio.UI(derivedio.TagRefSOPInstanceUID, "image-1"),
				)),
				derivedio.DS(tagWindowCenter, 10),
				derivedio.DS(tagWindowWidth, 20),
			),
			derivedio.DataSet(
				derivedio.Seq(tagReferencedImageSequence, derivedio.DataSet(
					derivedio.UI(derivedio.TagRefSOPClassUID, "1.2.840.10008.5.1.4.1.1.2"),
					derivedio.UI(derivedio.TagRefSOPInstanceUID, "image-2"),
				)),
				derivedio.DS(tagWindowCenter, 100),
				derivedio.DS(tagWindowWidth, 200),
			),
		),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	applied, err := Apply(state, ImageState{SOPInstanceUID: "image-2"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.WindowCenter != 100 || applied.WindowWidth != 200 {
		t.Fatalf("selected window = %g/%g, want image-2 window 100/200", applied.WindowCenter, applied.WindowWidth)
	}
	if len(state.SoftcopyVOIs) != 2 {
		t.Fatalf("SoftcopyVOIs = %d, want 2", len(state.SoftcopyVOIs))
	}
}

func TestGSPSReadLegacySoftcopyVOIPrefersUnscopedItem(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.Seq(tagSoftcopyVOILUTSequence,
			derivedio.DataSet(
				derivedio.Seq(tagReferencedImageSequence, derivedio.DataSet(
					derivedio.UI(derivedio.TagRefSOPInstanceUID, "1.2.3.1"),
				)),
				derivedio.DS(tagWindowCenter, 10),
				derivedio.DS(tagWindowWidth, 20),
			),
			derivedio.DataSet(
				derivedio.DS(tagWindowCenter, 100),
				derivedio.DS(tagWindowWidth, 200),
			),
		),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(state.SoftcopyVOIs) != 2 {
		t.Fatalf("SoftcopyVOIs = %d, want 2", len(state.SoftcopyVOIs))
	}
	if state.SoftcopyVOI.WindowCenter != 100 || state.SoftcopyVOI.WindowWidth != 200 {
		t.Fatalf("legacy SoftcopyVOI = %g/%g, want unscoped 100/200", state.SoftcopyVOI.WindowCenter, state.SoftcopyVOI.WindowWidth)
	}
}

func TestGSPSApplySelectsSoftcopyVoiForReferencedFrame(t *testing.T) {
	state := &State{SoftcopyVOIs: []SoftcopyVOI{
		{WindowCenter: 10, WindowWidth: 20, ReferencedImages: []ReferencedImage{{SOPInstanceUID: "multiframe", Frames: []int{1}}}},
		{WindowCenter: 30, WindowWidth: 40, ReferencedImages: []ReferencedImage{{SOPInstanceUID: "multiframe", Frames: []int{2}}}},
	}}

	applied, err := Apply(state, ImageState{SOPInstanceUID: "multiframe", FrameNumber: 2})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.WindowCenter != 30 || applied.WindowWidth != 40 {
		t.Fatalf("selected frame window = %g/%g, want frame-2 window 30/40", applied.WindowCenter, applied.WindowWidth)
	}
	if softcopyVOIApplies(state.SoftcopyVOIs[0], ImageState{SOPInstanceUID: "multiframe"}) {
		t.Fatal("frame-scoped VOI matched an image with unknown frame number")
	}
}

func TestGSPSWriteRejectsInvalidAnnotationCoordinates(t *testing.T) {
	tests := []struct {
		name       string
		annotation GraphicAnnotation
		want       error
	}{
		{name: "unsupported anchor units", annotation: GraphicAnnotation{Text: "note", AnchorUnits: "MATRIX"}, want: ErrUnsupportedUnits},
		{name: "non-finite pixel anchor", annotation: GraphicAnnotation{Text: "note", AnchorUnits: AnnotationUnitsPixel, Anchor: Point2D{X: math.NaN()}}, want: ErrInvalidObject},
		{name: "display anchor below range", annotation: GraphicAnnotation{Text: "note", AnchorUnits: AnnotationUnitsDisplay, Anchor: Point2D{X: -0.01, Y: 0.5}}, want: ErrInvalidObject},
		{name: "display graphic above range", annotation: GraphicAnnotation{GraphicUnits: AnnotationUnitsDisplay, Polyline: []Point2D{{X: 0.5, Y: 1.01}}}, want: ErrInvalidObject},
		{name: "non-finite graphic point", annotation: GraphicAnnotation{GraphicUnits: AnnotationUnitsPixel, Polyline: []Point2D{{X: math.Inf(1)}}}, want: ErrInvalidObject},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Write(&State{
				SOPInstanceUID:     "1.2.3.4",
				StudyInstanceUID:   "1.2.3",
				SeriesInstanceUID:  "1.2.3.1",
				DisplayedArea:      DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
				GraphicAnnotations: []GraphicAnnotation{tt.annotation},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("Write() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestGSPSReadWriteApplyPreservesVoiLut(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, GrayscaleSoftcopyPresentationStateStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, "1.2.826.0.1.3680043.9.7433.307.1"),
		derivedio.UI(derivedio.TagStudyInstanceUID, "1.2.826.0.1.3680043.9.7433.307.2"),
		derivedio.UI(derivedio.TagSeriesInstanceUID, "1.2.826.0.1.3680043.9.7433.307.3"),
		derivedio.Seq(tagDisplayedAreaSelectionSeq, derivedio.DataSet(
			derivedio.IS(tagDisplayedAreaTopLeft, 1, 1),
			derivedio.IS(tagDisplayedAreaBottomRight, 512, 512),
		)),
		derivedio.Seq(tagSoftcopyVOILUTSequence, derivedio.DataSet(
			derivedio.Seq(tagVOILUTSequence, derivedio.DataSet(
				derivedio.Raw(tagLUTDescriptor, core.VRUS, derivedio.Uint16Bytes([]uint16{3, 10, 16})),
				derivedio.LO(tagLUTExplanation, "Bone LUT"),
				derivedio.Raw(tagLUTData, core.VROW, derivedio.Uint16Bytes([]uint16{0, 2000, 4000})),
			)),
		)),
	}, std.Dictionary)

	state, err := Read(dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if state.SoftcopyVOI.LUT == nil || state.SoftcopyVOI.LUT.Lookup(11) != 2000 || state.SoftcopyVOI.Explanation != "Bone LUT" {
		t.Fatalf("read VOI LUT = %+v explanation %q", state.SoftcopyVOI.LUT, state.SoftcopyVOI.Explanation)
	}
	applied, err := Apply(state, ImageState{})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.VOI.LUT == nil || applied.VOI.LUT.Lookup(12) != 4000 {
		t.Fatalf("applied VOI LUT = %+v, want last entry 4000", applied.VOI.LUT)
	}

	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write after Read: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}
	if roundTrip.SoftcopyVOI.LUT == nil || roundTrip.SoftcopyVOI.LUT.Lookup(11) != 2000 || roundTrip.SoftcopyVOI.Explanation != "Bone LUT" {
		t.Fatalf("round-trip VOI LUT = %+v explanation %q", roundTrip.SoftcopyVOI.LUT, roundTrip.SoftcopyVOI.Explanation)
	}
}

func TestGSPSApplyRejectsNilState(t *testing.T) {
	_, err := Apply(nil, ImageState{SOPInstanceUID: "1.2.3.4.1"})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Apply(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestGSPSApplyRejectsUnreferencedImage(t *testing.T) {
	state := &State{
		SOPInstanceUID: "1.2.3.5.1",
		ReferencedImages: []ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.3.4.1",
		}},
	}
	_, err := Apply(state, ImageState{SOPInstanceUID: "9.9.9.1"})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("Apply(unreferenced image) error = %v, want ErrMissingReference", err)
	}
}

func TestGSPSApplySucceedsWhenNoReferencedImagesSet(t *testing.T) {
	// When the state has no referenced images, any image UID is accepted.
	state := &State{
		SOPInstanceUID:   "1.2.3.5.1",
		ReferencedImages: nil,
		SoftcopyVOI:      SoftcopyVOI{WindowCenter: 50, WindowWidth: 200},
	}
	applied, err := Apply(state, ImageState{SOPInstanceUID: "1.2.3.4.99"})
	if err != nil {
		t.Fatalf("Apply without referenced images: %v", err)
	}
	if applied.WindowCenter != 50 || applied.WindowWidth != 200 {
		t.Fatalf("window = %.1f/%.1f, want 50/200", applied.WindowCenter, applied.WindowWidth)
	}
}

func TestGSPSWriteUsesDefaultSopClassWhenEmpty(t *testing.T) {
	state := &State{
		SOPInstanceUID:    "1.2.3.5.2",
		StudyInstanceUID:  "1.2.3.6",
		SeriesInstanceUID: "1.2.3.7",
		ReferencedImages:  []ReferencedImage{{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.4.1"}},
		DisplayedArea:     DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
	}
	// SOPClassUID is empty — should default to GSPS.
	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write with empty SOPClassUID: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if roundTrip.SOPClassUID != GrayscaleSoftcopyPresentationStateStorage {
		t.Fatalf("SOPClassUID = %q, want GSPS default", roundTrip.SOPClassUID)
	}
}

func TestGSPSWriteRejectsUndefinedDisplayedArea(t *testing.T) {
	_, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.309.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.309.2",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.309.3",
	})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Write error = %v, want ErrInvalidObject for missing Displayed Area", err)
	}
}

func TestGSPSWriteAllowsExplicitZeroOriginDisplayedArea(t *testing.T) {
	file, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.309.10",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.309.11",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.309.12",
		DisplayedArea:     DisplayedArea{Defined: true},
	})
	if err != nil {
		t.Fatalf("Write explicit zero-origin area: %v", err)
	}
	items, ok := file.Dataset.GetSequence(tagDisplayedAreaSelectionSeq)
	if !ok || len(items) != 1 {
		t.Fatalf("DisplayedAreaSelectionSequence ok=%v len=%d, want one item", ok, len(items))
	}
	topLeft := derivedio.Ints(items[0], tagDisplayedAreaTopLeft)
	if len(topLeft) != 2 || topLeft[0] != 0 || topLeft[1] != 0 {
		t.Fatalf("DisplayedAreaTopLeft = %v, want 0\\0", topLeft)
	}
}

func TestGSPSWriteOmitsUndefinedSoftcopyVoi(t *testing.T) {
	file, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.309.4",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.309.5",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.309.6",
		DisplayedArea:     DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if _, ok := file.Dataset.Get(tagSoftcopyVOILUTSequence); ok {
		t.Fatal("SoftcopyVOILUTSequence present for undefined VOI")
	}
}

func TestGSPSWriteRejectsInvalidLinearWindowWidth(t *testing.T) {
	_, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.309.7",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.309.8",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.309.9",
		DisplayedArea:     DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		SoftcopyVOI:       SoftcopyVOI{WindowCenter: 40, WindowWidth: 0.5},
	})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Write error = %v, want ErrInvalidObject for LINEAR width below 1", err)
	}
}

func TestGSPSPresentationLUTDefaultsToIdentityWhenEmpty(t *testing.T) {
	state := &State{
		SOPInstanceUID:    "1.2.3.5.3",
		StudyInstanceUID:  "1.2.3.6",
		SeriesInstanceUID: "1.2.3.7",
		ReferencedImages:  []ReferencedImage{{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.4.1"}},
		DisplayedArea:     DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		// PresentationLUTShape intentionally omitted
	}
	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	applied, err := Apply(roundTrip, ImageState{SOPInstanceUID: "1.2.3.4.1"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.PresentationLUT != PresentationLUTIdentity {
		t.Fatalf("PresentationLUT = %q, want IDENTITY", applied.PresentationLUT)
	}
}

func TestGSPSWritePreservesNoFlipAsN(t *testing.T) {
	state := &State{
		SOPInstanceUID:    "1.2.3.5.4",
		StudyInstanceUID:  "1.2.3.6",
		SeriesInstanceUID: "1.2.3.7",
		ReferencedImages:  []ReferencedImage{{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.4.1"}},
		DisplayedArea:     DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		SpatialTransform:  SpatialTransform{FlipHorizontal: false},
	}
	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}
	readFile, err := object.ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := Read(readFile.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	applied, err := Apply(roundTrip, ImageState{SOPInstanceUID: "1.2.3.4.1"})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if applied.FlipHorizontal {
		t.Fatal("FlipHorizontal should be false")
	}
}

func TestGSPSReferencedSeriesSequenceGroupsBySeries(t *testing.T) {
	state := &State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.2",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.10",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.11",
		ReferencedImages: []ReferencedImage{
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.4.1"},
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.2", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.4.2"},
		},
		DisplayedArea: DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
	}
	file, err := Write(state)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	items, ok := file.Dataset.GetSequence(tagReferencedSeriesSequence)
	if !ok || len(items) != 2 {
		t.Fatalf("ReferencedSeriesSequence = len %d ok=%v, want 2 items", len(items), ok)
	}
	for i, want := range []string{"1.2.826.0.1.3680043.9.7433.11.20.1", "1.2.826.0.1.3680043.9.7433.11.20.2"} {
		got := derivedUIDForTest(t, items[i], core.NewTag(0x0020, 0x000E))
		if got != want {
			t.Fatalf("ReferencedSeriesSequence[%d] SeriesInstanceUID = %q, want %q", i, got, want)
		}
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if roundTrip.ReferencedImages[1].SeriesInstanceUID != "1.2.826.0.1.3680043.9.7433.11.20.2" {
		t.Fatalf("ReferencedImages = %+v, want series UID preserved", roundTrip.ReferencedImages)
	}
}

func TestGSPSWriteRejectsReferencedImageWithoutSeriesUid(t *testing.T) {
	_, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.3",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.12",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.13",
		ReferencedImages: []ReferencedImage{{
			SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID: "1.2.3.4.1",
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("Write error = %v, want ErrMissingReference", err)
	}
}

func TestGSPSWriteRejectsPartialReferencedImageUids(t *testing.T) {
	tests := []struct {
		name string
		ref  ReferencedImage
	}{
		{
			name: "missing SOP class",
			ref: ReferencedImage{
				SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1",
				SOPInstanceUID:    "1.2.3.4.1",
			},
		},
		{
			name: "missing SOP instance",
			ref: ReferencedImage{
				SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.20.1",
				SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Write(&State{
				SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.3",
				StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.12",
				SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.13",
				ReferencedImages:  []ReferencedImage{tt.ref},
			})
			if !errors.Is(err, ErrMissingReference) {
				t.Fatalf("Write error = %v, want ErrMissingReference", err)
			}
		})
	}
}

func TestGSPSWriteIncludesRequiredGraphicLayerAndAnnotationTags(t *testing.T) {
	file, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.30",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.10",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.11",
		ReferencedImages: []ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.31",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.3.4.1",
		}},
		DisplayedArea: DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		SoftcopyVOI:   SoftcopyVOI{WindowCenter: 40, WindowWidth: 400},
		GraphicAnnotations: []GraphicAnnotation{{
			LayerName: "MEASUREMENTS",
			Text:      "Rectangle",
			Anchor:    Point2D{X: 10, Y: 11},
			Polyline:  []Point2D{{X: 10, Y: 11}, {X: 20, Y: 11}, {X: 20, Y: 21}, {X: 10, Y: 21}, {X: 10, Y: 11}},
		}},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	requireElementVR(t, file.Dataset, tagImageRotation, core.VRUS)
	layers, ok := file.Dataset.GetSequence(tagGraphicLayerSequence)
	if !ok || len(layers) != 1 {
		t.Fatalf("GraphicLayerSequence ok=%v len=%d, want one layer", ok, len(layers))
	}
	if got, _ := layers[0].GetString(tagGraphicLayer); got != "MEASUREMENTS" {
		t.Fatalf("GraphicLayer = %q, want MEASUREMENTS", got)
	}
	if got, err := layers[0].GetInt(tagGraphicLayerOrder); err != nil || got != 1 {
		t.Fatalf("GraphicLayerOrder = %d err=%v, want 1", got, err)
	}
	annotations, ok := file.Dataset.GetSequence(tagGraphicAnnotationSequence)
	if !ok || len(annotations) != 1 {
		t.Fatalf("GraphicAnnotationSequence ok=%v len=%d, want one item", ok, len(annotations))
	}
	textItems, ok := annotations[0].GetSequence(tagTextObjectSequence)
	if !ok || len(textItems) != 1 {
		t.Fatalf("TextObjectSequence ok=%v len=%d, want one text item", ok, len(textItems))
	}
	if got, _ := textItems[0].GetString(tagAnchorPointAnnotationUnits); got != "PIXEL" {
		t.Fatalf("AnchorPointAnnotationUnits = %q, want PIXEL", got)
	}
	if got, _ := textItems[0].GetString(tagAnchorPointVisibility); got != "N" {
		t.Fatalf("AnchorPointVisibility = %q, want N", got)
	}
	requireElementVR(t, textItems[0], tagAnchorPoint, core.VRFL)
	graphics, ok := annotations[0].GetSequence(tagGraphicObjectSequence)
	if !ok || len(graphics) != 1 {
		t.Fatalf("GraphicObjectSequence ok=%v len=%d, want one graphic item", ok, len(graphics))
	}
	if got, _ := graphics[0].GetString(tagGraphicAnnotationUnits); got != "PIXEL" {
		t.Fatalf("GraphicAnnotationUnits = %q, want PIXEL", got)
	}
	dimensions, ok := graphics[0].GetRaw(tagGraphicDimensions)
	if !ok || len(dimensions) != 2 || binary.LittleEndian.Uint16(dimensions) != 2 {
		t.Fatalf("GraphicDimensions raw = %v ok=%v, want US 2", dimensions, ok)
	}
	if got, _ := graphics[0].GetString(tagGraphicFilled); got != "N" {
		t.Fatalf("GraphicFilled = %q, want N for closed polyline", got)
	}
	requireElementVR(t, graphics[0], tagNumberOfGraphicPoints, core.VRUS)
	requireElementVR(t, graphics[0], tagGraphicData, core.VRFL)
	displayedAreas, ok := file.Dataset.GetSequence(tagDisplayedAreaSelectionSeq)
	if !ok || len(displayedAreas) != 1 {
		t.Fatalf("DisplayedAreaSelectionSequence ok=%v len=%d, want one item", ok, len(displayedAreas))
	}
	requireElementVR(t, displayedAreas[0], tagDisplayedAreaTopLeft, core.VRSL)
	requireElementVR(t, displayedAreas[0], tagDisplayedAreaBottomRight, core.VRSL)
}

func TestGSPSWriteSetsGraphicFilledForClosedCircle(t *testing.T) {
	file, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.132",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.110",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.111",
		ReferencedImages: []ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.131",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.3.4.1",
		}},
		DisplayedArea: DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		GraphicAnnotations: []GraphicAnnotation{{
			LayerName:   "MEASUREMENTS",
			GraphicType: GraphicTypeCircle,
			Polyline:    []Point2D{{X: 10, Y: 10}, {X: 14, Y: 10}},
		}},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	annotations, ok := file.Dataset.GetSequence(tagGraphicAnnotationSequence)
	if !ok || len(annotations) != 1 {
		t.Fatalf("GraphicAnnotationSequence ok=%v len=%d, want one item", ok, len(annotations))
	}
	graphics, ok := annotations[0].GetSequence(tagGraphicObjectSequence)
	if !ok || len(graphics) != 1 {
		t.Fatalf("GraphicObjectSequence ok=%v len=%d, want one graphic item", ok, len(graphics))
	}
	if got, _ := graphics[0].GetString(tagGraphicFilled); got != "N" {
		t.Fatalf("GraphicFilled = %q, want N for closed circle", got)
	}
}

func TestGSPSWriteOutputIsDeterministic(t *testing.T) {
	first := writeGSPSTestBytes(t)
	second := writeGSPSTestBytes(t)
	if !bytes.Equal(first, second) {
		t.Fatal("GSPS bytes differ for identical presentation state input")
	}
}

func writeGSPSTestBytes(t *testing.T) []byte {
	t.Helper()
	file, err := Write(&State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.11.32",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.11.10",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.11",
		ReferencedImages: []ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.11.31",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.3.4.1",
		}},
		DisplayedArea: DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		SoftcopyVOI:   SoftcopyVOI{WindowCenter: 40, WindowWidth: 400},
		GraphicAnnotations: []GraphicAnnotation{{
			LayerName: "MEASUREMENTS",
			Text:      "Length",
			Anchor:    Point2D{X: 10, Y: 11},
			Polyline:  []Point2D{{X: 10, Y: 11}, {X: 20, Y: 21}},
		}},
	})
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}
	return buf.Bytes()
}

func derivedUIDForTest(t *testing.T, obj *object.Object, tag core.Tag) string {
	t.Helper()
	value, ok := obj.GetUID(tag)
	if !ok {
		t.Fatalf("missing UID tag %s", tag)
	}
	return value
}

func pointsEqual(a, b []Point2D) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func requireElementVR(t *testing.T, obj *object.Object, tag core.Tag, want core.VR) {
	t.Helper()
	element, ok := obj.Get(tag)
	if !ok {
		t.Fatalf("missing element %s", tag)
	}
	if got := element.VR(); got != want {
		t.Fatalf("element %s VR = %s, want %s", tag, got, want)
	}
}
