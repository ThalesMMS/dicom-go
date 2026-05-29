package pixeldata

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestFrameOffsetTablesUseItemOffsetsPaddingAndEOTFallback(t *testing.T) {
	bot, eot, lengths, err := frameOffsetTables([]uint64{3, 4, 5})
	if err != nil {
		t.Fatal(err)
	}
	if eot != nil || lengths != nil {
		t.Fatalf("unexpected EOT for uint32 offsets: %v %v", eot, lengths)
	}
	want := make([]byte, 12)
	binary.LittleEndian.PutUint32(want[4:], 12) // 8-byte Item header + even(3)
	binary.LittleEndian.PutUint32(want[8:], 24) // previous + 8 + even(4)
	if !bytes.Equal(bot, want) {
		t.Fatalf("BOT = %v, want %v", bot, want)
	}

	bot, eot, lengths, err = frameOffsetTables([]uint64{uint64(^uint32(0)), 1})
	if err != nil {
		t.Fatal(err)
	}
	if bot != nil || len(eot) != 16 || len(lengths) != 16 {
		t.Fatalf("EOT fallback = bot:%v offsets:%d lengths:%d", bot, len(eot), len(lengths))
	}
	if got := binary.LittleEndian.Uint64(eot[8:]); got != uint64(^uint32(0))+9 {
		t.Fatalf("second EOT offset = %d", got)
	}
	if got := binary.LittleEndian.Uint64(lengths[:8]); got != uint64(^uint32(0)) {
		t.Fatalf("first EOT length = %d", got)
	}
}

func TestTranscodeDataSetRejectsRepresentationAndPixelKindMismatches(t *testing.T) {
	native := transcodeNativeObject([]byte{1, 2, 3, 4})
	_, _, err := TranscodeDataSet(context.Background(), native, transfer.RLELossless, transfer.ExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrIncompatiblePixelData) {
		t.Fatalf("native under RLE error = %v", err)
	}

	float := object.FromElements(removeElements(native.Elements(), core.TagPixelData), nil)
	float.Put(core.NewRawElement(tagFloatPixelData, core.VROF, []byte{0, 0, 0, 0}))
	_, _, err = TranscodeDataSet(context.Background(), float, transfer.ExplicitVRLittleEndian, transfer.RLELossless, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("Float Pixel Data error = %v", err)
	}

	fragmented := transcodeNativeObject([]byte{1, 2, 3, 4})
	fragmented.Put(core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{{1, 2, 3, 4}}},
	})
	_, _, err = TranscodeDataSet(context.Background(), fragmented, transfer.ExplicitVRLittleEndian, transfer.ImplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrIncompatiblePixelData) {
		t.Fatalf("fragmented Pixel Data under native syntax error = %v", err)
	}
}

func TestTranscodeDataSetRejectsNestedAndBitPackedPerFramePixelData(t *testing.T) {
	nested := transcodeNativeObject([]byte{1, 2, 3, 4})
	nested.Put(core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x0088, 0x0200), VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(core.TagPixelData, core.VROB, []byte{9}),
		}}}},
	})
	_, _, err := TranscodeDataSet(context.Background(), nested, transfer.ExplicitVRLittleEndian, transfer.EncapsulatedUncompressedExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("nested Pixel Data error = %v", err)
	}

	packed := transcodeNativeObject([]byte{0x03})
	packed.Put(uint16RawElement(tagRows, 1))
	packed.Put(uint16RawElement(tagColumns, 2))
	packed.Put(uint16RawElement(tagBitsAllocated, 1))
	packed.Put(uint16RawElement(tagBitsStored, 1))
	packed.Put(uint16RawElement(tagHighBit, 0))
	_, _, err = TranscodeDataSet(context.Background(), packed, transfer.ExplicitVRLittleEndian, transfer.EncapsulatedUncompressedExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("bit-packed per-frame error = %v", err)
	}
}

