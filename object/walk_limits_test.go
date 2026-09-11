package object

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
)

func TestWalkContextPreservesPreorder(t *testing.T) {
	obj := walkFixtureObject()
	var got []string
	err := obj.WalkPathContext(context.Background(), WalkOptions{}, func(path WalkPath, _ core.Element) error {
		got = append(got, path.String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"(0008,1111)",
		"(0008,1111)[0]/(0010,0010)",
		"(0008,1111)[0]/(0008,1115)",
		"(0008,1111)[0]/(0008,1115)[0]/(0008,1150)",
		"(0008,1111)[1]/(0010,0020)",
		"(0020,000D)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("paths = %#v, want %#v", got, want)
	}
}

func TestWalkContextEnforcesLimitsBeforeCallback(t *testing.T) {
	tests := []struct {
		name  string
		obj   *Object
		opts  WalkOptions
		limit WalkLimit
	}{
		{name: "elements", obj: walkWideSequenceFixtureObject(16), opts: WalkOptions{MaxElements: 3}, limit: WalkLimitElements},
		{name: "depth", obj: walkNestedLimitFixture(5), opts: WalkOptions{MaxDepth: 2}, limit: WalkLimitDepth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			visited := 0
			err := tt.obj.WalkContext(context.Background(), tt.opts, func([]core.Tag, core.Element) error {
				visited++
				return nil
			})
			if !errors.Is(err, ErrWalkResourceLimit) {
				t.Fatalf("error = %v, want ErrWalkResourceLimit", err)
			}
			var limitErr *WalkLimitError
			if !errors.As(err, &limitErr) || limitErr.Limit != tt.limit || limitErr.Visited != visited {
				t.Fatalf("limit error = %#v, callbacks = %d", limitErr, visited)
			}
		})
	}
}

func TestWalkContextCancellationReportsCompletedCallbacks(t *testing.T) {
	ctx := &walkCancelAfterContext{Context: context.Background(), remaining: 8}
	visited := 0
	err := walkWideSequenceFixtureObject(128).WalkContext(ctx, WalkOptions{}, func([]core.Tag, core.Element) error {
		visited++
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	var canceled *WalkCanceledError
	if !errors.As(err, &canceled) || canceled.Visited != visited {
		t.Fatalf("cancellation = %#v, callbacks = %d", canceled, visited)
	}
}

func TestWalkContextRejectsInvalidInputs(t *testing.T) {
	obj := walkFixtureObject()
	if err := obj.WalkContext(nil, WalkOptions{}, func([]core.Tag, core.Element) error { return nil }); !errors.Is(err, ErrInvalidWalkOptions) {
		t.Fatalf("nil context error = %v, want ErrInvalidWalkOptions", err)
	}
	if err := obj.WalkPathContext(context.Background(), WalkOptions{MaxDepth: -1}, func(WalkPath, core.Element) error { return nil }); !errors.Is(err, ErrInvalidWalkOptions) {
		t.Fatalf("negative limit error = %v, want ErrInvalidWalkOptions", err)
	}
}

func FuzzWalkContextBounds(f *testing.F) {
	f.Add(uint8(1), uint8(1))
	f.Add(uint8(80), uint8(64))
	f.Fuzz(func(t *testing.T, depthByte, maxByte uint8) {
		depth := int(depthByte % 96)
		maximum := int(maxByte%64) + 1
		obj := walkNestedLimitFixture(depth)
		err := obj.WalkPathContext(context.Background(), WalkOptions{MaxDepth: maximum, MaxElements: maximum}, func(WalkPath, core.Element) error {
			return nil
		})
		if err != nil && !errors.Is(err, ErrWalkResourceLimit) {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func BenchmarkObjectWalkFull8192Elements(b *testing.B) {
	obj := walkWideSequenceFixtureObject(8192)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := obj.Walk(func([]core.Tag, core.Element) error { return nil }); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkObjectWalkBounded128Of8192Elements(b *testing.B) {
	obj := walkWideSequenceFixtureObject(8192)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := obj.WalkContext(context.Background(), WalkOptions{MaxElements: 128}, func([]core.Tag, core.Element) error { return nil })
		if !errors.Is(err, ErrWalkResourceLimit) {
			b.Fatalf("WalkContext() error = %v, want ErrWalkResourceLimit", err)
		}
	}
}

type walkCancelAfterContext struct {
	context.Context
	remaining int
}

func (ctx *walkCancelAfterContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *walkCancelAfterContext) Done() <-chan struct{}       { return nil }
func (ctx *walkCancelAfterContext) Err() error {
	ctx.remaining--
	if ctx.remaining <= 0 {
		return context.Canceled
	}
	return nil
}

func walkNestedLimitFixture(depth int) *Object {
	elements := []core.Element{dicomtest.NewStringElement(walkPatientIDTag, core.VRLO, "leaf")}
	for level := 0; level < depth; level++ {
		elements = []core.Element{dicomtest.NewSequenceElement(walkOuterSeqTag, core.DataSet{Elements: elements})}
	}
	return FromElements(elements, std.Dictionary)
}
