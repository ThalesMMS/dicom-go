package jpegxladapter

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

const (
	helperMagic                  = "JXLH"
	HelperProtocolVersion        = uint32(1)
	helperOpcodeHello            = uint32(1)
	helperOpcodeDecode           = uint32(2)
	helperOpcodeShutdown         = uint32(3)
	helperStatusOK               = uint32(0)
	helperStatusMalformed        = uint32(1)
	helperStatusUnsupported      = uint32(2)
	helperStatusSizeMismatch     = uint32(3)
	helperStatusTooLarge         = uint32(4)
	helperStatusProtocol         = uint32(5)
	helperRequestHeaderSize      = 40
	helperReplyHeaderSize        = 36
	defaultMaxHelperPayloadBytes = 64 << 20
)

var (
	ErrHelperUnavailable = errors.New("jpegxladapter: jpeg xl helper unavailable")
	ErrHelperCrashed     = errors.New("jpegxladapter: jpeg xl helper crashed")
	ErrHelperProtocol    = errors.New("jpegxladapter: jpeg xl helper protocol error")
	ErrHelperTooLarge    = errors.New("jpegxladapter: jpeg xl helper payload exceeds limit")
)

type helperRequest struct {
	Opcode        uint32
	RequestID     uint64
	Rows          uint32
	Columns       uint32
	Samples       uint32
	BitsAllocated uint32
	Payload       []byte
}

type helperReply struct {
	RequestID     uint64
	Status        uint32
	Rows          uint32
	Columns       uint32
	Samples       uint32
	BitsAllocated uint32
	Pixels        []byte
}

func writeHelperRequest(w io.Writer, req helperRequest) error {
	var header [helperRequestHeaderSize]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[4:], HelperProtocolVersion)
	binary.LittleEndian.PutUint32(header[8:], req.Opcode)
	binary.LittleEndian.PutUint64(header[12:], req.RequestID)
	binary.LittleEndian.PutUint32(header[20:], req.Rows)
	binary.LittleEndian.PutUint32(header[24:], req.Columns)
	binary.LittleEndian.PutUint32(header[28:], req.Samples)
	binary.LittleEndian.PutUint32(header[32:], req.BitsAllocated)
	binary.LittleEndian.PutUint32(header[36:], uint32(len(req.Payload)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(req.Payload) == 0 {
		return nil
	}
	_, err := w.Write(req.Payload)
	return err
}

func readHelperRequest(r io.Reader, maxPayload uint32) (helperRequest, error) {
	var header [helperRequestHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return helperRequest{}, mapHelperIOError(err)
	}
	if string(header[:4]) != helperMagic {
		return helperRequest{}, fmt.Errorf("%w: request magic mismatch", ErrHelperProtocol)
	}
	if version := binary.LittleEndian.Uint32(header[4:]); version != HelperProtocolVersion {
		return helperRequest{}, fmt.Errorf("%w: request version %d", ErrHelperProtocol, version)
	}
	n := binary.LittleEndian.Uint32(header[36:])
	if n > maxPayload {
		return helperRequest{}, fmt.Errorf("%w: request bytes=%d limit=%d", ErrHelperTooLarge, n, maxPayload)
	}
	req := helperRequest{
		Opcode:        binary.LittleEndian.Uint32(header[8:]),
		RequestID:     binary.LittleEndian.Uint64(header[12:]),
		Rows:          binary.LittleEndian.Uint32(header[20:]),
		Columns:       binary.LittleEndian.Uint32(header[24:]),
		Samples:       binary.LittleEndian.Uint32(header[28:]),
		BitsAllocated: binary.LittleEndian.Uint32(header[32:]),
	}
	if n == 0 {
		return req, nil
	}
	req.Payload = make([]byte, n)
	if _, err := io.ReadFull(r, req.Payload); err != nil {
		return helperRequest{}, mapHelperIOError(err)
	}
	return req, nil
}

func writeHelperReply(w io.Writer, reply helperReply) error {
	pixels := reply.Pixels
	if reply.Status != helperStatusOK {
		pixels = nil
	}
	var header [helperReplyHeaderSize]byte
	copy(header[:4], helperMagic)
	binary.LittleEndian.PutUint32(header[4:], HelperProtocolVersion)
	binary.LittleEndian.PutUint64(header[8:], reply.RequestID)
	binary.LittleEndian.PutUint32(header[16:], reply.Status)
	binary.LittleEndian.PutUint32(header[20:], reply.Rows)
	binary.LittleEndian.PutUint32(header[24:], reply.Columns)
	binary.LittleEndian.PutUint32(header[28:], reply.Samples)
	binary.LittleEndian.PutUint32(header[32:], uint32(len(pixels)))
	if _, err := w.Write(header[:]); err != nil {
		return err
	}
	if len(pixels) == 0 {
		return nil
	}
	_, err := w.Write(pixels)
	return err
}

func readHelperReply(r io.Reader, maxPixels uint32) (helperReply, error) {
	var header [helperReplyHeaderSize]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return helperReply{}, mapHelperIOError(err)
	}
	if string(header[:4]) != helperMagic {
		return helperReply{}, fmt.Errorf("%w: reply magic mismatch", ErrHelperProtocol)
	}
	if version := binary.LittleEndian.Uint32(header[4:]); version != HelperProtocolVersion {
		return helperReply{}, fmt.Errorf("%w: reply version %d", ErrHelperProtocol, version)
	}
	n := binary.LittleEndian.Uint32(header[32:])
	reply := helperReply{
		RequestID: binary.LittleEndian.Uint64(header[8:]),
		Status:    binary.LittleEndian.Uint32(header[16:]),
		Rows:      binary.LittleEndian.Uint32(header[20:]),
		Columns:   binary.LittleEndian.Uint32(header[24:]),
		Samples:   binary.LittleEndian.Uint32(header[28:]),
	}
	if reply.Status != helperStatusOK {
		if n > 0 {
			return helperReply{}, fmt.Errorf("%w: error reply carried %d pixel bytes", ErrHelperProtocol, n)
		}
		return reply, nil
	}
	if n > maxPixels {
		return helperReply{}, fmt.Errorf("%w: reply bytes=%d limit=%d", ErrHelperTooLarge, n, maxPixels)
	}
	if n == 0 {
		return reply, nil
	}
	reply.Pixels = make([]byte, n)
	if _, err := io.ReadFull(r, reply.Pixels); err != nil {
		return helperReply{}, mapHelperIOError(err)
	}
	return reply, nil
}

func mapHelperIOError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%w: %w", ErrHelperCrashed, err)
	}
	return err
}
