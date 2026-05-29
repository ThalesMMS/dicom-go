package deid

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	testTagPatientName       = core.NewTag(0x0010, 0x0010)
	testTagPatientID         = core.NewTag(0x0010, 0x0020)
	testTagPatientBirthDate  = core.NewTag(0x0010, 0x0030)
	testTagInstitutionName   = core.NewTag(0x0008, 0x0080)
	testTagAccessionNumber   = core.NewTag(0x0008, 0x0050)
	testTagStudyDate         = core.NewTag(0x0008, 0x0020)
	testTagStudyInstanceUID  = core.NewTag(0x0020, 0x000D)
	testTagSeriesInstanceUID = core.NewTag(0x0020, 0x000E)
	testTagSOPInstanceUID    = core.NewTag(0x0008, 0x0018)
	testTagReferencedSOPUID  = core.NewTag(0x0008, 0x1155)
	testTagReferencedImageSQ = core.NewTag(0x0008, 0x1140)
	testTagNestedSequence    = core.NewTag(0x0008, 0x1115)
	testTagPrivateCreator    = core.NewTag(0x0011, 0x0010)
	testTagPrivateCreatorAlt = core.NewTag(0x0011, 0x0020)
	testTagPrivateData       = core.NewTag(0x0011, 0x1010)
	testTagPrivateDataAlt    = core.NewTag(0x0011, 0x2010)
	testTagPrivateSequence   = core.NewTag(0x0011, 0x1020)
	testTagBurnedIn          = core.NewTag(0x0028, 0x0301)
	testTagRows              = core.NewTag(0x0028, 0x0010)
	testTagColumns           = core.NewTag(0x0028, 0x0011)
	testTagSamplesPerPixel   = core.NewTag(0x0028, 0x0002)
	testTagPlanarConfig      = core.NewTag(0x0028, 0x0006)
	testTagPhotometric       = core.NewTag(0x0028, 0x0004)
	testTagBitsAllocated     = core.NewTag(0x0028, 0x0100)
	testTagBitsStored        = core.NewTag(0x0028, 0x0101)
	testTagHighBit           = core.NewTag(0x0028, 0x0102)
	testTagPixelRep          = core.NewTag(0x0028, 0x0103)
	testTagPixelData         = core.NewTag(0x7FE0, 0x0010)
)

func TestAnonymizeObjectReplacesPatientFieldsBlanksPhiAndRemapsUids(t *testing.T) {
	// Given
	obj := deidTestObject()
	uids := NewUIDRemapper()

	// When
	err := AnonymizeObject(obj, Options{}, uids)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}
	if name, _ := obj.GetString(testTagPatientName); !strings.HasPrefix(name, "Anonymous") {
		t.Fatalf("PatientName = %q, want Anonymous*", name)
	}
	if id, _ := obj.GetString(testTagPatientID); strings.TrimSpace(id) != "ANON" {
		t.Fatalf("PatientID = %q, want ANON", id)
	}
	if birthDate, _ := obj.GetString(testTagPatientBirthDate); strings.TrimSpace(birthDate) != "" {
		t.Fatalf("PatientBirthDate = %q, want blank", birthDate)
	}
	if institution, _ := obj.GetString(testTagInstitutionName); strings.TrimSpace(institution) != "" {
		t.Fatalf("InstitutionName = %q, want blank", institution)
	}
	if accession, _ := obj.GetString(testTagAccessionNumber); strings.TrimSpace(accession) != "" {
		t.Fatalf("AccessionNumber = %q, want blank", accession)
	}
	studyUID := trimmedUID(obj, testTagStudyInstanceUID)
	if studyUID == "1.2.3" || !strings.HasPrefix(studyUID, "2.25.") {
		t.Fatalf("StudyInstanceUID = %q, want new 2.25.* UID", studyUID)
	}
	if uids.Map("1.2.3") != studyUID {
		t.Fatal("UIDRemapper did not preserve the study UID mapping")
	}
}

