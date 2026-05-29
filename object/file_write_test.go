package object

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"strings"
	"testing"
)

func TestWriteFileRoundTripDerivesMetaAndUsesExplicitLEFileMeta(t *testing.T) {
	dataset := canonicalMinimalDataSet()
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

	meta, _, datasetOffset, err := readFileMeta(bufio.NewReader(bytes.NewReader(data[132:])), 132, ReadFileOptions{})
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

func TestWriteFileReportsBufferedFlushError(t *testing.T) {
	wantErr := errors.New("write failed")
	file := &File{
		Dataset:        FromDataSet(canonicalMinimalDataSet(), std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	err := WriteFile(errorWriter{err: wantErr}, file)
	if !errors.Is(err, wantErr) {
		t.Fatalf("WriteFile() error = %v, want %v", err, wantErr)
	}
}

type errorWriter struct {
	err error
}

func (w errorWriter) Write([]byte) (int, error) {
	return 0, w.err
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

func TestWriteFileCanPreserveAbsentDataSetSOPUIDsForBasicDirectoryIOD(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, "1.2.840.10008.1.3.10"),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, dicomtest.TestSOPInstanceUID),
		}, std.Dictionary),
		Dataset: FromElements([]core.Element{
			dicomtest.NewStringElement(core.NewTag(0x0004, 0x1130), core.VRCS, "TESTSET"),
		}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, file, WriteFileOptions{OmitReconciledDataSetSOPUIDs: true}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if roundTrip.Dataset.Has(tagSOPClassUID) || roundTrip.Dataset.Has(tagSOPInstanceUID) {
		t.Fatal("write option injected SOP Common UIDs into the Basic Directory dataset")
	}
	if got, ok := roundTrip.Meta.GetUID(tagMediaStorageSOPClassUID); !ok || got != "1.2.840.10008.1.3.10" {
		t.Fatalf("MediaStorageSOPClassUID = %q, %v", got, ok)
	}
	if got, ok := roundTrip.Meta.GetUID(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("MediaStorageSOPInstanceUID = %q, %v", got, ok)
	}
}

func TestWriteFileRejectsOmittedDataSetSOPUIDsOutsideBasicDirectoryIOD(t *testing.T) {
	file := &File{
		Meta: FromElements([]core.Element{
			dicomtest.NewUIElement(tagMediaStorageSOPClassUID, dicomtest.TestSOPClassUID),
			dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, dicomtest.TestSOPInstanceUID),
		}, std.Dictionary),
		Dataset:        New(std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, file, WriteFileOptions{OmitReconciledDataSetSOPUIDs: true}); err == nil {
		t.Fatal("WriteFileWithOptions accepted omitted SOP Common UIDs outside the Basic Directory IOD")
	}
	if buf.Len() != 0 {
		t.Fatalf("failed validation wrote %d bytes", buf.Len())
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

func TestRebuildFileMeta(t *testing.T) {
	tests := []struct {
		name    string
		file    *File
		wantErr error
		check   func(*testing.T, *File)
	}{
		{
			name: "synchronizes current dataset and transfer syntax",
			file: &File{
				Meta: FromElements([]core.Element{
					dicomtest.NewUIElement(tagMediaStorageSOPClassUID, "9.8.7"),
					dicomtest.NewUIElement(tagMediaStorageSOPInstanceUID, "9.8.7.6"),
					dicomtest.NewUIElement(tagTransferSyntaxUID, transfer.ExplicitVRLittleEndian.UID),
				}, std.Dictionary),
				Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
				TransferSyntax: transfer.ImplicitVRLittleEndian,
			},
			check: func(t *testing.T, file *File) {
				assertUID := func(tag core.Tag, want string) {
					t.Helper()
					if got, ok := file.Meta.GetString(tag); !ok || got != want {
						t.Fatalf("file meta %s = %q, ok=%v; want %q", tag, got, ok, want)
					}
				}
				assertUID(tagMediaStorageSOPClassUID, dicomtest.TestSOPClassUID)
				assertUID(tagMediaStorageSOPInstanceUID, dicomtest.TestSOPInstanceUID)
				assertUID(tagTransferSyntaxUID, transfer.ImplicitVRLittleEndian.UID)
				if file.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
					t.Fatalf("TransferSyntax = %q; want %q", file.TransferSyntax.UID, transfer.ImplicitVRLittleEndian.UID)
				}
			},
		},
		{name: "rejects nil file", wantErr: ErrNilFile},
		{
			name: "rejects missing SOP class",
			file: &File{
				Dataset: FromElements([]core.Element{
					dicomtest.NewUIElement(tagSOPInstanceUID, dicomtest.TestSOPInstanceUID),
				}, std.Dictionary),
				TransferSyntax: transfer.ImplicitVRLittleEndian,
			},
			wantErr: ErrMissingSOPClassUID,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.file.RebuildFileMeta()
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("RebuildFileMeta() error = %v, want %v", err, tt.wantErr)
			}
			if tt.check != nil {
				tt.check(t, tt.file)
			}
		})
	}
}
func TestPrepareFileMetaIgnoresNonFileMetaElements(t *testing.T) {
	prepared, err := prepareFileMeta(&File{
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
	meta := prepared.elements

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
func TestWriteFileRoundTripDeflatedExplicitVRLittleEndian(t *testing.T) {
	want := canonicalMinimalDataSet()
	file := &File{
		Dataset:        FromDataSet(want, std.Dictionary),
		TransferSyntax: transfer.DeflatedExplicitVRLittleEndian,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	data := buf.Bytes()

	roundTrip, err := ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.TransferSyntax.UID; got != transfer.DeflatedExplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax uid = %q, want %q", got, transfer.DeflatedExplicitVRLittleEndian.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != transfer.DeflatedExplicitVRLittleEndian.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, transfer.DeflatedExplicitVRLittleEndian.UID)
	}
	if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), want); diff != "" {
		t.Fatalf("dataset mismatch after deflated round-trip:\n%s", diff)
	}

	_, _, datasetOffset, err := readFileMeta(bufio.NewReader(bytes.NewReader(data[132:])), 132, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantDataSetBytes := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, want.Elements...)
	if bytes.Equal(data[datasetOffset:], wantDataSetBytes) {
		t.Fatal("deflated Part 10 dataset was written without compression")
	}
	if got := inflateBytes(t, data[datasetOffset:]); !bytes.Equal(got, wantDataSetBytes) {
		t.Fatalf("inflated dataset bytes = % X, want % X", got, wantDataSetBytes)
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

	want := canonicalMinimalDataSet()
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

	prepared, err := prepareFileMeta(file, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	meta := prepared.elements

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

func TestWriteFileWithDefaultMissingTransferSyntax(t *testing.T) {
	assertMissingTransferSyntaxRoundTrip(t,
		WriteFileOptions{DefaultMissingTransferSyntax: true},
		transfer.ImplicitVRLittleEndian,
	)
}

func TestWriteFileWithOverrideMissingTransferSyntax(t *testing.T) {
	tests := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
	}

	for _, syntax := range tests {
		t.Run(syntax.UID, func(t *testing.T) {
			assertMissingTransferSyntaxRoundTrip(t,
				WriteFileOptions{OverrideMissingTransferSyntax: syntax.UID},
				syntax,
			)
		})
	}
}

func TestWriteFileRejectsInvalidMissingTransferSyntaxOverride(t *testing.T) {
	err := WriteFileWithOptions(io.Discard, fileWithoutWriteTransferSyntax(), WriteFileOptions{
		OverrideMissingTransferSyntax: "not-a-transfer-syntax",
	})
	if !errors.Is(err, transfer.ErrUnknownTransferSyntax) {
		t.Fatalf("expected ErrUnknownTransferSyntax, got %v", err)
	}
	if err == nil || !strings.Contains(err.Error(), "not-a-transfer-syntax") {
		t.Fatalf("error = %v, want invalid UID in message", err)
	}
}

func TestWriteFileMissingTransferSyntaxOptionsDoNotOverridePresentSyntax(t *testing.T) {
	file := fileWithoutWriteTransferSyntax()
	file.TransferSyntax = transfer.ImplicitVRLittleEndian

	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, file, WriteFileOptions{
		OverrideMissingTransferSyntax: transfer.ExplicitVRBigEndian.UID,
	}); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.TransferSyntax.UID; got != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("transfer syntax UID = %q, want existing %q", got, transfer.ImplicitVRLittleEndian.UID)
	}
}

func TestPrepareFileMetaCalculatesKnownGroupLength(t *testing.T) {
	prepared, err := prepareFileMeta(&File{
		Dataset:        FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err)
	}
	meta := prepared.elements

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
	wantEncoded := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, meta...)
	if !bytes.Equal(prepared.encoded, wantEncoded) {
		t.Fatalf("prepared encoded meta differs from encoding prepared elements")
	}
}

