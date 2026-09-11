package ups

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
)

func TestSubscriptionFilterUsesCFindMatchingSemantics(t *testing.T) {
	t.Parallel()
	filter, err := compileSubscriptionFilter(map[string][]string{
		TagWorklistLabel.String():                       {"CARD*", "Radio?ogy"},
		TagScheduledProcedureStepPriority.String():      {"high"},
		TagScheduledProcedureStepStartDateTime.String(): {"20260808-20260809"},
	})
	if err != nil {
		t.Fatal(err)
	}
	identifier, err := BuildQueryIdentifier(Query{Keys: map[core.Tag]QueryKey{
		TagWorklistLabel:                       Match("CARD*", "Radio?ogy"),
		TagScheduledProcedureStepPriority:      Match("high"),
		TagScheduledProcedureStepStartDateTime: Match("20260808-20260809"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseQueryIdentifier(identifier)
	if err != nil {
		t.Fatal(err)
	}
	step := Step{Attributes: core.DataSet{Elements: []core.Element{
		StringElement(TagWorklistLabel, core.VRLO, "Radiology"),
		StringElement(TagScheduledProcedureStepPriority, core.VRCS, "HIGH"),
		StringElement(TagScheduledProcedureStepStartDateTime, core.VRDT, "20260808120000"),
	}}}
	if got, want := subscriptionFilterMatches(filter, step), queryMatchesStep(parsed, step); !got || got != want {
		t.Fatal("bounded subscription filter did not match the equivalent C-FIND candidate")
	}
	missing := step
	missing.Attributes = core.DataSet{Elements: append([]core.Element(nil), step.Attributes.Elements[1:]...)}
	if got, want := subscriptionFilterMatches(filter, missing), queryMatchesStep(parsed, missing); got || got != want {
		t.Fatal("filter matched a candidate missing an ANDed key")
	}
	outsideRange := step
	outsideRange.Attributes = core.DataSet{Elements: append([]core.Element(nil), step.Attributes.Elements...)}
	outsideRange.Attributes.Elements[2] = StringElement(TagScheduledProcedureStepStartDateTime, core.VRDT, "20260810120000")
	if got, want := subscriptionFilterMatches(filter, outsideRange), queryMatchesStep(parsed, outsideRange); got || got != want {
		t.Fatal("filter matched a candidate outside the temporal range")
	}
}

func TestFilteredSubscriptionScanCancellationRollsBack(t *testing.T) {
	t.Parallel()
	service, store := testServiceAndStore(t, ServiceOptions{CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	first := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.837.61")
	second := createTestStep(t, service, "1.2.826.0.1.3680043.10.543.837.62")
	ctx := &cancelAfterChecksContext{allowedChecks: 2}
	_, err := service.Subscribe(ctx, SubscribeRequest{
		SOPInstanceUID: FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true,
		MatchingKeys: map[string][]string{TagWorklistLabel.String(): {"RADIOLOGY"}},
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Subscribe error = %v, want cancellation", err)
	}
	for _, uid := range []string{FilteredGlobalSubscriptionSOPInstanceUID, first.SOPInstanceUID, second.SOPInstanceUID} {
		if subscription, getErr := service.Subscription(context.Background(), uid, "WATCHER"); !errors.Is(getErr, ErrNotFound) {
			t.Fatalf("subscription after canceled scan = %#v, %v", subscription, getErr)
		}
	}
	deliveries, listErr := store.ListDeliveries(context.Background(), DeliveryQuery{ReceivingAETitle: "WATCHER", Limit: 16})
	if listErr != nil || len(deliveries) != 0 {
		t.Fatalf("deliveries after canceled scan = %#v, %v", deliveries, listErr)
	}
	for _, uid := range []string{first.SOPInstanceUID, second.SOPInstanceUID} {
		events, eventsErr := store.ListEvents(context.Background(), EventQuery{SOPInstanceUID: uid, Limit: 16})
		if eventsErr != nil || len(events) != 1 {
			t.Fatalf("events for %s after canceled scan = %#v, %v", uid, events, eventsErr)
		}
	}
}

func TestFilteredSubscriptionScanLimitReturns0213AndRollsBack(t *testing.T) {
	t.Parallel()
	store, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	creator, err := NewService(store, ServiceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first := createTestStep(t, creator, "1.2.826.0.1.3680043.10.543.837.71")
	second := createTestStep(t, creator, "1.2.826.0.1.3680043.10.543.837.72")
	service, err := NewService(store, ServiceOptions{
		Limits: Limits{MaxSubscriptionFilterScanned: 1},
		CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
			return CallbackTarget{Address: "127.0.0.1:11112"}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Subscribe(context.Background(), SubscribeRequest{
		SOPInstanceUID: FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true,
		MatchingKeys: map[string][]string{TagWorklistLabel.String(): {"RADIOLOGY"}},
	})
	if !IsStatus(err, StatusResourceLimitation) {
		t.Fatalf("Subscribe error = %v, want 0213", err)
	}
	for _, uid := range []string{FilteredGlobalSubscriptionSOPInstanceUID, first.SOPInstanceUID, second.SOPInstanceUID} {
		if subscription, getErr := service.Subscription(context.Background(), uid, "WATCHER"); !errors.Is(getErr, ErrNotFound) {
			t.Fatalf("subscription after limited scan = %#v, %v", subscription, getErr)
		}
	}
}

func TestStoreWithoutFilteredCapabilityReturnsC307(t *testing.T) {
	t.Parallel()
	memory, err := NewMemoryStore(MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolverCalled := false
	service, err := NewService(&unfilteredOnlyStore{Store: memory}, ServiceOptions{CallbackResolver: CallbackResolverFunc(func(context.Context, CallbackRequest) (CallbackTarget, error) {
		resolverCalled = true
		return CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	_, err = service.Subscribe(context.Background(), SubscribeRequest{
		SOPInstanceUID: FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		MatchingKeys: map[string][]string{TagWorklistLabel.String(): {"RADIOLOGY"}},
	})
	if !IsStatus(err, StatusUPSNotFound) {
		t.Fatalf("Subscribe error = %v, want C307", err)
	}
	if resolverCalled {
		t.Fatal("unsupported store reached callback resolver")
	}
}

type unfilteredOnlyStore struct{ Store }

type cancelAfterChecksContext struct {
	allowedChecks int
	checks        int
}

func (ctx *cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }
func (ctx *cancelAfterChecksContext) Done() <-chan struct{}       { return nil }
func (ctx *cancelAfterChecksContext) Value(any) any               { return nil }
func (ctx *cancelAfterChecksContext) Err() error {
	ctx.checks++
	if ctx.checks > ctx.allowedChecks {
		return context.Canceled
	}
	return nil
}