func TestAnonymizeObjectBlanksReportedBasicProfileIdentifiers(t *testing.T) {
	attributes := []struct {
		name string
		tag  core.Tag
		vr   core.VR
	}{
		{"PatientBirthTime", core.NewTag(0x0010, 0x0032), core.VRTM},
		{"IssuerOfPatientID", core.NewTag(0x0010, 0x0021), core.VRLO},
		{"OtherPatientNames", core.NewTag(0x0010, 0x1001), core.VRPN},
		{"PatientBirthName", core.NewTag(0x0010, 0x1005), core.VRPN},
		{"MedicalRecordLocator", core.NewTag(0x0010, 0x1090), core.VRLO},
		{"MothersBirthName", core.NewTag(0x0010, 0x1060), core.VRPN},
		{"PatientTelephoneNumbers", core.NewTag(0x0010, 0x2154), core.VRSH},
		{"PatientComments", core.NewTag(0x0010, 0x4000), core.VRLT},
		{"InstitutionAddress", core.NewTag(0x0008, 0x0081), core.VRST},
		{"StationName", core.NewTag(0x0008, 0x1010), core.VRSH},
		{"StudyDescription", core.NewTag(0x0008, 0x1030), core.VRLO},
		{"SeriesDescription", core.NewTag(0x0008, 0x103E), core.VRLO},
		{"ReferringPhysicianAddress", core.NewTag(0x0008, 0x0092), core.VRST},
		{"ReferringPhysicianTelephoneNumbers", core.NewTag(0x0008, 0x0094), core.VRSH},
		{"RequestingPhysician", core.NewTag(0x0032, 0x1032), core.VRPN},
		{"DeviceSerialNumber", core.NewTag(0x0018, 0x1000), core.VRLO},
		{"ProtocolName", core.NewTag(0x0018, 0x1030), core.VRLO},
		{"StudyID", core.NewTag(0x0020, 0x0010), core.VRSH},
		{"ImageComments", core.NewTag(0x0020, 0x4000), core.VRLT},
	}
	obj := deidTestObject()
	for _, attribute := range attributes {
		obj.Put(core.NewRawElement(attribute.tag, attribute.vr, []byte("IDENTIFYING VALUE")))
	}

	if err := AnonymizeObject(obj, Options{}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	for _, attribute := range attributes {
		if value, _ := obj.GetString(attribute.tag); strings.TrimSpace(value) != "" {
			t.Errorf("%s = %q, want blank", attribute.name, value)
		}
	}
}

func TestAnonymizeObjectBlanksAllCommonDatesAndTimesByDefault(t *testing.T) {
	attributes := []struct {
		name  string
		tag   core.Tag
		vr    core.VR
		value string
	}{
		{"InstanceCreationDate", core.NewTag(0x0008, 0x0012), core.VRDA, "20240101"},
		{"InstanceCreationTime", core.NewTag(0x0008, 0x0013), core.VRTM, "123456"},
		{"StudyDate", core.NewTag(0x0008, 0x0020), core.VRDA, "20240101"},
		{"SeriesDate", core.NewTag(0x0008, 0x0021), core.VRDA, "20240102"},
		{"AcquisitionDate", core.NewTag(0x0008, 0x0022), core.VRDA, "20240103"},
		{"ContentDate", core.NewTag(0x0008, 0x0023), core.VRDA, "20240104"},
		{"AcquisitionDateTime", core.NewTag(0x0008, 0x002A), core.VRDT, "20240103123456"},
		{"StudyTime", core.NewTag(0x0008, 0x0030), core.VRTM, "123001"},
		{"SeriesTime", core.NewTag(0x0008, 0x0031), core.VRTM, "123002"},
		{"AcquisitionTime", core.NewTag(0x0008, 0x0032), core.VRTM, "123003"},
		{"ContentTime", core.NewTag(0x0008, 0x0033), core.VRTM, "123004"},
	}
	obj := deidTestObject()
	for _, attribute := range attributes {
		obj.Put(core.NewRawElement(attribute.tag, attribute.vr, []byte(attribute.value)))
	}

	if err := AnonymizeObject(obj, Options{}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	for _, attribute := range attributes {
		if value, _ := obj.GetString(attribute.tag); strings.TrimSpace(value) != "" {
			t.Errorf("%s = %q, want blank", attribute.name, value)
		}
	}
}

func TestAnonymizeObjectRemovesOverlayGroupsAtEverySequenceDepth(t *testing.T) {
	overlayData := core.NewTag(0x6000, 0x3000)
	overlayDescription := core.NewTag(0x6002, 0x0022)
	nestedPatientComments := core.NewTag(0x0010, 0x4000)
	otherPatientIDsSequence := core.NewTag(0x0010, 0x1002)
	obj := deidTestObject()
	obj.Put(core.NewRawElement(overlayData, core.VROW, []byte{1, 2}))
	obj.Put(sequenceElement(otherPatientIDsSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("OTHER-ID")),
	}}))
	obj.Put(sequenceElement(testTagNestedSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(overlayDescription, core.VRLO, []byte("PATIENT OVERLAY")),
		core.NewRawElement(nestedPatientComments, core.VRLT, []byte("NESTED PHI")),
	}}))

	if err := AnonymizeObject(obj, Options{}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	if obj.Has(overlayData) {
		t.Fatal("top-level OverlayData was retained")
	}
	if obj.Has(otherPatientIDsSequence) {
		t.Fatal("OtherPatientIDsSequence was retained")
	}
	items, ok := obj.GetSequence(testTagNestedSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("nested sequence items = %d, %v, want 1, true", len(items), ok)
	}
	if items[0].Has(overlayDescription) {
		t.Fatal("nested OverlayDescription was retained")
	}
	if value, _ := items[0].GetString(nestedPatientComments); strings.TrimSpace(value) != "" {
		t.Fatalf("nested PatientComments = %q, want blank", value)
	}
}

