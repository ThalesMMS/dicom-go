package rtstruct

import (
	"bytes"
	"errors"
	"image"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	dicomroi "github.com/ThalesMMS/dicom-go/roi"
)

func Test_FromVectorROIs_builds_patient_space_multislice_references(t *testing.T) {
	set, err := FromVectorROIs(testVectorOptions())
	if err != nil {
		t.Fatalf("FromVectorROIs: %v", err)
	}
	file, err := Write(set)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if roundTrip.FrameOfReferenceUID != "1.2.3.frame" {
		t.Fatalf("FrameOfReferenceUID = %q, want source frame", roundTrip.FrameOfReferenceUID)
	}
	if len(roundTrip.ROIs) != 2 {
		t.Fatalf("ROIs = %d, want 2", len(roundTrip.ROIs))
	}
	if roundTrip.ROIs[0].Number != 1 || roundTrip.ROIs[1].Number != 2 {
		t.Fatalf("ROI ordering = %d,%d, want stable numeric order", roundTrip.ROIs[0].Number, roundTrip.ROIs[1].Number)
	}
	first := roundTrip.ROIs[0].Contours[0]
	if len(first.Points) != 4 {
		t.Fatalf("first contour points = %d, want 4", len(first.Points))
	}
	if got := first.Points[1]; got.X != 1.5 || got.Y != 0 || got.Z != 10 {
		t.Fatalf("patient point = %+v, want pixel (2,0) mapped by column spacing", got)
	}
	if got := roundTrip.ROIs[1].Contours[0].ReferencedImages[0].SOPInstanceUID; got != "1.2.3.sop.2" {
		t.Fatalf("second ROI referenced SOP = %q, want 1.2.3.sop.2", got)
	}
	if got := roundTrip.ROIs[1].Contours[0].ReferencedImages[0].StudyInstanceUID; got != "1.2.3.study" {
		t.Fatalf("second ROI referenced study = %q, want 1.2.3.study", got)
	}
	if got := roundTrip.ROIs[1].Contours[0].ReferencedImages[0].SeriesInstanceUID; got != "1.2.3.series" {
		t.Fatalf("second ROI referenced series = %q, want 1.2.3.series", got)
	}
	refs, ok := file.Dataset.GetSequence(tagReferencedFrameOfReferenceSeq)
	if !ok || len(refs) != 1 {
		t.Fatalf("ReferencedFrameOfReferenceSequence ok=%v len=%d, want one item", ok, len(refs))
	}
	if got, _ := refs[0].GetUID(core.NewTag(0x0020, 0x0052)); got != "1.2.3.frame" {
		t.Fatalf("ReferencedFrameOfReferenceSequence FrameOfReferenceUID = %q, want 1.2.3.frame", got)
	}
	studies, ok := refs[0].GetSequence(tagRTReferencedStudySequence)
	if !ok || len(studies) != 1 {
		t.Fatalf("RTReferencedStudySequence ok=%v len=%d, want source study item", ok, len(studies))
	}
	series, ok := studies[0].GetSequence(tagRTReferencedSeriesSequence)
	if !ok || len(series) != 1 {
		t.Fatalf("RTReferencedSeriesSequence ok=%v len=%d, want source series item", ok, len(series))
	}
	images, ok := series[0].GetSequence(tagContourImageSequence)
	if !ok || len(images) != 2 {
		t.Fatalf("nested ContourImageSequence ok=%v len=%d, want two source images", ok, len(images))
	}
	roiItems, ok := file.Dataset.GetSequence(tagStructureSetROISequence)
	if !ok || len(roiItems) != 2 {
		t.Fatalf("StructureSetROISequence ok=%v len=%d, want two ROI items", ok, len(roiItems))
	}
	for i, item := range roiItems {
		if got, _ := item.GetUID(tagReferencedFrameOfReferenceUID); got != "1.2.3.frame" {
			t.Fatalf("StructureSetROISequence[%d] ReferencedFrameOfReferenceUID = %q, want 1.2.3.frame", i, got)
		}
	}
}

func Test_FromVectorROIs_rejects_missing_geometry(t *testing.T) {
	opts := testVectorOptions()
	opts.ROIs[0].Contours[0].Geometry = render.SliceGeometry{}
	_, err := FromVectorROIs(opts)
	if !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("FromVectorROIs error = %v, want ErrGeometryMismatch", err)
	}
}

func Test_FromVectorROIs_output_is_deterministic(t *testing.T) {
	first := writeVectorOptions(t, testVectorOptions())
	second := writeVectorOptions(t, testVectorOptions())
	if !bytes.Equal(first, second) {
		t.Fatal("RTSTRUCT bytes differ for identical vector ROI input")
	}
}

func Test_FromVectorROIs_rejects_duplicate_roi_numbers(t *testing.T) {
	opts := testVectorOptions()
	opts.ROIs[1].Number = opts.ROIs[0].Number
	_, err := FromVectorROIs(opts)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("FromVectorROIs duplicate ROI error = %v, want ErrInvalidObject", err)
	}
}

func writeVectorOptions(t *testing.T, opts FromVectorROIsOptions) []byte {
	t.Helper()
	set, err := FromVectorROIs(opts)
	if err != nil {
		t.Fatalf("FromVectorROIs: %v", err)
	}
	file, err := Write(set)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}
	return buf.Bytes()
}

func testVectorOptions() FromVectorROIsOptions {
	return FromVectorROIsOptions{
		SOPInstanceUID:      "1.2.3.rt",
		StudyInstanceUID:    "1.2.3.study",
		SeriesInstanceUID:   "1.2.3.rtseries",
		FrameOfReferenceUID: "1.2.3.frame",
		Label:               "TWIN_ROIS",
		ROIs: []VectorROI{
			{
				Number:   2,
				Name:     "Slice 2 box",
				ColorRGB: [3]int{255, 128, 0},
				Contours: []VectorROIContour{{
					ROI:      dicomroi.VectorROI{Shape: dicomroi.ROIRectangle, Points: []image.Point{{X: 1, Y: 1}, {X: 3, Y: 2}}},
					Geometry: testVectorGeometry(12),
					ReferencedImages: []ReferencedImage{{
						StudyInstanceUID:  "1.2.3.study",
						SeriesInstanceUID: "1.2.3.series",
						SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
						SOPInstanceUID:    "1.2.3.sop.2",
					}},
				}},
			},
			{
				Number:   1,
				Name:     "Slice 1 polygon",
				ColorRGB: [3]int{255, 214, 64},
				Contours: []VectorROIContour{{
					ROI: dicomroi.VectorROI{Shape: dicomroi.ROIPolygon, Points: []image.Point{
						{X: 0, Y: 0},
						{X: 2, Y: 0},
						{X: 2, Y: 2},
						{X: 0, Y: 2},
					}},
					Geometry: testVectorGeometry(10),
					ReferencedImages: []ReferencedImage{{
						StudyInstanceUID:  "1.2.3.study",
						SeriesInstanceUID: "1.2.3.series",
						SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
						SOPInstanceUID:    "1.2.3.sop.1",
					}},
				}},
			},
		},
	}
}

func testVectorGeometry(z float64) render.SliceGeometry {
	return render.SliceGeometry{
		Origin:     render.Vec3{Z: z},
		RowDir:     render.Vec3{X: 1},
		ColDir:     render.Vec3{Y: 1},
		Normal:     render.Vec3{Z: 1},
		RowSpacing: 0.5,
		ColSpacing: 0.75,
		Rows:       4,
		Columns:    4,
	}
}
