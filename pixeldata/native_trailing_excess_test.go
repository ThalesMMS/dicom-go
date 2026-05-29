package pixeldata

import (
	"bytes"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

// Real-world native Pixel Data frequently carries trailing block-padding
// bytes beyond rows*columns*samples. Frame extraction slices the leading
// expected bytes and ignores such excess.
func TestExtractNativeFramesToleratesTrailingPaddingBytes(t *testing.T) {
	valid := sequentialBytes(64)
	raw := append(append([]byte{}, valid...), make([]byte, 24)...)
	obj := nativeExcessTestObject(t, nil, raw)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 1 {
		t.Fatalf("frame count = %d, want 1", len(frames.Data))
	}
	if !bytes.Equal(frames.Data[0], valid) {
		t.Fatalf("frame data = %v, want leading %d valid bytes", frames.Data[0], len(valid))
	}
}

func TestExtractNativeFramesMultiFrameToleratesTrailingPaddingBytes(t *testing.T) {
	frameA := sequentialBytes(64)
	frameB := make([]byte, 64)
	for i := range frameB {
		frameB[i] = byte(255 - i)
	}
	raw := append(append(append([]byte{}, frameA...), frameB...), make([]byte, 16)...)
	numberOfFrames := "2"
	obj := nativeExcessTestObject(t, &numberOfFrames, raw)

	frames, err := ExtractNativeFrames(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != 2 {
		t.Fatalf("frame count = %d, want 2", len(frames.Data))
	}
	if !bytes.Equal(frames.Data[0], frameA) || !bytes.Equal(frames.Data[1], frameB) {
		t.Fatal("frame contents differ from leading expected bytes")
	}
}

// Excess of one full frame or more suggests wrong frame metadata rather than
// padding and must still be rejected.
func TestExtractNativeFramesRejectsExcessOfFullFrameOrMore(t *testing.T) {
	raw := sequentialBytes(128)
	obj := nativeExcessTestObject(t, nil, raw)

	_, err := ExtractNativeFrames(obj)
	if err == nil {
		t.Fatal("ExtractNativeFrames() error = nil, want ErrPixelDataSizeMismatch")
	}
	if !errors.Is(err, ErrPixelDataSizeMismatch) {
		t.Fatalf("error = %v, want ErrPixelDataSizeMismatch", err)
	}
}

// nativeExcessTestObject builds an 8x8, 8-bit, single-sample MONOCHROME2
// object around raw Pixel Data bytes.
func nativeExcessTestObject(t *testing.T, numberOfFrames *string, raw []byte) *object.Object {
	t.Helper()
	return object.FromElements(append(
		pixelMetadataElements(8, 8, 1, 8, 8, 7, 0, numberOfFrames,
			dicomtest.NewStringElement(tagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		),
		dicomtest.NewOBElement(core.TagPixelData, raw),
	), nil)
}
