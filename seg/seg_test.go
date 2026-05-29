package seg

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/derivedio"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/render"
	"github.com/ThalesMMS/dicom-go/roi"
)

func TestBinarySegmentationRoundTripsRoiMasks(t *testing.T) {
	// Given: a sparse ROI segmentation over two source image slices.
	geometry := testGeometry()
	mask := roi.NewRasterMask(8, 6)
	mask.SetRun(2, 1, 4)
	mask.SetRun(3, 2, 5)
	source := roi.NewSegmentation3D(geometry, 8, 6)
	source.SetMask(1, mask)

	doc, err := FromROI(FromROIOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.1",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.2.1",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.2.2",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.2.3",
		Segment: Segment{
			Number:        1,
			Label:         "Liver",
			AlgorithmType: AlgorithmManual,
		},
		ReferencedImages: []ReferencedImage{
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.66.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.0"},
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.66.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
		},
		Segmentation: source,
	})
	if err != nil {
		t.Fatalf("FromROI: %v", err)
	}

	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	for _, tag := range []core.Tag{
		derivedio.TagRows,
		derivedio.TagColumns,
		derivedio.TagSamplesPerPixel,
		derivedio.TagBitsAllocated,
		derivedio.TagBitsStored,
		derivedio.TagHighBit,
		derivedio.TagPixelRepresentation,
	} {
		requireElementVR(t, file.Dataset, tag, core.VRUS)
	}
	segments, ok := file.Dataset.GetSequence(tagSegmentSequence)
	if !ok || len(segments) != 1 {
		t.Fatalf("SegmentSequence ok=%v len=%d, want one item", ok, len(segments))
	}
	requireElementVR(t, segments[0], tagSegmentNumber, core.VRUS)
	perFrameItems, ok := file.Dataset.GetSequence(tagPerFrameFunctionalGroups)
	if !ok || len(perFrameItems) != len(doc.Frames) {
		t.Fatalf("PerFrameFunctionalGroupsSequence ok=%v len=%d, want %d", ok, len(perFrameItems), len(doc.Frames))
	}
	segmentIDs, ok := perFrameItems[0].GetSequence(tagSegmentIdentificationSequence)
	if !ok || len(segmentIDs) != 1 {
		t.Fatalf("SegmentIdentificationSequence ok=%v len=%d, want one item", ok, len(segmentIDs))
	}
	requireElementVR(t, segmentIDs[0], tagReferencedSegmentNumber(), core.VRUS)
	var encoded bytes.Buffer
	if err := object.WriteFile(&encoded, file); err != nil {
		t.Fatalf("object.WriteFile: %v", err)
	}

	// When: the Part 10 object is read back and converted to the ROI model.
	readFile, err := object.ReadFile(bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("object.ReadFile: %v", err)
	}
	roundTrip, err := Read(readFile.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	got, err := ToROI(roundTrip, 1)
	if err != nil {
		t.Fatalf("ToROI: %v", err)
	}

	// Then: geometry, references, and sparse voxels survive the DICOM round-trip.
	if roundTrip.SOPClassUID != SegmentationStorage {
		t.Fatalf("SOPClassUID = %q, want %q", roundTrip.SOPClassUID, SegmentationStorage)
	}
	if roundTrip.Segments[0].Label != "Liver" {
		t.Fatalf("Segment label = %q, want Liver", roundTrip.Segments[0].Label)
	}
	if len(roundTrip.ReferencedImages) != 2 {
		t.Fatalf("ReferencedImages = %d, want 2", len(roundTrip.ReferencedImages))
	}
	if roundTrip.ReferencedImages[0].SeriesInstanceUID != "1.2.826.0.1.3680043.9.7433.66.20.1" {
		t.Fatalf("ReferencedImages = %+v, want series UID preserved", roundTrip.ReferencedImages)
	}
	if got.Columns != 8 || got.Rows != 6 {
		t.Fatalf("ROI size = %dx%d, want 8x6", got.Columns, got.Rows)
	}
	for _, voxel := range [][2]int{{1, 2}, {3, 2}, {2, 3}, {4, 3}} {
		if !got.Voxel(voxel[0], voxel[1], 1) {
			t.Fatalf("missing voxel %v on slice 1", voxel)
		}
	}
	if got.Voxel(0, 0, 1) {
		t.Fatal("unexpected voxel at 0,0 on slice 1")
	}
}