func TestUIDRemapperIsSafeForConcurrentMapping(t *testing.T) {
	uids := NewUIDRemapper()
	errs := make(chan string, 20)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if got := uids.Map("1.2.3"); got == "" {
					errs <- "Map returned empty UID"
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
}

func TestMintUIDFallbackRemainsUniqueWhenEntropyFails(t *testing.T) {
	oldRandRead := randRead
	randRead = func([]byte) (int, error) { return 0, errors.New("no entropy") }
	defer func() { randRead = oldRandRead }()

	first := mintUID()
	second := mintUID()
	if first == second {
		t.Fatalf("mintUID fallback returned duplicate %q", first)
	}
	if !strings.HasPrefix(first, "2.25.") || !strings.HasPrefix(second, "2.25.") {
		t.Fatalf("fallback UIDs = %q, %q; want 2.25.*", first, second)
	}
}

func TestAnonymizeObjectKeepsDatesWhenRequested(t *testing.T) {
	// Given
	obj := deidTestObject()
	attributes := []struct {
		tag   core.Tag
		vr    core.VR
		value string
	}{
		{testTagStudyDate, core.VRDA, "20240101"},
		{core.NewTag(0x0008, 0x0021), core.VRDA, "20240102"},
		{core.NewTag(0x0008, 0x0022), core.VRDA, "20240103"},
		{core.NewTag(0x0008, 0x0023), core.VRDA, "20240104"},
		{core.NewTag(0x0008, 0x0030), core.VRTM, "123001"},
		{core.NewTag(0x0008, 0x0031), core.VRTM, "123002"},
		{core.NewTag(0x0008, 0x0032), core.VRTM, "123003"},
		{core.NewTag(0x0008, 0x0033), core.VRTM, "123004"},
	}
	for _, attribute := range attributes {
		obj.Put(core.NewRawElement(attribute.tag, attribute.vr, []byte(attribute.value)))
	}

	// When
	err := AnonymizeObject(obj, Options{KeepDates: true}, nil)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	for _, attribute := range attributes {
		if value, _ := obj.GetString(attribute.tag); strings.TrimSpace(value) != attribute.value {
			t.Errorf("date/time %s = %q, want kept %q", attribute.tag, value, attribute.value)
		}
	}
}

func TestAnonymizeObjectUsesCustomPatientValues(t *testing.T) {
	// Given
	obj := deidTestObject()

	// When
	err := AnonymizeObject(obj, Options{
		PatientName: "Research^Subject",
		PatientID:   "CASE-001",
	}, nil)

	// Then
	if err != nil {
		t.Fatal(err)
	}
	if name, _ := obj.GetString(testTagPatientName); strings.TrimSpace(name) != "Research^Subject" {
		t.Fatalf("PatientName = %q, want Research^Subject", name)
	}
	if id, _ := obj.GetString(testTagPatientID); strings.TrimSpace(id) != "CASE-001" {
		t.Fatalf("PatientID = %q, want CASE-001", id)
	}
}

func TestAnonymizeObjectReturnsTypedErrorForNilObject(t *testing.T) {
	// When
	err := AnonymizeObject(nil, Options{}, nil)

	// Then
	if !errors.Is(err, ErrNilObject) {
		t.Fatalf("AnonymizeObject(nil) error = %v, want ErrNilObject", err)
	}
}

func TestAnonymizeObjectMapsMissingHierarchyUidsPerObjectAndDistinctly(t *testing.T) {
	// Given
	uids := NewUIDRemapper()
	first := uidOnlyObject("", "", "1.2.3.4.5.1")
	second := uidOnlyObject("", "", "1.2.3.4.5.2")

	// When
	if err := AnonymizeObject(first, Options{}, uids); err != nil {
		t.Fatal(err)
	}
	if err := AnonymizeObject(second, Options{}, uids); err != nil {
		t.Fatal(err)
	}

	// Then
	if trimmedUID(first, testTagStudyInstanceUID) == trimmedUID(second, testTagStudyInstanceUID) {
		t.Fatal("empty StudyInstanceUID should not collapse across objects")
	}
	if trimmedUID(first, testTagSeriesInstanceUID) == trimmedUID(second, testTagSeriesInstanceUID) {
		t.Fatal("empty SeriesInstanceUID should not collapse across objects")
	}
	if trimmedUID(first, testTagSOPInstanceUID) == trimmedUID(second, testTagSOPInstanceUID) {
		t.Fatal("distinct SOPInstanceUIDs should stay distinct after remapping")
	}

	// Given
	allMissing := uidOnlyObject("", "", "")

	// When
	if err := AnonymizeObject(allMissing, Options{}, NewUIDRemapper()); err != nil {
		t.Fatal(err)
	}

	// Then
	studyUID := trimmedUID(allMissing, testTagStudyInstanceUID)
	seriesUID := trimmedUID(allMissing, testTagSeriesInstanceUID)
	sopUID := trimmedUID(allMissing, testTagSOPInstanceUID)
	if studyUID == seriesUID || studyUID == sopUID || seriesUID == sopUID {
		t.Fatalf("all-empty UIDs collapsed: study=%q series=%q sop=%q", studyUID, seriesUID, sopUID)
	}
}

func TestAnonymizeObjectTraversesNestedSequencesAndRemapsReferencedUids(t *testing.T) {
	// Given
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
		core.NewRawElement(testTagSeriesInstanceUID, core.VRUI, []byte("1.2.3.4")),
		core.NewRawElement(testTagSOPInstanceUID, core.VRUI, []byte("1.2.3.4.5")),
		sequenceElement(testTagReferencedImageSQ,
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(testTagPatientName, core.VRPN, []byte("NESTED^PATIENT")),
				core.NewRawElement(testTagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
				core.NewRawElement(testTagSeriesInstanceUID, core.VRUI, []byte("1.2.3.4")),
				core.NewRawElement(testTagReferencedSOPUID, core.VRUI, []byte("1.2.3.4.5")),
				sequenceElement(testTagNestedSequence,
					core.DataSet{Elements: []core.Element{
						core.NewRawElement(tagReferringPhysician, core.VRPN, []byte("DOC^REF")),
					}},
				),
			}},
		),
	}, std.Dictionary)

	// When
	if err := AnonymizeObject(obj, Options{}, NewUIDRemapper()); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	// Then
	topStudyUID := trimmedUID(obj, testTagStudyInstanceUID)
	topSeriesUID := trimmedUID(obj, testTagSeriesInstanceUID)
	topSOPUID := trimmedUID(obj, testTagSOPInstanceUID)
	items, ok := obj.GetSequence(testTagReferencedImageSQ)
	if !ok || len(items) != 1 {
		t.Fatalf("ReferencedImageSequence items = %d, %v, want 1 true", len(items), ok)
	}
	if got, _ := items[0].GetString(testTagPatientName); !strings.HasPrefix(got, "Anonymous") {
		t.Fatalf("nested PatientName = %q, want Anonymous*", got)
	}
	if got := strings.TrimRight(mustString(t, items[0], testTagStudyInstanceUID), "\x00"); got != topStudyUID {
		t.Fatalf("nested StudyInstanceUID = %q, want top remap %q", got, topStudyUID)
	}
	if got := strings.TrimRight(mustString(t, items[0], testTagSeriesInstanceUID), "\x00"); got != topSeriesUID {
		t.Fatalf("nested SeriesInstanceUID = %q, want top remap %q", got, topSeriesUID)
	}
	if got := strings.TrimRight(mustString(t, items[0], testTagReferencedSOPUID), "\x00"); got != topSOPUID {
		t.Fatalf("nested ReferencedSOPInstanceUID = %q, want top SOP remap %q", got, topSOPUID)
	}
	nested, ok := items[0].GetSequence(testTagNestedSequence)
	if !ok || len(nested) != 1 {
		t.Fatalf("NestedSequence items = %d, %v, want 1 true", len(nested), ok)
	}
	if got, _ := nested[0].GetString(tagReferringPhysician); strings.TrimSpace(got) != "" {
		t.Fatalf("nested ReferringPhysician = %q, want blank", got)
	}
}

