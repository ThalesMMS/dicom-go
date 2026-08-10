package ups

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
)

const (
	defaultMemoryMaxSteps         = 10_000
	defaultMemoryMaxSubscriptions = 100_000
	defaultMemoryMaxEvents        = 100_000
	defaultMemoryMaxDeliveries    = 500_000
	defaultMemoryMaxFanOut        = 10_000
)

// MemoryStore is a concurrency-safe reference repository. Its single mutex is
// intentional: CommitUPS demonstrates the atomic state-plus-outbox contract
// expected from persistent implementations.
type MemoryStore struct {
	mu               sync.RWMutex
	maxSteps         int
	maxSubscriptions int
	maxEvents        int
	maxDeliveries    int
	maxFanOut        int
	sequence         uint64
	eventSequence    uint64
	leaseSequence    uint64
	steps            map[string]Step
	subscriptions    map[string]Subscription
	events           map[string]Event
	eventOrder       []string
	deliveries       map[string]Delivery
	deliveryOrder    []string
}

func NewMemoryStore(options MemoryStoreOptions) (*MemoryStore, error) {
	if options.MaxSteps < 0 || options.MaxSubscriptions < 0 || options.MaxEvents < 0 || options.MaxDeliveries < 0 || options.MaxFanOut < 0 {
		return nil, ErrResourceLimit
	}
	if options.MaxSteps == 0 {
		options.MaxSteps = defaultMemoryMaxSteps
	}
	if options.MaxEvents == 0 {
		options.MaxEvents = defaultMemoryMaxEvents
	}
	if options.MaxSubscriptions == 0 {
		options.MaxSubscriptions = defaultMemoryMaxSubscriptions
	}
	if options.MaxDeliveries == 0 {
		options.MaxDeliveries = defaultMemoryMaxDeliveries
	}
	if options.MaxFanOut == 0 {
		options.MaxFanOut = defaultMemoryMaxFanOut
	}
	return &MemoryStore{
		maxSteps: options.MaxSteps, maxSubscriptions: options.MaxSubscriptions,
		maxEvents: options.MaxEvents, maxDeliveries: options.MaxDeliveries, maxFanOut: options.MaxFanOut,
		steps: make(map[string]Step), subscriptions: make(map[string]Subscription),
		events: make(map[string]Event), deliveries: make(map[string]Delivery),
	}, nil
}

func (store *MemoryStore) GetStep(ctx context.Context, sopInstanceUID string) (Step, error) {
	if err := contextError(ctx); err != nil {
		return Step{}, err
	}
	store.mu.RLock()
	step, ok := store.steps[sopInstanceUID]
	store.mu.RUnlock()
	if !ok {
		return Step{}, ErrNotFound
	}
	return cloneStep(ctx, step)
}

func (store *MemoryStore) ListSteps(ctx context.Context, query StepQuery) ([]Step, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, ErrResourceLimit
	}
	wanted := make(map[State]bool, len(query.States))
	for _, state := range query.States {
		wanted[state] = true
	}
	store.mu.RLock()
	steps := make([]Step, 0, min(query.Limit, len(store.steps)))
	for _, step := range store.steps {
		if step.Sequence <= query.AfterSequence || len(wanted) != 0 && !wanted[step.State] {
			continue
		}
		steps = append(steps, step)
	}
	store.mu.RUnlock()
	sort.Slice(steps, func(left, right int) bool { return steps[left].Sequence < steps[right].Sequence })
	if len(steps) > query.Limit {
		steps = steps[:query.Limit]
	}
	for index := range steps {
		clone, err := cloneStep(ctx, steps[index])
		if err != nil {
			return nil, err
		}
		steps[index] = clone
	}
	return steps, nil
}