func TestPerFrameSequenceOmitsEmptyReferencedSOPInstanceUID(t *testing.T) {
	doc := &Document{Frames: []Frame{{SegmentNumber: 1}}}
	obj := object.FromElements([]core.Element{perFrameSequence(doc)}, nil)
	items, ok := obj.GetSequence(tagPerFrameFunctionalGroups)
	if !ok || len(items) != 1 {
		t.Fatalf("PerFrameFunctionalGroupsSequence = len %d, ok %v; want one item", len(items), ok)
	}
	if items[0].Has(derivedio.TagRefSOPInstanceUID) {
		t.Fatal("per-frame item contains an empty Referenced SOP Instance UID")
	}
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

func TestFromROIMasksRoundTripsMultipleMasksWithReferencesAndCodes(t *testing.T) {
	first := roi.NewRasterMask(5, 4)
	first.SetRun(1, 1, 4)
	second := roi.NewRasterMask(5, 4)
	second.Set(3, 2, true)

	doc, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.41",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.4.1",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.4.2",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.4.3",
		Geometry:            testGeometry(),
		Masks: []ROIMask{
			{
				Segment: Segment{
					Number:                    5,
					Label:                     "Liver mask",
					AlgorithmType:             AlgorithmManual,
					RecommendedDisplayCIELab:  [3]uint16{32768, 40000, 25000},
					SegmentedPropertyCategory: CodedEntry{CodeValue: "T-D0050", CodingSchemeDesignator: "SRT", CodeMeaning: "Tissue"},
					SegmentedPropertyType:     CodedEntry{CodeValue: "10200004", CodingSchemeDesignator: "SCT", CodeMeaning: "Liver"},
				},
				SliceIndex: 4,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.40.1",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.4",
					Frames:            []int{3},
				},
				Mask: first,
			},
			{
				Segment: Segment{
					Number:                    9,
					Label:                     "Kidney mask",
					AlgorithmType:             AlgorithmManual,
					SegmentedPropertyCategory: CodedEntry{CodeValue: "T-D0050", CodingSchemeDesignator: "SRT", CodeMeaning: "Tissue"},
					SegmentedPropertyType:     CodedEntry{CodeValue: "64033007", CodingSchemeDesignator: "SCT", CodeMeaning: "Kidney"},
				},
				SliceIndex: 7,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.40.2",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.7",
					Frames:            []int{2},
				},
				Mask: second,
			},
		},
	})
	if err != nil {
		t.Fatalf("FromROIMasks: %v", err)
	}

	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if roundTrip.Rows != 4 || roundTrip.Columns != 5 {
		t.Fatalf("SEG dimensions = %dx%d, want 5x4 masks", roundTrip.Columns, roundTrip.Rows)
	}
	if len(roundTrip.Segments) != 2 {
		t.Fatalf("segments = %d, want 2", len(roundTrip.Segments))
	}
	if got := roundTrip.Segments[0].SegmentedPropertyType.CodeMeaning; got != "Liver" {
		t.Fatalf("first segmented property type = %q, want Liver", got)
	}
	if got := roundTrip.Segments[0].RecommendedDisplayCIELab; got != ([3]uint16{32768, 40000, 25000}) {
		t.Fatalf("first recommended display CIELab = %v, want [32768 40000 25000]", got)
	}
	if got := roundTrip.Segments[1].SegmentedPropertyCategory.CodeMeaning; got != "Tissue" {
		t.Fatalf("second segmented property category = %q, want Tissue", got)
	}
	if len(roundTrip.ReferencedImages) != 2 {
		t.Fatalf("referenced images = %d, want 2", len(roundTrip.ReferencedImages))
	}
	if roundTrip.Frames[0].SliceIndex != 4 || roundTrip.Frames[0].ReferencedSOPInstanceUID != "1.2.3.source.4" || roundTrip.Frames[0].ReferencedFrameNumber != 3 {
		t.Fatalf("first frame reference = %+v, want slice 4 source frame 3", roundTrip.Frames[0])
	}
	if roundTrip.Frames[1].SliceIndex != 7 || roundTrip.Frames[1].ReferencedSOPInstanceUID != "1.2.3.source.7" || roundTrip.Frames[1].ReferencedFrameNumber != 2 {
		t.Fatalf("second frame reference = %+v, want slice 7 source frame 2", roundTrip.Frames[1])
	}
	liver, err := ToROI(roundTrip, 5)
	if err != nil {
		t.Fatalf("ToROI liver: %v", err)
	}
	if !liver.Voxel(2, 1, 4) || liver.Voxel(3, 2, 7) {
		t.Fatalf("liver mask voxels not isolated to segment 5")
	}
	kidney, err := ToROI(roundTrip, 9)
	if err != nil {
		t.Fatalf("ToROI kidney: %v", err)
	}
	if !kidney.Voxel(3, 2, 7) || kidney.Voxel(2, 1, 4) {
		t.Fatalf("kidney mask voxels not isolated to segment 9")
	}
}

func TestFromROIMasksRoundTripsMultipleSegmentsOnSliceZero(t *testing.T) {
	first := roi.NewRasterMask(5, 4)
	first.Set(1, 1, true)
	second := roi.NewRasterMask(5, 4)
	second.Set(3, 2, true)

	doc, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.45",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.4.41",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.4.42",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.4.43",
		Geometry:            testGeometry(),
		Masks: []ROIMask{
			{
				Segment:    Segment{Number: 1, Label: "First"},
				SliceIndex: 0,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.3.series",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.1",
				},
				Geometry: testGeometry().Slices[0],
				Mask:     first,
			},
			{
				Segment:    Segment{Number: 2, Label: "Second"},
				SliceIndex: 0,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.3.series",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.1",
				},
				Geometry: testGeometry().Slices[0],
				Mask:     second,
			},
		},
	})
	if err != nil {
		t.Fatalf("FromROIMasks: %v", err)
	}
	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if len(roundTrip.Frames) != 2 {
		t.Fatalf("frames = %d, want 2", len(roundTrip.Frames))
	}
	if roundTrip.Frames[0].SliceIndex != 0 || roundTrip.Frames[1].SliceIndex != 0 {
		t.Fatalf("frame slice indexes = %d/%d, want both on slice 0", roundTrip.Frames[0].SliceIndex, roundTrip.Frames[1].SliceIndex)
	}
	secondROI, err := ToROI(roundTrip, 2)
	if err != nil {
		t.Fatalf("ToROI segment 2: %v", err)
	}
	if !secondROI.Voxel(3, 2, 0) || secondROI.Voxel(3, 2, 1) {
		t.Fatalf("segment 2 voxel was not reconstructed on explicit source slice 0")
	}
}

func TestBinarySegmentationPacksAndReadsFramesAsContinuousBits(t *testing.T) {
	first := roi.NewRasterMask(3, 3)
	first.Set(0, 0, true)
	first.Set(2, 2, true)
	second := roi.NewRasterMask(3, 3)
	second.Set(0, 0, true)
	second.Set(2, 2, true)
	doc := &Document{
		SOPClassUID: SegmentationStorage,
		Rows:        3,
		Columns:     3,
		Frames: []Frame{
			{SegmentNumber: 1, Mask: first},
			{SegmentNumber: 1, Mask: second},
		},
	}
	want := []byte{0x01, 0x03, 0x02}

	if got := binaryPixelData(doc); !bytes.Equal(got, want) {
		t.Fatalf("binaryPixelData() = %#v, want continuous bit stream %#v", got, want)
	}

	obj := object.FromElements([]core.Element{
		derivedio.Raw(derivedio.TagPixelData, core.VROB, want),
	}, nil)
	frames := readBinaryFrames(obj, doc, []*object.Object{nil, nil})
	if len(frames) != 2 {
		t.Fatalf("readBinaryFrames() returned %d frames, want 2", len(frames))
	}
	for i, frame := range frames {
		if frame.Mask == nil {
			t.Fatalf("frame %d mask is nil", i)
		}
		if !frame.Mask.Get(0, 0) || !frame.Mask.Get(2, 2) {
			t.Fatalf("frame %d mask lost continuous bits at first/last pixel", i)
		}
		if frame.Mask.Get(1, 0) || frame.Mask.Get(0, 1) {
			t.Fatalf("frame %d mask has unexpected shifted pixels", i)
		}
	}
}

