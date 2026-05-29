package audit

import (
	"context"
	"testing"
	"time"
)

func TestEmitFillsTimeAndContext(t *testing.T) {
	var got Event
	var gotCtx context.Context

	Emit(nil, SinkFunc(func(ctx context.Context, event Event) {
		gotCtx = ctx
		got = event
	}), Event{Kind: OperationSucceeded, Service: "C-STORE"})

	if gotCtx == nil {
		t.Fatal("Emit() context = nil, want context.Background fallback")
	}
	if got.Kind != OperationSucceeded || got.Service != "C-STORE" {
		t.Fatalf("Emit() event = %+v", got)
	}
	if got.Time.IsZero() {
		t.Fatal("Emit() did not fill zero event time")
	}
	if time.Since(got.Time) > time.Minute {
		t.Fatalf("Emit() filled unexpected time %v", got.Time)
	}
}

func TestEmitKeepsExplicitTimeAndIgnoresNilSink(t *testing.T) {
	Emit(context.Background(), nil, Event{})

	wantTime := time.Date(2026, 6, 22, 12, 0, 0, 0, time.UTC)
	var got Event
	Emit(context.Background(), SinkFunc(func(_ context.Context, event Event) {
		got = event
	}), Event{Time: wantTime, Kind: OperationFailed})

	if !got.Time.Equal(wantTime) {
		t.Fatalf("Emit() time = %v, want %v", got.Time, wantTime)
	}
	if got.Kind != OperationFailed {
		t.Fatalf("Emit() kind = %v, want %v", got.Kind, OperationFailed)
	}
}

func TestSinkFuncNilIsNoop(t *testing.T) {
	var sink SinkFunc
	sink.EmitAuditEvent(context.Background(), Event{Kind: OperationSucceeded})
}
