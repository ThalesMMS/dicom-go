package ups_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestFilteredGlobalSubscriptionFiltersExistingAndFutureUPS(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, service := filteredService(t, ups.MemoryStoreOptions{})
	matchingExisting := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.1", "RADIOLOGY")
	nonMatchingExisting := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.2", "CARDIOLOGY")
	filter := map[string][]string{
		ups.TagWorklistLabel.HexString():   {"RAD*"},
		ups.TagProcedureStepState.String(): {"scheduled"},
	}
	result, err := service.Subscribe(ctx, ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		DeletionLock: true, MatchingKeys: filter,
	})
	if err != nil || result.Status != ups.StatusSuccess || result.Subscription.Filter == nil {
		t.Fatalf("filtered subscribe = %#v, %v", result, err)
	}
	filter[ups.TagWorklistLabel.HexString()][0] = "CARD*"
	delete(filter, ups.TagProcedureStepState.String())
	for index := range result.Subscription.Filter.Keys {
		if result.Subscription.Filter.Keys[index].Tag == ups.TagWorklistLabel {
			result.Subscription.Filter.Keys[index].Values[0] = "CARD*"
		}
	}

	assertSubscriptionState(t, service, matchingExisting, "WATCHER", ups.SubscriptionWithDeletionLock)
	assertNoSubscription(t, service, nonMatchingExisting, "WATCHER")
	deliveries := deliveriesForAE(t, service, "WATCHER")
	if len(deliveries) != 1 || deliveries[0].SOPInstanceUID != matchingExisting || deliveries[0].EventType != ups.EventStateReport {
		t.Fatalf("initial filtered deliveries = %#v", deliveries)
	}

	// Recreating the service over the same store models restart/reconnect. The
	// canonical filter must not depend on service-local state or caller slices.
	restarted := newFilteredService(t, store)
	matchingFuture := createScheduledWithWorklist(t, restarted, "1.2.826.0.1.3680043.10.543.837.3", "RAD-CT")
	nonMatchingFuture := createScheduledWithWorklist(t, restarted, "1.2.826.0.1.3680043.10.543.837.4", "CARDIOLOGY")
	assertSubscriptionState(t, restarted, matchingFuture, "WATCHER", ups.SubscriptionWithDeletionLock)
	assertNoSubscription(t, restarted, nonMatchingFuture, "WATCHER")

	// The predicate creates durable instance subscriptions; it is not reapplied
	// at delivery time when a workitem later enters or leaves the matching set.
	if _, err := restarted.Set(ctx, ups.SetRequest{SOPInstanceUID: matchingFuture, Modifications: ups.NewDataSet(
		ups.StringElement(ups.TagWorklistLabel, core.VRLO, "CARDIOLOGY"),
	)}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.Set(ctx, ups.SetRequest{SOPInstanceUID: nonMatchingFuture, Modifications: ups.NewDataSet(
		ups.StringElement(ups.TagWorklistLabel, core.VRLO, "RADIOLOGY"),
	)}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ChangeState(ctx, ups.ChangeStateRequest{
		SOPInstanceUID: matchingFuture, State: ups.StateInProgress,
		TransactionUID: "1.2.826.0.1.3680043.10.543.837.30",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ChangeState(ctx, ups.ChangeStateRequest{
		SOPInstanceUID: nonMatchingFuture, State: ups.StateInProgress,
		TransactionUID: "1.2.826.0.1.3680043.10.543.837.40",
	}); err != nil {
		t.Fatal(err)
	}
	deliveries = deliveriesForAE(t, restarted, "WATCHER")
	counts := make(map[string]int)
	for _, delivery := range deliveries {
		counts[delivery.SOPInstanceUID]++
	}
	if counts[matchingExisting] != 1 || counts[matchingFuture] != 2 || counts[nonMatchingExisting] != 0 || counts[nonMatchingFuture] != 0 {
		t.Fatalf("delivery counts = %#v; deliveries = %#v", counts, deliveries)
	}
}

func TestFilteredGlobalWithoutLockSkipsExistingInitialEventButDeliversFutureEvents(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	existing := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.11", "RADIOLOGY")
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
	}); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionState(t, service, existing, "WATCHER", ups.SubscriptionWithoutLock)
	if deliveries := deliveriesForAE(t, service, "WATCHER"); len(deliveries) != 0 {
		t.Fatalf("unexpected existing initial deliveries = %#v", deliveries)
	}
	future := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.12", "RADIOLOGY")
	deliveries := deliveriesForAE(t, service, "WATCHER")
	if len(deliveries) != 1 || deliveries[0].SOPInstanceUID != future {
		t.Fatalf("future deliveries = %#v", deliveries)
	}
}

func TestGlobalAndFilteredGlobalSubscriptionsCoexist(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.GlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
	}); err != nil {
		t.Fatal(err)
	}
	assertSubscriptionState(t, service, ups.GlobalSubscriptionSOPInstanceUID, "WATCHER", ups.SubscriptionWithoutLock)
	assertSubscriptionState(t, service, ups.FilteredGlobalSubscriptionSOPInstanceUID, "WATCHER", ups.SubscriptionWithoutLock)
}