func fileWithoutWriteTransferSyntax() *File {
	return &File{
		Dataset: FromDataSet(core.DataSet{Elements: dicomtest.MinimalDataset()}, std.Dictionary),
	}
}

func assertMissingTransferSyntaxRoundTrip(t *testing.T, opts WriteFileOptions, wantSyntax transfer.Syntax) {
	t.Helper()

	var buf bytes.Buffer
	if err := WriteFileWithOptions(&buf, fileWithoutWriteTransferSyntax(), opts); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got := roundTrip.TransferSyntax.UID; got != wantSyntax.UID {
		t.Fatalf("transfer syntax UID = %q, want %q", got, wantSyntax.UID)
	}
	if got, ok := roundTrip.Meta.GetString(tagTransferSyntaxUID); !ok || got != wantSyntax.UID {
		t.Fatalf("file meta transfer syntax = %q ok=%v, want %q", got, ok, wantSyntax.UID)
	}
	wantDataset := canonicalMinimalDataSet()
	if diff := dicomtest.DiffDataSet(roundTrip.Dataset.ToDataSet(), wantDataset); diff != "" {
		t.Fatalf("dataset mismatch after round-trip:\n%s", diff)
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

	meta, _, datasetOffset, err := readFileMeta(bufio.NewReader(bytes.NewReader(data[132:])), 132, ReadFileOptions{})
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
	sortElementsByTag(want.Elements)

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
func TestWriteFileRoundTripPreservesOptionalStillImageCodecPayloads(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0x4F, 0xFF, 0x51},
		{0x01, 0x02, 0x03, 0x00},
	}
	for _, syntax := range []transfer.Syntax{
		transfer.JPEGLSLossless,
		transfer.JPEGLSNearLossless,
		transfer.JPEG2000LosslessOnly,
		transfer.JPEG2000,
		transfer.HTJ2KLossless,
		transfer.HTJ2K,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			file := &File{
				Dataset:        FromElements(encapsulatedStillImageDataset(wantFragments...), std.Dictionary),
				TransferSyntax: syntax,
			}

			var buf bytes.Buffer
			if err := WriteFile(&buf, file); err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if got.TransferSyntax.UID != syntax.UID {
				t.Fatalf("transfer syntax = %q, want %q", got.TransferSyntax.UID, syntax.UID)
			}
			elem, ok := got.Dataset.Get(core.TagPixelData)
			if !ok {
				t.Fatal("missing Pixel Data")
			}
			value, ok := elem.Value.(core.FragmentSequence)
			if !ok {
				t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
			}
			if len(value.Fragments) != len(wantFragments) {
				t.Fatalf("fragments = %d, want %d", len(value.Fragments), len(wantFragments))
			}
			for i := range wantFragments {
				if !bytes.Equal(value.Fragments[i], wantFragments[i]) {
					t.Fatalf("fragment %d = %v, want %v", i, value.Fragments[i], wantFragments[i])
				}
			}
		})
	}
}
func TestWriteFileRoundTripPreservesSupportedStillImagePayloads(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0xD8, 0xFF, 0xC1, 0x00, 0x0B},
		{0xFF, 0xD8, 0xFF, 0xC3, 0x00, 0x0B},
	}
	for _, syntax := range []transfer.Syntax{
		transfer.JPEGExtended,
		transfer.JPEGLosslessNonHierarchical,
		transfer.JPEGLosslessSV1,
		transfer.RLELossless,
	} {
		t.Run(syntax.Name, func(t *testing.T) {
			file := &File{
				Dataset:        FromElements(encapsulatedStillImageDataset(wantFragments...), std.Dictionary),
				TransferSyntax: syntax,
			}

			var buf bytes.Buffer
			if err := WriteFile(&buf, file); err != nil {
				t.Fatal(err)
			}
			got, err := ReadFile(bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			if got.TransferSyntax.UID != syntax.UID {
				t.Fatalf("transfer syntax = %q, want %q", got.TransferSyntax.UID, syntax.UID)
			}
			elem, ok := got.Dataset.Get(core.TagPixelData)
			if !ok {
				t.Fatal("Pixel Data missing after round trip")
			}
			fragments, ok := elem.Value.(core.FragmentSequence)
			if !ok {
				t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
			}
			if len(fragments.Fragments) != len(wantFragments) {
				t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
			}
			for i := range wantFragments {
				if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
					t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
				}
			}
		})
	}
}
func TestWriteFileRoundTripPreservesVideoMediaPayload(t *testing.T) {
	wantFragments := [][]byte{
		{0x00, 0x00, 0x01, 0x09, 0x10, 0x00},
		{0x00, 0x00, 0x01, 0x65, 0x88, 0x00},
	}
	file := &File{
		Dataset:        FromElements(videoMediaDataset(wantFragments...), std.Dictionary),
		TransferSyntax: transfer.HEVCMP51,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.HEVCMP51.UID {
		t.Fatalf("transfer syntax = %q, want %q", got.TransferSyntax.UID, transfer.HEVCMP51.UID)
	}
	elem, ok := got.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("Pixel Data missing after round trip")
	}
	fragments, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
	}
	if len(fragments.Fragments) != len(wantFragments) {
		t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
	}
	for i := range wantFragments {
		if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
			t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
		}
	}
}
func TestWriteFileRoundTripPreservesJPEGXLPayload(t *testing.T) {
	wantFragments := [][]byte{
		{0xFF, 0x0A, 0x20, 0x01},
		{0x00, 0x00, 0x00, 0x0C, 0x4A, 0x58, 0x4C, 0x20},
	}
	file := &File{
		Dataset:        FromElements(jpegXLDataset(wantFragments...), std.Dictionary),
		TransferSyntax: transfer.JPEGXLJPEGRecompression,
	}

	var buf bytes.Buffer
	if err := WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	got, err := ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.JPEGXLJPEGRecompression.UID {
		t.Fatalf("transfer syntax = %q, want %q", got.TransferSyntax.UID, transfer.JPEGXLJPEGRecompression.UID)
	}
	elem, ok := got.Dataset.Get(core.TagPixelData)
	if !ok {
		t.Fatal("Pixel Data missing after round trip")
	}
	fragments, ok := elem.Value.(core.FragmentSequence)
	if !ok {
		t.Fatalf("Pixel Data value = %T, want FragmentSequence", elem.Value)
	}
	if len(fragments.Fragments) != len(wantFragments) {
		t.Fatalf("fragment count = %d, want %d", len(fragments.Fragments), len(wantFragments))
	}
	for i := range wantFragments {
		if !bytes.Equal(fragments.Fragments[i], wantFragments[i]) {
			t.Fatalf("fragment %d = % X, want % X", i, fragments.Fragments[i], wantFragments[i])
		}
	}
}
func TestWriteDataSetProducesRawDatasetOutput(t *testing.T) {
	wantElements := canonicalMinimalElements()
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

func TestWriteDataSetSortsElementsByTagAfterReplacement(t *testing.T) {
	tagSOPInstanceUID := core.NewTag(0x0008, 0x0018)
	tagPatientName := core.NewTag(0x0010, 0x0010)
	sopInstanceUID := newStringElement(tagSOPInstanceUID, core.VRUI, "1.2.3")
	oldPatientName := newStringElement(tagPatientName, core.VRPN, "OLD^PATIENT")
	newPatientName := newStringElement(tagPatientName, core.VRPN, "NEW^PATIENT")
	pixelData := core.NewRawElement(core.TagPixelData, core.VROB, []byte{0x01, 0x02})

	obj := FromElements([]core.Element{pixelData, sopInstanceUID, oldPatientName}, std.Dictionary)
	obj.Put(newPatientName)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		sopInstanceUID,
		newPatientName,
		pixelData,
	)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes are not sorted by tag:\n got % X\nwant % X", got, want)
	}
}

