package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"testing"
)

func buildPart10FileWithTransferSyntaxUID(uid string) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(uid).Encode())
	buf.Write(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...))
	return buf.Bytes()
}
func buildMalformedPart10File(meta, dataset []byte) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(meta)
	buf.Write(dataset)
	return buf.Bytes()
}
func requireMinimalFile(t *testing.T, file *File) {
	t.Helper()

	if file.Meta == nil {
		t.Fatal("expected file meta object to be populated")
	}
	if file.Dataset == nil {
		t.Fatal("expected dataset object to be populated")
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0010)); !ok || got != "TEST^PATIENT" {
		t.Fatalf("unexpected patient name: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0010, 0x0020)); !ok || got != "TESTID001" {
		t.Fatalf("unexpected patient id: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("unexpected SOP instance uid: %q ok=%v", got, ok)
	}
	if got, ok := file.Dataset.GetString(core.NewTag(0x0020, 0x000D)); !ok || got != dicomtest.TestStudyInstanceUID {
		t.Fatalf("unexpected study instance uid: %q ok=%v", got, ok)
	}
	if got := file.TransferSyntax.UID; got != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("unexpected transfer syntax uid: %q", got)
	}
}
func requireRawDataSetWithoutFileMeta(t *testing.T, obj *Object) {
	t.Helper()

	if got, ok := obj.GetString(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("unexpected SOP instance uid: %q ok=%v", got, ok)
	}
	if obj.Has(tagTransferSyntaxUID) {
		t.Fatal("raw dataset reader should not synthesize TransferSyntaxUID file meta")
	}
}
func requireUnsupportedTransferSyntax(t *testing.T, err error) {
	t.Helper()

	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func assertGroupLengthMatchesEncodedMeta(t *testing.T, meta *Object) {
	t.Helper()
	raw, ok := meta.GetRaw(tagFileMetaInformationGroupLength)
	if !ok || len(raw) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", raw, ok)
	}
	got := binary.LittleEndian.Uint32(raw)

	var rest []core.Element
	for _, el := range meta.SortedElements() {
		if el.Tag() == tagFileMetaInformationGroupLength {
			continue
		}
		rest = append(rest, el)
	}
	want := uint32(len(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, rest...)))
	if got != want {
		t.Fatalf("group length = %d, want %d", got, want)
	}
}
