package rtstruct

import (
	"errors"
	"fmt"
	"image"
	"math"
	"sort"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	dicomroi "github.com/ThalesMMS/dicom-go/roi"
)

const (
	RTStructureSetStorage  = "1.2.840.10008.5.1.4.1.1.481.3"
	ContourPoint           = "POINT"
	ContourOpenPlanar      = "OPEN_PLANAR"
	ContourOpenNonPlanar   = "OPEN_NONPLANAR"
	ContourClosedPlanar    = "CLOSED_PLANAR"
	ContourClosedPlanarXOR = "CLOSEDPLANAR_XOR"

	detachedStudyManagementSOPClass = "1.2.840.10008.3.1.2.3.2"
)

var (
	ErrUnsupportedSOPClass    = errors.New("dicom/rtstruct: unsupported SOP class")
	ErrInvalidObject          = errors.New("dicom/rtstruct: invalid object")
	ErrMissingReference       = errors.New("dicom/rtstruct: missing reference")
	ErrGeometryMismatch       = errors.New("dicom/rtstruct: geometry mismatch")
	ErrUnsupportedContourType = errors.New("dicom/rtstruct: unsupported contour geometric type")
)

var (
	tagStructureSetLabel             = core.NewTag(0x3006, 0x0002)
	tagReferencedFrameOfReferenceSeq = core.NewTag(0x3006, 0x0010)
	tagRTReferencedStudySequence     = core.NewTag(0x3006, 0x0012)
	tagRTReferencedSeriesSequence    = core.NewTag(0x3006, 0x0014)
	tagReferencedFrameOfReferenceUID = core.NewTag(0x3006, 0x0024)
	tagStructureSetROISequence       = core.NewTag(0x3006, 0x0020)
	tagROINumber                     = core.NewTag(0x3006, 0x0022)
	tagROIName                       = core.NewTag(0x3006, 0x0026)
	tagROIContourSequence            = core.NewTag(0x3006, 0x0039)
	tagROIDisplayColor               = core.NewTag(0x3006, 0x002A)
	tagReferencedROINumber           = core.NewTag(0x3006, 0x0084)
	tagContourSequence               = core.NewTag(0x3006, 0x0040)
	tagContourGeometricType          = core.NewTag(0x3006, 0x0042)
	tagNumberOfContourPoints         = core.NewTag(0x3006, 0x0046)
	tagContourData                   = core.NewTag(0x3006, 0x0050)
	tagContourImageSequence          = core.NewTag(0x3006, 0x0016)
	tagRTROIObservationsSequence     = core.NewTag(0x3006, 0x0080)
	tagObservationNumber             = core.NewTag(0x3006, 0x0082)
	tagRTROIInterpretedType          = core.NewTag(0x3006, 0x00A4)
)

type Point3D struct {
	X float64
	Y float64
	Z float64
}

type ReferencedImage struct {
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPClassUID       string
	SOPInstanceUID    string
}

type Contour struct {
	GeometricType    string
	Points           []Point3D
	ReferencedImages []ReferencedImage
}

type ROI struct {
	Number   int
	Name     string
	ColorRGB [3]int
	Contours []Contour
}

type StructureSet struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Label               string
	ROIs                []ROI
}

type RasterizeOptions struct {
	ROINumber int
	Geometry  render.VolumeGeometry
	Columns   int
	Rows      int
}

type VectorROIContour struct {
	ROI              dicomroi.VectorROI
	Geometry         render.SliceGeometry
	ReferencedImages []ReferencedImage
}

type VectorROI struct {
	Number   int
	Name     string
	ColorRGB [3]int
	Contours []VectorROIContour
}

type FromVectorROIsOptions struct {
	SOPClassUID         string
	SOPInstanceUID      string
	StudyInstanceUID    string
	SeriesInstanceUID   string
	FrameOfReferenceUID string
	Label               string
	ROIs                []VectorROI
}

