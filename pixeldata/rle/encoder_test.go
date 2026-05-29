package rle

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/pixeldata"
)

func TestEncoderMono8GoldenPreservesRowBoundaries(t *testing.T) {
	encoder := NewEncoder()
	metadata := encoderMetadata(2, 4, 1, 8, "MONOCHROME2")

	encoded, err := encoder.EncodeFrame(context.Background(), []byte{1, 2, 3, 4, 4, 4, 4, 4}, metadata)
	if err != nil {
		t.Fatal(err)
	}

	want := make([]byte, 64)
	binary.LittleEndian.PutUint32(want[0:4], 1)
	binary.LittleEndian.PutUint32(want[4:8], 64)
	want = append(want, 0x03, 1, 2, 3, 4, 0xfd, 4, 0)
	if !bytes.Equal(encoded.Data, want) {
		t.Fatalf("EncodeFrame() = % x, want % x", encoded.Data, want)
	}
	if encoded.PhotometricInterpretation != "" || encoded.PlanarConfiguration != nil {
		t.Fatalf("EncodeFrame() metadata transforms = %#v, want none", encoded)
	}
}

func TestEncoderMono16GoldenUsesMostSignificantBytePlaneFirst(t *testing.T) {
	encoded, err := NewEncoder().EncodeFrame(
		context.Background(),
		[]byte{0x02, 0x01, 0xb2, 0xa1},
		encoderMetadata(1, 2, 1, 16, "MONOCHROME2"),
	)
	if err != nil {
		t.Fatal(err)
	}

	want := make([]byte, 64)
	binary.LittleEndian.PutUint32(want[0:4], 2)
	binary.LittleEndian.PutUint32(want[4:8], 64)
	binary.LittleEndian.PutUint32(want[8:12], 68)
	want = append(want,
		0x01, 0x01, 0xa1, 0x00,
		0x01, 0x02, 0xb2, 0x00,
	)
	if !bytes.Equal(encoded.Data, want) {
		t.Fatalf("EncodeFrame() = % x, want % x", encoded.Data, want)
	}
}