func TestTranscodeEncapsulatedUncompressedRoundTrip(t *testing.T) {
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	encoded, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.EncapsulatedUncompressedExplicitVRLittleEndian, TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	pixel, err := ExtractView(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !pixel.Encapsulated || len(pixel.Sequence.Fragments) != 1 || !bytes.Equal(pixel.Sequence.Fragments[0], []byte{1, 2, 3, 4}) {
		t.Fatalf("encapsulated Pixel Data = %#v", pixel)
	}
	native, _, err := TranscodeDataSet(context.Background(), encoded, transfer.EncapsulatedUncompressedExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	raw, ok := native.GetRaw(core.TagPixelData)
	if !ok || !bytes.Equal(raw, []byte{1, 2, 3, 4}) {
		t.Fatalf("native Pixel Data = %v, %v", raw, ok)
	}
}

func TestTranscodeDataSetEquivalentFastPathDetachesAndPreservesPayload(t *testing.T) {
	fragment := []byte{1, 2, 3, 4}
	obj := transcodeNativeObject([]byte{0, 0, 0, 0})
	obj.Put(core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{OffsetTable: []byte{0, 0, 0, 0}, Fragments: [][]byte{fragment}},
	})
	obj.Put(core.NewRawElement(tagExtendedOffsetTable, core.VROV, []byte{9, 8, 7, 6, 0, 0, 0, 0}))

	got, report, err := TranscodeDataSet(context.Background(), obj, transfer.RLELossless, transfer.RLELossless, TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.PixelDataPreserved {
		t.Fatalf("report = %#v, want preserved", report)
	}
	pixel, err := ExtractView(got)
	if err != nil {
		t.Fatal(err)
	}
	pixel.Sequence.Fragments[0][0] = 0xff
	before, _ := ExtractView(obj)
	if before.Sequence.Fragments[0][0] != 1 {
		t.Fatal("equivalent fast path aliased source fragment")
	}
	if _, ok := got.Get(tagExtendedOffsetTable); !ok {
		t.Fatal("equivalent fast path removed existing EOT")
	}
}

func TestTranscodeFileAcceptsConstructedFileWithoutMetaAndRejectsMismatch(t *testing.T) {
	constructed := &object.File{
		Dataset:        transcodeNativeObject([]byte{1, 2, 3, 4}),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	got, _, err := TranscodeFile(nil, constructed, transfer.ImplicitVRLittleEndian, TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Meta == nil {
		t.Fatal("TranscodeFile did not synthesize File Meta")
	}
	if syntaxUID, ok := got.Meta.GetString(tagFileMetaTransferSyntaxUID); !ok || syntaxUID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("File Meta Transfer Syntax UID = %q, %v", syntaxUID, ok)
	}

	constructed.Meta = object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagFileMetaTransferSyntaxUID, VR: core.VRUI}, Value: core.StringValue{transfer.RLELossless.UID}},
	}, nil)
	_, _, err = TranscodeFile(context.Background(), constructed, transfer.ImplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrIncompatiblePixelData) {
		t.Fatalf("mismatched File/Meta transfer syntax error = %v", err)
	}
}

func TestTranscodeDataSetRejectsUnknownVREndianConversion(t *testing.T) {
	source := object.FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VRUN, []byte{1, 2, 3, 4}),
	}, nil)
	source.SetValueByteOrder(binary.BigEndian)
	_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRBigEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("VR UN endian conversion error = %v", err)
	}
}

type lossyTestEncoder struct{}

func (lossyTestEncoder) Capabilities() EncoderCapabilities {
	return EncoderCapabilities{
		TransferSyntaxUID: transfer.JPEGBaseline.UID,
		BitsAllocated:     []uint16{8}, PixelRepresentations: []uint16{0}, SamplesPerPixel: []uint16{1},
		PhotometricInterpretations: []string{"MONOCHROME2"}, Lossless: false, LossyMethod: "ISO_10918_1", SupportsMultiFrame: true,
	}
}

func (lossyTestEncoder) EncodeFrame(context.Context, []byte, Metadata) (EncodedFrame, error) {
	return EncodedFrame{Data: []byte{0xff, 0xd8, 0xff, 0xd9}}, nil
}

type metadataDeltaTestEncoder struct{ output EncodedFrame }

func (metadataDeltaTestEncoder) Capabilities() EncoderCapabilities {
	return EncoderCapabilities{
		TransferSyntaxUID: transfer.RLELossless.UID,
		BitsAllocated:     []uint16{8}, PixelRepresentations: []uint16{0}, SamplesPerPixel: []uint16{1},
		PhotometricInterpretations: []string{"MONOCHROME2"}, Lossless: true, SupportsMultiFrame: true,
	}
}

func (e metadataDeltaTestEncoder) EncodeFrame(context.Context, []byte, Metadata) (EncodedFrame, error) {
	return e.output, nil
}

