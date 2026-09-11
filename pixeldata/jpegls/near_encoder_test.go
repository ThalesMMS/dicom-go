package jpegls

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestNearEncoderExplicitPolicyAndMetadata(t *testing.T) {
	for _, opts := range []EncoderOptions{{Near: -1, AllowLossy: true}, {Near: 256, AllowLossy: true}, {Near: 1}} {
		if e, err := NewEncoderWithOptions(opts); err == nil || e != nil {
			t.Fatal("invalid/unauthorized NEAR accepted")
		}
	}
	e, err := NewEncoderWithOptions(EncoderOptions{})
	if err != nil || !e.Capabilities().Lossless || e.Capabilities().TransferSyntaxUID != UID {
		t.Fatal("zero policy changed")
	}
	r := pixeldata.NewMemoryEncoderRegistry()
	if err := RegisterEncoder(r); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.GetEncoder(NearLosslessUID); ok {
		t.Fatal("lossy encoder registered implicitly")
	}
	if err := RegisterNearLosslessEncoder(nil, EncoderOptions{Near: 1, AllowLossy: true}); !errors.Is(err, pixeldata.ErrEncoderRegistryNil) {
		t.Fatal(err)
	}
	if err := RegisterNearLosslessEncoder(r, EncoderOptions{AllowLossy: true}); err == nil {
		t.Fatal(".81 registered for zero NEAR")
	}
	if err := RegisterNearLosslessEncoder(r, EncoderOptions{Near: 1, AllowLossy: true}); err != nil {
		t.Fatal(err)
	}
	e, err = NewEncoderWithOptions(EncoderOptions{Near: 3, AllowLossy: true})
	if err != nil {
		t.Fatal(err)
	}
	m := encoderMetadata(1, 3, 1, 8, "MONOCHROME2")
	for _, mutate := range []func(*pixeldata.Metadata){
		func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 },
		func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "PALETTE COLOR" },
		func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "YBR_FULL" },
		func(m *pixeldata.Metadata) { m.BitsStored = 2; m.HighBit = 1 },
		func(m *pixeldata.Metadata) { m.HighBit = 6 },
	} {
		bad := m
		mutate(&bad)
		got, err := e.EncodeFrame(context.Background(), []byte{0, 127, 255}, bad)
		if !errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata) || len(got.Data) != 0 {
			t.Fatalf("unsupported profile: %v", err)
		}
	}
	if got, err := e.EncodeFrame(context.Background(), []byte{0}, m); !errors.Is(err, pixeldata.ErrPixelDataSizeMismatch) || len(got.Data) != 0 {
		t.Fatal("frame size")
	}
}

func TestNearEncoderCancellationAndConcurrentReuse(t *testing.T) {
	e, _ := NewEncoderWithOptions(EncoderOptions{Near: 1, AllowLossy: true})
	m := encoderMetadata(33, 129, 1, 8, "MONOCHROME2")
	frame := make([]byte, 33*129)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := e.EncodeFrame(ctx, frame, m); !errors.Is(err, context.Canceled) || len(got.Data) != 0 {
		t.Fatal("canceled encode published output")
	}
	ctx = &nearCancelContext{Context: context.Background(), remaining: 5}
	if got, err := e.EncodeFrame(ctx, frame, m); !errors.Is(err, context.Canceled) || len(got.Data) != 0 {
		t.Fatal("in-frame cancel published output")
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := e.EncodeFrame(context.Background(), frame, m); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
}

type nearCancelContext struct {
	context.Context
	remaining int
}

func (c *nearCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func FuzzNearEncoderBound(f *testing.F) {
	f.Add([]byte{0, 255, 1, 254, 17, 17, 18, 19}, uint8(1))
	f.Add([]byte{127, 128, 127, 128, 0, 255}, uint8(127))
	f.Fuzz(func(t *testing.T, frame []byte, nearByte uint8) {
		if len(frame) == 0 || len(frame) > 4096 {
			return
		}
		near := int(nearByte%127) + 1
		e, err := NewEncoderWithOptions(EncoderOptions{Near: near, AllowLossy: true})
		if err != nil {
			t.Fatal(err)
		}
		m := encoderMetadata(1, uint16(len(frame)), 1, 8, "MONOCHROME2")
		got, err := e.EncodeFrame(context.Background(), frame, m)
		if err != nil {
			t.Fatal(err)
		}
		recon, err := decodeFrameModeContext(context.Background(), got.Data, m, true)
		if err != nil {
			t.Fatal(err)
		}
		for i, v := range frame {
			if absInt(int(v)-int(recon[i])) > near {
				t.Fatalf("bound at %d", i)
			}
		}
	})
}