func TestBinarySegmentationReadsLegacyByteAlignedFrames(t *testing.T) {
	doc := &Document{
		SOPClassUID: SegmentationStorage,
		Rows:        3,
		Columns:     3,
	}
	legacyPerFrameBytes := []byte{
		0x01, 0x01,
		0x01, 0x01,
	}
	obj := object.FromElements([]core.Element{
		derivedio.Raw(derivedio.TagPixelData, core.VROB, legacyPerFrameBytes),
	}, nil)

	frames := readBinaryFrames(obj, doc, []*object.Object{nil, nil})

	if len(frames) != 2 {
		t.Fatalf("readBinaryFrames() returned %d frames, want 2", len(frames))
	}
	for i, frame := range frames {
		if frame.Mask == nil {
			t.Fatalf("frame %d mask is nil", i)
		}
		if !frame.Mask.Get(0, 0) || !frame.Mask.Get(2, 2) {
			t.Fatalf("frame %d mask lost legacy byte-aligned bits at first/last pixel", i)
		}
		if frame.Mask.Get(1, 0) || frame.Mask.Get(0, 1) || frame.Mask.Get(1, 2) {
			t.Fatalf("frame %d mask has shifted legacy pixels", i)
		}
	}
}

func TestReadFrameItemInterpretsDimensionIndexValuesAsOneBased(t *testing.T) {
	item := object.FromElements([]core.Element{
		ulValuesForTest(tagDimensionIndexValues, 2),
	}, nil)

	frame := readFrameItem(item, 0, 0, render.VolumeGeometry{})

	if frame.SliceIndex != 1 {
		t.Fatalf("SliceIndex = %d, want 1 from one-based DimensionIndexValues", frame.SliceIndex)
	}
}

func TestReadFramesUsesDeclaredSpatialDimensionIndex(t *testing.T) {
	frameItems := []core.DataSet{
		derivedio.DataSet(
			derivedio.Seq(tagSegmentIdentificationSequence, derivedio.DataSet(derivedio.IS(tagReferencedSegmentNumber(), 7))),
			ulValuesForTest(tagDimensionIndexValues, 7, 2),
		),
		derivedio.DataSet(
			derivedio.Seq(tagSegmentIdentificationSequence, derivedio.DataSet(derivedio.IS(tagReferencedSegmentNumber(), 7))),
			ulValuesForTest(tagDimensionIndexValues, 7, 3),
		),
	}
	obj := object.FromElements([]core.Element{
		derivedio.Seq(tagDimensionIndexSequence,
			derivedio.DataSet(
				atValuesForTest(tagDimensionIndexPointer, tagReferencedSegmentNumber()),
				atValuesForTest(tagFunctionalGroupPointer, tagSegmentIdentificationSequence),
			),
			derivedio.DataSet(
				atValuesForTest(tagDimensionIndexPointer, tagImagePositionPatient),
				atValuesForTest(tagFunctionalGroupPointer, tagPlanePositionSequence),
			),
		),
		derivedio.Seq(tagPerFrameFunctionalGroups, frameItems...),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{0x11}),
	}, nil)
	doc := &Document{Rows: 2, Columns: 2}

	frames := readFrames(obj, doc)

	if len(frames) != 2 {
		t.Fatalf("readFrames returned %d frames, want 2", len(frames))
	}
	if frames[0].SliceIndex != 1 || frames[1].SliceIndex != 2 {
		t.Fatalf("SliceIndex = %d/%d, want spatial dimension values 1/2", frames[0].SliceIndex, frames[1].SliceIndex)
	}
}

func TestReadFramesIgnoresDimensionValuesWithoutDimensionIndexSequence(t *testing.T) {
	frameItems := []core.DataSet{
		derivedio.DataSet(ulValuesForTest(tagDimensionIndexValues, 9)),
		derivedio.DataSet(ulValuesForTest(tagDimensionIndexValues, 10)),
	}
	obj := object.FromElements([]core.Element{
		derivedio.Seq(tagPerFrameFunctionalGroups, frameItems...),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{0x03}),
	}, nil)
	doc := &Document{Rows: 1, Columns: 1}

	frames := readFrames(obj, doc)

	if len(frames) != 2 {
		t.Fatalf("readFrames returned %d frames, want 2", len(frames))
	}
	if frames[0].SliceIndex != 0 || frames[1].SliceIndex != 1 {
		t.Fatalf("SliceIndex = %d/%d, want fallback frame indexes 0/1", frames[0].SliceIndex, frames[1].SliceIndex)
	}
}

func TestReadBinaryFramesUsesOnlySegmentWhenIdentificationIsAbsent(t *testing.T) {
	obj := derivedio.Object(
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{0x01}),
	)
	doc := &Document{
		SOPClassUID: SegmentationStorage,
		Rows:        2,
		Columns:     2,
		Segments:    []Segment{{Number: 7}},
	}

	doc.Frames = readFrames(obj, doc)

	if len(doc.Frames) != 1 || doc.Frames[0].SegmentNumber != 7 {
		t.Fatalf("decoded frames = %+v, want one frame assigned to segment 7", doc.Frames)
	}
	segmentation, err := ToROI(doc, 7)
	if err != nil {
		t.Fatalf("ToROI() error = %v", err)
	}
	if !segmentation.Voxel(0, 0, 0) {
		t.Fatal("ToROI() silently discarded binary frame without Segment Identification")
	}
}