// Write encodes the StructureSet into a DICOM RT Structure Set file.
// It returns an error if set is nil or the SOP Class UID is not RTStructureSetStorage.
func Write(set *StructureSet) (*object.File, error) {
	if set == nil {
		return nil, fmt.Errorf("%w: structure set is nil", ErrInvalidObject)
	}
	sopClassUID := set.SOPClassUID
	if sopClassUID == "" {
		sopClassUID = RTStructureSetStorage
	}
	if sopClassUID != RTStructureSetStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	if err := validateStructureSet(set); err != nil {
		return nil, err
	}
	label := set.Label
	if label == "" {
		label = "TWIN_RTSTRUCT"
	}
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, sopClassUID),
		derivedio.UI(derivedio.TagSOPInstanceUID, set.SOPInstanceUID),
		derivedio.CS(derivedio.TagModality, "RTSTRUCT"),
		derivedio.UI(derivedio.TagStudyInstanceUID, set.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, set.SeriesInstanceUID),
		derivedio.SH(tagStructureSetLabel, label),
		referencedFrameOfReferenceSequence(set),
		structureSetROISequence(set),
		roiContourSequence(set.ROIs),
		rtROIObservationsSequence(set.ROIs),
	)
	return derivedio.File(sopClassUID, set.SOPInstanceUID, dataset)
}

// Read reconstructs a StructureSet from a DICOM RT Structure Set object. It returns an error if the object is nil or does not represent an RT Structure Set.
func Read(obj *object.Object) (*StructureSet, error) {
	if obj == nil {
		return nil, fmt.Errorf("%w: dataset is nil", ErrInvalidObject)
	}
	sopClassUID := derivedio.CleanUID(obj, derivedio.TagSOPClassUID)
	if sopClassUID != RTStructureSetStorage {
		return nil, fmt.Errorf("%w: %s", ErrUnsupportedSOPClass, sopClassUID)
	}
	set := &StructureSet{
		SOPClassUID:       sopClassUID,
		SOPInstanceUID:    derivedio.CleanUID(obj, derivedio.TagSOPInstanceUID),
		StudyInstanceUID:  derivedio.CleanUID(obj, derivedio.TagStudyInstanceUID),
		SeriesInstanceUID: derivedio.CleanUID(obj, derivedio.TagSeriesInstanceUID),
		Label:             derivedio.CleanString(obj, tagStructureSetLabel),
	}
	if refs := derivedio.Sequence(obj, tagReferencedFrameOfReferenceSeq); len(refs) > 0 {
		set.FrameOfReferenceUID = readFrameOfReferenceUID(refs[0])
	}
	referencedImages := readReferencedImageHierarchy(obj)
	roiByNumber := readROIs(obj)
	attachContours(obj, roiByNumber, referencedImages)
	numbers := make([]int, 0, len(roiByNumber))
	for number := range roiByNumber {
		numbers = append(numbers, number)
	}
	sort.Ints(numbers)
	for _, number := range numbers {
		item := roiByNumber[number]
		set.ROIs = append(set.ROIs, *item)
	}
	return set, nil
}

// FromVectorROIs converts image-pixel vector ROIs into patient-space RTSTRUCT
// contours. Callers provide output identifiers, a frame-of-reference UID, per
// contour source slice geometry, and source image references; the returned
// StructureSet can be serialized with Write.
func FromVectorROIs(opts FromVectorROIsOptions) (*StructureSet, error) {
	if opts.FrameOfReferenceUID == "" {
		return nil, fmt.Errorf("%w: missing frame of reference UID", ErrMissingReference)
	}
	out := &StructureSet{
		SOPClassUID:         opts.SOPClassUID,
		SOPInstanceUID:      opts.SOPInstanceUID,
		StudyInstanceUID:    opts.StudyInstanceUID,
		SeriesInstanceUID:   opts.SeriesInstanceUID,
		FrameOfReferenceUID: opts.FrameOfReferenceUID,
		Label:               opts.Label,
	}
	for _, item := range opts.ROIs {
		roiItem := ROI{
			Number:   item.Number,
			Name:     item.Name,
			ColorRGB: item.ColorRGB,
		}
		for _, contour := range item.Contours {
			points, err := vectorContourPoints(contour.ROI)
			if err != nil {
				return nil, err
			}
			if err := validateSliceGeometry(contour.Geometry); err != nil {
				return nil, err
			}
			if err := validateReferencedImages(contour.ReferencedImages, true); err != nil {
				return nil, err
			}
			roiItem.Contours = append(roiItem.Contours, Contour{
				GeometricType:    ContourClosedPlanar,
				Points:           imagePointsToPatient(points, contour.Geometry),
				ReferencedImages: cloneReferencedImages(contour.ReferencedImages),
			})
		}
		out.ROIs = append(out.ROIs, roiItem)
	}
	if err := validateStructureSet(out); err != nil {
		return nil, err
	}
	return out, nil
}

