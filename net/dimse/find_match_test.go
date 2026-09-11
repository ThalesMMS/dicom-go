package dimse

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	dicomencoding "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/internal/testutil"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestProjectFindResponsePreservesRequiredSpecificCharacterSetAcrossWire(t *testing.T) {
	tests := []struct {
		name        string
		level       string
		declaration []string
		patientName string
		raw         bool
	}{
		{name: "Study Root UTF-8", level: QueryRetrieveLevelStudy, declaration: []string{"ISO_IR 192"}, patientName: "José^Silva"},
		{name: "Patient Root Latin-1", level: QueryRetrieveLevelPatient, declaration: []string{"ISO_IR 100"}, patientName: "André^Lima", raw: true},
		{name: "Patient Root ISO 2022", level: QueryRetrieveLevelPatient, declaration: []string{"", "ISO 2022 IR 87"}, patientName: "=山田^太郎", raw: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			nameElement := core.Element{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{test.patientName}}
			if test.raw {
				charset, err := dicomencoding.ParseCharacterSet(test.declaration...)
				if err != nil {
					t.Fatal(err)
				}
				encoded, err := charset.EncodePersonName(test.patientName)
				if err != nil {
					t.Fatal(err)
				}
				nameElement.Value = core.RawValue(encoded)
			}
			query := object.FromElements([]core.Element{
				// This declaration governs the request only. The response must use
				// the provider's declaration below.
				{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"ISO_IR 192"}},
				{Header: core.ElementHeader{Tag: tags.QueryRetrieveLevel, VR: core.VRCS}, Value: core.StringValue{test.level}},
				{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{""}},
			}, std.Dictionary)
			candidate := object.FromElements([]core.Element{
				{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue(test.declaration)},
				nameElement,
			}, std.Dictionary)
			projected, err := ProjectFindResponse(query, candidate, "", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got, ok := projected.GetStrings(tagMWLSpecificCharacterSet); !ok || strings.Join(got, "\\") != strings.Join(test.declaration, "\\") {
				t.Fatalf("SpecificCharacterSet = %#v, want %#v", got, test.declaration)
			}
			var wire bytes.Buffer
			if err := object.WriteDataSet(&wire, projected, transfer.ImplicitVRLittleEndian); err != nil {
				t.Fatalf("wire write: %v", err)
			}
			roundTrip, err := object.ReadDataSet(bytes.NewReader(wire.Bytes()), transfer.ImplicitVRLittleEndian)
			if err != nil {
				t.Fatalf("wire read: %v", err)
			}
			if got, ok := roundTrip.GetString(tags.PatientName); !ok || got != test.patientName {
				t.Fatalf("round-trip PatientName = %q, present=%t, want %q", got, ok, test.patientName)
			}
		})
	}
}

func TestProjectFindResponseOmitsUnneededSpecificCharacterSet(t *testing.T) {
	query := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{""}}}, std.Dictionary)
	candidate := object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"ISO_IR 192"}},
		{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{"ASCII^ONLY"}},
	}, std.Dictionary)
	projected, err := ProjectFindResponse(query, candidate, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if projected.Has(tagMWLSpecificCharacterSet) {
		t.Fatal("ASCII-only response included an unnecessary SpecificCharacterSet")
	}
}

func TestProjectFindResponseMapsMissingOrMalformedCharacterSetToUnableToProcess(t *testing.T) {
	query := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{""}}}, std.Dictionary)
	for _, candidate := range []*object.Object{
		object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{"José"}}}, std.Dictionary),
		object.FromElements([]core.Element{
			{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"UNKNOWN"}},
			{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{"José"}},
		}, std.Dictionary),
		object.FromElements([]core.Element{
			{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"ISO_IR 100"}},
			{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.StringValue{"山田^太郎"}},
		}, std.Dictionary),
		object.FromElements([]core.Element{
			{Header: core.ElementHeader{Tag: tagMWLSpecificCharacterSet, VR: core.VRCS}, Value: core.StringValue{"ISO_IR 100"}},
			{Header: core.ElementHeader{Tag: tags.PatientName, VR: core.VRPN}, Value: core.RawValue{'J', 0x1b, 'o'}},
		}, std.Dictionary),
	} {
		_, err := ProjectFindResponse(query, candidate, "", nil)
		var statusErr *CFindSCPError
		if !errors.As(err, &statusErr) || statusErr.Status != CFindStatusUnableToProcess {
			t.Fatalf("ProjectFindResponse() error = %v, want unable-to-process CFindSCPError", err)
		}
		if !errors.Is(err, ErrCFindResponseCharacterSet) {
			t.Fatalf("ProjectFindResponse() error = %v, want ErrCFindResponseCharacterSet", err)
		}
	}
}

