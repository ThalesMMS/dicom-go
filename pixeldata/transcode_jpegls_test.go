package pixeldata_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestTranscodeNativeJPEGLSAndBackIsBitExact(t *testing.T) {
	tc := codecfixture.NativeSmall()
	assertJPEGLSPipelineRoundTrip(t, tc.Object(), tc.ExpectedFrames)
}

func TestTranscodeNativeJPEGLSPipelineIsBitExactAcrossProfiles(t *testing.T) {
	multi := codecfixture.NativeMultiFrame()
	signed12 := codecfixture.NativeSmall().Object()
	signed12Pixels := []byte{0x34, 0x02, 0xff, 0x07, 0x00, 0x08, 0xcd, 0x0a}
	putUint16(signed12, core.NewTag(0x0028, 0x0010), 1)
	putUint16(signed12, core.NewTag(0x0028, 0x0011), 4)
	putUint16(signed12, core.NewTag(0x0028, 0x0100), 16)
	putUint16(signed12, core.NewTag(0x0028, 0x0101), 12)
	putUint16(signed12, core.NewTag(0x0028, 0x0102), 11)
	putUint16(signed12, core.NewTag(0x0028, 0x0103), 1)
	signed12.Put(core.NewRawElement(core.TagPixelData, core.VROW, signed12Pixels))

	rgb8 := codecfixture.NativeSmall().Object()
	rgb8Pixels := []byte{1, 2, 3, 4, 5, 6, 7, 8, 9}
	putUint16(rgb8, core.NewTag(0x0028, 0x0010), 1)
	putUint16(rgb8, core.NewTag(0x0028, 0x0011), 3)
	putUint16(rgb8, core.NewTag(0x0028, 0x0002), 3)
	putUint16(rgb8, core.NewTag(0x0028, 0x0006), 0)
	putUint16(rgb8, core.NewTag(0x0028, 0x0100), 8)
	putUint16(rgb8, core.NewTag(0x0028, 0x0101), 8)
	putUint16(rgb8, core.NewTag(0x0028, 0x0102), 7)
	rgb8.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0004), VR: core.VRCS}, Value: core.StringValue{"RGB"}})
	rgb8.Put(core.NewRawElement(core.TagPixelData, core.VROW, rgb8Pixels))

	tests := []struct {
		name   string
		object *object.Object
		frames [][]byte
	}{
		{name: "multiframe mono8", object: multi.Object(), frames: multi.ExpectedFrames},
		{name: "signed 12 stored in 16", object: signed12, frames: [][]byte{signed12Pixels}},
		{name: "RGB8 interleaved", object: rgb8, frames: [][]byte{rgb8Pixels}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertJPEGLSPipelineRoundTrip(t, test.object, test.frames)
		})
	}
}

func TestTranscodeRejectsNearLosslessTarget(t *testing.T) {
	tc := codecfixture.NativeSmall()
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := jpegls.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	_, _, err := pixeldata.TranscodeDataSet(
		context.Background(), tc.Object(), tc.Syntax, transfer.JPEGLSNearLossless,
		pixeldata.TranscodeOptions{EncoderRegistry: encoders},
	)
	if err == nil {
		t.Fatal("TranscodeDataSet(Near-Lossless) = nil, want encoder missing")
	}
}

func assertJPEGLSPipelineRoundTrip(t *testing.T, source *object.Object, wantFrames [][]byte) {
	t.Helper()
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := jpegls.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	compressed, _, err := pixeldata.TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGLSLossless, pixeldata.TranscodeOptions{EncoderRegistry: encoders})
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.ExtractView(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(pixel.Sequence.Fragments) != len(wantFrames) || len(pixel.Sequence.OffsetTable) != len(wantFrames)*4 {
		t.Fatalf("JPEG-LS frames/BOT = %d/%d, want %d/%d", len(pixel.Sequence.Fragments), len(pixel.Sequence.OffsetTable), len(wantFrames), len(wantFrames)*4)
	}
	var offset uint32
	for index, fragment := range pixel.Sequence.Fragments {
		if got := binary.LittleEndian.Uint32(pixel.Sequence.OffsetTable[index*4:]); got != offset {
			t.Fatalf("BOT[%d] = %d, want %d", index, got, offset)
		}
		offset += uint32(8 + (len(fragment)+1)&^1)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := jpegls.Register(decoders); err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(context.Background(), compressed, transfer.JPEGLSLossless, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{DecoderRegistry: decoders})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := pixeldata.ExtractNativeFramesView(native)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != len(wantFrames) {
		t.Fatalf("round-trip frame count = %d, want %d", len(frames.Data), len(wantFrames))
	}
	for index := range wantFrames {
		if !bytes.Equal(frames.Data[index], wantFrames[index]) {
			t.Fatalf("round-trip frame %d = % x, want % x", index, frames.Data[index], wantFrames[index])
		}
	}
}
