package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"testing"
)

func writeElementBytes(t *testing.T, syntax transfer.Syntax, elem core.Element) []byte {
	t.Helper()

	return writeElementBytesWithOptions(t, syntax, elem, defaultWriterOptions())
}

func writeElementBytesWithOptions(t *testing.T, syntax transfer.Syntax, elem core.Element, opts WriterOptions) []byte {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriterWithOptions(&buf, syntax, opts)
	if err := writer.WriteElement(elem); err != nil {
		t.Fatalf("WriteElement() error = %v", err)
	}
	return buf.Bytes()
}

func roundTripElement(t *testing.T, syntax transfer.Syntax, elem core.Element) core.Element {
	t.Helper()

	data := writeElementBytes(t, syntax, elem)
	reader := NewReader(bytes.NewReader(data), syntax, ReaderOptions{Dictionary: std.Dictionary})
	tok, err := reader.Next()
	if err != nil {
		t.Fatalf("Reader.Next() error = %v", err)
	}
	if tok.Kind != TokenElement {
		t.Fatalf("token kind = %s, want %s", tok.Kind, TokenElement)
	}
	_, err = reader.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("second Reader.Next() error = %v, want EOF", err)
	}
	return tok.Element
}

func roundTripDataSet(t *testing.T, syntax transfer.Syntax, dataSet core.DataSet, opts WriterOptions, dict dictionary.DataDictionary) core.DataSet {
	t.Helper()

	var buf bytes.Buffer
	writer := NewWriterWithOptions(&buf, syntax, opts)
	for _, elem := range dataSet.Elements {
		if err := writer.WriteElement(elem); err != nil {
			t.Fatalf("WriteElement(%s) error = %v", elem.Tag(), err)
		}
	}

	reader := NewReader(bytes.NewReader(buf.Bytes()), syntax, ReaderOptions{Dictionary: dict})
	got, err := reader.ReadDataSet()
	if err != nil {
		t.Fatalf("ReadDataSet() error = %v", err)
	}
	return got
}

func roundTripTransferSyntaxes() []transfer.Syntax {
	return []transfer.Syntax{
		transfer.ExplicitVRLittleEndian,
		transfer.ExplicitVRBigEndian,
		transfer.ImplicitVRLittleEndian,
	}
}

func sequenceRoundTripDictionary() dictionary.DataDictionary {
	return &multiCountingDictionary{
		entries: map[core.Tag]core.VR{
			core.NewTag(0x0008, 0x1111): core.VRSQ,
			core.NewTag(0x0008, 0x1140): core.VRSQ,
			core.NewTag(0x0008, 0x1155): core.VRUI,
			core.NewTag(0x0010, 0x0010): core.VRPN,
			core.NewTag(0x0010, 0x0020): core.VRLO,
		},
	}
}

func fragmentExpectedForSyntax(base core.DataSet, syntax transfer.Syntax) core.DataSet {
	if syntax != transfer.ImplicitVRLittleEndian {
		return base
	}
	return core.DataSet{
		Elements: []core.Element{
			{
				Header: core.ElementHeader{
					Tag:       core.TagPixelData,
					VR:        core.VROW,
					Length:    core.UndefinedLength,
					LengthSet: true,
				},
				Value: base.Elements[0].Value,
			},
		},
	}
}

func assertRoundTripMatch(t *testing.T, got, want core.Element) {
	t.Helper()

	if got.Tag() != want.Tag() {
		t.Fatalf("tag = %s, want %s", got.Tag(), want.Tag())
	}
	if got.VR() != want.VR() {
		t.Fatalf("VR = %s, want %s", got.VR(), want.VR())
	}

	gotRaw, gotOK := got.RawBytes()
	wantRaw, wantOK := want.RawBytes()
	if gotOK != wantOK {
		t.Fatalf("raw-bytes flag = %v, want %v", gotOK, wantOK)
	}
	if !bytes.Equal(gotRaw, wantRaw) {
		t.Fatalf("raw bytes = % X, want % X", gotRaw, wantRaw)
	}
}

func u16Bytes(order binary.ByteOrder, value uint16) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	buf := make([]byte, 2)
	order.PutUint16(buf, value)
	return buf
}

func u32Bytes(order binary.ByteOrder, value uint32) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	buf := make([]byte, 4)
	order.PutUint32(buf, value)
	return buf
}

type boomIOError struct {
	message string
}

func (e *boomIOError) Error() string {
	return e.message
}

type failAfterWriter struct {
	limit int
	wrote int
	err   error
}

func (w *failAfterWriter) Write(p []byte) (int, error) {
	if w.wrote >= w.limit {
		return 0, w.err
	}

	remaining := w.limit - w.wrote
	if len(p) > remaining {
		w.wrote += remaining
		if w.err != nil {
			return remaining, w.err
		}
		return remaining, io.ErrShortWrite
	}
	w.wrote += len(p)
	return len(p), nil
}
