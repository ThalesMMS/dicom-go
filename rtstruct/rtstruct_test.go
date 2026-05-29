package rtstruct

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"math"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
)

func TestRTStructureSetRoundTripsAndRasterizesClosedContour(t *testing.T) {
	// Given: an RTSTRUCT with one closed planar ROI in patient coordinates.
	set := StructureSet{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.481.1",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.481.study",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.481.series",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.481.frame",
		ROIs: []ROI{{
			Number: 1,
			Name:   "PTV",
			Contours: []Contour{{
				GeometricType: ContourClosedPlanar,
				Points: []Point3D{
					{X: 1, Y: 1, Z: 0},
					{X: 4, Y: 1, Z: 0},
					{X: 4, Y: 4, Z: 0},
					{X: 1, Y: 4, Z: 0},
				},
				ReferencedImages: []ReferencedImage{{
					SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID: "1.2.3.ct.1",
				}},
			}},
		}},
	}
	file, err := Write(&set)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}

	// When: the Part 10 object is read back and rasterized against the volume.
	readFile, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := Read(readFile.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	mask, err := Rasterize(roundTrip, RasterizeOptions{
		ROINumber: 1,
		Geometry:  testRTGeometry(),
		Columns:   8,
		Rows:      8,
	})
	if err != nil {
		t.Fatalf("Rasterize: %v", err)
	}

	// Then: contour metadata and patient-space rasterization are preserved.
	if roundTrip.SOPClassUID != RTStructureSetStorage {
		t.Fatalf("SOPClassUID = %q, want RTSTRUCT", roundTrip.SOPClassUID)
	}
	if got := roundTrip.ROIs[0].Name; got != "PTV" {
		t.Fatalf("ROI name = %q, want PTV", got)
	}
	if !mask.Voxel(2, 2, 0) {
		t.Fatal("expected rasterized center voxel")
	}
	if mask.Voxel(7, 7, 0) {
		t.Fatal("unexpected voxel outside contour")
	}
}

func TestRTStructureSetReadWriteRoundTripsPointAndOpenContours(t *testing.T) {
	set := &StructureSet{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.481.305",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.481.305.1",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.481.305.2",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.481.305.3",
		ROIs: []ROI{{
			Number: 1,
			Name:   "Mixed contours",
			Contours: []Contour{
				{GeometricType: ContourPoint, Points: []Point3D{{X: 1, Y: 2, Z: 3}}},
				{GeometricType: ContourOpenPlanar, Points: []Point3D{{X: 1}, {X: 2}}},
				{GeometricType: ContourOpenNonPlanar, Points: []Point3D{{X: 1}, {X: 2, Z: 1}}},
			},
		}},
	}
	source := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, RTStructureSetStorage),
		derivedio.UI(derivedio.TagSOPInstanceUID, set.SOPInstanceUID),
		derivedio.UI(derivedio.TagStudyInstanceUID, set.StudyInstanceUID),
		derivedio.UI(derivedio.TagSeriesInstanceUID, set.SeriesInstanceUID),
		referencedFrameOfReferenceSequence(set),
		structureSetROISequence(set),
		roiContourSequence(set.ROIs),
	)

	readSet, err := Read(source)
	if err != nil {
		t.Fatalf("initial Read: %v", err)
	}
	file, err := Write(readSet)
	if err != nil {
		t.Fatalf("Write after Read: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("second Read: %v", err)
	}

	if len(roundTrip.ROIs) != 1 || len(roundTrip.ROIs[0].Contours) != 3 {
		t.Fatalf("round-trip contours = %+v, want three contours", roundTrip.ROIs)
	}
	wantTypes := []string{ContourPoint, ContourOpenPlanar, ContourOpenNonPlanar}
	wantPoints := []int{1, 2, 2}
	for i, contour := range roundTrip.ROIs[0].Contours {
		if contour.GeometricType != wantTypes[i] || len(contour.Points) != wantPoints[i] {
			t.Fatalf("contour %d = %s/%d points, want %s/%d", i, contour.GeometricType, len(contour.Points), wantTypes[i], wantPoints[i])
		}
	}
}

func TestFiniteVectorRejectsOverflowedLength(t *testing.T) {
	if finiteVector(render.Vec3{X: math.MaxFloat64, Y: math.MaxFloat64}) {
		t.Fatal("finiteVector() accepted a vector whose length overflows to infinity")
	}
}

