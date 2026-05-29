package pixeldata_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	stdjpeg "image/jpeg"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	decompressTagSOPClassUID               = core.NewTag(0x0008, 0x0016)
	decompressTagSOPInstanceUID            = core.NewTag(0x0008, 0x0018)
	decompressTagTransferSyntaxUID         = core.NewTag(0x0002, 0x0010)
	decompressTagSamplesPerPixel           = core.NewTag(0x0028, 0x0002)
	decompressTagPhotometricInterpretation = core.NewTag(0x0028, 0x0004)
	decompressTagPlanarConfiguration       = core.NewTag(0x0028, 0x0006)
	decompressTagNumberOfFrames            = core.NewTag(0x0028, 0x0008)
	decompressTagRows                      = core.NewTag(0x0028, 0x0010)
	decompressTagColumns                   = core.NewTag(0x0028, 0x0011)
	decompressTagBitsAllocated             = core.NewTag(0x0028, 0x0100)
	decompressTagBitsStored                = core.NewTag(0x0028, 0x0101)
	decompressTagHighBit                   = core.NewTag(0x0028, 0x0102)
	decompressTagPixelRepresentation       = core.NewTag(0x0028, 0x0103)
	decompressTagExtendedOffsetTable       = core.NewTag(0x7FE0, 0x0001)
	decompressTagExtendedOffsetTableLength = core.NewTag(0x7FE0, 0x0002)
	decompressTagEncapsulatedValueLength   = core.NewTag(0x7FE0, 0x0003)
)

func TestDecompressFileNativeNoOpDefaultsToExplicitVRLittleEndian(t *testing.T) {
	tc := codecfixture.NativeSmall()
	file := tc.File()
	file.TransferSyntax = transfer.ImplicitVRLittleEndian

	got, err := pixeldata.DecompressFile(file, pixeldata.DecompressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got == file || got.Dataset == file.Dataset {
		t.Fatal("DecompressFile() returned source aliases, want independent file/dataset")
	}
	if got.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntax = %q, want %q", got.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if raw := rawPixelData(t, got.Dataset); !bytes.Equal(raw, tc.ExpectedFrames[0]) {
		t.Fatalf("PixelData = %v, want native no-op bytes %v", raw, tc.ExpectedFrames[0])
	}
	if got, want := uidValue(t, got.Dataset, decompressTagSOPInstanceUID), uidValue(t, file.Dataset, decompressTagSOPInstanceUID); got != want {
		t.Fatalf("SOPInstanceUID = %q, want preserved %q", got, want)
	}
}

func TestDecompressDataSetReturnsTargetSyntaxAndNativePixelData(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}

	got, syntax, err := pixeldata.DecompressDataSet(tc.Object(), tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if syntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("target syntax = %q, want %q", syntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if raw := rawPixelData(t, got); !bytes.Equal(raw, tc.ExpectedFrames[0]) {
		t.Fatalf("PixelData = %v, want decompressed RLE bytes %v", raw, tc.ExpectedFrames[0])
	}
}

func TestDecompressDataSetRemovesEncapsulatedOffsetMetadata(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	dataset := tc.Object()
	stale := []struct {
		tag core.Tag
		vr  core.VR
	}{
		{decompressTagExtendedOffsetTable, core.VROV},
		{decompressTagExtendedOffsetTableLength, core.VROV},
		{decompressTagEncapsulatedValueLength, core.VRUV},
	}
	for _, item := range stale {
		dataset.Put(core.NewRawElement(item.tag, item.vr, make([]byte, 8)))
	}
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}

	got, _, err := pixeldata.DecompressDataSet(dataset, tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range stale {
		if _, ok := got.Get(item.tag); ok {
			t.Errorf("decompressed dataset retained stale encapsulated tag %s", item.tag)
		}
		if _, ok := dataset.Get(item.tag); !ok {
			t.Errorf("source dataset lost tag %s", item.tag)
		}
	}
}

func TestDecompressDataSetContextUsesContextCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	codec := &contextDecompressCodec{frames: pixeldata.Frames{
		Rows:    2,
		Columns: 2,
		Data:    [][]byte{{0x11, 0x22, 0x33, 0x44}},
	}}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	ctx := context.WithValue(context.Background(), decompressContextKey{}, "present")
	got, syntax, err := pixeldata.DecompressDataSetContext(ctx, tc.Object(), tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}
	if codec.contextCalls != 1 || codec.legacyCalls != 0 || codec.contextValue != "present" {
		t.Fatalf("codec calls = context:%d legacy:%d value:%q, want 1/0/present", codec.contextCalls, codec.legacyCalls, codec.contextValue)
	}
	if syntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("target syntax = %q, want explicit VR little endian", syntax.UID)
	}
	if raw := rawPixelData(t, got); !bytes.Equal(raw, []byte{0x11, 0x22, 0x33, 0x44}) {
		t.Fatalf("PixelData = % X, want contextual decoder output", raw)
	}
}

