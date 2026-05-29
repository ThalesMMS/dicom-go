package seg

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/roi"
)

const (
	SegmentationStorage         = "1.2.840.10008.5.1.4.1.1.66.4"
	LabelMapSegmentationStorage = "1.2.840.10008.5.1.4.1.1.66.7"

	SegmentationTypeBinary     = "BINARY"
	SegmentationTypeFractional = "FRACTIONAL"
	SegmentationTypeLabelMap   = "LABELMAP"

	AlgorithmManual = "MANUAL"

	// MaxReadFrames bounds the number of frame objects materialized by Read.
	MaxReadFrames = 100_000
	// MaxReadPixels bounds the total number of pixels visited while decoding.
	MaxReadPixels int64 = 1 << 30

	maxReadDimension = 1<<16 - 1
)

var (
	ErrUnsupportedSOPClass         = errors.New("dicom/seg: unsupported SOP class")
	ErrUnsupportedSegmentationType = errors.New("dicom/seg: unsupported segmentation type")
	ErrUnsupportedPixelData        = errors.New("dicom/seg: unsupported Pixel Data")
	ErrUnsupportedFramePayload     = errors.New("dicom/seg: unsupported frame payload")
	ErrResourceLimitExceeded       = errors.New("dicom/seg: resource limit exceeded")
	ErrInvalidObject               = errors.New("dicom/seg: invalid object")
	ErrMissingReference            = errors.New("dicom/seg: missing reference")
)

var (
	tagSourceImageSequence           = core.NewTag(0x0008, 0x2112)
	tagDerivationImageSequence       = core.NewTag(0x0008, 0x9124)
	tagReferencedSeriesSequence      = core.NewTag(0x0008, 0x1115)
	tagReferencedInstanceSequence    = core.NewTag(0x0008, 0x114A)
	tagCodeValue                     = core.NewTag(0x0008, 0x0100)
	tagCodingSchemeDesignator        = core.NewTag(0x0008, 0x0102)
	tagCodeMeaning                   = core.NewTag(0x0008, 0x0104)
	tagSliceThickness                = core.NewTag(0x0018, 0x0050)
	tagSpacingBetweenSlices          = core.NewTag(0x0018, 0x0088)
	tagImagePositionPatient          = core.NewTag(0x0020, 0x0032)
	tagImageOrientationPatient       = core.NewTag(0x0020, 0x0037)
	tagInStackPositionNumber         = core.NewTag(0x0020, 0x9057)
	tagSegmentSequence               = core.NewTag(0x0062, 0x0002)
	tagSegmentationType              = core.NewTag(0x0062, 0x0001)
	tagSegmentedPropertyCategorySeq  = core.NewTag(0x0062, 0x0003)
	tagSegmentNumber                 = core.NewTag(0x0062, 0x0004)
	tagSegmentLabel                  = core.NewTag(0x0062, 0x0005)
	tagSegmentAlgorithmType          = core.NewTag(0x0062, 0x0008)
	tagSegmentIdentificationSequence = core.NewTag(0x0062, 0x000A)
	tagRecommendedDisplayCIELabValue = core.NewTag(0x0062, 0x000D)
	tagSegmentedPropertyTypeSeq      = core.NewTag(0x0062, 0x000F)
	tagPixelSpacing                  = core.NewTag(0x0028, 0x0030)
	tagPixelMeasuresSequence         = core.NewTag(0x0028, 0x9110)
	tagDimensionIndexValues          = core.NewTag(0x0020, 0x9157)
	tagDimensionIndexPointer         = core.NewTag(0x0020, 0x9165)
	tagFunctionalGroupPointer        = core.NewTag(0x0020, 0x9167)
	tagDimensionIndexSequence        = core.NewTag(0x0020, 0x9222)
	tagPlanePositionSequence         = core.NewTag(0x0020, 0x9113)
	tagPlaneOrientationSequence      = core.NewTag(0x0020, 0x9116)
	tagSharedFunctionalGroups        = core.NewTag(0x5200, 0x9229)
	tagPerFrameFunctionalGroups      = core.NewTag(0x5200, 0x9230)
)

type ReferencedImage struct {
	SeriesInstanceUID string
	SOPClassUID       string
	SOPInstanceUID    string
	Frames            []int
}

type CodedEntry struct {
	CodeValue              string
	CodingSchemeDesignator string
	CodeMeaning            string
}

type Segment struct {
	Number                    int
	Label                     string
	AlgorithmType             string
	RecommendedDisplayCIELab  [3]uint16
	SegmentedPropertyCategory CodedEntry
	SegmentedPropertyType     CodedEntry
}

// RGBToDICOMCIELab converts an 8-bit sRGB color into DICOM's PCS-encoded
// CIELab triplet (PS3.3 C.10.7.1.1).
func RGBToDICOMCIELab(rgb [3]int) [3]uint16 {
	r := displaySRGBToLinear(float64(clampDisplayByte(rgb[0])) / 255)
	g := displaySRGBToLinear(float64(clampDisplayByte(rgb[1])) / 255)
	b := displaySRGBToLinear(float64(clampDisplayByte(rgb[2])) / 255)

	x65 := 0.4124564*r + 0.3575761*g + 0.1804375*b
	y65 := 0.2126729*r + 0.7151522*g + 0.0721750*b
	z65 := 0.0193339*r + 0.1191920*g + 0.9503041*b
	x50 := 1.0479298*x65 + 0.0229468*y65 - 0.0501922*z65
	y50 := 0.0296278*x65 + 0.9904345*y65 - 0.0170738*z65
	z50 := -0.0092430*x65 + 0.0150552*y65 + 0.7518743*z65

	fx := displayLabF(x50 / 0.96422)
	fy := displayLabF(y50)
	fz := displayLabF(z50 / 0.82521)
	return [3]uint16{
		scaleDisplayLabWord(116*fy-16, 0, 100),
		scaleDisplayLabWord(500*(fx-fy), -128, 127),
		scaleDisplayLabWord(200*(fy-fz), -128, 127),
	}
}

// DICOMCIELabToRGB converts a DICOM PCS-encoded CIELab triplet to 8-bit sRGB.
func DICOMCIELabToRGB(encoded [3]uint16) [3]int {
	l := float64(encoded[0]) * 100 / 65535
	a := float64(encoded[1])*255/65535 - 128
	b := float64(encoded[2])*255/65535 - 128
	fy := (l + 16) / 116
	fx := fy + a/500
	fz := fy - b/200
	x50 := 0.96422 * displayLabFInverse(fx)
	y50 := displayLabFInverse(fy)
	z50 := 0.82521 * displayLabFInverse(fz)

	x65 := 0.9554734*x50 - 0.0230985*y50 + 0.0632593*z50
	y65 := -0.0283697*x50 + 1.0099955*y50 + 0.0210414*z50
	z65 := 0.0123140*x50 - 0.0205077*y50 + 1.3303659*z50
	r := 3.2404542*x65 - 1.5371385*y65 - 0.4985314*z65
	g := -0.9692660*x65 + 1.8760108*y65 + 0.0415560*z65
	bl := 0.0556434*x65 - 0.2040259*y65 + 1.0572252*z65
	return [3]int{
		int(math.Round(255 * displayLinearToSRGB(r))),
		int(math.Round(255 * displayLinearToSRGB(g))),
		int(math.Round(255 * displayLinearToSRGB(bl))),
	}
}

func displaySRGBToLinear(value float64) float64 {
	if value <= 0.04045 {
		return value / 12.92
	}
	return math.Pow((value+0.055)/1.055, 2.4)
}

