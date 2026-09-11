package jpegls

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestEncoderRegistersOnlyLosslessUID(t *testing.T) {
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
	if encoder.Capabilities().TransferSyntaxUID == transfer.JPEGLSNearLossless.UID {
		t.Fatal("encoder must not advertise Near-Lossless UID 1.2.840.10008.1.2.4.81")
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
	if _, ok := registry.GetEncoder(UID); !ok {
		t.Fatal("registered JPEG-LS lossless encoder not found")
	}
	if _, ok := registry.GetEncoder(transfer.JPEGLSNearLossless.UID); ok {
		t.Fatal("Near-Lossless encoder must not be registered")
	}
	if err := pixeldata.CheckEncoderAvailability(registry, transfer.JPEGLSNearLossless.UID); err == nil {
		t.Fatal("CheckEncoderAvailability(Near-Lossless) = nil, want encoder missing")
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
		{name: "one stored bit", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.BitsStored = 1; m.HighBit = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "stored exceeds allocated", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.BitsStored = 9 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "high bit mismatch", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.HighBit = 6 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "pixel representation", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PixelRepresentation = 2 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "signed palette", metadata: withMetadata(encoderMetadata(1, 2, 1, 8, "PALETTE COLOR"), func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "signed RGB", metadata: withMetadata(encoderMetadata(1, 1, 3, 8, "RGB"), func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }), frame: []byte{1, 2, 3}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "samples per pixel", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.SamplesPerPixel = 2 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "number of frames", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.NumberOfFrames = 0 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "planar one", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PlanarConfigurationPresent = true; m.PlanarConfiguration = 1 }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "wrong monochrome photometric", metadata: withMetadata(valid, func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "RGB" }), frame: []byte{1, 2}, want: pixeldata.ErrUnsupportedEncoderMetadata},
		{name: "YBR", metadata: withMetadata(encoderMetadata(1, 1, 3, 8, "YBR_FULL"), func(m *pixeldata.Metadata) {}), frame: []byte{1, 2, 3}, want: pixeldata.ErrUnsupportedEncoderMetadata},
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
	metadata := encoderMetadata(65535, 65535, 3, 16, "RGB")
	_, err := NewEncoder().EncodeFrame(context.Background(), nil, metadata)
	if !errors.Is(err, pixeldata.ErrEncoderOutputInvalid) && !errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata) {
		t.Fatalf("EncodeFrame() error = %v, want overflow rejection before allocation", err)
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

func TestEncoderCodestreamIsJPEGLSLosslessNEAR0(t *testing.T) {
	encoded, err := NewEncoder().EncodeFrame(
		context.Background(),
		[]byte{0, 64, 128, 255},
		encoderMetadata(2, 2, 1, 8, "MONOCHROME2"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.PhotometricInterpretation != "" || encoded.PlanarConfiguration != nil {
		t.Fatalf("EncodeFrame() metadata transforms = %#v, want none", encoded)
	}
	header, err := parseFrameHeader(encoded.Data)
	if err != nil {
		t.Fatal(err)
	}
	if header.near != 0 {
		t.Fatalf("NEAR = %d, want 0", header.near)
	}
	if header.precision != 8 || header.rows != 2 || header.columns != 2 || header.components != 1 {
		t.Fatalf("SOF55 = %+v, want 8-bit 2x2 mono", header)
	}
}

func TestDecoderUsesCodestreamPrecisionForDefaultPreset(t *testing.T) {
	encodeMetadata := encoderMetadata(1, 2, 1, 16, "MONOCHROME2")
	native := []byte{0x34, 0x12, 0xcd, 0xab}
	encoded, err := NewEncoder().EncodeFrame(context.Background(), native, encodeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	decodeMetadata := encodeMetadata
	decodeMetadata.BitsStored = 12
	decodeMetadata.HighBit = 11
	decoded, err := decodeFrame(encoded.Data, decodeMetadata)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, native) {
		t.Fatalf("decoded = % x, want % x", decoded, native)
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
			name:     "odd dimensions run and edge values",
			metadata: encoderMetadata(3, 5, 1, 8, "MONOCHROME2"),
			frame:    []byte{0, 0, 0, 0, 0, 255, 128, 1, 2, 3, 7, 7, 7, 7, 9},
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
			assertRoundTrip(t, tt.frame, tt.metadata)
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
		assertRoundTrip(t, frame, metadata)
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

func TestDecoderRejectsTruncatedAndHostileCodestreams(t *testing.T) {
	valid, err := NewEncoder().EncodeFrame(
		context.Background(),
		[]byte{0, 64, 128, 255},
		encoderMetadata(2, 2, 1, 8, "MONOCHROME2"),
	)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "truncated soi", data: []byte{0xff}},
		{name: "missing sof", data: []byte{0xff, 0xd8, 0xff, 0xd9}},
		{name: "truncated entropy", data: valid.Data[:len(valid.Data)/2]},
		{name: "near lossless sos", data: mutateNEAR(t, valid.Data, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeFrame(tt.data, encoderMetadata(2, 2, 1, 8, "MONOCHROME2"))
			if err == nil {
				t.Fatal("decodeFrame() error = nil, want rejection")
			}
		})
	}
}

func TestEncoderFuzzParametersDoNotPanic(t *testing.T) {
	encoder := NewEncoder()
	metadata := encoderMetadata(3, 3, 1, 8, "MONOCHROME2")
	for i := 0; i < 32; i++ {
		frame := bytes.Repeat([]byte{byte(i * 17)}, 9)
		if _, err := encoder.EncodeFrame(context.Background(), frame, metadata); err != nil {
			t.Fatal(err)
		}
	}
}

func BenchmarkEncoderMono8(b *testing.B) {
	frame := make([]byte, 64*64)
	for i := range frame {
		frame[i] = byte(i*37 + i/64)
	}
	metadata := encoderMetadata(64, 64, 1, 8, "MONOCHROME2")
	benchmarkEncoderWithPeakHeap(b, frame, metadata)
}

func BenchmarkEncoderRGB8(b *testing.B) {
	frame := make([]byte, 32*32*3)
	for i := range frame {
		frame[i] = byte(i*19 + i/32)
	}
	metadata := encoderMetadata(32, 32, 3, 8, "RGB")
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
		encoded, err := encoder.EncodeFrame(context.Background(), frame, metadata)
		if err != nil {
			b.Fatal(err)
		}
		if len(frame) > 0 {
			b.ReportMetric(float64(len(frame))/float64(len(encoded.Data)), "compression-ratio")
		}
	}
	b.StopTimer()
	close(stop)
	<-done
	b.ReportMetric(float64(peakDelta.Load()), "peak-heap-delta-bytes")
}

func assertRoundTrip(t *testing.T, frame []byte, metadata pixeldata.Metadata) {
	t.Helper()
	encoded, err := NewEncoder().EncodeFrame(context.Background(), frame, metadata)
	if err != nil {
		t.Fatal(err)
	}
	obj, pixel := jpeglsObjectWithFragment(t, metadata, encoded.Data)
	decoded, err := New().Decode(pixel, obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Data) != 1 || !bytes.Equal(decoded.Data[0], frame) {
		t.Fatalf("round trip = % x, want % x", decoded.Data, frame)
	}
}

func jpeglsObjectWithFragment(t *testing.T, metadata pixeldata.Metadata, fragment []byte) (*object.Object, pixeldata.PixelData) {
	t.Helper()
	elements := []core.Element{
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0010), core.VRUS, nil, metadata.Rows),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0011), core.VRUS, nil, metadata.Columns),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0002), core.VRUS, nil, metadata.SamplesPerPixel),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0004), core.VRCS, metadata.PhotometricInterpretation),
		dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1"),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0100), core.VRUS, nil, metadata.BitsAllocated),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0101), core.VRUS, nil, metadata.BitsStored),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0102), core.VRUS, nil, metadata.HighBit),
		dicomtest.Uint16Element(core.NewTag(0x0028, 0x0103), core.VRUS, nil, metadata.PixelRepresentation),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, fragment),
	}
	if metadata.PlanarConfigurationPresent {
		elements = append(elements[:4], append([]core.Element{
			dicomtest.Uint16Element(core.NewTag(0x0028, 0x0006), core.VRUS, nil, metadata.PlanarConfiguration),
		}, elements[4:]...)...)
	}
	obj := object.FromElements(elements, nil)
	pixel, err := pixeldata.Extract(obj)
	if err != nil {
		t.Fatal(err)
	}
	return obj, pixel
}

func mutateNEAR(t *testing.T, stream []byte, near byte) []byte {
	t.Helper()
	out := append([]byte(nil), stream...)
	for i := 0; i+1 < len(out); i++ {
		if out[i] == 0xff && out[i+1] == 0xda {
			// SOS: length (2) + Ns (1) + 2*Ns + NEAR
			if i+4 >= len(out) {
				t.Fatal("truncated SOS")
			}
			ns := int(out[i+4])
			nearIndex := i + 5 + 2*ns
			if nearIndex >= len(out) {
				t.Fatal("NEAR outside SOS")
			}
			out[nearIndex] = near
			return out
		}
	}
	t.Fatal("SOS marker not found")
	return out
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