func TestFilteredGlobalSuspendAndUnsubscribePreserveOutboxSemantics(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	first := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.21", "RADIOLOGY")
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true,
		MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.SuspendGlobal(context.Background(), "WATCHER"); err != nil {
		t.Fatal(err)
	}
	assertNoSubscription(t, service, ups.FilteredGlobalSubscriptionSOPInstanceUID, "WATCHER")
	assertSubscriptionState(t, service, first, "WATCHER", ups.SubscriptionWithDeletionLock)
	second := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.22", "RADIOLOGY")
	assertNoSubscription(t, service, second, "WATCHER")

	before := len(deliveriesForAE(t, service, "WATCHER"))
	if err := service.Unsubscribe(context.Background(), ups.UnsubscribeRequest{
		SOPInstanceUID: ups.GlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
	}); err != nil {
		t.Fatal(err)
	}
	assertNoSubscription(t, service, first, "WATCHER")
	if after := len(deliveriesForAE(t, service, "WATCHER")); after != before {
		t.Fatalf("global unsubscribe changed committed outbox: before=%d after=%d", before, after)
	}
}

func TestFilteredGlobalUnsubscribeDoesNotRemoveSpecificSubscription(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	step := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.23", "RADIOLOGY")
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: step, ReceivingAETitle: "WATCHER",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
		MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Unsubscribe(context.Background(), ups.UnsubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER",
	}); err != nil {
		t.Fatal(err)
	}
	assertNoSubscription(t, service, ups.FilteredGlobalSubscriptionSOPInstanceUID, "WATCHER")
	assertSubscriptionState(t, service, step, "WATCHER", ups.SubscriptionWithoutLock)
}

func TestFilteredGlobalSubscribeIsAtomicWithConcurrentCreate(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	const steps = 48
	start := make(chan struct{})
	errorsCh := make(chan error, steps+1)
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		<-start
		_, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
			SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true,
			MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
		})
		errorsCh <- err
	}()
	for index := 0; index < steps; index++ {
		index := index
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			label := "CARDIOLOGY"
			if index%2 == 0 {
				label = "RADIOLOGY"
			}
			_, err := service.Create(context.Background(), ups.CreateRequest{
				SOPInstanceUID: fmt.Sprintf("1.2.826.0.1.3680043.10.543.837.3.%d", index+1),
				Attributes:     scheduledWithWorklist(t, label),
			})
			errorsCh <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsCh)
	for err := range errorsCh {
		if err != nil {
			t.Fatal(err)
		}
	}
	for index := 0; index < steps; index++ {
		uid := fmt.Sprintf("1.2.826.0.1.3680043.10.543.837.3.%d", index+1)
		if index%2 == 0 {
			assertSubscriptionState(t, service, uid, "WATCHER", ups.SubscriptionWithDeletionLock)
		} else {
			assertNoSubscription(t, service, uid, "WATCHER")
		}
	}
	if deliveries := deliveriesForAE(t, service, "WATCHER"); len(deliveries) != steps/2 {
		t.Fatalf("concurrent delivery count = %d, want %d", len(deliveries), steps/2)
	}
}