func displayLinearToSRGB(value float64) float64 {
	value = math.Max(0, math.Min(1, value))
	if value <= 0.0031308 {
		return 12.92 * value
	}
	return 1.055*math.Pow(value, 1/2.4) - 0.055
}

func displayLabF(value float64) float64 {
	const delta = 6.0 / 29.0
	if value > delta*delta*delta {
		return math.Cbrt(value)
	}
	return value/(3*delta*delta) + 4.0/29.0
}

func displayLabFInverse(value float64) float64 {
	const delta = 6.0 / 29.0
	if value > delta {
		return value * value * value
	}
	return 3 * delta * delta * (value - 4.0/29.0)
}

func scaleDisplayLabWord(value, minValue, maxValue float64) uint16 {
	value = math.Max(minValue, math.Min(maxValue, value))
	return uint16(math.Round((value - minValue) * 65535 / (maxValue - minValue)))
}

func clampDisplayByte(value int) int {
	if value < 0 {
		return 0
	}
	if value > 255 {
		return 255
	}
	return value
}

type Frame struct {
	SegmentNumber            int
	SliceIndex               int
	ReferencedSOPInstanceUID string
	ReferencedFrameNumber    int
	Geometry                 render.SliceGeometry
	Mask                     *roi.RasterMask
	LabelMap                 []uint16
}

type Document struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Rows                int
	Columns             int
	SegmentationType    string
	Segments            []Segment
	ReferencedImages    []ReferencedImage
	Geometry            render.VolumeGeometry
	Frames              []Frame
}

type FromROIOptions struct {
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Segment             Segment
	ReferencedImages    []ReferencedImage
	Segmentation        *roi.Segmentation3D
}

type ROIMask struct {
	Segment     Segment
	SliceIndex  int
	SourceImage ReferencedImage
	Geometry    render.SliceGeometry
	Mask        *roi.RasterMask
}

type FromROIMasksOptions struct {
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Rows                int
	Columns             int
	Geometry            render.VolumeGeometry
	Masks               []ROIMask
}

// FromROI converts a 3D ROI segmentation into a binary segmentation document.
// It returns ErrInvalidObject if the segmentation is nil.
func FromROI(opts FromROIOptions) (*Document, error) {
	if opts.Segmentation == nil {
		return nil, fmt.Errorf("%w: segmentation is required", ErrInvalidObject)
	}
	segment := opts.Segment
	if segment.Number == 0 {
		segment.Number = 1
	}
	masks := make([]ROIMask, 0, len(opts.Segmentation.Slices()))
	for _, slice := range opts.Segmentation.Slices() {
		mask, _ := opts.Segmentation.MaskAt(slice)
		var ref ReferencedImage
		if slice >= 0 && slice < len(opts.ReferencedImages) {
			ref = opts.ReferencedImages[slice]
		}
		masks = append(masks, ROIMask{
			Segment:     segment,
			SliceIndex:  slice,
			SourceImage: ref,
			Mask:        mask,
		})
	}
	doc, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      opts.SOPInstanceUID,
		StudyInstanceUID:    opts.StudyInstanceUID,
		SeriesInstanceUID:   opts.SeriesInstanceUID,
		FrameOfReferenceUID: opts.FrameOfReferenceUID,
		Rows:                opts.Segmentation.Rows,
		Columns:             opts.Segmentation.Columns,
		Geometry:            opts.Segmentation.Geometry,
		Masks:               masks,
	})
	if err != nil {
		return nil, err
	}
	doc.ReferencedImages = append([]ReferencedImage(nil), opts.ReferencedImages...)
	return doc, nil
}

func FromROIMasks(opts FromROIMasksOptions) (*Document, error) {
	if len(opts.Masks) == 0 {
		return nil, fmt.Errorf("%w: at least one ROI mask is required", ErrInvalidObject)
	}
	rows, columns := opts.Rows, opts.Columns
	if rows == 0 || columns == 0 {
		for _, input := range opts.Masks {
			if input.Mask != nil {
				rows = input.Mask.Rows
				columns = input.Mask.Columns
				break
			}
		}
	}
	if rows <= 0 || columns <= 0 {
		return nil, fmt.Errorf("%w: non-positive mask dimensions", ErrInvalidObject)
	}
	doc := &Document{
		SOPClassUID:         SegmentationStorage,
		SOPInstanceUID:      opts.SOPInstanceUID,
		StudyInstanceUID:    opts.StudyInstanceUID,
		SeriesInstanceUID:   opts.SeriesInstanceUID,
		FrameOfReferenceUID: opts.FrameOfReferenceUID,
		Rows:                rows,
		Columns:             columns,
		SegmentationType:    SegmentationTypeBinary,
		Geometry:            opts.Geometry,
	}
	segmentIndexes := map[int]int{}
	referenceIndexes := map[string]int{}
	for i, input := range opts.Masks {
		if input.Mask == nil {
			return nil, fmt.Errorf("%w: ROI mask %d is nil", ErrInvalidObject, i)
		}
		if input.Mask.Empty() {
			return nil, fmt.Errorf("%w: ROI mask %d is empty", ErrInvalidObject, i)
		}
		if input.Mask.Rows != rows || input.Mask.Columns != columns {
			return nil, fmt.Errorf("%w: ROI mask %d dimensions %dx%d do not match %dx%d", ErrInvalidObject, i, input.Mask.Columns, input.Mask.Rows, columns, rows)
		}
		ref := normalizeReference(input.SourceImage)
		if ref.SeriesInstanceUID == "" || ref.SOPClassUID == "" || ref.SOPInstanceUID == "" {
			return nil, fmt.Errorf("%w: ROI mask %d missing source image reference", ErrMissingReference, i)
		}
		segment := normalizeSegment(input.Segment, i+1)
		if existing, ok := segmentIndexes[segment.Number]; ok {
			if !sameSegmentDefinition(doc.Segments[existing], segment) {
				return nil, fmt.Errorf("%w: conflicting segment metadata for segment %d", ErrInvalidObject, segment.Number)
			}
		} else {
			segmentIndexes[segment.Number] = len(doc.Segments)
			doc.Segments = append(doc.Segments, segment)
		}
		key := referenceKey(ref)
		if _, ok := referenceIndexes[key]; !ok {
			referenceIndexes[key] = len(doc.ReferencedImages)
			doc.ReferencedImages = append(doc.ReferencedImages, ref)
		}
		frame := Frame{
			SegmentNumber:            segment.Number,
			SliceIndex:               input.SliceIndex,
			ReferencedSOPInstanceUID: ref.SOPInstanceUID,
			Geometry:                 input.Geometry,
			Mask:                     input.Mask.Clone(),
		}
		if len(ref.Frames) > 0 {
			frame.ReferencedFrameNumber = ref.Frames[0]
		}
		doc.Frames = append(doc.Frames, frame)
	}
	return doc, nil
}

func GenericROICategoryCode() CodedEntry {
	return CodedEntry{CodeValue: "ROI", CodingSchemeDesignator: "99TWIN", CodeMeaning: "Region of Interest"}
}

func GenericROITypeCode() CodedEntry {
	return CodedEntry{CodeValue: "ROI_MASK", CodingSchemeDesignator: "99TWIN", CodeMeaning: "ROI mask"}
}

