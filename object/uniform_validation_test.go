package object

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

func TestObjectUniformValidationAndFileConsistency(t *testing.T) {
	sopClass := core.NewTag(0x0008, 0x0016)
	sopInstance := core.NewTag(0x0008, 0x0018)
	obj := FromElements([]core.Element{
		newStringElement(sopClass, core.VRUI, "1.2.3"),
		newStringElement(sopInstance, core.VRUI, "1..2"),
	}, std.Dictionary)
	result, err := obj.ValidateDataSet(context.Background(), validation.Options{Mode: validation.ModePreserve})
	if err != nil {
		t.Fatal(err)
	}
	if result.Report.Count(validation.CodeValueFormat) != 1 {
		t.Fatalf("object validation report = %#v", result.Report)
	}

	file := &File{
		Meta: FromElements([]core.Element{
			newStringElement(tagMediaStorageSOPClassUID, core.VRUI, "9.9"),
			newStringElement(tagMediaStorageSOPInstanceUID, core.VRUI, "9.9.1"),
			newStringElement(tagTransferSyntaxUID, core.VRUI, transfer.ImplicitVRLittleEndian.UID),
		}, std.Dictionary),
		Dataset: obj, TransferSyntax: transfer.ExplicitVRLittleEndian,
	}
	report, err := file.ValidateFile(context.Background(), validation.Options{Mode: validation.ModePreserve})
	if err != nil {
		t.Fatal(err)
	}
	if report.Count(validation.CodeFileMetaMismatch) != 3 {
		t.Fatalf("file validation report = %#v", report)
	}
}

func TestReadDataSetWithValidationAppliesLifecycleAndReturnsReport(t *testing.T) {
	patientID := core.NewTag(0x0010, 0x0020)
	var encoded bytes.Buffer
	if err := parser.NewWriter(&encoded, transfer.ExplicitVRLittleEndian).WriteElement(core.NewRawElement(patientID, core.VRLO, []byte("ORIGINAL"))); err != nil {
		t.Fatal(err)
	}
	chain, _ := validation.NewHookChain(validation.HookRegistration{
		Name: "replace", Points: []validation.HookPoint{validation.HookAfterElement},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			replacement := core.NewRawElement(patientID, core.VRLO, []byte("REPLACED"))
			return validation.HookDecision{Element: &replacement}, nil
		}),
	})
	obj, report, err := ReadDataSetWithValidation(context.Background(), bytes.NewReader(encoded.Bytes()), transfer.ExplicitVRLittleEndian, ReadFileOptions{Dictionary: std.Dictionary}, validation.Options{Hooks: chain})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := obj.GetString(patientID); got != "REPLACED" {
		t.Fatalf("read transformed value = %q", got)
	}
	if report.Count(validation.CodeHookTransformed) != 1 {
		t.Fatalf("read report = %#v", report)
	}
}