// Rasterize converts the specified ROI from a structure set into a 3D voxel segmentation.
// It returns the segmentation on success, ErrInvalidObject if set is nil,
// ErrGeometryMismatch if the geometry is empty, or ErrMissingReference if the
// ROI number is not found, or ErrUnsupportedContourType if the ROI contains a
// geometric type that cannot be represented as a filled raster mask.
func Rasterize(set *StructureSet, opts RasterizeOptions) (*dicomroi.Segmentation3D, error) {
	if set == nil {
		return nil, fmt.Errorf("%w: structure set is nil", ErrInvalidObject)
	}
	out := dicomroi.NewSegmentation3D(opts.Geometry, opts.Columns, opts.Rows)
	for _, item := range set.ROIs {
		if item.Number != opts.ROINumber {
			continue
		}
		xor, err := contourCombinationMode(item.Contours)
		if err != nil {
			return nil, err
		}
		for _, contour := range item.Contours {
			sliceIndex, points, err := contourToImage(contour, opts.Geometry)
			if err != nil {
				return nil, err
			}
			mask := dicomroi.VectorROI{Shape: dicomroi.ROIPolygon, Points: points}.Rasterize(opts.Columns, opts.Rows)
			existing := out.Mask(sliceIndex)
			if xor {
				existing.XOR(mask)
			} else {
				existing.Union(mask)
			}
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: ROI %d", ErrMissingReference, opts.ROINumber)
}

func contourCombinationMode(contours []Contour) (xor bool, err error) {
	for _, contour := range contours {
		switch contour.GeometricType {
		case ContourClosedPlanarXOR:
			xor = true
		case "", ContourClosedPlanar:
		default:
			return false, fmt.Errorf("%w: %s", ErrUnsupportedContourType, contour.GeometricType)
		}
	}
	if xor {
		for _, contour := range contours {
			if contour.GeometricType != ContourClosedPlanarXOR {
				return false, fmt.Errorf("%w: CLOSEDPLANAR_XOR cannot be mixed with %q", ErrGeometryMismatch, contour.GeometricType)
			}
		}
	}
	return xor, nil
}

func vectorContourPoints(item dicomroi.VectorROI) ([]image.Point, error) {
	switch item.Shape {
	case dicomroi.ROIEllipse:
		return ellipseContourPoints(item.Points)
	case dicomroi.ROIPolygon:
		if len(item.Points) < 3 {
			return nil, fmt.Errorf("%w: polygon contour needs at least 3 points", ErrGeometryMismatch)
		}
		return append([]image.Point(nil), item.Points...), nil
	default:
		return rectangleContourPoints(item.Points)
	}
}

func rectangleContourPoints(points []image.Point) ([]image.Point, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("%w: rectangle contour needs two corners", ErrGeometryMismatch)
	}
	a, b := points[0], points[1]
	minX, maxX := minMaxInt(a.X, b.X)
	minY, maxY := minMaxInt(a.Y, b.Y)
	if minX == maxX || minY == maxY {
		return nil, fmt.Errorf("%w: rectangle contour has zero area", ErrGeometryMismatch)
	}
	return []image.Point{
		image.Pt(minX, minY),
		image.Pt(maxX, minY),
		image.Pt(maxX, maxY),
		image.Pt(minX, maxY),
	}, nil
}

func ellipseContourPoints(points []image.Point) ([]image.Point, error) {
	if len(points) < 2 {
		return nil, fmt.Errorf("%w: ellipse contour needs two bounding corners", ErrGeometryMismatch)
	}
	a, b := points[0], points[1]
	minX, maxX := minMaxInt(a.X, b.X)
	minY, maxY := minMaxInt(a.Y, b.Y)
	rx := float64(maxX-minX) / 2
	ry := float64(maxY-minY) / 2
	if rx <= 0 || ry <= 0 {
		return nil, fmt.Errorf("%w: ellipse contour has zero area", ErrGeometryMismatch)
	}
	cx := float64(minX) + rx
	cy := float64(minY) + ry
	const steps = 36
	out := make([]image.Point, 0, steps)
	for i := 0; i < steps; i++ {
		t := 2 * math.Pi * float64(i) / steps
		out = append(out, image.Pt(
			int(math.Round(cx+rx*math.Cos(t))),
			int(math.Round(cy+ry*math.Sin(t))),
		))
	}
	return out, nil
}

func imagePointsToPatient(points []image.Point, geometry render.SliceGeometry) []Point3D {
	out := make([]Point3D, 0, len(points))
	for _, point := range points {
		patient := geometry.Origin.
			Add(geometry.RowDir.Scale(float64(point.X) * geometry.ColSpacing)).
			Add(geometry.ColDir.Scale(float64(point.Y) * geometry.RowSpacing))
		out = append(out, Point3D{X: patient.X, Y: patient.Y, Z: patient.Z})
	}
	return out
}

func validateSliceGeometry(geometry render.SliceGeometry) error {
	if !finitePositive(geometry.RowSpacing) || !finitePositive(geometry.ColSpacing) ||
		!finiteVector(geometry.RowDir) || !finiteVector(geometry.ColDir) || !finiteVector(geometry.Normal) {
		return fmt.Errorf("%w: missing slice geometry", ErrGeometryMismatch)
	}
	return nil
}

func finitePositive(value float64) bool {
	return value > 0 && !math.IsNaN(value) && !math.IsInf(value, 0)
}

func finiteVector(value render.Vec3) bool {
	length := value.Length()
	return !math.IsNaN(value.X) && !math.IsInf(value.X, 0) &&
		!math.IsNaN(value.Y) && !math.IsInf(value.Y, 0) &&
		!math.IsNaN(value.Z) && !math.IsInf(value.Z, 0) &&
		!math.IsNaN(length) && !math.IsInf(length, 0) && length > 0
}

func validateReferencedImages(refs []ReferencedImage, requireHierarchy bool) error {
	if len(refs) == 0 {
		return fmt.Errorf("%w: missing contour image reference", ErrMissingReference)
	}
	for _, ref := range refs {
		if ref.SOPClassUID == "" || ref.SOPInstanceUID == "" {
			return fmt.Errorf("%w: missing contour image SOP reference", ErrMissingReference)
		}
		if requireHierarchy && (ref.StudyInstanceUID == "" || ref.SeriesInstanceUID == "") {
			return fmt.Errorf("%w: missing contour image study/series reference", ErrMissingReference)
		}
	}
	return nil
}

func cloneReferencedImages(refs []ReferencedImage) []ReferencedImage {
	return append([]ReferencedImage(nil), refs...)
}

func validateStructureSet(set *StructureSet) error {
	if set == nil {
		return nil
	}
	if len(set.ROIs) > 0 && set.FrameOfReferenceUID == "" {
		return fmt.Errorf("%w: missing frame of reference UID", ErrMissingReference)
	}
	seen := map[int]bool{}
	for _, roiItem := range set.ROIs {
		if roiItem.Number == 0 {
			return fmt.Errorf("%w: missing ROI number", ErrInvalidObject)
		}
		if seen[roiItem.Number] {
			return fmt.Errorf("%w: duplicate ROI number %d", ErrInvalidObject, roiItem.Number)
		}
		seen[roiItem.Number] = true
		for _, contour := range roiItem.Contours {
			if err := validateContourPointCount(contour); err != nil {
				return err
			}
			if len(contour.ReferencedImages) > 0 {
				for _, ref := range contour.ReferencedImages {
					if ref.SOPClassUID == "" || ref.SOPInstanceUID == "" {
						return fmt.Errorf("%w: missing contour image SOP reference", ErrMissingReference)
					}
				}
			}
		}
	}
	return nil
}

func validateContourPointCount(contour Contour) error {
	count := len(contour.Points)
	switch contour.GeometricType {
	case ContourPoint:
		if count != 1 {
			return fmt.Errorf("%w: POINT contour needs exactly 1 point, got %d", ErrGeometryMismatch, count)
		}
	case ContourOpenPlanar, ContourOpenNonPlanar:
		if count < 2 {
			return fmt.Errorf("%w: %s contour needs at least 2 points, got %d", ErrGeometryMismatch, contour.GeometricType, count)
		}
	case "", ContourClosedPlanar, ContourClosedPlanarXOR:
		if count < 3 {
			return fmt.Errorf("%w: %s contour needs at least 3 points, got %d", ErrGeometryMismatch, contour.GeometricType, count)
		}
	default:
		if count > 0 && count < 3 {
			return fmt.Errorf("%w: contour needs at least 3 points", ErrGeometryMismatch)
		}
	}
	return nil
}

func minMaxInt(a, b int) (int, int) {
	if a < b {
		return a, b
	}
	return b, a
}

func referencedFrameOfReferenceSequence(set *StructureSet) core.Element {
	elements := []core.Element{derivedio.UI(derivedio.TagFrameOfReferenceUID, set.FrameOfReferenceUID)}
	if studySeq := rtReferencedStudySequence(set); len(studySeq) > 0 {
		elements = append(elements, derivedio.Seq(tagRTReferencedStudySequence, studySeq...))
	}
	return derivedio.Seq(tagReferencedFrameOfReferenceSeq, derivedio.DataSet(elements...))
}

func rtReferencedStudySequence(set *StructureSet) []core.DataSet {
	type studyGroup struct {
		studyUID string
		series   map[string][]ReferencedImage
	}
	studies := map[string]*studyGroup{}
	for _, ref := range allReferencedImages(set.ROIs) {
		if ref.StudyInstanceUID == "" || ref.SeriesInstanceUID == "" {
			continue
		}
		group := studies[ref.StudyInstanceUID]
		if group == nil {
			group = &studyGroup{studyUID: ref.StudyInstanceUID, series: map[string][]ReferencedImage{}}
			studies[ref.StudyInstanceUID] = group
		}
		group.series[ref.SeriesInstanceUID] = append(group.series[ref.SeriesInstanceUID], ref)
	}
	studyUIDs := make([]string, 0, len(studies))
	for uid := range studies {
		studyUIDs = append(studyUIDs, uid)
	}
	sort.Strings(studyUIDs)
	out := make([]core.DataSet, 0, len(studyUIDs))
	for _, studyUID := range studyUIDs {
		group := studies[studyUID]
		seriesUIDs := make([]string, 0, len(group.series))
		for seriesUID := range group.series {
			seriesUIDs = append(seriesUIDs, seriesUID)
		}
		sort.Strings(seriesUIDs)
		seriesItems := make([]core.DataSet, 0, len(seriesUIDs))
		for _, seriesUID := range seriesUIDs {
			refs := append([]ReferencedImage(nil), group.series[seriesUID]...)
			sort.SliceStable(refs, func(i, j int) bool {
				if refs[i].SOPInstanceUID != refs[j].SOPInstanceUID {
					return refs[i].SOPInstanceUID < refs[j].SOPInstanceUID
				}
				return refs[i].SOPClassUID < refs[j].SOPClassUID
			})
			seriesItems = append(seriesItems, derivedio.DataSet(
				derivedio.UI(derivedio.TagSeriesInstanceUID, seriesUID),
				contourImageSequence(refs),
			))
		}
		out = append(out, derivedio.DataSet(
			derivedio.UI(derivedio.TagRefSOPClassUID, detachedStudyManagementSOPClass),
			derivedio.UI(derivedio.TagRefSOPInstanceUID, group.studyUID),
			derivedio.Seq(tagRTReferencedSeriesSequence, seriesItems...),
		))
	}
	return out
}

func allReferencedImages(items []ROI) []ReferencedImage {
	seen := map[[4]string]bool{}
	var out []ReferencedImage
	for _, roiItem := range items {
		for _, contour := range roiItem.Contours {
			for _, ref := range contour.ReferencedImages {
				key := [4]string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPClassUID, ref.SOPInstanceUID}
				if seen[key] {
					continue
				}
				seen[key] = true
				out = append(out, ref)
			}
		}
	}
	return out
}