func TestTranscodeDataSetValidatesEncoderMetadataDelta(t *testing.T) {
	planarTwo := uint16(2)
	tests := []struct {
		name    string
		output  EncodedFrame
		wantErr bool
		wantPI  string
	}{
		{name: "incompatible photometric", output: EncodedFrame{Data: []byte{1}, PhotometricInterpretation: "RGB"}, wantErr: true},
		{name: "invalid planar", output: EncodedFrame{Data: []byte{1}, PlanarConfiguration: &planarTwo}, wantErr: true},
		{name: "normalized photometric", output: EncodedFrame{Data: []byte{1}, PhotometricInterpretation: " monochrome2 "}, wantPI: "MONOCHROME2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registry := NewMemoryEncoderRegistry()
			if err := registry.RegisterEncoder(transfer.RLELossless.UID, metadataDeltaTestEncoder{output: test.output}); err != nil {
				t.Fatal(err)
			}
			got, _, err := TranscodeDataSet(context.Background(), transcodeNativeObject([]byte{1, 2, 3, 4}), transfer.ExplicitVRLittleEndian, transfer.RLELossless, TranscodeOptions{EncoderRegistry: registry})
			if test.wantErr {
				if !errors.Is(err, ErrEncoderOutputInvalid) {
					t.Fatalf("error = %v, want ErrEncoderOutputInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if pi, _ := got.GetString(tagPhotometricInterpretation); pi != test.wantPI {
				t.Fatalf("PhotometricInterpretation = %q, want %q", pi, test.wantPI)
			}
		})
	}
}

func TestValidateEncodedMetadataDeltaRequiresDeclaredTransforms(t *testing.T) {
	metadata := Metadata{
		SamplesPerPixel:            3,
		PhotometricInterpretation:  "RGB",
		PlanarConfigurationPresent: true,
		PlanarConfiguration:        0,
	}
	planarOne := uint16(1)
	encoded := EncodedFrame{Data: []byte{1}, PhotometricInterpretation: "YBR_FULL", PlanarConfiguration: &planarOne}
	if _, err := validateEncodedMetadataDelta(metadata, EncoderCapabilities{}, encoded); !errors.Is(err, ErrEncoderOutputInvalid) {
		t.Fatalf("undeclared transform error = %v", err)
	}
	capabilities := EncoderCapabilities{
		OutputPhotometricInterpretations: []string{"YBR_FULL"},
		OutputPlanarConfigurations:       []uint16{1},
	}
	got, err := validateEncodedMetadataDelta(metadata, capabilities, encoded)
	if err != nil {
		t.Fatal(err)
	}
	if got.PhotometricInterpretation != "YBR_FULL" || got.PlanarConfiguration == nil || *got.PlanarConfiguration != 1 {
		t.Fatalf("validated transform = %#v", got)
	}
}

func TestTranscodeDataSetLossyRequiresOptInAndUpdatesCumulativeMetadata(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder(transfer.JPEGBaseline.UID, lossyTestEncoder{}); err != nil {
		t.Fatal(err)
	}
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	source.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionRatio, VR: core.VRDS}, Value: core.StringValue{"2.5"}})
	source.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionMethod, VR: core.VRCS}, Value: core.StringValue{"PREVIOUS"}})
	beforeUID, _ := source.GetString(tagSOPInstanceUID)

	_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline, TranscodeOptions{EncoderRegistry: registry})
	if !errors.Is(err, ErrTranscodeLossyDisallowed) {
		t.Fatalf("lossy without opt-in error = %v", err)
	}
	got, report, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline, TranscodeOptions{EncoderRegistry: registry, AllowLossy: true})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Lossy {
		t.Fatalf("report = %#v", report)
	}
	if value, _ := got.GetString(tagLossyImageCompression); value != "01" {
		t.Fatalf("LossyImageCompression = %q", value)
	}
	if gotMethods := elementStrings(got, tagLossyImageCompressionMethod); len(gotMethods) != 2 || gotMethods[0] != "PREVIOUS" || gotMethods[1] != "ISO_10918_1" {
		t.Fatalf("methods = %v", gotMethods)
	}
	if gotRatios := elementStrings(got, tagLossyImageCompressionRatio); len(gotRatios) != 2 || gotRatios[0] != "2.5" {
		t.Fatalf("ratios = %v", gotRatios)
	}
	afterUID, _ := got.GetString(tagSOPInstanceUID)
	if beforeUID == afterUID || afterUID == "" {
		t.Fatalf("lossy SOP Instance UID = %q, before %q", afterUID, beforeUID)
	}
	if unchanged, _ := source.GetString(tagLossyImageCompression); unchanged != "" {
		t.Fatal("lossy transcode mutated source")
	}
}

