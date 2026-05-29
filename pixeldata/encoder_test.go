package pixeldata

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
)

type testFrameEncoder struct {
	capabilities EncoderCapabilities
	encoded      EncodedFrame
	err          error
	calls        int
	panicValue   any
	mutateInput  bool
}

func (e *testFrameEncoder) Capabilities() EncoderCapabilities { return e.capabilities }

func (e *testFrameEncoder) EncodeFrame(_ context.Context, frame []byte, _ Metadata) (EncodedFrame, error) {
	e.calls++
	if e.mutateInput && len(frame) > 0 {
		frame[0] ^= 0xff
	}
	if e.panicValue != nil {
		panic(e.panicValue)
	}
	return e.encoded, e.err
}

func testEncoderCapabilities() EncoderCapabilities {
	return EncoderCapabilities{
		TransferSyntaxUID:          "1.2.840.10008.1.2.5",
		BitsAllocated:              []uint16{8, 16},
		PixelRepresentations:       []uint16{0, 1},
		SamplesPerPixel:            []uint16{1, 3},
		PhotometricInterpretations: []string{"MONOCHROME1", "MONOCHROME2", "RGB"},
		Lossless:                   true,
		SupportsMultiFrame:         true,
	}
}

func testEncoderMetadata() Metadata {
	return Metadata{
		Rows: 2, Columns: 2, SamplesPerPixel: 1, BitsAllocated: 8,
		BitsStored: 8, HighBit: 7, PixelRepresentation: 0,
		NumberOfFrames: 1, PhotometricInterpretation: "MONOCHROME2",
	}
}

func TestMemoryEncoderRegistryFreezesCapabilitiesAndNormalizesUID(t *testing.T) {
	capabilities := testEncoderCapabilities()
	encoder := &testFrameEncoder{
		capabilities: capabilities,
		encoded:      EncodedFrame{Data: []byte{1, 2, 3, 4}},
		mutateInput:  true,
	}
	registry := NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder("1.2.840.10008.1.2.5 \x00", encoder); err != nil {
		t.Fatalf("RegisterEncoder() error = %v", err)
	}

	encoder.capabilities.BitsAllocated[0] = 32
	encoder.capabilities.PhotometricInterpretations[0] = "SECRET"
	got, ok := registry.GetEncoder("1.2.840.10008.1.2.5")
	if !ok {
		t.Fatal("GetEncoder() = false, want true")
	}
	gotCapabilities := got.Capabilities()
	if !reflect.DeepEqual(gotCapabilities.BitsAllocated, []uint16{8, 16}) ||
		!reflect.DeepEqual(gotCapabilities.PhotometricInterpretations, []string{"MONOCHROME1", "MONOCHROME2", "RGB"}) {
		t.Fatalf("Capabilities() = %#v, want frozen registration snapshot", gotCapabilities)
	}
	gotCapabilities.BitsAllocated[0] = 64
	if second := got.Capabilities(); second.BitsAllocated[0] != 8 {
		t.Fatalf("Capabilities() returned aliased slice: %#v", second.BitsAllocated)
	}

	input := []byte{1, 2, 3, 4}
	frame, err := registry.EncodeFrame(context.Background(), "1.2.840.10008.1.2.5", input, testEncoderMetadata())
	if err != nil || !reflect.DeepEqual(frame.Data, []byte{1, 2, 3, 4}) {
		t.Fatalf("EncodeFrame() = %#v, %v", frame, err)
	}
	if !reflect.DeepEqual(input, []byte{1, 2, 3, 4}) {
		t.Fatalf("EncodeFrame() mutated borrowed input: %v", input)
	}
}

