package ups_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestSpecificSubscriptionPersistsAndInitialEventIsDurable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolver := ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		return ups.CallbackTarget{Address: "127.0.0.1:11112", DialOptions: ul.DialOptions{}}, nil
	})
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	uid := "1.2.826.0.1.3680043.10.543.10"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	result, err := service.Subscribe(ctx, ups.SubscribeRequest{
		SOPInstanceUID: uid, ReceivingAETitle: "WATCHER", DeletionLock: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Subscription.State != ups.SubscriptionWithDeletionLock {
		t.Fatalf("subscription = %#v", result.Subscription)
	}

	// A new Service represents process restart while the caller-owned durable
	// backend remains. The subscription and initial delivery must still exist.
	restarted, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	subscription, err := restarted.Subscription(ctx, uid, "WATCHER")
	if err != nil {
		t.Fatal(err)
	}
	if subscription.State != ups.SubscriptionWithDeletionLock {
		t.Fatalf("reloaded subscription = %#v", subscription)
	}
	deliveries, err := restarted.Deliveries(ctx, ups.DeliveryQuery{ReceivingAETitle: "WATCHER", Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	if len(deliveries) != 1 || deliveries[0].EventType != ups.EventStateReport || deliveries[0].State != ups.DeliveryPending {
		t.Fatalf("initial deliveries = %#v", deliveries)
	}
}

func TestGlobalSubscriptionMatrixAndSuspend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolver := ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	first := "1.2.826.0.1.3680043.10.543.11"
	second := "1.2.826.0.1.3680043.10.543.12"
	third := "1.2.826.0.1.3680043.10.543.13"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: first, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(ctx, ups.SubscribeRequest{
		SOPInstanceUID:   ups.GlobalSubscriptionSOPInstanceUID,
		ReceivingAETitle: "WATCHER", DeletionLock: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: second, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	for _, uid := range []string{first, second} {
		subscription, err := service.Subscription(ctx, uid, "WATCHER")
		if err != nil {
			t.Fatal(err)
		}
		if subscription.State != ups.SubscriptionWithDeletionLock {
			t.Fatalf("subscription for %s = %#v", uid, subscription)
		}
	}
	if err := service.SuspendGlobal(ctx, "WATCHER"); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: third, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscription(ctx, third, "WATCHER"); !errors.Is(err, ups.ErrNotFound) {
		t.Fatalf("subscription inherited after suspend error = %v", err)
	}
	for _, uid := range []string{first, second} {
		if _, err := service.Subscription(ctx, uid, "WATCHER"); err != nil {
			t.Fatalf("specific subscription removed by suspend: %v", err)
		}
	}
}

func TestUnsubscribeAndSuspendDoNotRequireResolverAfterSubscription(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	available := true
	resolver := ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		if !available {
			return ups.CallbackTarget{}, errors.New("destination rotated")
		}
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	service, err := ups.NewService(store, ups.ServiceOptions{CallbackResolver: resolver})
	if err != nil {
		t.Fatal(err)
	}
	uid := "1.2.826.0.1.3680043.10.543.619.183"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(ctx, ups.SubscribeRequest{SOPInstanceUID: uid, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(ctx, ups.SubscribeRequest{SOPInstanceUID: ups.GlobalSubscriptionSOPInstanceUID, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	available = false
	if err := service.Unsubscribe(ctx, ups.UnsubscribeRequest{SOPInstanceUID: uid, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatalf("unsubscribe after resolver rotation: %v", err)
	}
	if err := service.SuspendGlobal(ctx, "WATCHER"); err != nil {
		t.Fatalf("suspend after resolver rotation: %v", err)
	}
}

func TestDeliveryFailureDoesNotRollbackStateAndIsObservable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store, err := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	resolverAvailable := true
	resolver := ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		if !resolverAvailable {
			return ups.CallbackTarget{}, errors.New("PATIENT^NAME")
		}
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})
	service, err := ups.NewService(store, ups.ServiceOptions{
		CallbackResolver: resolver,
		DeliveryLimits:   ups.DeliveryLimits{InitialBackoff: time.Millisecond, MaxAttempts: 3},
	})
	if err != nil {
		t.Fatal(err)
	}
	uid := "1.2.826.0.1.3680043.10.543.14"
	if _, err := service.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Subscribe(ctx, ups.SubscribeRequest{SOPInstanceUID: uid, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	const transactionUID = "1.2.826.0.1.3680043.10.543.140"
	if _, err := service.ChangeState(ctx, ups.ChangeStateRequest{SOPInstanceUID: uid, State: ups.StateInProgress, TransactionUID: transactionUID}); err != nil {
		t.Fatal(err)
	}
	resolverAvailable = false
	err = service.DeliverDue(ctx, 16)
	if err == nil || !errors.Is(err, ups.ErrDeliveryFailed) || contains(err.Error(), "PATIENT^NAME") {
		t.Fatalf("DeliverDue error = %v", err)
	}
	step, err := service.Get(ctx, uid)
	if err != nil {
		t.Fatal(err)
	}
	if step.State != ups.StateInProgress || step.TransactionUID != transactionUID {
		t.Fatalf("step rolled back after callback failure: %#v", step)
	}
	deliveries, err := service.Deliveries(ctx, ups.DeliveryQuery{SOPInstanceUID: uid, Limit: 16})
	if err != nil {
		t.Fatal(err)
	}
	failed := 0
	for _, delivery := range deliveries {
		if delivery.State == ups.DeliveryFailed && delivery.LastFailure == ups.DeliveryFailureCallbackUnknown {
			failed++
		}
	}
	if failed == 0 {
		t.Fatalf("deliveries = %#v", deliveries)
	}
}

func contains(value, needle string) bool {
	for index := 0; index+len(needle) <= len(value); index++ {
		if value[index:index+len(needle)] == needle {
			return true
		}
	}
	return false
}
