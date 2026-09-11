package clinicalrelation_test

import (
	"encoding/binary"
	"errors"
	"reflect"
	"testing"

	"github.com/ThalesMMS/dicom-go/clinicalrelation"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/gsps"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/rtdose"
	"github.com/ThalesMMS/dicom-go/rtstruct"
	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/sr"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/vps"
)

func TestResolveSupportedClinicalObjects(t *testing.T) {
	fixtures := clinicalFixtures(t)
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			identity := fixtureIdentity(t, fixture.file)
			resolved, err := clinicalrelation.Resolve(fixture.file.Dataset, identity)
			if err != nil {
				t.Fatalf("Resolve() error = %v", err)
			}
			if resolved.Kind != fixture.kind || resolved.Editable != fixture.editable {
				t.Fatalf("classification = %q/editable=%v, want %q/%v", resolved.Kind, resolved.Editable, fixture.kind, fixture.editable)
			}
			if resolved.StudyInstanceUID != fixture.studyUID || resolved.FrameOfReferenceUID != fixture.frameUID {
				t.Fatalf("identity = study %q frame %q, want %q/%q", resolved.StudyInstanceUID, resolved.FrameOfReferenceUID, fixture.studyUID, fixture.frameUID)
			}
			if got := resolved.ReferencedSOPInstanceUIDs(); !reflect.DeepEqual(got, fixture.referencedSOPs) {
				t.Fatalf("ReferencedSOPInstanceUIDs() = %#v, want %#v", got, fixture.referencedSOPs)
			}
			if got := resolved.ReferencedSeriesInstanceUIDs(); !reflect.DeepEqual(got, fixture.referencedSeries) {
				t.Fatalf("ReferencedSeriesInstanceUIDs() = %#v, want %#v", got, fixture.referencedSeries)
			}
			if len(resolved.Diagnostics) != 0 {
				t.Fatalf("Diagnostics = %#v, want none", resolved.Diagnostics)
			}
		})
	}
}

func TestResolveRejectsInvalidAndIncompleteReferencesWithTypedDiagnostics(t *testing.T) {
	for _, uid := range []string{"not-a-uid", "3.1", "1.40"} {
		t.Run("invalid SOP instance UID "+uid, func(t *testing.T) {
			file := gspsFixture(t)
			mutateGSPSReference(t, file, func(_ *core.DataSet, reference *core.DataSet) {
				putDataSetElement(reference, dicomtest.StringElement(core.NewTag(0x0008, 0x1155), core.VRUI, uid))
			})

			resolved, err := clinicalrelation.Resolve(file.Dataset, fixtureIdentity(t, file))
			if err != nil {
				t.Fatal(err)
			}
			assertRejectedReference(t, resolved, clinicalrelation.DiagnosticInvalidSOPInstanceUID)
		})
	}

	t.Run("missing referenced series UID", func(t *testing.T) {
		file := gspsFixture(t)
		mutateGSPSReference(t, file, func(series *core.DataSet, _ *core.DataSet) {
			removeDataSetElement(series, tags.SeriesInstanceUID)
		})

		resolved, err := clinicalrelation.Resolve(file.Dataset, fixtureIdentity(t, file))
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedReference(t, resolved, clinicalrelation.DiagnosticMissingSeriesInstanceUID)
	})

	t.Run("missing SOP instance UID", func(t *testing.T) {
		file := gspsFixture(t)
		mutateGSPSReference(t, file, func(_ *core.DataSet, reference *core.DataSet) {
			removeDataSetElement(reference, core.NewTag(0x0008, 0x1155))
		})

		resolved, err := clinicalrelation.Resolve(file.Dataset, fixtureIdentity(t, file))
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedReference(t, resolved, clinicalrelation.DiagnosticMissingSOPInstanceUID)
	})

	t.Run("missing KOS SOP instance UID", func(t *testing.T) {
		var file *object.File
		for _, fixture := range clinicalFixtures(t) {
			if fixture.kind == clinicalrelation.ObjectKindKOS {
				file = fixture.file
				break
			}
		}
		if file == nil {
			t.Fatal("missing KOS fixture")
		}
		if !removeFirstNestedElement(file.Dataset, core.NewTag(0x0008, 0x1155)) {
			t.Fatal("KOS fixture missing ReferencedSOPInstanceUID")
		}

		resolved, err := clinicalrelation.Resolve(file.Dataset, fixtureIdentity(t, file))
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedReferenceContains(t, resolved, clinicalrelation.DiagnosticMissingSOPInstanceUID)
	})

	t.Run("RT Dose reader omission remains diagnostic", func(t *testing.T) {
		file := doseFixture(
			"1.2.826.0.1.3680043.10.543.764.1",
			"1.2.826.0.1.3680043.10.543.764.2",
			"1.2.826.0.1.3680043.10.543.764.62",
		)
		removeNestedReferenceUID(t, file.Dataset, core.NewTag(0x300C, 0x0002))

		resolved, err := clinicalrelation.Resolve(file.Dataset, fixtureIdentity(t, file))
		if err != nil {
			t.Fatal(err)
		}
		assertRejectedReference(t, resolved, clinicalrelation.DiagnosticMissingSOPInstanceUID)
	})
}

