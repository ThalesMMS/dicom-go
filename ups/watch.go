package ups

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

type SubscriptionState string

const (
	SubscriptionNone             SubscriptionState = "none"
	SubscriptionWithDeletionLock SubscriptionState = "with_deletion_lock"
	SubscriptionWithoutLock      SubscriptionState = "without_deletion_lock"
)

type Subscription struct {
	SOPInstanceUID   string
	ReceivingAETitle string
	State            SubscriptionState
	Version          uint64
	UpdatedAt        time.Time
}

type SubscriptionQuery struct {
	SOPInstanceUID   string
	ReceivingAETitle string
	ActiveOnly       bool
	Limit            int
}

type SubscriptionStore interface {
	GetSubscription(context.Context, string, string) (Subscription, error)
	ListSubscriptions(context.Context, SubscriptionQuery) ([]Subscription, error)
	// ListActiveReceivingAETitles returns at most limit distinct, sorted AE
	// Titles. Implementations should perform the distinct projection in the
	// repository rather than materializing every specific subscription.
	ListActiveReceivingAETitles(context.Context, int) ([]string, error)
}

type SubscriptionMutationKind string

const (
	SubscriptionMutationSubscribe     SubscriptionMutationKind = "subscribe"
	SubscriptionMutationUnsubscribe   SubscriptionMutationKind = "unsubscribe"
	SubscriptionMutationSuspendGlobal SubscriptionMutationKind = "suspend_global"
)

type SubscriptionMutation struct {
	Kind             SubscriptionMutationKind
	SOPInstanceUID   string
	ReceivingAETitle string
	DeletionLock     bool
	UpdatedAt        time.Time
}

type SubscribeRequest struct {
	SOPInstanceUID   string
	ReceivingAETitle string
	DeletionLock     bool
	MatchingKeys     map[string][]string
}

type SubscribeResult struct {
	Subscription Subscription
	Status       uint16
}

type UnsubscribeRequest struct {
	SOPInstanceUID   string
	ReceivingAETitle string
}

type DeliveryState string

const (
	DeliveryPending    DeliveryState = "pending"
	DeliveryDelivering DeliveryState = "delivering"
	DeliveryFailed     DeliveryState = "failed"
	DeliveryExhausted  DeliveryState = "exhausted"
	DeliveryDelivered  DeliveryState = "delivered"
)

type DeliveryFailureClass string

const (
	DeliveryFailureNone                DeliveryFailureClass = ""
	DeliveryFailureCallbackUnknown     DeliveryFailureClass = "callback_unknown"
	DeliveryFailureCallbackOffline     DeliveryFailureClass = "callback_offline"
	DeliveryFailureTLS                 DeliveryFailureClass = "tls"
	DeliveryFailureTimeout             DeliveryFailureClass = "timeout"
	DeliveryFailureCanceled            DeliveryFailureClass = "canceled"
	DeliveryFailureAssociationRejected DeliveryFailureClass = "association_rejected"
	DeliveryFailureDIMSEStatus         DeliveryFailureClass = "dimse_status"
	DeliveryFailureProtocol            DeliveryFailureClass = "protocol"
)

type Delivery struct {
	ID               string
	EventID          string
	EventType        EventType
	SOPInstanceUID   string
	ReceivingAETitle string
	Event            Event
	State            DeliveryState
	Version          uint64
	Attempts         int
	NextAttemptAt    time.Time
	LeaseToken       string
	LeaseUntil       time.Time
	LastFailure      DeliveryFailureClass
	LastDIMSEStatus  uint16
}

type DeliveryQuery struct {
	SOPInstanceUID   string
	ReceivingAETitle string
	States           []DeliveryState
	Limit            int
}

type DeliveryOutcome struct {
	Delivered   bool
	Retryable   bool
	NextAttempt time.Time
	Failure     DeliveryFailureClass
	DIMSEStatus uint16
}

type DeliveryStore interface {
	ListDeliveries(context.Context, DeliveryQuery) ([]Delivery, error)
	ClaimDueDeliveries(context.Context, time.Time, int, time.Duration) ([]Delivery, error)
	CompleteDelivery(context.Context, string, string, DeliveryOutcome) (Delivery, error)
}

type DeliveryLimits struct {
	MaxBatch       int
	MaxAttempts    int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	AttemptTimeout time.Duration
	LeaseDuration  time.Duration
	CleanupTimeout time.Duration
}

