package jpeg

import (
	"bytes"
	"context"
	"errors"
	"math"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestJPEGEncoderCapabilitiesRegistrationAndQuality(t *testing.T) {
	if _, err := NewEncoder(0); !errors.Is(err, ErrInvalidQuality) {
		t.Fatalf("NewEncoder(0) error = %v, want ErrInvalidQuality", err)
	}
	encoder, err := NewEncoder(DefaultQuality)
	if err != nil {
		t.Fatal(err)
	}
	if encoder.Quality() != DefaultQuality {
		t.Fatalf("Quality = %d, want %d", encoder.Quality(), DefaultQuality)
	}
	caps := encoder.Capabilities()
	if caps.TransferSyntaxUID != transfer.JPEGBaseline.UID || caps.Lossless || caps.LossyMethod != jpegLossyMethod || !caps.SupportsMultiFrame {
		t.Fatalf("capabilities = %#v", caps)
	}
	if err := RegisterEncoder(nil, DefaultQuality); !errors.Is(err, pixeldata.ErrEncoderRegistryNil) {
		t.Fatalf("RegisterEncoder(nil) error = %v", err)
	}
	registry := pixeldata.NewMemoryEncoderRegistry()
	if err := RegisterEncoder(registry, DefaultQuality); err != nil {
		t.Fatal(err)
	}
	if err := pixeldata.CheckEncoderAvailability(registry, UID); err != nil {
		t.Fatal(err)
	}
}

func TestJPEGEncoderTranscodeRoundTripAndMetadata(t *testing.T) {
	const rows, columns = 32, 32
	pixels := make([]byte, rows*columns)
	for y := 0; y < rows; y++ {
		for x := 0; x < columns; x++ {
			pixels[y*columns+x] = byte((x*5 + y*3) & 0xff)
		}
	}
	source := nativeJPEGObject(rows, columns, 1, "MONOCHROME2", nil, pixels)
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := RegisterEncoder(encoders, 95); err != nil {
		t.Fatal(err)
	}
	compressed, report, err := pixeldata.TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline, pixeldata.TranscodeOptions{
		EncoderRegistry: encoders,
		AllowLossy:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lossy {
		t.Fatalf("report = %#v", report)
	}
	pixel, err := pixeldata.ExtractView(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !pixel.Encapsulated || len(pixel.Sequence.Fragments) != 1 || len(pixel.Sequence.Fragments[0]) == 0 {
		t.Fatalf("encapsulated pixel data = %#v", pixel.Sequence)
	}
	if !bytes.Contains(pixel.Sequence.Fragments[0], []byte{0xff, 0xc0}) || bytes.Contains(pixel.Sequence.Fragments[0], []byte{0xff, 0xc2}) {
		t.Fatal("encoded frame is not JPEG Baseline SOF0")
	}
	encodedMetadata, err := pixeldata.ExtractMetadata(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if encodedMetadata.PhotometricInterpretation != "MONOCHROME2" || encodedMetadata.PlanarConfigurationPresent {
		t.Fatalf("encoded metadata = %#v", encodedMetadata)
	}
	if got, _ := compressed.GetString(core.NewTag(0x0028, 0x2110)); got != "01" {
		t.Fatalf("LossyImageCompression = %q, want 01", got)
	}
	if got, _ := compressed.GetString(core.NewTag(0x0028, 0x2114)); got != jpegLossyMethod {
		t.Fatalf("LossyImageCompressionMethod = %q", got)
	}

	decoders := pixeldata.NewMemoryRegistry()
	if err := Register(decoders); err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(context.Background(), compressed, transfer.JPEGBaseline, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{DecoderRegistry: decoders})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := pixeldata.ExtractNativeFramesView(native)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 || meanAbsoluteError(frames.Data[0], pixels) > 4 || maxAbsoluteError(frames.Data[0], pixels) > 32 {
		t.Fatalf("round-trip frames=%d mean/max absolute error=%f/%d", len(frames.Data), meanAbsoluteError(frames.Data[0], pixels), maxAbsoluteError(frames.Data[0], pixels))
	}
}

func TestJPEGEncoderRGBUpdatesPhotometricAndRoundTrips(t *testing.T) {
	const rows, columns = 16, 16
	pixels := make([]byte, rows*columns*3)
	for i := 0; i < rows*columns; i++ {
		pixels[i*3] = 220
		pixels[i*3+1] = byte((i % columns) * 8)
		pixels[i*3+2] = 30
	}
	planar := uint16(0)
	source := nativeJPEGObject(rows, columns, 3, "RGB", &planar, pixels)
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := RegisterEncoder(encoders, 95); err != nil {
		t.Fatal(err)
	}
	compressed, _, err := pixeldata.TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline, pixeldata.TranscodeOptions{EncoderRegistry: encoders, AllowLossy: true})
	if err != nil {
		t.Fatal(err)
	}
	metadata, err := pixeldata.ExtractMetadata(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.PhotometricInterpretation != "YBR_FULL_422" || !metadata.PlanarConfigurationPresent || metadata.PlanarConfiguration != 0 {
		t.Fatalf("encoded metadata = %#v", metadata)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := Register(decoders); err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(context.Background(), compressed, transfer.JPEGBaseline, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{DecoderRegistry: decoders})
	if err != nil {
		t.Fatal(err)
	}
	decodedMetadata, err := pixeldata.ExtractMetadata(native)
	if err != nil {
		t.Fatal(err)
	}
	if decodedMetadata.PhotometricInterpretation != "RGB" || decodedMetadata.PlanarConfiguration != 0 {
		t.Fatalf("decoded metadata = %#v", decodedMetadata)
	}
}

func TestJPEGEncoderRejectsIncompatibleMetadataBeforeEncoding(t *testing.T) {
	encoder, err := NewEncoder(DefaultQuality)
	if err != nil {
		t.Fatal(err)
	}
	valid := pixeldata.Metadata{Rows: 1, Columns: 1, SamplesPerPixel: 1, PhotometricInterpretation: "MONOCHROME2", NumberOfFrames: 1, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
	tests := []struct {
		name   string
		mutate func(*pixeldata.Metadata)
	}{
		{name: "signed", mutate: func(m *pixeldata.Metadata) { m.PixelRepresentation = 1 }},
		{name: "bits stored", mutate: func(m *pixeldata.Metadata) { m.BitsStored = 7; m.HighBit = 6 }},
		{name: "photometric", mutate: func(m *pixeldata.Metadata) { m.PhotometricInterpretation = "PALETTE COLOR" }},
		{name: "monochrome planar configuration", mutate: func(m *pixeldata.Metadata) { m.PlanarConfigurationPresent = true }},
		{name: "planar RGB", mutate: func(m *pixeldata.Metadata) {
			m.SamplesPerPixel = 3
			m.PhotometricInterpretation = "RGB"
			m.PlanarConfigurationPresent = true
			m.PlanarConfiguration = 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			metadata := valid
			test.mutate(&metadata)
			if _, err := encoder.EncodeFrame(context.Background(), []byte{0}, metadata); !errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata) {
				t.Fatalf("EncodeFrame error = %v, want ErrUnsupportedEncoderMetadata", err)
			}
		})
	}
}

func TestJPEGEncoderQualityAffectsOutputAndConcurrentUseIsSafe(t *testing.T) {
	const rows, columns = 64, 64
	pixels := make([]byte, rows*columns)
	for i := range pixels {
		pixels[i] = byte((i*73 + i/7*29) & 0xff)
	}
	metadata := pixeldata.Metadata{Rows: rows, Columns: columns, SamplesPerPixel: 1, PhotometricInterpretation: "MONOCHROME2", NumberOfFrames: 1, BitsAllocated: 8, BitsStored: 8, HighBit: 7}
	low, _ := NewEncoder(20)
	high, _ := NewEncoder(95)
	lowFrame, err := low.EncodeFrame(context.Background(), pixels, metadata)
	if err != nil {
		t.Fatal(err)
	}
	highFrame, err := high.EncodeFrame(context.Background(), pixels, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if len(lowFrame.Data) >= len(highFrame.Data) {
		t.Fatalf("quality output sizes low/high = %d/%d", len(lowFrame.Data), len(highFrame.Data))
	}

	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := high.EncodeFrame(context.Background(), pixels, metadata)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
}

func nativeJPEGObject(rows, columns, samples uint16, photometric string, planar *uint16, pixels []byte) *object.Object {
	elements := jpegMetadataElementsWithOptions(rows, columns, samples, photometric, 1, jpegMetadataOptions{planarConfiguration: planar})
	elements = append(elements, dicomtest.NewOBElement(core.TagPixelData, pixels))
	return object.FromElements(elements, nil)
}

func meanAbsoluteError(got, want []byte) float64 {
	if len(got) != len(want) || len(want) == 0 {
		return math.Inf(1)
	}
	var total int
	for i := range want {
		delta := int(got[i]) - int(want[i])
		if delta < 0 {
			delta = -delta
		}
		total += delta
	}
	return float64(total) / float64(len(want))
}

func maxAbsoluteError(got, want []byte) int {
	if len(got) != len(want) {
		return math.MaxInt
	}
	maximum := 0
	for i := range want {
		delta := int(got[i]) - int(want[i])
		if delta < 0 {
			delta = -delta
		}
		if delta > maximum {
			maximum = delta
		}
	}
	return maximum
}
