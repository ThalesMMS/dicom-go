package jpegls

import (
	"bytes"
	"context"
	"testing"
)

func FuzzEncodeFrameMono8(f *testing.F) {
	f.Add([]byte{0, 64, 128, 255})
	f.Add(bytes.Repeat([]byte{7}, 15))
	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) == 0 {
			return
		}
		columns := len(frame)
		if columns > 32 {
			columns = 32
			frame = frame[:32]
		}
		metadata := encoderMetadata(1, uint16(columns), 1, 8, "MONOCHROME2")
		encoded, err := NewEncoder().EncodeFrame(context.Background(), frame, metadata)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeFrame(encoded.Data, metadata)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, frame) {
			t.Fatalf("round trip = % x, want % x", decoded, frame)
		}
	})
}

func FuzzEncodeFrameMono16(f *testing.F) {
	f.Add([]byte{0, 0, 1, 0, 0, 128, 255, 255})
	f.Add(bytes.Repeat([]byte{0x55, 0xaa}, 8))
	f.Fuzz(func(t *testing.T, frame []byte) {
		if len(frame) < 2 {
			return
		}
		if len(frame) > 64 {
			frame = frame[:64]
		}
		frame = frame[:len(frame)&^1]
		columns := len(frame) / 2
		metadata := encoderMetadata(1, uint16(columns), 1, 16, "MONOCHROME2")
		encoded, err := NewEncoder().EncodeFrame(context.Background(), frame, metadata)
		if err != nil {
			t.Fatal(err)
		}
		decoded, err := decodeFrame(encoded.Data, metadata)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(decoded, frame) {
			t.Fatalf("round trip = % x, want % x", decoded, frame)
		}
	})
}

func FuzzDecodeHostileCodestream(f *testing.F) {
	valid, err := NewEncoder().EncodeFrame(context.Background(), []byte{0, 64, 128, 255}, encoderMetadata(2, 2, 1, 8, "MONOCHROME2"))
	if err != nil {
		f.Fatal(err)
	}
	f.Add(valid.Data)
	f.Add(charLSLossless2x2)
	f.Add([]byte{0xff, 0xd8, 0xff, 0xd9})
	f.Add([]byte{0xff, 0xd8})
	f.Fuzz(func(t *testing.T, stream []byte) {
		_, _ = decodeFrame(stream, encoderMetadata(2, 2, 1, 8, "MONOCHROME2"))
	})
}
