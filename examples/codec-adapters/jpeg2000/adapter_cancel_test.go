package jpeg2000

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestDecodeContextDoesNotLaunchWhenCanceledBeforeAdmit(t *testing.T) {
	decoder := &gatedJPEG2000Decoder{output: []byte{1, 2}}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, err := NewWithDecoder(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("cancel presented as malformed codestream: %v", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-before-admit", len(frames.Data))
	}
	if decoder.launches.Load() != 0 {
		t.Fatalf("decoder launches = %d, want 0", decoder.launches.Load())
	}
}

func TestDecodeContextCancelsDuringDecodeWithoutPublishing(t *testing.T) {
	decoder := newGatedJPEG2000Decoder([]byte{1, 2}, "decode")
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	var frames pixeldata.Frames
	go func() {
		var err error
		frames, err = NewWithDecoder(decoder).DecodeContext(ctx, pixel, obj)
		errCh <- err
	}()
	decoder.waitEntered(t, "decode")
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
		}
		if errors.Is(err, ErrMalformedCodestream) {
			t.Fatalf("cancel presented as malformed codestream: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DecodeContext did not return after cancel")
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-during-decode", len(frames.Data))
	}
}

func TestDecodeContextCancelsDuringOutputWithoutPublishing(t *testing.T) {
	decoder := newGatedJPEG2000Decoder([]byte{1, 2}, "output")
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	var frames pixeldata.Frames
	go func() {
		var err error
		frames, err = NewWithDecoder(decoder).DecodeContext(ctx, pixel, obj)
		errCh <- err
	}()
	decoder.waitEntered(t, "output")
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("DecodeContext did not return after cancel during output")
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-during-output", len(frames.Data))
	}
}

func TestDecodeContextCodecTimeoutIsNotDeadlineExceeded(t *testing.T) {
	decoder := &gatedJPEG2000Decoder{err: ErrDecoderTimeout}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))

	frames, err := NewWithDecoder(decoder).DecodeContext(context.Background(), pixel, obj)
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("codec timeout wrapped context.DeadlineExceeded: %v", err)
	}
	if !errors.Is(err, ErrDecoderTimeout) {
		t.Fatalf("DecodeContext() error = %v, want ErrDecoderTimeout", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after codec timeout", len(frames.Data))
	}
}

func TestDecodeContextTimeoutIsDeadlineExceeded(t *testing.T) {
	decoder := newGatedJPEG2000Decoder([]byte{1, 2}, "decode")
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	frames, err := NewWithDecoder(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DecodeContext() error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("timeout presented as malformed codestream: %v", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after timeout", len(frames.Data))
	}
}

func TestDecodeContextPreservesLaunchError(t *testing.T) {
	decoder := &gatedJPEG2000Decoder{err: ErrOpenJPEGUnavailable}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))

	_, err := NewWithDecoder(decoder).DecodeContext(context.Background(), pixel, obj)
	if !errors.Is(err, ErrOpenJPEGUnavailable) {
		t.Fatalf("DecodeContext() error = %v, want ErrOpenJPEGUnavailable", err)
	}
	if errors.Is(err, ErrMalformedCodestream) {
		t.Fatalf("launch error presented as malformed codestream: %v", err)
	}
	if decoder.launches.Load() != 1 {
		t.Fatalf("decoder launches = %d, want 1 failed launch", decoder.launches.Load())
	}
}

func TestDecodeContextSimultaneousCompletionDoesNotPublish(t *testing.T) {
	decoder := &gatedJPEG2000Decoder{output: []byte{1, 2}, cancelAfter: true}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	ctx, cancel := context.WithCancel(context.Background())
	decoder.cancel = cancel

	frames, err := NewWithDecoder(decoder).DecodeContext(ctx, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after simultaneous completion", len(frames.Data))
	}
}