type CallbackRequest struct {
	ReceivingAETitle string
}

type CallbackTarget struct {
	Address     string
	DialOptions ul.DialOptions
}

type CallbackResolver interface {
	ResolveUPSCallback(context.Context, CallbackRequest) (CallbackTarget, error)
}

type CallbackResolverFunc func(context.Context, CallbackRequest) (CallbackTarget, error)

func (function CallbackResolverFunc) ResolveUPSCallback(ctx context.Context, request CallbackRequest) (CallbackTarget, error) {
	if function == nil {
		return CallbackTarget{}, ErrDeliveryFailed
	}
	return function(ctx, request)
}

type AssociationDialer interface {
	DialUPS(context.Context, string, ul.DialOptions) (*ul.Association, error)
}

type AssociationDialerFunc func(context.Context, string, ul.DialOptions) (*ul.Association, error)

func (function AssociationDialerFunc) DialUPS(ctx context.Context, address string, options ul.DialOptions) (*ul.Association, error) {
	if function == nil {
		return nil, ErrDeliveryFailed
	}
	return function(ctx, address, options)
}

type defaultAssociationDialer struct{}

func (defaultAssociationDialer) DialUPS(ctx context.Context, address string, options ul.DialOptions) (*ul.Association, error) {
	return ul.DialContext(ctx, address, options)
}

type DeliveryError struct {
	Class     DeliveryFailureClass
	Status    uint16
	Retryable bool
	Err       error
}

func (err *DeliveryError) Error() string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf("dicom ups: event delivery failed (%s)", err.Class)
}