// structureSetROISequence builds a DICOM StructureSetROISequence element containing the ROI number, frame of reference, and name for each input ROI.
func structureSetROISequence(set *StructureSet) core.Element {
	sets := make([]core.DataSet, 0, len(set.ROIs))
	for _, item := range set.ROIs {
		sets = append(sets, derivedio.DataSet(
			derivedio.IS(tagROINumber, item.Number),
			derivedio.UI(tagReferencedFrameOfReferenceUID, set.FrameOfReferenceUID),
			derivedio.LO(tagROIName, item.Name),
		))
	}
	return derivedio.Seq(tagStructureSetROISequence, sets...)
}

// roiContourSequence builds a DICOM ROI Contour Sequence element containing the referenced ROI number, display color, and contour sequence for each ROI.
func roiContourSequence(items []ROI) core.Element {
	sets := make([]core.DataSet, 0, len(items))
	for _, item := range items {
		elements := []core.Element{
			derivedio.IS(tagReferencedROINumber, item.Number),
			derivedio.IS(tagROIDisplayColor, item.ColorRGB[0], item.ColorRGB[1], item.ColorRGB[2]),
			contourSequence(item.Contours),
		}
		sets = append(sets, derivedio.DataSet(elements...))
	}
	return derivedio.Seq(tagROIContourSequence, sets...)
}

