package pixeldata_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestTranscodeNativeRLEAndBackIsBitExact(t *testing.T) {
	tc := codecfixture.NativeSmall()
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	compressed, report, err := pixeldata.TranscodeDataSet(
		context.Background(), tc.Object(), tc.Syntax, transfer.RLELossless,
		pixeldata.TranscodeOptions{EncoderRegistry: encoders},
	)
	if err != nil {
		t.Fatal(err)
	}
	if report.PixelDataPreserved || report.EncodedBytes == 0 {
		t.Fatalf("encode report = %#v", report)
	}
	encoded, err := pixeldata.ExtractView(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if !encoded.Encapsulated || len(encoded.Sequence.Fragments) != 1 || len(encoded.Sequence.OffsetTable) != 4 {
		t.Fatalf("encoded Pixel Data = %#v", encoded)
	}

	decoders := pixeldata.NewMemoryRegistry()
	if err := rle.Register(decoders); err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(
		context.Background(), compressed, transfer.RLELossless, transfer.ExplicitVRLittleEndian,
		pixeldata.TranscodeOptions{DecoderRegistry: decoders},
	)
	if err != nil {
		t.Fatal(err)
	}
	frames, err := pixeldata.ExtractNativeFramesView(native)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames.Data) != len(tc.ExpectedFrames) || !bytes.Equal(frames.Data[0], tc.ExpectedFrames[0]) {
		t.Fatalf("round trip = %v, want %v", frames.Data, tc.ExpectedFrames)
	}
}

func TestTranscodeNativeRLEPipelineIsBitExactAcrossProfiles(t *testing.T) {
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

	rgb16 := codecfixture.NativeSmall().Object()
	rgb16Pixels := []byte{1, 0x10, 2, 0x20, 3, 0x30, 4, 0x40, 5, 0x50, 6, 0x60}
	putUint16(rgb16, core.NewTag(0x0028, 0x0010), 1)
	putUint16(rgb16, core.NewTag(0x0028, 0x0011), 2)
	putUint16(rgb16, core.NewTag(0x0028, 0x0002), 3)
	putUint16(rgb16, core.NewTag(0x0028, 0x0006), 0)
	putUint16(rgb16, core.NewTag(0x0028, 0x0100), 16)
	putUint16(rgb16, core.NewTag(0x0028, 0x0101), 16)
	putUint16(rgb16, core.NewTag(0x0028, 0x0102), 15)
	rgb16.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0004), VR: core.VRCS}, Value: core.StringValue{"RGB"}})
	rgb16.Put(core.NewRawElement(core.TagPixelData, core.VROW, rgb16Pixels))

	tests := []struct {
		name   string
		object *object.Object
		frames [][]byte
	}{
		{name: "multiframe mono8", object: multi.Object(), frames: multi.ExpectedFrames},
		{name: "signed 12 stored in 16", object: signed12, frames: [][]byte{signed12Pixels}},
		{name: "RGB16 interleaved", object: rgb16, frames: [][]byte{rgb16Pixels}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertRLEPipelineRoundTrip(t, test.object, test.frames)
		})
	}
}