func (store *MemoryStore) ListEvents(ctx context.Context, query EventQuery) ([]Event, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, ErrResourceLimit
	}
	store.mu.RLock()
	result := make([]Event, 0, min(query.Limit, len(store.eventOrder)))
	started := query.AfterID == ""
	for _, id := range store.eventOrder {
		if !started {
			started = id == query.AfterID
			continue
		}
		event := store.events[id]
		if query.SOPInstanceUID != "" && event.SOPInstanceUID != query.SOPInstanceUID {
			continue
		}
		result = append(result, event)
		if len(result) == query.Limit {
			break
		}
	}
	store.mu.RUnlock()
	for index := range result {
		clone, err := cloneEvent(ctx, result[index])
		if err != nil {
			return nil, err
		}
		result[index] = clone
	}
	return result, nil
}

func (store *MemoryStore) GetSubscription(ctx context.Context, sopInstanceUID, receivingAETitle string) (Subscription, error) {
	if err := contextError(ctx); err != nil {
		return Subscription{}, err
	}
	store.mu.RLock()
	subscription, ok := store.subscriptions[subscriptionKey(sopInstanceUID, receivingAETitle)]
	store.mu.RUnlock()
	if !ok || subscription.State == SubscriptionNone {
		return Subscription{}, ErrNotFound
	}
	return subscription, nil
}

func (store *MemoryStore) ListSubscriptions(ctx context.Context, query SubscriptionQuery) ([]Subscription, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, ErrResourceLimit
	}
	store.mu.RLock()
	result := make([]Subscription, 0, min(query.Limit, len(store.subscriptions)))
	for _, subscription := range store.subscriptions {
		if query.SOPInstanceUID != "" && subscription.SOPInstanceUID != query.SOPInstanceUID {
			continue
		}
		if query.ReceivingAETitle != "" && subscription.ReceivingAETitle != query.ReceivingAETitle {
			continue
		}
		if query.ActiveOnly && subscription.State == SubscriptionNone {
			continue
		}
		result = append(result, subscription)
	}
	store.mu.RUnlock()
	sort.Slice(result, func(left, right int) bool {
		if result[left].ReceivingAETitle != result[right].ReceivingAETitle {
			return result[left].ReceivingAETitle < result[right].ReceivingAETitle
		}
		return result[left].SOPInstanceUID < result[right].SOPInstanceUID
	})
	if len(result) > query.Limit {
		result = result[:query.Limit]
	}
	return result, nil
}

func (store *MemoryStore) ListActiveReceivingAETitles(ctx context.Context, limit int) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 {
		return nil, ErrResourceLimit
	}
	store.mu.RLock()
	unique := make(map[string]struct{})
	for _, subscription := range store.subscriptions {
		if subscription.State != SubscriptionNone {
			unique[subscription.ReceivingAETitle] = struct{}{}
		}
	}
	store.mu.RUnlock()
	result := make([]string, 0, min(limit, len(unique)))
	for ae := range unique {
		result = append(result, ae)
	}
	sort.Strings(result)
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func (store *MemoryStore) ListDeliveries(ctx context.Context, query DeliveryQuery) ([]Delivery, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if query.Limit <= 0 {
		return nil, ErrResourceLimit
	}
	wanted := make(map[DeliveryState]bool, len(query.States))
	for _, state := range query.States {
		wanted[state] = true
	}
	store.mu.RLock()
	result := make([]Delivery, 0, min(query.Limit, len(store.deliveryOrder)))
	for _, id := range store.deliveryOrder {
		delivery := store.deliveries[id]
		if query.SOPInstanceUID != "" && delivery.SOPInstanceUID != query.SOPInstanceUID {
			continue
		}
		if query.ReceivingAETitle != "" && delivery.ReceivingAETitle != query.ReceivingAETitle {
			continue
		}
		if len(wanted) != 0 && !wanted[delivery.State] {
			continue
		}
		result = append(result, delivery)
		if len(result) == query.Limit {
			break
		}
	}
	store.mu.RUnlock()
	for index := range result {
		clone, err := cloneDelivery(ctx, result[index])
		if err != nil {
			return nil, err
		}
		result[index] = clone
	}
	return result, nil
}

