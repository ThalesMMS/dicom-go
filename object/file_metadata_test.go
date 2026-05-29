package object

import (
	"bytes"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestFileMetadataReturnsCommonPart10Fields(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(),
			dicomtest.NewStringElement(core.NewTag(0x0008, 0x0060), core.VRCS, "CT"),
			dicomtest.NewStringElement(core.NewTag(0x0008, 0x0050), core.VRSH, "ACC-1"),
			dicomtest.NewStringElement(core.NewTag(0x0020, 0x0013), core.VRIS, "7"),
			dicomtest.NewStringElement(core.NewTag(0x0008, 0x1030), core.VRLO, "Study"),
		)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	file, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}

	got := file.Metadata()
	if got.TransferSyntaxUID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntaxUID = %q, want %q", got.TransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID)
	}
	if got.TransferSyntaxName != transfer.ExplicitVRLittleEndian.Name {
		t.Fatalf("TransferSyntaxName = %q, want %q", got.TransferSyntaxName, transfer.ExplicitVRLittleEndian.Name)
	}
	if got.PatientName != "TEST^PATIENT" || got.PatientID != "TESTID001" {
		t.Fatalf("patient metadata = name %q id %q", got.PatientName, got.PatientID)
	}
	if got.SOPClassUID != dicomtest.TestSOPClassUID || got.SOPInstanceUID != dicomtest.TestSOPInstanceUID {
		t.Fatalf("SOP metadata = class %q instance %q", got.SOPClassUID, got.SOPInstanceUID)
	}
	if got.Modality != "CT" || got.AccessionNumber != "ACC-1" || got.InstanceNumber != "7" || got.StudyDescription != "Study" {
		t.Fatalf("selected metadata = %#v", got)
	}
}

func TestFileMetadataNilFileReturnsZeroValue(t *testing.T) {
	var file *File
	if got := file.Metadata(); got != (FileMetadata{}) {
		t.Fatalf("nil Metadata() = %#v, want zero value", got)
	}
}
