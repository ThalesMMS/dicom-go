package jpeg2000

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func encoderMetadata() pixeldata.Metadata {
	return pixeldata.Metadata{Rows: 1, Columns: 2, SamplesPerPixel: 3, BitsAllocated: 16, BitsStored: 8, HighBit: 7, NumberOfFrames: 1, PhotometricInterpretation: "RGB", PlanarConfigurationPresent: true}
}

func TestOpenJPEGEncoderMetadataAndPrecisionPacking(t *testing.T) {
	m := encoderMetadata()
	frame := []byte{1, 0, 2, 0, 3, 0, 4, 0, 5, 0, 255, 0}
	before := bytes.Clone(frame)
	if err := validateOpenJPEGEncodeFrame(frame, m, 1024); err != nil {
		t.Fatal(err)
	}
	raw, err := openJPEGPNMInput(context.Background(), frame, m)
	if err != nil || !bytes.Equal(raw, append([]byte("P6\n2 1\n255\n"), 1, 2, 3, 4, 5, 255)) {
		t.Fatalf("packing: %v %v", raw, err)
	}
	if !bytes.Equal(before, frame) {
		t.Fatal("source changed")
	}
	for _, change := range []func(*pixeldata.Metadata){func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }, func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "YBR_RCT" }, func(m *pixeldata.Metadata) { m.PlanarConfiguration = 1 }, func(m *pixeldata.Metadata) { m.BitsStored = 17 }, func(m *pixeldata.Metadata) { m.HighBit = 8 }, func(m *pixeldata.Metadata) { m.Rows = 0 }} {
		bad := m
		change(&bad)
		if !errors.Is(validateOpenJPEGEncodeFrame(frame, bad, 1024), pixeldata.ErrUnsupportedEncoderMetadata) {
			t.Fatal("unsupported metadata accepted")
		}
	}
	if !errors.Is(validateOpenJPEGEncodeFrame(frame, m, 1), ErrOpenJPEGEncoderLimit) {
		t.Fatal("frame limit")
	}
	frame[1] = 1
	if _, err := openJPEGPNMInput(context.Background(), frame, m); !errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata) {
		t.Fatal("padding clipped")
	}
	caps := (*OpenJPEGLosslessEncoder)(nil).Capabilities()
	caps.BitsAllocated[0] = 1
	if got := (*OpenJPEGLosslessEncoder)(nil).Capabilities(); got.BitsAllocated[0] != 8 || got.TransferSyntaxUID != transfer.JPEG2000LosslessOnly.UID || !got.Lossless {
		t.Fatal("capabilities")
	}
}

func TestOpenJPEGEncoderOptionsAndUnavailableRuntime(t *testing.T) {
	for _, o := range []OpenJPEGEncoderOptions{{Timeout: -time.Second}, {MaxFrameBytes: -1}, {MaxOutputBytes: 129 << 20}, {MaxConcurrent: 5}} {
		if _, err := NewOpenJPEGLosslessEncoder(context.Background(), o); !errors.Is(err, ErrOpenJPEGEncoderOptions) {
			t.Fatalf("options: %v", err)
		}
	}
	if _, err := NewOpenJPEGLosslessEncoder(context.Background(), OpenJPEGEncoderOptions{Executable: "missing-opj-compress-synthetic-CANARY"}); !errors.Is(err, ErrOpenJPEGEncoderUnavailable) {
		t.Fatalf("runtime: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := NewOpenJPEGLosslessEncoder(ctx, OpenJPEGEncoderOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation")
	}
	if err := RegisterOpenJPEGLosslessEncoder(context.Background(), nil, OpenJPEGEncoderOptions{}); !errors.Is(err, pixeldata.ErrEncoderRegistryNil) {
		t.Fatal("nil registry")
	}
}