func (store *MemoryStore) ClaimDueDeliveries(ctx context.Context, now time.Time, limit int, lease time.Duration) ([]Delivery, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if limit <= 0 || lease <= 0 {
		return nil, ErrResourceLimit
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	claimed := make([]Delivery, 0, min(limit, len(store.deliveryOrder)))
	for _, id := range store.deliveryOrder {
		if len(claimed) == limit {
			break
		}
		delivery := store.deliveries[id]
		due := delivery.State == DeliveryPending || delivery.State == DeliveryFailed || delivery.State == DeliveryDelivering && !delivery.LeaseUntil.After(now)
		if !due || delivery.NextAttemptAt.After(now) {
			continue
		}
		store.leaseSequence++
		delivery.State = DeliveryDelivering
		delivery.Version++
		delivery.Attempts++
		delivery.LeaseToken = fmt.Sprintf("lease-%020d", store.leaseSequence)
		delivery.LeaseUntil = now.Add(lease)
		store.deliveries[id] = delivery
		claimed = append(claimed, delivery)
	}
	for index := range claimed {
		clone, err := cloneDelivery(context.Background(), claimed[index])
		if err != nil {
			return nil, err
		}
		claimed[index] = clone
	}
	return claimed, nil
}

func (store *MemoryStore) CompleteDelivery(ctx context.Context, id, leaseToken string, outcome DeliveryOutcome) (Delivery, error) {
	if err := contextError(ctx); err != nil {
		return Delivery{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	delivery, ok := store.deliveries[id]
	if !ok {
		return Delivery{}, ErrNotFound
	}
	if delivery.State != DeliveryDelivering || delivery.LeaseToken == "" || delivery.LeaseToken != leaseToken {
		return Delivery{}, ErrConcurrentUpdate
	}
	delivery.Version++
	delivery.LeaseToken = ""
	delivery.LeaseUntil = time.Time{}
	delivery.LastFailure = outcome.Failure
	delivery.LastDIMSEStatus = outcome.DIMSEStatus
	if outcome.Delivered {
		delivery.State = DeliveryDelivered
		delivery.NextAttemptAt = time.Time{}
	} else if outcome.Retryable {
		delivery.State = DeliveryFailed
		delivery.NextAttemptAt = outcome.NextAttempt
	} else {
		delivery.State = DeliveryExhausted
		delivery.NextAttemptAt = time.Time{}
	}
	store.deliveries[id] = delivery
	return cloneDelivery(context.Background(), delivery)
}

func (store *MemoryStore) CommitUPS(ctx context.Context, request CommitRequest) (CommitResult, error) {
	if err := contextError(ctx); err != nil {
		return CommitResult{}, err
	}
	if request.Step == nil && request.Subscription == nil && len(request.Events) == 0 {
		return CommitResult{}, ErrConflict
	}
	var nextStep Step
	if request.Step != nil {
		if request.Step.Next.SOPInstanceUID == "" {
			return CommitResult{}, ErrConflict
		}
		clone, err := cloneStep(ctx, request.Step.Next)
		if err != nil {
			return CommitResult{}, err
		}
		nextStep = clone
	}
	events := make([]Event, len(request.Events))
	for index, event := range request.Events {
		clone, cloneErr := cloneEvent(ctx, event)
		if cloneErr != nil {
			return CommitResult{}, cloneErr
		}
		events[index] = clone
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	// Stage subscription changes in a shallow value map so all capacity and CAS
	// checks complete before any authoritative map is mutated.
	stagedSubscriptions := make(map[string]Subscription, len(store.subscriptions)+1)
	for key, subscription := range store.subscriptions {
		stagedSubscriptions[key] = subscription
	}
	var currentStep Step
	var stepExists bool
	if request.Step != nil {
		currentStep, stepExists = store.steps[nextStep.SOPInstanceUID]
		if request.Step.ExpectedVersion == 0 {
			if stepExists {
				return CommitResult{}, ErrConflict
			}
			if len(store.steps) >= store.maxSteps {
				return CommitResult{}, ErrResourceLimit
			}
			nextStep.Version = 1
			nextStep.Sequence = store.sequence + 1
		} else {
			if !stepExists {
				return CommitResult{}, ErrNotFound
			}
			if currentStep.Version != request.Step.ExpectedVersion {
				return CommitResult{}, ErrConcurrentUpdate
			}
			if currentStep.Sequence != nextStep.Sequence {
				return CommitResult{}, ErrConflict
			}
			nextStep.Version = currentStep.Version + 1
			nextStep.Sequence = currentStep.Sequence
		}
	}

	// A newly created UPS inherits every active global subscription.
	if request.Step != nil && !stepExists {
		for _, subscription := range stagedSubscriptions {
			if subscription.SOPInstanceUID != GlobalSubscriptionSOPInstanceUID || subscription.State == SubscriptionNone {
				continue
			}
			key := subscriptionKey(nextStep.SOPInstanceUID, subscription.ReceivingAETitle)
			if _, exists := stagedSubscriptions[key]; exists {
				continue
			}
			inherited := subscription
			inherited.SOPInstanceUID = nextStep.SOPInstanceUID
			inherited.Version = 1
			stagedSubscriptions[key] = inherited
		}
	}

	var committedSubscription *Subscription
	if mutation := request.Subscription; mutation != nil {
		if mutation.ReceivingAETitle == "" {
			return CommitResult{}, ErrConflict
		}
		switch mutation.Kind {
		case SubscriptionMutationSubscribe:
			state := SubscriptionWithoutLock
			if mutation.DeletionLock {
				state = SubscriptionWithDeletionLock
			}
			key := subscriptionKey(mutation.SOPInstanceUID, mutation.ReceivingAETitle)
			current := stagedSubscriptions[key]
			next := Subscription{SOPInstanceUID: mutation.SOPInstanceUID, ReceivingAETitle: mutation.ReceivingAETitle, State: state, Version: current.Version + 1, UpdatedAt: mutation.UpdatedAt}
			if next.Version == 0 {
				next.Version = 1
			}
			stagedSubscriptions[key] = next
			committedSubscription = &next
			if mutation.SOPInstanceUID == GlobalSubscriptionSOPInstanceUID {
				fanOut := 0
				for _, step := range store.steps {
					specificKey := subscriptionKey(step.SOPInstanceUID, mutation.ReceivingAETitle)
					specific, exists := stagedSubscriptions[specificKey]
					if exists && specific.State != SubscriptionNone {
						continue
					}
					fanOut++
					if fanOut > store.maxFanOut {
						return CommitResult{}, ErrResourceLimit
					}
					specific = Subscription{SOPInstanceUID: step.SOPInstanceUID, ReceivingAETitle: mutation.ReceivingAETitle, State: state, Version: specific.Version + 1, UpdatedAt: mutation.UpdatedAt}
					stagedSubscriptions[specificKey] = specific
					if mutation.DeletionLock {
						events = append(events, stateEventForStep(step, mutation.ReceivingAETitle, mutation.UpdatedAt))
					}
				}
			} else {
				step, ok := store.steps[mutation.SOPInstanceUID]
				if request.Step != nil && nextStep.SOPInstanceUID == mutation.SOPInstanceUID {
					step, ok = nextStep, true
				}
				if !ok {
					return CommitResult{}, ErrNotFound
				}
				events = append(events, stateEventForStep(step, mutation.ReceivingAETitle, mutation.UpdatedAt))
			}
		case SubscriptionMutationUnsubscribe:
			if mutation.SOPInstanceUID == GlobalSubscriptionSOPInstanceUID {
				for key, subscription := range stagedSubscriptions {
					if subscription.ReceivingAETitle == mutation.ReceivingAETitle {
						delete(stagedSubscriptions, key)
					}
				}
			} else {
				key := subscriptionKey(mutation.SOPInstanceUID, mutation.ReceivingAETitle)
				current := stagedSubscriptions[key]
				current.SOPInstanceUID = mutation.SOPInstanceUID
				current.ReceivingAETitle = mutation.ReceivingAETitle
				current.State = SubscriptionNone
				current.Version++
				current.UpdatedAt = mutation.UpdatedAt
				stagedSubscriptions[key] = current
			}
		case SubscriptionMutationSuspendGlobal:
			delete(stagedSubscriptions, subscriptionKey(GlobalSubscriptionSOPInstanceUID, mutation.ReceivingAETitle))
		default:
			return CommitResult{}, ErrConflict
		}
	}
	reclaimSubscriptionTombstones(stagedSubscriptions, store.steps, store.maxSubscriptions)
	activeSubscriptions := 0
	for _, subscription := range stagedSubscriptions {
		if subscription.State != SubscriptionNone {
			activeSubscriptions++
		}
	}
	if activeSubscriptions > store.maxSubscriptions {
		return CommitResult{}, ErrResourceLimit
	}

	// Assign immutable event IDs, then snapshot recipients into delivery rows in
	// the same commit. Later unsubscribe cannot erase an already committed event.
	stagedDeliveries := make([]Delivery, 0)
	nextEventSequence := store.eventSequence
	stagedEventIDs := make(map[string]struct{}, len(events))
	for index := range events {
		if events[index].SOPInstanceUID == "" && request.Step != nil {
			events[index].SOPInstanceUID = nextStep.SOPInstanceUID
		}
		if events[index].SOPInstanceUID == "" {
			return CommitResult{}, ErrConflict
		}
		if request.Step != nil && events[index].SOPInstanceUID == nextStep.SOPInstanceUID {
			events[index].StepVersion = nextStep.Version
		}
		if events[index].ID == "" {
			nextEventSequence++
			events[index].ID = fmt.Sprintf("event-%020d", nextEventSequence)
		}
		cloned, cloneErr := cloneEvent(context.Background(), events[index])
		if cloneErr != nil {
			return CommitResult{}, cloneErr
		}
		events[index] = cloned
		if _, duplicate := store.events[events[index].ID]; duplicate {
			return CommitResult{}, ErrConflict
		}
		if _, duplicate := stagedEventIDs[events[index].ID]; duplicate {
			return CommitResult{}, ErrConflict
		}
		stagedEventIDs[events[index].ID] = struct{}{}
		recipients := []string(nil)
		if events[index].DirectReceivingAE != "" {
			recipients = append(recipients, events[index].DirectReceivingAE)
		} else {
			for _, subscription := range stagedSubscriptions {
				if subscription.SOPInstanceUID == events[index].SOPInstanceUID && subscription.State != SubscriptionNone {
					recipients = append(recipients, subscription.ReceivingAETitle)
				}
			}
			sort.Strings(recipients)
		}
		if len(recipients) > store.maxFanOut {
			return CommitResult{}, ErrResourceLimit
		}
		for _, receivingAE := range recipients {
			id := deliveryKey(events[index].ID, receivingAE)
			if _, exists := store.deliveries[id]; exists {
				continue
			}
			stagedDeliveries = append(stagedDeliveries, Delivery{
				ID: id, EventID: events[index].ID, EventType: events[index].Type,
				SOPInstanceUID: events[index].SOPInstanceUID, ReceivingAETitle: receivingAE,
				Event: events[index], State: DeliveryPending, Version: 1,
			})
		}
	}
	if len(events) > store.maxEvents-len(store.events) || len(stagedDeliveries) > store.maxDeliveries-len(store.deliveries) {
		return CommitResult{}, ErrResourceLimit
	}

	if request.Step != nil {
		store.steps[nextStep.SOPInstanceUID] = nextStep
		if !stepExists {
			store.sequence = nextStep.Sequence
		}
	}
	store.subscriptions = stagedSubscriptions
	store.eventSequence = nextEventSequence
	for _, event := range events {
		store.events[event.ID] = event
		store.eventOrder = append(store.eventOrder, event.ID)
	}
	for _, delivery := range stagedDeliveries {
		store.deliveries[delivery.ID] = delivery
		store.deliveryOrder = append(store.deliveryOrder, delivery.ID)
	}
	result := CommitResult{}
	if request.Step != nil {
		clone, err := cloneStep(context.Background(), nextStep)
		if err != nil {
			return CommitResult{}, err
		}
		result.Step = &clone
	}
	if committedSubscription != nil {
		clone := *committedSubscription
		result.Subscription = &clone
	}
	return result, nil
}

func reclaimSubscriptionTombstones(subscriptions map[string]Subscription, steps map[string]Step, maximum int) {
	activeGlobal := make(map[string]bool)
	for _, subscription := range subscriptions {
		if subscription.SOPInstanceUID == GlobalSubscriptionSOPInstanceUID && subscription.State != SubscriptionNone {
			activeGlobal[subscription.ReceivingAETitle] = true
		}
	}
	type tombstoneEntry struct {
		key       string
		updatedAt time.Time
	}
	tombstones := make([]tombstoneEntry, 0)
	for key, subscription := range subscriptions {
		if subscription.State != SubscriptionNone {
			continue
		}
		if _, stepExists := steps[subscription.SOPInstanceUID]; !stepExists || !activeGlobal[subscription.ReceivingAETitle] {
			delete(subscriptions, key)
			continue
		}
		tombstones = append(tombstones, tombstoneEntry{key: key, updatedAt: subscription.UpdatedAt})
	}
	if len(tombstones) <= maximum {
		return
	}
	sort.SliceStable(tombstones, func(left, right int) bool {
		return tombstones[left].updatedAt.Before(tombstones[right].updatedAt)
	})
	for _, tombstone := range tombstones[:len(tombstones)-maximum] {
		delete(subscriptions, tombstone.key)
	}
}

func cloneStep(ctx context.Context, step Step) (Step, error) {
	limits, _ := normalizeLimits(Limits{})
	attributes, err := cloneDataSet(ctx, step.Attributes, limits)
	if err != nil {
		return Step{}, err
	}
	step.Attributes = attributes
	return step, nil
}

func cloneEvent(ctx context.Context, event Event) (Event, error) {
	limits, _ := normalizeLimits(Limits{})
	information, err := cloneDataSet(ctx, event.Information, limits)
	if err != nil {
		return Event{}, err
	}
	event.Information = information
	return event, nil
}

func cloneDelivery(ctx context.Context, delivery Delivery) (Delivery, error) {
	event, err := cloneEvent(ctx, delivery.Event)
	if err != nil {
		return Delivery{}, err
	}
	delivery.Event = event
	return delivery, nil
}

func subscriptionKey(sopInstanceUID, receivingAETitle string) string {
	return receivingAETitle + "\x00" + sopInstanceUID
}

func deliveryKey(eventID, receivingAETitle string) string {
	return eventID + "\x00" + receivingAETitle
}

func stateEventForStep(step Step, receivingAETitle string, now time.Time) Event {
	information := core.DataSet{Elements: []core.Element{StringElement(TagProcedureStepState, core.VRCS, string(step.State))}}
	if readiness, ok := dataSetElement(step.Attributes, TagInputReadinessState); ok {
		information.Elements = append(information.Elements, readiness)
	}
	return Event{
		Type: EventStateReport, SOPInstanceUID: step.SOPInstanceUID, StepVersion: step.Version,
		Information: information, DirectReceivingAE: receivingAETitle, CreatedAt: now,
	}
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	return ctx.Err()
}
