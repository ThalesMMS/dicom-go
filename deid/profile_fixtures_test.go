package deid

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestBasicProfileSyntheticSOPFixtures(t *testing.T) {
	fixtures := []struct {
		name        string
		sopClassUID string
		extra       []core.Element
		options     []ProfileOption
		wantError   error
	}{
		{name: "CT", sopClassUID: "1.2.840.10008.5.1.4.1.1.2"},
		{name: "MR", sopClassUID: "1.2.840.10008.5.1.4.1.1.4"},
		{name: "US", sopClassUID: "1.2.840.10008.5.1.4.1.1.6.1"},
		{name: "SR", sopClassUID: "1.2.840.10008.5.1.4.1.1.88.33", options: []ProfileOption{ProfileOptionCleanStructuredContent}, extra: []core.Element{
			profileSequence(core.NewTag(0x0040, 0xA730), core.DataSet{Elements: []core.Element{
				core.NewRawElement(tagContentValueType, core.VRCS, []byte("TEXT")),
				profileSequence(tagConceptNameCodeSequence, core.DataSet{Elements: []core.Element{
					core.NewRawElement(tagCodeValue, core.VRSH, []byte("121022")),
					core.NewRawElement(tagCodingSchemeDesignator, core.VRSH, []byte("DCM")),
				}}),
				core.NewRawElement(core.NewTag(0x0040, 0xA160), core.VRUT, []byte("SECRET SR TEXT")),
			}}),
		}},
		{name: "SEG", sopClassUID: "1.2.840.10008.5.1.4.1.1.66.4"},
		{name: "RT", sopClassUID: "1.2.840.10008.5.1.4.1.1.481.3"},
		{name: "DICOMDIR", sopClassUID: "1.2.840.10008.1.3.10", wantError: ErrDICOMDIRPolicyRequired, extra: []core.Element{
			core.NewRawElement(core.NewTag(0x0004, 0x1500), core.VRCS, []byte("SECRET\\PATH")),
		}},
		{name: "waveform", sopClassUID: "1.2.840.10008.5.1.4.1.1.9.1.1", extra: []core.Element{
			core.NewRawElement(core.NewTag(0x5400, 0x1010), core.VROW, []byte{1, 2, 3, 4}),
		}},
		{name: "encapsulated-document", sopClassUID: "1.2.840.10008.5.1.4.1.1.104.1", extra: []core.Element{
			core.NewRawElement(core.NewTag(0x0042, 0x0011), core.VROB, []byte("SECRET DOCUMENT")),
		}},
		{name: "enhanced-multiframe", sopClassUID: "1.2.840.10008.5.1.4.1.1.2.1", extra: []core.Element{
			profileSequence(core.NewTag(0x5200, 0x9230), core.DataSet{Elements: []core.Element{
				core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET^NESTED")),
			}}),
		}},
	}

	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			referenceSequence := core.NewTag(0x0008, 0x1115)
			elements := []core.Element{
				core.NewRawElement(tagSOPClassUID, core.VRUI, []byte(fixture.sopClassUID)),
				core.NewRawElement(tagSOPInstanceUID, core.VRUI, []byte("1.2.3.4.5")),
				core.NewRawElement(tagStudyInstanceUID, core.VRUI, []byte("1.2.3")),
				core.NewRawElement(tagSeriesInstanceUID, core.VRUI, []byte("1.2.3.1")),
				core.NewRawElement(tagPatientName, core.VRPN, []byte("SECRET^PATIENT")),
				core.NewRawElement(tagPatientID, core.VRLO, []byte("SECRET-ID")),
				profileSequence(referenceSequence, core.DataSet{Elements: []core.Element{
					core.NewRawElement(tagReferencedSOPUID, core.VRUI, []byte("1.2.3.4.5")),
				}}),
			}
			elements = append(elements, fixture.extra...)
			obj := object.FromElements(elements, nil)
			options := DefaultBasicProfileOptions()
			options.SelectedOptions = fixture.options
			report, err := ApplyBasicProfile(context.Background(), obj, options, NewUIDRemapper())
			if fixture.wantError != nil {
				if !errors.Is(err, fixture.wantError) {
					t.Fatalf("ApplyBasicProfile error = %v, want %v", err, fixture.wantError)
				}
				return
			}
			if err != nil {
				t.Fatalf("ApplyBasicProfile: %v", err)
			}
			if got, _ := obj.GetString(tagSOPClassUID); strings.TrimSpace(got) != fixture.sopClassUID {
				t.Fatalf("SOP Class UID = %q", got)
			}
			if got, _ := obj.GetString(tagPatientName); strings.TrimSpace(got) != "" {
				t.Fatalf("PatientName = %q", got)
			}
			mappedSOP := trimmedUID(obj, tagSOPInstanceUID)
			items, ok := obj.GetSequence(referenceSequence)
			if !ok || len(items) != 1 || trimmedUID(items[0], tagReferencedSOPUID) != mappedSOP {
				t.Fatal("referenced SOP UID did not preserve the internal relationship")
			}
			encodedReport, _ := json.Marshal(report)
			for _, secret := range []string{"SECRET", "1.2.3.4.5"} {
				if bytes.Contains(encodedReport, []byte(secret)) {
					t.Fatalf("report leaked %q", secret)
				}
			}
			var encoded bytes.Buffer
			if err := object.WriteDataSet(&encoded, obj, transfer.ExplicitVRLittleEndian); err != nil {
				t.Fatalf("WriteDataSet: %v", err)
			}
			if encoded.Len() == 0 {
				t.Fatal("empty serialized fixture")
			}
		})
	}
}