func contourSequence(contours []Contour) core.Element {
	sets := make([]core.DataSet, 0, len(contours))
	for _, contour := range contours {
		sets = append(sets, derivedio.DataSet(
			derivedio.CS(tagContourGeometricType, contour.GeometricType),
			derivedio.IS(tagNumberOfContourPoints, len(contour.Points)),
			derivedio.DS(tagContourData, flattenPoints(contour.Points)...),
			contourImageSequence(contour.ReferencedImages),
		))
	}
	return derivedio.Seq(tagContourSequence, sets...)
}

// contourImageSequence builds a DICOM sequence element for the provided referenced images.
func contourImageSequence(refs []ReferencedImage) core.Element {
	sets := make([]core.DataSet, 0, len(refs))
	for _, ref := range refs {
		sets = append(sets, derivedio.DataSet(
			derivedio.UI(derivedio.TagRefSOPClassUID, ref.SOPClassUID),
			derivedio.UI(derivedio.TagRefSOPInstanceUID, ref.SOPInstanceUID),
		))
	}
	return derivedio.Seq(tagContourImageSequence, sets...)
}

// rtROIObservationsSequence builds a DICOM RT ROI Observations sequence with one observation record per ROI.
func rtROIObservationsSequence(items []ROI) core.Element {
	sets := make([]core.DataSet, 0, len(items))
	for i, item := range items {
		sets = append(sets, derivedio.DataSet(
			derivedio.IS(tagObservationNumber, i+1),
			derivedio.IS(tagReferencedROINumber, item.Number),
			derivedio.CS(tagRTROIInterpretedType, "ORGAN"),
		))
	}
	return derivedio.Seq(tagRTROIObservationsSequence, sets...)
}