func TestMemoryEncoderRegistryRejectsInvalidRegistrations(t *testing.T) {
	valid := testEncoderCapabilities()
	tests := []struct {
		name    string
		uid     string
		encoder FrameEncoder
		want    error
	}{
		{name: "nil registry", uid: valid.TransferSyntaxUID, encoder: &testFrameEncoder{capabilities: valid}, want: ErrEncoderRegistryNil},
		{name: "nil encoder", uid: valid.TransferSyntaxUID, want: ErrEncoderNil},
		{name: "empty uid", encoder: &testFrameEncoder{capabilities: valid}, want: ErrEncoderUIDInvalid},
		{name: "uid mismatch", uid: "1.2.3", encoder: &testFrameEncoder{capabilities: valid}, want: ErrEncoderCapabilitiesInvalid},
		{name: "empty bits", uid: valid.TransferSyntaxUID, encoder: &testFrameEncoder{capabilities: func() EncoderCapabilities { c := valid; c.BitsAllocated = nil; return c }()}, want: ErrEncoderCapabilitiesInvalid},
		{name: "lossy method missing", uid: valid.TransferSyntaxUID, encoder: &testFrameEncoder{capabilities: func() EncoderCapabilities { c := valid; c.Lossless = false; return c }()}, want: ErrEncoderCapabilitiesInvalid},
		{name: "lossy method invalid CS", uid: valid.TransferSyntaxUID, encoder: &testFrameEncoder{capabilities: func() EncoderCapabilities { c := valid; c.Lossless = false; c.LossyMethod = "patient/name"; return c }()}, want: ErrEncoderCapabilitiesInvalid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var registry *MemoryEncoderRegistry
			if test.name != "nil registry" {
				registry = NewMemoryEncoderRegistry()
			}
			if err := registry.RegisterEncoder(test.uid, test.encoder); !errors.Is(err, test.want) {
				t.Fatalf("RegisterEncoder() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestEncoderRegistryTypedAvailabilityAndMetadataErrors(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	encoder := &testFrameEncoder{capabilities: testEncoderCapabilities(), encoded: EncodedFrame{Data: []byte{1}}}
	if err := registry.RegisterEncoder(encoder.capabilities.TransferSyntaxUID, encoder); err != nil {
		t.Fatal(err)
	}

	err := CheckEncoderAvailability(registry, "1.2.840.10008.1.2.4.50")
	if !errors.Is(err, ErrEncoderNotFound) {
		t.Fatalf("CheckEncoderAvailability() error = %v, want ErrEncoderNotFound", err)
	}
	var availability *EncoderAvailabilityError
	if !errors.As(err, &availability) || !reflect.DeepEqual(availability.RegisteredEncoderUIDs, []string{"1.2.840.10008.1.2.5"}) {
		t.Fatalf("availability = %#v", availability)
	}

	metadata := testEncoderMetadata()
	metadata.BitsAllocated = 32
	_, err = registry.EncodeFrame(context.Background(), encoder.capabilities.TransferSyntaxUID, []byte{1}, metadata)
	if !errors.Is(err, ErrUnsupportedEncoderMetadata) || encoder.calls != 0 {
		t.Fatalf("EncodeFrame() error = %v calls=%d, want metadata rejection before backend", err, encoder.calls)
	}
}

func TestEncoderRegistryCancellationAndPanicsAreRedacted(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	encoder := &testFrameEncoder{
		capabilities: testEncoderCapabilities(),
		panicValue:   "SECRET PATIENT PATH",
	}
	if err := registry.RegisterEncoder(encoder.capabilities.TransferSyntaxUID, encoder); err != nil {
		t.Fatal(err)
	}
	_, err := registry.EncodeFrame(context.Background(), encoder.capabilities.TransferSyntaxUID, []byte{1, 2, 3, 4}, testEncoderMetadata())
	if !errors.Is(err, ErrEncoderFailed) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("panic error = %q, want redacted ErrEncoderFailed", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = registry.EncodeFrame(ctx, encoder.capabilities.TransferSyntaxUID, []byte{1, 2, 3, 4}, testEncoderMetadata())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled EncodeFrame() error = %v", err)
	}
}

func TestEncoderRegistryPreservesTypedBackendErrorsWithoutEchoingThem(t *testing.T) {
	backendErr := errors.New("SECRET backend detail")
	registry := NewMemoryEncoderRegistry()
	encoder := &testFrameEncoder{capabilities: testEncoderCapabilities(), err: backendErr}
	if err := registry.RegisterEncoder(encoder.capabilities.TransferSyntaxUID, encoder); err != nil {
		t.Fatal(err)
	}
	_, err := registry.EncodeFrame(context.Background(), encoder.capabilities.TransferSyntaxUID, []byte{1, 2, 3, 4}, testEncoderMetadata())
	if !errors.Is(err, ErrEncoderFailed) || !errors.Is(err, backendErr) || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("EncodeFrame() error = %q, want typed redacted backend failure", err)
	}
}

func TestMemoryEncoderRegistryConcurrentUse(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	capabilities := testEncoderCapabilities()
	encoder := &testFrameEncoder{capabilities: capabilities, encoded: EncodedFrame{Data: []byte{1}}}
	if err := registry.RegisterEncoder(capabilities.TransferSyntaxUID, encoder); err != nil {
		t.Fatal(err)
	}
	const workers = 16
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wait.Done()
			if _, ok := registry.GetEncoder(capabilities.TransferSyntaxUID); !ok {
				t.Error("GetEncoder() = false")
			}
		}()
	}
	wait.Wait()
}

func TestMemoryEncoderRegistryRejectsDuplicateUID(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	encoder := &testFrameEncoder{capabilities: testEncoderCapabilities()}
	if err := registry.RegisterEncoder(encoder.capabilities.TransferSyntaxUID, encoder); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterEncoder(encoder.capabilities.TransferSyntaxUID, encoder); !errors.Is(err, ErrEncoderAlreadyRegistered) {
		t.Fatalf("duplicate registration error = %v, want ErrEncoderAlreadyRegistered", err)
	}
}
