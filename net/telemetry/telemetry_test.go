package telemetry

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestDispatcherDoesNotBlockAndAccountsForDrops(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	dispatcher := NewDispatcher(SinkFunc(func(context.Context, Event) {
		select {
		case <-started:
		default:
			close(started)
		}
		<-release
	}), 1)
	dispatcher.Emit(Event{Kind: PDUObserved})
	<-started

	startedAt := time.Now()
	dispatcher.Emit(Event{Kind: PDUObserved})
	dispatcher.Emit(Event{Kind: PDUObserved})
	if elapsed := time.Since(startedAt); elapsed > 25*time.Millisecond {
		t.Fatalf("Emit() blocked for %v", elapsed)
	}
	if got := dispatcher.Stats().DroppedEvents; got != 1 {
		t.Fatalf("DroppedEvents = %d, want 1", got)
	}
	close(release)
	dispatcher.Close()
	select {
	case <-dispatcher.Done():
	case <-time.After(time.Second):
		t.Fatal("dispatcher did not drain")
	}
}

func TestDispatcherRecoversSinkPanicAndContinues(t *testing.T) {
	delivered := make(chan struct{}, 1)
	calls := 0
	dispatcher := NewDispatcher(SinkFunc(func(context.Context, Event) {
		calls++
		if calls == 1 {
			panic("sink failure")
		}
		delivered <- struct{}{}
	}), 2)
	dispatcher.Emit(Event{Kind: PDUObserved})
	dispatcher.Emit(Event{Kind: PDUObserved})
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("dispatcher stopped after sink panic")
	}
	dispatcher.Close()
	<-dispatcher.Done()
	if got := dispatcher.Stats().SinkPanics; got != 1 {
		t.Fatalf("SinkPanics = %d, want 1", got)
	}
}

func TestEndpointPolicyRequiresKeyAndHashesWithoutPlaintext(t *testing.T) {
	if err := (EndpointPolicy{AETitles: EndpointHMACSHA256}).Validate(); err == nil {
		t.Fatal("EndpointPolicy.Validate() accepted HMAC mode without key")
	}
	policy := EndpointPolicy{
		AETitles:  EndpointHMACSHA256,
		Addresses: EndpointHMACSHA256,
		HMACKey:   []byte("test-only-secret"),
	}
	first := policy.AETitle("HOROS")
	second := policy.AETitle("HOROS")
	if first == "" || first != second || strings.Contains(first, "HOROS") {
		t.Fatalf("hashed AE title = %q", first)
	}
	if got := policy.Address("127.0.0.1:4007"); got == "" || strings.Contains(got, "127.0.0.1") {
		t.Fatalf("hashed address = %q", got)
	}
	if got := (EndpointPolicy{}).AETitle("HOROS"); got != "" {
		t.Fatalf("default AE title = %q, want omitted", got)
	}
	if got := (EndpointPolicy{AETitles: EndpointPlaintext}).AETitle("HOROS"); got != "HOROS" {
		t.Fatalf("explicit plaintext AE title = %q", got)
	}
}
