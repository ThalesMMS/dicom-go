package jpegxladapter

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestHelperProtocolRoundTripPreservesNumericMetadataAndPayload(t *testing.T) {
	var buf bytes.Buffer
	req := helperRequest{
		Opcode:        helperOpcodeDecode,
		RequestID:     42,
		Rows:          64,
		Columns:       32,
		Samples:       1,
		BitsAllocated: 16,
		Payload:       []byte{0x01, 0x02, 0x03, 0x04},
	}
	if err := writeHelperRequest(&buf, req); err != nil {
		t.Fatal(err)
	}
	got, err := readHelperRequest(&buf, defaultMaxHelperPayloadBytes)
	if err != nil {
		t.Fatal(err)
	}
	if got.Opcode != helperOpcodeDecode || got.RequestID != 42 {
		t.Fatalf("request identity = opcode %d id %d, want decode/42", got.Opcode, got.RequestID)
	}
	if got.Rows != 64 || got.Columns != 32 || got.Samples != 1 || got.BitsAllocated != 16 {
		t.Fatalf("numeric metadata = %+v", got)
	}
	if !bytes.Equal(got.Payload, req.Payload) {
		t.Fatalf("payload = %v, want %v", got.Payload, req.Payload)
	}

	buf.Reset()
	reply := helperReply{
		RequestID:     42,
		Status:        helperStatusOK,
		Rows:          64,
		Columns:       32,
		Samples:       1,
		BitsAllocated: 16,
		Pixels:        []byte{0xaa, 0xbb},
	}
	if err := writeHelperReply(&buf, reply); err != nil {
		t.Fatal(err)
	}
	gotReply, err := readHelperReply(&buf, 4)
	if err != nil {
		t.Fatal(err)
	}
	if gotReply.RequestID != 42 || gotReply.Status != helperStatusOK {
		t.Fatalf("reply identity = %+v", gotReply)
	}
	if !bytes.Equal(gotReply.Pixels, reply.Pixels) {
		t.Fatalf("pixels = %v, want %v", gotReply.Pixels, reply.Pixels)
	}
}

func TestHelperProtocolRejectsBadMagic(t *testing.T) {
	var buf bytes.Buffer
	if err := writeHelperRequest(&buf, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[0] = 'X'
	_, err := readHelperRequest(bytes.NewReader(raw), defaultMaxHelperPayloadBytes)
	if !errors.Is(err, ErrHelperProtocol) {
		t.Fatalf("bad magic error = %v, want ErrHelperProtocol", err)
	}
}

func TestHelperProtocolRejectsVersionMismatch(t *testing.T) {
	var buf bytes.Buffer
	if err := writeHelperRequest(&buf, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	raw[4] = 99
	_, err := readHelperRequest(bytes.NewReader(raw), defaultMaxHelperPayloadBytes)
	if !errors.Is(err, ErrHelperProtocol) {
		t.Fatalf("version error = %v, want ErrHelperProtocol", err)
	}
}

func TestHelperProtocolRejectsOversizedPayloadBeforeAllocating(t *testing.T) {
	var header [helperRequestHeaderSize]byte
	copy(header[:4], helperMagic)
	putUint32(header[4:], HelperProtocolVersion)
	putUint32(header[8:], helperOpcodeDecode)
	putUint64(header[12:], 7)
	putUint32(header[36:], defaultMaxHelperPayloadBytes+1)
	_, err := readHelperRequest(bytes.NewReader(header[:]), defaultMaxHelperPayloadBytes)
	if !errors.Is(err, ErrHelperTooLarge) {
		t.Fatalf("oversized error = %v, want ErrHelperTooLarge", err)
	}
}

func TestHelperProtocolTruncatedReplyIsChannelLoss(t *testing.T) {
	_, err := readHelperReply(bytes.NewReader([]byte("JXLH\x01")), 16)
	if !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, ErrHelperCrashed) {
		t.Fatalf("truncated reply error = %v, want unexpected EOF or crash", err)
	}
}

func TestHelperReplyRejectsPixelBytesAboveLimit(t *testing.T) {
	var header [helperReplyHeaderSize]byte
	copy(header[:4], helperMagic)
	putUint32(header[4:], HelperProtocolVersion)
	putUint64(header[8:], 1)
	putUint32(header[32:], 32)
	_, err := readHelperReply(bytes.NewReader(header[:]), 8)
	if !errors.Is(err, ErrHelperTooLarge) {
		t.Fatalf("oversized pixels error = %v, want ErrHelperTooLarge", err)
	}
}

func gray8Meta() pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows: 1, Columns: 2, SamplesPerPixel: 1,
		BitsAllocated: 8, BitsStored: 8, HighBit: 7, NumberOfFrames: 1,
		PhotometricInterpretation: "MONOCHROME2",
	}
}

func putUint32(b []byte, v uint32) {
	b[0] = byte(v)
	b[1] = byte(v >> 8)
	b[2] = byte(v >> 16)
	b[3] = byte(v >> 24)
}

func putUint64(b []byte, v uint64) {
	putUint32(b[0:], uint32(v))
	putUint32(b[4:], uint32(v>>32))
}
