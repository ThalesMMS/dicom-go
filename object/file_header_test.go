package object

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestReadPart10HeaderLeavesSourceAtDataSet(t *testing.T) {
	dataset := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(len(part10) - len(dataset))
	source := bytes.NewReader(part10)

	header, err := ReadPart10Header(source, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if header.DataSetOffset != wantOffset {
		t.Fatalf("DataSetOffset = %d, want %d", header.DataSetOffset, wantOffset)
	}
	if header.TransferSyntax != transfer.ExplicitVRLittleEndian {
		t.Fatalf("TransferSyntax = %#v, want %#v", header.TransferSyntax, transfer.ExplicitVRLittleEndian)
	}
	if header.Meta == nil {
		t.Fatal("Meta is nil")
	}
	if got, ok := header.Meta.GetString(tagMediaStorageSOPInstanceUID); !ok || got != dicomtest.TestSOPInstanceUID {
		t.Fatalf("MediaStorageSOPInstanceUID = %q, %v", got, ok)
	}
	if len(header.Preamble) != part10PreambleLength || !bytes.Equal(header.Preamble, part10[:part10PreambleLength]) {
		t.Fatalf("Preamble length/content mismatch")
	}
	position, err := source.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatal(err)
	}
	if position != wantOffset {
		t.Fatalf("source position = %d, want %d", position, wantOffset)
	}
}

func TestReadPart10HeaderPreservesNonZeroBaseAndCallerOwnership(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	dataset := dicomtest.EncodeElements(transfer.ImplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	const streamBase = 37
	payload := append(make([]byte, streamBase), part10...)
	source := &closeTrackingReadSeeker{Reader: bytes.NewReader(payload)}
	if _, err := source.Seek(streamBase, io.SeekStart); err != nil {
		t.Fatal(err)
	}

	header, err := ReadPart10Header(source, ReadFileOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(streamBase + len(part10) - len(dataset))
	if header.DataSetOffset != wantOffset {
		t.Fatalf("DataSetOffset = %d, want %d", header.DataSetOffset, wantOffset)
	}
	if position, err := source.Seek(0, io.SeekCurrent); err != nil || position != wantOffset {
		t.Fatalf("source position = %d, %v, want %d", position, err, wantOffset)
	}
	if source.closeCalls != 0 {
		t.Fatalf("source close calls = %d, want 0", source.closeCalls)
	}
}

func TestReadPart10HeaderMissingGroupLengthFollowsReadOptions(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	dataset := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	const groupLengthElementBytes = 12 // (0002,0000) UL: 8-byte header + 4-byte value.
	withoutGroupLength := append([]byte(nil), part10[:part10PreambleLength+len(part10Magic)]...)
	withoutGroupLength = append(withoutGroupLength, part10[part10PreambleLength+len(part10Magic)+groupLengthElementBytes:]...)

	_, err = ReadPart10Header(bytes.NewReader(withoutGroupLength), ReadFileOptions{})
	if !errors.Is(err, ErrInvalidFileMetaGroupLength) || !errors.Is(err, ErrFileMeta) {
		t.Fatalf("strict error = %v, want ErrInvalidFileMetaGroupLength and ErrFileMeta", err)
	}

	source := bytes.NewReader(withoutGroupLength)
	header, err := ReadPart10Header(source, ReadFileOptions{AllowMissingMetaElementGroupLength: true})
	if err != nil {
		t.Fatal(err)
	}
	wantOffset := int64(len(withoutGroupLength) - len(dataset))
	if header.DataSetOffset != wantOffset {
		t.Fatalf("DataSetOffset = %d, want %d", header.DataSetOffset, wantOffset)
	}
	if position, err := source.Seek(0, io.SeekCurrent); err != nil || position != wantOffset {
		t.Fatalf("source position = %d, %v, want %d", position, err, wantOffset)
	}
}

func TestReadPart10HeaderPreservesReadFileErrorClassification(t *testing.T) {
	unsupported, err := dicomtest.Part10File(transfer.XMLEncoding, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		data   []byte
		target error
	}{
		{name: "missing preamble", data: make([]byte, 256), target: ErrMissingPreamble},
		{name: "missing transfer syntax", data: buildPart10FileWithTransferSyntaxUID(" \x00"), target: ErrMissingTransferSyntax},
		{name: "unknown transfer syntax", data: buildPart10FileWithTransferSyntaxUID("1.2.840.10008.999.626"), target: transfer.ErrUnknownTransferSyntax},
		{name: "unsupported transfer syntax", data: unsupported, target: transfer.ErrUnsupportedTransferSyntax},
	}
	targets := []error{
		ErrMissingPreamble,
		ErrFileMeta,
		ErrMissingTransferSyntax,
		transfer.ErrUnknownTransferSyntax,
		transfer.ErrUnsupportedTransferSyntax,
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, gotErr := ReadPart10Header(bytes.NewReader(test.data), ReadFileOptions{})
			if !errors.Is(gotErr, test.target) {
				t.Fatalf("error = %v, want %v", gotErr, test.target)
			}
			for _, target := range targets {
				want := target == test.target
				if got := errors.Is(gotErr, target); got != want {
					t.Errorf("errors.Is(%v) = %v, want %v (error=%v)", target, got, want, gotErr)
				}
			}
		})
	}
}

