package dicom

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadFileWithOptionsDelegatesToObjectLayer(t *testing.T) {
	data, err := dicomtest.Part10File(
		transfer.ExplicitVRLittleEndian,
		append(dicomtest.MinimalDataset(), dicomtest.NewOBElement(core.TagPixelData, []byte{0x01, 0x02, 0x03, 0x04}))...,
	)
	if err != nil {
		t.Fatal(err)
	}

	_, err = ReadFileWithOptions(bytes.NewReader(data), ReadFileOptions{
		MaxElementBytes: 2,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, parser.ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}
}