func (err *DeliveryError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

func (err *DeliveryError) Is(target error) bool {
	return target == ErrDeliveryFailed
}

func cloneTLSConfig(config *tls.Config) *tls.Config {
	if config == nil {
		return nil
	}
	return config.Clone()
}

func normalizeDeliveryLimits(limits DeliveryLimits) (DeliveryLimits, error) {
	if limits.MaxBatch < 0 || limits.MaxAttempts < 0 || limits.InitialBackoff < 0 || limits.MaxBackoff < 0 || limits.AttemptTimeout < 0 || limits.LeaseDuration < 0 || limits.CleanupTimeout < 0 {
		return DeliveryLimits{}, ErrResourceLimit
	}
	if limits.MaxBatch == 0 {
		limits.MaxBatch = 256
	}
	if limits.MaxAttempts == 0 {
		limits.MaxAttempts = 5
	}
	if limits.InitialBackoff == 0 {
		limits.InitialBackoff = time.Second
	}
	if limits.MaxBackoff == 0 {
		limits.MaxBackoff = time.Minute
	}
	if limits.AttemptTimeout == 0 {
		limits.AttemptTimeout = 20 * time.Second
	}
	if limits.LeaseDuration == 0 {
		limits.LeaseDuration = 30 * time.Second
	}
	if limits.CleanupTimeout == 0 {
		limits.CleanupTimeout = 5 * time.Second
	}
	if limits.MaxBackoff < limits.InitialBackoff || limits.AttemptTimeout <= 0 || limits.LeaseDuration <= 0 || limits.CleanupTimeout <= 0 ||
		limits.AttemptTimeout >= limits.LeaseDuration || limits.CleanupTimeout >= limits.LeaseDuration-limits.AttemptTimeout {
		return DeliveryLimits{}, ErrResourceLimit
	}
	return limits, nil
}

func (service *Service) Subscribe(ctx context.Context, request SubscribeRequest) (SubscribeResult, error) {
	ctx = normalizeContext(ctx)
	request.ReceivingAETitle = strings.TrimSpace(request.ReceivingAETitle)
	if request.SOPInstanceUID == FilteredGlobalSubscriptionSOPInstanceUID {
		return SubscribeResult{}, statusError("N-ACTION Subscribe", StatusUPSNotFound, ErrInvalidDataSet)
	}
	if len(request.MatchingKeys) != 0 {
		status := StatusActionNotAppropriate
		if request.SOPInstanceUID == GlobalSubscriptionSOPInstanceUID || request.SOPInstanceUID == FilteredGlobalSubscriptionSOPInstanceUID {
			status = StatusUPSNotFound
		}
		return SubscribeResult{}, statusError("N-ACTION Subscribe", status, ErrInvalidDataSet)
	}
	if err := service.validateSubscriber(ctx, request.ReceivingAETitle); err != nil {
		return SubscribeResult{}, err
	}
	if request.SOPInstanceUID != GlobalSubscriptionSOPInstanceUID {
		if _, err := service.store.GetStep(ctx, request.SOPInstanceUID); errors.Is(err, ErrNotFound) {
			return SubscribeResult{}, statusError("N-ACTION Subscribe", StatusUPSNotFound, err)
		} else if err != nil {
			return SubscribeResult{}, safeRepositoryError(err)
		}
	}
	status := StatusSuccess
	deletionLock := request.DeletionLock
	if deletionLock && service.refuseDeletionLocks {
		deletionLock = false
		status = StatusDeletionLockNotGranted
	}
	result, err := service.store.CommitUPS(ctx, CommitRequest{Subscription: &SubscriptionMutation{
		Kind: SubscriptionMutationSubscribe, SOPInstanceUID: request.SOPInstanceUID,
		ReceivingAETitle: request.ReceivingAETitle, DeletionLock: deletionLock, UpdatedAt: service.clock().UTC(),
	}})
	if err != nil {
		return SubscribeResult{}, safeRepositoryError(err)
	}
	if result.Subscription == nil {
		return SubscribeResult{}, safeRepositoryError(ErrConflict)
	}
	return SubscribeResult{Subscription: *result.Subscription, Status: status}, nil
}

func (service *Service) Unsubscribe(ctx context.Context, request UnsubscribeRequest) error {
	ctx = normalizeContext(ctx)
	request.ReceivingAETitle = strings.TrimSpace(request.ReceivingAETitle)
	if !validAETitle(request.ReceivingAETitle) {
		return statusError("N-ACTION Unsubscribe", StatusReceivingAEUnknown, ErrInvalidDataSet)
	}
	if request.SOPInstanceUID == "" {
		return statusError("N-ACTION Unsubscribe", StatusActionNotAppropriate, ErrInvalidDataSet)
	}
	if request.SOPInstanceUID == FilteredGlobalSubscriptionSOPInstanceUID {
		return statusError("N-ACTION Unsubscribe", StatusUPSNotFound, ErrNotFound)
	}
	if request.SOPInstanceUID != GlobalSubscriptionSOPInstanceUID {
		if _, err := service.store.GetStep(ctx, request.SOPInstanceUID); errors.Is(err, ErrNotFound) {
			return statusError("N-ACTION Unsubscribe", StatusUPSNotFound, err)
		} else if err != nil {
			return safeRepositoryError(err)
		}
	}
	_, err := service.store.CommitUPS(ctx, CommitRequest{Subscription: &SubscriptionMutation{
		Kind: SubscriptionMutationUnsubscribe, SOPInstanceUID: request.SOPInstanceUID,
		ReceivingAETitle: request.ReceivingAETitle, UpdatedAt: service.clock().UTC(),
	}})
	if err != nil {
		return safeRepositoryError(err)
	}
	return nil
}

func (service *Service) SuspendGlobal(ctx context.Context, receivingAETitle string) error {
	ctx = normalizeContext(ctx)
	receivingAETitle = strings.TrimSpace(receivingAETitle)
	if !validAETitle(receivingAETitle) {
		return statusError("N-ACTION Suspend Global", StatusReceivingAEUnknown, ErrInvalidDataSet)
	}
	_, err := service.store.CommitUPS(ctx, CommitRequest{Subscription: &SubscriptionMutation{
		Kind: SubscriptionMutationSuspendGlobal, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID,
		ReceivingAETitle: receivingAETitle, UpdatedAt: service.clock().UTC(),
	}})
	if err != nil {
		return safeRepositoryError(err)
	}
	return nil
}

func (service *Service) Subscription(ctx context.Context, sopInstanceUID, receivingAETitle string) (Subscription, error) {
	subscription, err := service.store.GetSubscription(normalizeContext(ctx), sopInstanceUID, receivingAETitle)
	if err != nil {
		return Subscription{}, safeRepositoryError(err)
	}
	return subscription, nil
}

func (service *Service) Deliveries(ctx context.Context, query DeliveryQuery) ([]Delivery, error) {
	if query.Limit == 0 {
		query.Limit = 1_000
	}
	if query.Limit < 0 || query.Limit > 1_000 {
		return nil, ErrResourceLimit
	}
	deliveries, err := service.store.ListDeliveries(normalizeContext(ctx), query)
	if err != nil {
		return nil, safeRepositoryError(err)
	}
	return deliveries, nil
}

func (service *Service) DeliverDue(ctx context.Context, limit int) error {
	ctx = normalizeContext(ctx)
	if limit <= 0 || limit > service.deliveryLimits.MaxBatch {
		return ErrResourceLimit
	}
	var deliveryErrors []error
	for processed := 0; processed < limit; processed++ {
		deliveries, err := service.store.ClaimDueDeliveries(ctx, service.clock().UTC(), 1, service.deliveryLimits.LeaseDuration)
		if err != nil {
			deliveryErrors = append(deliveryErrors, safeRepositoryError(err))
			break
		}
		if len(deliveries) == 0 {
			break
		}
		delivery := deliveries[0]
		if delivery.Attempts > service.deliveryLimits.MaxAttempts {
			persistCtx, cancel := context.WithTimeout(context.Background(), service.deliveryLimits.CleanupTimeout)
			_, persistErr := service.store.CompleteDelivery(persistCtx, delivery.ID, delivery.LeaseToken, DeliveryOutcome{
				Failure: DeliveryFailureProtocol, Retryable: false,
			})
			cancel()
			if persistErr != nil {
				deliveryErrors = append(deliveryErrors, safeRepositoryError(persistErr))
			}
			continue
		}
		attemptCtx, attemptCancel := context.WithTimeout(ctx, service.deliveryLimits.AttemptTimeout)
		outcome, attemptErr := service.attemptDelivery(attemptCtx, delivery)
		attemptCancel()
		persistCtx, cancel := context.WithTimeout(context.Background(), service.deliveryLimits.CleanupTimeout)
		_, persistErr := service.store.CompleteDelivery(persistCtx, delivery.ID, delivery.LeaseToken, outcome)
		cancel()
		if persistErr != nil {
			deliveryErrors = append(deliveryErrors, safeRepositoryError(persistErr))
			continue
		}
		if attemptErr != nil {
			deliveryErrors = append(deliveryErrors, attemptErr)
		}
	}
	return errors.Join(deliveryErrors...)
}

// ReportSCPStatusChange durably queues the restart/shutdown event for the
// configured fallback AEs and every currently active subscriber. Duplicate AEs
// are collapsed before the atomic outbox commit.
func (service *Service) ReportSCPStatusChange(ctx context.Context, change SCPStatusChange) error {
	ctx = normalizeContext(ctx)
	if service == nil || !validSCPStatusChange(change) {
		return ErrInvalidDataSet
	}
	receivingAEs, err := service.store.ListActiveReceivingAETitles(ctx, service.limits.MaxStatusRecipients+1)
	if err != nil {
		return safeRepositoryError(err)
	}
	if len(receivingAEs) > service.limits.MaxStatusRecipients {
		return ErrResourceLimit
	}
	recipients := make(map[string]bool, len(service.fallbackReceivingAEs)+len(receivingAEs))
	for _, ae := range service.fallbackReceivingAEs {
		recipients[ae] = true
	}
	for _, ae := range receivingAEs {
		recipients[ae] = true
	}
	if len(recipients) == 0 {
		return ErrConflict
	}
	if len(recipients) > service.limits.MaxStatusRecipients {
		return ErrResourceLimit
	}
	information := core.DataSet{Elements: []core.Element{
		StringElement(TagSCPStatus, core.VRCS, string(change.Status)),
		StringElement(TagSubscriptionListStatus, core.VRCS, string(change.SubscriptionListStatus)),
		StringElement(TagUnifiedProcedureStepListStatus, core.VRCS, string(change.UPSListStatus)),
	}}
	aes := make([]string, 0, len(recipients))
	for ae := range recipients {
		aes = append(aes, ae)
	}
	sort.Strings(aes)
	events := make([]Event, 0, len(aes))
	for _, ae := range aes {
		events = append(events, Event{
			Type: EventSCPStatusChange, SOPInstanceUID: GlobalSubscriptionSOPInstanceUID,
			Information: information, DirectReceivingAE: ae, CreatedAt: service.clock().UTC(),
		})
	}
	_, err = service.store.CommitUPS(ctx, CommitRequest{Events: events})
	if err != nil {
		return safeRepositoryError(err)
	}
	return nil
}

func validSCPStatusChange(change SCPStatusChange) bool {
	if change.Status != SCPStatusRestarted && change.Status != SCPStatusGoingDown {
		return false
	}
	if !validCSValue(string(change.SubscriptionListStatus)) || !validCSValue(string(change.UPSListStatus)) {
		return false
	}
	if change.Status != SCPStatusRestarted {
		return true
	}
	validSubscriptions := change.SubscriptionListStatus == ListStatusWarmStart || change.SubscriptionListStatus == ListStatusColdStart
	validUPS := change.UPSListStatus == ListStatusWarmStart || change.UPSListStatus == ListStatusColdStart
	return validSubscriptions && validUPS
}

func validCSValue(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 16 || value != strings.ToUpper(value) || strings.ContainsRune(value, '\\') {
		return false
	}
	for _, character := range value {
		if character != ' ' && character != '_' && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func (service *Service) attemptDelivery(ctx context.Context, delivery Delivery) (DeliveryOutcome, error) {
	if service.callbackResolver == nil {
		return service.failureOutcome(delivery, DeliveryFailureCallbackUnknown, true, 0), &DeliveryError{Class: DeliveryFailureCallbackUnknown, Retryable: true, Err: ErrDeliveryFailed}
	}
	target, err := service.callbackResolver.ResolveUPSCallback(ctx, CallbackRequest{ReceivingAETitle: delivery.ReceivingAETitle})
	if err != nil || strings.TrimSpace(target.Address) == "" {
		return service.failureOutcome(delivery, DeliveryFailureCallbackUnknown, true, 0), &DeliveryError{Class: DeliveryFailureCallbackUnknown, Retryable: true, Err: err}
	}
	target.DialOptions, err = cloneUPSDialOptions(target.DialOptions)
	if err != nil {
		return service.failureOutcome(delivery, DeliveryFailureProtocol, false, 0), &DeliveryError{Class: DeliveryFailureProtocol, Err: err}
	}
	if strings.TrimSpace(target.DialOptions.CalledAETitle) == "" {
		target.DialOptions.CalledAETitle = delivery.ReceivingAETitle
	}
	if strings.TrimSpace(target.DialOptions.CallingAETitle) == "" {
		target.DialOptions.CallingAETitle = service.callbackCallingAE
	}
	assoc, err := service.associationDialer.DialUPS(ctx, target.Address, target.DialOptions)
	if err != nil {
		if assoc != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), service.deliveryLimits.CleanupTimeout)
			_ = assoc.AbortWithContext(cleanupCtx, ul.AbortReasonNotSpecified)
			cancel()
			_ = assoc.Close()
		}
		class := classifyDeliveryFailure(err)
		return service.failureOutcome(delivery, class, true, 0), &DeliveryError{Class: class, Retryable: true, Err: err}
	}
	if assoc == nil {
		return service.failureOutcome(delivery, DeliveryFailureCallbackOffline, true, 0), &DeliveryError{Class: DeliveryFailureCallbackOffline, Retryable: true, Err: ErrDeliveryFailed}
	}
	dataSet := object.FromDataSet(delivery.Event.Information, std.Dictionary)
	client := dimse.NewNormalizedClient(assoc)
	result, sendErr := client.EventReportWithOptions(dimse.NormalizedOperationOptions{
		OperationOptions:                     dimse.OperationOptions{Context: ctx},
		PresentationContextAbstractSyntaxUID: EventSOPClassUID,
		MaxResponseDataSetBytes:              service.limits.MaxDataSetBytes,
	}, dimse.NormalizedEventReportRequest{
		AffectedSOPClassUID: PushSOPClassUID, AffectedSOPInstanceUID: delivery.SOPInstanceUID,
		CommandDataSetType: dimse.DataSetPresent, EventTypeID: uint16(delivery.EventType),
	}, dataSet)
	if sendErr != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), service.deliveryLimits.CleanupTimeout)
		_ = assoc.AbortWithContext(cleanupCtx, ul.AbortReasonNotSpecified)
		cancel()
		_ = assoc.Close()
		status := uint16(0)
		class := classifyDeliveryFailure(sendErr)
		var statusErr *dimse.NormalizedStatusError
		if errors.As(sendErr, &statusErr) {
			status = statusErr.Status
			class = DeliveryFailureDIMSEStatus
		}
		return service.failureOutcome(delivery, class, class != DeliveryFailureDIMSEStatus, status), &DeliveryError{Class: class, Status: status, Retryable: class != DeliveryFailureDIMSEStatus, Err: sendErr}
	}
	_ = result
	cleanupCtx, cancel := context.WithTimeout(context.Background(), service.deliveryLimits.CleanupTimeout)
	_ = assoc.Release(cleanupCtx)
	cancel()
	_ = assoc.Close()
	return DeliveryOutcome{Delivered: true}, nil
}

