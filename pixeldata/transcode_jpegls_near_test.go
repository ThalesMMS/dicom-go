package pixeldata_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpegls"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func nearTranscodeOptions(t testing.TB) pixeldata.TranscodeOptions {
	t.Helper()
	enc := pixeldata.NewMemoryEncoderRegistry()
	dec := pixeldata.NewMemoryRegistry()
	if err := jpegls.RegisterNearLosslessEncoder(enc, jpegls.EncoderOptions{Near: 1, AllowLossy: true}); err != nil {
		t.Fatal(err)
	}
	if err := jpegls.RegisterNearLossless(dec); err != nil {
		t.Fatal(err)
	}
	return pixeldata.TranscodeOptions{EncoderRegistry: enc, DecoderRegistry: dec, AllowLossy: true}
}

func nearSourceBytes(t testing.TB, source *object.Object) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := object.WriteDataSet(&b, source, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestTranscodeNearLosslessIdentityAndHistory(t *testing.T) {
	for _, tc := range codecfixture.JPEGLSNearEncoderCases() {
		if tc.Near != 1 || (tc.Metadata.BitsStored != 8 && tc.Metadata.BitsStored != 16) {
			continue
		}
		t.Run(tc.Name, func(t *testing.T) {
			source := tc.Object()
			defer source.Close()
			for _, e := range []core.Element{
				{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x2110), VR: core.VRCS}, Value: core.StringValue{"01"}},
				{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x2114), VR: core.VRCS}, Value: core.StringValue{"ISO_10918_1"}},
				{Header: core.ElementHeader{Tag: core.NewTag(0x0028, 0x2112), VR: core.VRDS}, Value: core.StringValue{"2.5"}},
				{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0008), VR: core.VRCS}, Value: core.StringValue{"ORIGINAL", "PRIMARY", "SYNTHETIC"}},
			} {
				source.Put(e)
			}
			before := nearSourceBytes(t, source)
			oldUID, _ := source.GetUID(tags.SOPInstanceUID)
			opts := nearTranscodeOptions(t)
			opts.AllowLossy = false
			if got, _, err := pixeldata.TranscodeDataSet(context.Background(), source, tc.Syntax, transfer.JPEGLSNearLossless, opts); !errors.Is(err, pixeldata.ErrTranscodeLossyDisallowed) || got != nil {
				t.Fatal("lossy transcoder authorization bypassed")
			}
			opts.AllowLossy = true
			file, report, err := pixeldata.TranscodeFile(context.Background(), &object.File{Dataset: source, TransferSyntax: tc.Syntax}, transfer.JPEGLSNearLossless, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if !report.Lossy || report.Frames != 2 || file.TransferSyntax.UID != transfer.JPEGLSNearLossless.UID {
				t.Fatal("wrong transfer report")
			}
			uid, _ := file.Dataset.GetUID(tags.SOPInstanceUID)
			if !core.IsValidUID(uid) || uid == oldUID {
				t.Fatal("derived identity")
			}
			if value, _ := file.Dataset.GetString(core.NewTag(0x0028, 0x2110)); value != "01" {
				t.Fatal("lossy history cleared")
			}
			if methods, _ := file.Dataset.GetStrings(core.NewTag(0x0028, 0x2114)); !slices.Equal(methods, []string{"ISO_10918_1", "ISO_14495_1"}) {
				t.Fatal("method history lost")
			}
			if ratios, _ := file.Dataset.GetStrings(core.NewTag(0x0028, 0x2112)); len(ratios) != 2 || ratios[0] != "2.5" {
				t.Fatal("ratio history lost")
			}
			if types, _ := file.Dataset.GetStrings(core.NewTag(0x0008, 0x0008)); !slices.Equal(types, []string{"DERIVED", "PRIMARY", "SYNTHETIC"}) {
				t.Fatal("image derivation lost")
			}
			var part10 bytes.Buffer
			if err := object.WriteFile(&part10, file); err != nil {
				t.Fatal(err)
			}
			reopened, err := object.ReadFile(bytes.NewReader(part10.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			defer reopened.Close()
			if reopened.TransferSyntax.UID != transfer.JPEGLSNearLossless.UID {
				t.Fatal("file meta syntax")
			}
			decompressed, _, err := pixeldata.TranscodeDataSet(context.Background(), reopened.Dataset, reopened.TransferSyntax, tc.Syntax, opts)
			if err != nil {
				t.Fatal(err)
			}
			defer decompressed.Close()
			if methods, _ := decompressed.GetStrings(core.NewTag(0x0028, 0x2114)); !slices.Equal(methods, []string{"ISO_10918_1", "ISO_14495_1"}) {
				t.Fatal("decode cleared prior losses")
			}
			if !bytes.Equal(before, nearSourceBytes(t, source)) {
				t.Fatal("origin mutated")
			}
		})
	}
}

type failSecondNearEncoder struct {
	pixeldata.FrameEncoder
	calls int
	cause error
}

func (e *failSecondNearEncoder) EncodeFrame(ctx context.Context, data []byte, m pixeldata.Metadata) (pixeldata.EncodedFrame, error) {
	e.calls++
	if e.calls == 2 {
		return pixeldata.EncodedFrame{}, e.cause
	}
	return e.FrameEncoder.EncodeFrame(ctx, data, m)
}

func TestTranscodeNearLosslessFailureDoesNotPublish(t *testing.T) {
	for _, cause := range []error{context.Canceled, errors.New("synthetic second frame codec failure")} {
		t.Run(cause.Error(), func(t *testing.T) {
			dir := t.TempDir()
			sourcePath := filepath.Join(dir, "original.dcm")
			dest := filepath.Join(dir, "derived.dcm")
			data, err := codecfixture.NativeMultiFrame().Part10Bytes()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(sourcePath, data, 0o600); err != nil {
				t.Fatal(err)
			}
			opts := nearTranscodeOptions(t)
			encoder, _ := jpegls.NewEncoderWithOptions(jpegls.EncoderOptions{Near: 1, AllowLossy: true})
			failing := &failSecondNearEncoder{FrameEncoder: encoder, cause: cause}
			opts.EncoderRegistry = pixeldata.NewMemoryEncoderRegistry()
			if err := opts.EncoderRegistry.RegisterEncoder(transfer.JPEGLSNearLossless.UID, failing); err != nil {
				t.Fatal(err)
			}
			if _, err := pixeldata.TranscodePath(context.Background(), sourcePath, dest, transfer.JPEGLSNearLossless, opts); !errors.Is(err, cause) {
				t.Fatalf("late failure lost: %v", err)
			}
			if failing.calls != 2 {
				t.Fatal("did not exercise later frame")
			}
			after, err := os.ReadFile(sourcePath)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("original changed")
			}
			entries, err := os.ReadDir(dir)
			if err != nil || len(entries) != 1 || entries[0].Name() != "original.dcm" {
				t.Fatal("partial destination or temporaries retained")
			}
			opts = nearTranscodeOptions(t)
			if _, err := pixeldata.TranscodePath(context.Background(), sourcePath, dest, transfer.JPEGLSNearLossless, opts); err != nil {
				t.Fatal(err)
			}
			file, err := object.OpenFile(dest)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			if file.TransferSyntax.UID != transfer.JPEGLSNearLossless.UID {
				t.Fatal("published wrong transfer syntax")
			}
		})
	}
}
