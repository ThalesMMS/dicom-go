package deid

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const callbackFailureSecret = "SECRET CALLBACK DETAIL"

var errCallbackFailureSecret = errors.New(callbackFailureSecret)

type callbackFailureMode string

const (
	callbackPanic         callbackFailureMode = "panic"
	callbackNilPanic      callbackFailureMode = "nil panic"
	callbackReturnedError callbackFailureMode = "returned error"
	callbackCancellation  callbackFailureMode = "cancellation"
	callbackCanceledPanic callbackFailureMode = "canceled panic"
)

func failProfileCallback(mode callbackFailureMode, cancel context.CancelFunc) error {
	switch mode {
	case callbackPanic:
		panic(callbackFailureSecret)
	case callbackNilPanic:
		panic(nil)
	case callbackReturnedError:
		return errCallbackFailureSecret
	case callbackCancellation:
		cancel()
		return errCallbackFailureSecret
	case callbackCanceledPanic:
		cancel()
		panic(callbackFailureSecret)
	default:
		return nil
	}
}

func TestProfileCallbackGuardFailureMatrix(t *testing.T) {
	element := core.NewRawElement(core.NewTag(0x0010, 0x0010), core.VRPN, []byte("SAFE"))
	inputObject := object.FromElements(nil, nil)
	hooks := []struct {
		name   string
		invoke func(context.Context, context.CancelFunc, callbackFailureMode, *int) (any, error)
		zero   any
	}{
		{
			name: "requirement resolver",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				return callRequirementResolver(ctx, func(context.Context, AttributeContext) (AttributeRequirement, error) {
					(*calls)++
					return AttributeType1, failProfileCallback(mode, cancel)
				}, AttributeContext{})
			},
			zero: AttributeRequirement(0),
		},
		{
			name: "element cleaner",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				return callElementCleaner(ctx, func(context.Context, CleanContext) (core.Element, error) {
					(*calls)++
					return element, failProfileCallback(mode, cancel)
				}, CleanContext{})
			},
			zero: core.Element{},
		},
		{
			name: "dummy provider",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				return callDummyProvider(ctx, func(context.Context, DummyContext) (core.Element, error) {
					(*calls)++
					return element, failProfileCallback(mode, cancel)
				}, DummyContext{})
			},
			zero: core.Element{},
		},
		{
			name: "date shift policy",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				return callDateShiftPolicy(ctx, func(context.Context, DateShiftContext) (int, error) {
					(*calls)++
					return 7, failProfileCallback(mode, cancel)
				}, inputObject)
			},
			zero: 0,
		},
		{
			name: "pixel cleaner",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				return callPixelCleaner(ctx, func(context.Context, *object.Object) ([]PixelRegion, error) {
					(*calls)++
					return []PixelRegion{{X: 1, Y: 2, Width: 3, Height: 4}}, failProfileCallback(mode, cancel)
				}, inputObject)
			},
			zero: []PixelRegion(nil),
		},
		{
			name: "visual cleaner",
			invoke: func(ctx context.Context, cancel context.CancelFunc, mode callbackFailureMode, calls *int) (any, error) {
				err := callVisualCleaner(ctx, func(context.Context, *object.Object) error {
					(*calls)++
					return failProfileCallback(mode, cancel)
				}, inputObject)
				return struct{}{}, err
			},
			zero: struct{}{},
		},
	}
	modes := []callbackFailureMode{callbackPanic, callbackNilPanic, callbackReturnedError, callbackCancellation, callbackCanceledPanic}
	for _, hook := range hooks {
		for _, mode := range modes {
			t.Run(hook.name+"/"+string(mode), func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				calls := 0
				output, err := hook.invoke(ctx, cancel, mode, &calls)
				wantErr := ErrProfileCallback
				if mode == callbackCancellation || mode == callbackCanceledPanic {
					wantErr = context.Canceled
				}
				if calls != 1 {
					t.Fatalf("callback calls = %d, want 1", calls)
				}
				if !errors.Is(err, wantErr) {
					t.Fatalf("error = %v, want %v", err, wantErr)
				}
				if errors.Is(err, errCallbackFailureSecret) {
					t.Fatalf("error wraps callback detail: %v", err)
				}
				if strings.Contains(err.Error(), callbackFailureSecret) {
					t.Fatalf("error leaked callback detail: %v", err)
				}
				if !reflect.DeepEqual(output, hook.zero) {
					t.Fatalf("output = %#v, want cleared %#v", output, hook.zero)
				}
			})
		}
	}
}

func TestPixelCleanerRegionsAreDefensivelyCopied(t *testing.T) {
	callbackRegions := []PixelRegion{{X: 1, Y: 2, Width: 3, Height: 4}}
	regions, err := callPixelCleaner(context.Background(), func(context.Context, *object.Object) ([]PixelRegion, error) {
		return callbackRegions, nil
	}, object.FromElements(nil, nil))
	if err != nil {
		t.Fatal(err)
	}
	callbackRegions[0].X = 99
	if regions[0].X != 1 {
		t.Fatalf("callback mutation changed guarded regions: %#v", regions)
	}
	regions[0].Y = 99
	if callbackRegions[0].Y != 2 {
		t.Fatalf("caller mutation changed callback-owned regions: %#v", callbackRegions)
	}
}