func (service *Service) failureOutcome(delivery Delivery, class DeliveryFailureClass, retryable bool, status uint16) DeliveryOutcome {
	nextAttempts := delivery.Attempts
	if nextAttempts < 1 {
		nextAttempts = 1
	}
	if nextAttempts >= service.deliveryLimits.MaxAttempts {
		retryable = false
	}
	backoff := service.deliveryLimits.InitialBackoff
	for attempt := 1; attempt < nextAttempts && backoff < service.deliveryLimits.MaxBackoff; attempt++ {
		if backoff > service.deliveryLimits.MaxBackoff/2 {
			backoff = service.deliveryLimits.MaxBackoff
			break
		}
		backoff *= 2
	}
	if backoff > service.deliveryLimits.MaxBackoff {
		backoff = service.deliveryLimits.MaxBackoff
	}
	return DeliveryOutcome{Retryable: retryable, NextAttempt: service.clock().UTC().Add(backoff), Failure: class, DIMSEStatus: status}
}

func (service *Service) validateSubscriber(ctx context.Context, receivingAETitle string) error {
	if strings.TrimSpace(receivingAETitle) == "" || len(receivingAETitle) > 16 || strings.ContainsRune(receivingAETitle, '\\') {
		return statusError("N-ACTION Subscription", StatusReceivingAEUnknown, ErrInvalidDataSet)
	}
	if service.callbackResolver == nil {
		return statusError("N-ACTION Subscription", StatusEventReportsUnsupported, ErrDeliveryFailed)
	}
	target, err := service.callbackResolver.ResolveUPSCallback(ctx, CallbackRequest{ReceivingAETitle: receivingAETitle})
	if err != nil || strings.TrimSpace(target.Address) == "" {
		return statusError("N-ACTION Subscription", StatusReceivingAEUnknown, err)
	}
	return nil
}