func TestEncoderRGB8GoldenUsesSampleOrderAndEvenSegmentPadding(t *testing.T) {
	metadata := encoderMetadata(1, 2, 3, 8, "RGB")
	encoded, err := NewEncoder().EncodeFrame(
		context.Background(),
		[]byte{1, 2, 3, 4, 5, 6},
		metadata,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := make([]byte, 64)
	binary.LittleEndian.PutUint32(want[0:4], 3)
	binary.LittleEndian.PutUint32(want[4:8], 64)
	binary.LittleEndian.PutUint32(want[8:12], 68)
	binary.LittleEndian.PutUint32(want[12:16], 72)
	want = append(want,
		0x01, 1, 4, 0,
		0x01, 2, 5, 0,
		0x01, 3, 6, 0,
	)
	if !bytes.Equal(encoded.Data, want) {
		t.Fatalf("EncodeFrame() = % x, want % x", encoded.Data, want)
	}
}

func TestEncoderPackBitsRunBoundaries(t *testing.T) {
	tests := []struct {
		name string
		row  []byte
		want []byte
	}{
		{name: "two bytes remain literal", row: bytes.Repeat([]byte{7}, 2), want: []byte{0x01, 7, 7, 0}},
		{name: "three bytes replicate", row: bytes.Repeat([]byte{7}, 3), want: []byte{0xfe, 7}},
		{name: "maximum replicate", row: bytes.Repeat([]byte{7}, 128), want: []byte{0x81, 7}},
		{name: "replicate and one literal", row: bytes.Repeat([]byte{7}, 129), want: []byte{0x81, 7, 0x00, 7}},
		{name: "replicate and two literals", row: bytes.Repeat([]byte{7}, 130), want: []byte{0x81, 7, 0x01, 7, 7, 0}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := NewEncoder().EncodeFrame(
				context.Background(),
				tt.row,
				encoderMetadata(1, uint16(len(tt.row)), 1, 8, "MONOCHROME2"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := encoded.Data[64:]; !bytes.Equal(got, tt.want) {
				t.Fatalf("encoded segment = % x, want % x", got, tt.want)
			}
		})
	}
}

func TestEncoderPackBitsLiteralBoundaries(t *testing.T) {
	row := make([]byte, 129)
	for i := range row {
		row[i] = byte(i)
	}
	encoded, err := NewEncoder().EncodeFrame(
		context.Background(), row, encoderMetadata(1, uint16(len(row)), 1, 8, "MONOCHROME2"),
	)
	if err != nil {
		t.Fatal(err)
	}

	want := append([]byte{0x7f}, row[:128]...)
	want = append(want, 0x00, row[128])
	if len(want)%2 != 0 {
		want = append(want, 0)
	}
	if got := encoded.Data[64:]; !bytes.Equal(got, want) {
		t.Fatalf("encoded segment = % x, want % x", got, want)
	}
}

func TestEncoderRoundTripSupportedNativeFrames(t *testing.T) {
	tests := []struct {
		name     string
		metadata pixeldata.Metadata
		frame    []byte
	}{
		{
			name:     "monochrome 8",
			metadata: encoderMetadata(2, 4, 1, 8, "MONOCHROME1"),
			frame:    []byte{1, 1, 1, 2, 3, 4, 5, 6},
		},
		{
			name:     "palette 8",
			metadata: encoderMetadata(1, 4, 1, 8, "PALETTE COLOR"),
			frame:    []byte{1, 2, 3, 4},
		},
		{
			name:     "monochrome 16",
			metadata: encoderMetadata(2, 2, 1, 16, "MONOCHROME2"),
			frame:    []byte{0x34, 0x12, 0xff, 0x00, 0x00, 0x80, 0xcd, 0xab},
		},
		{
			name:     "RGB 8",
			metadata: encoderMetadata(1, 3, 3, 8, "RGB"),
			frame:    []byte{1, 2, 3, 4, 5, 6, 7, 8, 9},
		},
		{
			name:     "RGB 16",
			metadata: encoderMetadata(1, 2, 3, 16, "RGB"),
			frame:    []byte{1, 0x10, 2, 0x20, 3, 0x30, 4, 0x40, 5, 0x50, 6, 0x60},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			encoded, err := NewEncoder().EncodeFrame(context.Background(), tt.frame, tt.metadata)
			if err != nil {
				t.Fatal(err)
			}
			obj, pixel := rleObjectWithFragment(
				t,
				tt.metadata.Rows,
				tt.metadata.Columns,
				tt.metadata.SamplesPerPixel,
				tt.metadata.BitsAllocated,
				tt.metadata.PhotometricInterpretation,
				encoded.Data,
			)
			decoded, err := New().Decode(pixel, obj)
			if err != nil {
				t.Fatal(err)
			}
			if len(decoded.Data) != 1 || !bytes.Equal(decoded.Data[0], tt.frame) {
				t.Fatalf("round trip = % x, want % x", decoded.Data, tt.frame)
			}
		})
	}
}

func TestEncoderAcceptsTwelveStoredBitsAndSignedOrUnsignedPixels(t *testing.T) {
	frame := []byte{0x34, 0x02, 0xff, 0x07, 0x00, 0x08, 0xcd, 0x0a}
	for _, representation := range []uint16{0, 1} {
		metadata := encoderMetadata(1, 4, 1, 16, "MONOCHROME2")
		metadata.BitsStored = 12
		metadata.HighBit = 11
		metadata.PixelRepresentation = representation

		encoded, err := NewEncoder().EncodeFrame(context.Background(), frame, metadata)
		if err != nil {
			t.Fatalf("PixelRepresentation=%d: %v", representation, err)
		}
		obj, pixel := rleObjectWithFragment(t, 1, 4, 1, 16, "MONOCHROME2", encoded.Data)
		decoded, err := New().Decode(pixel, obj)
		if err != nil {
			t.Fatal(err)
		}
		if len(decoded.Data) != 1 || !bytes.Equal(decoded.Data[0], frame) {
			t.Fatalf("PixelRepresentation=%d round trip = % x, want % x", representation, decoded.Data, frame)
		}
	}
}

func TestEncoderIsSafeForConcurrentUse(t *testing.T) {
	const workers = 16
	encoder := NewEncoder()
	metadata := encoderMetadata(2, 4, 1, 8, "MONOCHROME2")
	frame := []byte{1, 2, 3, 4, 4, 4, 4, 4}
	want, err := encoder.EncodeFrame(context.Background(), frame, metadata)
	if err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, workers)
	for range workers {
		go func() {
			got, err := encoder.EncodeFrame(context.Background(), frame, metadata)
			if err == nil && !bytes.Equal(got.Data, want.Data) {
				err = errors.New("concurrent encoding was not deterministic")
			}
			errs <- err
		}()
	}
	for range workers {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEncoderCapabilitiesAndExplicitRegistration(t *testing.T) {
	encoder := NewEncoder()
	want := pixeldata.EncoderCapabilities{
		TransferSyntaxUID:          UID,
		BitsAllocated:              []uint16{8, 16},
		PixelRepresentations:       []uint16{0, 1},
		SamplesPerPixel:            []uint16{1, 3},
		PhotometricInterpretations: []string{"MONOCHROME1", "MONOCHROME2", "PALETTE COLOR", "RGB"},
		Lossless:                   true,
		SupportsMultiFrame:         true,
		Backend:                    "pure-go",
	}
	if got := encoder.Capabilities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Capabilities() = %#v, want %#v", got, want)
	}

	mutated := encoder.Capabilities()
	mutated.BitsAllocated[0] = 1
	mutated.PhotometricInterpretations[0] = "changed"
	if got := encoder.Capabilities(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Capabilities() after caller mutation = %#v, want %#v", got, want)
	}

	if err := RegisterEncoder(nil); !errors.Is(err, pixeldata.ErrEncoderRegistryNil) {
		t.Fatalf("RegisterEncoder(nil) error = %v, want ErrEncoderRegistryNil", err)
	}
	registry := pixeldata.NewMemoryEncoderRegistry()
	if err := RegisterEncoder(registry); err != nil {
		t.Fatal(err)
	}
	if registered, ok := registry.GetEncoder(UID); !ok || registered == nil {
		t.Fatal("registered RLE encoder not found")
	}
	encoded, err := registry.EncodeFrame(
		context.Background(), UID, []byte{1, 2}, encoderMetadata(1, 2, 1, 8, "MONOCHROME2"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded.Data) == 0 {
		t.Fatal("registered encoder returned an empty frame")
	}
}

func TestEncoderRejectsInvalidMetadataAndFrameLengths(t *testing.T) {
	valid := encoderMetadata(1, 2, 1, 8, "MONOCHROME2")
	tests := []struct {
		name     string
		metadata pixeldata.Metadata
		frame    []byte
		want     error
	}{
		{name: "zero rows", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.Rows = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "zero columns", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.Columns = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "allocated 12", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.BitsAllocated = 12 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "zero stored bits", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.BitsStored = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "stored exceeds allocated", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.BitsStored = 9 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "high bit mismatch", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.HighBit = 6 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "pixel representation", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PixelRepresentation = 2 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "signed palette", metadata: withMetadata(encoderMetadata(1, 2, 1, 8, "PALETTE COLOR"), func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "signed RGB", metadata: withMetadata(encoderMetadata(1, 1, 3, 8, "RGB"), func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }), frame: []byte{1, 2, 3}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "samples per pixel", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.SamplesPerPixel = 2 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "number of frames", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.NumberOfFrames = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "planar one", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PlanarConfigurationPresent = true; m.PlanarConfiguration = 1 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "wrong monochrome photometric", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "RGB" }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "RGB without planar configuration", metadata: withMetadata(encoderMetadata(1, 1, 3, 8, "RGB"), func(m *pixeldata.Metadata) { m.PlanarConfigurationPresent = false }), frame: []byte{1, 2, 3}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "short frame", metadata: valid, frame: []byte{1}, want: pixeldata.ErrPixelDataSizeMismatch},
		{name: "long frame", metadata: valid, frame: []byte{1, 2, 3}, want: pixeldata.ErrPixelDataSizeMismatch},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewEncoder().EncodeFrame(context.Background(), tt.frame, tt.metadata)
			if !errors.Is(err, tt.want) {
				t.Fatalf("EncodeFrame() error = %v, want %v", err, tt.want)
			}
		})
	}

	monoWithPlanarZero := withMetadata(valid, func(m *pixeldata.Metadata) { m.PlanarConfigurationPresent = true })
	if _, err := NewEncoder().EncodeFrame(context.Background(), []byte{1, 2}, monoWithPlanarZero); err != nil {
		t.Fatalf("monochrome PlanarConfiguration=0 error = %v", err)
	}
}

