package dicom

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/gsps"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/rtstruct"
	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/vps"
)

func Test_DerivedObjects_are_accepted_by_dcmdump_when_available(t *testing.T) {
	dcmdump := dcmdumpPath(t)
	must := func(file *object.File, err error) *object.File {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return file
	}
	for _, tc := range []struct {
		name string
		file *object.File
	}{
		{name: "seg", file: must(seg.Write(testDcmdumpSEG()))},
		{name: "sr", file: must(sr.WriteMeasurementReport(testDcmdumpSR()))},
		{name: "gsps", file: must(gsps.Write(testDcmdumpGSPS()))},
		{name: "rtstruct", file: must(rtstruct.Write(testDcmdumpRTSTRUCT()))},
		{name: "vps", file: must(vps.Write(testDcmdumpVPS()))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tc.name+".dcm")
			out, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := object.WriteFile(out, tc.file); err != nil {
				_ = out.Close()
				t.Fatalf("object.WriteFile: %v", err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			output, err := exec.CommandContext(ctx, dcmdump, path).CombinedOutput()
			cancel()
			if err != nil {
				t.Fatalf("dcmdump %s: %v\n%s", path, err, string(output))
			}
		})
	}
}

func dcmdumpPath(t *testing.T) string {
	t.Helper()
	if path, err := exec.LookPath("dcmdump"); err == nil {
		return path
	}
	const homebrewDcmdump = "/opt/homebrew/bin/dcmdump"
	if _, err := os.Stat(homebrewDcmdump); err == nil {
		return homebrewDcmdump
	}
	t.Skip("dcmdump not available")
	return ""
}

func testDcmdumpSEG() *seg.Document {
	return &seg.Document{
		SOPClassUID:         seg.LabelMapSegmentationStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.700.1",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.700.2",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.700.3",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.700.4",
		Rows:                2,
		Columns:             2,
		Segments:            []seg.Segment{{Number: 1, Label: "Target", AlgorithmType: seg.AlgorithmManual}},
		Frames: []seg.Frame{{
			SegmentNumber: 1,
			LabelMap:      []uint16{0, 1, 0, 0},
		}},
	}
}

func testDcmdumpSR() *sr.MeasurementReport {
	return &sr.MeasurementReport{
		SOPClassUID:    sr.Comprehensive3DSRStorage,
		SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.701.1",
		ContentDate:    "20260621",
		ContentTime:    "120000",
		Groups: []sr.MeasurementGroup{{
			Tracking: sr.TrackingIdentifier{UID: "1.2.826.0.1.3680043.9.7433.701.2", Identifier: "Lesion 1"},
			ReferencedSegment: sr.SegmentReference{
				SOPClassUID:    seg.SegmentationStorage,
				SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.701.3",
				SegmentNumber:  1,
			},
			Measurements: []sr.ReportMeasurement{{
				ConceptName: sr.CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
				Value:       12.5,
				Units:       sr.CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"},
				Image: sr.ImageReference{
					SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.701.4",
				},
				Spatial: sr.SpatialReference{
					GraphicType: sr.GraphicTypePoint3D,
					Coordinates: []sr.Point3D{{X: 1, Y: 2, Z: 3}},
				},
			}},
		}},
	}
}

func testDcmdumpGSPS() *gsps.State {
	return &gsps.State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.702.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.702.2",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.702.3",
		ReferencedImages: []gsps.ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.702.5",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.702.4",
		}},
		DisplayedArea:        gsps.DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 512, BottomRightY: 512},
		SoftcopyVOI:          gsps.SoftcopyVOI{WindowCenter: 40, WindowWidth: 400},
		PresentationLUTShape: gsps.PresentationLUTIdentity,
	}
}

func testDcmdumpRTSTRUCT() *rtstruct.StructureSet {
	return &rtstruct.StructureSet{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.703.1",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.703.2",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.703.3",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.703.4",
		ROIs: []rtstruct.ROI{{
			Number: 1,
			Name:   "PTV",
			Contours: []rtstruct.Contour{{
				GeometricType: rtstruct.ContourClosedPlanar,
				Points: []rtstruct.Point3D{
					{X: 0, Y: 0, Z: 0},
					{X: 10, Y: 0, Z: 0},
					{X: 10, Y: 10, Z: 0},
					{X: 0, Y: 10, Z: 0},
				},
			}},
		}},
	}
}

func testDcmdumpVPS() *vps.State {
	return &vps.State{
		SOPClassUID:       vps.VolumeRenderingVolumetricPresentationStateStorage,
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.704.1",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.704.2",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.704.3",
		Inputs: []vps.Input{{
			Number:      1,
			InputSetUID: "1.2.826.0.1.3680043.9.7433.704.4",
			ReferencedInstances: []vps.ReferencedInstance{{
				SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
				SOPInstanceUID: "1.2.826.0.1.3680043.9.7433.704.5",
			}},
		}},
		RenderPresetName: render.PresetBonesSkin1,
	}
}
