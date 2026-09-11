package pixeldata

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const decodeContextTestUID = "1.2.826.0.1.3680043.8.498.922"

func TestDecodeFramesContextDoesNotAdmitCanceledCall(t *testing.T) {
	codec := &recordingContextCodec{frames: Frames{Rows: 1, Columns: 2, Data: [][]byte{{9, 8}}}}
	registry, obj, pixel := registryWithContextCodec(t, codec)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, err := registry.DecodeFramesContext(ctx, decodeContextTestUID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-before-admit", len(frames.Data))
	}
	if codec.contextCalls.Load() != 0 || codec.legacyCalls.Load() != 0 {
		t.Fatalf("codec calls context=%d legacy=%d, want no admission", codec.contextCalls.Load(), codec.legacyCalls.Load())
	}
}

func TestDecodeFramesContextCancelsDuringDecodeWithoutPublishing(t *testing.T) {
	started := make(chan struct{})
	codec := &blockingContextCodec{
		started: started,
		block:   make(chan struct{}),
		frames:  Frames{Rows: 1, Columns: 2, Data: [][]byte{{1, 2}}},
	}
	registry, obj, pixel := registryWithContextCodec(t, codec)
	ctx, cancel := context.WithCancel(context.Background())

	errCh := make(chan error, 1)
	var frames Frames
	go func() {
		var err error
		frames, err = registry.DecodeFramesContext(ctx, decodeContextTestUID, pixel, obj)
		errCh <- err
	}()

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for decode admission")
	}
	cancel()

	select {
	case err := <-errCh:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("DecodeFramesContext() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("decode did not return after cancel")
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after cancel-during-decode", len(frames.Data))
	}
}

func TestDecodeFramesContextTimeoutIsDeadlineExceeded(t *testing.T) {
	codec := &blockingContextCodec{
		started: make(chan struct{}, 1),
		block:   make(chan struct{}),
		frames:  Frames{Rows: 1, Columns: 2, Data: [][]byte{{1, 2}}},
	}
	registry, obj, pixel := registryWithContextCodec(t, codec)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	frames, err := registry.DecodeFramesContext(ctx, decodeContextTestUID, pixel, obj)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DecodeFramesContext() error = %v, want context.DeadlineExceeded", err)
	}
	if errors.Is(err, ErrCodecDecodeFailed) {
		t.Fatalf("timeout presented as codec decode failure: %v", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after timeout", len(frames.Data))
	}
}

func TestDecodeFramesContextSimultaneousCompletionDoesNotPublish(t *testing.T) {
	codec := &recordingContextCodec{
		frames:      Frames{Rows: 1, Columns: 2, Data: [][]byte{{4, 5}}},
		cancelAfter: true,
	}
	registry, obj, pixel := registryWithContextCodec(t, codec)
	ctx, cancel := context.WithCancel(context.Background())
	codec.cancel = cancel

	frames, err := registry.DecodeFramesContext(ctx, decodeContextTestUID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d frames after simultaneous completion and cancel", len(frames.Data))
	}
}