func TestRTStructureSetWriteRejectsContourCardinalityMismatches(t *testing.T) {
	tests := []Contour{
		{GeometricType: ContourPoint},
		{GeometricType: ContourPoint, Points: []Point3D{{}, {X: 1}}},
		{GeometricType: ContourOpenPlanar, Points: []Point3D{{}}},
		{GeometricType: ContourClosedPlanar, Points: []Point3D{{}, {X: 1}}},
	}
	for _, contour := range tests {
		t.Run(fmt.Sprintf("%s_%d", contour.GeometricType, len(contour.Points)), func(t *testing.T) {
			set := &StructureSet{
				SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.481.305.20",
				FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.481.305.21",
				ROIs:                []ROI{{Number: 1, Contours: []Contour{contour}}},
			}
			if _, err := Write(set); !errors.Is(err, ErrGeometryMismatch) {
				t.Fatalf("Write error = %v, want ErrGeometryMismatch", err)
			}
		})
	}
}

func TestRTStructureSetWriteRejectsNilSet(t *testing.T) {
	_, err := Write(nil)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Write(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestRTStructureSetWriteRejectsWrongSopClass(t *testing.T) {
	set := &StructureSet{
		SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2", // CT
		SOPInstanceUID: "1.2.3.rt.1",
	}
	_, err := Write(set)
	if !errors.Is(err, ErrUnsupportedSOPClass) {
		t.Fatalf("Write(wrong SOP class) error = %v, want ErrUnsupportedSOPClass", err)
	}
}

func TestRTStructureSetReadRejectsNilDataset(t *testing.T) {
	_, err := Read(nil)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Read(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestRTStructureSetReadRejectsWrongSopClass(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.2"}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.3.not.rt"}},
	}, std.Dictionary)
	_, err := Read(dataset)
	if !errors.Is(err, ErrUnsupportedSOPClass) {
		t.Fatalf("Read(wrong SOP class) error = %v, want ErrUnsupportedSOPClass", err)
	}
}

func TestRTStructureSetRasterizeRejectsNilSet(t *testing.T) {
	_, err := Rasterize(nil, RasterizeOptions{ROINumber: 1, Geometry: testRTGeometry(), Columns: 8, Rows: 8})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Rasterize(nil) error = %v, want ErrInvalidObject", err)
	}
}

