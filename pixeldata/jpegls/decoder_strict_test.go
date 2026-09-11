package jpegls

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/pixeldata"
)

var charLSLossless2x2 = []byte{
	0xff, 0xd8, 0xff, 0xf7, 0x00, 0x0b, 0x08, 0x00,
	0x02, 0x00, 0x02, 0x01, 0x01, 0x11, 0x00, 0xff,
	0xda, 0x00, 0x08, 0x01, 0x01, 0x00, 0x00, 0x00,
	0x00, 0x80, 0x00, 0x00, 0xbf, 0x00, 0x00, 0x00,
	0xff, 0x00, 0x00, 0x00, 0x7f, 0x40, 0xff, 0xd9,
}

func TestDecoderMatchesCharLSLosslessVector(t *testing.T) {
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	got, err := decodeFrame(charLSLossless2x2, metadata)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 64, 128, 255}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded pixels = %v, want %v", got, want)
	}
}

func TestDecoderAcceptsLSEID1AndApplicationSegments(t *testing.T) {
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	stream := append([]byte(nil), charLSLossless2x2...)

	var applicationSegments []byte
	for marker := byte(0xe0); marker <= 0xef; marker++ {
		applicationSegments = append(applicationSegments, 0xff, marker, 0x00, 0x03, marker)
	}
	applicationSegments = append(applicationSegments, 0xff, 0xfe, 0x00, 0x03, 'C')
	stream = insertAt(stream, 2, applicationSegments)
	sos := markerOffset(t, stream, 0xda)
	stream = insertAt(stream, sos, lseID1(255, 3, 7, 21, 64))

	got, err := decodeFrame(stream, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if want := []byte{0, 64, 128, 255}; !bytes.Equal(got, want) {
		t.Fatalf("decoded pixels = %v, want %v", got, want)
	}
}

func TestDecoderRejectsMalformedLSE(t *testing.T) {
	base := append([]byte(nil), charLSLossless2x2...)
	sof := markerOffset(t, base, 0xf7)
	sos := markerOffset(t, base, 0xda)
	valid := lseID1(255, 3, 7, 21, 64)
	withLSE := insertAt(base, sos, valid)

	invalidID := append([]byte(nil), valid...)
	invalidID[4] = 2
	invalidLength := append([]byte(nil), valid...)
	invalidLength[3] = 12
	invalidThreshold := lseID1(255, 3, 2, 21, 64)
	invalidReset := lseID1(255, 3, 7, 21, 2)

	tests := []struct {
		name   string
		stream []byte
	}{
		{name: "unknown ID", stream: insertAt(base, sos, invalidID)},
		{name: "invalid length", stream: insertAt(base, sos, invalidLength)},
		{name: "unordered thresholds", stream: insertAt(base, sos, invalidThreshold)},
		{name: "reset below three", stream: insertAt(base, sos, invalidReset)},
		{name: "duplicate", stream: insertAt(withLSE, markerOffset(t, withLSE, 0xda), valid)},
		{name: "before SOF55", stream: insertAt(base, sof, valid)},
	}
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeFrame(test.stream, metadata); !errors.Is(err, ErrInvalidCodestream) {
				t.Fatalf("error = %v, want ErrInvalidCodestream", err)
			}
		})
	}
}

func TestDecoderRejectsMalformedSOF55AndSOS(t *testing.T) {
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	mono := encodedFrame(t, []byte{0, 64, 128, 255}, metadata)
	rgbMetadata := encoderMetadata(1, 2, 3, 8, "RGB")
	rgb := encodedFrame(t, []byte{1, 2, 3, 4, 5, 6}, rgbMetadata)

	sof := markerOffset(t, mono, 0xf7)
	sos := markerOffset(t, mono, 0xda)
	rgbSOF := markerOffset(t, rgb, 0xf7)
	sofSegment := markerSegment(t, mono, sof)
	scanSegment := scanSegments(t, mono)[0]

	tests := []struct {
		name   string
		stream []byte
	}{
		{name: "precision below two", stream: mutateAt(mono, sof+4, 1)},
		{name: "zero rows", stream: mutateAt(mono, sof+6, 0)},
		{name: "invalid sampling", stream: mutateAt(mono, sof+11, 0x21)},
		{name: "invalid table selector", stream: mutateAt(mono, sof+12, 1)},
		{name: "duplicate SOF55", stream: insertAt(mono, sos, sofSegment)},
		{name: "SOS before SOF55", stream: insertAt(mono, sof, scanSegment)},
		{name: "SOS component count", stream: mutateAt(mono, sos+4, 2)},
		{name: "unknown SOS component", stream: mutateAt(mono, sos+5, 2)},
		{name: "SOS mapping selector", stream: mutateAt(mono, sos+6, 1)},
		{name: "SOS NEAR", stream: mutateAt(mono, sos+7, 1)},
		{name: "SOS ILV", stream: mutateAt(mono, sos+8, 1)},
		{name: "SOS transform", stream: mutateAt(mono, sos+9, 1)},
		{name: "duplicate scan", stream: insertAt(mono, markerOffset(t, mono, 0xd9), scanSegment)},
		{name: "duplicate component IDs", stream: mutateAt(rgb, rgbSOF+13, 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			selected := metadata
			if test.name == "duplicate component IDs" {
				selected = rgbMetadata
			}
			if _, err := decodeFrame(test.stream, selected); !errors.Is(err, ErrInvalidCodestream) {
				t.Fatalf("error = %v, want ErrInvalidCodestream", err)
			}
		})
	}
}