func TestAnonymizeObjectRemapsInstanceUidsConsistentlyWithoutChangingSopClass(t *testing.T) {
	const (
		sharedFrameUID  = "1.2.826.0.1.3680043.9.7433.50"
		firstFailedUID  = "1.2.826.0.1.3680043.9.7433.51"
		secondFailedUID = "1.2.826.0.1.3680043.9.7433.52"
		sopClassUID     = "1.2.840.10008.5.1.4.1.1.2"
	)
	tagSOPClassUID := core.NewTag(0x0008, 0x0016)
	tagInstanceCreatorUID := core.NewTag(0x0008, 0x0014)
	tagIrradiationEventUID := core.NewTag(0x0008, 0x3010)
	tagFrameOfReferenceUID := core.NewTag(0x0020, 0x0052)
	tagUIDContentItem := core.NewTag(0x0040, 0xA124)
	tagReferencedFrameOfReferenceUID := core.NewTag(0x3006, 0x0024)
	tagFailedSOPInstanceUIDList := core.NewTag(0x0008, 0x0058)

	obj := deidTestObject()
	obj.Put(core.NewRawElement(tagSOPClassUID, core.VRUI, []byte(sopClassUID)))
	obj.Put(core.NewRawElement(tagInstanceCreatorUID, core.VRUI, []byte("1.2.826.0.1.3680043.9.7433.53")))
	obj.Put(core.NewRawElement(tagIrradiationEventUID, core.VRUI, []byte("1.2.826.0.1.3680043.9.7433.54")))
	obj.Put(core.NewRawElement(tagFrameOfReferenceUID, core.VRUI, []byte(sharedFrameUID)))
	obj.Put(core.NewRawElement(tagFailedSOPInstanceUIDList, core.VRUI, []byte(firstFailedUID+"\\"+secondFailedUID)))
	obj.Put(sequenceElement(testTagNestedSequence, core.DataSet{Elements: []core.Element{
		core.NewRawElement(tagUIDContentItem, core.VRUI, []byte(sharedFrameUID)),
		core.NewRawElement(tagReferencedFrameOfReferenceUID, core.VRUI, []byte(sharedFrameUID)),
	}}))
	uids := NewUIDRemapper()

	if err := AnonymizeObject(obj, Options{}, uids); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	mappedFrameUID := uids.Map(sharedFrameUID)
	if got, _ := obj.GetUID(tagFrameOfReferenceUID); got != mappedFrameUID {
		t.Errorf("FrameOfReferenceUID = %q, want shared remap %q", got, mappedFrameUID)
	}
	for _, tag := range []core.Tag{tagInstanceCreatorUID, tagIrradiationEventUID} {
		if got, _ := obj.GetUID(tag); got == "" || !strings.HasPrefix(got, "2.25.") {
			t.Errorf("instance UID %s = %q, want remapped 2.25.*", tag, got)
		}
	}
	items, ok := obj.GetSequence(testTagNestedSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("nested sequence items = %d, %v, want 1, true", len(items), ok)
	}
	for _, tag := range []core.Tag{tagUIDContentItem, tagReferencedFrameOfReferenceUID} {
		if got, _ := items[0].GetUID(tag); got != mappedFrameUID {
			t.Errorf("nested UID %s = %q, want shared remap %q", tag, got, mappedFrameUID)
		}
	}
	failedUIDs, ok := obj.GetUIDs(tagFailedSOPInstanceUIDList)
	if !ok || len(failedUIDs) != 2 {
		t.Fatalf("FailedSOPInstanceUIDList = %v, %v, want two UIDs", failedUIDs, ok)
	}
	if failedUIDs[0] != uids.Map(firstFailedUID) || failedUIDs[1] != uids.Map(secondFailedUID) || failedUIDs[0] == failedUIDs[1] {
		t.Errorf("FailedSOPInstanceUIDList = %v, want two independent stable remaps", failedUIDs)
	}
	if got, _ := obj.GetUID(tagSOPClassUID); got != sopClassUID {
		t.Errorf("SOPClassUID = %q, want unchanged %q", got, sopClassUID)
	}
}

func TestAnonymizeObjectRemapsEveryBasicProfileActionUAttribute(t *testing.T) {
	elements := make([]core.Element, 0, len(instanceUIDAttributes))
	originals := make(map[core.Tag]string, len(instanceUIDAttributes))
	for i, tag := range instanceUIDAttributes {
		if _, duplicate := originals[tag]; duplicate {
			t.Fatalf("duplicate action-U tag %s", tag)
		}
		uid := fmt.Sprintf("1.2.826.0.1.3680043.9.7433.60.%d", i+1)
		originals[tag] = uid
		elements = append(elements, core.NewRawElement(tag, core.VRUI, []byte(uid)))
	}
	obj := object.FromElements(elements, std.Dictionary)
	uids := NewUIDRemapper()

	if err := AnonymizeObject(obj, Options{}, uids); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	for tag, original := range originals {
		got, ok := obj.GetUID(tag)
		if !ok || got != uids.Map(original) || got == original {
			t.Errorf("action-U attribute %s = %q, %v; want stable replacement for %q", tag, got, ok, original)
		}
	}
}