func TestDecodeFramesContextIsolatesConcurrentCalls(t *testing.T) {
	var current atomic.Int32
	var max atomic.Int32
	codec := &recordingContextCodec{
		frames: Frames{Rows: 1, Columns: 2, Data: [][]byte{{1, 2}}},
		onContext: func(ctx context.Context) {
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
	registry, obj, pixel := registryWithContextCodec(t, codec)

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frames, err := registry.DecodeFramesContext(context.Background(), decodeContextTestUID, pixel, obj)
			if err != nil {
				errCh <- err
				return
			}
			if len(frames.Data) != 1 || frames.Data[0][0] != 1 {
				errCh <- errors.New("unexpected frames from concurrent decode")
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	if codec.contextCalls.Load() != 2 {
		t.Fatalf("context calls = %d, want 2 isolated decodes", codec.contextCalls.Load())
	}
	if max.Load() < 2 {
		t.Fatalf("max concurrent decodes = %d, want overlapping isolated calls", max.Load())
	}
}

func TestDecodeFramesContextFallsBackToLegacyDecode(t *testing.T) {
	codec := &fakeCodec{frames: Frames{Rows: 1, Columns: 2, Data: [][]byte{{7, 8}}}}
	registry := NewMemoryRegistry()
	if err := registry.RegisterCodec(decodeContextTestUID, codec); err != nil {
		t.Fatal(err)
	}
	obj, pixel := testEncapsulatedPixelObject(t)

	frames, err := registry.DecodeFramesContext(context.Background(), decodeContextTestUID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if codec.calls != 1 {
		t.Fatalf("legacy Decode calls = %d, want 1", codec.calls)
	}
	if len(frames.Data) != 1 || frames.Data[0][0] != 7 {
		t.Fatalf("frames = %#v, want legacy output", frames)
	}
}

func TestPackageDecodeFramesContextUsesDefaultRegistry(t *testing.T) {
	codec := &recordingContextCodec{frames: Frames{Rows: 1, Columns: 2, Data: [][]byte{{3, 4}}}}
	previous := DefaultRegistry
	t.Cleanup(func() { DefaultRegistry = previous })
	DefaultRegistry = NewMemoryRegistry()
	if err := DefaultRegistry.RegisterCodec(decodeContextTestUID, codec); err != nil {
		t.Fatal(err)
	}
	obj, pixel := testEncapsulatedPixelObject(t)

	frames, err := DecodeFramesContext(context.Background(), decodeContextTestUID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if codec.contextCalls.Load() != 1 {
		t.Fatalf("context calls = %d, want 1", codec.contextCalls.Load())
	}
	if len(frames.Data) != 1 || frames.Data[0][0] != 3 {
		t.Fatalf("frames = %#v", frames)
	}
}

func registryWithContextCodec(t *testing.T, codec Codec) (*MemoryRegistry, *object.Object, PixelData) {
	t.Helper()
	registry := NewMemoryRegistry()
	if err := registry.RegisterCodec(decodeContextTestUID, codec); err != nil {
		t.Fatal(err)
	}
	obj, pixel := testEncapsulatedPixelObject(t)
	return registry, obj, pixel
}

type recordingContextCodec struct {
	frames       Frames
	err          error
	cancel       context.CancelFunc
	cancelAfter  bool
	onContext    func(context.Context)
	contextCalls atomic.Int32
	legacyCalls  atomic.Int32
}

func (c *recordingContextCodec) Decode(PixelData, *object.Object) (Frames, error) {
	c.legacyCalls.Add(1)
	if c.err != nil {
		return Frames{}, c.err
	}
	return c.frames, nil
}

func (c *recordingContextCodec) DecodeContext(ctx context.Context, _ PixelData, _ *object.Object) (Frames, error) {
	c.contextCalls.Add(1)
	if c.onContext != nil {
		c.onContext(ctx)
	}
	if c.cancelAfter && c.cancel != nil {
		c.cancel()
	}
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}
	if c.err != nil {
		return Frames{}, c.err
	}
	return c.frames, nil
}

type blockingContextCodec struct {
	started chan struct{}
	block   chan struct{}
	frames  Frames
}

func (c *blockingContextCodec) Decode(PixelData, *object.Object) (Frames, error) {
	return Frames{}, errors.New("legacy Decode should not run")
}

func (c *blockingContextCodec) DecodeContext(ctx context.Context, _ PixelData, _ *object.Object) (Frames, error) {
	if c.started != nil {
		select {
		case <-c.started:
		default:
			close(c.started)
		}
	}
	select {
	case <-ctx.Done():
		return Frames{}, ctx.Err()
	case <-c.block:
		return c.frames, nil
	}
}

func TestDecodeFramesContextDecodesNativeWithoutCodec(t *testing.T) {
	obj, pixel := nativePixelObject(t)

	frames, err := NewMemoryRegistry().DecodeFramesContext(context.Background(), transfer.ExplicitVRLittleEndian.UID, pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if frames.Rows != 8 || frames.Columns != 8 || len(frames.Data) != 1 {
		t.Fatalf("native DecodeFramesContext() = %#v", frames)
	}
}

func TestDecodeFramesContextDoesNotAdmitCanceledNativeCall(t *testing.T) {
	obj, pixel := nativePixelObject(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, err := NewMemoryRegistry().DecodeFramesContext(ctx, transfer.ExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("native DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d native frames after cancel-before-admit", len(frames.Data))
	}
}

func TestDecodeFramesContextDoesNotPublishNativeFramesAfterCancel(t *testing.T) {
	obj, pixel := nativePixelObject(t)
	ctx := newErrAfterCallsContext(2)

	frames, err := NewMemoryRegistry().DecodeFramesContext(ctx, transfer.ExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("native DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d native frames after cancel", len(frames.Data))
	}
}

func TestDecodeFramesContextDoesNotAdmitCanceledEncapsulatedUncompressedCall(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, nil, []byte{0, 1}, []byte{2, 3})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	frames, err := NewMemoryRegistry().DecodeFramesContext(ctx, transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("encapsulated-uncompressed DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d encapsulated-uncompressed frames after cancel-before-admit", len(frames.Data))
	}
}

func TestDecodeFramesContextDoesNotPublishEncapsulatedUncompressedFramesAfterCancel(t *testing.T) {
	obj, pixel := encapsulatedUncompressedPixelObject(t, 2, 2, nil, []byte{0, 1}, []byte{2, 3})
	ctx := newErrAfterCallsContext(2)

	frames, err := NewMemoryRegistry().DecodeFramesContext(ctx, transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID, pixel, obj)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("encapsulated-uncompressed DecodeFramesContext() error = %v, want context.Canceled", err)
	}
	if len(frames.Data) != 0 {
		t.Fatalf("published %d encapsulated-uncompressed frames after cancel", len(frames.Data))
	}
}

func nativePixelObject(t *testing.T) (*object.Object, PixelData) {
	t.Helper()
	raw := sequentialBytes(64)
	obj := object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, nil,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)
	pixel, err := Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func newErrAfterCallsContext(after int32) context.Context {
	return &errAfterCallsContext{Context: context.Background(), after: after}
}

type errAfterCallsContext struct {
	context.Context
	after int32
	calls atomic.Int32
}

func (c *errAfterCallsContext) Err() error {
	if c.calls.Add(1) >= c.after {
		return context.Canceled
	}
	return nil
}