func TestWriteDataSetDeflatesOutput(t *testing.T) {
	wantElements := canonicalMinimalElements()
	obj := FromDataSet(core.DataSet{Elements: wantElements}, std.Dictionary)

	var buf bytes.Buffer
	if err := WriteDataSet(&buf, obj, transfer.DeflatedExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, wantElements...)
	got := buf.Bytes()
	if bytes.Equal(got, want) {
		t.Fatal("WriteDataSet wrote uncompressed bytes for deflated syntax")
	}
	if inflated := inflateBytes(t, got); !bytes.Equal(inflated, want) {
		t.Fatalf("inflated raw dataset bytes = % X, want % X", inflated, want)
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

	want := dicomtest.EncodeElements(transfer.ExplicitVRBigEndian, canonicalMinimalElements()...)
	if got := buf.Bytes(); !bytes.Equal(got, want) {
		t.Fatalf("raw dataset bytes = % X, want % X", got, want)
	}
}
func TestWriteDataSetRoundTripAcrossTransferSyntaxes(t *testing.T) {
	tests := []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ImplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.DeflatedExplicitVRLittleEndian,
	}

	want := canonicalMinimalDataSet()
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

	want := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, canonicalMinimalElements()...)
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

func canonicalMinimalElements() []core.Element {
	return sortedElementsForTest(dicomtest.MinimalDataset())
}

func canonicalMinimalDataSet() core.DataSet {
	return core.DataSet{Elements: canonicalMinimalElements()}
}

func sortedElementsForTest(elements []core.Element) []core.Element {
	sorted := append([]core.Element(nil), elements...)
	sortElementsByTag(sorted)
	return sorted
}