func TestReadFramesUsesSpatialPointerOverPlanePositionGroup(t *testing.T) {
	temporalPositionIndex := core.NewTag(0x0020, 0x9128)
	frameItems := []core.DataSet{
		derivedio.DataSet(ulValuesForTest(tagDimensionIndexValues, 7, 2)),
		derivedio.DataSet(ulValuesForTest(tagDimensionIndexValues, 7, 3)),
	}
	obj := object.FromElements([]core.Element{
		derivedio.Seq(tagDimensionIndexSequence,
			derivedio.DataSet(
				atValuesForTest(tagDimensionIndexPointer, temporalPositionIndex),
				atValuesForTest(tagFunctionalGroupPointer, tagPlanePositionSequence),
			),
			derivedio.DataSet(
				atValuesForTest(tagDimensionIndexPointer, tagInStackPositionNumber),
				atValuesForTest(tagFunctionalGroupPointer, tagPlanePositionSequence),
			),
		),
		derivedio.Seq(tagPerFrameFunctionalGroups, frameItems...),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{0x11}),
	}, nil)
	doc := &Document{Rows: 2, Columns: 2}

	frames := readFrames(obj, doc)

	if len(frames) != 2 {
		t.Fatalf("readFrames returned %d frames, want 2", len(frames))
	}
	if frames[0].SliceIndex != 1 || frames[1].SliceIndex != 2 {
		t.Fatalf("SliceIndex = %d/%d, want spatial dimension values 1/2", frames[0].SliceIndex, frames[1].SliceIndex)
	}
}

func TestReadFrameItemPrefersPlanePositionWhenVolumeGeometryMatches(t *testing.T) {
	item := object.FromElements([]core.Element{
		ulValuesForTest(tagDimensionIndexValues, 1),
		derivedio.Seq(tagPlanePositionSequence, derivedio.DataSet(
			derivedio.DS(tagImagePositionPatient, 0, 0, 2.5),
		)),
	}, nil)

	frame := readFrameItem(item, 0, 0, testGeometry())

	if frame.SliceIndex != 1 {
		t.Fatalf("SliceIndex = %d, want plane position to resolve to slice 1", frame.SliceIndex)
	}
	if frame.Geometry.Origin.Z != 2.5 {
		t.Fatalf("Frame geometry origin z = %g, want 2.5", frame.Geometry.Origin.Z)
	}
}

func TestFromROIMasksRejectsMissingSourceReference(t *testing.T) {
	mask := roi.NewRasterMask(2, 2)
	mask.Set(0, 0, true)
	_, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.42",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.4.11",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.4.12",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.4.13",
		Masks: []ROIMask{{
			Segment: Segment{Label: "Missing source"},
			Mask:    mask,
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("FromROIMasks error = %v, want ErrMissingReference", err)
	}
}

func TestFromROIMasksRejectsMismatchedMaskDimensions(t *testing.T) {
	_, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.43",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.4.21",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.4.22",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.4.23",
		Rows:                4,
		Columns:             4,
		Masks: []ROIMask{{
			Segment:     Segment{Label: "Wrong size"},
			SourceImage: ReferencedImage{SeriesInstanceUID: "1.2.3.series", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.source"},
			Mask:        roi.NewRasterMask(5, 4),
		}},
	})
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("FromROIMasks error = %v, want ErrInvalidObject", err)
	}
}

func TestSEGWriteIncludesGeometryFunctionalGroups(t *testing.T) {
	geometry := testGeometry()
	first := roi.NewRasterMask(8, 6)
	first.Set(1, 1, true)
	second := roi.NewRasterMask(8, 6)
	second.Set(2, 2, true)
	doc, err := FromROIMasks(FromROIMasksOptions{
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.44",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.4.31",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.4.32",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.4.33",
		Geometry:            geometry,
		Masks: []ROIMask{
			{
				Segment:    Segment{Label: "First"},
				SliceIndex: 4,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.3.series",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.1",
				},
				Geometry: geometry.Slices[0],
				Mask:     first,
			},
			{
				Segment:    Segment{Label: "Second"},
				SliceIndex: 7,
				SourceImage: ReferencedImage{
					SeriesInstanceUID: "1.2.3.series",
					SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
					SOPInstanceUID:    "1.2.3.source.2",
					Frames:            []int{4},
				},
				Geometry: geometry.Slices[1],
				Mask:     second,
			},
		},
	})
	if err != nil {
		t.Fatalf("FromROIMasks: %v", err)
	}
	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}

	shared, ok := file.Dataset.GetSequence(tagSharedFunctionalGroups)
	if !ok || len(shared) != 1 {
		t.Fatalf("SharedFunctionalGroups = len %d ok=%v, want one item", len(shared), ok)
	}
	pixelMeasures, ok := shared[0].GetSequence(tagPixelMeasuresSequence)
	if !ok || len(pixelMeasures) != 1 {
		t.Fatalf("PixelMeasuresSequence = len %d ok=%v, want one item", len(pixelMeasures), ok)
	}
	spacing, err := pixelMeasures[0].GetFloats(tagPixelSpacing)
	if err != nil || len(spacing) != 2 || spacing[0] != 1 || spacing[1] != 1 {
		t.Fatalf("PixelSpacing = %v err=%v, want 1\\1", spacing, err)
	}
	orientationItems, ok := shared[0].GetSequence(tagPlaneOrientationSequence)
	if !ok || len(orientationItems) != 1 {
		t.Fatalf("PlaneOrientationSequence = len %d ok=%v, want one item", len(orientationItems), ok)
	}
	orientation, err := orientationItems[0].GetFloats(tagImageOrientationPatient)
	if err != nil || len(orientation) != 6 || orientation[0] != 1 || orientation[4] != 1 {
		t.Fatalf("ImageOrientationPatient = %v err=%v, want axial orientation", orientation, err)
	}

	perFrame, ok := file.Dataset.GetSequence(tagPerFrameFunctionalGroups)
	if !ok || len(perFrame) != 2 {
		t.Fatalf("PerFrameFunctionalGroups = len %d ok=%v, want two items", len(perFrame), ok)
	}
	if perFrame[0].Has(derivedio.TagRefFrameNumber) {
		t.Fatal("single-frame source emitted invalid Referenced Frame Number 0")
	}
	if got := derivedio.Int(perFrame[1], derivedio.TagRefFrameNumber); got != 4 {
		t.Fatalf("second Referenced Frame Number = %d, want 4", got)
	}
	firstDimensions := ulValuesFromObjectForTest(t, perFrame[0], tagDimensionIndexValues)
	secondDimensions := ulValuesFromObjectForTest(t, perFrame[1], tagDimensionIndexValues)
	if len(firstDimensions) != 1 || firstDimensions[0] != 5 || len(secondDimensions) != 1 || secondDimensions[0] != 8 {
		t.Fatalf("DimensionIndexValues = %v/%v, want one-based slice positions 5/8", firstDimensions, secondDimensions)
	}
	positions, ok := perFrame[1].GetSequence(tagPlanePositionSequence)
	if !ok || len(positions) != 1 {
		t.Fatalf("second PlanePositionSequence = len %d ok=%v, want one item", len(positions), ok)
	}
	position, err := positions[0].GetFloats(tagImagePositionPatient)
	if err != nil || len(position) != 3 || position[2] != 2.5 {
		t.Fatalf("second ImagePositionPatient = %v err=%v, want z=2.5", position, err)
	}
	derivation, ok := perFrame[1].GetSequence(tagDerivationImageSequence)
	if !ok || len(derivation) != 1 {
		t.Fatalf("second DerivationImageSequence = len %d ok=%v, want one item", len(derivation), ok)
	}
	source, ok := derivation[0].GetSequence(tagSourceImageSequence)
	if !ok || len(source) != 1 {
		t.Fatalf("second SourceImageSequence = len %d ok=%v, want one item", len(source), ok)
	}
	if got := uidForTest(t, source[0], derivedio.TagRefSOPInstanceUID); got != "1.2.3.source.2" {
		t.Fatalf("source image ref = %q, want second source", got)
	}
}