func TestDecompressDataSetContextValidatesPixelRepresentationBeforeContextCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	dataset := tc.Object()
	dataset.Put(core.NewRawElement(core.TagPixelData, core.VROB, []byte{0x11, 0x22, 0x33, 0x44}))
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), dataset, tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if !errors.Is(err, pixeldata.ErrIncompatiblePixelData) {
		t.Fatalf("error = %v, want ErrIncompatiblePixelData", err)
	}
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want representation rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetContextChecksCancellationWhileCollectingFrames(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	codec := &contextDecompressCodec{frames: pixeldata.Frames{
		Rows:    2,
		Columns: 2,
		Data:    [][]byte{{0x11, 0x22, 0x33, 0x44}},
	}}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelAfterErrChecksContext{Context: context.Background(), cancelAt: 4}

	_, _, err := pixeldata.DecompressDataSetContext(ctx, tc.Object(), tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled during frame collection", err)
	}
}

func TestDecompressDataSetContextReturnsCancellationFromBackendBoundary(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	ctx, cancel := context.WithCancel(context.Background())
	codec := &contextDecompressCodec{
		frames: pixeldata.Frames{
			Rows:    2,
			Columns: 2,
			Data:    [][]byte{{0x11, 0x22, 0x33, 0x44}},
		},
		cancel: cancel,
	}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(ctx, tc.Object(), tc.Syntax, pixeldata.DecompressOptions{Registry: registry})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled after backend", err)
	}
}

func TestDefaultDecompressLimitsAreFinite(t *testing.T) {
	limits := pixeldata.DefaultDecompressLimits()
	if limits.MaxFrames <= 0 || limits.MaxPixels <= 0 || limits.MaxNativeBytes <= 0 || limits.MaxInputBytes <= 0 || limits.MaxExpansionRatio <= 0 {
		t.Fatalf("DefaultDecompressLimits() = %+v, want positive finite fields", limits)
	}
}