// flattenPoints converts a slice of 3D points into a flat float64 slice with coordinates in X, Y, Z order.
func flattenPoints(points []Point3D) []float64 {
	out := make([]float64, 0, len(points)*3)
	for _, point := range points {
		out = append(out, point.X, point.Y, point.Z)
	}
	return out
}

// readROIs reads the StructureSetROISequence from a DICOM object and returns ROIs indexed by number.
func readROIs(obj *object.Object) map[int]*ROI {
	out := map[int]*ROI{}
	for _, item := range derivedio.Sequence(obj, tagStructureSetROISequence) {
		number := derivedio.Int(item, tagROINumber)
		out[number] = &ROI{Number: number, Name: derivedio.CleanString(item, tagROIName)}
	}
	return out
}

// attachContours reads ROI contour sequences and display colors from a DICOM object and attaches them to ROIs in the provided map, creating ROI entries as needed.
func attachContours(obj *object.Object, out map[int]*ROI, referencedImages map[string]ReferencedImage) {
	for _, item := range derivedio.Sequence(obj, tagROIContourSequence) {
		number := derivedio.Int(item, tagReferencedROINumber)
		roiItem := out[number]
		if roiItem == nil {
			roiItem = &ROI{Number: number}
			out[number] = roiItem
		}
		values, err := item.GetInts(tagROIDisplayColor)
		if err == nil && len(values) >= 3 {
			roiItem.ColorRGB = [3]int{int(values[0]), int(values[1]), int(values[2])}
		}
		for _, contourObj := range derivedio.Sequence(item, tagContourSequence) {
			roiItem.Contours = append(roiItem.Contours, readContour(contourObj, referencedImages))
		}
	}
}