func TestProjectFindResponseDoesNotUseCharacterSetForUnrelatedVR(t *testing.T) {
	query := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: tags.StudyInstanceUID, VR: core.VRUI}, Value: core.StringValue{""}}}, std.Dictionary)
	candidate := object.FromElements([]core.Element{{Header: core.ElementHeader{Tag: tags.StudyInstanceUID, VR: core.VRUI}, Value: core.RawValue{0xff}}}, std.Dictionary)
	projected, err := ProjectFindResponse(query, candidate, "", nil)
	if err != nil {
		t.Fatalf("non-character-set VR projection error = %v", err)
	}
	if projected.Has(tagMWLSpecificCharacterSet) {
		t.Fatal("SpecificCharacterSet was added for a VR it does not govern")
	}
}

func TestValidateResponseTextEncodingUsesSingleValueJISRomanSemantics(t *testing.T) {
	charset, err := dicomencoding.ParseCharacterSet("ISO_IR 13")
	if err != nil {
		t.Fatal(err)
	}
	tag := core.NewTag(0x0008, 0x4000)
	for _, element := range []core.Element{
		{Header: core.ElementHeader{Tag: tag, VR: core.VRST}, Value: core.StringValue{"¥"}},
		{Header: core.ElementHeader{Tag: tag, VR: core.VRST}, Value: core.RawValue{0x5c}},
	} {
		if err := validateResponseTextEncoding([]core.Element{element}, charset); err != nil {
			t.Fatalf("validateResponseTextEncoding(%T) error = %v", element.Value, err)
		}
	}
}