func TestDecoderAssociatesScansByComponentID(t *testing.T) {
	metadata := encoderMetadata(2, 2, 3, 8, "RGB")
	want := []byte{1, 11, 21, 2, 12, 22, 3, 13, 23, 4, 14, 24}
	stream := encodedFrame(t, want, metadata)
	scans := scanSegments(t, stream)
	firstSOS := markerOffset(t, stream, 0xda)
	eoi := markerOffset(t, stream, 0xd9)
	reordered := append([]byte(nil), stream[:firstSOS]...)
	reordered = append(reordered, scans[2]...)
	reordered = append(reordered, scans[0]...)
	reordered = append(reordered, scans[1]...)
	reordered = append(reordered, stream[eoi:]...)

	got, err := decodeFrame(reordered, metadata)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decoded pixels = %v, want %v", got, want)
	}
}

func TestDecoderRejectsTrailingEntropyData(t *testing.T) {
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	stream := encodedFrame(t, []byte{0, 64, 128, 255}, metadata)
	stream = insertAt(stream, markerOffset(t, stream, 0xd9), []byte{0})
	if _, err := decodeFrame(stream, metadata); !errors.Is(err, ErrInvalidCodestream) {
		t.Fatalf("error = %v, want ErrInvalidCodestream", err)
	}
}

func TestDecoderHonorsCancellationInsideFrame(t *testing.T) {
	metadata := encoderMetadata(64, 64, 1, 8, "MONOCHROME2")
	frame := make([]byte, int(metadata.Rows)*int(metadata.Columns))
	for i := range frame {
		frame[i] = byte(i*37 + 11)
	}
	stream := encodedFrame(t, frame, metadata)
	ctx := &decoderCancelContext{Context: context.Background(), remaining: 2}
	if _, err := decodeFrameContext(ctx, stream, metadata); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestDecoderRejectsRequestExceedingTotalWorkingSet(t *testing.T) {
	metadata := encoderMetadata(1024, 1024, 1, 8, "MONOCHROME2")
	obj, pixel := jpeglsObjectWithFragment(t, metadata, charLSLossless2x2)
	obj.Put(dicomtest.NewStringElement(core.NewTag(0x0028, 0x0008), core.VRIS, "1000"))
	if _, err := New().Decode(pixel, obj); !errors.Is(err, ErrInvalidCodestream) {
		t.Fatalf("error = %v, want ErrInvalidCodestream", err)
	}
}

func TestDecoderIsSafeForConcurrentUse(t *testing.T) {
	metadata := encoderMetadata(2, 2, 1, 8, "MONOCHROME2")
	obj, pixel := jpeglsObjectWithFragment(t, metadata, charLSLossless2x2)
	codec := New()
	want := []byte{0, 64, 128, 255}

	const goroutines = 16
	const iterations = 20
	errs := make(chan error, goroutines)
	var wg sync.WaitGroup
	for worker := 0; worker < goroutines; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for iteration := 0; iteration < iterations; iteration++ {
				frames, err := codec.Decode(pixel, obj)
				if err != nil {
					errs <- err
					return
				}
				if len(frames.Data) != 1 || !bytes.Equal(frames.Data[0], want) {
					errs <- fmt.Errorf("decoded pixels = %v, want %v", frames.Data, want)
					return
				}
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
}

type decoderCancelContext struct {
	context.Context
	remaining int
}

func (c *decoderCancelContext) Err() error {
	c.remaining--
	if c.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func encodedFrame(t *testing.T, frame []byte, metadata pixeldata.Metadata) []byte {
	t.Helper()
	encoded, err := NewEncoder().EncodeFrame(context.Background(), frame, metadata)
	if err != nil {
		t.Fatal(err)
	}
	return encoded.Data
}

func lseID1(maxval, t1, t2, t3, reset uint16) []byte {
	return []byte{
		0xff, 0xf8, 0x00, 0x0d, 0x01,
		byte(maxval >> 8), byte(maxval), byte(t1 >> 8), byte(t1),
		byte(t2 >> 8), byte(t2), byte(t3 >> 8), byte(t3),
		byte(reset >> 8), byte(reset),
	}
}

func markerOffset(t *testing.T, stream []byte, marker byte) int {
	t.Helper()
	offset := bytes.Index(stream, []byte{0xff, marker})
	if offset < 0 {
		t.Fatalf("marker 0xff%02x not found", marker)
	}
	return offset
}

func markerSegment(t *testing.T, stream []byte, offset int) []byte {
	t.Helper()
	if offset < 0 || offset+4 > len(stream) {
		t.Fatal("truncated marker segment")
	}
	length := int(stream[offset+2])<<8 | int(stream[offset+3])
	end := offset + 2 + length
	if length < 2 || end > len(stream) {
		t.Fatal("invalid marker segment length")
	}
	return append([]byte(nil), stream[offset:end]...)
}

func scanSegments(t *testing.T, stream []byte) [][]byte {
	t.Helper()
	var scans [][]byte
	position := markerOffset(t, stream, 0xda)
	for position+1 < len(stream) && stream[position] == 0xff && stream[position+1] == 0xda {
		header := markerSegment(t, stream, position)
		entropyStart := position + len(header)
		_, end, err := readEntropy(stream, entropyStart)
		if err != nil {
			t.Fatal(err)
		}
		scans = append(scans, append([]byte(nil), stream[position:end]...))
		position = end
	}
	return scans
}

func insertAt(stream []byte, offset int, addition []byte) []byte {
	result := make([]byte, 0, len(stream)+len(addition))
	result = append(result, stream[:offset]...)
	result = append(result, addition...)
	return append(result, stream[offset:]...)
}

func mutateAt(stream []byte, offset int, value byte) []byte {
	result := append([]byte(nil), stream...)
	result[offset] = value
	return result
}