func TestWriteDataSetWithValidationStrictAndRoundTripDefaults(t *testing.T) {
	invalid := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1..2")),
	}, std.Dictionary)
	var rejected bytes.Buffer
	result, err := WriteDataSetWithValidation(context.Background(), &rejected, invalid, transfer.ExplicitVRLittleEndian, validation.Options{Mode: validation.ModeStrict})
	if err == nil || !errors.Is(err, validation.ErrValidationFailed) {
		t.Fatalf("strict write error = %v", err)
	}
	if rejected.Len() != 0 || result.BytesWritten != 0 || result.Complete {
		t.Fatalf("strict write result=%#v bytes=%d", result, rejected.Len())
	}

	valid := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	}, std.Dictionary)
	var ordinary, validated bytes.Buffer
	if err := WriteDataSet(&ordinary, valid, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	result, err = WriteDataSetWithValidation(context.Background(), &validated, valid, transfer.ExplicitVRLittleEndian, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.BytesWritten != int64(validated.Len()) || !bytes.Equal(ordinary.Bytes(), validated.Bytes()) {
		t.Fatalf("validated result=%#v\nordinary=% x\nvalidated=% x", result, ordinary.Bytes(), validated.Bytes())
	}
}

func TestValidatedReadWritePreservesDeferredPixelData(t *testing.T) {
	tests := []struct {
		name    string
		syntax  transfer.Syntax
		element core.Element
	}{
		{
			name:    "native",
			syntax:  transfer.ExplicitVRLittleEndian,
			element: dicomtest.NewOBElement(core.TagPixelData, []byte{0x01, 0x02, 0x03, 0x04}),
		},
		{
			name:   "encapsulated",
			syntax: transfer.JPEGBaseline,
			element: dicomtest.NewFragmentSequenceElement(
				core.TagPixelData,
				[]byte{0x00, 0x00, 0x00, 0x00},
				[]byte{0xFF, 0xD8, 0xFF, 0xD9},
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			encoded := dicomtest.EncodeElements(test.syntax, test.element)
			obj, _, err := ReadDataSetWithValidation(
				context.Background(),
				bytes.NewReader(encoded),
				test.syntax,
				ReadFileOptions{DeferPixelData: true},
				validation.Options{},
			)
			if err != nil {
				t.Fatal(err)
			}
			element, ok := obj.Get(core.TagPixelData)
			if !ok || element.Value != nil || obj.valueProvider == nil {
				t.Fatalf("deferred Pixel Data = %#v, provider=%T", element, obj.valueProvider)
			}

			var output bytes.Buffer
			result, err := WriteDataSetWithValidation(context.Background(), &output, obj, test.syntax, validation.Options{})
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || result.BytesWritten != int64(len(encoded)) || !bytes.Equal(output.Bytes(), encoded) {
				t.Fatalf("validated deferred round trip result=%#v\nwant=% X\ngot =% X", result, encoded, output.Bytes())
			}
		})
	}
}

func TestValidatedReadRetainsProviderForNestedDeferredWaveform(t *testing.T) {
	waveformSequenceTag := core.NewTag(0x5400, 0x0100)
	waveformDataTag := core.NewTag(0x5400, 0x1010)
	encoded := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		dicomtest.NewSequenceElement(waveformSequenceTag, core.DataSet{Elements: []core.Element{
			core.NewRawElement(waveformDataTag, core.VROW, []byte{0x01, 0x00, 0x02, 0x00}),
		}}),
	)
	obj, _, err := ReadDataSetWithValidation(
		context.Background(),
		bytes.NewReader(encoded),
		transfer.ExplicitVRLittleEndian,
		ReadFileOptions{DeferWaveformData: true},
		validation.Options{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if obj.deferredCount != 1 || obj.valueProvider == nil {
		t.Fatalf("nested deferred state = count %d provider %T, want 1/non-nil", obj.deferredCount, obj.valueProvider)
	}
}

func TestOpenDataSetWithValidationDoesNotTransferSourceOnStrictError(t *testing.T) {
	encoded := dicomtest.EncodeElements(
		transfer.ExplicitVRLittleEndian,
		core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1..2")),
		dicomtest.NewOBElement(core.TagPixelData, []byte{1, 2, 3, 4}),
	)
	path := filepath.Join(t.TempDir(), "invalid.dcm")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	obj, _, err := OpenDataSetWithValidation(
		context.Background(), path, transfer.ExplicitVRLittleEndian,
		ReadFileOptions{Dictionary: std.Dictionary, DeferPixelData: true},
		validation.Options{Mode: validation.ModeStrict},
	)
	if !errors.Is(err, validation.ErrValidationFailed) || obj == nil {
		t.Fatalf("OpenDataSetWithValidation() = %#v, %v", obj, err)
	}
	if obj.source != nil {
		t.Fatal("strictly rejected object retained ownership of the file source")
	}
	if err := obj.Close(); err != nil {
		t.Fatalf("Close() after rejected open error = %v", err)
	}
}

func TestWriteDataSetWithValidationPreservesDeflatedRoundTrip(t *testing.T) {
	obj := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	}, std.Dictionary)
	var ordinary, validated bytes.Buffer
	if err := WriteDataSet(&ordinary, obj, transfer.DeflatedExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	result, err := WriteDataSetWithValidation(context.Background(), &validated, obj, transfer.DeflatedExplicitVRLittleEndian, validation.Options{})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Complete || result.BytesWritten != int64(validated.Len()) || !bytes.Equal(ordinary.Bytes(), validated.Bytes()) {
		t.Fatalf("validated deflated result=%#v\nordinary=% X\nvalidated=% X", result, ordinary.Bytes(), validated.Bytes())
	}
}

func TestWriteDataSetWithValidationEmitsValidEmptyDeflatedStream(t *testing.T) {
	empty := FromElements(nil, std.Dictionary)
	filtered := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	}, std.Dictionary)
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "filter-all", Points: []validation.HookPoint{validation.HookPreSerialization},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			return validation.HookDecision{Filter: true}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var ordinary bytes.Buffer
	if err := WriteDataSet(&ordinary, empty, transfer.DeflatedExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		obj  *Object
		opts validation.Options
	}{
		{name: "empty", obj: empty},
		{name: "all filtered", obj: filtered, opts: validation.Options{Hooks: chain}},
	} {
		t.Run(test.name, func(t *testing.T) {
			var destination bytes.Buffer
			result, err := WriteDataSetWithValidation(context.Background(), &destination, test.obj, transfer.DeflatedExplicitVRLittleEndian, test.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !result.Complete || result.BytesWritten == 0 || !bytes.Equal(destination.Bytes(), ordinary.Bytes()) {
				t.Fatalf("result=%#v\nordinary=% X\nvalidated=% X", result, ordinary.Bytes(), destination.Bytes())
			}
		})
	}
}

