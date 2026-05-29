package dicomdir

import (
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/dictionary/uid"
)

func TestRecordTypeForSOPClass(t *testing.T) {
	tests := []struct {
		name string
		uid  string
		want RecordType
		ok   bool
	}{
		{name: "CT image", uid: "1.2.840.10008.5.1.4.1.1.2", want: RecordTypeImage, ok: true},
		{name: "RT image remains image", uid: "1.2.840.10008.5.1.4.1.1.481.1", want: RecordTypeImage, ok: true},
		{name: "RT dose", uid: "1.2.840.10008.5.1.4.1.1.481.2", want: RecordTypeRTDose, ok: true},
		{name: "RT structure set", uid: "1.2.840.10008.5.1.4.1.1.481.3", want: RecordTypeRTStructureSet, ok: true},
		{name: "RT plan", uid: "1.2.840.10008.5.1.4.1.1.481.5", want: RecordTypeRTPlan, ok: true},
		{name: "RT ion plan", uid: "1.2.840.10008.5.1.4.1.1.481.8", want: RecordTypeRTPlan, ok: true},
		{name: "RT beams treatment record", uid: "1.2.840.10008.5.1.4.1.1.481.4", want: RecordTypeRTTreatmentRecord, ok: true},
		{name: "RT ion beams treatment record has no normative directory record mapping", uid: "1.2.840.10008.5.1.4.1.1.481.9"},
		{name: "presentation state", uid: "1.2.840.10008.5.1.4.1.1.11.1", want: RecordTypePresentation, ok: true},
		{name: "basic structured display", uid: "1.2.840.10008.5.1.4.1.1.131", want: RecordTypePresentation, ok: true},
		{name: "waveform presentation state", uid: "1.2.840.10008.5.1.4.1.1.9.100.1", want: RecordTypeWaveformPresentation, ok: true},
		{name: "waveform", uid: "1.2.840.10008.5.1.4.1.1.9.1.1", want: RecordTypeWaveform, ok: true},
		{name: "structured report", uid: "1.2.840.10008.5.1.4.1.1.88.11", want: RecordTypeSRDocument, ok: true},
		{name: "key object selection document", uid: "1.2.840.10008.5.1.4.1.1.88.59", want: RecordTypeKeyObjectDocument, ok: true},
		{name: "MR spectroscopy", uid: "1.2.840.10008.5.1.4.1.1.4.2", want: RecordTypeSpectroscopy, ok: true},
		{name: "raw data", uid: "1.2.840.10008.5.1.4.1.1.66", want: RecordTypeRawData, ok: true},
		{name: "spatial registration", uid: "1.2.840.10008.5.1.4.1.1.66.1", want: RecordTypeRegistration, ok: true},
		{name: "deformable spatial registration", uid: "1.2.840.10008.5.1.4.1.1.66.3", want: RecordTypeRegistration, ok: true},
		{name: "spatial fiducials", uid: "1.2.840.10008.5.1.4.1.1.66.2", want: RecordTypeFiducial, ok: true},
		{name: "encapsulated PDF", uid: "1.2.840.10008.5.1.4.1.1.104.1", want: RecordTypeEncapsulatedDocument, ok: true},
		{name: "encapsulated STL", uid: "1.2.840.10008.5.1.4.1.1.104.3", want: RecordTypeEncapsulatedDocument, ok: true},
		{name: "real world value map", uid: "1.2.840.10008.5.1.4.1.1.67", want: RecordTypeValueMap, ok: true},
		{name: "stereometric relationship", uid: "1.2.840.10008.5.1.4.1.1.77.1.5.3", want: RecordTypeStereometric, ok: true},
		{name: "surface segmentation", uid: "1.2.840.10008.5.1.4.1.1.66.5", want: RecordTypeSurface, ok: true},
		{name: "surface scan mesh", uid: "1.2.840.10008.5.1.4.1.1.68.1", want: RecordTypeSurfaceScan, ok: true},
		{name: "tractography", uid: "1.2.840.10008.5.1.4.1.1.66.6", want: RecordTypeTract, ok: true},
		{name: "annotation", uid: "1.2.840.10008.5.1.4.1.1.91.1", want: RecordTypeAnnotation, ok: true},
		{name: "segmentation", uid: "1.2.840.10008.5.1.4.1.1.66.4", want: RecordTypeImage, ok: true},
		{name: "label map segmentation", uid: "1.2.840.10008.5.1.4.1.1.66.7", want: RecordTypeImage, ok: true},
		{name: "height map segmentation", uid: "1.2.840.10008.5.1.4.1.1.66.8", want: RecordTypeImage, ok: true},
		{name: "parametric map", uid: "1.2.840.10008.5.1.4.1.1.30", want: RecordTypeImage, ok: true},
		{name: "enhanced US volume", uid: "1.2.840.10008.5.1.4.1.1.6.2", want: RecordTypeImage, ok: true},
		{name: "ophthalmic thickness map", uid: "1.2.840.10008.5.1.4.1.1.81.1", want: RecordTypeImage, ok: true},
		{name: "corneal topography map", uid: "1.2.840.10008.5.1.4.1.1.82.1", want: RecordTypeImage, ok: true},
		{name: "ophthalmic OCT B-scan volume analysis", uid: "1.2.840.10008.5.1.4.1.1.77.1.5.8", want: RecordTypeImage, ok: true},
		{name: "padded UID", uid: "1.2.840.10008.5.1.4.1.1.2 \x00", want: RecordTypeImage, ok: true},
		{name: "known non-storage SOP class", uid: "1.2.840.10008.1.1"},
		{name: "known unsupported storage SOP class", uid: "1.2.840.10008.5.1.4.38.1"},
		{name: "unknown image-like UID", uid: "1.2.840.10008.5.1.4.1.1.999.1"},
		{name: "empty UID"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := recordTypeForSOPClass(tt.uid)
			if ok != tt.ok || got != tt.want {
				t.Fatalf("recordTypeForSOPClass() = (%q, %v), want (%q, %v)", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestRecordTypeForEveryImageStorageInUIDInventory(t *testing.T) {
	for _, entry := range uid.EntriesByType(uid.SOPClass) {
		if !strings.Contains(entry.Keyword, "ImageStorage") {
			continue
		}
		got, ok := recordTypeForSOPClass(entry.UID)
		if !ok || got != RecordTypeImage {
			t.Errorf("%s (%s) = (%q, %v), want (%q, true)", entry.Keyword, entry.UID, got, ok, RecordTypeImage)
		}
	}
}