func TestTranscodeDataSetRejectsUnpairedLossyHistory(t *testing.T) {
	registry := NewMemoryEncoderRegistry()
	if err := registry.RegisterEncoder(transfer.JPEGBaseline.UID, lossyTestEncoder{}); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		ratios  []string
		methods []string
	}{
		{name: "ratio only", ratios: []string{"2.5"}},
		{name: "method only", methods: []string{"ISO_10918_1"}},
		{name: "mismatched VM", ratios: []string{"2.5", "3.0"}, methods: []string{"ISO_10918_1"}},
		{name: "invalid DS", ratios: []string{"NOT_A_NUMBER"}, methods: []string{"ISO_10918_1"}},
		{name: "hexadecimal DS", ratios: []string{"0x1p2"}, methods: []string{"ISO_10918_1"}},
		{name: "underscore DS", ratios: []string{"1_000"}, methods: []string{"ISO_10918_1"}},
		{name: "non-positive ratio", ratios: []string{"-2"}, methods: []string{"ISO_10918_1"}},
		{name: "non-finite ratio", ratios: []string{"NaN"}, methods: []string{"ISO_10918_1"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := transcodeNativeObject([]byte{1, 2, 3, 4})
			if test.ratios != nil {
				source.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionRatio, VR: core.VRDS}, Value: core.StringValue(test.ratios)})
			}
			if test.methods != nil {
				source.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionMethod, VR: core.VRCS}, Value: core.StringValue(test.methods)})
			}
			_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.JPEGBaseline, TranscodeOptions{EncoderRegistry: registry, AllowLossy: true})
			if !errors.Is(err, ErrEncoderOutputInvalid) {
				t.Fatalf("error = %v, want ErrEncoderOutputInvalid", err)
			}
		})
	}
}

func TestTranscodeLimitsAreExactAndCancellationWins(t *testing.T) {
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	limits := DefaultTranscodeLimits()
	limits.MaxInputBytes = 3
	_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{Limits: limits})
	if !errors.Is(err, ErrTranscodeResourceLimit) {
		t.Fatalf("MaxInputBytes error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = TranscodeDataSet(ctx, source, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled error = %v", err)
	}
}

func TestTranscodeDirectAPIsBoundValueMultiplicityAndSequenceItems(t *testing.T) {
	t.Run("string VM", func(t *testing.T) {
		source := transcodeNativeObject([]byte{1, 2, 3, 4})
		source.Put(core.Element{
			Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1010), VR: core.VRLO},
			Value:  core.StringValue(make([]string, 2049)),
		})
		limits := DefaultTranscodeLimits()
		limits.MaxInputBytes = 1024
		_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{Limits: limits})
		if !errors.Is(err, ErrTranscodeResourceLimit) {
			t.Fatalf("large StringValue error = %v, want ErrTranscodeResourceLimit", err)
		}
	})

	t.Run("sequence items", func(t *testing.T) {
		source := transcodeNativeObject([]byte{1, 2, 3, 4})
		items := make([]core.DataSet, 200)
		source.Put(core.Element{
			Header: core.ElementHeader{Tag: core.NewTag(0x0011, 0x1020), VR: core.VRSQ},
			Value:  core.SequenceValue{Items: items},
		})
		limits := DefaultTranscodeLimits()
		limits.MaxElements = 64
		_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{Limits: limits})
		if !errors.Is(err, ErrTranscodeResourceLimit) {
			t.Fatalf("DataSet sequence item error = %v, want ErrTranscodeResourceLimit", err)
		}
		_, _, err = TranscodeCoreDataSet(context.Background(), source.ToDataSet(), transfer.ExplicitVRLittleEndian, transfer.ExplicitVRLittleEndian, TranscodeOptions{Limits: limits})
		if !errors.Is(err, ErrTranscodeResourceLimit) {
			t.Fatalf("core.DataSet sequence item error = %v, want ErrTranscodeResourceLimit", err)
		}
	})
}

