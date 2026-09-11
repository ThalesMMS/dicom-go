package pixeldata

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestExtractAllWithOptionsEnforcesLimits(t *testing.T) {
	many := extractAllManyMatchesFixture(4, 8)
	deep := extractAllNestedFixture(4)
	fragmented := object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{Fragments: [][]byte{{}, {}, {}, {}}},
	}}, nil)
	tests := []struct {
		name  string
		obj   *object.Object
		opts  ExtractAllOptions
		limit ExtractAllLimit
	}{
		{name: "matches", obj: many, opts: ExtractAllOptions{MaxMatches: 2}, limit: ExtractAllLimitMatches},
		{name: "bytes", obj: many, opts: ExtractAllOptions{MaxBytes: 15}, limit: ExtractAllLimitBytes},
		{name: "depth", obj: deep, opts: ExtractAllOptions{MaxSequenceDepth: 2}, limit: ExtractAllLimitDepth},
		{name: "fragments", obj: fragmented, opts: ExtractAllOptions{MaxFragments: 3}, limit: ExtractAllLimitFragments},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ExtractAllWithOptions(context.Background(), tt.obj, tt.opts)
			if got != nil {
				t.Fatalf("partial result length = %d, want nil", len(got))
			}
			if !errors.Is(err, ErrExtractAllResourceLimit) {
				t.Fatalf("error = %v, want ErrExtractAllResourceLimit", err)
			}
			var limitErr *ExtractAllLimitError
			if !errors.As(err, &limitErr) || limitErr.Limit != tt.limit {
				t.Fatalf("limit error = %#v, want %q", limitErr, tt.limit)
			}
		})
	}
}

func TestExtractAllWithOptionsCancellation(t *testing.T) {
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := ExtractAllWithOptions(canceled, extractAllManyMatchesFixture(1, 1), ExtractAllOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-canceled error = %v, want context.Canceled", err)
	}

	ctx := &extractAllCancelAfterContext{Context: context.Background(), remaining: 20}
	if _, err := ExtractAllWithOptions(ctx, extractAllManyMatchesFixture(1024, 1), ExtractAllOptions{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("mid-traversal error = %v, want context.Canceled", err)
	}
	if ctx.checks > 21 {
		t.Fatalf("context checked %d times after cancellation, want prompt stop", ctx.checks)
	}
}

func TestExtractAllWithOptionsRejectsInvalidInputs(t *testing.T) {
	if _, err := ExtractAllWithOptions(nil, nil, ExtractAllOptions{}); !errors.Is(err, ErrInvalidExtractAllOptions) {
		t.Fatalf("nil context error = %v, want ErrInvalidExtractAllOptions", err)
	}
	if _, err := ExtractAllWithOptions(context.Background(), nil, ExtractAllOptions{MaxBytes: -1}); !errors.Is(err, ErrInvalidExtractAllOptions) {
		t.Fatalf("negative limit error = %v, want ErrInvalidExtractAllOptions", err)
	}
}

func FuzzExtractAllWithOptionsBounds(f *testing.F) {
	f.Add(uint8(1), uint8(1), uint8(1))
	f.Add(uint8(32), uint8(16), uint8(8))
	f.Fuzz(func(t *testing.T, matchesByte, payloadByte, depthByte uint8) {
		matches := int(matchesByte%64) + 1
		payloadBytes := int(payloadByte%64) + 1
		depth := int(depthByte % 80)
		obj := extractAllManyMatchesFixture(matches, payloadBytes)
		if depth > 0 {
			obj = extractAllNestedFixture(depth)
		}
		_, err := ExtractAllWithOptions(context.Background(), obj, ExtractAllOptions{
			MaxMatches:       16,
			MaxBytes:         512,
			MaxSequenceDepth: 32,
			MaxFragments:     32,
		})
		if err != nil && !errors.Is(err, ErrExtractAllResourceLimit) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

type extractAllCancelAfterContext struct {
	context.Context
	remaining int
	checks    int
}

func (ctx *extractAllCancelAfterContext) Deadline() (time.Time, bool) {
	return time.Time{}, false
}

func (ctx *extractAllCancelAfterContext) Done() <-chan struct{} {
	return nil
}

func (ctx *extractAllCancelAfterContext) Err() error {
	ctx.checks++
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func extractAllNestedFixture(depth int) *object.Object {
	elements := []core.Element{dicomtest.NewOBElement(core.TagPixelData, []byte{1})}
	sequenceTag := core.NewTag(0x0008, 0x1111)
	for level := 0; level < depth; level++ {
		elements = []core.Element{dicomtest.NewSequenceElement(sequenceTag, core.DataSet{Elements: elements})}
	}
	return object.FromElements(elements, nil)
}
