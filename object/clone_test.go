package object

import (
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCloneFileCopiesFileWithoutSharingDataset(t *testing.T) {
	src := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	clone, err := CloneFile(src)
	if err != nil {
		t.Fatalf("CloneFile() error = %v", err)
	}
	clone.Dataset.Put(core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("CLONE^PATIENT")))

	if got, _ := src.Dataset.GetString(core.NewTag(0x0010, 0x0010)); got == "CLONE^PATIENT" {
		t.Fatal("CloneFile() shared dataset storage with source")
	}
}

func TestCloneFileRejectsNilFile(t *testing.T) {
	_, err := CloneFile(nil)
	if !errors.Is(err, ErrNilFile) {
		t.Fatalf("CloneFile(nil) error = %v, want ErrNilFile", err)
	}
}