func normalizeSegment(segment Segment, fallbackNumber int) Segment {
	if segment.Number == 0 {
		segment.Number = fallbackNumber
	}
	segment.Label = strings.TrimSpace(segment.Label)
	if segment.Label == "" {
		segment.Label = fmt.Sprintf("Segment %d", segment.Number)
	}
	segment.AlgorithmType = strings.TrimSpace(segment.AlgorithmType)
	if segment.AlgorithmType == "" {
		segment.AlgorithmType = AlgorithmManual
	}
	segment.SegmentedPropertyCategory = normalizeCode(segment.SegmentedPropertyCategory, GenericROICategoryCode())
	segment.SegmentedPropertyType = normalizeCode(segment.SegmentedPropertyType, GenericROITypeCode())
	return segment
}

func normalizeCode(code CodedEntry, fallback CodedEntry) CodedEntry {
	code.CodeValue = strings.TrimSpace(code.CodeValue)
	code.CodingSchemeDesignator = strings.TrimSpace(code.CodingSchemeDesignator)
	code.CodeMeaning = strings.TrimSpace(code.CodeMeaning)
	if code.CodeValue == "" && code.CodingSchemeDesignator == "" && code.CodeMeaning == "" {
		return fallback
	}
	return code
}

func sameSegmentDefinition(a, b Segment) bool {
	return a.Number == b.Number &&
		a.Label == b.Label &&
		a.AlgorithmType == b.AlgorithmType &&
		a.RecommendedDisplayCIELab == b.RecommendedDisplayCIELab &&
		a.SegmentedPropertyCategory == b.SegmentedPropertyCategory &&
		a.SegmentedPropertyType == b.SegmentedPropertyType
}

func normalizeReference(ref ReferencedImage) ReferencedImage {
	ref.SeriesInstanceUID = strings.TrimSpace(ref.SeriesInstanceUID)
	ref.SOPClassUID = strings.TrimSpace(ref.SOPClassUID)
	ref.SOPInstanceUID = strings.TrimSpace(ref.SOPInstanceUID)
	ref.Frames = append([]int(nil), ref.Frames...)
	return ref
}

func referenceKey(ref ReferencedImage) string {
	var b strings.Builder
	b.WriteString(ref.SeriesInstanceUID)
	b.WriteByte('|')
	b.WriteString(ref.SOPClassUID)
	b.WriteByte('|')
	b.WriteString(ref.SOPInstanceUID)
	for _, frame := range ref.Frames {
		b.WriteString(fmt.Sprintf("|%d", frame))
	}
	return b.String()
}

// ToROI reconstructs a three-dimensional ROI segmentation from a DICOM segmentation document for a specified segment. If segmentNumber is zero, it uses the first segment's number.
//
// Returns the reconstructed segmentation, or an error if doc is nil.
func ToROI(doc *Document, segmentNumber int) (*roi.Segmentation3D, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: document is nil", ErrInvalidObject)
	}
	if segmentNumber == 0 && len(doc.Segments) > 0 {
		segmentNumber = doc.Segments[0].Number
	}
	out := roi.NewSegmentation3D(doc.Geometry, doc.Columns, doc.Rows)
	for _, frame := range doc.Frames {
		if frame.SegmentNumber != segmentNumber {
			continue
		}
		switch {
		case frame.Mask != nil:
			out.SetMask(frame.SliceIndex, frame.Mask)
		case len(frame.LabelMap) > 0:
			if doc.Columns <= 0 {
				return nil, fmt.Errorf("%w: label-map frame has non-positive columns", ErrInvalidObject)
			}
			mask := roi.NewRasterMask(doc.Columns, doc.Rows)
			for idx, value := range frame.LabelMap {
				if int(value) == segmentNumber {
					mask.Set(idx%doc.Columns, idx/doc.Columns, true)
				}
			}
			out.SetMask(frame.SliceIndex, mask)
		}
	}
	return out, nil
}

// Write serializes a Document into a DICOM segmentation file. If the Document's SOP class UID is not set, it defaults to SegmentationStorage (binary masks). It returns an error if the Document is nil or the SOP class UID is unsupported.
func Write(doc *Document) (*object.File, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: document is nil", ErrInvalidObject)
	}
	sopClassUID := doc.SOPClassUID
	if sopClassUID == "" {
		sopClassUID = SegmentationStorage
	}
	if sopClassUID != SegmentationStorage && sopClassUID != LabelMapSegmentationStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	if err := validateRepresentableSegmentationType(sopClassUID, doc.SegmentationType); err != nil {
		return nil, err
	}
	if err := validateReferencedSeries(doc.ReferencedImages); err != nil {
		return nil, err
	}
	if err := validateFramePayloads(doc, sopClassUID); err != nil {
		return nil, err
	}
	if err := validateWriteDimensions(doc); err != nil {
		return nil, err
	}
	if err := validateUnsignedShortFields(doc); err != nil {
		return nil, err
	}
	elements := []core.Element{
		derivedio.UI(derivedio.TagSOPClassUID, sopClassUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, doc.SOPInstanceUID),
		derivedio.CS(derivedio.TagModality, "SEG"),
		derivedio.UI(derivedio.TagStudyInstanceUID, doc.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, doc.SeriesInstanceUID),
		derivedio.UI(derivedio.TagFrameOfReferenceUID, doc.FrameOfReferenceUID),
		derivedio.US(derivedio.TagRows, uint16(doc.Rows)),
		derivedio.US(derivedio.TagColumns, uint16(doc.Columns)),
		derivedio.IS(derivedio.TagNumberOfFrames, len(doc.Frames)),
		derivedio.US(derivedio.TagSamplesPerPixel, 1),
		derivedio.CS(derivedio.TagPhotometricInterpretation, "MONOCHROME2"),
		derivedio.CS(tagSegmentationType, segmentationType(doc)),
		segmentSequence(doc.Segments),
		referencedSeriesSequence(doc.ReferencedImages),
		dimensionIndexSequence(),
	}
	if shared, ok := sharedFunctionalGroupsSequence(doc); ok {
		elements = append(elements, shared)
	}
	elements = append(elements, perFrameSequence(doc))
	switch sopClassUID {
	case LabelMapSegmentationStorage:
		elements = append(elements,
			derivedio.US(derivedio.TagBitsAllocated, 16),
			derivedio.US(derivedio.TagBitsStored, 16),
			derivedio.US(derivedio.TagHighBit, 15),
			derivedio.US(derivedio.TagPixelRepresentation, 0),
			derivedio.Raw(derivedio.TagPixelData, core.VROW, labelMapPixelData(doc)),
		)
	default:
		elements = append(elements,
			derivedio.US(derivedio.TagBitsAllocated, 1),
			derivedio.US(derivedio.TagBitsStored, 1),
			derivedio.US(derivedio.TagHighBit, 0),
			derivedio.US(derivedio.TagPixelRepresentation, 0),
			derivedio.Raw(derivedio.TagPixelData, core.VROB, binaryPixelData(doc)),
		)
	}
	return derivedio.File(sopClassUID, doc.SOPInstanceUID, derivedio.Object(elements...))
}

// Read parses a DICOM segmentation dataset into a Document.
// It returns an error if the dataset is nil, if the SOP class UID is not
// SegmentationStorage or LabelMapSegmentationStorage, or if its segmentation
// type cannot be represented by Document.
func Read(obj *object.Object) (*Document, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClassUID := derivedio.CleanUID(obj, derivedio.TagSOPClassUID)
	if sopClassUID != SegmentationStorage && sopClassUID != LabelMapSegmentationStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	doc := &Document{
		SOPClassUID:         sopClassUID,
		SOPInstanceUID:      derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:    derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID:   derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		FrameOfReferenceUID: derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID),
		Rows:                derivedio.Int(obj, derivedio.TagRows),
		Columns:             derivedio.Int(obj, derivedio.TagColumns),
		SegmentationType:    derivedio.CleanString(obj, tagSegmentationType),
		Segments:            readSegments(obj),
		ReferencedImages:    readReferencedImages(obj),
	}
	if err := validateRepresentableSegmentationType(doc.SOPClassUID, doc.SegmentationType); err != nil {
		return nil, err
	}
	if err := validateReadDimensions(obj, doc); err != nil {
		return nil, err
	}
	if err := validateNativePixelData(obj); err != nil {
		return nil, err
	}
	geometry, err := readVolumeGeometry(obj, doc.Rows, doc.Columns)
	if err != nil {
		return nil, err
	}
	doc.Geometry = geometry
	doc.Frames = readFrames(obj, doc)
	return doc, nil
}