func TestDecompressDataSetContextRejectsFrameLimitBeforeCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	dataset := tc.Object()
	dataset.Put(dicomtest.NewStringElement(decompressTagNumberOfFrames, core.VRIS, "2"))
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), dataset, tc.Syntax, pixeldata.DecompressOptions{
		Registry: registry,
		Limits:   pixeldata.DecompressLimits{MaxFrames: 1},
	})
	if !errors.Is(err, pixeldata.ErrDecompressResourceLimit) {
		t.Fatalf("error = %v, want ErrDecompressResourceLimit", err)
	}
	var limitErr *pixeldata.DecompressResourceLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != "MaxFrames" || limitErr.Actual != 2 || limitErr.Maximum != 1 {
		t.Fatalf("error = %#v, want MaxFrames actual=2 maximum=1", err)
	}
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetContextRejectsPixelLimitBeforeCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), tc.Object(), tc.Syntax, pixeldata.DecompressOptions{
		Registry: registry,
		Limits:   pixeldata.DecompressLimits{MaxPixels: 1},
	})
	assertDecompressLimit(t, err, "MaxPixels", 4, 1)
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetContextRejectsNativeByteLimitBeforeCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), tc.Object(), tc.Syntax, pixeldata.DecompressOptions{
		Registry: registry,
		Limits:   pixeldata.DecompressLimits{MaxNativeBytes: 3},
	})
	assertDecompressLimit(t, err, "MaxNativeBytes", 4, 3)
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetContextNativeYBRFull422UsesPackedNativeBudget(t *testing.T) {
	dataset := object.FromElements(append(
		decompressColorMetadataElements(1, 2, "YBR_FULL_422"),
		core.NewRawElement(core.TagPixelData, core.VROB, []byte{10, 20, 128, 128}),
	), std.Dictionary)

	got, _, err := pixeldata.DecompressDataSetContext(context.Background(), dataset, transfer.ExplicitVRLittleEndian, pixeldata.DecompressOptions{
		Limits: pixeldata.DecompressLimits{MaxNativeBytes: 4, MaxExpansionRatio: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	if raw := rawPixelData(t, got); !bytes.Equal(raw, []byte{10, 20, 128, 128}) {
		t.Fatalf("PixelData = %v, want packed YBR_FULL_422 bytes", raw)
	}
}

func TestDecompressDataSetContextRejectsInputByteLimitBeforeCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	dataset := tc.Object()
	dataset.Put(dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, []byte{0x01, 0x02}))
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), dataset, tc.Syntax, pixeldata.DecompressOptions{
		Registry: registry,
		Limits:   pixeldata.DecompressLimits{MaxInputBytes: 1},
	})
	assertDecompressLimit(t, err, "MaxInputBytes", 2, 1)
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetContextRejectsExpansionLimitBeforeCodec(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	dataset := tc.Object()
	dataset.Put(dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, []byte{0x01}))
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, _, err := pixeldata.DecompressDataSetContext(context.Background(), dataset, tc.Syntax, pixeldata.DecompressOptions{
		Registry: registry,
		Limits:   pixeldata.DecompressLimits{MaxExpansionRatio: 3},
	})
	assertDecompressLimit(t, err, "MaxExpansionRatio", 4, 3)
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

func TestDecompressDataSetUpdatesJPEGYBRMetadataToDecodedRGB(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := jpeg.Register(registry); err != nil {
		t.Fatal(err)
	}
	file := jpegBaselineColorFile(t, "YBR_FULL_422", []color.RGBA{
		{R: 255, G: 0, B: 0, A: 255},
	})

	got, _, err := pixeldata.DecompressDataSet(file.Dataset, file.TransferSyntax, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}

	photometric, ok := got.GetString(decompressTagPhotometricInterpretation)
	if !ok {
		t.Fatal("PhotometricInterpretation missing")
	}
	if photometric != "RGB" {
		t.Fatalf("PhotometricInterpretation = %q, want RGB", photometric)
	}
	if planar := uint16Value(t, got, decompressTagPlanarConfiguration); planar != 0 {
		t.Fatalf("PlanarConfiguration = %d, want 0", planar)
	}
	raw := rawPixelData(t, got)
	if len(raw) != 3 {
		t.Fatalf("PixelData length = %d, want one RGB pixel", len(raw))
	}
	if raw[0] <= raw[1] || raw[0] <= raw[2] {
		t.Fatalf("PixelData = %v, want decoded RGB red-dominant bytes", raw)
	}
}

func TestDecompressDataSetKeepsJPEGYBRFull422WhenDecoderReturnsSubsampledBytes(t *testing.T) {
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(transfer.JPEGBaseline.UID, fakeDecompressCodec{
		frames: pixeldata.Frames{
			Rows:    1,
			Columns: 2,
			Data:    [][]byte{{10, 20, 128, 128}},
		},
	}); err != nil {
		t.Fatal(err)
	}
	dataset := object.FromElements(append(
		decompressColorMetadataElements(1, 2, "YBR_FULL_422"),
		dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, []byte{0x00}),
	), std.Dictionary)

	got, _, err := pixeldata.DecompressDataSet(dataset, transfer.JPEGBaseline, pixeldata.DecompressOptions{Registry: registry})
	if err != nil {
		t.Fatal(err)
	}

	photometric, ok := got.GetString(decompressTagPhotometricInterpretation)
	if !ok {
		t.Fatal("PhotometricInterpretation missing")
	}
	if photometric != "YBR_FULL_422" {
		t.Fatalf("PhotometricInterpretation = %q, want YBR_FULL_422", photometric)
	}
	if raw := rawPixelData(t, got); !bytes.Equal(raw, []byte{10, 20, 128, 128}) {
		t.Fatalf("PixelData = %v, want original YBR_FULL_422 bytes", raw)
	}
}

