package deid

import (
	"bytes"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestRemapHierarchyUIDsChangesOnlyHierarchyUIDs(t *testing.T) {
	obj := uidOnlyObject("1.2.3.study", "1.2.3.series", "1.2.3.sop")
	uids := NewUIDRemapper()

	if err := RemapHierarchyUIDs(obj, uids); err != nil {
		t.Fatalf("RemapHierarchyUIDs() error = %v", err)
	}

	study := trimmedUID(obj, testTagStudyInstanceUID)
	series := trimmedUID(obj, testTagSeriesInstanceUID)
	sop := trimmedUID(obj, testTagSOPInstanceUID)
	if study == "1.2.3.study" || series == "1.2.3.series" || sop == "1.2.3.sop" {
		t.Fatalf("UIDs were not remapped: study=%q series=%q sop=%q", study, series, sop)
	}
	for _, uid := range []string{study, series, sop} {
		if !strings.HasPrefix(uid, "2.25.") {
			t.Fatalf("remapped UID = %q, want 2.25.*", uid)
		}
	}
}

func TestCloneWithRemappedHierarchyUIDsDoesNotMutateSourceAndUsesSharedMap(t *testing.T) {
	srcA := uidOnlyFile("1.2.3.study", "1.2.3.series", "1.2.3.sop.1")
	srcB := uidOnlyFile("1.2.3.study", "1.2.3.series", "1.2.3.sop.2")
	uids := NewUIDRemapper()

	cloneA, err := CloneWithRemappedHierarchyUIDs(srcA, uids)
	if err != nil {
		t.Fatalf("CloneWithRemappedHierarchyUIDs A: %v", err)
	}
	cloneB, err := CloneWithRemappedHierarchyUIDs(srcB, uids)
	if err != nil {
		t.Fatalf("CloneWithRemappedHierarchyUIDs B: %v", err)
	}

	if got := trimmedUID(srcA.Dataset, testTagStudyInstanceUID); got != "1.2.3.study" {
		t.Fatalf("source StudyInstanceUID mutated to %q", got)
	}
	if trimmedUID(cloneA.Dataset, testTagStudyInstanceUID) != trimmedUID(cloneB.Dataset, testTagStudyInstanceUID) {
		t.Fatalf("shared study remap mismatch: %q/%q", trimmedUID(cloneA.Dataset, testTagStudyInstanceUID), trimmedUID(cloneB.Dataset, testTagStudyInstanceUID))
	}
	if trimmedUID(cloneA.Dataset, testTagSOPInstanceUID) == trimmedUID(cloneB.Dataset, testTagSOPInstanceUID) {
		t.Fatal("distinct SOPInstanceUIDs should remain distinct after remap")
	}
	for name, clone := range map[string]*object.File{"A": cloneA, "B": cloneB} {
		datasetUID, datasetOK := clone.Dataset.GetUID(testTagSOPInstanceUID)
		metaUID, metaOK := clone.Meta.GetUID(testTagMediaStorageSOPInstanceUIDForClone)
		if !datasetOK || !metaOK || metaUID != datasetUID {
			t.Errorf("clone %s SOP Instance UID dataset/meta = %q/%q, want equal", name, datasetUID, metaUID)
		}
	}
}

func TestRemapHierarchyUIDsMintsDistinctMissingUIDs(t *testing.T) {
	objA := uidOnlyObject("", "", "")
	objB := uidOnlyObject("", "", "")
	uids := NewUIDRemapper()

	if err := RemapHierarchyUIDs(objA, uids); err != nil {
		t.Fatalf("RemapHierarchyUIDs A: %v", err)
	}
	if err := RemapHierarchyUIDs(objB, uids); err != nil {
		t.Fatalf("RemapHierarchyUIDs B: %v", err)
	}

	for _, check := range []struct {
		name string
		tag  core.Tag
	}{
		{"study", testTagStudyInstanceUID},
		{"series", testTagSeriesInstanceUID},
		{"sop", testTagSOPInstanceUID},
	} {
		if got := trimmedUID(objA, check.tag); got == "" {
			t.Fatalf("object A %s UID is empty", check.name)
		}
		if got := trimmedUID(objB, check.tag); got == "" {
			t.Fatalf("object B %s UID is empty", check.name)
		}
		if trimmedUID(objA, check.tag) == trimmedUID(objB, check.tag) {
			t.Fatalf("missing %s UID remapped to same value %q across objects", check.name, trimmedUID(objA, check.tag))
		}
	}
}

func TestCloneAnonymizedFileDoesNotMutateSource(t *testing.T) {
	sourcePreamble := bytes.Repeat([]byte{0xa5}, 128)
	src := &object.File{
		Preamble:       sourcePreamble,
		Dataset:        deidTestObject(),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	src.Dataset.Put(core.NewRawElement(testTagSOPClassUIDForClone, core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2")))
	src.Dataset.Put(core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("NO ")))
	src.Meta = object.FromElements([]core.Element{
		core.NewRawElement(testTagMediaStorageSOPClassUIDForClone, core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2")),
		core.NewRawElement(testTagMediaStorageSOPInstanceUIDForClone, core.VRUI, []byte("1.2.3.4.5")),
		core.NewRawElement(testTagTransferSyntaxUIDForClone, core.VRUI, []byte(transfer.ExplicitVRLittleEndian.UID)),
		core.NewRawElement(testTagSourceApplicationEntityTitleForClone, core.VRAE, []byte("SOURCE_AE ")),
	}, nil)

	clone, report, err := CloneAnonymizedFile(src, Options{PatientName: "Anonymous", PatientID: "ANON"}, nil)
	if err != nil {
		t.Fatalf("CloneAnonymizedFile() error = %v", err)
	}
	if report.BurnedInPixel.Risk != BurnedInPixelRiskAbsent || report.BurnedInPixel.MetadataValue != "NO" {
		t.Fatalf("report not populated: %#v", report)
	}
	if got, _ := src.Dataset.GetString(testTagPatientName); got != "DOE^JANE" {
		t.Fatalf("source PatientName mutated to %q", got)
	}
	if got, _ := clone.Dataset.GetString(testTagPatientName); got != "Anonymous" {
		t.Fatalf("clone PatientName = %q, want Anonymous", got)
	}
	datasetUID, datasetOK := clone.Dataset.GetUID(testTagSOPInstanceUID)
	metaUID, metaOK := clone.Meta.GetUID(testTagMediaStorageSOPInstanceUIDForClone)
	if !datasetOK || !metaOK || metaUID != datasetUID || metaUID == "1.2.3.4.5" {
		t.Fatalf("clone SOP Instance UID dataset/meta = %q/%q, want equal remapped values", datasetUID, metaUID)
	}
	if clone.Meta.Has(testTagSourceApplicationEntityTitleForClone) {
		t.Fatal("clone retained SourceApplicationEntityTitle")
	}
	if len(clone.Preamble) != 0 {
		t.Fatalf("clone retained source preamble canary: % X", clone.Preamble)
	}
	if sourceMetaUID, _ := src.Meta.GetUID(testTagMediaStorageSOPInstanceUIDForClone); sourceMetaUID != "1.2.3.4.5" {
		t.Fatalf("source MediaStorageSOPInstanceUID mutated to %q", sourceMetaUID)
	}
	if sourceAE, _ := src.Meta.GetString(testTagSourceApplicationEntityTitleForClone); strings.TrimSpace(sourceAE) != "SOURCE_AE" {
		t.Fatalf("source SourceApplicationEntityTitle mutated to %q", sourceAE)
	}
	if !bytes.Equal(src.Preamble, sourcePreamble) {
		t.Fatalf("source preamble mutated: % X", src.Preamble)
	}
}

func TestRederiveFileMetaUpdatesExistingCloneInPlace(t *testing.T) {
	file := uidOnlyFile("1.2.3.study", "1.2.3.series", "1.2.3.sop")
	dataset := file.Dataset

	got, err := rederiveFileMeta(file)
	if err != nil {
		t.Fatalf("rederiveFileMeta() error = %v", err)
	}
	if got != file || got.Dataset != dataset {
		t.Fatal("rederiveFileMeta() replaced the file or its already-cloned dataset")
	}
	datasetUID, datasetOK := got.Dataset.GetUID(testTagSOPInstanceUID)
	metaUID, metaOK := got.Meta.GetUID(testTagMediaStorageSOPInstanceUIDForClone)
	if !datasetOK || !metaOK || metaUID != datasetUID {
		t.Fatalf("SOP Instance UID dataset/meta = %q/%q, want equal", datasetUID, metaUID)
	}
}

func uidOnlyFile(study, series, sop string) *object.File {
	dataset := uidOnlyObject(study, series, sop)
	dataset.Put(core.NewRawElement(testTagSOPClassUIDForClone, core.VRUI, []byte("1.2.840.10008.5.1.4.1.1.2")))
	return &object.File{Dataset: dataset, TransferSyntax: transfer.ExplicitVRLittleEndian}
}

var (
	testTagSOPClassUIDForClone                  = core.NewTag(0x0008, 0x0016)
	testTagMediaStorageSOPClassUIDForClone      = core.NewTag(0x0002, 0x0002)
	testTagMediaStorageSOPInstanceUIDForClone   = core.NewTag(0x0002, 0x0003)
	testTagTransferSyntaxUIDForClone            = core.NewTag(0x0002, 0x0010)
	testTagSourceApplicationEntityTitleForClone = core.NewTag(0x0002, 0x0016)
)