func TestSEGRoundTripPreservesSharedGeometryAndPhysicalStatistics(t *testing.T) {
	slices := []render.SliceGeometry{
		{
			Origin: render.Vec3{X: 10, Y: 20, Z: 30}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1},
			RowSpacing: 0.7, ColSpacing: 1.3, Rows: 2, Columns: 3,
		},
		{
			Origin: render.Vec3{X: 10, Y: 20, Z: 34}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1},
			RowSpacing: 0.7, ColSpacing: 1.3, Rows: 2, Columns: 3,
		},
		{
			Origin: render.Vec3{X: 10, Y: 20, Z: 38}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1},
			RowSpacing: 0.7, ColSpacing: 1.3, Rows: 2, Columns: 3,
		},
		{
			Origin: render.Vec3{X: 10, Y: 20, Z: 42}, RowDir: render.Vec3{X: 1}, ColDir: render.Vec3{Y: 1}, Normal: render.Vec3{Z: 1},
			RowSpacing: 0.7, ColSpacing: 1.3, Rows: 2, Columns: 3,
		},
	}
	geometry := render.BuildVolumeGeometry(slices, render.DefaultGeometryTolerances())
	first := roi.NewRasterMask(3, 2)
	first.Set(0, 0, true)
	second := roi.NewRasterMask(3, 2)
	second.Set(1, 1, true)
	doc := &Document{
		SOPClassUID:       SegmentationStorage,
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.66.299",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.299.1",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.299.2",
		Rows:              2,
		Columns:           3,
		Segments:          []Segment{{Number: 1, Label: "Target"}},
		Geometry:          geometry,
		Frames: []Frame{
			{SegmentNumber: 1, SliceIndex: 0, Geometry: slices[0], Mask: first},
			{SegmentNumber: 1, SliceIndex: 3, Geometry: slices[3], Mask: second},
		},
	}

	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	if len(roundTrip.Geometry.Slices) != 4 {
		t.Fatalf("Geometry slices = %d, want 4 including two reconstructed gaps", len(roundTrip.Geometry.Slices))
	}
	firstGeometry := roundTrip.Geometry.Slices[0]
	if firstGeometry.RowDir != (render.Vec3{X: 1}) || firstGeometry.ColDir != (render.Vec3{Y: 1}) || firstGeometry.Normal != (render.Vec3{Z: 1}) {
		t.Fatalf("orientation = row %v col %v normal %v, want axial", firstGeometry.RowDir, firstGeometry.ColDir, firstGeometry.Normal)
	}
	if firstGeometry.RowSpacing != 0.7 || firstGeometry.ColSpacing != 1.3 || roundTrip.Geometry.MeanSpacing != 4 {
		t.Fatalf("spacing = row %g col %g mean %g, want 0.7/1.3/4", firstGeometry.RowSpacing, firstGeometry.ColSpacing, roundTrip.Geometry.MeanSpacing)
	}
	if firstGeometry.Origin != slices[0].Origin || roundTrip.Geometry.Slices[1].Origin != slices[1].Origin || roundTrip.Geometry.Slices[3].Origin != slices[3].Origin {
		t.Fatalf("origins = %v/%v/%v, want %v/%v/%v", firstGeometry.Origin, roundTrip.Geometry.Slices[1].Origin, roundTrip.Geometry.Slices[3].Origin, slices[0].Origin, slices[1].Origin, slices[3].Origin)
	}

	segmentation, err := ToROI(roundTrip, 1)
	if err != nil {
		t.Fatalf("ToROI: %v", err)
	}
	if !segmentation.Voxel(0, 0, 0) || !segmentation.Voxel(1, 1, 3) || segmentation.Voxel(1, 1, 1) {
		t.Fatal("round-trip did not preserve sparse physical slice indexes 0 and 3")
	}
	stats := segmentation.Statistics(func(_, _, _ int) (float64, bool) { return 1, true }, 0)
	if math.Abs(stats.AreaMM2-1.82) > 1e-12 || math.Abs(stats.VolumeMM3-7.28) > 1e-12 {
		t.Fatalf("physical stats = area %g volume %g, want 1.82 mm2 / 7.28 mm3", stats.AreaMM2, stats.VolumeMM3)
	}
}

