package jpegls

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestDecodeContextDoesNotAdmitWhenCanceled(t *testing.T) {
	decoder := &countingJPEGLSDecoder{output: []byte{1, 2}}
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, err := NewLossless(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("cancel presented as malformed frame: %v", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-before-admit", len(frames.Data))
	}
	if decoder.calls.Load() != 0 {
		t.Fatalf("decoder calls = %d, want 0", decoder.calls.Load())
	}
}

func TestDecodeContextCancelsBetweenFramesWithoutPublishing(t *testing.T) {
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{numberOfFrames: 2}, []byte("a"), []byte("b"))
	ctx, cancel := context.WithCancel(context.Background())
	decoder := &countingJPEGLSDecoder{
		output: []byte{1, 2},
		after: func(call int32) {
			if call == 1 {
				cancel()
			}
		},
	}

	frames, err := NewLossless(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-between-frames", len(frames.Data))
	}
	if decoder.calls.Load() != 1 {
		t.Fatalf("decoder calls = %d, want 1 admitted frame", decoder.calls.Load())
	}
}

func TestDecodeContextTimeoutIsDeadlineExceeded(t *testing.T) {
	decoder := &countingJPEGLSDecoder{output: []byte{1, 2}}
	obj, pixel := jpeglsObject(t, jpeglsMetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(2 * time.Millisecond)

	frames, err := NewLossless(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DecodeContext() error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrMalformedFrame) {
		t.Fatalf("timeout presented as malformed frame: %v", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after timeout", len(frames.Data))
	}
	if decoder.calls.Load() != 0 {
		t.Fatalf("decoder calls = %d, want 0", decoder.calls.Load())
	}
}

func TestJPEGLSCodecImplementsContextCodec(t *testing.T) {
	var codec pixeldata.Codec = NewLossless(&countingJPEGLSDecoder{output: []byte{1, 2}})
	if _, ok := codec.(pixeldata.ContextCodec); !ok {
		t.Fatal("JPEG-LS codec does not implement pixeldata.ContextCodec")
	}
}

type countingJPEGLSDecoder struct {
	output []byte
	after  func(int32)
	calls  atomic.Int32
}

func (d *countingJPEGLSDecoder) DecodeJPEGLS(_ []byte, _ DecoderInput) ([]byte, error) {
	n := d.calls.Add(1)
	if d.after != nil {
		d.after(n)
	}
	return append([]byte(nil), d.output...), nil
}