func TestAnonymizeObjectPrivateTagPolicyRemoveRetainAndAllowlist(t *testing.T) {
	// Given
	defaultRemove := privateTagTestObject()

	// When
	if err := AnonymizeObject(defaultRemove, Options{}, nil); err != nil {
		t.Fatalf("default AnonymizeObject() error = %v", err)
	}

	// Then
	assertPrivateTags(t, defaultRemove, false, false, false)

	// Given
	retain := privateTagTestObject()

	// When
	if err := AnonymizeObject(retain, Options{PrivateTagPolicy: RetainPrivateTags}, nil); err != nil {
		t.Fatalf("retain AnonymizeObject() error = %v", err)
	}

	// Then
	assertPrivateTags(t, retain, true, true, true)

	// Given
	allowCreator := privateTagTestObject()

	// When
	if err := AnonymizeObject(allowCreator, Options{PrivateTagPolicy: AllowPrivateTags(PrivateTagAllow{Tag: testTagPrivateCreator})}, nil); err != nil {
		t.Fatalf("allowlist AnonymizeObject() error = %v", err)
	}

	// Then
	assertPrivateTags(t, allowCreator, true, false, false)

	// Given
	allowData := privateTagTestObject()

	// When
	if err := AnonymizeObject(allowData, Options{PrivateTagPolicy: AllowPrivateTags(PrivateTagAllow{Tag: testTagPrivateData, Creator: "CREATOR"})}, nil); err != nil {
		t.Fatalf("allowlist data AnonymizeObject() error = %v", err)
	}

	// Then
	assertPrivateTags(t, allowData, true, true, false)
}

func TestAnonymizeObjectPrivateTagAllowlistUsesCreatorValue(t *testing.T) {
	// Given
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagPrivateCreator, core.VRLO, []byte("ALLOWED")),
		core.NewRawElement(testTagPrivateData, core.VRLO, []byte("KEEP")),
		core.NewRawElement(testTagPrivateCreatorAlt, core.VRLO, []byte("OTHER")),
		core.NewRawElement(testTagPrivateDataAlt, core.VRLO, []byte("DROP")),
	}, std.Dictionary)

	// When
	if err := AnonymizeObject(obj, Options{PrivateTagPolicy: AllowPrivateTags(PrivateTagAllow{Tag: testTagPrivateData, Creator: "ALLOWED"})}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	// Then
	if !obj.Has(testTagPrivateCreator) || !obj.Has(testTagPrivateData) {
		t.Fatal("allowed private creator/data were removed")
	}
	if obj.Has(testTagPrivateCreatorAlt) || obj.Has(testTagPrivateDataAlt) {
		t.Fatal("different private creator block was retained")
	}
}

func TestAnonymizeObjectRetainedPrivateSequenceStillTraversesItems(t *testing.T) {
	// Given
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagPrivateCreator, core.VRLO, []byte("CREATOR")),
		sequenceElement(testTagPrivateSequence,
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(testTagPatientName, core.VRPN, []byte("PRIVATESEQ^PATIENT")),
			}},
		),
	}, std.Dictionary)

	// When
	if err := AnonymizeObject(obj, Options{PrivateTagPolicy: RetainPrivateTags}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	// Then
	items, ok := obj.GetSequence(testTagPrivateSequence)
	if !ok || len(items) != 1 {
		t.Fatalf("private sequence items = %d, %v, want 1 true", len(items), ok)
	}
	if got, _ := items[0].GetString(testTagPatientName); !strings.HasPrefix(got, "Anonymous") {
		t.Fatalf("private sequence PatientName = %q, want Anonymous*", got)
	}
}

func TestAnonymizeObjectRecursiveSequenceChangesDoNotMutateSharedSourceObject(t *testing.T) {
	// Given
	sharedSeq := sequenceElement(testTagReferencedImageSQ,
		core.DataSet{Elements: []core.Element{
			core.NewRawElement(testTagPatientName, core.VRPN, []byte("SOURCE^PATIENT")),
		}},
	)
	source := object.FromElements([]core.Element{sharedSeq}, std.Dictionary)
	target := object.FromElements(source.Elements(), std.Dictionary)

	// When
	if err := AnonymizeObject(target, Options{}, nil); err != nil {
		t.Fatalf("AnonymizeObject() error = %v", err)
	}

	// Then
	sourceItems, _ := source.GetSequence(testTagReferencedImageSQ)
	if got, _ := sourceItems[0].GetString(testTagPatientName); strings.TrimSpace(got) != "SOURCE^PATIENT" {
		t.Fatalf("source nested PatientName = %q, want SOURCE^PATIENT", got)
	}
	targetItems, _ := target.GetSequence(testTagReferencedImageSQ)
	if got, _ := targetItems[0].GetString(testTagPatientName); !strings.HasPrefix(got, "Anonymous") {
		t.Fatalf("target nested PatientName = %q, want Anonymous*", got)
	}
}

