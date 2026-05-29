package parser

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func largeDefinedValueStream(t *testing.T, pixelBytes int) []byte {
	t.Helper()
	pixels := make([]byte, pixelBytes)
	for i := range pixels {
		pixels[i] = byte(i * 31)
	}
	return dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewUShortElement(core.Tag{Group: 0x0028, Element: 0x0010}, 512),
		dicomtest.NewUShortElement(core.Tag{Group: 0x0028, Element: 0x0011}, 512),
		core.NewRawElement(core.TagPixelData, core.VROW, pixels),
	)
}

func pixelDataRawBytes(t *testing.T, ds core.DataSet) []byte {
	t.Helper()
	for _, elem := range ds.Elements {
		if elem.Header.Tag == core.TagPixelData {
			raw, ok := elem.RawBytes()
			if !ok {
				t.Fatal("pixel data element has no raw bytes")
			}
			return raw
		}
	}
	t.Fatal("pixel data element not found")
	return nil
}

// A value larger than the chunked-read threshold must parse to identical
// bytes whether the source is sized (io.ReadSeeker with a known end) or an
// unsized plain stream.
func TestLargeDefinedValueSizedSourceMatchesUnsized(t *testing.T) {
	data := largeDefinedValueStream(t, 300<<10)

	sizedReader := NewReader(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	sizedSet, err := sizedReader.ReadDataSet()
	if err != nil {
		t.Fatalf("sized source: %v", err)
	}

	unsizedReader := NewReader(unseekableReader{r: bytes.NewReader(data)}, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	unsizedSet, err := unsizedReader.ReadDataSet()
	if err != nil {
		t.Fatalf("unsized source: %v", err)
	}

	if len(sizedSet.Elements) != len(unsizedSet.Elements) {
		t.Fatalf("element count mismatch: sized %d, unsized %d", len(sizedSet.Elements), len(unsizedSet.Elements))
	}
	for i, sizedElem := range sizedSet.Elements {
		unsizedElem := unsizedSet.Elements[i]
		if sizedElem.Header.Tag != unsizedElem.Header.Tag {
			t.Fatalf("element %d tag mismatch: %s vs %s", i, sizedElem.Header.Tag, unsizedElem.Header.Tag)
		}
		sizedRaw, sizedOK := sizedElem.RawBytes()
		unsizedRaw, unsizedOK := unsizedElem.RawBytes()
		if sizedOK != unsizedOK {
			t.Fatalf("element %d (%s) raw availability mismatch", i, sizedElem.Header.Tag)
		}
		if !bytes.Equal(sizedRaw, unsizedRaw) {
			t.Fatalf("element %d (%s) bytes differ between sized and unsized sources", i, sizedElem.Header.Tag)
		}
	}

	sizedPixels := pixelDataRawBytes(t, sizedSet)
	if len(sizedPixels) != 300<<10 {
		t.Fatalf("unexpected pixel byte count: %d", len(sizedPixels))
	}
}

// A large value whose defined length runs exactly to the physical end of the
// source must parse successfully.
func TestLargeDefinedValueEndsExactlyAtSourceEnd(t *testing.T) {
	data := largeDefinedValueStream(t, 300<<10)

	reader := NewReader(bytes.NewReader(data), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	ds, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pixelDataRawBytes(t, ds)); got != 300<<10 {
		t.Fatalf("unexpected pixel byte count: %d", got)
	}
}

// A defined length that claims more bytes than the source physically holds
// must fail identically for sized and unsized sources, without taking the
// single-allocation path.
func TestLargeDefinedValueTruncatedSourceFailsForSizedAndUnsized(t *testing.T) {
	data := largeDefinedValueStream(t, 300<<10)
	truncated := data[:len(data)-(200<<10)]

	sizedReader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	_, sizedErr := sizedReader.ReadDataSet()
	if sizedErr == nil {
		t.Fatal("sized source: expected error for truncated value")
	}
	if !errors.Is(sizedErr, io.ErrUnexpectedEOF) {
		t.Fatalf("sized source: expected unexpected EOF, got %v", sizedErr)
	}

	unsizedReader := NewReader(unseekableReader{r: bytes.NewReader(truncated)}, transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})
	_, unsizedErr := unsizedReader.ReadDataSet()
	if unsizedErr == nil {
		t.Fatal("unsized source: expected error for truncated value")
	}
	if sizedErr.Error() != unsizedErr.Error() {
		t.Fatalf("sized and unsized errors differ:\n  sized:   %v\n  unsized: %v", sizedErr, unsizedErr)
	}
}

// A reader constructed over a source positioned mid-stream (BaseOffset in
// play) must still bound values by the remaining physical bytes.
func TestLargeDefinedValueSizedSourceWithBaseOffset(t *testing.T) {
	prefix := make([]byte, 4<<10)
	data := largeDefinedValueStream(t, 300<<10)
	full := append(append([]byte{}, prefix...), data...)

	source := bytes.NewReader(full)
	if _, err := source.Seek(int64(len(prefix)), io.SeekStart); err != nil {
		t.Fatal(err)
	}
	reader := NewReader(source, transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary: std.Dictionary,
		BaseOffset: int64(len(prefix)),
	})
	ds, err := reader.ReadDataSet()
	if err != nil {
		t.Fatal(err)
	}
	if got := len(pixelDataRawBytes(t, ds)); got != 300<<10 {
		t.Fatalf("unexpected pixel byte count: %d", got)
	}
}