func readContour(obj *object.Object, referencedImages map[string]ReferencedImage) Contour {
	contour := Contour{
		GeometricType: derivedio.CleanString(obj, tagContourGeometricType),
	}
	coords := derivedio.Floats(obj, tagContourData)
	for i := 0; i+2 < len(coords); i += 3 {
		contour.Points = append(contour.Points, Point3D{X: coords[i], Y: coords[i+1], Z: coords[i+2]})
	}
	for _, refObj := range derivedio.Sequence(obj, tagContourImageSequence) {
		ref := ReferencedImage{
			SOPClassUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
			SOPInstanceUID: derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
		}
		if hierarchyRef, ok := referencedImages[ref.SOPInstanceUID]; ok {
			ref.StudyInstanceUID = hierarchyRef.StudyInstanceUID
			ref.SeriesInstanceUID = hierarchyRef.SeriesInstanceUID
			if ref.SOPClassUID == "" {
				ref.SOPClassUID = hierarchyRef.SOPClassUID
			}
		}
		contour.ReferencedImages = append(contour.ReferencedImages, ref)
	}
	return contour
}

func readFrameOfReferenceUID(obj *object.Object) string {
	if value := derivedio.CleanUID(obj, derivedio.TagFrameOfReferenceUID); value != "" {
		return value
	}
	return derivedio.CleanUID(obj, tagReferencedFrameOfReferenceUID)
}

func readReferencedImageHierarchy(obj *object.Object) map[string]ReferencedImage {
	out := map[string]ReferencedImage{}
	for _, frame := range derivedio.Sequence(obj, tagReferencedFrameOfReferenceSeq) {
		for _, studyItem := range derivedio.Sequence(frame, tagRTReferencedStudySequence) {
			studyUID := derivedio.CleanUID(studyItem, derivedio.TagRefSOPInstanceUID)
			if studyUID == "" {
				studyUID = derivedio.CleanUID(studyItem, derivedio.TagStudyInstanceUID)
			}
			for _, seriesItem := range derivedio.Sequence(studyItem, tagRTReferencedSeriesSequence) {
				seriesUID := derivedio.CleanUID(seriesItem, derivedio.TagSeriesInstanceUID)
				for _, refObj := range derivedio.Sequence(seriesItem, tagContourImageSequence) {
					ref := ReferencedImage{
						StudyInstanceUID:  studyUID,
						SeriesInstanceUID: seriesUID,
						SOPClassUID:       derivedio.CleanUID(refObj, derivedio.TagRefSOPClassUID),
						SOPInstanceUID:    derivedio.CleanUID(refObj, derivedio.TagRefSOPInstanceUID),
					}
					if ref.SOPInstanceUID != "" {
						out[ref.SOPInstanceUID] = ref
					}
				}
			}
		}
	}
	return out
}