func validateReadDimensions(obj *object.Object, doc *Document) error {
	if doc.Rows <= 0 || doc.Columns <= 0 {
		return fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidObject, doc.Rows, doc.Columns)
	}
	if doc.Rows > maxReadDimension || doc.Columns > maxReadDimension {
		return fmt.Errorf("%w: Rows=%d Columns=%d exceed %d", ErrResourceLimitExceeded, doc.Rows, doc.Columns, maxReadDimension)
	}

	frameCount := len(derivedio.Sequence(obj, tagPerFrameFunctionalGroups))
	if frameCount == 0 {
		value, err := obj.GetInt(derivedio.TagNumberOfFrames)
		if err != nil || value <= 0 {
			return fmt.Errorf("%w: invalid Number Of Frames", ErrInvalidObject)
		}
		if value > int64(MaxReadFrames) {
			return fmt.Errorf("%w: Number Of Frames=%d exceeds %d", ErrResourceLimitExceeded, value, MaxReadFrames)
		}
		frameCount = int(value)
	}
	if frameCount > MaxReadFrames {
		return fmt.Errorf("%w: %d per-frame items exceed %d", ErrResourceLimitExceeded, frameCount, MaxReadFrames)
	}

	pixelsPerFrame := int64(doc.Rows) * int64(doc.Columns)
	if pixelsPerFrame > MaxReadPixels/int64(frameCount) {
		return fmt.Errorf("%w: %d x %d x %d pixels exceed %d", ErrResourceLimitExceeded, doc.Rows, doc.Columns, frameCount, MaxReadPixels)
	}
	return nil
}

func validateWriteDimensions(doc *Document) error {
	if doc.Rows <= 0 || doc.Columns <= 0 {
		return fmt.Errorf("%w: Rows=%d Columns=%d", ErrInvalidObject, doc.Rows, doc.Columns)
	}
	if doc.Rows > maxReadDimension || doc.Columns > maxReadDimension {
		return fmt.Errorf("%w: Rows=%d Columns=%d exceed %d", ErrResourceLimitExceeded, doc.Rows, doc.Columns, maxReadDimension)
	}
	if len(doc.Frames) <= 0 {
		return fmt.Errorf("%w: invalid Number Of Frames", ErrInvalidObject)
	}
	if len(doc.Frames) > MaxReadFrames {
		return fmt.Errorf("%w: Number Of Frames=%d exceeds %d", ErrResourceLimitExceeded, len(doc.Frames), MaxReadFrames)
	}
	return nil
}

func validateNativePixelData(obj *object.Object) error {
	elem, ok := obj.Get(derivedio.TagPixelData)
	if !ok {
		return fmt.Errorf("%w: Pixel Data is missing", ErrInvalidObject)
	}
	switch elem.Value.(type) {
	case core.RawValue:
		return nil
	case core.FragmentSequence:
		return fmt.Errorf("%w: encapsulated Pixel Data", ErrUnsupportedPixelData)
	default:
		return fmt.Errorf("%w: Pixel Data has value type %T", ErrInvalidObject, elem.Value)
	}
}

func readVolumeGeometry(obj *object.Object, rows, columns int) (render.VolumeGeometry, error) {
	template, fallbackSpacing, ok := readSharedSliceGeometry(obj, rows, columns)
	if !ok {
		return render.VolumeGeometry{}, nil
	}

	frameItems := derivedio.Sequence(obj, tagPerFrameFunctionalGroups)
	spatialDimensionIndex := spatialDimensionValueIndex(obj)
	origins := make(map[int]render.Vec3, len(frameItems))
	firstIndex, lastIndex := -1, -1
	for i, item := range frameItems {
		index := i
		if values, valuesOK := readDimensionIndexValues(item); valuesOK {
			if decoded, indexOK := sliceIndexFromDimensionValues(values, spatialDimensionIndex); indexOK {
				index = decoded
			}
		}
		if index < 0 || index >= MaxReadFrames {
			return render.VolumeGeometry{}, fmt.Errorf("%w: slice index %d exceeds %d", ErrResourceLimitExceeded, index, MaxReadFrames-1)
		}
		origin, originOK := readFrameImagePosition(item)
		if !originOK {
			continue
		}
		origins[index] = origin
		if firstIndex < 0 || index < firstIndex {
			firstIndex = index
		}
		if index > lastIndex {
			lastIndex = index
		}
	}
	if len(origins) == 0 {
		return render.VolumeGeometry{
			RowDir:            template.RowDir,
			ColDir:            template.ColDir,
			Normal:            template.Normal,
			MeanSpacing:       fallbackSpacing,
			Regular:           true,
			OrientationStable: true,
			Classification:    render.VolumeSingleSlice,
		}, nil
	}

	step := template.Normal.Scale(fallbackSpacing)
	if firstIndex != lastIndex {
		candidate := origins[lastIndex].Sub(origins[firstIndex]).Scale(1 / float64(lastIndex-firstIndex))
		if candidate.Length() > 0 {
			step = candidate
		}
	}

	slices := make([]render.SliceGeometry, 0, lastIndex+1)
	anchor := origins[firstIndex]
	if step.Length() > 0 {
		for index := 0; index <= lastIndex; index++ {
			geometry := template
			geometry.Origin = anchor.Add(step.Scale(float64(index - firstIndex)))
			if origin, exists := origins[index]; exists {
				geometry.Origin = origin
			}
			slices = append(slices, geometry)
		}
	} else {
		for index := 0; index <= lastIndex; index++ {
			origin, exists := origins[index]
			if !exists {
				continue
			}
			geometry := template
			geometry.Origin = origin
			slices = append(slices, geometry)
		}
	}

	geometry := render.BuildVolumeGeometry(slices, render.DefaultGeometryTolerances())
	if geometry.MeanSpacing <= 0 && fallbackSpacing > 0 {
		geometry.MeanSpacing = fallbackSpacing
	}
	return geometry, nil
}

