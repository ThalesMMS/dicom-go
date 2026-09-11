package jpegxladapter

import (
	"errors"
	"io"
	"math"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestServeHelperMapsDecoderErrorsToStatus(t *testing.T) {
	tests := []struct {
		name   string
		err    error
		status uint32
	}{
		{name: "malformed", err: ErrMalformedCodestream, status: helperStatusMalformed},
		{name: "unsupported", err: ErrUnsupportedMetadata, status: helperStatusUnsupported},
		{name: "size", err: ErrImageSizeMismatch, status: helperStatusSizeMismatch},
		{name: "pixel-size", err: pixeldata.ErrPixelDataSizeMismatch, status: helperStatusSizeMismatch},
		{name: "too-large", err: ErrHelperTooLarge, status: helperStatusTooLarge},
		{name: "other", err: errors.New("transient helper failure"), status: helperStatusProtocol},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqR, reqW := io.Pipe()
			repR, repW := io.Pipe()
			done := make(chan error, 1)
			go func() {
				done <- ServeHelper(reqR, repW, func([]byte, pixeldata.Metadata) ([]byte, error) {
					return nil, tt.err
				})
			}()
			t.Cleanup(func() {
				_ = reqW.Close()
				select {
				case <-done:
				default:
				}
			})

			if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
				t.Fatal(err)
			}
			hello, err := readHelperReply(repR, 0)
			if err != nil {
				t.Fatal(err)
			}
			if hello.Status != helperStatusOK {
				t.Fatalf("hello status = %d, want OK", hello.Status)
			}

			if err := writeHelperRequest(reqW, helperRequest{
				Opcode:        helperOpcodeDecode,
				RequestID:     2,
				Rows:          1,
				Columns:       2,
				Samples:       1,
				BitsAllocated: 8,
				Payload:       []byte("jxl"),
			}); err != nil {
				t.Fatal(err)
			}
			reply, err := readHelperReply(repR, 2)
			if err != nil {
				t.Fatal(err)
			}
			if reply.Status != tt.status {
				t.Fatalf("status = %d, want %d", reply.Status, tt.status)
			}
			if len(reply.Pixels) != 0 {
				t.Fatalf("published %d pixels after decoder error", len(reply.Pixels))
			}

			if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeShutdown, RequestID: 3}); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("ServeHelper() = %v", err)
			}
		})
	}
}

func TestServeHelperRejectsGeometryOutsideUint16(t *testing.T) {
	tests := []struct {
		name string
		req  helperRequest
	}{
		{name: "rows", req: helperRequest{Rows: math.MaxUint16 + 1, Columns: 2, Samples: 1, BitsAllocated: 8}},
		{name: "columns", req: helperRequest{Rows: 1, Columns: math.MaxUint16 + 1, Samples: 1, BitsAllocated: 8}},
		{name: "samples", req: helperRequest{Rows: 1, Columns: 2, Samples: math.MaxUint16 + 1, BitsAllocated: 8}},
		{name: "bits", req: helperRequest{Rows: 1, Columns: 2, Samples: 1, BitsAllocated: math.MaxUint16 + 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reqR, reqW := io.Pipe()
			repR, repW := io.Pipe()
			done := make(chan error, 1)
			var decoded atomic.Pointer[pixeldata.Metadata]
			go func() {
				done <- ServeHelper(reqR, repW, func(_ []byte, meta pixeldata.Metadata) ([]byte, error) {
					copied := meta
					decoded.Store(&copied)
					return []byte{1, 2}, nil
				})
			}()
			t.Cleanup(func() {
				_ = reqW.Close()
				select {
				case <-done:
				default:
				}
			})

			if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
				t.Fatal(err)
			}
			if _, err := readHelperReply(repR, 0); err != nil {
				t.Fatal(err)
			}

			tt.req.Opcode = helperOpcodeDecode
			tt.req.RequestID = 2
			tt.req.Payload = []byte("jxl")
			if err := writeHelperRequest(reqW, tt.req); err != nil {
				t.Fatal(err)
			}
			reply, err := readHelperReply(repR, 2)
			if err != nil {
				t.Fatal(err)
			}
			if reply.Status != helperStatusUnsupported {
				t.Fatalf("status = %d, want unsupported", reply.Status)
			}
			if got := decoded.Load(); got != nil {
				t.Fatalf("decode ran with wrapped metadata %+v", *got)
			}
			if len(reply.Pixels) != 0 {
				t.Fatalf("published %d pixels for out-of-range geometry", len(reply.Pixels))
			}

			if err := writeHelperRequest(reqW, helperRequest{
				Opcode:        helperOpcodeDecode,
				RequestID:     3,
				Rows:          1,
				Columns:       2,
				Samples:       1,
				BitsAllocated: 8,
				Payload:       []byte("jxl"),
			}); err != nil {
				t.Fatal(err)
			}
			ok, err := readHelperReply(repR, 2)
			if err != nil {
				t.Fatal(err)
			}
			if ok.Status != helperStatusOK {
				t.Fatalf("follow-up status = %d, want OK", ok.Status)
			}
			if ok.Rows != 1 || ok.Columns != 2 || ok.Samples != 1 {
				t.Fatalf("follow-up geometry = %d %d %d, want 1 2 1", ok.Rows, ok.Columns, ok.Samples)
			}

			if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeShutdown, RequestID: 4}); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatalf("ServeHelper() = %v", err)
			}
		})
	}
}