func TestEncoderRejectsFragmentLengthOverflowBeforeAllocating(t *testing.T) {
	metadata := encoderMetadata(65535, 65535, 1, 8, "MONOCHROME2")
	_, err := NewEncoder().EncodeFrame(context.Background(), nil, metadata)
	if !errors.Is(err, pixeldata.ErrEncoderOutputInvalid) {
		t.Fatalf("EncodeFrame() error = %v, want ErrEncoderOutputInvalid", err)
	}
}

func TestEncoderHonorsContextCancellationBetweenRows(t *testing.T) {
	ctx := &cancelAfterErrorChecks{Context: context.Background(), cancelAt: 4}
	_, err := NewEncoder().EncodeFrame(
		ctx,
		[]byte{1, 2, 3, 4},
		encoderMetadata(2, 2, 1, 8, "MONOCHROME2"),
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("EncodeFrame() error = %v, want context.Canceled", err)
	}
}

func BenchmarkEncoderMono8(b *testing.B) {
	frame := make([]byte, 512*512)
	for i := range frame {
		frame[i] = byte(i*37 + i/512)
	}
	metadata := encoderMetadata(512, 512, 1, 8, "MONOCHROME2")
	benchmarkEncoderWithPeakHeap(b, frame, metadata)
}

func BenchmarkEncoderRGB16(b *testing.B) {
	frame := make([]byte, 512*512*3*2)
	for i := range frame {
		frame[i] = byte(i*19 + i/1024)
	}
	metadata := encoderMetadata(512, 512, 3, 16, "RGB")
	benchmarkEncoderWithPeakHeap(b, frame, metadata)
}

