package parser

import (
	"bytes"
	"encoding/binary"
	"errors"
	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
	"io"
	"strings"
	"testing"
)

func TestNextReturnsCleanEOFOnlyAtElementBoundary(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	if _, err := reader.Next(); err != nil {
		t.Fatal(err)
	}

	_, err := reader.Next()
	if !errors.Is(err, io.EOF) {
		t.Fatalf("expected clean EOF, got %v", err)
	}
	if errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected clean EOF, got unexpected EOF: %v", err)
	}
	var parseErr *ParseError
	if errors.As(err, &parseErr) {
		t.Fatalf("expected plain EOF at element boundary, got parse error: %v", parseErr)
	}
}
func TestNextWrapsPartialHeaderEOFWithOffset(t *testing.T) {
	tests := []struct {
		name string
		data []byte
	}{
		{name: "one_tag_byte", data: []byte{0x10}},
		{name: "two_tag_bytes", data: []byte{0x10, 0x00}},
		{name: "three_tag_bytes", data: []byte{0x10, 0x00, 0x10}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(tt.data),
				transfer.ExplicitVRLittleEndian,
				ReaderOptions{Dictionary: std.Dictionary, BaseOffset: 132},
			)

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if errors.Is(err, io.EOF) {
				t.Fatalf("partial header should not match io.EOF: %v", err)
			}
			if !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("expected unexpected EOF, got %v", err)
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != OpReadTag {
				t.Fatalf("unexpected op: %s", parseErr.Op)
			}
			if parseErr.Offset != 132 {
				t.Fatalf("unexpected offset: got %d want 132", parseErr.Offset)
			}
			if !strings.Contains(parseErr.Error(), "offset 132") {
				t.Fatalf("error string missing offset: %v", parseErr)
			}
		})
	}
}
func TestNextWrapsValueEOFWithContext(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	truncated := buf[:len(buf)-2]
	reader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("partial value should not match io.EOF: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Offset != 8 {
		t.Fatalf("unexpected value offset: got %d want 8", parseErr.Offset)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if parseErr.VR != core.VRPN {
		t.Fatalf("unexpected VR: %s", parseErr.VR)
	}
	if parseErr.Length != 4 {
		t.Fatalf("unexpected length: %s", parseErr.Length)
	}
	msg := parseErr.Error()
	for _, want := range []string{"read value", "offset 8", "(0010,0010)", "VR PN", "length 4"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("error string %q missing %q", msg, want)
		}
	}
}
func TestNextWrapsMaxElementBytesGuardAsParseError(t *testing.T) {
	tests := []struct {
		name   string
		syntax transfer.Syntax
		vr     core.VR
		tag    core.Tag
	}{
		{name: "explicit little endian", syntax: transfer.ExplicitVRLittleEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "implicit little endian", syntax: transfer.ImplicitVRLittleEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "explicit big endian", syntax: transfer.ExplicitVRBigEndian, vr: core.VRPN, tag: core.NewTag(0x0010, 0x0010)},
		{name: "explicit little endian long header", syntax: transfer.ExplicitVRLittleEndian, vr: core.VROB, tag: core.NewTag(0x0002, 0x0001)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value := []byte("TEST")
			if tt.vr == core.VROB {
				value = []byte{1, 2, 3, 4}
			}
			buf := definedElementBytes(tt.syntax, tt.tag, tt.vr, uint32(len(value)), value)
			reader := NewReader(bytes.NewReader(buf), tt.syntax, ReaderOptions{
				Dictionary:      std.Dictionary,
				MaxElementBytes: 2,
			})

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != OpReadValue {
				t.Fatalf("unexpected op: %s", parseErr.Op)
			}
			wantOffset := definedHeaderLength(tt.syntax, tt.vr)
			if parseErr.Offset != wantOffset {
				t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, wantOffset)
			}
			if parseErr.Tag != tt.tag {
				t.Fatalf("unexpected tag: %s", parseErr.Tag)
			}
			if parseErr.VR != tt.vr {
				t.Fatalf("unexpected VR: %s", parseErr.VR)
			}
			if parseErr.Length != 4 {
				t.Fatalf("unexpected length: %s", parseErr.Length)
			}
			if got := reader.Position(); got != wantOffset {
				t.Fatalf("reader position after size-limit error = %d, want %d", got, wantOffset)
			}
		})
	}
}
func TestNextRejectsGiantElementLengthBeforeAllocation(t *testing.T) {
	buf := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x7FE0, 0x0010), core.VROB, [2]byte{}, 0xFFFFFFFE)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:      std.Dictionary,
		MaxElementBytes: 16,
	})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementBytesExceeded) {
		t.Fatalf("expected ErrMaxElementBytesExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Length != core.Length(0xFFFFFFFE) {
		t.Fatalf("unexpected length: got %s want %s", parseErr.Length, core.Length(0xFFFFFFFE))
	}
	if got := reader.Position(); got != definedHeaderLength(transfer.ExplicitVRLittleEndian, core.VROB) {
		t.Fatalf("reader position after size-limit error = %d", got)
	}
}
func TestNextRejectsMaxElements(t *testing.T) {
	stream := bytes.Join([][]byte{
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "ONE^TEST"), transfer.ExplicitVRLittleEndian),
		dicomtest.EncodeElement(dicomtest.NewPNElement(core.NewTag(0x0010, 0x0020), "TWO^TEST"), transfer.ExplicitVRLittleEndian),
	}, nil)
	reader := NewReader(bytes.NewReader(stream), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:  std.Dictionary,
		MaxElements: 1,
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxElementsExceeded) {
		t.Fatalf("expected ErrMaxElementsExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckElementCount {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0020) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
}
func TestNextRejectsMaxTotalBytesAtBoundary(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:    std.Dictionary,
		MaxTotalBytes: int64(len(buf)),
	})

	if _, err := reader.Next(); err != nil {
		t.Fatalf("first element: %v", err)
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxTotalBytesExceeded) {
		t.Fatalf("expected ErrMaxTotalBytesExceeded, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckTotalBytes {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Offset != int64(len(buf)) {
		t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, len(buf))
	}
}
func TestNextRejectsInvalidExplicitVRBytes(t *testing.T) {
	// Unlike permissive readers, dicom-go rejects unknown VR bytes during
	// header parsing instead of silently treating them as UN or raw bytes.
	buf := []byte{
		0x10, 0x00, 0x10, 0x00,
		'Z', 'Z',
		0x04, 0x00,
		'T', 'E', 'S', 'T',
	}
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadVR {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if !strings.Contains(parseErr.Error(), `invalid VR "ZZ"`) {
		t.Fatalf("unexpected error message: %v", parseErr)
	}
}
func TestNextRejectsNonASCIIExplicitVRBytes(t *testing.T) {
	buf := []byte{
		0x10, 0x00, 0x10, 0x00,
		0xFF, 0xFE,
		0x04, 0x00,
		'T', 'E', 'S', 'T',
	}
	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadVR {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
	if parseErr.Tag != core.NewTag(0x0010, 0x0010) {
		t.Fatalf("unexpected tag: %s", parseErr.Tag)
	}
	if !strings.Contains(parseErr.Error(), "invalid VR") {
		t.Fatalf("unexpected error message: %v", parseErr)
	}
}
func TestNextRejectsGarbageInputWithOffsetContext(t *testing.T) {
	tests := []struct {
		name       string
		syntax     transfer.Syntax
		data       []byte
		wantOp     Op
		wantOffset int64
	}{
		{
			name:       "random_bytes_implicit_vr",
			syntax:     transfer.ImplicitVRLittleEndian,
			data:       []byte{0xDE, 0xAD, 0xBE, 0xEF, 0x20, 0x00, 0x00, 0x00, 0x99},
			wantOp:     OpReadValue,
			wantOffset: 8,
		},
		{
			name:       "truncated_explicit_vr_header_after_tag",
			syntax:     transfer.ExplicitVRLittleEndian,
			data:       []byte{0x10, 0x00, 0x10, 0x00, 'P'},
			wantOp:     OpReadVR,
			wantOffset: 4,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(bytes.NewReader(tt.data), tt.syntax, ReaderOptions{Dictionary: std.Dictionary})

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != tt.wantOp {
				t.Fatalf("unexpected op: got %s want %s", parseErr.Op, tt.wantOp)
			}
			if parseErr.Offset != tt.wantOffset {
				t.Fatalf("unexpected offset: got %d want %d", parseErr.Offset, tt.wantOffset)
			}
		})
	}
}
func TestReadAllPropagatesPartialReadError(t *testing.T) {
	buf := dicomtest.EncodeElement(
		dicomtest.NewPNElement(core.NewTag(0x0010, 0x0010), "TEST"),
		transfer.ExplicitVRLittleEndian,
	)
	truncated := buf[:len(buf)-2]
	reader := NewReader(bytes.NewReader(truncated), transfer.ExplicitVRLittleEndian, ReaderOptions{Dictionary: std.Dictionary})

	_, err := reader.ReadAll()
	if err == nil {
		t.Fatal("expected error")
	}
	if errors.Is(err, io.EOF) {
		t.Fatalf("ReadAll should not treat partial read as clean EOF: %v", err)
	}
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("expected unexpected EOF, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpReadValue {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}
func TestNextContextValidationErrorsIncludeParseContext(t *testing.T) {
	tests := []struct {
		name       string
		stream     []byte
		syntax     transfer.Syntax
		skip       int
		dict       dictionary.DataDictionary
		wantErr    error
		wantTag    core.Tag
		wantOffset int64
		wantOp     Op
	}{
		{
			name:       "item delimiter at top level",
			stream:     dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			syntax:     transfer.ExplicitVRLittleEndian,
			wantErr:    ErrUnexpectedItemDelimiter,
			wantTag:    core.TagItemDelimitationItem,
			wantOffset: 8,
			wantOp:     OpReadTag,
		},
		{
			name: "sequence delimiter inside item context",
			stream: bytes.Join([][]byte{
				dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			syntax:     transfer.ExplicitVRLittleEndian,
			skip:       2,
			wantErr:    ErrUnexpectedSequenceDelimiter,
			wantTag:    core.TagSequenceDelimitationItem,
			wantOffset: 28,
			wantOp:     OpReadTag,
		},
		{
			name: "sequence delimiter when defined-length sequence is open",
			stream: bytes.Join([][]byte{
				dicomtest.SequenceHeaderBytes(transfer.ExplicitVRLittleEndian, core.NewTag(0x0008, 0x1111), 4),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			syntax:     transfer.ExplicitVRLittleEndian,
			skip:       1,
			wantErr:    ErrUnexpectedSequenceDelimiter,
			wantTag:    core.TagSequenceDelimitationItem,
			wantOffset: 20,
			wantOp:     OpReadTag,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dict := tt.dict
			if dict == nil {
				dict = std.Dictionary
			}
			reader := NewReader(bytes.NewReader(tt.stream), tt.syntax, ReaderOptions{Dictionary: dict})
			for i := 0; i < tt.skip; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatalf("preparing token stream: %v", err)
				}
			}

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}

			var parseErr *ParseError
			if !errors.As(err, &parseErr) {
				t.Fatalf("expected ParseError, got %T", err)
			}
			if parseErr.Op != tt.wantOp {
				t.Fatalf("parse error op = %s, want %s", parseErr.Op, tt.wantOp)
			}
			if parseErr.Tag != tt.wantTag {
				t.Fatalf("parse error tag = %s, want %s", parseErr.Tag, tt.wantTag)
			}
			if parseErr.Offset != tt.wantOffset {
				t.Fatalf("parse error offset = %d, want %d", parseErr.Offset, tt.wantOffset)
			}
		})
	}
}
func TestNextRejectsUnexpectedDelimitersByContext(t *testing.T) {
	tests := []struct {
		name    string
		syntax  transfer.Syntax
		stream  []byte
		skip    int
		dict    dictionary.DataDictionary
		wantErr error
	}{
		{
			name:   "item delimiter while sequence is open",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    1,
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "sequence delimiter while item is open",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    2,
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
		{
			name:   "item delimiter for defined-length item",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 4),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    2,
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "sequence delimiter for defined-length sequence",
			syntax: transfer.ExplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 4),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    1,
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
		{
			name:   "implicit item delimiter for defined-length item",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 0xFFFFFFFF),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 4),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItemDelimitationItem, 0),
			}, nil),
			skip:    2,
			dict:    &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
			wantErr: ErrUnexpectedItemDelimiter,
		},
		{
			name:   "implicit sequence delimiter for defined-length sequence",
			syntax: transfer.ImplicitVRLittleEndian,
			stream: bytes.Join([][]byte{
				implicitHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), 4),
				dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagSequenceDelimitationItem, 0),
			}, nil),
			skip:    1,
			dict:    &countingDictionary{entry: dictionaryEntry(core.NewTag(0x0008, 0x1111), core.VRSQ)},
			wantErr: ErrUnexpectedSequenceDelimiter,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dict := tt.dict
			if dict == nil {
				dict = std.Dictionary
			}
			reader := NewReader(bytes.NewReader(tt.stream), tt.syntax, ReaderOptions{Dictionary: dict})
			for i := 0; i < tt.skip; i++ {
				if _, err := reader.Next(); err != nil {
					t.Fatalf("preparing token stream: %v", err)
				}
			}

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("expected %v, got %v", tt.wantErr, err)
			}
		})
	}
}
func TestNextRejectsItemOutsideSequenceContext(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF)),
		transfer.ExplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnexpectedSequenceControlTag) {
		t.Fatalf("expected unexpected-control-tag error, got %v", err)
	}
}
func TestNextRejectsMaxSequenceDepth(t *testing.T) {
	nestedSeq := explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x2222), core.VRSQ, [2]byte{}, 0xFFFFFFFF)
	buf := bytes.Join([][]byte{
		explicitLongHeaderBytes(binary.LittleEndian, core.NewTag(0x0008, 0x1111), core.VRSQ, [2]byte{}, 0xFFFFFFFF),
		dicomtest.SequenceControlBytes(binary.LittleEndian, core.TagItem, 0xFFFFFFFF),
		nestedSeq,
	}, nil)

	reader := NewReader(bytes.NewReader(buf), transfer.ExplicitVRLittleEndian, ReaderOptions{
		Dictionary:       std.Dictionary,
		MaxSequenceDepth: 2,
	})

	for i := 0; i < 2; i++ {
		if _, err := reader.Next(); err != nil {
			t.Fatalf("token %d: %v", i, err)
		}
	}

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrMaxDepthExceeded) {
		t.Fatalf("expected max-depth error, got %v", err)
	}

	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T", err)
	}
	if parseErr.Op != OpCheckDepth {
		t.Fatalf("unexpected op: %s", parseErr.Op)
	}
}
func TestNextRejectsUnknownSequenceControlTag(t *testing.T) {
	reader := NewReader(
		bytes.NewReader(dicomtest.SequenceControlBytes(binary.LittleEndian, core.NewTag(0xFFFE, 0x0000), 0)),
		transfer.ImplicitVRLittleEndian,
		ReaderOptions{Dictionary: std.Dictionary},
	)

	_, err := reader.Next()
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, ErrUnexpectedSequenceControlTag) {
		t.Fatalf("expected unexpected-control-tag error, got %v", err)
	}
}
func TestNextRejectsNonZeroDelimiterLength(t *testing.T) {
	tests := []struct {
		name string
		tag  core.Tag
	}{
		{name: "item delimitation", tag: core.TagItemDelimitationItem},
		{name: "sequence delimitation", tag: core.TagSequenceDelimitationItem},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reader := NewReader(
				bytes.NewReader(dicomtest.SequenceControlBytes(binary.LittleEndian, tt.tag, 4)),
				transfer.ExplicitVRLittleEndian,
				ReaderOptions{Dictionary: std.Dictionary},
			)

			_, err := reader.Next()
			if err == nil {
				t.Fatal("expected error")
			}
			if !errors.Is(err, ErrUnexpectedDelimiterLength) {
				t.Fatalf("expected delimiter-length error, got %v", err)
			}
		})
	}
}