func TestMemoryFindIndexStudyRootImageUIDFilters(t *testing.T) {
	index := MemoryFindIndex{
		Candidates: []*object.Object{
			imageCandidate("1.2.100", "1.2.100.1", "1.2.100.1.1"),
			imageCandidate("1.2.100", "1.2.100.1", "1.2.100.1.2"),
			imageCandidate("1.2.200", "1.2.200.1", "1.2.200.1.1"),
		},
		RetrieveAETitle: "FINDSCP",
	}
	identifier, err := BuildStudyRootImageFindKeys(map[string]string{
		"StudyInstanceUID":  "1.2.100",
		"SeriesInstanceUID": "1.2.100.1",
		"SOPInstanceUID":    "1.2.100.1.1\\1.2.100.1.2",
	}, "SOPClassUID", "InstanceNumber")
	if err != nil {
		t.Fatal(err)
	}
	matches, err := index.Find(context.Background(), CFindRequestContext{
		Identifier:         object.FromElements(identifier, std.Dictionary),
		QueryRetrieveLevel: QueryRetrieveLevelImage,
	})
	if err != nil {
		t.Fatalf("Find() error = %v", err)
	}
	if len(matches) != 2 {
		t.Fatalf("matches = %d, want 2", len(matches))
	}
	got := map[string]bool{}
	for _, match := range matches {
		if level, _ := match.GetString(tags.QueryRetrieveLevel); level != QueryRetrieveLevelImage {
			t.Fatalf("QueryRetrieveLevel = %q, want IMAGE", level)
		}
		uid, _ := match.GetString(tags.SOPInstanceUID)
		got[uid] = true
		if ae, _ := match.GetString(tags.RetrieveAETitle); ae != "FINDSCP" {
			t.Fatalf("RetrieveAETitle = %q", ae)
		}
	}
	if !got["1.2.100.1.1"] || !got["1.2.100.1.2"] {
		t.Fatalf("SOP Instance UIDs = %v", got)
	}

	missIdentifier, err := BuildStudyRootImageFindKeys(map[string]string{
		"StudyInstanceUID":  "1.2.100",
		"SeriesInstanceUID": "1.2.100.1",
		"SOPInstanceUID":    "9.9.9",
	})
	if err != nil {
		t.Fatal(err)
	}
	miss, err := index.Find(context.Background(), CFindRequestContext{
		Identifier:         object.FromElements(missIdentifier, std.Dictionary),
		QueryRetrieveLevel: QueryRetrieveLevelImage,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(miss) != 0 {
		t.Fatalf("miss matches = %d, want 0", len(miss))
	}
}

func TestMemoryFindIndexIndependentFindSCUAllStudyRootLevels(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	index := MemoryFindIndex{
		Candidates: []*object.Object{
			imageCandidate("1.2.100", "1.2.100.1", "1.2.100.1.1"),
		},
		RetrieveAETitle: "FINDSCP",
	}
	levels := []struct {
		name       string
		identifier []core.Element
		wantUIDTag core.Tag
		wantUID    string
	}{
		{
			name: "STUDY",
			identifier: mustBuild(t, func() ([]core.Element, error) {
				return BuildStudyRootStudyFindKeys(map[string]string{"StudyInstanceUID": "1.2.100"}, "PatientID")
			}),
			wantUIDTag: tags.StudyInstanceUID,
			wantUID:    "1.2.100",
		},
		{
			name: "SERIES",
			identifier: mustBuild(t, func() ([]core.Element, error) {
				return BuildStudyRootSeriesFindKeys(map[string]string{
					"StudyInstanceUID":  "1.2.100",
					"SeriesInstanceUID": "1.2.100.1",
				}, "Modality")
			}),
			wantUIDTag: tags.SeriesInstanceUID,
			wantUID:    "1.2.100.1",
		},
		{
			name: "IMAGE",
			identifier: mustBuild(t, func() ([]core.Element, error) {
				return BuildStudyRootImageFindKeys(map[string]string{
					"StudyInstanceUID":  "1.2.100",
					"SeriesInstanceUID": "1.2.100.1",
					"SOPInstanceUID":    "1.2.100.1.1",
				}, "SOPClassUID")
			}),
			wantUIDTag: tags.SOPInstanceUID,
			wantUID:    "1.2.100.1.1",
		},
	}
	for _, level := range levels {
		t.Run(level.name, func(t *testing.T) {
			matches := runStudyRootFindAgainstIndex(t, ctx, index, level.identifier)
			if len(matches) != 1 {
				t.Fatalf("matches = %d, want 1", len(matches))
			}
			if got, _ := matches[0].GetString(level.wantUIDTag); got != level.wantUID {
				t.Fatalf("%s = %q, want %q", level.wantUIDTag, got, level.wantUID)
			}
		})
	}
}

func TestMemoryFindIndexHonorsCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	index := MemoryFindIndex{Candidates: []*object.Object{imageCandidate("1.2.100", "1.2.100.1", "1.2.100.1.1")}}
	identifier, err := BuildStudyRootImageFindKeys(map[string]string{
		"StudyInstanceUID":  "",
		"SeriesInstanceUID": "",
		"SOPInstanceUID":    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = index.Find(ctx, CFindRequestContext{
		Identifier:         object.FromElements(identifier, std.Dictionary),
		QueryRetrieveLevel: QueryRetrieveLevelImage,
	})
	if err == nil {
		t.Fatal("Find() error = nil, want canceled")
	}
}

func TestInteropIndependentFindSCUStudyRootLevels(t *testing.T) {
	testutil.SkipIfIntegration(t)
	host := getenvDefault("ORTHANC_HOST", "127.0.0.1")
	port := getenvDefault("ORTHANC_PORT", "4242")
	studyUID := os.Getenv("STUDY_UID")
	if studyUID == "" {
		t.Skip("set STUDY_UID for independent Study Root C-FIND interop")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	assoc, err := ul.DialContext(ctx, host+":"+port, ul.DialOptions{
		CallingAETitle: getenvDefault("CALLING_AET", "DICOMGO"),
		CalledAETitle:  getenvDefault("CALLED_AET", "ORTHANC"),
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = assoc.Release(ctx) }()
	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		t.Fatal(err)
	}
	for _, level := range []string{QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage} {
		keys := map[string]string{"StudyInstanceUID": studyUID}
		var identifier []core.Element
		switch level {
		case QueryRetrieveLevelStudy:
			identifier, err = BuildStudyRootStudyFindKeys(keys)
		case QueryRetrieveLevelSeries:
			identifier, err = BuildStudyRootSeriesFindKeys(keys)
		default:
			identifier, err = BuildStudyRootImageFindKeys(keys)
		}
		if err != nil {
			t.Fatalf("build %s identifier: %v", level, err)
		}
		results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
			AffectedSOPClassUID: StudyRootFindSOPClassUID,
			MessageID:           1,
		}, object.FromElements(identifier, std.Dictionary), syntax)
		var pending int
		for result := range results {
			if CategorizeCFindStatus(result.Status()) == CFindStatusPending {
				pending++
			}
		}
		if err := <-errs; err != nil {
			t.Fatalf("%s C-FIND: %v", level, err)
		}
		if pending == 0 && level == QueryRetrieveLevelStudy {
			t.Fatalf("%s C-FIND returned no matches for STUDY_UID %s", level, studyUID)
		}
	}
}

func imageCandidate(studyUID, seriesUID, sopUID string) *object.Object {
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tags.QueryRetrieveLevel, VR: core.VRCS}, Value: core.StringValue{QueryRetrieveLevelImage}},
		{Header: core.ElementHeader{Tag: tags.PatientID, VR: core.VRLO}, Value: core.StringValue{"R001"}},
		{Header: core.ElementHeader{Tag: tags.StudyInstanceUID, VR: core.VRUI}, Value: core.StringValue{studyUID}},
		{Header: core.ElementHeader{Tag: tags.SeriesInstanceUID, VR: core.VRUI}, Value: core.StringValue{seriesUID}},
		{Header: core.ElementHeader{Tag: tags.SOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{sopUID}},
		{Header: core.ElementHeader{Tag: tags.SOPClassUID, VR: core.VRUI}, Value: core.StringValue{"1.2.840.10008.5.1.4.1.1.2"}},
		{Header: core.ElementHeader{Tag: tags.InstanceNumber, VR: core.VRIS}, Value: core.StringValue{"1"}},
		{Header: core.ElementHeader{Tag: tags.Modality, VR: core.VRCS}, Value: core.StringValue{"CT"}},
	}, std.Dictionary)
}

func mustBuild(t *testing.T, fn func() ([]core.Element, error)) []core.Element {
	t.Helper()
	elems, err := fn()
	if err != nil {
		t.Fatal(err)
	}
	return elems
}

func runStudyRootFindAgainstIndex(t *testing.T, ctx context.Context, index MemoryFindIndex, identifier []core.Element) []*object.Object {
	t.Helper()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)
		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		serverDone <- ServeStudyRootCFind(ctx, assoc, pc.ID, index)
	}()

	assoc, err := ul.Dial(listener.Addr().String(), ul.DialOptions{
		CallingAETitle: "FINDSCU",
		CalledAETitle:  "FINDSCP",
		Context:        ctx,
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer closeOrFail(t, "client association", assoc)
	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
	}, object.FromElements(identifier, std.Dictionary), transfer.ImplicitVRLittleEndian)
	var matches []*object.Object
	for result := range results {
		if CategorizeCFindStatus(result.Status()) == CFindStatusPending && result.Identifier != nil {
			matches = append(matches, result.Identifier)
		}
	}
	if err := <-errs; err != nil {
		t.Fatalf("Find SCU error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
	return matches
}