func TestDecompressFileExplicitVRBigEndian16BitTranscodesToLittleEndian(t *testing.T) {
	file := bigEndianNativeDecompressFile(t, 16, 16, 15, u16Bytes(binary.BigEndian, 0x1234, 0xabcd))

	got, err := pixeldata.DecompressFile(file, pixeldata.DecompressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntax = %q, want %q", got.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
	}
	if raw := rawPixelData(t, got.Dataset); !bytes.Equal(raw, u16Bytes(binary.LittleEndian, 0x1234, 0xabcd)) {
		t.Fatalf("PixelData = % X, want little-endian samples", raw)
	}

	roundTrip := roundTripDecompressedFile(t, got)
	metadata, err := pixeldata.ExtractMetadata(roundTrip.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Rows != 1 || metadata.Columns != 2 || metadata.BitsAllocated != 16 || metadata.BitsStored != 16 || metadata.HighBit != 15 {
		t.Fatalf("round-trip metadata = %+v, want little-endian metadata preserved", metadata)
	}
	if raw := rawPixelData(t, roundTrip.Dataset); !bytes.Equal(raw, u16Bytes(binary.LittleEndian, 0x1234, 0xabcd)) {
		t.Fatalf("round-trip PixelData = % X, want little-endian samples", raw)
	}
}

func TestDecompressFileExplicitVRBigEndian8BitKeepsPixelBytes(t *testing.T) {
	file := bigEndianNativeDecompressFile(t, 8, 8, 7, []byte{0x12, 0xab})

	got, err := pixeldata.DecompressFile(file, pixeldata.DecompressOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if raw := rawPixelData(t, got.Dataset); !bytes.Equal(raw, []byte{0x12, 0xab}) {
		t.Fatalf("PixelData = % X, want unchanged 8-bit samples", raw)
	}
	metadata, err := pixeldata.ExtractMetadata(got.Dataset)
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Rows != 1 || metadata.Columns != 2 || metadata.BitsAllocated != 8 {
		t.Fatalf("metadata = %+v, want 8-bit big-endian source decoded", metadata)
	}
}

func TestDecompressFileSupportedStillImageCodecs(t *testing.T) {
	tests := []decompressCase{
		jpegBaselineDecompressCase(t, []byte{0, 255}),
		codecFixtureDecompressCase(t, codecfixture.RLELosslessSmall()),
		codecFixtureDecompressCase(t, codecfixture.JPEGExtendedSmall()),
		codecFixtureDecompressCase(t, codecfixture.JPEGLosslessSmall()),
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := pixeldata.DecompressFile(tt.file, pixeldata.DecompressOptions{Registry: tt.registry})
			if err != nil {
				t.Fatal(err)
			}
			if got.TransferSyntax.UID != transfer.ExplicitVRLittleEndian.UID {
				t.Fatalf("TransferSyntax = %q, want %q", got.TransferSyntax.UID, transfer.ExplicitVRLittleEndian.UID)
			}
			if raw := rawPixelData(t, got.Dataset); !bytesWithinTolerance(raw, tt.wantFrame, tt.tolerance) {
				t.Fatalf("PixelData = %v, want %v within tolerance %d", raw, tt.wantFrame, tt.tolerance)
			}
		})
	}
}

func TestDecompressFileUsesConfiguredNativeTargetSyntax(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}

	got, err := pixeldata.DecompressFile(tc.File(), pixeldata.DecompressOptions{
		Registry:             registry,
		TargetTransferSyntax: transfer.ImplicitVRLittleEndian,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.TransferSyntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("TransferSyntax = %q, want %q", got.TransferSyntax.UID, transfer.ImplicitVRLittleEndian.UID)
	}
}

func TestDecompressDataSetRejectsUnsupportedBigEndianTarget(t *testing.T) {
	tc := codecfixture.NativeSmall()

	_, _, err := pixeldata.DecompressDataSet(tc.Object(), transfer.ExplicitVRLittleEndian, pixeldata.DecompressOptions{
		TargetTransferSyntax: transfer.ExplicitVRBigEndian,
	})
	if !errors.Is(err, pixeldata.ErrUnsupportedDecompression) {
		t.Fatalf("target Big Endian error = %v, want ErrUnsupportedDecompression", err)
	}
}