func assertRLEPipelineRoundTrip(t *testing.T, source *object.Object, wantFrames [][]byte) {
	t.Helper()
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	compressed, _, err := pixeldata.TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.RLELossless, pixeldata.TranscodeOptions{EncoderRegistry: encoders})
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := pixeldata.ExtractView(compressed)
	if err != nil {
		t.Fatal(err)
	}
	if len(pixel.Sequence.Fragments) != len(wantFrames) || len(pixel.Sequence.OffsetTable) != len(wantFrames)*4 {
		t.Fatalf("RLE frames/BOT = %d/%d, want %d/%d", len(pixel.Sequence.Fragments), len(pixel.Sequence.OffsetTable), len(wantFrames), len(wantFrames)*4)
	}
	var offset uint32
	for index, fragment := range pixel.Sequence.Fragments {
		if got := binary.LittleEndian.Uint32(pixel.Sequence.OffsetTable[index*4:]); got != offset {
			t.Fatalf("BOT[%d] = %d, want %d", index, got, offset)
		}
		offset += uint32(8 + (len(fragment)+1)&^1)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := rle.Register(decoders); err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(context.Background(), compressed, transfer.RLELossless, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{DecoderRegistry: decoders})
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

func putUint16(dataset *object.Object, tag core.Tag, value uint16) {
	raw := make([]byte, 2)
	binary.LittleEndian.PutUint16(raw, value)
	dataset.Put(core.NewRawElement(tag, core.VRUS, raw))
}

func TestTranscodeNativeBigEndianRoundTrip(t *testing.T) {
	tc := codecfixture.NativeSmall()
	bigEndian, _, err := pixeldata.TranscodeDataSet(context.Background(), tc.Object(), tc.Syntax, transfer.ExplicitVRBigEndian, pixeldata.TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	native, _, err := pixeldata.TranscodeDataSet(context.Background(), bigEndian, transfer.ExplicitVRBigEndian, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	frames, err := pixeldata.ExtractNativeFramesView(native)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(frames.Data[0], tc.ExpectedFrames[0]) {
		t.Fatalf("Big Endian round trip = %v, want %v", frames.Data[0], tc.ExpectedFrames[0])
	}
}

func TestTranscodePathRLEPublishesValidatedPrivateFile(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	tc := codecfixture.NativeSmall()
	sourceBytes, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := rle.Register(decoders); err != nil {
		t.Fatal(err)
	}
	report, err := pixeldata.TranscodePath(context.Background(), sourcePath, destinationPath, transfer.RLELossless, pixeldata.TranscodeOptions{
		DecoderRegistry: decoders, EncoderRegistry: encoders,
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.EncodedBytes == 0 || report.Lossy {
		t.Fatalf("report = %#v", report)
	}
	info, err := os.Stat(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("destination mode = %o, want 600", info.Mode().Perm())
	}
	if gotSource, err := os.ReadFile(sourcePath); err != nil || !bytes.Equal(gotSource, sourceBytes) {
		t.Fatalf("source changed: err=%v", err)
	}
}

func TestTranscodePathRLEAtomicallyReplacesExistingDestination(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not safely replace an existing destination")
	}
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	tc := codecfixture.NativeSmall()
	sourceBytes, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destinationPath, []byte("STALE"), 0o600); err != nil {
		t.Fatal(err)
	}
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := rle.Register(decoders); err != nil {
		t.Fatal(err)
	}
	if _, err := pixeldata.TranscodePath(context.Background(), sourcePath, destinationPath, transfer.RLELossless, pixeldata.TranscodeOptions{
		DecoderRegistry: decoders, EncoderRegistry: encoders,
	}); err != nil {
		t.Fatal(err)
	}
	output, err := object.OpenFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	defer output.Close()
	if output.TransferSyntax.UID != transfer.RLELossless.UID {
		t.Fatalf("destination transfer syntax = %q, want RLE Lossless", output.TransferSyntax.UID)
	}
	temporaryEntries, err := filepath.Glob(filepath.Join(dir, ".dicom-transcode-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(temporaryEntries) != 0 {
		t.Fatalf("temporary entries after replacement = %v", temporaryEntries)
	}
}

func TestTranscodePathRLERequiresDecoderBeforeEncoding(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	tc := codecfixture.NativeSmall()
	sourceBytes, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := rle.RegisterEncoder(encoders); err != nil {
		t.Fatal(err)
	}
	_, err = pixeldata.TranscodePath(context.Background(), sourcePath, destinationPath, transfer.RLELossless, pixeldata.TranscodeOptions{EncoderRegistry: encoders})
	if !errors.Is(err, pixeldata.ErrCodecRegistryNil) {
		t.Fatalf("TranscodePath() error = %v, want ErrCodecRegistryNil", err)
	}
	if _, statErr := os.Stat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want not exist", statErr)
	}
}

func TestTranscodePathCancellationPreservesDestination(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	tc := codecfixture.NativeSmall()
	sourceBytes, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	wantDestination := []byte("EXISTING")
	if err := os.WriteFile(destinationPath, wantDestination, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = pixeldata.TranscodePath(ctx, sourcePath, destinationPath, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("TranscodePath() error = %v, want context.Canceled", err)
	}
	got, err := os.ReadFile(destinationPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wantDestination) {
		t.Fatalf("destination = %q, want unchanged", got)
	}
}

func TestTranscodePathRejectsSymlinkEntries(t *testing.T) {
	dir := t.TempDir()
	tc := codecfixture.NativeSmall()
	sourceBytes, err := tc.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	realSource := filepath.Join(dir, "real-source.dcm")
	if err := os.WriteFile(realSource, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceLink := filepath.Join(dir, "source-link.dcm")
	if err := os.Symlink(realSource, sourceLink); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err = pixeldata.TranscodePath(context.Background(), sourceLink, filepath.Join(dir, "output.dcm"), transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{})
	if err == nil {
		t.Fatal("TranscodePath accepted a source symlink")
	}

	destinationTarget := filepath.Join(dir, "destination-target.dcm")
	if err := os.WriteFile(destinationTarget, []byte("KEEP"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinationLink := filepath.Join(dir, "destination-link.dcm")
	if err := os.Symlink(destinationTarget, destinationLink); err != nil {
		t.Fatal(err)
	}
	_, err = pixeldata.TranscodePath(context.Background(), realSource, destinationLink, transfer.ExplicitVRLittleEndian, pixeldata.TranscodeOptions{})
	if !errors.Is(err, pixeldata.ErrTranscodeDestinationUnsafe) {
		t.Fatalf("destination symlink error = %v, want ErrTranscodeDestinationUnsafe", err)
	}
	if got, readErr := os.ReadFile(destinationTarget); readErr != nil || string(got) != "KEEP" {
		t.Fatalf("destination symlink target changed: %q, %v", got, readErr)
	}
}

func TestTranscodePathRejectsUndecodableLossyEncoderOutput(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.dcm")
	destinationPath := filepath.Join(dir, "destination.dcm")
	sourceBytes, err := codecfixture.NativeSmall().Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourcePath, sourceBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	encoders := pixeldata.NewMemoryEncoderRegistry()
	if err := encoders.RegisterEncoder(transfer.JPEGBaseline.UID, invalidLossyEncoder{}); err != nil {
		t.Fatal(err)
	}
	decoders := pixeldata.NewMemoryRegistry()
	if err := jpeg.Register(decoders); err != nil {
		t.Fatal(err)
	}
	_, err = pixeldata.TranscodePath(context.Background(), sourcePath, destinationPath, transfer.JPEGBaseline, pixeldata.TranscodeOptions{
		EncoderRegistry: encoders,
		DecoderRegistry: decoders,
		AllowLossy:      true,
	})
	if err == nil {
		t.Fatal("TranscodePath published an undecodable lossy payload")
	}
	if _, statErr := os.Stat(destinationPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("destination stat error = %v, want not exist", statErr)
	}
}

type invalidLossyEncoder struct{}

func (invalidLossyEncoder) Capabilities() pixeldata.EncoderCapabilities {
	return pixeldata.EncoderCapabilities{
		TransferSyntaxUID:          transfer.JPEGBaseline.UID,
		BitsAllocated:              []uint16{8},
		PixelRepresentations:       []uint16{0},
		SamplesPerPixel:            []uint16{1},
		PhotometricInterpretations: []string{"MONOCHROME2"},
		LossyMethod:                "ISO_10918_1",
		SupportsMultiFrame:         true,
	}
}

func (invalidLossyEncoder) EncodeFrame(context.Context, []byte, pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	return pixeldata.EncodedFrame{Data: []byte{0xff, 0xd8, 0xff, 0xd9}}, nil
}
