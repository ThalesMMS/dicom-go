package encapsulated

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestStreamMarkerSeamsAndPreallocationLimits(t *testing.T) {
	for _, syntax := range []transfer.Syntax{transfer.JPEGBaseline, transfer.JPEGLSLossless} {
		data := boundaryStream([]byte{1, 0xff, 0, 2, 3})
		for cut := 2; cut < len(data); cut += 2 {
			calls := 0
			s, err := NewStream(context.Background(), parser.EncodedFrameSinkFunc(func(f parser.EncodedFrame) error {
				calls++
				if !bytes.Equal(f.Data, data) {
					t.Fatal("seam changed bytes")
				}
				return nil
			}), Limits{})
			if err != nil {
				t.Fatal(err)
			}
			if err := s.StartPixelData(parser.FrameMetadata{NumberOfFrames: 2}, syntax); err != nil {
				t.Fatal(err)
			}
			if err := s.Item(0, true, bytes.NewReader(nil)); err != nil {
				t.Fatal(err)
			}
			for _, part := range [][]byte{data[:cut], data[cut:], data} {
				if err := s.Item(uint32(len(part)), false, bytes.NewReader(part)); err != nil {
					t.Fatalf("cut=%d: %v", cut, err)
				}
			}
			if err := s.EndPixelData(); err != nil || calls != 2 {
				t.Fatalf("end: %v calls=%d", err, calls)
			}
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	reader := &cancelChunkReader{cancel: cancel}
	s, _ := NewStream(ctx, parser.EncodedFrameSinkFunc(func(parser.EncodedFrame) error { t.Fatal("partial frame"); return nil }), Limits{MaxFrameBytes: 128 << 10})
	if err := s.StartPixelData(parser.FrameMetadata{NumberOfFrames: 1}, transfer.JPEGBaseline); err != nil {
		t.Fatal(err)
	}
	if err := s.Item(0, true, bytes.NewReader(nil)); err != nil {
		t.Fatal(err)
	}
	if err := s.Item(256<<10, false, reader); !errors.Is(err, ErrResourceLimit) || reader.calls != 0 {
		t.Fatalf("limit read payload: %v", err)
	}
	if err := s.Item(64<<10, false, reader); !errors.Is(err, context.Canceled) || reader.calls != 1 {
		t.Fatalf("mid-read cancel: %v calls=%d", err, reader.calls)
	}
}

func TestStreamClosePreventsFurtherDelivery(t *testing.T) {
	calls := 0
	s, err := NewStream(context.Background(), parser.EncodedFrameSinkFunc(func(parser.EncodedFrame) error { calls++; return nil }), Limits{})
	if err != nil {
		t.Fatal(err)
	}
	s.Close()
	if err := s.StartPixelData(parser.FrameMetadata{NumberOfFrames: 1}, transfer.JPEGBaseline); !errors.Is(err, context.Canceled) || calls != 0 {
		t.Fatalf("closed stream continued: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if s, err := NewStream(ctx, parser.EncodedFrameSinkFunc(nil), Limits{}); s != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled stream: %v", err)
	}
}

type cancelChunkReader struct {
	calls  int
	cancel context.CancelFunc
}

func (r *cancelChunkReader) Read(p []byte) (int, error) {
	r.calls++
	clear(p)
	r.cancel()
	return len(p), nil
}

func FuzzEncodedStreamItems(f *testing.F) {
	f.Add(boundaryStream([]byte{1, 2, 3}), uint16(2), byte(0))
	f.Add(boundaryStream([]byte{1, 0xff, 0, 3}), uint16(4), byte(3))
	f.Fuzz(func(t *testing.T, data []byte, cut uint16, mode byte) {
		if len(data) > 64<<10 || len(data) < 4 {
			return
		}
		data = append([]byte(nil), data...)
		if len(data)&1 != 0 {
			data = append(data, 0)
		}
		split := int(cut) % len(data) &^ 1
		if split < 2 || split == len(data) {
			return
		}
		syntax := transfer.JPEGBaseline
		if mode&1 != 0 {
			syntax = transfer.JPEGLSLossless
		}
		calls := 0
		s, _ := NewStream(context.Background(), parser.EncodedFrameSinkFunc(func(frame parser.EncodedFrame) error {
			if frame.Index != calls || len(frame.Data) > 128<<10 {
				t.Fatal("unbounded/invalid output")
			}
			calls++
			return nil
		}), Limits{MaxFrames: 2, MaxFragments: 3, MaxFragmentsPerFrame: 3, MaxBytes: 256 << 10, MaxFrameBytes: 128 << 10})
		defer s.Close()
		if err := s.StartPixelData(parser.FrameMetadata{NumberOfFrames: 2}, syntax); err != nil {
			t.Fatal(err)
		}
		var bot []byte
		if mode&2 != 0 {
			bot = make([]byte, 8)
			binary.LittleEndian.PutUint32(bot[4:], uint32(len(data)+16))
		}
		if err := s.Item(uint32(len(bot)), true, bytes.NewReader(bot)); err != nil {
			return
		}
		for _, part := range [][]byte{data[:split], data[split:], boundaryStream([]byte{3, 4})} {
			if err := s.Item(uint32(len(part)), false, bytes.NewReader(part)); err != nil {
				return
			}
		}
		if err := s.EndPixelData(); err == nil && calls != 2 {
			t.Fatal("false success")
		}
	})
}

var _ io.Reader = (*cancelChunkReader)(nil)