func TestResolveReturnsTypedStructuralError(t *testing.T) {
	dataset := object.FromElements([]core.Element{
		dicomtest.StringElement(tags.SOPClassUID, core.VRUI, seg.SegmentationStorage),
		dicomtest.StringElement(tags.SOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.764.991"),
	}, std.Dictionary)

	_, err := clinicalrelation.Resolve(dataset, clinicalrelation.Identity{})
	if !errors.Is(err, clinicalrelation.ErrResolution) {
		t.Fatalf("Resolve() error = %v, want ErrResolution", err)
	}
	var resolutionErr *clinicalrelation.ResolutionError
	if !errors.As(err, &resolutionErr) || resolutionErr.Code != clinicalrelation.DiagnosticMalformedObject || resolutionErr.Kind != clinicalrelation.ObjectKindSEG {
		t.Fatalf("Resolve() error = %#v, want typed malformed SEG error", err)
	}
}

func TestIsEditableSOPClass(t *testing.T) {
	for _, sopClassUID := range []string{
		seg.SegmentationStorage,
		seg.LabelMapSegmentationStorage,
		gsps.GrayscaleSoftcopyPresentationStateStorage,
		sr.KeyObjectSelectionDocumentStorage,
		rtstruct.RTStructureSetStorage,
		sr.EnhancedSRStorage,
		sr.ComprehensiveSRStorage,
		sr.Comprehensive3DSRStorage,
		vps.GrayscalePlanarMPRVolumetricPresentationStateStorage,
		vps.CompositingPlanarMPRVolumetricPresentationStateStorage,
		vps.VolumeRenderingVolumetricPresentationStateStorage,
		vps.SegmentedVolumeRenderingVolumetricPresentationStateStorage,
		vps.MultipleVolumeRenderingVolumetricPresentationStateStorage,
	} {
		if !clinicalrelation.IsEditableSOPClass(sopClassUID) {
			t.Errorf("IsEditableSOPClass(%q) = false, want true", sopClassUID)
		}
	}
	if clinicalrelation.IsEditableSOPClass("1.2.840.10008.5.1.4.1.1.2") {
		t.Fatal("IsEditableSOPClass(CT) = true, want false")
	}
}

type clinicalFixture struct {
	name             string
	file             *object.File
	kind             clinicalrelation.ObjectKind
	editable         bool
	studyUID         string
	frameUID         string
	referencedSOPs   []string
	referencedSeries []string
}

func clinicalFixtures(t *testing.T) []clinicalFixture {
	t.Helper()
	const (
		studyUID     = "1.2.826.0.1.3680043.10.543.764.1"
		frameUID     = "1.2.826.0.1.3680043.10.543.764.2"
		ctClass      = "1.2.840.10008.5.1.4.1.1.2"
		sourceSOP    = "1.2.826.0.1.3680043.10.543.764.3"
		sourceSeries = "1.2.826.0.1.3680043.10.543.764.10"
	)

	segFile, err := seg.Write(&seg.Document{
		SOPClassUID:         seg.LabelMapSegmentationStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.10.543.764.21",
		StudyInstanceUID:    studyUID,
		SeriesInstanceUID:   "1.2.826.0.1.3680043.10.543.764.20",
		FrameOfReferenceUID: frameUID,
		Rows:                1,
		Columns:             1,
		Segments:            []seg.Segment{{Number: 1, Label: "Target", AlgorithmType: seg.AlgorithmManual}},
		ReferencedImages: []seg.ReferencedImage{{
			SeriesInstanceUID: sourceSeries,
			SOPClassUID:       ctClass,
			SOPInstanceUID:    sourceSOP,
		}},
		Frames: []seg.Frame{{SegmentNumber: 1, ReferencedSOPInstanceUID: sourceSOP, LabelMap: []uint16{1}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	gspsFile := gspsFixture(t)

	kosDocument := sr.NewKeyObjectSelection("1.2.826.0.1.3680043.10.543.764.41", []sr.ImageReference{{
		StudyInstanceUID:  studyUID,
		SeriesInstanceUID: sourceSeries,
		SOPClassUID:       ctClass,
		SOPInstanceUID:    sourceSOP,
	}}, nil)
	kosDocument.StudyInstanceUID = studyUID
	kosDocument.SeriesInstanceUID = "1.2.826.0.1.3680043.10.543.764.40"
	kosDataset, err := kosDocument.Dataset()
	if err != nil {
		t.Fatal(err)
	}
	kosFile := &object.File{Dataset: kosDataset, TransferSyntax: transfer.ExplicitVRLittleEndian}

	rtstructFile, err := rtstruct.Write(&rtstruct.StructureSet{
		SOPInstanceUID:      "1.2.826.0.1.3680043.10.543.764.51",
		StudyInstanceUID:    studyUID,
		SeriesInstanceUID:   "1.2.826.0.1.3680043.10.543.764.50",
		FrameOfReferenceUID: frameUID,
		ROIs: []rtstruct.ROI{{Number: 1, Name: "Target", Contours: []rtstruct.Contour{{
			GeometricType: rtstruct.ContourClosedPlanar,
			Points:        []rtstruct.Point3D{{X: 0, Y: 0}, {X: 1, Y: 0}, {X: 1, Y: 1}, {X: 0, Y: 1}},
			ReferencedImages: []rtstruct.ReferencedImage{{
				StudyInstanceUID: studyUID, SeriesInstanceUID: sourceSeries, SOPClassUID: ctClass, SOPInstanceUID: sourceSOP,
			}},
		}}}},
	})
	if err != nil {
		t.Fatal(err)
	}

	rtdoseRef := "1.2.826.0.1.3680043.10.543.764.62"
	rtdoseFile := doseFixture(studyUID, frameUID, rtdoseRef)

	segmentSOP := "1.2.826.0.1.3680043.10.543.764.72"
	srFile, err := sr.WriteMeasurementReport(&sr.MeasurementReport{
		SOPClassUID:         sr.Comprehensive3DSRStorage,
		SOPInstanceUID:      "1.2.826.0.1.3680043.10.543.764.71",
		StudyInstanceUID:    studyUID,
		SeriesInstanceUID:   "1.2.826.0.1.3680043.10.543.764.70",
		FrameOfReferenceUID: frameUID,
		Groups: []sr.MeasurementGroup{{
			Tracking:          sr.TrackingIdentifier{UID: "1.2.826.0.1.3680043.10.543.764.73", Identifier: "Target"},
			ReferencedSegment: sr.SegmentReference{SOPClassUID: seg.SegmentationStorage, SOPInstanceUID: segmentSOP, SegmentNumber: 1},
			Measurements: []sr.ReportMeasurement{{
				ConceptName: sr.CodedEntry{CodeValue: "121206", CodingSchemeDesignator: "DCM", CodeMeaning: "Distance"},
				Value:       1,
				Units:       sr.CodedEntry{CodeValue: "mm", CodingSchemeDesignator: "UCUM", CodeMeaning: "millimeter"},
				Image:       sr.ImageReference{SOPClassUID: ctClass, SOPInstanceUID: sourceSOP},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	vpsFile, err := vps.Write(&vps.State{
		SOPClassUID:       vps.VolumeRenderingVolumetricPresentationStateStorage,
		SOPInstanceUID:    "1.2.826.0.1.3680043.10.543.764.81",
		StudyInstanceUID:  studyUID,
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.543.764.80",
		Inputs: []vps.Input{{
			Number: 1, InputSetUID: "1.2.826.0.1.3680043.10.543.764.82",
			ReferencedInstances: []vps.ReferencedInstance{{SOPClassUID: ctClass, SOPInstanceUID: sourceSOP}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	return []clinicalFixture{
		{name: "seg", file: segFile, kind: clinicalrelation.ObjectKindSEG, editable: true, studyUID: studyUID, frameUID: frameUID, referencedSOPs: []string{sourceSOP}, referencedSeries: []string{sourceSeries}},
		{name: "gsps", file: gspsFile, kind: clinicalrelation.ObjectKindGSPS, editable: true, studyUID: studyUID, referencedSOPs: []string{sourceSOP}, referencedSeries: []string{sourceSeries}},
		{name: "kos", file: kosFile, kind: clinicalrelation.ObjectKindKOS, editable: true, studyUID: studyUID, referencedSOPs: []string{sourceSOP}, referencedSeries: []string{sourceSeries}},
		{name: "rtstruct", file: rtstructFile, kind: clinicalrelation.ObjectKindRTSTRUCT, editable: true, studyUID: studyUID, frameUID: frameUID, referencedSOPs: []string{sourceSOP}, referencedSeries: []string{sourceSeries}},
		{name: "rtdose", file: rtdoseFile, kind: clinicalrelation.ObjectKindRTDose, studyUID: studyUID, frameUID: frameUID, referencedSOPs: []string{rtdoseRef}},
		{name: "sr", file: srFile, kind: clinicalrelation.ObjectKindSRMeasurement, editable: true, studyUID: studyUID, frameUID: frameUID, referencedSOPs: []string{segmentSOP, sourceSOP}},
		{name: "vps", file: vpsFile, kind: clinicalrelation.ObjectKindVPS, editable: true, studyUID: studyUID, referencedSOPs: []string{sourceSOP}},
	}
}

func gspsFixture(t *testing.T) *object.File {
	t.Helper()
	file, err := gsps.Write(&gsps.State{
		SOPInstanceUID:    "1.2.826.0.1.3680043.10.543.764.31",
		StudyInstanceUID:  "1.2.826.0.1.3680043.10.543.764.1",
		SeriesInstanceUID: "1.2.826.0.1.3680043.10.543.764.30",
		ReferencedImages: []gsps.ReferencedImage{{
			SeriesInstanceUID: "1.2.826.0.1.3680043.10.543.764.10",
			SOPClassUID:       "1.2.840.10008.5.1.4.1.1.2",
			SOPInstanceUID:    "1.2.826.0.1.3680043.10.543.764.3",
		}},
		DisplayedArea:        gsps.DisplayedArea{TopLeftX: 1, TopLeftY: 1, BottomRightX: 1, BottomRightY: 1},
		PresentationLUTShape: gsps.PresentationLUTIdentity,
	})
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func doseFixture(studyUID, frameUID, referencedSOPUID string) *object.File {
	return &object.File{
		Dataset: object.FromElements([]core.Element{
			dicomtest.StringElement(tags.SOPClassUID, core.VRUI, rtdose.RTDoseStorage),
			dicomtest.StringElement(tags.SOPInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.764.61"),
			dicomtest.StringElement(tags.StudyInstanceUID, core.VRUI, studyUID),
			dicomtest.StringElement(tags.SeriesInstanceUID, core.VRUI, "1.2.826.0.1.3680043.10.543.764.60"),
			dicomtest.StringElement(tags.FrameOfReferenceUID, core.VRUI, frameUID),
			dicomtest.StringElement(core.NewTag(0x3004, 0x0002), core.VRCS, "GY"),
			dicomtest.StringElement(core.NewTag(0x3004, 0x0004), core.VRCS, "PHYSICAL"),
			dicomtest.StringElement(core.NewTag(0x3004, 0x000A), core.VRCS, "PLAN"),
			dicomtest.StringElement(core.NewTag(0x3004, 0x000E), core.VRDS, "0.01"),
			stringElement(core.NewTag(0x0020, 0x0032), core.VRDS, "0", "0", "0"),
			stringElement(core.NewTag(0x0020, 0x0037), core.VRDS, "1", "0", "0", "0", "1", "0"),
			stringElement(core.NewTag(0x0028, 0x0030), core.VRDS, "1", "1"),
			dicomtest.StringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1"),
			uint16Element(core.NewTag(0x0028, 0x0010), 1),
			uint16Element(core.NewTag(0x0028, 0x0011), 1),
			uint16Element(core.NewTag(0x0028, 0x0002), 1),
			dicomtest.StringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "MONOCHROME2"),
			uint16Element(core.NewTag(0x0028, 0x0100), 16),
			uint16Element(core.NewTag(0x0028, 0x0101), 16),
			uint16Element(core.NewTag(0x0028, 0x0102), 15),
			uint16Element(core.NewTag(0x0028, 0x0103), 0),
			sequenceElement(core.NewTag(0x300C, 0x0002), core.DataSet{Elements: []core.Element{
				dicomtest.StringElement(core.NewTag(0x0008, 0x1150), core.VRUI, "1.2.840.10008.5.1.4.1.1.481.5"),
				dicomtest.StringElement(core.NewTag(0x0008, 0x1155), core.VRUI, referencedSOPUID),
			}}),
			core.NewRawElement(core.NewTag(0x7FE0, 0x0010), core.VROW, []byte{100, 0}),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
}

func fixtureIdentity(t *testing.T, file *object.File) clinicalrelation.Identity {
	t.Helper()
	return clinicalrelation.Identity{
		SOPClassUID:       requiredUID(t, file.Dataset, tags.SOPClassUID),
		SOPInstanceUID:    requiredUID(t, file.Dataset, tags.SOPInstanceUID),
		StudyInstanceUID:  requiredUID(t, file.Dataset, tags.StudyInstanceUID),
		SeriesInstanceUID: requiredUID(t, file.Dataset, tags.SeriesInstanceUID),
	}
}

func requiredUID(t *testing.T, dataset *object.Object, tag core.Tag) string {
	t.Helper()
	value, ok := dataset.GetUID(tag)
	if !ok {
		t.Fatalf("fixture missing %s", tag)
	}
	return value
}

func mutateGSPSReference(t *testing.T, file *object.File, mutate func(series, reference *core.DataSet)) {
	t.Helper()
	seriesElement, ok := file.Dataset.Get(core.NewTag(0x0008, 0x1115))
	if !ok {
		t.Fatal("missing ReferencedSeriesSequence")
	}
	seriesSequence, ok := seriesElement.Value.(core.SequenceValue)
	if !ok || len(seriesSequence.Items) != 1 {
		t.Fatalf("ReferencedSeriesSequence = %#v, want one item", seriesElement.Value)
	}
	series := &seriesSequence.Items[0]
	referenceElementIndex := dataSetElementIndex(series, core.NewTag(0x0008, 0x1140))
	if referenceElementIndex < 0 {
		t.Fatal("missing ReferencedImageSequence")
	}
	referenceSequence, ok := series.Elements[referenceElementIndex].Value.(core.SequenceValue)
	if !ok || len(referenceSequence.Items) != 1 {
		t.Fatalf("ReferencedImageSequence = %#v, want one item", series.Elements[referenceElementIndex].Value)
	}
	mutate(series, &referenceSequence.Items[0])
	referenceElementIndex = dataSetElementIndex(series, core.NewTag(0x0008, 0x1140))
	if referenceElementIndex < 0 {
		t.Fatal("mutation removed ReferencedImageSequence")
	}
	series.Elements[referenceElementIndex].Value = referenceSequence
	seriesElement.Value = seriesSequence
	file.Dataset.Put(seriesElement)
}

func putDataSetElement(dataset *core.DataSet, element core.Element) {
	if index := dataSetElementIndex(dataset, element.Tag()); index >= 0 {
		dataset.Elements[index] = element
		return
	}
	dataset.Elements = append(dataset.Elements, element)
}

func removeDataSetElement(dataset *core.DataSet, tag core.Tag) {
	if index := dataSetElementIndex(dataset, tag); index >= 0 {
		dataset.Elements = append(dataset.Elements[:index], dataset.Elements[index+1:]...)
	}
}

func dataSetElementIndex(dataset *core.DataSet, tag core.Tag) int {
	for index := range dataset.Elements {
		if dataset.Elements[index].Tag() == tag {
			return index
		}
	}
	return -1
}

func removeNestedReferenceUID(t *testing.T, dataset *object.Object, sequenceTag core.Tag) {
	t.Helper()
	sequenceElement, ok := dataset.Get(sequenceTag)
	if !ok {
		t.Fatalf("fixture missing %s", sequenceTag)
	}
	sequence, ok := sequenceElement.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != 1 {
		t.Fatalf("%s = %#v, want one sequence item", sequenceTag, sequenceElement.Value)
	}
	removeDataSetElement(&sequence.Items[0], core.NewTag(0x0008, 0x1155))
	sequenceElement.Value = sequence
	dataset.Put(sequenceElement)
}

func removeFirstNestedElement(dataset *object.Object, tag core.Tag) bool {
	for _, element := range dataset.Elements() {
		sequence, ok := element.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for index := range sequence.Items {
			if removeFirstDataSetElement(&sequence.Items[index], tag) {
				element.Value = sequence
				dataset.Put(element)
				return true
			}
		}
	}
	return false
}

func removeFirstDataSetElement(dataset *core.DataSet, tag core.Tag) bool {
	if index := dataSetElementIndex(dataset, tag); index >= 0 {
		removeDataSetElement(dataset, tag)
		return true
	}
	for index := range dataset.Elements {
		sequence, ok := dataset.Elements[index].Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for itemIndex := range sequence.Items {
			if removeFirstDataSetElement(&sequence.Items[itemIndex], tag) {
				dataset.Elements[index].Value = sequence
				return true
			}
		}
	}
	return false
}

func assertRejectedReference(t *testing.T, resolved clinicalrelation.Resolution, code clinicalrelation.DiagnosticCode) {
	t.Helper()
	if len(resolved.References) != 0 || len(resolved.ReferencedSOPInstanceUIDs()) != 0 {
		t.Fatalf("invalid reference produced links: %#v", resolved.References)
	}
	if len(resolved.Diagnostics) != 1 || resolved.Diagnostics[0].Code != code || resolved.Diagnostics[0].ReferenceIndex != 0 {
		t.Fatalf("Diagnostics = %#v, want one %s diagnostic", resolved.Diagnostics, code)
	}
}

func assertRejectedReferenceContains(t *testing.T, resolved clinicalrelation.Resolution, code clinicalrelation.DiagnosticCode) {
	t.Helper()
	if len(resolved.References) != 0 || len(resolved.ReferencedSOPInstanceUIDs()) != 0 {
		t.Fatalf("invalid reference produced links: %#v", resolved.References)
	}
	for _, diagnostic := range resolved.Diagnostics {
		if diagnostic.Code == code && diagnostic.ReferenceIndex == 0 {
			return
		}
	}
	t.Fatalf("Diagnostics = %#v, want %s diagnostic", resolved.Diagnostics, code)
}

func stringElement(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}

func uint16Element(tag core.Tag, value uint16) core.Element {
	data := make([]byte, 2)
	binary.LittleEndian.PutUint16(data, value)
	return core.NewRawElement(tag, core.VRUS, data)
}

func sequenceElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ}, Value: core.SequenceValue{Items: items}}
}