func benchmarkEncoderWithPeakHeap(b *testing.B, frame []byte, metadata pixeldata.Metadata) {
	encoder := NewEncoder()
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var peakDelta atomic.Uint64
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Microsecond)
		defer ticker.Stop()
		for {
			var current runtime.MemStats
			runtime.ReadMemStats(&current)
			if current.HeapAlloc > baseline.HeapAlloc {
				delta := current.HeapAlloc - baseline.HeapAlloc
				for previous := peakDelta.Load(); delta > previous && !peakDelta.CompareAndSwap(previous, delta); previous = peakDelta.Load() {
				}
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	b.ResetTimer()
	for range b.N {
		if _, err := encoder.EncodeFrame(context.Background(), frame, metadata); err != nil {
			b.Fatal(err)
		}
	}
	b.StopTimer()
	close(stop)
	<-done
	b.ReportMetric(float64(peakDelta.Load()), "peak-heap-delta-bytes")
}

type cancelAfterErrorChecks struct {
	context.Context
	checks   int
	cancelAt int
}

func (c *cancelAfterErrorChecks) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

func withMetadata(metadata pixeldata.Metadata, change func(*pixeldata.Metadata)) pixeldata.Metadata {
	change(&metadata)
	return metadata
}

func encoderMetadata(rows, columns, samplesPerPixel, bitsAllocated uint16, photometric string) pixeldata.Metadata {
	return pixeldata.Metadata{
		Rows:                       rows,
		Columns:                    columns,
		SamplesPerPixel:            samplesPerPixel,
		BitsAllocated:              bitsAllocated,
		BitsStored:                 bitsAllocated,
		HighBit:                    bitsAllocated - 1,
		PixelRepresentation:        0,
		PlanarConfiguration:        0,
		PlanarConfigurationPresent: samplesPerPixel > 1,
		NumberOfFrames:             1,
		PhotometricInterpretation:  photometric,
	}
}