func TestWriteDataSetWithValidationDeflatedRejectsWithoutCommittingBytes(t *testing.T) {
	invalid := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0008, 0x0018), core.VRUI, []byte("1..2")),
	}, std.Dictionary)
	for _, opts := range []validation.Options{
		{Mode: validation.ModeStrict},
		{MaxFindings: -1},
	} {
		var destination bytes.Buffer
		result, err := WriteDataSetWithValidation(context.Background(), &destination, invalid, transfer.DeflatedExplicitVRLittleEndian, opts)
		if err == nil {
			t.Fatalf("options %#v unexpectedly succeeded", opts)
		}
		if destination.Len() != 0 || result.BytesWritten != 0 || result.Complete {
			t.Fatalf("options %#v committed rejected deflate: result=%#v bytes=% X", opts, result, destination.Bytes())
		}
	}
}

func TestWriteDataSetWithValidationDeflatedPostHookFailureReportsCommittedStream(t *testing.T) {
	obj := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	}, std.Dictionary)
	chain, err := validation.NewHookChain(validation.HookRegistration{
		Name: "post-failure", Points: []validation.HookPoint{validation.HookPostWrite},
		Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
			return validation.HookDecision{}, errors.New("sensitive callback failure")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	result, err := WriteDataSetWithValidation(context.Background(), &destination, obj, transfer.DeflatedExplicitVRLittleEndian, validation.Options{Hooks: chain})
	if err == nil || !errors.Is(err, validation.ErrHookFailed) {
		t.Fatalf("post-write error = %v, want ErrHookFailed", err)
	}
	if !result.Complete || result.BytesWritten != int64(destination.Len()) || destination.Len() == 0 {
		t.Fatalf("deflated post-write result=%#v destination=%d", result, destination.Len())
	}
	if bytes.Contains([]byte(err.Error()), []byte("sensitive")) {
		t.Fatalf("post-write error leaked callback text: %v", err)
	}
}

func TestWriteDataSetWithValidationDeflatedFilteredPostFailureCommitsEmptyStream(t *testing.T) {
	obj := FromElements([]core.Element{
		core.NewRawElement(core.NewTag(0x0010, 0x0020), core.VRLO, []byte("IDENTIFIER")),
	}, std.Dictionary)
	chain, err := validation.NewHookChain(
		validation.HookRegistration{
			Name: "filter", Points: []validation.HookPoint{validation.HookPreSerialization},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{Filter: true}, nil
			}),
		},
		validation.HookRegistration{
			Name: "post-failure", Points: []validation.HookPoint{validation.HookPostWrite},
			Hook: validation.HookFunc(func(context.Context, validation.HookEvent) (validation.HookDecision, error) {
				return validation.HookDecision{}, errors.New("private post failure")
			}),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	var ordinary bytes.Buffer
	if err := WriteDataSet(&ordinary, FromElements(nil, std.Dictionary), transfer.DeflatedExplicitVRLittleEndian); err != nil {
		t.Fatal(err)
	}
	var destination bytes.Buffer
	result, err := WriteDataSetWithValidation(context.Background(), &destination, obj, transfer.DeflatedExplicitVRLittleEndian, validation.Options{Hooks: chain})
	if err == nil || !errors.Is(err, validation.ErrHookFailed) {
		t.Fatalf("error = %v, want ErrHookFailed", err)
	}
	if !result.Complete || result.BytesWritten == 0 || !bytes.Equal(destination.Bytes(), ordinary.Bytes()) {
		t.Fatalf("result=%#v\nordinary=% X\nvalidated=% X", result, ordinary.Bytes(), destination.Bytes())
	}
}