// contourToImage converts 3D contour points to 2D pixel coordinates for the nearest volume slice.
// It returns the slice index, the 2D image points, and an error if the volume geometry is empty.
func contourToImage(contour Contour, geometry render.VolumeGeometry) (int, []image.Point, error) {
	if len(geometry.Slices) == 0 {
		return 0, nil, fmt.Errorf("%w: empty volume geometry", ErrGeometryMismatch)
	}
	if len(contour.Points) == 0 {
		return 0, nil, fmt.Errorf("%w: empty contour points", ErrGeometryMismatch)
	}
	for i, slice := range geometry.Slices {
		if err := validateSliceGeometry(slice); err != nil {
			return 0, nil, fmt.Errorf("%w: slice %d", err, i)
		}
	}
	sliceIndex, distance := nearestSlice(contour.Points[0], geometry)
	tolerance := contourPlaneTolerance(geometry, sliceIndex)
	if distance > tolerance {
		return 0, nil, fmt.Errorf("%w: contour plane is %.6g mm from nearest slice %d (tolerance %.6g mm)", ErrGeometryMismatch, distance, sliceIndex, tolerance)
	}
	slice := geometry.Slices[sliceIndex]
	rowDir := slice.RowDir.Normalize()
	colDir := slice.ColDir.Normalize()
	normal := slice.Normal.Normalize()
	points := make([]image.Point, 0, len(contour.Points))
	for _, point := range contour.Points {
		delta := render.Vec3{X: point.X, Y: point.Y, Z: point.Z}.Sub(slice.Origin)
		if planeDistance := math.Abs(delta.Dot(normal)); planeDistance > tolerance {
			return 0, nil, fmt.Errorf("%w: contour point is %.6g mm from slice %d (tolerance %.6g mm)", ErrGeometryMismatch, planeDistance, sliceIndex, tolerance)
		}
		x := int(math.Round(delta.Dot(rowDir) / slice.ColSpacing))
		y := int(math.Round(delta.Dot(colDir) / slice.RowSpacing))
		points = append(points, image.Point{X: x, Y: y})
	}
	return sliceIndex, points, nil
}

// nearestSlice returns the index and plane distance of the slice nearest to the point.
func nearestSlice(point Point3D, geometry render.VolumeGeometry) (int, float64) {
	p := render.Vec3{X: point.X, Y: point.Y, Z: point.Z}
	best := 0
	bestDistance := math.Inf(1)
	for i, slice := range geometry.Slices {
		distance := math.Abs(p.Sub(slice.Origin).Dot(slice.Normal.Normalize()))
		if distance < bestDistance {
			best = i
			bestDistance = distance
		}
	}
	return best, bestDistance
}

func contourPlaneTolerance(geometry render.VolumeGeometry, sliceIndex int) float64 {
	tolerances := render.DefaultGeometryTolerances()
	spacing := localSliceSpacing(geometry, sliceIndex)
	if !finitePositive(spacing) {
		spacing = geometry.MeanSpacing
	}
	if !finitePositive(spacing) {
		return tolerances.SpacingAbs
	}
	return math.Max(tolerances.SpacingAbs, tolerances.SpacingRel*spacing)
}

func localSliceSpacing(geometry render.VolumeGeometry, sliceIndex int) float64 {
	if sliceIndex < 0 || sliceIndex >= len(geometry.Slices) {
		return 0
	}
	normal := geometry.Slices[sliceIndex].Normal.Normalize()
	best := math.Inf(1)
	for _, neighbor := range []int{sliceIndex - 1, sliceIndex + 1} {
		if neighbor < 0 || neighbor >= len(geometry.Slices) {
			continue
		}
		distance := math.Abs(geometry.Slices[neighbor].Origin.Sub(geometry.Slices[sliceIndex].Origin).Dot(normal))
		if finitePositive(distance) && distance < best {
			best = distance
		}
	}
	if math.IsInf(best, 1) {
		return 0
	}
	return best
}