func cloneUPSDialOptions(options ul.DialOptions) (ul.DialOptions, error) {
	options.TLSConfig = cloneTLSConfig(options.TLSConfig)
	contexts := make([]ul.PresentationContext, len(options.Contexts))
	hasEvent := false
	for index, context := range options.Contexts {
		contexts[index] = context
		contexts[index].TransferSyntaxUIDs = append([]string(nil), context.TransferSyntaxUIDs...)
		hasEvent = hasEvent || context.AbstractSyntaxUID == EventSOPClassUID
	}
	if !hasEvent {
		if len(contexts) == ul.MaxPresentationContexts {
			return ul.DialOptions{}, ErrResourceLimit
		}
		context, err := PresentationContext(EventSOPClassUID)
		if err != nil {
			return ul.DialOptions{}, err
		}
		contexts = append(contexts, context)
	}
	options.Contexts = contexts
	options.RoleSelections = append([]ul.RoleSelectionItem(nil), options.RoleSelections...)
	options.ExtendedNegotiation = append([]ul.SopClassExtendedNegotiationItem(nil), options.ExtendedNegotiation...)
	for index := range options.ExtendedNegotiation {
		options.ExtendedNegotiation[index].Data = append([]byte(nil), options.ExtendedNegotiation[index].Data...)
	}
	if options.UserIdentity != nil {
		identity := *options.UserIdentity
		identity.PrimaryField = append([]byte(nil), identity.PrimaryField...)
		identity.SecondaryField = append([]byte(nil), identity.SecondaryField...)
		options.UserIdentity = &identity
	}
	if options.AsynchronousOperationsWindow != nil {
		window := *options.AsynchronousOperationsWindow
		options.AsynchronousOperationsWindow = &window
	}
	return options, nil
}

func classifyDeliveryFailure(err error) DeliveryFailureClass {
	if errors.Is(err, context.Canceled) {
		return DeliveryFailureCanceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return DeliveryFailureTimeout
	}
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var hostname x509.HostnameError
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &unknownAuthority) || errors.As(err, &certificateInvalid) || errors.As(err, &hostname) || errors.As(err, &recordHeader) {
		return DeliveryFailureTLS
	}
	var networkError net.Error
	if errors.As(err, &networkError) {
		if networkError.Timeout() {
			return DeliveryFailureTimeout
		}
		return DeliveryFailureCallbackOffline
	}
	return DeliveryFailureProtocol
}
