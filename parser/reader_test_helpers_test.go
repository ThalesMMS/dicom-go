package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"testing"
)

type countingDictionary struct {
	entry      dictEntry
	byTagCalls int
}

type multiCountingDictionary struct {
	entries    map[core.Tag]core.VR
	byTagCalls int
}

type dictEntry struct {
	tag core.Tag
	vr  core.VR
}

func dictionaryEntry(tag core.Tag, vr core.VR) dictEntry {
	return dictEntry{tag: tag, vr: vr}
}

func (d *countingDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if d == nil {
		return dictionary.Entry{}, false
	}
	d.byTagCalls++
	if d.entry.tag != tag {
		return dictionary.Entry{}, false
	}
	return dictionary.Entry{Tag: d.entry.tag, VR: d.entry.vr}, true
}

func (d *countingDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func (d *multiCountingDictionary) ByTag(tag core.Tag) (dictionary.Entry, bool) {
	if d == nil {
		return dictionary.Entry{}, false
	}
	d.byTagCalls++
	vr, ok := d.entries[tag]
	if !ok {
		return dictionary.Entry{}, false
	}
	return dictionary.Entry{Tag: tag, VR: vr}, true
}

func (d *multiCountingDictionary) ByKeyword(string) (dictionary.Entry, bool) {
	return dictionary.Entry{}, false
}

func explicitLongHeaderBytes(order binary.ByteOrder, tag core.Tag, vr core.VR, reserved [2]byte, length uint32) []byte {
	if reserved == [2]byte{} {
		return dicomtest.ExplicitLongHeaderBytes(order, tag, vr, length)
	}
	var buf bytes.Buffer
	writeTag(&buf, order, tag)
	buf.WriteString(vr.String())
	buf.Write(reserved[:])
	writeUint32(&buf, order, length)
	return buf.Bytes()
}

func implicitHeaderBytes(order binary.ByteOrder, tag core.Tag, length uint32) []byte {
	var buf bytes.Buffer
	writeTag(&buf, order, tag)
	writeUint32(&buf, order, length)
	return buf.Bytes()
}

type integrationElement struct {
	tag        core.Tag
	vr         core.VR
	value      []byte
	wantString string
}

func encapsulatedPixelDataBytes(offsetTable []byte, fragments ...[]byte) []byte {
	return dicomtest.EncodeElement(
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, offsetTable, fragments...),
		transfer.JPEGBaseline,
	)
}

func readAllTokens(reader *Reader) ([]Token, error) {
	var tokens []Token
	for {
		tok, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return tokens, nil
		}
		if err != nil {
			return nil, err
		}
		tokens = append(tokens, tok)
	}
}

func assertTokenKinds(t *testing.T, tokens []Token, want []TokenKind) {
	t.Helper()
	if len(tokens) != len(want) {
		t.Fatalf("token count = %d, want %d", len(tokens), len(want))
	}
	for i, wantKind := range want {
		if tokens[i].Kind != wantKind {
			t.Fatalf("token %d kind = %v, want %v", i, tokens[i].Kind, wantKind)
		}
	}
}

func testRejectOddLengthAcrossTransferSyntaxes(t *testing.T, name string, opts ReaderOptions) {
	t.Helper()

	tests := []struct {
		name   string
		syntax transfer.Syntax
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian},
	}

	for _, tt := range tests {
		t.Run(name+"/"+tt.name, func(t *testing.T) {
			assertOddLengthReject(t, tt.syntax, opts, core.NewTag(0x0010, 0x0010), core.VRPN)
			if tt.syntax.ExplicitVR {
				assertOddLengthReject(t, tt.syntax, opts, core.NewTag(0x0010, 0x0010), core.VROB)
			}
		})
	}
}

func assertOddLengthReject(t *testing.T, syntax transfer.Syntax, opts ReaderOptions, tag core.Tag, vr core.VR) {
	t.Helper()

	buf := definedElementBytes(syntax, tag, vr, 3, []byte("ODD"))
	reader := NewReader(bytes.NewReader(buf), syntax, opts)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrOddElementLength) {
		t.Fatalf("expected odd-length error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	wantOffset := definedHeaderLength(syntax, vr)
	if parseErr.Offset != wantOffset {
		t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, wantOffset)
	}
	if parseErr.Tag != tag {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if parseErr.VR != vr {
		t.Fatalf("unexpected VR: %s", parseErr.VR)
	}
	if parseErr.Length != 3 {
		t.Fatalf("unexpected length: %s", parseErr.Length)
	}
	if got := reader.Position(); got != wantOffset {
		t.Fatalf("reader position after odd-length error = %d, want %d", got, wantOffset)
	}
}

func definedElementBytes(syntax transfer.Syntax, tag core.Tag, vr core.VR, length uint32, value []byte) []byte {
	var buf bytes.Buffer
	writeTag(&buf, syntax.ByteOrder, tag)

	if syntax.ExplicitVR {
		buf.WriteString(vr.String())
		if vr.UsesLongExplicitLength() {
			buf.Write([]byte{0x00, 0x00})
			writeUint32(&buf, syntax.ByteOrder, length)
		} else {
			if length > 0xFFFF {
				panic("definedElementBytes: short explicit VR length exceeds uint16")
			}
			writeUint16(&buf, syntax.ByteOrder, uint16(length))
		}
	} else {
		writeUint32(&buf, syntax.ByteOrder, length)
	}

	buf.Write(value)
	return buf.Bytes()
}

func definedHeaderLength(syntax transfer.Syntax, vr core.VR) int64 {
	if !syntax.ExplicitVR {
		return 8
	}
	if vr.UsesLongExplicitLength() {
		return 12
	}
	return 8
}

func writeTag(buf *bytes.Buffer, order binary.ByteOrder, tag core.Tag) {
	writeUint16(buf, order, tag.Group)
	writeUint16(buf, order, tag.Element)
}

func writeUint16(buf *bytes.Buffer, order binary.ByteOrder, value uint16) {
	var raw [2]byte
	order.PutUint16(raw[:], value)
	buf.Write(raw[:])
}

func writeUint32(buf *bytes.Buffer, order binary.ByteOrder, value uint32) {
	var raw [4]byte
	order.PutUint32(raw[:], value)
	buf.Write(raw[:])
}