func readSharedSliceGeometry(obj *object.Object, rows, columns int) (render.SliceGeometry, float64, bool) {
	sharedItems := derivedio.Sequence(obj, tagSharedFunctionalGroups)
	if len(sharedItems) == 0 {
		return render.SliceGeometry{}, 0, false
	}
	pixelMeasures := derivedio.Sequence(sharedItems[0], tagPixelMeasuresSequence)
	orientationItems := derivedio.Sequence(sharedItems[0], tagPlaneOrientationSequence)
	if len(pixelMeasures) == 0 || len(orientationItems) == 0 {
		return render.SliceGeometry{}, 0, false
	}

	spacing, spacingErr := pixelMeasures[0].GetFloats(tagPixelSpacing)
	orientation, orientationErr := orientationItems[0].GetFloats(tagImageOrientationPatient)
	if spacingErr != nil || len(spacing) < 2 || !positiveFinite(spacing[0]) || !positiveFinite(spacing[1]) ||
		orientationErr != nil || len(orientation) < 6 {
		return render.SliceGeometry{}, 0, false
	}
	rowDir := render.Vec3{X: orientation[0], Y: orientation[1], Z: orientation[2]}.Normalize()
	colRaw := render.Vec3{X: orientation[3], Y: orientation[4], Z: orientation[5]}
	colDir := colRaw.Sub(rowDir.Scale(colRaw.Dot(rowDir))).Normalize()
	normal := rowDir.Cross(colDir).Normalize()
	if rowDir == (render.Vec3{}) || colDir == (render.Vec3{}) || normal == (render.Vec3{}) {
		return render.SliceGeometry{}, 0, false
	}

	fallbackSpacing := positiveFloat(pixelMeasures[0], tagSpacingBetweenSlices)
	if fallbackSpacing == 0 {
		fallbackSpacing = positiveFloat(pixelMeasures[0], tagSliceThickness)
	}
	return render.SliceGeometry{
		RowDir: rowDir, ColDir: colDir, Normal: normal,
		RowSpacing: spacing[0], ColSpacing: spacing[1], Rows: rows, Columns: columns,
	}, fallbackSpacing, true
}

func positiveFloat(obj *object.Object, tag core.Tag) float64 {
	values, err := obj.GetFloats(tag)
	if err != nil || len(values) == 0 || !positiveFinite(values[0]) {
		return 0
	}
	return values[0]
}

