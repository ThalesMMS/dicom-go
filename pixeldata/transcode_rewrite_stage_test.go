package pixeldata

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestRewriteAndDetachDataSetEstablishesOwnershipBoundary(t *testing.T) {
	sequenceTag := core.NewTag(0x0011, 0x1010)
	nestedTag := core.NewTag(0x0011, 0x1011)
	source := transcodeNativeObject([]byte{1, 2, 3, 4})
	source.Put(core.Element{
		Header: core.ElementHeader{Tag: sequenceTag, VR: core.VRSQ, Length: core.UndefinedLength, LengthSet: true},
		Value: core.SequenceValue{Items: []core.DataSet{{Elements: []core.Element{
			core.NewRawElement(nestedTag, core.VROB, []byte{9, 8}),
		}}}},
	})
	fragments := [][]byte{{5, 6, 7}}
	limits := DefaultTranscodeLimits()

	draft, encodedBytes, err := rewriteEncodedDataSet(context.Background(), encodedRewriteInput{
		source:    source,
		metadata:  Metadata{NumberOfFrames: 1},
		fragments: fragments,
		delta:     &encodedMetadataDelta{photometric: "MONOCHROME1"},
		limits:    limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	if encodedBytes != 3 {
		t.Fatalf("rewriteEncodedDataSet() encoded bytes = %d, want 3", encodedBytes)
	}
	if raw, ok := source.GetRaw(core.TagPixelData); !ok || !bytes.Equal(raw, []byte{1, 2, 3, 4}) {
		t.Fatalf("rewriteEncodedDataSet mutated source Pixel Data: %v, %v", raw, ok)
	}
	if got, _ := source.GetString(tagPhotometricInterpretation); got != "MONOCHROME2" {
		t.Fatalf("rewriteEncodedDataSet mutated source photometric interpretation: %q", got)
	}

	detached, err := detachDataSet(context.Background(), draft, limits)
	if err != nil {
		t.Fatal(err)
	}
	if detached.object == nil {
		t.Fatal("detachDataSet returned a nil object")
	}
	if got, _ := detached.object.GetString(tagPhotometricInterpretation); got != "MONOCHROME1" {
		t.Fatalf("detached photometric interpretation = %q, want MONOCHROME1", got)
	}

	sourceNested := stageNestedRaw(t, source, sequenceTag)
	detachedNested := stageNestedRaw(t, detached.object, sequenceTag)
	sourceNested[0] = 0xaa
	if detachedNested[0] != 9 {
		t.Fatal("detached data set aliases a nested source value")
	}
	detachedNested[1] = 0xbb
	if sourceNested[1] != 8 {
		t.Fatal("source data set aliases a nested detached value")
	}

	pixel, err := ExtractView(detached.object)
	if err != nil {
		t.Fatal(err)
	}
	fragments[0][0] = 0xcc
	if pixel.Sequence.Fragments[0][0] != 5 {
		t.Fatal("detached data set retained caller-owned fragment storage")
	}
	pixel.Sequence.Fragments[0][1] = 0xdd
	if fragments[0][1] != 6 {
		t.Fatal("detached fragment aliases caller-owned storage")
	}
}

func TestDetachDataSetCancellationReturnsNoPartialResult(t *testing.T) {
	limits := DefaultTranscodeLimits()
	draft, _, err := rewriteEncodedDataSet(context.Background(), encodedRewriteInput{
		source:    transcodeNativeObject([]byte{1, 2, 3, 4}),
		metadata:  Metadata{NumberOfFrames: 1},
		fragments: [][]byte{{5, 6, 7, 8}},
		limits:    limits,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := detachDataSet(ctx, draft, limits)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("detachDataSet() error = %v, want context.Canceled", err)
	}
	if got.object != nil {
		t.Fatal("detachDataSet returned a partial object after cancellation")
	}
}

func stageNestedRaw(t *testing.T, dataset interface {
	Get(core.Tag) (core.Element, bool)
}, sequenceTag core.Tag) []byte {
	t.Helper()
	element, ok := dataset.Get(sequenceTag)
	if !ok {
		t.Fatalf("sequence %s not found", sequenceTag)
	}
	sequence, ok := element.Value.(core.SequenceValue)
	if !ok || len(sequence.Items) != 1 || len(sequence.Items[0].Elements) != 1 {
		t.Fatalf("sequence value = %#v", element.Value)
	}
	raw, ok := sequence.Items[0].Elements[0].RawBytes()
	if !ok {
		t.Fatalf("nested value = %#v", sequence.Items[0].Elements[0].Value)
	}
	return raw
}