func TestDecodeContextIsolatesConcurrentCalls(t *testing.T) {
	var current atomic.Int32
	var max atomic.Int32
	decoder := &gatedJPEG2000Decoder{
		output: []byte{1, 2},
		onDecode: func() {
			n := current.Add(1)
			for {
				prev := max.Load()
				if n <= prev || max.CompareAndSwap(prev, n) {
					break
				}
			}
			time.Sleep(30 * time.Millisecond)
			current.Add(-1)
		},
	}
	obj, pixel := jpeg2000Object(t, jpeg2000MetadataOptions{}, []byte("encoded"))
	codec := NewWithDecoder(decoder)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frames, err := codec.DecodeContext(context.Background(), pixel, obj)
			if err != nil {
				errCh <- err
				return
			}
			if len(frames.Data) != 1 {
				errCh <- errors.New("missing isolated frame")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if decoder.launches.Load() != 2 {
		t.Fatalf("launches = %d, want 2 isolated calls", decoder.launches.Load())
	}
	if max.Load() < 2 {
		t.Fatalf("max concurrent decodes = %d, want overlap", max.Load())
	}
}

func TestJPEG2000CodecImplementsContextCodec(t *testing.T) {
	var codec pixeldata.Codec = NewWithDecoder(&gatedJPEG2000Decoder{output: []byte{1, 2}})
	if _, ok := codec.(pixeldata.ContextCodec); !ok {
		t.Fatal("JPEG 2000 codec does not implement pixeldata.ContextCodec")
	}
}

func TestOpenJPEGUnavailableHonorsCancelBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openJPEGDecoder{}.DecodeFrameContext(ctx, []byte("encoded"), pixeldata.Metadata{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unavailable OpenJPEG cancel error = %v, want context.Canceled", err)
	}
}

func TestOpenJPHUnavailableHonorsCancelBeforeLaunch(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := openJPHDecoder{}.DecodeFrameContext(ctx, []byte("encoded"), pixeldata.Metadata{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("unavailable OpenJPH cancel error = %v, want context.Canceled", err)
	}
}

type gatedJPEG2000Decoder struct {
	mu          sync.Mutex
	entered     map[string]chan struct{}
	blockAt     string
	output      []byte
	err         error
	cancel      context.CancelFunc
	cancelAfter bool
	onDecode    func()
	launches    atomic.Int32
}

func newGatedJPEG2000Decoder(output []byte, blockAt string) *gatedJPEG2000Decoder {
	return &gatedJPEG2000Decoder{
		entered: map[string]chan struct{}{
			"decode": make(chan struct{}),
			"output": make(chan struct{}),
		},
		blockAt: blockAt,
		output:  output,
	}
}

func (d *gatedJPEG2000Decoder) DecodeFrame(payload []byte, metadata pixeldata.Metadata) ([]byte, error) {
	return d.DecodeFrameContext(context.Background(), payload, metadata)
}

func (d *gatedJPEG2000Decoder) DecodeFrameContext(ctx context.Context, _ []byte, _ pixeldata.Metadata) ([]byte, error) {
	d.launches.Add(1)
	if d.onDecode != nil {
		d.onDecode()
	}
	if err := d.waitPhase(ctx, "decode"); err != nil {
		return nil, err
	}
	if d.err != nil {
		return nil, d.err
	}
	if err := d.waitPhase(ctx, "output"); err != nil {
		return nil, err
	}
	if d.cancelAfter && d.cancel != nil {
		d.cancel()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]byte(nil), d.output...), nil
}

func (d *gatedJPEG2000Decoder) waitPhase(ctx context.Context, phase string) error {
	if d.blockAt != phase {
		return nil
	}
	d.signalEntered(phase)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(5 * time.Second):
		return errors.New("gated decoder timed out holding " + phase)
	}
}

func (d *gatedJPEG2000Decoder) signalEntered(phase string) {
	d.mu.Lock()
	ch, ok := d.entered[phase]
	d.mu.Unlock()
	if !ok {
		return
	}
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func (d *gatedJPEG2000Decoder) waitEntered(t *testing.T, phase string) {
	t.Helper()
	d.mu.Lock()
	ch := d.entered[phase]
	d.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(2 * time.Second):
		t.Fatalf("decoder did not enter %s", phase)
	}
}
