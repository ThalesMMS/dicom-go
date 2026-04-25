package object

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"testing"
)

func TestWriteFileRoundTripDerivesMetaAndUsesExplicitLEFileMeta(t *testing.T) {
	dataset := core.DataSet{Elements: dicomtest.MinimalDataset()}
	file := &File{
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if len(data) < 140 {
		t.Fatalf("encoded Part 10 length = %d, want at least 140", len(data))
	}
	if !bytes.Equal(data[:128], make([]byte, 128)) {
		t.Fatal("default preamble should be zero-filled")
	}
	if got := string(data[128:132]); got != "DICM" {
		t.Fatalf("magic marker = %q, want %q", got, "DICM")
	}
	wantMetaPrefix := []byte{0x02, 0x00, 0x00, 0x00, 'U', 'L', 0x04, 0x00}
	if !bytes.Equal(data[132:140], wantMetaPrefix) {
		t.Fatalf("file meta prefix = % X, want % X", data[132:140], wantMetaPrefix)
	}

	roundTrip, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.TransferSyntax.UID; got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax uid = %q, want %q", got, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
		t.Fatalf("implementation class uid = %q ok=%v, want %q", got, ok, implementationClassUID)
	}
	if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), dataset); diff != "" {
		t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
	}

	meta, _, datasetOffset, err := readFileMeta(bytes.NewReader(data[132:]), 132, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	rawGroupLength, ok := meta.GetRaw(tagFileMetaInformationGroupLength)
	if !ok || len(rawGroupLength) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", rawGroupLength, ok)
	}
	gotGroupLength := binary.LittleEndian.Uint32(rawGroupLength)
	wantGroupLength := uint32(datasetOffset - 132 - 12)
	if gotGroupLength != wantGroupLength {
		t.Fatalf("file meta group length = %d, want %d", gotGroupLength, wantGroupLength)
	}
}
func TestWriteFileWithOptionsPreservesCustomPreamble(t *testing.T) {
	preamble := bytes.Repeat([]byte{0x5A}, 128)
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, file, WriteFileOptions{Preamble: preamble}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes()[:128], preamble) {
		t.Fatal("custom preamble was not preserved in output")
	}

	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(roundTrip.Preamble, preamble) {
		t.Fatal("custom preamble was not preserved after round-trip")
	}
}
func TestWriteFilePreservesExistingFilePreambleWhenOptionsOmitIt(t *testing.T) {
	preamble := bytes.Repeat([]byte{0x41}, 128)
	file := &File{
		Preamble:       preamble,
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(buf.Bytes()[:128], preamble) {
		t.Fatal("existing file preamble was not preserved")
	}
}
func TestWriteFileRejectsInvalidPreambleLength(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err := WriteFileWithOptions(io.Discard, file, WriteFileOptions{Preamble: []byte{0x00}})
	if !errors.Is(err, ErrInvalidPreambleLength) {
		t.Fatalf("expected ErrInvalidPreambleLength, got %v", err)
	}
}
func TestWriteFileInjectsResolvedSOPUIDsIntoDataset(t *testing.T) {
	dataset := core.DataSet{
		Elements: []core.Element{
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST^PATIENT"),
			dicomtest.NewStringElement(core.NewTag(0x0010, 0x0020), core.VRLO, "TESTID001"),
		},
	}
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, dicomtest.TestSOPClassUID),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, dicomtest.TestSOPInstanceUID),
		}, std.Dictionary),
		Dataset:        FromDataSet(dataset, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roundTrip.Dataset.GetString(tagSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("dataset SOPClassUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := roundTrip.Dataset.GetString(tagSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("dataset SOPInstanceUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
}
func TestWriteFileReconcilesMetaWithDatasetAndTransferSyntax(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.Uint32Element(tagFileMetaInformationGroupLength, core.VRUL, binary.LittleEndian, 1),
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, "9.8.7"),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, "9.8.7.6"),
			dicomtest.NewUIElement(tagTransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}

	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("media storage SOP class uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("media storage SOP instance uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
}
func TestPrepareFileMetaIgnoresNonFileMetaElements(t *testing.T) {
	meta, err := prepareFileMeta(&File{
		Meta: FromElements([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0002, 0x0013), core.VRSH, "DICOMGO_TEST"),
			dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SHOULD^NOT^MOVE"),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	metaObj := FromElements(meta, std.Dictionary)
	if metaObj.Has(core.NewTag(0x0010, 0x0010)) {
		t.Fatal("prepareFileMeta should ignore non-(0002,xxxx) elements from file.Meta")
	}
	assertGroupLengthMatchesEncodedMeta(t, metaObj)
}
func TestWriteFileRejectsMissingRequiredUIDs(t *testing.T) {
	datasetWithoutSOPClass := append([]core.Element(nil), dicomtest.MinimalDataset()[1:]...)
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: datasetWithoutSOPClass}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err := WriteFile(io.Discard, file)
	if !errors.Is(err, ErrMissingSOPClassUID) {
		t.Fatalf("expected ErrMissingSOPClassUID, got %v", err)
	}

	datasetWithoutSOPInstance := append([]core.Element(nil), dicomtest.MinimalDataset()[:3]...)
	file = &File{
		Dataset:        FromDataSet(core.DataSet{Elements: datasetWithoutSOPInstance}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err = WriteFile(io.Discard, file)
	if !errors.Is(err, ErrMissingSOPInstanceUID) {
		t.Fatalf("expected ErrMissingSOPInstanceUID, got %v", err)
	}
}
func TestWriteFileRejectsDeflatedTransferSyntax(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.DeflatedExplicitVRLittleEndian,
	}

	err := WriteFile(io.Discard, file)
	if !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func TestResolveWriteTransferSyntaxReturnsCanonicalSyntax(t *testing.T) {
	got, err := resolveWriteTransferSyntax(&File{
		TransferSyntax: transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != transfer.ExplicitVRBigEndian {
		t.Fatalf("resolved syntax = %#v, want %#v", got, transfer.ExplicitVRBigEndian)
	}
}
func TestWriteFileRoundTrip(t *testing.T) {
	tests := []struct {
		name     string
		syntax   transfer.Syntax
		preamble []byte
	}{
		{name: "explicit_le_zero_preamble", syntax: transfer.ExplicitVRLittleEndian},
		{name: "explicit_le_custom_preamble", syntax: transfer.ExplicitVRLittleEndian, preamble: bytes.Repeat([]byte{0x7A}, 128)},
		{name: "implicit_le_zero_preamble", syntax: transfer.ImplicitVRLittleEndian},
		{name: "implicit_le_custom_preamble", syntax: transfer.ImplicitVRLittleEndian, preamble: bytes.Repeat([]byte{0x33}, 128)},
	}

	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			file := &File{
				Dataset:        FromDataSet(want, std.Dictionary),
				TransferSyntax: tt.syntax,
			}

			var buf bytes.Buffer
			if err := WriteFileWithOptions(&buf, file, WriteFileOptions{Preamble: tt.preamble}); err != nil {
				t.Fatal(err)
			}
			data := buf.Bytes()

			wantPreamble := tt.preamble
			if wantPreamble == nil {
				wantPreamble = make([]byte, 128)
			}
			if !bytes.Equal(data[:128], wantPreamble) {
				t.Fatalf("preamble mismatch: got % X want % X", data[:128], wantPreamble)
			}
			if got := string(data[128:132]); got != "DICM" {
				t.Fatalf("magic marker = %q, want %q", got, "DICM")
			}

			roundTrip, err := ReadFile(bytes.NewReader(data))
			if err != nil {
				t.Fatal(err)
			}
			if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), want); diff != "" {
				t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
			}
			if got := roundTrip.TransferSyntax.UID; got != tt.syntax.UID {
				t.Fatalf("transfer syntax uid = %q, want %q", got, tt.syntax.UID)
			}
			if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
				t.Fatalf("media storage SOP class uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
			}
			if got, ok := roundTrip.Meta.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
				t.Fatalf("media storage SOP instance uid = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
			}
			if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != tt.syntax.UID {
				t.Fatalf("transfer syntax uid in file meta = %q ok=%v, want %q", got, ok, tt.syntax.UID)
			}
			if got, ok := roundTrip.Meta.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
				t.Fatalf("implementation class uid = %q ok=%v, want %q", got, ok, implementationClassUID)
			}
			assertGroupLengthMatchesEncodedMeta(t, roundTrip.Meta)
		})
	}
}
func TestPrepareFileMetaDerivesMissingElements(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0002, 0x0013), core.VRSH, "DICOMGO_TEST"),
		}, std.Dictionary),
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ImplicitVRLittleEndian,
	}

	meta, err := prepareFileMeta(file, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	metaObj := FromElements(meta, std.Dictionary)
	if got, ok := metaObj.GetString(tagMediaStorageSOPClassUID); !ok || got != dicomtest.TestSOPClassUID {
		t.Fatalf("derived MediaStorageSOPClassUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPClassUID)
	}
	if got, ok := metaObj.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("derived MediaStorageSOPInstanceUID = %q ok=%v, want %q", got, ok, dicomtest.TestSOPInstanceUID)
	}
	if got, ok := metaObj.GetString(tagTransferSyntaxUID); !ok || got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("derived TransferSyntaxUID = %q ok=%v, want %q", got, ok, transfer.ImplicitVRLittleEndian.UID)
	}
	if got, ok := metaObj.GetString(tagImplementationClassUID); !ok || got != implementationClassUID {
		t.Fatalf("derived ImplementationClassUID = %q ok=%v, want %q", got, ok, implementationClassUID)
	}
	versionRaw, ok := metaObj.GetRaw(tagFileMetaInformationVersion)
	if !ok || !bytes.Equal(versionRaw, []byte{0x00, 0x01}) {
		t.Fatalf("FileMetaInformationVersion raw = %v ok=%v, want [0 1]", versionRaw, ok)
	}
	assertGroupLengthMatchesEncodedMeta(t, metaObj)
}
func TestPrepareFileMetaReturnsErrorsWhenMandatoryValuesCannotBeResolved(t *testing.T) {
	tests := []struct {
		name string
		file *File
		want error
	}{
		{
			name: "missing SOP class",
			file: &File{
				Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()[1:]}, std.Dictionary),
				TransferSyntax: transfer.ExplicitVRLittleEndian,
			},
			want: ErrMissingSOPClassUID,
		},
		{
			name: "missing SOP instance",
			file: &File{
				Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()[:3]}, std.Dictionary),
				TransferSyntax: transfer.ExplicitVRLittleEndian,
			},
			want: ErrMissingSOPInstanceUID,
		},
		{
			name: "missing transfer syntax",
			file: &File{
				Dataset: FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
			},
			want: ErrMissingTransferSyntax,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := WriteFile(io.Discard, tt.file)
			if !errors.Is(err, tt.want) {
				t.Fatalf("expected %v, got %v", tt.want, err)
			}
		})
	}
}
func TestPrepareFileMetaCalculatesKnownGroupLength(t *testing.T) {
	meta, err := prepareFileMeta(&File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}

	if len(meta) == 0 || meta[0].Tag() != tagFileMetaInformationGroupLength {
		t.Fatalf("first meta element = %v, want %v", meta[0].Tag(), tagFileMetaInformationGroupLength)
	}
	raw, ok := meta[0].RawBytes()
	if !ok || len(raw) != 4 {
		t.Fatalf("group length raw bytes = %v ok=%v, want 4-byte raw value", raw, ok)
	}
	got := binary.LittleEndian.Uint32(raw)
	want := uint32(len(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, meta[1:]...)))
	if got != want {
		t.Fatalf("group length = %d, want %d", got, want)
	}
}
func TestWriteFileAlwaysEncodesFileMetaInExplicitVRLittleEndian(t *testing.T) {
	file := &File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRBigEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	if got := data[132:140]; !bytes.Equal(got, []byte{0x02, 0x00, 0x00, 0x00, 'U', 'L', 0x04, 0x00}) {
		t.Fatalf("file meta first header = % X, want Explicit VR Little Endian UL header", got)
	}

	meta, _, datasetOffset, err := readFileMeta(bytes.NewReader(data[132:]), 132, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.ExplicitVRBigEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.ExplicitVRBigEndian.UID)
	}
	wantDatasetPrefix := dicomtest.EncodeElement(dicomtest.NewUIElement(tagSOPClassUID, dicomtest.TestSOPClassUID), transfer.ExplicitVRBigEndian)[:8]
	gotDatasetPrefix := data[datasetOffset : datasetOffset+8]
	if !bytes.Equal(gotDatasetPrefix, wantDatasetPrefix) {
		t.Fatalf("dataset first header = % X, want % X", gotDatasetPrefix, wantDatasetPrefix)
	}
}
func TestWriteFileRoundTripWithNestedSequences(t *testing.T) {
	seqTag := core.NewTag(0x0008, 0x1111)
	nestedSeqTag := core.NewTag(0x0008, 0x1115)
	want := core.DataSet{
		Elements: append([]core.Element{}, dicomtest.MinimalDataset()...),
	}
	want.Elements = append(want.Elements, dicomtest.NewSequenceElement(
		seqTag,
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SEQ^ONE"),
				dicomtest.NewSequenceElement(
					nestedSeqTag,
					core.DataSet{
						Elements: []core.Element{
							dicomtest.NewUIElement(core.NewTag(0x0008, 0x1150), dicomtest.TestSOPClassUID),
						},
					},
				),
			},
		},
		core.DataSet{
			Elements: []core.Element{
				dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "SEQ^TWO"),
			},
		},
	))

	file := &File{
		Dataset:        FromDataSet(want, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if diff := dicomtest.DiffDataSet(got.Dataset.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after sequence round-trip:\n%s", diff)
	}
}
func TestWriteFileRejectsUnsupportedEncapsulatedTransferSyntax(t *testing.T) {
	pixel := dicomtest.NewFragmentSequenceElement(
		core.TagPixelData,
		[]byte{0x00, 0x00, 0x00, 0x00},
		[]byte{0xFF, 0xD8, 0xFF, 0xD9},
		[]byte{0x01, 0x02, 0x03, 0x00},
	)
	want := core.DataSet{Elements: append(dicomtest.MinimalDataset(), pixel)}
	file := &File{
		Dataset:        FromDataSet(want, std.Dictionary),
		TransferSyntax: transfer.JPEGBaseline,
	}

	if err := WriteFile(io.Discard, file); !errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		t.Fatalf("expected ErrUnsupportedTransferSyntax, got %v", err)
	}
}
func TestWriteDataSetProducesRawDatasetOutput(t *testing.T) {
	wantElements := dicomtest.MinimalDataset()
	obj := FromDataSet(core.DataSet{Elements: wantElements}, std.Dictionary)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, wantElements...)
	got := buf.Bytes()
	if !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
	if len(got) >= 132 && bytes.Equal(got[:128], make([]byte, 128)) && string(got[128:132]) == "DICM" {
		t.Fatal("WriteDataSet should not write a Part 10 preamble or DICM marker")
	}
	if bytes.Contains(got, []byte("DICM")) {
		t.Fatal("WriteDataSet output should not contain the Part 10 marker")
	}
}
func TestWriteDataSetRejectsNilObject(t *testing.T) {
	err := WriteDataSet(io.Discard, nil, transfer.ExplicitVRLittleEndian)
	if err == nil {
		t.Fatal("expected error")
	}
	if got := err.Error(); got != "dicom: nil object passed to WriteDataSet" {
		t.Fatalf("error = %q, want %q", got, "dicom: nil object passed to WriteDataSet")
	}
}
func TestWriteDataSetCanonicalizesRegisteredUIDOnlySyntax(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.Syntax{UID: transfer.ExplicitVRBigEndian.UID}); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, dicomtest.MinimalDataset()...)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
}
func TestWriteDataSetRoundTripAcrossTransferSyntaxes(t *testing.T) {
	tests := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	}

	want := core.DataSet{Elements: dicomtest.MinimalDataset()}
	obj := FromDataSet(want, std.Dictionary)
	for _, syntax := range tests {
		t.Run(syntax.UID, func(t *testing.T) {
			var buf bytes.Buffer
			if err := WriteDataSet(&buf, obj, syntax); err != nil {
				t.Fatal(err)
			}
			got, err := ReadDataSet(bytes.NewReader(buf.Bytes()), syntax)
			if err != nil {
				t.Fatal(err)
			}
			if diff := dicomtest.DiffDataSet(got.ToDataSet(), want); diff != "" {
				t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
			}
		})
	}
}
func TestWriteDataSetAcceptsCallerProvidedUnknownSyntaxWithHints(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	var buf bytes.Buffer
	err := WriteDataSet(&buf, obj, transfer.Syntax{
		UID:        "9.8.7.6.5",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Supported:  false,
	})
	if err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
}
func TestWriteDataSetRejectsCallerProvidedUnknownDeflatedSyntax(t *testing.T) {
	obj := FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary)

	err := WriteDataSet(io.Discard, obj, transfer.Syntax{
		UID:        "9.8.7.6.5",
		ExplicitVR: true,
		ByteOrder:  binary.LittleEndian,
		Deflated:   true,
	})
	if !errors.Is(err, transfer.ErrUnknownTransferSyntax) {
		t.Fatalf("expected ErrUnknownTransferSyntax, got %v", err)
	}
}