func TestLabelMapSegmentationRoundTripsLabelValues(t *testing.T) {
	// Given: a dense label-map segmentation with one labeled voxel.
	doc := Document{
		SOPClassUID:         LabelMapSegmentationStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.7",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.3.1",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.3.2",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.3.3",
		Rows:                3,
		Columns:             4,
		Segments:            []Segment{{Number: 2, Label: "Tumor", AlgorithmType: AlgorithmManual}},
		Frames: []Frame{{
			SegmentNumber: 2,
			SliceIndex:    0,
			LabelMap:      []uint16{0, 0, 0, 0, 0, 2, 0, 0, 0, 0, 0, 0},
		}},
	}

	// When: the object is written and read back through DICOM elements.
	file, err := Write(&doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Then: label-map SOP class and pixel label value survive.
	if roundTrip.SOPClassUID != LabelMapSegmentationStorage {
		t.Fatalf("SOPClassUID = %q, want label map", roundTrip.SOPClassUID)
	}
	if len(roundTrip.Frames) != 1 || len(roundTrip.Frames[0].LabelMap) != 12 {
		t.Fatalf("LabelMap length = %d, want 12", len(roundTrip.Frames[0].LabelMap))
	}
	if roundTrip.Frames[0].LabelMap[5] != 2 {
		t.Fatalf("label value = %d, want 2", roundTrip.Frames[0].LabelMap[5])
	}
}

func TestWriteRejectsFramePayloadIncompatibleWithSopClass(t *testing.T) {
	mask := roi.NewRasterMask(2, 1)
	mask.Set(0, 0, true)
	tests := []struct {
		name     string
		sopClass string
		frame    Frame
	}{
		{
			name:     "label map payload as binary segmentation",
			sopClass: SegmentationStorage,
			frame:    Frame{SegmentNumber: 1, LabelMap: []uint16{1, 0}},
		},
		{
			name:     "binary mask payload as label map segmentation",
			sopClass: LabelMapSegmentationStorage,
			frame:    Frame{SegmentNumber: 1, Mask: mask},
		},
	}

	for i, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc := &Document{
				SOPClassUID:       tt.sopClass,
				SOPInstanceUID:    fmt.Sprintf("1.2.826.0.1.3680043.9.7433.301.%d", i+1),
				StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.301.10",
				SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.301.11",
				Rows:              1,
				Columns:           2,
				Segments:          []Segment{{Number: 1, Label: "Target"}},
				Frames:            []Frame{tt.frame},
			}

			if _, err := Write(doc); !errors.Is(err, ErrUnsupportedFramePayload) {
				t.Fatalf("Write error = %v, want ErrUnsupportedFramePayload", err)
			}
		})
	}
}

func TestLabelMapSegmentationPreservesPerFrameMetadata(t *testing.T) {
	// Given: a label-map frame with explicit per-frame segment, slice, and source reference metadata.
	doc := Document{
		SOPClassUID:         LabelMapSegmentationStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.8",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.3.11",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.3.12",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.3.13",
		Rows:                2,
		Columns:             2,
		Segments:            []Segment{{Number: 7, Label: "Target", AlgorithmType: AlgorithmManual}},
		Frames: []Frame{{
			SegmentNumber:            7,
			SliceIndex:               4,
			ReferencedSOPInstanceUID: "1.2.826.0.1.3680043.9.7433.3.14",
			ReferencedFrameNumber:    3,
			LabelMap:                 []uint16{0, 7, 0, 0},
		}},
	}

	// When: the label-map object is written and read back.
	file, err := Write(&doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Then: pixel data is attached to the decoded frame without losing per-frame metadata.
	if len(roundTrip.Frames) != 1 {
		t.Fatalf("frames = %d, want 1", len(roundTrip.Frames))
	}
	frame := roundTrip.Frames[0]
	if frame.SegmentNumber != 7 || frame.SliceIndex != 4 {
		t.Fatalf("frame metadata = segment %d slice %d, want segment 7 slice 4", frame.SegmentNumber, frame.SliceIndex)
	}
	if frame.ReferencedSOPInstanceUID != "1.2.826.0.1.3680043.9.7433.3.14" || frame.ReferencedFrameNumber != 3 {
		t.Fatalf("frame reference = %q/%d, want source reference", frame.ReferencedSOPInstanceUID, frame.ReferencedFrameNumber)
	}
	if len(frame.LabelMap) != 4 || frame.LabelMap[1] != 7 {
		t.Fatalf("LabelMap = %+v, want label value preserved", frame.LabelMap)
	}
}

func TestToROIRejectsLabelMapWithZeroColumns(t *testing.T) {
	// Given: a malformed label-map SEG with pixel values but no column count.
	doc := &Document{
		Rows:    2,
		Columns: 0,
		Segments: []Segment{{
			Number: 1,
		}},
		Frames: []Frame{{
			SegmentNumber: 1,
			LabelMap:      []uint16{1},
		}},
	}

	// When / Then: conversion reports a typed invalid-object error instead of dividing by zero.
	_, err := ToROI(doc, 1)
	if !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("ToROI error = %v, want ErrInvalidObject", err)
	}
}

