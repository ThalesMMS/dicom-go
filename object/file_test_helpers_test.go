package object

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
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
func buildDeflatedPart10File(t *testing.T, dataset ...core.Element) []byte {
	t.Helper()

	return buildDeflatedPart10FileWithTransferSyntax(t, transfer.DeflatedExplicitVRLittleEndian, dataset...)
}
func buildDeflatedPart10FileWithTransferSyntax(t *testing.T, syntax transfer.Syntax, dataset ...core.Element) []byte {
	t.Helper()

	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(syntax.UID).Encode())
	buf.Write(deflateBytes(t, dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dataset...)))
	return buf.Bytes()
}
func deflateBytes(t *testing.T, data []byte) []byte {
	t.Helper()

	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
func inflateBytes(t *testing.T, data []byte) []byte {
	t.Helper()

	r := flate.NewReader(bytes.NewReader(data))
	defer func() { _ = r.Close() }()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return got
}
func buildMalformedPart10File(meta, dataset []byte) []byte {
	var buf bytes.Buffer
	buf.Write(make([]byte, 128))
	buf.WriteString("DICM")
	buf.Write(meta)
	buf.Write(dataset)
	return buf.Bytes()
}
func videoMediaDataset(fragments ...[]byte) []core.Element {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	out := elements[:0]
	for _, elem := range elements {
		switch elem.Tag() {
		case core.TagPixelData,
			core.NewTag(0x0028, 0x0002), // SamplesPerPixel
			core.NewTag(0x0028, 0x0004): // PhotometricInterpretation
			continue
		default:
			out = append(out, elem)
		}
	}
	out = append(out,
		dicomtest.NewUShortElement(core.NewTag(0x0028, 0x0002), 3),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, "YBR_PARTIAL_420"),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1"),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...),
	)
	return out
}
func jpegXLDataset(fragments ...[]byte) []core.Element {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	out := elements[:0]
	for _, elem := range elements {
		if elem.Tag() == core.TagPixelData {
			continue
		}
		out = append(out, elem)
	}
	return append(out, dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...))
}
func encapsulatedStillImageDataset(fragments ...[]byte) []core.Element {
	elements := append([]core.Element(nil), dicomtest.MinimalDataset()...)
	out := elements[:0]
	for _, elem := range elements {
		if elem.Tag() == core.TagPixelData {
			continue
		}
		out = append(out, elem)
	}
	return append(out, dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragments...))
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