func TestAnonymizeObjectWithReportReportsBurnedInAnnotationWithoutModifyingPixels(t *testing.T) {
	// Given
	pixels := []byte{1, 2, 3, 4}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("YES ")),
		usElement(testTagRows, 2),
		usElement(testTagColumns, 2),
		usElement(testTagSamplesPerPixel, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("MONOCHROME2")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	report, err := AnonymizeObjectWithReport(obj, Options{}, nil)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}
	if report.BurnedInPixel.Risk != BurnedInPixelRiskPresent {
		t.Fatalf("BurnedInPixel.Risk = %v, want present", report.BurnedInPixel.Risk)
	}
	if report.BurnedInPixel.MetadataValue != "YES" {
		t.Fatalf("BurnedInPixel.MetadataValue = %q, want YES", report.BurnedInPixel.MetadataValue)
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, pixels) {
		t.Fatalf("PixelData = %v, want unchanged %v", got, pixels)
	}
}

func TestAnonymizeObjectWithReportRedactsMalformedBurnedInMetadata(t *testing.T) {
	// Given: malformed metadata may itself contain arbitrary sensitive text.
	const sensitiveMetadata = "PATIENT_SECRET"
	obj := deidTestObject()
	obj.Put(core.NewRawElement(testTagBurnedIn, core.VRCS, []byte(sensitiveMetadata)))

	// When
	report, err := AnonymizeObjectWithReport(obj, Options{}, nil)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}
	if report.BurnedInPixel.Risk != BurnedInPixelRiskUnknown {
		t.Fatalf("BurnedInPixel.Risk = %v, want unknown", report.BurnedInPixel.Risk)
	}
	if report.BurnedInPixel.MetadataValue != "OTHER" {
		t.Fatalf("BurnedInPixel.MetadataValue = %q, want OTHER", report.BurnedInPixel.MetadataValue)
	}
	if strings.Contains(fmt.Sprintf("%#v", report), sensitiveMetadata) {
		t.Fatalf("report leaked malformed BurnedInAnnotation metadata: %#v", report)
	}
}

func TestAnonymizeObjectWithReportSurfacesRecognizableVisualFeatureRisk(t *testing.T) {
	tag := core.NewTag(0x0028, 0x0302)
	for _, test := range []struct {
		name  string
		value *string
		want  BurnedInPixelRisk
	}{
		{name: "missing", want: BurnedInPixelRiskUnknown},
		{name: "yes", value: ptr("YES"), want: BurnedInPixelRiskPresent},
		{name: "no", value: ptr("NO"), want: BurnedInPixelRiskAbsent},
		{name: "malformed", value: ptr("SECRET^PATIENT"), want: BurnedInPixelRiskUnknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := deidTestObject()
			if test.value != nil {
				obj.Put(core.NewRawElement(tag, core.VRCS, []byte(*test.value)))
			}
			report, err := AnonymizeObjectWithReport(obj, Options{}, nil)
			if err != nil {
				t.Fatalf("AnonymizeObjectWithReport: %v", err)
			}
			if report.RecognizableVisualFeatures.Risk != test.want {
				t.Fatalf("RecognizableVisualFeatures.Risk = %v, want %v", report.RecognizableVisualFeatures.Risk, test.want)
			}
			if strings.Contains(fmt.Sprintf("%#v", report), "SECRET^PATIENT") {
				t.Fatal("report leaked malformed recognizable-visual-feature metadata")
			}
		})
	}
}

func TestVisualRiskMetadataRejectsMultipleComponents(t *testing.T) {
	obj := object.FromElements([]core.Element{
		core.NewRawElement(tagBurnedInAnnotation, core.VRCS, []byte("NO\\YES")),
		core.NewRawElement(tagRecognizableVisualFeatures, core.VRCS, []byte("NO\\YES")),
	}, nil)
	report, err := AnonymizeObjectWithReport(obj, Options{}, nil)
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport: %v", err)
	}
	if report.BurnedInPixel.Risk != BurnedInPixelRiskUnknown || report.BurnedInPixel.MetadataValue != "OTHER" {
		t.Fatalf("BurnedInPixel = %#v, want unknown/OTHER", report.BurnedInPixel)
	}
	if report.RecognizableVisualFeatures.Risk != BurnedInPixelRiskUnknown || report.RecognizableVisualFeatures.MetadataValue != "OTHER" {
		t.Fatalf("RecognizableVisualFeatures = %#v, want unknown/OTHER", report.RecognizableVisualFeatures)
	}

	strict := object.FromElements([]core.Element{
		core.NewRawElement(tagBurnedInAnnotation, core.VRCS, []byte("NO\\YES")),
		core.NewRawElement(tagRecognizableVisualFeatures, core.VRCS, []byte("NO\\YES")),
	}, nil)
	plan, err := PlanBasicProfile(context.Background(), strict, DefaultBasicProfileOptions(), nil)
	if err != nil {
		t.Fatalf("PlanBasicProfile: %v", err)
	}
	if plan.Report().Complete || len(plan.Report().ResidualRisks) < 2 {
		t.Fatalf("strict visual risk report = complete:%v risks:%#v", plan.Report().Complete, plan.Report().ResidualRisks)
	}
}

func ptr(value string) *string { return &value }

func TestAnonymizeObjectWithReportFallbackDimensionsHonorBigEndianByteOrder(t *testing.T) {
	// Given: Rows and Columns are available, but the remaining pixel metadata
	// is intentionally incomplete so pixelPolicyMetadata uses its fallback.
	rows := make([]byte, 2)
	columns := make([]byte, 2)
	binary.BigEndian.PutUint16(rows, 258)
	binary.BigEndian.PutUint16(columns, 515)
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("YES ")),
		core.NewRawElement(testTagRows, core.VRUS, rows),
		core.NewRawElement(testTagColumns, core.VRUS, columns),
	}, std.Dictionary)
	obj.SetValueByteOrder(binary.BigEndian)

	// When
	var gotRows, gotColumns int
	_, err := AnonymizeObjectWithReport(obj, Options{
		BurnedInPixelPolicy: func(ctx BurnedInPixelPolicyContext) BurnedInPixelPolicyResult {
			if ctx.Metadata != nil {
				t.Fatalf("Metadata = %#v, want nil fallback metadata", ctx.Metadata)
			}
			gotRows, gotColumns = ctx.Rows, ctx.Columns
			return BurnedInPixelPolicyResult{}
		},
	}, nil)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}
	if gotRows != 258 || gotColumns != 515 {
		t.Fatalf("policy dimensions = %dx%d, want 258x515", gotRows, gotColumns)
	}
}