func TestDecompressFileErrorsStayTyped(t *testing.T) {
	tests := []struct {
		name string
		tc   codecfixture.Case
		want error
	}{
		{
			name: "missing optional adapter",
			tc:   codecfixture.MissingJPEGXLAdapter(),
			want: pixeldata.ErrCodecNotFound,
		},
		{
			name: "unavailable optional dependency",
			tc:   codecfixture.DependencyUnavailableJPEGXL(),
			want: codecfixture.ErrDependencyUnavailable,
		},
		{
			name: "malformed pixel data",
			tc:   codecfixture.MalformedJPEGExtended(),
			want: jpeg.ErrInvalidFragment,
		},
		{
			name: "unsupported metadata",
			tc:   codecfixture.UnsupportedMetadataJPEGExtended(),
			want: jpeg.ErrUnsupportedBitsAllocated,
		},
		{
			name: "metadata mismatch",
			tc:   codecfixture.MetadataMismatchRLE(),
			want: pixeldata.ErrPixelDataSizeMismatch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			registry, err := tt.tc.Registry()
			if err != nil {
				t.Fatal(err)
			}
			_, err = pixeldata.DecompressFile(tt.tc.File(), pixeldata.DecompressOptions{Registry: registry})
			if !errors.Is(err, tt.want) {
				t.Fatalf("DecompressFile() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestDecompressFileContextRejectsTransferSyntaxMismatchWithFileMeta(t *testing.T) {
	tc := codecfixture.RLELosslessSmall()
	file := tc.File()
	file.Meta = object.FromElements([]core.Element{
		dicomtest.NewStringElement(decompressTagTransferSyntaxUID, core.VRUI, transfer.JPEGBaseline.UID),
	}, std.Dictionary)
	codec := &contextDecompressCodec{}
	registry := pixeldata.NewMemoryRegistry()
	if err := registry.RegisterCodec(tc.Syntax.UID, codec); err != nil {
		t.Fatal(err)
	}

	_, err := pixeldata.DecompressFileContext(context.Background(), file, pixeldata.DecompressOptions{Registry: registry})
	if !errors.Is(err, pixeldata.ErrIncompatiblePixelData) {
		t.Fatalf("error = %v, want ErrIncompatiblePixelData", err)
	}
	if codec.contextCalls != 0 || codec.legacyCalls != 0 {
		t.Fatalf("codec calls = context:%d legacy:%d, want preflight rejection", codec.contextCalls, codec.legacyCalls)
	}
}

type decompressCase struct {
	name      string
	file      *object.File
	registry  pixeldata.Registry
	wantFrame []byte
	tolerance byte
}

func jpegBaselineDecompressCase(t *testing.T, pixels []byte) decompressCase {
	t.Helper()
	registry := pixeldata.NewMemoryRegistry()
	if err := jpeg.Register(registry); err != nil {
		t.Fatal(err)
	}
	return decompressCase{
		name:      "jpeg-baseline",
		file:      jpegBaselineFile(t, pixels),
		registry:  registry,
		wantFrame: append([]byte(nil), pixels...),
		tolerance: 32,
	}
}

func codecFixtureDecompressCase(t *testing.T, tc codecfixture.Case) decompressCase {
	t.Helper()
	registry, err := tc.Registry()
	if err != nil {
		t.Fatal(err)
	}
	return decompressCase{
		name:      tc.Name,
		file:      tc.File(),
		registry:  registry,
		wantFrame: tc.ExpectedFrames[0],
		tolerance: tc.Tolerance,
	}
}

func jpegBaselineFile(t *testing.T, pixels []byte) *object.File {
	t.Helper()
	return &object.File{
		Dataset: object.FromElements(append(
			decompressMetadataElements(1, uint16(len(pixels))),
			dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeGrayJPEG(t, len(pixels), 1, pixels)),
		), std.Dictionary),
		TransferSyntax: transfer.JPEGBaseline,
	}
}

func jpegBaselineColorFile(t *testing.T, photometric string, pixels []color.RGBA) *object.File {
	t.Helper()
	return &object.File{
		Dataset: object.FromElements(append(
			decompressColorMetadataElements(1, uint16(len(pixels)), photometric),
			dicomtest.NewFragmentSequenceElement(core.TagPixelData, nil, encodeColorJPEG(t, len(pixels), 1, pixels)),
		), std.Dictionary),
		TransferSyntax: transfer.JPEGBaseline,
	}
}

func bigEndianNativeDecompressFile(t *testing.T, bitsAllocated, bitsStored, highBit uint16, pixelBytes []byte) *object.File {
	t.Helper()
	vr := core.VROB
	if bitsAllocated > 8 {
		vr = core.VROW
	}
	elements := append(
		decompressMetadataElementsWithOrder(binary.BigEndian, 1, 2, bitsAllocated, bitsStored, highBit),
		core.NewRawElement(core.TagPixelData, vr, pixelBytes),
	)
	data, err := dicomtest.Part10File(transfer.ExplicitVRBigEndian, elements...)
	if err != nil {
		t.Fatal(err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	return file
}

func decompressMetadataElements(rows, columns uint16) []core.Element {
	return decompressMetadataElementsWithOrder(nil, rows, columns, 8, 8, 7)
}

func decompressMetadataElementsWithOrder(order binary.ByteOrder, rows, columns, bitsAllocated, bitsStored, highBit uint16) []core.Element {
	return []core.Element{
		dicomtest.NewStringElement(decompressTagSOPClassUID, core.VRUI, dicomtest.TestSOPClassUID),
		dicomtest.NewStringElement(decompressTagSOPInstanceUID, core.VRUI, dicomtest.TestSOPInstanceUID),
		dicomtest.Uint16Element(decompressTagRows, core.VRUS, order, rows),
		dicomtest.Uint16Element(decompressTagColumns, core.VRUS, order, columns),
		dicomtest.Uint16Element(decompressTagSamplesPerPixel, core.VRUS, order, 1),
		dicomtest.NewStringElement(decompressTagPhotometricInterpretation, core.VRCS, "MONOCHROME2"),
		dicomtest.Uint16Element(decompressTagBitsAllocated, core.VRUS, order, bitsAllocated),
		dicomtest.Uint16Element(decompressTagBitsStored, core.VRUS, order, bitsStored),
		dicomtest.Uint16Element(decompressTagHighBit, core.VRUS, order, highBit),
		dicomtest.Uint16Element(decompressTagPixelRepresentation, core.VRUS, order, 0),
	}
}

func decompressColorMetadataElements(rows, columns uint16, photometric string) []core.Element {
	return []core.Element{
		dicomtest.NewStringElement(decompressTagSOPClassUID, core.VRUI, dicomtest.TestSOPClassUID),
		dicomtest.NewStringElement(decompressTagSOPInstanceUID, core.VRUI, dicomtest.TestSOPInstanceUID),
		dicomtest.Uint16Element(decompressTagRows, core.VRUS, nil, rows),
		dicomtest.Uint16Element(decompressTagColumns, core.VRUS, nil, columns),
		dicomtest.Uint16Element(decompressTagSamplesPerPixel, core.VRUS, nil, 3),
		dicomtest.NewStringElement(decompressTagPhotometricInterpretation, core.VRCS, photometric),
		dicomtest.Uint16Element(decompressTagBitsAllocated, core.VRUS, nil, 8),
		dicomtest.Uint16Element(decompressTagBitsStored, core.VRUS, nil, 8),
		dicomtest.Uint16Element(decompressTagHighBit, core.VRUS, nil, 7),
		dicomtest.Uint16Element(decompressTagPixelRepresentation, core.VRUS, nil, 0),
	}
}

func encodeGrayJPEG(t *testing.T, width, height int, pixels []byte) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	if len(pixels) != len(img.Pix) {
		t.Fatalf("pixel count = %d, want %d", len(pixels), len(img.Pix))
	}
	copy(img.Pix, pixels)
	for i := range img.Pix {
		img.SetGray(i%width, i/width, color.Gray{Y: img.Pix[i]})
	}
	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func encodeColorJPEG(t *testing.T, width, height int, pixels []color.RGBA) []byte {
	t.Helper()
	if len(pixels) != width*height {
		t.Fatalf("pixel count = %d, want %d", len(pixels), width*height)
	}
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for i, pixel := range pixels {
		img.SetRGBA(i%width, i/width, pixel)
	}
	var buf bytes.Buffer
	if err := stdjpeg.Encode(&buf, img, &stdjpeg.Options{Quality: 100}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func rawPixelData(t *testing.T, obj *object.Object) []byte {
	t.Helper()
	elem, ok := obj.Get(core.TagPixelData)
	if !ok {
		t.Fatal("PixelData missing")
	}
	raw, ok := elem.RawBytes()
	if !ok {
		t.Fatalf("PixelData = %T, want native raw bytes", elem.Value)
	}
	return raw
}

func assertDecompressLimit(t *testing.T, err error, limit string, actual, maximum int64) {
	t.Helper()
	if !errors.Is(err, pixeldata.ErrDecompressResourceLimit) {
		t.Fatalf("error = %v, want ErrDecompressResourceLimit", err)
	}
	var limitErr *pixeldata.DecompressResourceLimitError
	if !errors.As(err, &limitErr) || limitErr.Limit != limit || limitErr.Actual != actual || limitErr.Maximum != maximum {
		t.Fatalf("error = %#v, want %s actual=%d maximum=%d", err, limit, actual, maximum)
	}
}

func uint16Value(t *testing.T, obj *object.Object, tag core.Tag) uint16 {
	t.Helper()
	raw, ok := obj.GetRaw(tag)
	if !ok {
		t.Fatalf("missing raw element %s", tag)
	}
	if len(raw) != 2 {
		t.Fatalf("%s length = %d, want 2", tag, len(raw))
	}
	return binary.LittleEndian.Uint16(raw)
}

func roundTripDecompressedFile(t *testing.T, file *object.File) *object.File {
	t.Helper()
	var buf bytes.Buffer
	if err := object.WriteFile(&buf, file); err != nil {
		t.Fatal(err)
	}
	roundTrip, err := object.ReadFile(bytes.NewReader(buf.Bytes()))
	if err != nil {
		t.Fatal(err)
	}
	return roundTrip
}

type fakeDecompressCodec struct {
	frames pixeldata.Frames
}

func (c fakeDecompressCodec) Decode(pixeldata.PixelData, *object.Object) (pixeldata.Frames, error) {
	return c.frames, nil
}

type decompressContextKey struct{}

type cancelAfterErrChecksContext struct {
	context.Context
	cancelAt int
	checks   int
}

func (c *cancelAfterErrChecksContext) Err() error {
	c.checks++
	if c.checks >= c.cancelAt {
		return context.Canceled
	}
	return nil
}

type contextDecompressCodec struct {
	frames       pixeldata.Frames
	contextCalls int
	legacyCalls  int
	contextValue string
	cancel       context.CancelFunc
}

func (c *contextDecompressCodec) Decode(pixeldata.PixelData, *object.Object) (pixeldata.Frames, error) {
	c.legacyCalls++
	return c.frames, nil
}

func (c *contextDecompressCodec) DecodeContext(ctx context.Context, _ pixeldata.PixelData, _ *object.Object) (pixeldata.Frames, error) {
	c.contextCalls++
	c.contextValue, _ = ctx.Value(decompressContextKey{}).(string)
	if c.cancel != nil {
		c.cancel()
	}
	return c.frames, nil
}

func u16Bytes(order binary.ByteOrder, values ...uint16) []byte {
	if order == nil {
		order = binary.LittleEndian
	}
	raw := make([]byte, len(values)*2)
	for i, value := range values {
		order.PutUint16(raw[i*2:], value)
	}
	return raw
}

func uidValue(t *testing.T, obj *object.Object, tag core.Tag) string {
	t.Helper()
	value, ok := obj.GetString(tag)
	if !ok {
		t.Fatalf("missing UID %s", tag)
	}
	return transfer.NormalizeUID(value)
}

func bytesWithinTolerance(got, want []byte, tolerance byte) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		delta := int(got[i]) - int(want[i])
		if delta < 0 {
			delta = -delta
		}
		if delta > int(tolerance) {
			return false
		}
	}
	return true
}