func TestTranscodeCoreDataSetAdapterAndDuplicatePolicy(t *testing.T) {
	source := transcodeNativeObject([]byte{1, 2, 3, 4}).ToDataSet()
	got, report, err := TranscodeCoreDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.ImplicitVRLittleEndian, TranscodeOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if !report.PixelDataPreserved {
		t.Fatalf("report = %#v", report)
	}
	gotObject := object.FromDataSet(got, nil)
	if raw, ok := gotObject.GetRaw(core.TagPixelData); !ok || !bytes.Equal(raw, []byte{1, 2, 3, 4}) {
		t.Fatalf("Pixel Data = %v, %v", raw, ok)
	}
	duplicate := source
	duplicate.Elements = append(duplicate.Elements, duplicate.Elements[0])
	_, _, err = TranscodeCoreDataSet(context.Background(), duplicate, transfer.ExplicitVRLittleEndian, transfer.ImplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("duplicate DataSet error = %v, want ErrTranscodeUnsupported", err)
	}
}

func TestTranscodeFileBudgetsPreambleMetaAndDataSetTogether(t *testing.T) {
	file := &object.File{
		Preamble:       make([]byte, 128),
		Meta:           object.FromElements([]core.Element{core.NewRawElement(core.NewTag(0x0002, 0x0102), core.VROB, bytes.Repeat([]byte{1}, 32))}, nil),
		Dataset:        object.FromElements([]core.Element{core.NewRawElement(core.NewTag(0x0011, 0x1010), core.VROB, bytes.Repeat([]byte{2}, 32))}, nil),
		TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	limits := DefaultTranscodeLimits()
	limits.MaxInputBytes = 160
	limits.MaxOutputBytes = 160
	_, _, err := TranscodeFile(context.Background(), file, transfer.ExplicitVRLittleEndian, TranscodeOptions{Limits: limits})
	if !errors.Is(err, ErrTranscodeResourceLimit) {
		t.Fatalf("TranscodeFile() error = %v, want ErrTranscodeResourceLimit", err)
	}
	file.Preamble = make([]byte, 127)
	_, _, err = TranscodeFile(context.Background(), file, transfer.ExplicitVRLittleEndian, TranscodeOptions{})
	if !errors.Is(err, ErrTranscodeUnsupported) {
		t.Fatalf("invalid preamble error = %v, want ErrTranscodeUnsupported", err)
	}
}

func TestTranscodeMetadataErrorsDoNotEchoValues(t *testing.T) {
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	source.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x0008), VR: core.VRIS}, Value: core.StringValue{"PATIENT^NAME"}})
	_, _, err := TranscodeDataSet(context.Background(), source, transfer.ExplicitVRLittleEndian, transfer.RLELossless, TranscodeOptions{})
	if !errors.Is(err, ErrInvalidMetadata) {
		t.Fatalf("error = %v, want ErrInvalidMetadata", err)
	}
	if strings.Contains(err.Error(), "PATIENT^NAME") {
		t.Fatalf("error leaked metadata value: %v", err)
	}
}

func TestLosslessVerificationUsesOutputBudgetForEncodedOutput(t *testing.T) {
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	output := transcodeNativeObject([]byte{0, 0, 0, 0})
	output.Put(core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{bytes.Repeat([]byte{0xaa}, 100)}},
	})
	registry := NewMemoryRegistry()
	if err := registry.RegisterCodec(transfer.JPEGBaseline.UID, fixedNativeCodec{frame: []byte{1, 2, 3, 4}}); err != nil {
		t.Fatal(err)
	}
	limits := DefaultTranscodeLimits()
	limits.MaxInputBytes = 4
	limits.MaxOutputBytes = 100
	if err := verifyLosslessPixelData(context.Background(), source, transfer.ExplicitVRLittleEndian, output, transfer.JPEGBaseline, registry, limits); err != nil {
		t.Fatalf("verifyLosslessPixelData() error = %v", err)
	}
}

type fixedNativeCodec struct{ frame []byte }

func (codec fixedNativeCodec) Decode(PixelData, *object.Object) (Frames, error) {
	return Frames{Rows: 2, Columns: 2, Data: [][]byte{core.CloneBytes(codec.frame)}}, nil
}

func transcodeNativeObject(pixels []byte) *object.Object {
	return object.FromElements([]core.Element{
		{Header: core.ElementHeader{Tag: tagSOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{"1.2.3.4"}},
		uint16RawElement(tagRows, 2), uint16RawElement(tagColumns, 2), uint16RawElement(tagSamplesPerPixel, 1),
		photometricInterpretationElement("MONOCHROME2"), uint16RawElement(tagBitsAllocated, 8),
		uint16RawElement(tagBitsStored, 8), uint16RawElement(tagHighBit, 7), uint16RawElement(tagPixelRepresentation, 0),
		core.NewRawElement(core.TagPixelData, core.VROB, pixels),
	}, nil)
}