func TestServeHelperRepliesProtocolAndContinuesOnUnknownOpcode(t *testing.T) {
	reqR, reqW := io.Pipe()
	repR, repW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		done <- ServeHelper(reqR, repW, func([]byte, pixeldata.Metadata) ([]byte, error) {
			t.Error("decode invoked for unknown opcode")
			return nil, errors.New("unused")
		})
	}()
	t.Cleanup(func() {
		_ = reqW.Close()
		select {
		case <-done:
		default:
		}
	})

	if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeHello, RequestID: 1}); err != nil {
		t.Fatal(err)
	}
	hello, err := readHelperReply(repR, 0)
	if err != nil {
		t.Fatal(err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("hello status = %d, want OK", hello.Status)
	}

	if err := writeHelperRequest(reqW, helperRequest{
		Opcode:    99,
		RequestID: 2,
		Payload:   []byte{1, 2, 3, 4, 5, 6, 7, 8},
	}); err != nil {
		t.Fatal(err)
	}

	replyCh := make(chan helperReply, 1)
	errCh := make(chan error, 1)
	go func() {
		reply, err := readHelperReply(repR, 0)
		if err != nil {
			errCh <- err
			return
		}
		replyCh <- reply
	}()
	select {
	case err := <-done:
		t.Fatalf("ServeHelper stopped after unknown opcode: %v", err)
	case err := <-errCh:
		t.Fatalf("read unknown-opcode reply: %v", err)
	case reply := <-replyCh:
		if reply.Status != helperStatusProtocol {
			t.Fatalf("unknown opcode status = %d, want protocol", reply.Status)
		}
		if reply.RequestID != 2 {
			t.Fatalf("unknown opcode request id = %d, want 2", reply.RequestID)
		}
		if len(reply.Pixels) != 0 {
			t.Fatalf("published %d pixels for unknown opcode", len(reply.Pixels))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no protocol reply for unknown opcode")
	}

	if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeHello, RequestID: 3}); err != nil {
		t.Fatal(err)
	}
	hello, err = readHelperReply(repR, 0)
	if err != nil {
		t.Fatalf("follow-up hello after unknown opcode: %v", err)
	}
	if hello.Status != helperStatusOK {
		t.Fatalf("follow-up hello status = %d, want OK", hello.Status)
	}

	if err := writeHelperRequest(reqW, helperRequest{Opcode: helperOpcodeShutdown, RequestID: 4}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("ServeHelper() = %v", err)
	}
}