func TestRTStructureSetRasterizeReturnsErrorForMissingRoi(t *testing.T) {
	set := &StructureSet{
		SOPInstanceUID:      "1.2.3.rt.missing",
		StudyInstanceUID:    "1.2.3.study",
		SeriesInstanceUID:   "1.2.3.rt.series",
		FrameOfReferenceUID: "1.2.3.frame",
		ROIs: []ROI{{
			Number: 1,
			Name:   "PTV",
			Contours: []Contour{{
				GeometricType: ContourClosedPlanar,
				Points:        []Point3D{{X: 0, Y: 0, Z: 0}, {X: 5, Y: 0, Z: 0}, {X: 5, Y: 5, Z: 0}, {X: 0, Y: 5, Z: 0}},
			}},
		}},
	}
	_, err := Rasterize(set, RasterizeOptions{
		ROINumber: 99, // does not exist
		Geometry:  testRTGeometry(),
		Columns:   8,
		Rows:      8,
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("Rasterize(missing ROI) error = %v, want ErrMissingReference", err)
	}
}

func TestRTStructureSetRasterizeRejectsUnsupportedContourTypes(t *testing.T) {
	tests := []string{
		ContourPoint,
		ContourOpenPlanar,
		ContourOpenNonPlanar,
		"PRIVATE_SHAPE",
	}
	for _, geometricType := range tests {
		t.Run(geometricType, func(t *testing.T) {
			set := &StructureSet{ROIs: []ROI{{
				Number: 1,
				Contours: []Contour{{
					GeometricType: geometricType,
					Points:        []Point3D{{X: 1, Y: 1, Z: 0}},
				}},
			}}}

			_, err := Rasterize(set, RasterizeOptions{
				ROINumber: 1,
				Geometry:  testRTGeometry(),
				Columns:   8,
				Rows:      8,
			})
			if !errors.Is(err, ErrUnsupportedContourType) {
				t.Fatalf("Rasterize(%s) error = %v, want ErrUnsupportedContourType", geometricType, err)
			}
			if !strings.Contains(err.Error(), geometricType) {
				t.Fatalf("Rasterize(%s) error = %q, want geometric type context", geometricType, err)
			}
		})
	}
}

func TestRTStructureSetRasterizeAppliesCLOSEDPLANARXORHoles(t *testing.T) {
	set := &StructureSet{ROIs: []ROI{{
		Number: 1,
		Contours: []Contour{
			{
				GeometricType: ContourClosedPlanarXOR,
				Points: []Point3D{
					{X: 1, Y: 1, Z: 0}, {X: 6, Y: 1, Z: 0},
					{X: 6, Y: 6, Z: 0}, {X: 1, Y: 6, Z: 0},
				},
			},
			{
				GeometricType: ContourClosedPlanarXOR,
				Points: []Point3D{
					{X: 2, Y: 2, Z: 0}, {X: 5, Y: 2, Z: 0},
					{X: 5, Y: 5, Z: 0}, {X: 2, Y: 5, Z: 0},
				},
			},
		},
	}}}

	mask, err := Rasterize(set, RasterizeOptions{
		ROINumber: 1,
		Geometry:  testRTGeometry(),
		Columns:   8,
		Rows:      8,
	})
	if err != nil {
		t.Fatalf("Rasterize(CLOSEDPLANAR_XOR) error = %v", err)
	}
	if !mask.Voxel(1, 1, 0) {
		t.Fatal("outer ring voxel = false, want true")
	}
	if mask.Voxel(3, 3, 0) {
		t.Fatal("inner hole voxel = true, want false")
	}
	if mask.Voxel(7, 7, 0) {
		t.Fatal("outside voxel = true, want false")
	}
}

func TestRTStructureSetRasterizeRejectsMixedXORAndNonXORContours(t *testing.T) {
	set := &StructureSet{ROIs: []ROI{{
		Number: 1,
		Contours: []Contour{
			{GeometricType: ContourClosedPlanarXOR, Points: []Point3D{{X: 0, Y: 0, Z: 0}, {X: 4, Y: 0, Z: 0}, {X: 0, Y: 4, Z: 0}}},
			{GeometricType: ContourClosedPlanar, Points: []Point3D{{X: 1, Y: 1, Z: 0}, {X: 2, Y: 1, Z: 0}, {X: 1, Y: 2, Z: 0}}},
		},
	}}}

	_, err := Rasterize(set, RasterizeOptions{
		ROINumber: 1,
		Geometry:  testRTGeometry(),
		Columns:   8,
		Rows:      8,
	})
	if !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("Rasterize(mixed XOR) error = %v, want ErrGeometryMismatch", err)
	}
}

func TestRTStructureSetWriteUsesDefaultSopClassWhenEmpty(t *testing.T) {
	set := &StructureSet{
		SOPInstanceUID:      "1.2.3.rt.default",
		StudyInstanceUID:    "1.2.3.study",
		SeriesInstanceUID:   "1.2.3.rt.series",
		FrameOfReferenceUID: "1.2.3.frame",
	}
	file, err := Write(set)
	if err != nil {
		t.Fatalf("Write with empty SOPClassUID: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if roundTrip.SOPClassUID != RTStructureSetStorage {
		t.Fatalf("SOPClassUID = %q, want RTSTRUCT default", roundTrip.SOPClassUID)
	}
}

func TestRTStructureSetWritePreservesMultipleROIs(t *testing.T) {
	// Given: an RTSTRUCT with two ROIs.
	set := StructureSet{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.481.multi",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.481.study",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.481.series",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.481.frame",
		ROIs: []ROI{
			{Number: 1, Name: "PTV", Contours: []Contour{{GeometricType: ContourClosedPlanar, Points: []Point3D{{X: 0}, {X: 5}, {X: 5, Y: 5}, {X: 0, Y: 5}}}}},
			{Number: 2, Name: "GTV", Contours: []Contour{{GeometricType: ContourClosedPlanar, Points: []Point3D{{X: 1}, {X: 3}, {X: 3, Y: 3}, {X: 1, Y: 3}}}}},
		},
	}
	file, err := Write(&set)
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
	if len(roundTrip.ROIs) != 2 {
		t.Fatalf("ROIs = %d, want 2", len(roundTrip.ROIs))
	}
}

func TestRasterizeRejectsEmptyContourPoints(t *testing.T) {
	// Given: an RTSTRUCT ROI with an empty contour but valid volume geometry.
	set := &StructureSet{
		ROIs: []ROI{{
			Number: 1,
			Contours: []Contour{{
				GeometricType: ContourClosedPlanar,
			}},
		}},
	}

	// When / Then: rasterization returns a geometry error instead of panicking.
	_, err := Rasterize(set, RasterizeOptions{
		ROINumber: 1,
		Geometry:  testRTGeometry(),
		Columns:   8,
		Rows:      8,
	})
	if !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("Rasterize error = %v, want ErrGeometryMismatch", err)
	}
}