func TestAnonymizeObjectWithReportAppliesExplicitBurnedInPixelRegions(t *testing.T) {
	// Given
	pixels := []byte{
		1, 2, 3, 4,
		5, 6, 7, 8,
		9, 10, 11, 12,
		13, 14, 15, 16,
	}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("YES ")),
		usElement(testTagRows, 4),
		usElement(testTagColumns, 4),
		usElement(testTagSamplesPerPixel, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("MONOCHROME2")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	report, err := AnonymizeObjectWithReport(obj, Options{
		BurnedInPixelPolicy: func(ctx BurnedInPixelPolicyContext) BurnedInPixelPolicyResult {
			if ctx.Report.Risk != BurnedInPixelRiskPresent {
				t.Fatalf("policy risk = %v, want present", ctx.Report.Risk)
			}
			if ctx.Rows != 4 || ctx.Columns != 4 {
				t.Fatalf("policy dimensions = %dx%d, want 4x4", ctx.Rows, ctx.Columns)
			}
			return BurnedInPixelPolicyResult{
				Regions: []PixelRegion{{X: 1, Y: 1, Width: 2, Height: 2}},
				Fill:    []byte{0xEE},
			}
		},
	}, nil)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}
	want := []byte{
		1, 2, 3, 4,
		5, 0xEE, 0xEE, 8,
		9, 0xEE, 0xEE, 12,
		13, 14, 15, 16,
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, want) {
		t.Fatalf("PixelData = %v, want %v", got, want)
	}
	if len(report.BurnedInPixel.RedactedRegions) != 1 || report.BurnedInPixel.RedactedRegions[0] != (PixelRegion{X: 1, Y: 1, Width: 2, Height: 2}) {
		t.Fatalf("RedactedRegions = %#v", report.BurnedInPixel.RedactedRegions)
	}
}

func TestAnonymizeObjectWithReportAppliesPlanarPixelRegionRedaction(t *testing.T) {
	// Given
	pixels := []byte{
		1, 2, 3, 4, // R plane
		5, 6, 7, 8, // G plane
		9, 10, 11, 12, // B plane
	}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("YES ")),
		usElement(testTagRows, 2),
		usElement(testTagColumns, 2),
		usElement(testTagSamplesPerPixel, 3),
		usElement(testTagPlanarConfig, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("RGB")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	report, err := AnonymizeObjectWithReport(obj, Options{
		BurnedInPixelPolicy: func(BurnedInPixelPolicyContext) BurnedInPixelPolicyResult {
			return BurnedInPixelPolicyResult{
				Regions: []PixelRegion{{X: 1, Y: 0, Width: 1, Height: 1}},
			}
		},
	}, nil)

	// Then
	if err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}
	want := []byte{
		1, 0, 3, 4,
		5, 0, 7, 8,
		9, 0, 11, 12,
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, want) {
		t.Fatalf("PixelData = %v, want %v", got, want)
	}
	if len(report.BurnedInPixel.RedactedRegions) != 1 || report.BurnedInPixel.RedactedRegions[0] != (PixelRegion{X: 1, Y: 0, Width: 1, Height: 1}) {
		t.Fatalf("RedactedRegions = %#v", report.BurnedInPixel.RedactedRegions)
	}
}

