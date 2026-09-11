package pixeldata

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/transfer"
)

type stageFrameEncoder struct {
	capabilities EncoderCapabilities
	encode       func(context.Context, []byte, Metadata) (EncodedFrame, error)
}

func (e *stageFrameEncoder) Capabilities() EncoderCapabilities { return e.capabilities }

func (e *stageFrameEncoder) EncodeFrame(ctx context.Context, frame []byte, metadata Metadata) (EncodedFrame, error) {
	return e.encode(ctx, frame, metadata)
}

func TestTransformFramesDetachesEncoderInputsAndOutputs(t *testing.T) {
	frames := &NativeFrames{Data: [][]byte{{1, 2}, {3, 4}}}
	original := [][]byte{append([]byte(nil), frames.Data[0]...), append([]byte(nil), frames.Data[1]...)}
	returned := make([][]byte, 0, len(frames.Data))
	encoder := &stageFrameEncoder{encode: func(_ context.Context, frame []byte, _ Metadata) (EncodedFrame, error) {
		frame[0] = 0xff
		encoded := []byte{frame[0], frame[1], byte(len(returned))}
		returned = append(returned, encoded)
		return EncodedFrame{Data: encoded}, nil
	}}

	got, err := transformFrames(context.Background(), frameTransformInput{
		frames:   frames,
		metadata: Metadata{SamplesPerPixel: 1, PhotometricInterpretation: "MONOCHROME2"},
		encoder: resolvedFrameEncoder{
			encoder: encoder,
			capabilities: EncoderCapabilities{
				Lossless: true,
			},
			target: transfer.RLELossless,
		},
		maxOutputBytes: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frames.Data[0], original[0]) || !bytes.Equal(frames.Data[1], original[1]) {
		t.Fatalf("transformFrames mutated borrowed input: got %v, want %v", frames.Data, original)
	}
	if len(got.fragments) != 2 || got.encodedBytes != 6 {
		t.Fatalf("transformFrames result = %#v", got)
	}
	returned[0][0] = 0x11
	if got.fragments[0][0] != 0xff {
		t.Fatal("transformFrames retained encoder-owned output")
	}
	got.fragments[1][0] = 0x22
	if returned[1][0] != 0xff {
		t.Fatal("transformFrames result aliases encoder-owned output")
	}
}

func TestTransformFramesCancellationReturnsNoPartialResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	encoder := &stageFrameEncoder{encode: func(_ context.Context, frame []byte, _ Metadata) (EncodedFrame, error) {
		calls++
		cancel()
		return EncodedFrame{Data: append([]byte(nil), frame...)}, nil
	}}

	got, err := transformFrames(ctx, frameTransformInput{
		frames:   &NativeFrames{Data: [][]byte{{1}, {2}, {3}}},
		metadata: Metadata{SamplesPerPixel: 1, PhotometricInterpretation: "MONOCHROME2"},
		encoder: resolvedFrameEncoder{
			encoder:      encoder,
			capabilities: EncoderCapabilities{Lossless: true},
			target:       transfer.RLELossless,
		},
		maxOutputBytes: 64,
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("transformFrames() error = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("encoder calls = %d, want 1", calls)
	}
	assertEmptyTransformedFrames(t, got)
}

func TestTransformFramesFailureReturnsNoPartialResult(t *testing.T) {
	wantErr := errors.New("stage encoder failure")
	calls := 0
	encoder := &stageFrameEncoder{encode: func(_ context.Context, frame []byte, _ Metadata) (EncodedFrame, error) {
		calls++
		if calls == 2 {
			return EncodedFrame{}, wantErr
		}
		return EncodedFrame{Data: append([]byte(nil), frame...)}, nil
	}}

	got, err := transformFrames(context.Background(), frameTransformInput{
		frames:   &NativeFrames{Data: [][]byte{{1}, {2}, {3}}},
		metadata: Metadata{SamplesPerPixel: 1, PhotometricInterpretation: "MONOCHROME2"},
		encoder: resolvedFrameEncoder{
			encoder:      encoder,
			capabilities: EncoderCapabilities{Lossless: true},
			target:       transfer.RLELossless,
		},
		maxOutputBytes: 64,
	})
	if !errors.Is(err, wantErr) || !errors.Is(err, ErrEncoderFailed) {
		t.Fatalf("transformFrames() error = %v, want typed encoder failure wrapping backend cause", err)
	}
	if calls != 2 {
		t.Fatalf("encoder calls = %d, want 2", calls)
	}
	assertEmptyTransformedFrames(t, got)
}

func assertEmptyTransformedFrames(t *testing.T, got transformedFrames) {
	t.Helper()
	if got.fragments != nil || got.encodedBytes != 0 || got.delta.photometric != "" || got.delta.planar != nil || got.lossless || got.lossyMethod != "" {
		t.Fatalf("transformFrames returned partial result after error: %#v", got)
	}
}