func positiveFinite(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validateRepresentableSegmentationType(sopClassUID string, segmentationType string) error {
	switch sopClassUID {
	case SegmentationStorage:
		switch segmentationType {
		case "", SegmentationTypeBinary:
			return nil
		case SegmentationTypeFractional:
			return fmt.Errorf("%w: %s samples are not representable as binary masks", ErrUnsupportedSegmentationType, segmentationType)
		default:
			return fmt.Errorf("%w: %q for Segmentation Storage", ErrUnsupportedSegmentationType, segmentationType)
		}
	case LabelMapSegmentationStorage:
		if segmentationType == "" || segmentationType == SegmentationTypeLabelMap {
			return nil
		}
		return fmt.Errorf("%w: %q for Label Map Segmentation Storage", ErrUnsupportedSegmentationType, segmentationType)
	default:
		return fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
}

// segmentationType returns the segmentation type string for the document.
func segmentationType(doc *Document) string {
	if doc.SegmentationType != "" {
		return doc.SegmentationType
	}
	if doc.SOPClassUID == LabelMapSegmentationStorage {
		return SegmentationTypeLabelMap
	}
	return SegmentationTypeBinary
}

func segmentSequence(segments []Segment) core.Element {
	items := make([]core.DataSet, 0, len(segments))
	for i, segment := range segments {
		segment = normalizeSegment(segment, i+1)
		elements := []core.Element{
			derivedio.US(tagSegmentNumber, uint16(segment.Number)),
			derivedio.LO(tagSegmentLabel, segment.Label),
			derivedio.CS(tagSegmentAlgorithmType, segment.AlgorithmType),
			codeSequence(tagSegmentedPropertyCategorySeq, segment.SegmentedPropertyCategory),
			codeSequence(tagSegmentedPropertyTypeSeq, segment.SegmentedPropertyType),
		}
		if segment.RecommendedDisplayCIELab != ([3]uint16{}) {
			elements = append(elements, derivedio.US(tagRecommendedDisplayCIELabValue,
				segment.RecommendedDisplayCIELab[0],
				segment.RecommendedDisplayCIELab[1],
				segment.RecommendedDisplayCIELab[2],
			))
		}
		items = append(items, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagSegmentSequence, items...)
}

func codeSequence(tag core.Tag, code CodedEntry) core.Element {
	return derivedio.Seq(tag, derivedio.DataSet(
		derivedio.SH(tagCodeValue, code.CodeValue),
		derivedio.SH(tagCodingSchemeDesignator, code.CodingSchemeDesignator),
		derivedio.LO(tagCodeMeaning, code.CodeMeaning),
	))
}

// referencedSeriesSequence constructs a DICOM referenced series sequence from the provided images.
func referencedSeriesSequence(refs []ReferencedImage) core.Element {
	type seriesGroup struct {
		seriesUID string
		images    []core.DataSet
	}
	var groups []seriesGroup
	index := map[string]int{}
	for _, ref := range refs {
		if i, ok := index[ref.SeriesInstanceUID]; ok {
			groups[i].images = append(groups[i].images, refDataSet(ref))
			continue
		}
		index[ref.SeriesInstanceUID] = len(groups)
		groups = append(groups, seriesGroup{seriesUID: ref.SeriesInstanceUID, images: []core.DataSet{refDataSet(ref)}})
	}
	items := make([]core.DataSet, 0, len(groups))
	for _, group := range groups {
		items = append(items, derivedio.DataSet(
			derivedio.UI(derivedio.TagSeriesInstanceUID, group.seriesUID),
			derivedio.Seq(tagReferencedInstanceSequence, group.images...),
		))
	}
	return derivedio.Seq(tagReferencedSeriesSequence, items...)
}

func validateReferencedSeries(refs []ReferencedImage) error {
	for _, ref := range refs {
		if ref.SOPInstanceUID != "" && ref.SeriesInstanceUID == "" {
			return fmt.Errorf("%w: referenced image %s missing series UID", ErrMissingReference, ref.SOPInstanceUID)
		}
	}
	return nil
}

func validateFramePayloads(doc *Document, sopClassUID string) error {
	for i, frame := range doc.Frames {
		if frame.Mask != nil && (frame.Mask.Rows != doc.Rows || frame.Mask.Columns != doc.Columns) {
			return fmt.Errorf("%w: frame %d mask dimensions %dx%d do not match %dx%d", ErrInvalidObject, i, frame.Mask.Columns, frame.Mask.Rows, doc.Columns, doc.Rows)
		}
		switch sopClassUID {
		case SegmentationStorage:
			if frame.Mask == nil && labelMapHasValues(frame.LabelMap) {
				return fmt.Errorf("%w: binary frame %d contains only label-map values", ErrUnsupportedFramePayload, i)
			}
		case LabelMapSegmentationStorage:
			if len(frame.LabelMap) == 0 && frame.Mask != nil && !frame.Mask.Empty() {
				return fmt.Errorf("%w: label-map frame %d contains only a binary mask", ErrUnsupportedFramePayload, i)
			}
		}
	}
	return nil
}

func validateUnsignedShortFields(doc *Document) error {
	if doc.Rows <= 0 || doc.Rows > 1<<16-1 || doc.Columns <= 0 || doc.Columns > 1<<16-1 {
		return fmt.Errorf("%w: dimensions %dx%d are outside US", ErrInvalidObject, doc.Columns, doc.Rows)
	}
	for i, segment := range doc.Segments {
		number := normalizeSegment(segment, i+1).Number
		if number <= 0 || number > 1<<16-1 {
			return fmt.Errorf("%w: segment %d number %d is outside US", ErrInvalidObject, i, number)
		}
	}
	for i, frame := range doc.Frames {
		if frame.SegmentNumber <= 0 || frame.SegmentNumber > 1<<16-1 {
			return fmt.Errorf("%w: frame %d segment number %d is outside US", ErrInvalidObject, i, frame.SegmentNumber)
		}
	}
	return nil
}

func labelMapHasValues(values []uint16) bool {
	for _, value := range values {
		if value != 0 {
			return true
		}
	}
	return false
}

// refDataSet builds a DICOM dataset with the referenced image's SOP class and instance UIDs, and frame numbers if available.
func refDataSet(ref ReferencedImage) core.DataSet {
	elements := []core.Element{
		derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID),
		derivedio.UI(derivedio.TagRefSOPInstanceUID, ref.SOPInstanceUID),
	}
	if len(ref.Frames) > 0 {
		elements = append(elements, derivedio.IS(derivedio.TagRefFrameNumber, ref.Frames...))
	}
	return derivedio.DataSet(elements...)
}

func sharedFunctionalGroupsSequence(doc *Document) (core.Element, bool) {
	if doc == nil {
		return core.Element{}, false
	}
	geom, ok := sharedGeometry(doc)
	if !ok {
		return core.Element{}, false
	}
	elements := []core.Element{
		derivedio.Seq(tagPixelMeasuresSequence, derivedio.DataSet(
			derivedio.DS(tagPixelSpacing, geom.RowSpacing, geom.ColSpacing),
			derivedio.DS(tagSliceThickness, geometryThickness(doc.Geometry)),
		)),
		derivedio.Seq(tagPlaneOrientationSequence, derivedio.DataSet(
			derivedio.DS(tagImageOrientationPatient,
				geom.RowDir.X, geom.RowDir.Y, geom.RowDir.Z,
				geom.ColDir.X, geom.ColDir.Y, geom.ColDir.Z,
			),
		)),
	}
	if doc.Geometry.MeanSpacing > 0 {
		elements[0] = derivedio.Seq(tagPixelMeasuresSequence, derivedio.DataSet(
			derivedio.DS(tagPixelSpacing, geom.RowSpacing, geom.ColSpacing),
			derivedio.DS(tagSliceThickness, geometryThickness(doc.Geometry)),
			derivedio.DS(tagSpacingBetweenSlices, doc.Geometry.MeanSpacing),
		))
	}
	return derivedio.Seq(tagSharedFunctionalGroups, derivedio.DataSet(elements...)), true
}

func sharedGeometry(doc *Document) (render.SliceGeometry, bool) {
	for _, frame := range doc.Frames {
		if validSliceGeometry(frame.Geometry) {
			return frame.Geometry, true
		}
	}
	if len(doc.Geometry.Slices) > 0 {
		return doc.Geometry.Slices[0], true
	}
	return render.SliceGeometry{}, false
}

func geometryThickness(geometry render.VolumeGeometry) float64 {
	if geometry.MeanSpacing > 0 {
		return geometry.MeanSpacing
	}
	return 1
}

func dimensionIndexSequence() core.Element {
	return derivedio.Seq(tagDimensionIndexSequence, derivedio.DataSet(
		tagPointerElement(tagDimensionIndexPointer, tagInStackPositionNumber),
		tagPointerElement(tagFunctionalGroupPointer, tagPlanePositionSequence),
	))
}

func tagPointerElement(tag core.Tag, value core.Tag) core.Element {
	var raw [4]byte
	binary.LittleEndian.PutUint16(raw[0:], value.Group)
	binary.LittleEndian.PutUint16(raw[2:], value.Element)
	return core.NewRawElement(tag, core.VRAT, raw[:])
}

// perFrameSequence constructs the per-frame functional groups DICOM sequence, containing segment identification, dimension indices, and reference information for each frame.
func perFrameSequence(doc *Document) core.Element {
	items := make([]core.DataSet, 0, len(doc.Frames))
	for _, frame := range doc.Frames {
		elements := []core.Element{
			derivedio.Seq(tagSegmentIdentificationSequence, derivedio.DataSet(derivedio.US(tagReferencedSegmentNumber(), uint16(frame.SegmentNumber)))),
			dimensionIndexValuesElement(frame),
		}
		if frame.ReferencedSOPInstanceUID != "" {
			elements = append(elements, derivedio.UI(derivedio.TagRefSOPInstanceUID, frame.ReferencedSOPInstanceUID))
		}
		if frame.ReferencedFrameNumber > 0 {
			elements = append(elements, derivedio.IS(derivedio.TagRefFrameNumber, frame.ReferencedFrameNumber))
		}
		if geom, ok := geometryForFrame(doc.Geometry, frame); ok {
			elements = append(elements, derivedio.Seq(tagPlanePositionSequence, derivedio.DataSet(
				derivedio.DS(tagImagePositionPatient, geom.Origin.X, geom.Origin.Y, geom.Origin.Z),
			)))
		}
		if frame.ReferencedSOPInstanceUID != "" {
			elements = append(elements, derivationImageSequence(doc, frame))
		}
		items = append(items, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagPerFrameFunctionalGroups, items...)
}

func geometryForFrame(geometry render.VolumeGeometry, frame Frame) (render.SliceGeometry, bool) {
	if validSliceGeometry(frame.Geometry) {
		return frame.Geometry, true
	}
	if frame.SliceIndex >= 0 && frame.SliceIndex < len(geometry.Slices) {
		return geometry.Slices[frame.SliceIndex], true
	}
	if len(geometry.Slices) == 1 {
		return geometry.Slices[0], true
	}
	return render.SliceGeometry{}, false
}

func validSliceGeometry(geometry render.SliceGeometry) bool {
	return geometry.Rows > 0 &&
		geometry.Columns > 0 &&
		geometry.RowSpacing > 0 &&
		geometry.ColSpacing > 0 &&
		geometry.RowDir.Length() > 0 &&
		geometry.ColDir.Length() > 0
}

func derivationImageSequence(doc *Document, frame Frame) core.Element {
	ref := sourceReferenceForFrame(doc, frame)
	source := []core.Element{}
	if ref.SOPClassUID != "" {
		source = append(source, derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID))
	}
	source = append(source, derivedio.UI(derivedio.TagRefSOPInstanceUID, frame.ReferencedSOPInstanceUID))
	if frame.ReferencedFrameNumber > 0 {
		source = append(source, derivedio.IS(derivedio.TagRefFrameNumber, frame.ReferencedFrameNumber))
	}
	return derivedio.Seq(tagDerivationImageSequence, derivedio.DataSet(
		derivedio.Seq(tagSourceImageSequence, derivedio.DataSet(source...)),
	))
}

func sourceReferenceForFrame(doc *Document, frame Frame) ReferencedImage {
	if doc == nil {
		return ReferencedImage{}
	}
	for _, ref := range doc.ReferencedImages {
		if ref.SOPInstanceUID != frame.ReferencedSOPInstanceUID {
			continue
		}
		if frame.ReferencedFrameNumber == 0 || containsFrame(ref.Frames, frame.ReferencedFrameNumber) || len(ref.Frames) == 0 {
			return ref
		}
	}
	return ReferencedImage{SOPInstanceUID: frame.ReferencedSOPInstanceUID}
}

func containsFrame(frames []int, want int) bool {
	for _, frame := range frames {
		if frame == want {
			return true
		}
	}
	return false
}

// tagReferencedSegmentNumber returns the DICOM tag (0062,000B) for the referenced segment number.
func tagReferencedSegmentNumber() core.Tag {
	return core.NewTag(0x0062, 0x000B)
}

// binaryPixelData encodes binary segmentation masks from the document frames into packed DICOM PixelData bytes, where each bit represents a pixel value.
func binaryPixelData(doc *Document) []byte {
	bitsPerFrame := doc.Rows * doc.Columns
	if bitsPerFrame <= 0 {
		return nil
	}
	totalBits := bitsPerFrame * len(doc.Frames)
	out := make([]byte, (totalBits+7)/8)
	for frameIndex, frame := range doc.Frames {
		if frame.Mask == nil {
			continue
		}
		baseBit := frameIndex * bitsPerFrame
		frame.Mask.ForEachRun(func(y int, run roi.MaskRun) {
			rowBit := baseBit + y*doc.Columns
			setPackedBitRange(out, rowBit+run.Start, rowBit+run.End)
		})
	}
	return out
}

func setPackedBitRange(data []byte, startBit, endBit int) {
	if startBit < 0 {
		startBit = 0
	}
	dataBits := len(data) * 8
	if endBit > dataBits {
		endBit = dataBits
	}
	if startBit >= endBit {
		return
	}
	if offset := startBit & 7; offset != 0 {
		count := minInt(8-offset, endBit-startBit)
		data[startBit>>3] |= lowBitMask(count) << uint(offset)
		startBit += count
	}
	for endBit-startBit >= 8 {
		data[startBit>>3] = 0xFF
		startBit += 8
	}
	if startBit < endBit {
		data[startBit>>3] |= lowBitMask(endBit - startBit)
	}
}

func lowBitMask(count int) byte {
	if count <= 0 {
		return 0
	}
	if count >= 8 {
		return 0xFF
	}
	return byte(1<<uint(count)) - 1
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// labelMapPixelData returns the serialized pixel data for the label-map frames in a document.
func labelMapPixelData(doc *Document) []byte {
	valuesPerFrame := doc.Rows * doc.Columns
	values := make([]uint16, 0, valuesPerFrame*len(doc.Frames))
	for _, frame := range doc.Frames {
		labelMap := frame.LabelMap
		if len(labelMap) < valuesPerFrame {
			padded := make([]uint16, valuesPerFrame)
			copy(padded, labelMap)
			labelMap = padded
		}
		values = append(values, labelMap[:valuesPerFrame]...)
	}
	return derivedio.Uint16Bytes(values)
}

// readSegments reads segment definitions from a DICOM dataset object.
func readSegments(obj *object.Object) []Segment {
	items := derivedio.Sequence(obj, tagSegmentSequence)
	out := make([]Segment, 0, len(items))
	for _, item := range items {
		segment := Segment{
			Number:                    derivedio.Int(item, tagSegmentNumber),
			Label:                     derivedio.CleanString(item, tagSegmentLabel),
			AlgorithmType:             derivedio.CleanString(item, tagSegmentAlgorithmType),
			SegmentedPropertyCategory: readCodeSequence(item, tagSegmentedPropertyCategorySeq),
			SegmentedPropertyType:     readCodeSequence(item, tagSegmentedPropertyTypeSeq),
		}
		if values := derivedio.Ints(item, tagRecommendedDisplayCIELabValue); len(values) >= 3 && uint16ValuesRepresentable(values[:3]) {
			segment.RecommendedDisplayCIELab = [3]uint16{uint16(values[0]), uint16(values[1]), uint16(values[2])}
		}
		out = append(out, segment)
	}
	return out
}

func uint16ValuesRepresentable(values []int64) bool {
	for _, value := range values {
		if value < 0 || value > 1<<16-1 {
			return false
		}
	}
	return true
}

func readCodeSequence(obj *object.Object, tag core.Tag) CodedEntry {
	items := derivedio.Sequence(obj, tag)
	if len(items) == 0 {
		return CodedEntry{}
	}
	return CodedEntry{
		CodeValue:              derivedio.CleanString(items[0], tagCodeValue),
		CodingSchemeDesignator: derivedio.CleanString(items[0], tagCodingSchemeDesignator),
		CodeMeaning:            derivedio.CleanString(items[0], tagCodeMeaning),
	}
}

// readReferencedImages extracts referenced image identifiers from a DICOM segmentation dataset.
// It reads the referenced series and instance sequences and returns the SOP class UIDs,
// SOP instance UIDs, and frame numbers for each referenced image.
func readReferencedImages(obj *object.Object) []ReferencedImage {
	seriesItems := derivedio.Sequence(obj, tagReferencedSeriesSequence)
	var out []ReferencedImage
	for _, series := range seriesItems {
		seriesUID := derivedio.CleanUID(series, derivedio.TagSeriesInstanceUID)
		for _, refObj := range derivedio.Sequence(series, tagReferencedInstanceSequence) {
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

// readFrames extracts frame data from a DICOM segmentation dataset. If per-frame metadata is absent, frames are populated based on the dataset's frame count. Returns a slice of Frame objects with segment information and pixel data.
func readFrames(obj *object.Object, doc *Document) []Frame {
	frameItems := derivedio.Sequence(obj, tagPerFrameFunctionalGroups)
	if len(frameItems) == 0 {
		frameCount := derivedio.Int(obj, derivedio.TagNumberOfFrames)
		frameItems = make([]*object.Object, frameCount)
	}
	switch doc.SOPClassUID {
	case LabelMapSegmentationStorage:
		return readLabelMapFrames(obj, doc, frameItems)
	default:
		return readBinaryFrames(obj, doc, frameItems)
	}
}

// readBinaryFrames reads binary DICOM pixel data and creates Frame objects with raster masks for each frame.
func readBinaryFrames(obj *object.Object, doc *Document, frameItems []*object.Object) []Frame {
	data, _ := obj.GetRaw(derivedio.TagPixelData)
	bitsPerFrame := doc.Rows * doc.Columns
	if bitsPerFrame <= 0 {
		return nil
	}
	spatialDimensionIndex := spatialDimensionValueIndex(obj)
	frames := make([]Frame, 0, len(frameItems))
	frameBitStride := binaryFrameBitStride(bitsPerFrame, len(frameItems), len(data))
	for i, item := range frameItems {
		frame := readFrameItem(item, i, spatialDimensionIndex, doc.Geometry)
		if frame.SegmentNumber == 0 && len(doc.Segments) == 1 {
			frame.SegmentNumber = doc.Segments[0].Number
		}
		baseBit := i * frameBitStride
		frame.Mask = readPackedBinaryMask(data, baseBit, doc.Columns, doc.Rows)
		frames = append(frames, frame)
	}
	return frames
}

func readPackedBinaryMask(data []byte, baseBit, columns, rows int) *roi.RasterMask {
	mask := roi.NewRasterMask(columns, rows)
	if len(data) == 0 || columns <= 0 || rows <= 0 {
		return mask
	}
	dataBits := len(data) * 8
	for y := 0; y < rows; y++ {
		rowStart := baseBit + y*columns
		if rowStart >= dataBits {
			break
		}
		rowEnd := rowStart + columns
		if rowEnd > dataBits {
			rowEnd = dataBits
		}
		activeStart, activeEnd := -1, -1
		for position := rowStart; position < rowEnd; {
			offset := position & 7
			count := minInt(8-offset, rowEnd-position)
			value := (data[position>>3] >> uint(offset)) & lowBitMask(count)
			consumed := 0
			for consumed < count {
				zeros := bits.TrailingZeros8(value >> uint(consumed))
				if zeros >= count-consumed {
					break
				}
				start := consumed + zeros
				ones := bits.TrailingZeros8(^(value >> uint(start)))
				if ones > count-start {
					ones = count - start
				}
				runStart := position - rowStart + start
				runEnd := runStart + ones
				if activeStart < 0 {
					activeStart, activeEnd = runStart, runEnd
				} else if runStart == activeEnd {
					activeEnd = runEnd
				} else {
					mask.SetRun(y, activeStart, activeEnd)
					activeStart, activeEnd = runStart, runEnd
				}
				consumed = start + ones
			}
			position += count
		}
		if activeStart >= 0 {
			mask.SetRun(y, activeStart, activeEnd)
		}
	}
	return mask
}

func binaryFrameBitStride(bitsPerFrame, frameCount, dataBytes int) int {
	bytesPerFrame := (bitsPerFrame + 7) / 8
	legacyBytes := bytesPerFrame * frameCount
	continuousBytes := (bitsPerFrame*frameCount + 7) / 8
	if bitsPerFrame%8 != 0 && dataBytes == legacyBytes && legacyBytes != continuousBytes {
		return bytesPerFrame * 8
	}
	return bitsPerFrame
}

func readLabelMapFrames(obj *object.Object, doc *Document, frameItems []*object.Object) []Frame {
	data, _ := obj.GetRaw(derivedio.TagPixelData)
	values := derivedio.Uint16s(data)
	valuesPerFrame := doc.Rows * doc.Columns
	spatialDimensionIndex := spatialDimensionValueIndex(obj)
	frames := make([]Frame, 0, len(frameItems))
	for i, item := range frameItems {
		frame := readFrameItem(item, i, spatialDimensionIndex, doc.Geometry)
		start := i * valuesPerFrame
		end := start + valuesPerFrame
		if start < len(values) {
			if end > len(values) {
				end = len(values)
			}
			frame.LabelMap = append([]uint16(nil), values[start:end]...)
		}
		if frame.SegmentNumber == 0 && len(doc.Segments) > 0 {
			frame.SegmentNumber = doc.Segments[0].Number
		}
		frames = append(frames, frame)
	}
	return frames
}

// readFrameItem extracts frame metadata from a DICOM per-frame functional group item, using fallbackIndex as the default slice index.
func readFrameItem(item *object.Object, fallbackIndex int, spatialDimensionIndex int, geometry render.VolumeGeometry) Frame {
	frame := Frame{SliceIndex: fallbackIndex}
	if item == nil {
		return frame
	}
	if segItems := derivedio.Sequence(item, tagSegmentIdentificationSequence); len(segItems) > 0 {
		frame.SegmentNumber = derivedio.Int(segItems[0], tagReferencedSegmentNumber())
	}
	if values, ok := readDimensionIndexValues(item); ok {
		if sliceIndex, ok := sliceIndexFromDimensionValues(values, spatialDimensionIndex); ok {
			frame.SliceIndex = sliceIndex
		}
	}
	if frame.SliceIndex >= 0 && frame.SliceIndex < len(geometry.Slices) {
		frame.Geometry = geometry.Slices[frame.SliceIndex]
	}
	if origin, ok := readFrameImagePosition(item); ok {
		frame.Geometry.Origin = origin
		if sliceIndex, ok := sliceIndexFromPlanePosition(origin, geometry); ok {
			frame.SliceIndex = sliceIndex
			frame.Geometry = geometry.Slices[sliceIndex]
		}
	}
	frame.ReferencedSOPInstanceUID = derivedio.CleanUID(item, derivedio.TagRefSOPInstanceUID)
	frame.ReferencedFrameNumber = derivedio.Int(item, derivedio.TagRefFrameNumber)
	return frame
}

func dimensionIndexValuesElement(frame Frame) core.Element {
	value := frame.SliceIndex + 1
	if value < 1 {
		value = 1
	}
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], uint32(value))
	return core.NewRawElement(tagDimensionIndexValues, core.VRUL, raw[:])
}

func spatialDimensionValueIndex(obj *object.Object) int {
	items := derivedio.Sequence(obj, tagDimensionIndexSequence)
	if len(items) == 0 {
		return -1
	}
	for i, item := range items {
		dimensionPointers := readTagValues(item, tagDimensionIndexPointer)
		if tagValueEquals(item, tagDimensionIndexPointer, tagImagePositionPatient) ||
			tagValueEquals(item, tagDimensionIndexPointer, tagInStackPositionNumber) {
			return i
		}
		if len(dimensionPointers) == 0 && tagValueEquals(item, tagFunctionalGroupPointer, tagPlanePositionSequence) {
			return i
		}
	}
	return -1
}

func tagValueEquals(obj *object.Object, sourceTag core.Tag, want core.Tag) bool {
	values := readTagValues(obj, sourceTag)
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func readTagValues(obj *object.Object, tag core.Tag) []core.Tag {
	if obj == nil {
		return nil
	}
	elem, ok := obj.Get(tag)
	if !ok || elem.VR() != core.VRAT {
		return nil
	}
	raw, ok := elem.RawBytes()
	if !ok || len(raw)%4 != 0 {
		return nil
	}
	values := make([]core.Tag, len(raw)/4)
	for i := range values {
		offset := i * 4
		values[i] = core.NewTag(
			binary.LittleEndian.Uint16(raw[offset:]),
			binary.LittleEndian.Uint16(raw[offset+2:]),
		)
	}
	return values
}

func readDimensionIndexValues(obj *object.Object) ([]int, bool) {
	if obj == nil {
		return nil, false
	}
	elem, ok := obj.Get(tagDimensionIndexValues)
	if !ok {
		return nil, false
	}
	switch elem.VR() {
	case core.VRIS:
		values, err := obj.GetInts(tagDimensionIndexValues)
		if err != nil {
			return nil, false
		}
		out := make([]int, len(values))
		for i, value := range values {
			out[i] = int(value)
		}
		return out, true
	case core.VRUL:
		raw, ok := elem.RawBytes()
		if !ok || len(raw)%4 != 0 {
			return nil, false
		}
		out := make([]int, len(raw)/4)
		for i := range out {
			out[i] = int(binary.LittleEndian.Uint32(raw[i*4:]))
		}
		return out, true
	case core.VRUS:
		raw, ok := elem.RawBytes()
		if !ok || len(raw)%2 != 0 {
			return nil, false
		}
		out := make([]int, len(raw)/2)
		for i := range out {
			out[i] = int(binary.LittleEndian.Uint16(raw[i*2:]))
		}
		return out, true
	default:
		return nil, false
	}
}

func sliceIndexFromDimensionValues(values []int, spatialDimensionIndex int) (int, bool) {
	if spatialDimensionIndex < 0 || spatialDimensionIndex >= len(values) {
		return 0, false
	}
	value := values[spatialDimensionIndex]
	if value <= 0 {
		return 0, false
	}
	return value - 1, true
}

func readFrameImagePosition(item *object.Object) (render.Vec3, bool) {
	positionItems := derivedio.Sequence(item, tagPlanePositionSequence)
	if len(positionItems) == 0 {
		return render.Vec3{}, false
	}
	values, err := positionItems[0].GetFloats(tagImagePositionPatient)
	if err != nil || len(values) < 3 {
		return render.Vec3{}, false
	}
	return render.Vec3{X: values[0], Y: values[1], Z: values[2]}, true
}

func sliceIndexFromPlanePosition(origin render.Vec3, geometry render.VolumeGeometry) (int, bool) {
	if len(geometry.Slices) == 0 || geometry.Normal.Length() == 0 {
		return 0, false
	}
	position := origin.Dot(geometry.Normal)
	bestIndex := -1
	bestDistance := math.Inf(1)
	for i, slice := range geometry.Slices {
		distance := math.Abs(slice.PositionAlong(geometry.Normal) - position)
		if distance < bestDistance {
			bestDistance = distance
			bestIndex = i
		}
	}
	if bestIndex < 0 {
		return 0, false
	}
	tolerance := 0.01
	if geometry.MeanSpacing > 0 {
		tolerance = math.Max(tolerance, geometry.MeanSpacing*0.001)
	}
	if bestDistance > tolerance {
		return 0, false
	}
	return bestIndex, true
}