func TestFilteredGlobalValidationReturnsNormalizedArgumentStatuses(t *testing.T) {
	store, _ := filteredService(t, ups.MemoryStoreOptions{})
	resolverCalls := 0
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		resolverCalls++
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	for name, test := range map[string]struct {
		keys       map[string][]string
		wantStatus uint16
	}{
		"empty":             {map[string][]string{}, ups.StatusInvalidArgumentValue},
		"unknown tag":       {map[string][]string{"99990001": {"X"}}, ups.StatusNoSuchArgument},
		"UI wildcard":       {map[string][]string{ups.TagSOPInstanceUID.String(): {"1.2.*"}}, ups.StatusInvalidArgumentValue},
		"sequence matching": {map[string][]string{ups.TagScheduledWorkitemCodeSequence.String(): {"CODE"}}, ups.StatusInvalidArgumentValue},
	} {
		t.Run(name, func(t *testing.T) {
			_, subscribeErr := service.Subscribe(context.Background(), ups.SubscribeRequest{
				SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", MatchingKeys: test.keys,
			})
			if !ups.IsStatus(subscribeErr, test.wantStatus) {
				t.Fatalf("Subscribe error = %v, want 0x%04X", subscribeErr, test.wantStatus)
			}
		})
	}
	tooMany := make(map[string][]string)
	for index := 0; index < 17; index++ {
		tooMany[fmt.Sprintf("%04X%04X", 0x7001+index*2, 1)] = []string{"X"}
	}
	_, err = service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", MatchingKeys: tooMany,
	})
	if !ups.IsStatus(err, ups.StatusResourceLimitation) {
		t.Fatalf("over-limit Subscribe error = %v", err)
	}
	if resolverCalls != 0 {
		t.Fatalf("invalid filters reached resolver %d times", resolverCalls)
	}
	for _, uid := range []string{ups.GlobalSubscriptionSOPInstanceUID, "1.2.826.0.1.3680043.10.543.837.50"} {
		_, subscribeErr := service.Subscribe(context.Background(), ups.SubscribeRequest{
			SOPInstanceUID: uid, ReceivingAETitle: "WATCHER",
			MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
		})
		if !ups.IsStatus(subscribeErr, ups.StatusActionNotAppropriate) {
			t.Fatalf("matching keys for %s error = %v", uid, subscribeErr)
		}
	}

	options := service.NormalizedOptions(nil)
	ctx := normalizedContext(context.Background(), ups.WatchSOPClassUID)
	for name, test := range map[string]struct {
		information *object.Object
		wantStatus  uint16
	}{
		"unknown argument": {
			ups.NewDataSet(
				ups.StringElement(ups.TagReceivingAE, core.VRAE, "WATCHER"),
				ups.StringElement(ups.TagDeletionLock, core.VRLO, "FALSE"),
				ups.StringElement(core.NewTag(0x9999, 0x0001), core.VRLO, "X"),
			), ups.StatusNoSuchArgument,
		},
		"invalid VR": {
			ups.NewDataSet(
				ups.StringElement(ups.TagReceivingAE, core.VRAE, "WATCHER"),
				ups.StringElement(ups.TagDeletionLock, core.VRLO, "FALSE"),
				ups.StringElement(ups.TagWorklistLabel, core.VRCS, "RADIOLOGY"),
			), ups.StatusInvalidArgumentValue,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result, actionErr := options.ActionHandler(ctx, dimse.NormalizedActionRequest{
				RequestedSOPClassUID:    ups.PushSOPClassUID,
				RequestedSOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID,
				ActionTypeID:            ups.ActionSubscribe,
			}, test.information)
			if actionErr == nil || result.Response.Status != test.wantStatus {
				t.Fatalf("Action = %#v, %v; want 0x%04X", result, actionErr, test.wantStatus)
			}
		})
	}
}