func TestSEGReferencedSeriesSequenceGroupsBySeries(t *testing.T) {
	doc := &Document{
		SOPClassUID:         SegmentationStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.9.7433.66.9",
		StudyInstanceUID:    "1.2.826.0.1.3680043.9.7433.3.21",
		SeriesInstanceUID:   "1.2.826.0.1.3680043.9.7433.3.22",
		FrameOfReferenceUID: "1.2.826.0.1.3680043.9.7433.3.23",
		Rows:                2,
		Columns:             2,
		Segments:            []Segment{{Number: 1, Label: "Target", AlgorithmType: AlgorithmManual}},
		ReferencedImages: []ReferencedImage{
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.66.20.1", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.1"},
			{SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.66.20.2", SOPClassUID: "1.2.840.10008.5.1.4.1.1.2", SOPInstanceUID: "1.2.3.image.2"},
		},
		Frames: []Frame{{SegmentNumber: 1, Mask: roi.NewRasterMask(2, 2)}},
	}
	file, err := Write(doc)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	items, ok := file.Dataset.GetSequence(tagReferencedSeriesSequence)
	if !ok || len(items) != 2 {
		t.Fatalf("ReferencedSeriesSequence = len %d ok=%v, want 2 items", len(items), ok)
	}
	for i, want := range []string{"1.2.826.0.1.3680043.9.7433.66.20.1", "1.2.826.0.1.3680043.9.7433.66.20.2"} {
		got := uidForTest(t, items[i], core.NewTag(0x0020, 0x000E))
		if got != want {
			t.Fatalf("ReferencedSeriesSequence[%d] SeriesInstanceUID = %q, want %q", i, got, want)
		}
	}
	roundTrip, err := Read(file.Dataset)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if roundTrip.ReferencedImages[1].SeriesInstanceUID != "1.2.826.0.1.3680043.9.7433.66.20.2" {
		t.Fatalf("ReferencedImages = %+v, want series UID preserved", roundTrip.ReferencedImages)
	}
}

func TestSEGWriteRejectsReferencedImageWithoutSeriesUid(t *testing.T) {
	_, err := Write(&Document{
		SOPClassUID:       SegmentationStorage,
		SOPInstanceUID:    "1.2.826.0.1.3680043.9.7433.66.10",
		StudyInstanceUID:  "1.2.826.0.1.3680043.9.7433.3.31",
		SeriesInstanceUID: "1.2.826.0.1.3680043.9.7433.3.32",
		Rows:              2,
		Columns:           2,
		Segments:          []Segment{{Number: 1, Label: "Target", AlgorithmType: AlgorithmManual}},
		ReferencedImages: []ReferencedImage{{
			SOPClassUID:    "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID: "1.2.3.image.1",
		}},
	})
	if !errors.Is(err, ErrMissingReference) {
		t.Fatalf("Write error = %v, want ErrMissingReference", err)
	}
}

func uidForTest(t *testing.T, obj *object.Object, tag core.Tag) string {
	t.Helper()
	value, ok := obj.GetUID(tag)
	if !ok {
		t.Fatalf("missing UID tag %s", tag)
	}
	return value
}

func TestSEGWriteRejectsFractionalSegmentationBeforeSerializing(t *testing.T) {
	// Given: a document labelled FRACTIONAL even though Frame has no field that
	// can preserve 8-bit probability or occupancy samples.
	doc := &Document{
		SOPClassUID:      SegmentationStorage,
		SegmentationType: SegmentationTypeFractional,
	}

	// When / Then: Write fails explicitly instead of emitting FRACTIONAL with
	// one-bit binary Pixel Data.
	file, err := Write(doc)
	if !errors.Is(err, ErrUnsupportedSegmentationType) {
		t.Fatalf("Write error = %v, want ErrUnsupportedSegmentationType", err)
	}
	if file != nil {
		t.Fatal("Write returned a file for an unsupported fractional segmentation")
	}
}

func ulValuesForTest(tag core.Tag, values ...uint32) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		binary.LittleEndian.PutUint32(raw[i*4:], value)
	}
	return core.NewRawElement(tag, core.VRUL, raw)
}

func atValuesForTest(tag core.Tag, values ...core.Tag) core.Element {
	raw := make([]byte, len(values)*4)
	for i, value := range values {
		offset := i * 4
		binary.LittleEndian.PutUint16(raw[offset:], value.Group)
		binary.LittleEndian.PutUint16(raw[offset+2:], value.Element)
	}
	return core.NewRawElement(tag, core.VRAT, raw)
}

func ulValuesFromObjectForTest(t *testing.T, obj *object.Object, tag core.Tag) []uint32 {
	t.Helper()
	elem, ok := obj.Get(tag)
	if !ok {
		t.Fatalf("missing UL tag %s", tag)
	}
	if elem.VR() != core.VRUL {
		t.Fatalf("VR for %s = %s, want UL", tag, elem.VR())
	}
	raw, ok := elem.RawBytes()
	if !ok {
		t.Fatalf("tag %s does not carry raw bytes", tag)
	}
	if len(raw)%4 != 0 {
		t.Fatalf("tag %s has %d raw bytes, want multiple of 4", tag, len(raw))
	}
	values := make([]uint32, len(raw)/4)
	for i := range values {
		values[i] = binary.LittleEndian.Uint32(raw[i*4:])
	}
	return values
}

func TestReadRejectsWrongSopClassWithTypedError(t *testing.T) {
	// Given: a non-SEG dataset.
	dataset := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0016), VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.2"}},
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0018), VR: core.VRUI}, Value: core.StringValue{"1.2.3.not.seg"}},
	}, nil)

	// When / Then: Read surfaces a branchable unsupported-SOP-class error.
	_, err := Read(dataset)
	if !errors.Is(err, ErrUnsupportedSOPClass) {
		t.Fatalf("Read error = %v, want ErrUnsupportedSOPClass", err)
	}
}

func TestReadRejectsFractionalSegmentationBeforeDecodingPixels(t *testing.T) {
	// Given: a valid SEG SOP class whose 8-bit samples represent fractional
	// values, not a packed binary mask.
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, SegmentationStorage),
		derivedio.CS(tagSegmentationType, SegmentationTypeFractional),
		derivedio.IS(derivedio.TagRows, 1),
		derivedio.IS(derivedio.TagColumns, 2),
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
		derivedio.IS(derivedio.TagBitsAllocated, 8),
		derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{64, 255}),
	)

	// When / Then: Read fails explicitly instead of interpreting the sample
	// bytes as sixteen packed binary pixels.
	_, err := Read(dataset)
	if !errors.Is(err, ErrUnsupportedSegmentationType) {
		t.Fatalf("Read error = %v, want ErrUnsupportedSegmentationType", err)
	}
}