func TestReadPart10HeaderDoesNotReadDataSetBytes(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	dataset := dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	wantOffset := int64(len(part10) - len(dataset))
	source := &readExtentSeeker{Reader: bytes.NewReader(part10)}

	if _, err := ReadPart10Header(source, ReadFileOptions{}); err != nil {
		t.Fatal(err)
	}
	if source.maxReadPosition != wantOffset {
		t.Fatalf("maximum source byte read = %d, want dataset boundary %d", source.maxReadPosition, wantOffset)
	}
}

func TestReadPart10HeaderEnforcesFileMetaLimits(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		opts   ReadFileOptions
		target error
	}{
		{name: "total bytes", opts: ReadFileOptions{MaxTotalBytes: part10PreambleLength + int64(len(part10Magic))}, target: parser.ErrMaxTotalBytesExceeded},
		{name: "element bytes", opts: ReadFileOptions{MaxElementBytes: 2}, target: parser.ErrMaxElementBytesExceeded},
		{name: "element count", opts: ReadFileOptions{MaxElements: 1}, target: parser.ErrMaxElementsExceeded},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ReadPart10Header(bytes.NewReader(part10), test.opts)
			if !errors.Is(err, test.target) || !errors.Is(err, ErrFileMeta) {
				t.Fatalf("error = %v, want %v and ErrFileMeta", err, test.target)
			}
		})
	}
}

func TestReadPart10HeaderMaxTotalBytesBoundsPreambleRead(t *testing.T) {
	part10, err := dicomtest.Part10File(transfer.ExplicitVRLittleEndian, dicomtest.MinimalDataset()...)
	if err != nil {
		t.Fatal(err)
	}
	source := &readExtentSeeker{Reader: bytes.NewReader(part10)}

	_, err = ReadPart10Header(source, ReadFileOptions{MaxTotalBytes: 1})
	if !errors.Is(err, parser.ErrMaxTotalBytesExceeded) {
		t.Fatalf("error = %v, want parser.ErrMaxTotalBytesExceeded", err)
	}
	if source.maxReadPosition > 1 {
		t.Fatalf("maximum source byte read = %d, want <= 1", source.maxReadPosition)
	}
}

func TestReadPart10HeaderRejectsNilSource(t *testing.T) {
	if _, err := ReadPart10Header(nil, ReadFileOptions{}); err == nil {
		t.Fatal("ReadPart10Header(nil) error = nil")
	}
}

type closeTrackingReadSeeker struct {
	*bytes.Reader
	closeCalls int
}

func (s *closeTrackingReadSeeker) Close() error {
	s.closeCalls++
	return nil
}

type readExtentSeeker struct {
	*bytes.Reader
	maxReadPosition int64
}

func (s *readExtentSeeker) Read(p []byte) (int, error) {
	n, err := s.Reader.Read(p)
	position, seekErr := s.Reader.Seek(0, io.SeekCurrent)
	if seekErr != nil {
		return n, seekErr
	}
	if position > s.maxReadPosition {
		s.maxReadPosition = position
	}
	return n, err
}
