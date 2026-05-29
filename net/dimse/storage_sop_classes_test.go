package dimse

import (
	"testing"

	"github.com/ThalesMMS/dicom-go/seg"
	"github.com/ThalesMMS/dicom-go/waveform"
)

func TestDefaultStorageSOPClassUIDsIncludesCurrentStorageClasses(t *testing.T) {
	got := DefaultStorageSOPClassUIDs()
	for _, uid := range []string{
		"1.2.840.10008.5.1.4.1.1.7", // Secondary Capture Image Storage
		seg.SegmentationStorage,
		waveform.SupportedStorageSOPClassUIDs()[0],
	} {
		if !containsUID(got, uid) {
			t.Fatalf("DefaultStorageSOPClassUIDs() missing %q: %#v", uid, got)
		}
	}
}

func TestDefaultStorageSOPClassUIDsReturnsCopy(t *testing.T) {
	first := DefaultStorageSOPClassUIDs()
	if len(first) == 0 {
		t.Fatal("DefaultStorageSOPClassUIDs() returned no UIDs")
	}
	first[0] = "mutated"

	second := DefaultStorageSOPClassUIDs()
	if second[0] == "mutated" {
		t.Fatal("DefaultStorageSOPClassUIDs() returned shared backing storage")
	}
}

func containsUID(uids []string, uid string) bool {
	for _, got := range uids {
		if got == uid {
			return true
		}
	}
	return false
}