func TestAnonymizeObjectWithReportDoesNotMutateSourcePixelsWithoutExplicitRegions(t *testing.T) {
	// Given
	pixels := []byte{10, 20, 30, 40}
	obj := object.FromElements([]core.Element{
		core.NewRawElement(testTagBurnedIn, core.VRCS, []byte("YES ")),
		usElement(testTagRows, 2),
		usElement(testTagColumns, 2),
		usElement(testTagSamplesPerPixel, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("MONOCHROME2")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	if _, err := AnonymizeObjectWithReport(obj, Options{}, nil); err != nil {
		t.Fatalf("AnonymizeObjectWithReport() error = %v", err)
	}

	// Then
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, pixels) {
		t.Fatalf("PixelData = %v, want unchanged %v", got, pixels)
	}
}

func TestMaskPixelRegionsRejectsPackedOneBitPixelsWithoutMutation(t *testing.T) {
	// Given: eight one-bit pixels packed into one byte.
	pixels := []byte{0xff}
	obj := object.FromElements([]core.Element{
		usElement(testTagRows, 1),
		usElement(testTagColumns, 8),
		usElement(testTagSamplesPerPixel, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("MONOCHROME2 ")),
		usElement(testTagBitsAllocated, 1),
		usElement(testTagBitsStored, 1),
		usElement(testTagHighBit, 0),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	_, err := MaskPixelRegions(obj, []PixelRegion{{X: 7, Width: 1, Height: 1}}, nil)

	// Then
	if !errors.Is(err, ErrUnsupportedPixelRedaction) {
		t.Fatalf("MaskPixelRegions() error = %v, want ErrUnsupportedPixelRedaction", err)
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, pixels) {
		t.Fatalf("PixelData = %v, want unchanged %v", got, pixels)
	}
}

func TestMaskPixelRegionsRejectsSubsampledYBRWithoutMutation(t *testing.T) {
	// Given: YBR_FULL_422 stores two pixels in four bytes rather than the six
	// bytes implied by SamplesPerPixel=3.
	pixels := []byte{10, 20, 128, 128}
	obj := object.FromElements([]core.Element{
		usElement(testTagRows, 1),
		usElement(testTagColumns, 2),
		usElement(testTagSamplesPerPixel, 3),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("YBR_FULL_422")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	_, err := MaskPixelRegions(obj, []PixelRegion{{X: 1, Width: 1, Height: 1}}, nil)

	// Then
	if !errors.Is(err, ErrUnsupportedPixelRedaction) {
		t.Fatalf("MaskPixelRegions() error = %v, want ErrUnsupportedPixelRedaction", err)
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, pixels) {
		t.Fatalf("PixelData = %v, want unchanged %v", got, pixels)
	}
}

func TestMaskPixelRegionsRejectsOverflowingBoundsWithoutMutation(t *testing.T) {
	// Given: the first region is valid, while adding Width to the second X
	// would overflow int. A failed operation must not commit the first mask.
	pixels := []byte{1, 2}
	obj := object.FromElements([]core.Element{
		usElement(testTagRows, 1),
		usElement(testTagColumns, 2),
		usElement(testTagSamplesPerPixel, 1),
		core.NewRawElement(testTagPhotometric, core.VRCS, []byte("MONOCHROME2 ")),
		usElement(testTagBitsAllocated, 8),
		usElement(testTagBitsStored, 8),
		usElement(testTagHighBit, 7),
		usElement(testTagPixelRep, 0),
		core.NewRawElement(testTagPixelData, core.VROB, pixels),
	}, std.Dictionary)

	// When
	_, err := MaskPixelRegions(obj, []PixelRegion{
		{X: 0, Width: 1, Height: 1},
		{X: math.MaxInt, Width: 1, Height: 1},
	}, []byte{0xee})

	// Then
	if !errors.Is(err, ErrUnsupportedPixelRedaction) {
		t.Fatalf("MaskPixelRegions() error = %v, want ErrUnsupportedPixelRedaction", err)
	}
	got, _ := obj.GetRaw(testTagPixelData)
	if !bytes.Equal(got, pixels) {
		t.Fatalf("PixelData = %v, want unchanged %v", got, pixels)
	}
}

func deidTestObject() *object.Object {
	return object.FromElements([]core.Element{
		core.NewRawElement(testTagPatientName, core.VRPN, []byte("DOE^JANE")),
		core.NewRawElement(testTagPatientID, core.VRLO, []byte("PID-123 ")),
		core.NewRawElement(testTagPatientBirthDate, core.VRDA, []byte("19800101")),
		core.NewRawElement(testTagInstitutionName, core.VRLO, []byte("Mercy Hospital ")),
		core.NewRawElement(testTagAccessionNumber, core.VRSH, []byte("ACC-9 ")),
		core.NewRawElement(testTagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
		core.NewRawElement(testTagSeriesInstanceUID, core.VRUI, []byte("1.2.3.4")),
		core.NewRawElement(testTagSOPInstanceUID, core.VRUI, []byte("1.2.3.4.5")),
	}, std.Dictionary)
}

func uidOnlyObject(study, series, sop string) *object.Object {
	return object.FromElements([]core.Element{
		core.NewRawElement(testTagStudyInstanceUID, core.VRUI, []byte(study)),
		core.NewRawElement(testTagSeriesInstanceUID, core.VRUI, []byte(series)),
		core.NewRawElement(testTagSOPInstanceUID, core.VRUI, []byte(sop)),
	}, std.Dictionary)
}

func trimmedUID(obj *object.Object, tag core.Tag) string {
	value, _ := obj.GetString(tag)
	return strings.TrimRight(value, "\x00")
}

func usElement(tag core.Tag, value uint16) core.Element {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	return core.NewRawElement(tag, core.VRUS, raw)
}

func sequenceElement(tag core.Tag, items ...core.DataSet) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: core.VRSQ},
		Value:  core.SequenceValue{Items: items},
	}
}

func privateTagTestObject() *object.Object {
	return object.FromElements([]core.Element{
		core.NewRawElement(testTagPrivateCreator, core.VRLO, []byte("CREATOR")),
		core.NewRawElement(testTagPrivateData, core.VRLO, []byte("PRIVATE PHI")),
		sequenceElement(testTagReferencedImageSQ,
			core.DataSet{Elements: []core.Element{
				core.NewRawElement(testTagPrivateData, core.VRLO, []byte("NESTED PRIVATE PHI")),
			}},
		),
	}, std.Dictionary)
}

func assertPrivateTags(t *testing.T, obj *object.Object, wantCreator, wantData, wantNestedData bool) {
	t.Helper()
	if got := obj.Has(testTagPrivateCreator); got != wantCreator {
		t.Fatalf("private creator present = %v, want %v", got, wantCreator)
	}
	if got := obj.Has(testTagPrivateData); got != wantData {
		t.Fatalf("private data present = %v, want %v", got, wantData)
	}
	items, ok := obj.GetSequence(testTagReferencedImageSQ)
	if !ok || len(items) != 1 {
		t.Fatalf("ReferencedImageSequence items = %d, %v, want 1 true", len(items), ok)
	}
	if got := items[0].Has(testTagPrivateData); got != wantNestedData {
		t.Fatalf("nested private data present = %v, want %v", got, wantNestedData)
	}
}

func mustString(t *testing.T, obj *object.Object, tag core.Tag) string {
	t.Helper()
	value, ok := obj.GetString(tag)
	if !ok {
		t.Fatalf("missing %s", tag)
	}
	return value
}