func TestReadRejectsMissingPixelData(t *testing.T) {
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, SegmentationStorage),
		derivedio.CS(tagSegmentationType, SegmentationTypeBinary),
		derivedio.IS(derivedio.TagRows, 1),
		derivedio.IS(derivedio.TagColumns, 1),
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
	)

	if _, err := Read(dataset); !errors.Is(err, ErrInvalidObject) {
		t.Fatalf("Read error = %v, want ErrInvalidObject", err)
	}
}

func TestReadRejectsEncapsulatedPixelData(t *testing.T) {
	dataset := derivedio.Object(
		derivedio.UI(derivedio.TagSOPClassUID, SegmentationStorage),
		derivedio.CS(tagSegmentationType, SegmentationTypeBinary),
		derivedio.IS(derivedio.TagRows, 1),
		derivedio.IS(derivedio.TagColumns, 1),
		derivedio.IS(derivedio.TagNumberOfFrames, 1),
		core.Element{
			Header: core.ElementHeader{
				Tag:       derivedio.TagPixelData,
				VR:        core.VROB,
				Length:    core.UndefinedLength,
				LengthSet: true,
			},
			Value: core.FragmentSequence{Fragments: [][]byte{{0x01}}},
		},
	)

	if _, err := Read(dataset); !errors.Is(err, ErrUnsupportedPixelData) {
		t.Fatalf("Read error = %v, want ErrUnsupportedPixelData", err)
	}
}

func TestReadRejectsUnsafeDimensionsBeforeAllocating(t *testing.T) {
	tests := []struct {
		name    string
		rows    int
		columns int
		frames  int
		want    error
	}{
		{name: "excessive frame count", rows: 1, columns: 1, frames: MaxReadFrames + 1, want: ErrResourceLimitExceeded},
		{name: "rows exceed US range", rows: 65_536, columns: 1, frames: 1, want: ErrResourceLimitExceeded},
		{name: "excessive total pixels", rows: 65_535, columns: 65_535, frames: 1, want: ErrResourceLimitExceeded},
		{name: "zero rows", rows: 0, columns: 1, frames: 1, want: ErrInvalidObject},
		{name: "zero frames", rows: 1, columns: 1, frames: 0, want: ErrInvalidObject},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dataset := derivedio.Object(
				derivedio.UI(derivedio.TagSOPClassUID, SegmentationStorage),
				derivedio.CS(tagSegmentationType, SegmentationTypeBinary),
				derivedio.IS(derivedio.TagRows, tt.rows),
				derivedio.IS(derivedio.TagColumns, tt.columns),
				derivedio.IS(derivedio.TagNumberOfFrames, tt.frames),
				derivedio.Raw(derivedio.TagPixelData, core.VROB, []byte{0x00}),
			)

			if _, err := Read(dataset); !errors.Is(err, tt.want) {
				t.Fatalf("Read error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestWriteRejectsUnrepresentableDimensionsAndFrameCount(t *testing.T) {
	tests := []struct {
		name   string
		doc    *Document
		wanted error
	}{
		{
			name:   "rows exceed US range",
			doc:    &Document{Rows: maxReadDimension + 1, Columns: 1, Frames: []Frame{{}}},
			wanted: ErrResourceLimitExceeded,
		},
		{
			name:   "frame count exceeds supported IS range",
			doc:    &Document{Rows: 1, Columns: 1, Frames: make([]Frame, MaxReadFrames+1)},
			wanted: ErrResourceLimitExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Write(tt.doc); !errors.Is(err, tt.wanted) {
				t.Fatalf("Write() error = %v, want %v", err, tt.wanted)
			}
		})
	}
}

func TestReadSegmentsSkipsUnrepresentableCIELabValues(t *testing.T) {
	obj := derivedio.Object(derivedio.Seq(tagSegmentSequence, derivedio.DataSet(
		derivedio.IS(tagRecommendedDisplayCIELabValue, 1, -1, 65_536),
	)))
	segments := readSegments(obj)
	if len(segments) != 1 {
		t.Fatalf("readSegments() returned %d segments, want 1", len(segments))
	}
	if got := segments[0].RecommendedDisplayCIELab; got != ([3]uint16{}) {
		t.Fatalf("RecommendedDisplayCIELab = %v, want invalid metadata skipped", got)
	}
}

func TestDICOMCIELabDisplayColorRoundTrip(t *testing.T) {
	for _, rgb := range [][3]int{{0, 0, 0}, {255, 255, 255}, {12, 120, 240}} {
		got := DICOMCIELabToRGB(RGBToDICOMCIELab(rgb))
		for channel := range rgb {
			if delta := got[channel] - rgb[channel]; delta < -1 || delta > 1 {
				t.Fatalf("RGB round trip for %v = %v; channel %d differs by %d", rgb, got, channel, delta)
			}
		}
	}
}

func testGeometry() render.VolumeGeometry {
	slices := []render.SliceGeometry{
		{
			Origin:     render.Vec3{X: 0, Y: 0, Z: 0},
			RowDir:     render.Vec3{X: 1},
			ColDir:     render.Vec3{Y: 1},
			Normal:     render.Vec3{Z: 1},
			RowSpacing: 1,
			ColSpacing: 1,
			Rows:       6,
			Columns:    8,
		},
		{
			Origin:     render.Vec3{X: 0, Y: 0, Z: 2.5},
			RowDir:     render.Vec3{X: 1},
			ColDir:     render.Vec3{Y: 1},
			Normal:     render.Vec3{Z: 1},
			RowSpacing: 1,
			ColSpacing: 1,
			Rows:       6,
			Columns:    8,
		},
	}
	return render.BuildVolumeGeometry(slices, render.DefaultGeometryTolerances())
}