func TestNormalizedFilteredGlobalSubscribeMaterializesMatchingUPS(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{})
	matching := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.51", "RADIOLOGY")
	nonMatching := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.52", "CARDIOLOGY")
	information, err := ups.BuildSubscriptionInformation("WATCHER", true, map[string][]string{
		ups.TagWorklistLabel.String(): {"RAD*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	result, actionErr := service.NormalizedOptions(nil).ActionHandler(
		normalizedContext(context.Background(), ups.WatchSOPClassUID),
		dimse.NormalizedActionRequest{
			RequestedSOPClassUID:    ups.PushSOPClassUID,
			RequestedSOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID,
			ActionTypeID:            ups.ActionSubscribe,
		},
		information,
	)
	if actionErr != nil || result.Response.Status != ups.StatusSuccess {
		t.Fatalf("normalized filtered subscribe = %#v, %v", result, actionErr)
	}
	assertSubscriptionState(t, service, matching, "WATCHER", ups.SubscriptionWithDeletionLock)
	assertNoSubscription(t, service, nonMatching, "WATCHER")
}

func TestFilteredGlobalResourceLimitRollsBackAtomically(t *testing.T) {
	t.Parallel()
	_, service := filteredService(t, ups.MemoryStoreOptions{MaxFanOut: 1})
	first := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.41", "RADIOLOGY")
	second := createScheduledWithWorklist(t, service, "1.2.826.0.1.3680043.10.543.837.42", "RADIOLOGY")
	_, err := service.Subscribe(context.Background(), ups.SubscribeRequest{
		SOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER", DeletionLock: true,
		MatchingKeys: map[string][]string{ups.TagWorklistLabel.String(): {"RADIOLOGY"}},
	})
	if !ups.IsStatus(err, ups.StatusResourceLimitation) {
		t.Fatalf("Subscribe error = %v", err)
	}
	information, buildErr := ups.BuildSubscriptionInformation("WATCHER", true, map[string][]string{
		ups.TagWorklistLabel.String(): {"RADIOLOGY"},
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	action, actionErr := service.NormalizedOptions(nil).ActionHandler(
		normalizedContext(context.Background(), ups.WatchSOPClassUID),
		dimse.NormalizedActionRequest{
			RequestedSOPClassUID:    ups.PushSOPClassUID,
			RequestedSOPInstanceUID: ups.FilteredGlobalSubscriptionSOPInstanceUID,
			ActionTypeID:            ups.ActionSubscribe,
		},
		information,
	)
	if actionErr == nil || action.Response.Status != ups.StatusResourceLimitation {
		t.Fatalf("normalized limited subscribe = %#v, %v", action, actionErr)
	}
	for _, uid := range []string{ups.FilteredGlobalSubscriptionSOPInstanceUID, first, second} {
		assertNoSubscription(t, service, uid, "WATCHER")
	}
	if deliveries := deliveriesForAE(t, service, "WATCHER"); len(deliveries) != 0 {
		t.Fatalf("rolled-back deliveries = %#v", deliveries)
	}
}

func filteredService(t *testing.T, options ups.MemoryStoreOptions) (*ups.MemoryStore, *ups.Service) {
	t.Helper()
	store, err := ups.NewMemoryStore(options)
	if err != nil {
		t.Fatal(err)
	}
	return store, newFilteredService(t, store)
}

func newFilteredService(t *testing.T, store *ups.MemoryStore) *ups.Service {
	t.Helper()
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func scheduledWithWorklist(t *testing.T, label string) *object.Object {
	t.Helper()
	attributes := scheduledAttributes(t)
	attributes.Put(ups.StringElement(ups.TagWorklistLabel, core.VRLO, label))
	return attributes
}

func createScheduledWithWorklist(t *testing.T, service *ups.Service, uid, label string) string {
	t.Helper()
	if _, err := service.Create(context.Background(), ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledWithWorklist(t, label)}); err != nil {
		t.Fatal(err)
	}
	return uid
}

func assertSubscriptionState(t *testing.T, service *ups.Service, uid, ae string, want ups.SubscriptionState) {
	t.Helper()
	subscription, err := service.Subscription(context.Background(), uid, ae)
	if err != nil || subscription.State != want {
		t.Fatalf("subscription %s/%s = %#v, %v; want %s", uid, ae, subscription, err, want)
	}
}

func assertNoSubscription(t *testing.T, service *ups.Service, uid, ae string) {
	t.Helper()
	if subscription, err := service.Subscription(context.Background(), uid, ae); !errors.Is(err, ups.ErrNotFound) {
		t.Fatalf("subscription %s/%s = %#v, %v; want not found", uid, ae, subscription, err)
	}
}

func deliveriesForAE(t *testing.T, service *ups.Service, ae string) []ups.Delivery {
	t.Helper()
	deliveries, err := service.Deliveries(context.Background(), ups.DeliveryQuery{ReceivingAETitle: ae, Limit: 1_000})
	if err != nil {
		t.Fatal(err)
	}
	return deliveries
}