func TestContourToImageUsesColumnAndRowSpacingForAnisotropicPixels(t *testing.T) {
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{{
		Origin:     render.Vec3{},
		RowDir:     render.Vec3{X: 1},
		ColDir:     render.Vec3{Y: 1},
		Normal:     render.Vec3{Z: 1},
		RowSpacing: 0.5,
		ColSpacing: 1.0,
		Rows:       64,
		Columns:    64,
	}}, render.DefaultGeometryTolerances())
	contour := Contour{Points: []Point3D{
		{X: 10, Y: 5, Z: 0},
		{X: 12, Y: 5, Z: 0},
		{X: 12, Y: 6, Z: 0},
		{X: 10, Y: 6, Z: 0},
	}}

	sliceIndex, points, err := contourToImage(contour, geometry)
	if err != nil {
		t.Fatalf("contourToImage: %v", err)
	}
	if sliceIndex != 0 {
		t.Fatalf("slice index = %d, want 0", sliceIndex)
	}
	want := []image.Point{
		image.Pt(10, 10),
		image.Pt(12, 10),
		image.Pt(12, 12),
		image.Pt(10, 12),
	}
	if len(points) != len(want) {
		t.Fatalf("points = %v, want %v", points, want)
	}
	for i := range want {
		if points[i] != want[i] {
			t.Fatalf("point %d = %v, want %v; all points %v", i, points[i], want[i], points)
		}
	}
}

func TestContourToImageRejectsInvalidSliceSpacing(t *testing.T) {
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{{
		RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1},
		RowSpacing: 0, ColSpacing: 1, Rows: 8, Columns: 8,
	}}, render.DefaultGeometryTolerances())
	contour := Contour{Points: []Point3D{{X: 1, Y: 1}, {X: 3, Y: 1}, {X: 3, Y: 3}}}

	_, _, err := contourToImage(contour, geometry)
	if !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("contourToImage error = %v, want ErrGeometryMismatch", err)
	}
}

func TestContourToImageRejectsContourFarFromVolume(t *testing.T) {
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		{Origin: render.Vec3{Z: 0}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1}, RowSpacing: 1, ColSpacing: 1, Rows: 8, Columns: 8},
		{Origin: render.Vec3{Z: 5}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1}, RowSpacing: 1, ColSpacing: 1, Rows: 8, Columns: 8},
	}, render.DefaultGeometryTolerances())
	contour := Contour{Points: []Point3D{{X: 1, Y: 1, Z: 100}, {X: 3, Y: 1, Z: 100}, {X: 3, Y: 3, Z: 100}}}

	_, _, err := contourToImage(contour, geometry)
	if !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("contourToImage error = %v, want ErrGeometryMismatch", err)
	}
}

func TestContourToImageUsesSpacingRelativePlaneTolerance(t *testing.T) {
	geometry := render.BuildVolumeGeometry([]render.SliceGeometry{
		{Origin: render.Vec3{Z: 0}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1}, RowSpacing: 1, ColSpacing: 1, Rows: 8, Columns: 8},
		{Origin: render.Vec3{Z: 5}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1}, RowSpacing: 1, ColSpacing: 1, Rows: 8, Columns: 8},
	}, render.DefaultGeometryTolerances())

	if _, _, err := contourToImage(Contour{Points: []Point3D{{Z: 0.20}}}, geometry); err != nil {
		t.Fatalf("0.20 mm offset should be within 5%% of 5 mm spacing: %v", err)
	}
	if _, _, err := contourToImage(Contour{Points: []Point3D{{Z: 0.30}}}, geometry); !errors.Is(err, ErrGeometryMismatch) {
		t.Fatalf("0.30 mm offset error = %v, want ErrGeometryMismatch", err)
	}
}

func testRTGeometry() render.VolumeGeometry {
	slices := []render.SliceGeometry{{
		Origin:     render.Vec3{},
		RowDir:     render.Vec3{X: 1},
		ColDir:     render.Vec3{Y: 1},
		Normal:     render.Vec3{Z: 1},
		RowSpacing: 1,
		ColSpacing: 1,
		Rows:       8,
		Columns:    8,
	}}
	return render.BuildVolumeGeometry(slices, render.DefaultGeometryTolerances())
}
