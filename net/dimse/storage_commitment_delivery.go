package dimse

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

// StorageCommitmentCallbackRequest contains only protocol identifiers needed
// for an application-owned, allowlisted callback lookup. Implementations must
// not derive an address directly from untrusted AE text.
type StorageCommitmentCallbackRequest struct {
	TransactionUID   string
	RequestorAETitle string
	CommitterAETitle string
}

// StorageCommitmentCallbackTarget freezes the transport and identity policy
// for one callback association.
type StorageCommitmentCallbackTarget struct {
	Address     string
	DialOptions ul.DialOptions
}

type StorageCommitmentCallbackResolver interface {
	ResolveStorageCommitmentCallback(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error)
}

type StorageCommitmentCallbackResolverFunc func(context.Context, StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error)

func (f StorageCommitmentCallbackResolverFunc) ResolveStorageCommitmentCallback(ctx context.Context, request StorageCommitmentCallbackRequest) (StorageCommitmentCallbackTarget, error) {
	if f == nil {
		return StorageCommitmentCallbackTarget{}, ErrStorageCommitmentCallbackUnknown
	}
	return f(ctx, request)
}

// StorageCommitmentAssociationDialer is injectable for deterministic retry and
// TLS tests. Associations returned by DialStorageCommitment are owned by the
// workflow.
type StorageCommitmentAssociationDialer interface {
	DialStorageCommitment(context.Context, string, ul.DialOptions) (*ul.Association, error)
}

type StorageCommitmentAssociationDialerFunc func(context.Context, string, ul.DialOptions) (*ul.Association, error)

func (f StorageCommitmentAssociationDialerFunc) DialStorageCommitment(ctx context.Context, address string, options ul.DialOptions) (*ul.Association, error) {
	if f == nil {
		return nil, ErrStorageCommitmentCallbackUnknown
	}
	return f(ctx, address, options)
}

type storageCommitmentDefaultDialer struct{}

func (storageCommitmentDefaultDialer) DialStorageCommitment(ctx context.Context, address string, options ul.DialOptions) (*ul.Association, error) {
	return ul.DialContext(ctx, address, options)
}

// StorageCommitmentDeliveryError is PHI-free while preserving the underlying
// cause for errors.Is/As. The cause must never be copied to persistence or
// implicit retry logs.
type StorageCommitmentDeliveryError struct {
	Class     StorageCommitmentFailureClass
	Status    uint16
	Retryable bool
	Err       error
}

func (e *StorageCommitmentDeliveryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("dicom dimse: storage commitment delivery failed (%s)", e.Class)
}

func (e *StorageCommitmentDeliveryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// NormalizedOptions returns dedicated Storage Commitment N-ACTION and
// N-EVENT-REPORT handlers for an established association. The association is
// borrowed and supplies the peer AE identity used for correlation.
func (w *StorageCommitmentWorkflow) NormalizedOptions(assoc *ul.Association) NormalizedSCPOptions {
	return NormalizedSCPOptions{
		MaxDataSetBytes: w.limits.MaxDataSetBytes,
		PresentationContextPolicy: func(pc ul.AcceptedContext, commandSOPClassUID string) error {
			if pc.AbstractSyntaxUID != StorageCommitmentPushModelSOPClassUID || commandSOPClassUID != StorageCommitmentPushModelSOPClassUID {
				return ErrPresentationContextMismatch
			}
			return nil
		},
		ActionHandler: func(ctx context.Context, request NormalizedActionRequest, dataSet *object.Object) (NormalizedActionSCPResult, error) {
			status := StatusSuccess
			if commandStatus, err := validateStorageCommitmentActionCommandStatus(request); err != nil || !storageCommitmentRequestorCanActAsSCU(assoc) {
				status = commandStatus
				if err == nil {
					status = StatusStorageCommitmentProcessingFailure
				}
				return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: status}}, ErrStorageCommitmentInvalidRequest
			}
			information, err := ParseStorageCommitmentActionInformation(dataSet)
			if err != nil {
				if errors.Is(err, ErrStorageCommitmentResourceLimit) {
					return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: StatusStorageCommitmentProcessingFailure}}, err
				}
				return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: StatusStorageCommitmentProcessingFailure}}, ErrStorageCommitmentInvalidRequest
			}
			transaction, err := w.acceptRequest(ctx, assoc, request.MessageID, information)
			if err != nil {
				status = StatusStorageCommitmentProcessingFailure
				return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: status}}, err
			}
			stopHeartbeat := w.startCommitterAcceptanceHeartbeat(ctx, transaction)
			return NormalizedActionSCPResult{Response: NormalizedActionResponse{Status: status}}, &normalizedActionResponseHook{
				after: func(afterCtx context.Context, response NormalizedActionResponse, responseSent bool) error {
					stopHeartbeat()
					if !responseSent || response.Status != StatusSuccess {
						return nil
					}
					_, afterErr := w.markCommitterAccepted(afterCtx, transaction.TransactionUID)
					return afterErr
				},
			}
		},
		EventReportHandler: func(ctx context.Context, request NormalizedEventReportRequest, dataSet *object.Object) (NormalizedEventReportSCPResult, error) {
			status := StatusSuccess
			if commandStatus, err := validateStorageCommitmentEventCommandStatus(request); err != nil {
				return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{Status: commandStatus}}, err
			}
			information, err := ParseStorageCommitmentEventInformation(dataSet)
			if err != nil {
				if errors.Is(err, ErrStorageCommitmentResourceLimit) {
					return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{Status: StatusStorageCommitmentProcessingFailure}}, err
				}
				return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{Status: StatusStorageCommitmentProcessingFailure}}, ErrStorageCommitmentInvalidResult
			}
			result, err := storageCommitmentResultFromEvent(request, information)
			if err == nil {
				err = w.receiveResult(ctx, assoc, result)
			}
			if err != nil {
				status = StatusStorageCommitmentProcessingFailure
			}
			return NormalizedEventReportSCPResult{Response: NormalizedEventReportResponse{Status: status}}, err
		},
	}
}

// ServeAction receives one N-ACTION on a borrowed association, durably creates
// the transaction, sends N-ACTION-RSP, and returns only after the response has
// been written. This ordering lets the caller safely process and then use
// DeliverOnAssociation without a competing receive loop.
func (w *StorageCommitmentWorkflow) ServeAction(ctx context.Context, assoc *ul.Association, pcID byte) (StorageCommitmentTransaction, error) {
	if !storageCommitmentRequestorCanActAsSCU(assoc) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	var accepted StorageCommitmentTransaction
	stopHeartbeat := context.CancelFunc(func() {})
	defer func() { stopHeartbeat() }()
	err := ServeStorageCommitmentSCPWithOptions(ctx, assoc, pcID, StorageCommitmentActionHandlerFunc(func(handlerCtx context.Context, request StorageCommitmentActionContext) (StorageCommitmentActionResult, error) {
		information := StorageCommitmentActionInformation{
			TransactionUID: request.TransactionUID,
			ReferencedSOPs: request.ReferencedSOPs,
		}
		transaction, err := w.acceptRequest(handlerCtx, assoc, request.Request.MessageID, information)
		if err != nil {
			return StorageCommitmentActionResult{Status: StatusStorageCommitmentProcessingFailure}, err
		}
		accepted = transaction
		stopHeartbeat = w.startCommitterAcceptanceHeartbeat(handlerCtx, transaction)
		return StorageCommitmentActionResult{Status: StatusSuccess}, nil
	}), StorageCommitmentSCPOptions{MaxDataSetBytes: w.limits.MaxDataSetBytes})
	if err != nil {
		return accepted, err
	}
	return w.markCommitterAccepted(ctx, accepted.TransactionUID)
}

// ServeAssociation handles Storage Commitment normalized commands on one
// borrowed association until release. It is the sole reader while active.
func (w *StorageCommitmentWorkflow) ServeAssociation(ctx context.Context, assoc *ul.Association, controls SCPControls) error {
	if assoc == nil {
		return ErrStorageCommitmentInvalidRequest
	}
	options := w.NormalizedOptions(assoc)
	return ServeAssociation(ctx, assoc, AssociationSCPOptions{Controls: controls, NormalizedSCP: &options})
}

// StorageCommitmentListenerOptions configures a bounded callback receiver. The
// listener remains owned by the caller.
type StorageCommitmentListenerOptions struct {
	Accept                    ul.AcceptOptions
	Controls                  SCPControls
	MaxConcurrentAssociations int
	OnError                   func(error)
}

// ServeListener accepts callback associations until ctx is canceled. Every
// accepted association is owned, released by the peer or closed by this loop.
func (w *StorageCommitmentWorkflow) ServeListener(ctx context.Context, listener *ul.Listener, options StorageCommitmentListenerOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if listener == nil || options.MaxConcurrentAssociations < 0 {
		return ErrStorageCommitmentInvalidRequest
	}
	maxConcurrent := options.MaxConcurrentAssociations
	if maxConcurrent == 0 {
		maxConcurrent = 8
	}
	acceptOptions := cloneStorageCommitmentAcceptOptions(options.Accept)
	acceptOptions.Context = ctx
	acceptOptions.SupportedAbstractSyntaxes = []string{StorageCommitmentPushModelSOPClassUID}
	acceptOptions.SupportedTransferSyntaxes = []string{ul.ExplicitVRLittleEndian, ul.ImplicitVRLittleEndian}
	acceptOptions.RoleSelections = []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}}
	sem := make(chan struct{}, maxConcurrent)
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
		assoc, err := listener.AcceptAssociation(acceptOptions)
		if err != nil {
			<-sem
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProtocol, err)
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-sem }()
			defer func() { _ = assoc.Close() }()
			serveErr := w.ServeAssociation(ctx, assoc, options.Controls)
			if serveErr != nil && ctx.Err() == nil && options.OnError != nil {
				callStorageCommitmentErrorCallback(options.OnError, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProtocol, serveErr))
			}
		}()
	}
}

func cloneStorageCommitmentAcceptOptions(options ul.AcceptOptions) ul.AcceptOptions {
	options.SupportedAbstractSyntaxes = append([]string(nil), options.SupportedAbstractSyntaxes...)
	options.SupportedTransferSyntaxes = append([]string(nil), options.SupportedTransferSyntaxes...)
	options.RoleSelections = append([]ul.RoleSelectionItem(nil), options.RoleSelections...)
	if options.TLSConfig != nil {
		options.TLSConfig = options.TLSConfig.Clone()
	}
	return options
}

func callStorageCommitmentErrorCallback(callback func(error), err error) {
	defer func() { _ = recover() }()
	callback(err)
}

func validateStorageCommitmentActionCommand(request NormalizedActionRequest) error {
	_, err := validateStorageCommitmentActionCommandStatus(request)
	return err
}

func validateStorageCommitmentActionCommandStatus(request NormalizedActionRequest) (uint16, error) {
	if request.RequestedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return StatusNoSuchSOPClass, ErrStorageCommitmentInvalidRequest
	}
	if request.RequestedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return StatusNoSuchSOPInstance, ErrStorageCommitmentInvalidRequest
	}
	if request.ActionTypeID != StorageCommitmentActionTypeID {
		return StatusNoSuchActionType, ErrStorageCommitmentInvalidRequest
	}
	if !normalizedHasDataSet(request.CommandDataSetType) {
		return StatusStorageCommitmentProcessingFailure, ErrStorageCommitmentInvalidRequest
	}
	return StatusSuccess, nil
}

func validateStorageCommitmentEventCommand(request NormalizedEventReportRequest) error {
	_, err := validateStorageCommitmentEventCommandStatus(request)
	return err
}

func validateStorageCommitmentEventCommandStatus(request NormalizedEventReportRequest) (uint16, error) {
	if request.AffectedSOPClassUID != StorageCommitmentPushModelSOPClassUID {
		return StatusNoSuchSOPClass, ErrStorageCommitmentInvalidResult
	}
	if request.AffectedSOPInstanceUID != StorageCommitmentPushModelSOPInstanceUID {
		return StatusNoSuchSOPInstance, ErrStorageCommitmentInvalidResult
	}
	if !validStorageCommitmentEventType(request.EventTypeID) {
		return StatusNoSuchEventType, ErrStorageCommitmentInvalidResult
	}
	if !normalizedHasDataSet(request.CommandDataSetType) {
		return StatusStorageCommitmentProcessingFailure, ErrStorageCommitmentInvalidResult
	}
	return StatusSuccess, nil
}

func (w *StorageCommitmentWorkflow) acceptRequest(ctx context.Context, assoc *ul.Association, messageID uint16, information StorageCommitmentActionInformation) (StorageCommitmentTransaction, error) {
	references, err := normalizeStorageCommitmentReferences(information.ReferencedSOPs, false, w.limits.MaxReferences)
	if errors.Is(err, ErrStorageCommitmentResourceLimit) {
		return StorageCommitmentTransaction{}, err
	}
	if err != nil || !validStoreUID(information.TransactionUID) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentInvalidRequest
	}
	now := w.now()
	acceptanceLeaseToken, err := newStorageCommitmentLeaseToken()
	if err != nil {
		return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	transaction := StorageCommitmentTransaction{
		TransactionUID:   information.TransactionUID,
		Direction:        StorageCommitmentDirectionCommitter,
		State:            StorageCommitmentStateAcceptancePrepared,
		DeliveryMode:     w.defaultDeliveryMode,
		ReferencedSOPs:   references,
		RequestMessageID: messageID,
		CreatedAt:        now, UpdatedAt: now, NextAttemptAt: now.Add(2 * w.leaseDuration),
		LeaseToken: acceptanceLeaseToken, LeaseUntil: now.Add(w.leaseDuration),
	}
	if assoc != nil {
		transaction.RequestorAETitle = strings.TrimSpace(assoc.CallingAETitle)
		transaction.CommitterAETitle = strings.TrimSpace(assoc.CalledAETitle)
	}
	transaction.RequestDigest = storageCommitmentRequestDigest(transaction)
	created, err := w.storeCreate(ctx, transaction)
	if err != nil {
		return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	if !created {
		existing, getErr := w.storeGet(ctx, transaction.TransactionUID)
		if getErr != nil {
			return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, getErr)
		}
		if !sameStorageCommitmentRequest(existing, transaction) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		if existing.State == StorageCommitmentStateRequestRejected {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		return existing, nil
	}
	return transaction, nil
}

func (w *StorageCommitmentWorkflow) markCommitterAccepted(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
	defer cancelPersist()
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(persistCtx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
		}
		if current.Direction != StorageCommitmentDirectionCommitter {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		if current.State != StorageCommitmentStateAcceptancePrepared {
			if current.State == StorageCommitmentStateRequestRejected {
				return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
			}
			return current, nil
		}
		next := cloneStorageCommitmentTransaction(current)
		next.State = StorageCommitmentStateAccepted
		next.UpdatedAt = w.now()
		next.NextAttemptAt = next.UpdatedAt
		next.LeaseToken = ""
		next.LeaseUntil = time.Time{}
		accepted, err := w.storeCompareAndSwap(persistCtx, current.TransactionUID, current.Version, next)
		if errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		}
		if err != nil {
			return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
		}
		return accepted, nil
	}
	return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
}

func (w *StorageCommitmentWorkflow) recoverCommitterAcceptance(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(ctx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, err
		}
		if current.Direction != StorageCommitmentDirectionCommitter {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		if current.State != StorageCommitmentStateAcceptancePrepared {
			return current, nil
		}
		if !current.LeaseUntil.IsZero() && current.LeaseUntil.Add(w.leaseDuration).After(w.now()) {
			return current, ErrStorageCommitmentConcurrentUpdate
		}
		next := cloneStorageCommitmentTransaction(current)
		next.State = StorageCommitmentStateAccepted
		next.UpdatedAt = w.now()
		next.NextAttemptAt = next.UpdatedAt
		next.LeaseToken = ""
		next.LeaseUntil = time.Time{}
		accepted, err := w.storeCompareAndSwap(ctx, current.TransactionUID, current.Version, next)
		if errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		}
		return accepted, err
	}
	return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
}

func (w *StorageCommitmentWorkflow) startCommitterAcceptanceHeartbeat(ctx context.Context, transaction StorageCommitmentTransaction) context.CancelFunc {
	if transaction.State != StorageCommitmentStateAcceptancePrepared || transaction.LeaseToken == "" {
		return func() {}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	heartbeatCtx, cancel := context.WithCancel(ctx)
	interval := w.leaseDuration / 3
	if interval <= 0 {
		interval = time.Nanosecond
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-heartbeatCtx.Done():
				return
			case <-ticker.C:
				if !w.renewCommitterAcceptance(heartbeatCtx, transaction.TransactionUID, transaction.LeaseToken) {
					return
				}
			}
		}
	}()
	return cancel
}

func (w *StorageCommitmentWorkflow) renewCommitterAcceptance(ctx context.Context, transactionUID, leaseToken string) bool {
	persistCtx, cancelPersist := context.WithTimeout(ctx, min(w.leaseDuration/2, time.Second))
	defer cancelPersist()
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(persistCtx, transactionUID)
		if err != nil || current.State != StorageCommitmentStateAcceptancePrepared || current.LeaseToken != leaseToken {
			return false
		}
		now := w.now()
		next := cloneStorageCommitmentTransaction(current)
		next.UpdatedAt = now
		next.LeaseUntil = now.Add(w.leaseDuration)
		next.NextAttemptAt = now.Add(2 * w.leaseDuration)
		if _, err := w.storeCompareAndSwap(persistCtx, current.TransactionUID, current.Version, next); errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		} else {
			return err == nil
		}
	}
	return false
}

// SetResult validates and durably stores the exact result partition before any
// delivery attempt. Repeating an identical result is idempotent.
func (w *StorageCommitmentWorkflow) SetResult(ctx context.Context, result StorageCommitmentResult) (StorageCommitmentTransaction, error) {
	return w.setResult(ctx, result, "", false)
}

func (w *StorageCommitmentWorkflow) setResult(ctx context.Context, result StorageCommitmentResult, processingLeaseToken string, allowPendingRequest bool) (StorageCommitmentTransaction, error) {
	transaction, err := w.storeGet(ctx, result.TransactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	result, err = w.validateResult(transaction, result)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	digest := storageCommitmentResultDigest(result)
	if transaction.Result != nil {
		if transaction.ResultDigest != digest {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		return transaction, nil
	}
	allowPending := allowPendingRequest && transaction.Direction == StorageCommitmentDirectionRequestor && transaction.State == StorageCommitmentStateRequestPending
	if processingLeaseToken == "" && transaction.State != StorageCommitmentStateAccepted && !allowPending {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	if processingLeaseToken != "" && (transaction.State != StorageCommitmentStateProcessing || transaction.LeaseToken != processingLeaseToken) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	next := cloneStorageCommitmentTransaction(transaction)
	next.State = StorageCommitmentStateResultReady
	next.Result = &result
	next.ResultDigest = digest
	next.UpdatedAt = w.now()
	next.NextAttemptAt = next.UpdatedAt
	next.LeaseToken = ""
	next.LeaseUntil = time.Time{}
	return w.storeCompareAndSwap(ctx, transaction.TransactionUID, transaction.Version, next)
}

func (w *StorageCommitmentWorkflow) validateResult(transaction StorageCommitmentTransaction, result StorageCommitmentResult) (StorageCommitmentResult, error) {
	if result.TransactionUID != transaction.TransactionUID {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	if len(result.ReferencedSOPs)+len(result.FailedSOPs) != len(transaction.ReferencedSOPs) || len(result.ReferencedSOPs)+len(result.FailedSOPs) > w.limits.MaxReferences {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	var err error
	if len(result.ReferencedSOPs) > 0 {
		result.ReferencedSOPs, err = normalizeStorageCommitmentReferences(result.ReferencedSOPs, false, w.limits.MaxReferences)
		if err != nil {
			return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
		}
	}
	if len(result.FailedSOPs) > 0 {
		result.FailedSOPs, err = normalizeStorageCommitmentReferences(result.FailedSOPs, true, w.limits.MaxReferences)
		if err != nil {
			return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
		}
	}
	if len(result.ReferencedSOPs) == 0 && len(result.FailedSOPs) == 0 {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	wanted := make(map[string]bool, len(transaction.ReferencedSOPs))
	for _, reference := range transaction.ReferencedSOPs {
		wanted[storageCommitmentReferenceKey(reference)] = true
	}
	seen := make(map[string]bool, len(wanted))
	for _, references := range [][]StorageCommitmentSOPReference{result.ReferencedSOPs, result.FailedSOPs} {
		for _, reference := range references {
			key := storageCommitmentReferenceKey(reference)
			if !wanted[key] || seen[key] {
				return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
			}
			seen[key] = true
		}
	}
	if len(seen) != len(wanted) {
		return StorageCommitmentResult{}, ErrStorageCommitmentInvalidResult
	}
	if result.ProducedAt.IsZero() {
		result.ProducedAt = w.now()
	}
	return cloneStorageCommitmentResult(result), nil
}

func storageCommitmentReferenceKey(reference StorageCommitmentSOPReference) string {
	return reference.SOPClassUID + "\x00" + reference.SOPInstanceUID
}

// Process claims one accepted transaction under an expiring CAS lease and
// invokes the configured processor outside the store.
func (w *StorageCommitmentWorkflow) Process(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	return w.process(ctx, transactionUID, false)
}

func (w *StorageCommitmentWorkflow) process(ctx context.Context, transactionUID string, requireDue bool) (StorageCommitmentTransaction, error) {
	if w.processor == nil {
		return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, errors.New("processor unavailable"))
	}
	claimed, err := w.claim(ctx, transactionUID, []StorageCommitmentState{StorageCommitmentStateAccepted, StorageCommitmentStateProcessing}, StorageCommitmentStateProcessing, requireDue)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	result, processErr := callStorageCommitmentProcessor(ctx, w.processor, claimed)
	if processErr != nil {
		persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
		defer cancelPersist()
		failed, updateErr := w.finishProcessingFailure(persistCtx, claimed, processErr)
		if updateErr != nil {
			return StorageCommitmentTransaction{}, updateErr
		}
		return failed, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, processErr)
	}
	result.TransactionUID = claimed.TransactionUID
	return w.setResult(ctx, result, claimed.LeaseToken, false)
}

func callStorageCommitmentProcessor(ctx context.Context, processor StorageCommitmentProcessor, transaction StorageCommitmentTransaction) (result StorageCommitmentResult, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("storage commitment processor panic")
		}
	}()
	return processor.ProcessStorageCommitment(ctx, cloneStorageCommitmentTransaction(transaction))
}

func (w *StorageCommitmentWorkflow) finishProcessingFailure(ctx context.Context, claimed StorageCommitmentTransaction, _ error) (StorageCommitmentTransaction, error) {
	current, err := w.storeGet(ctx, claimed.TransactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if current.State != StorageCommitmentStateProcessing || current.LeaseToken != claimed.LeaseToken {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
	}
	next := cloneStorageCommitmentTransaction(current)
	next.State = StorageCommitmentStateAccepted
	next.ProcessingTries++
	next.LastFailure = StorageCommitmentFailureProcessing
	next.LeaseToken = ""
	next.LeaseUntil = time.Time{}
	next.UpdatedAt = w.now()
	next.NextAttemptAt = next.UpdatedAt.Add(storageCommitmentBackoff(w.retry, next.ProcessingTries))
	return w.storeCompareAndSwap(ctx, current.TransactionUID, current.Version, next)
}

// ProcessDue processes a finite page. It continues independent transactions
// and returns a joined set of PHI-free errors.
func (w *StorageCommitmentWorkflow) ProcessDue(ctx context.Context, limit int) error {
	if limit <= 0 || limit > w.limits.MaxListBatch {
		return ErrStorageCommitmentResourceLimit
	}
	items, err := w.storeList(ctx, StorageCommitmentTransactionQuery{
		DueAt: w.now(), Limit: limit,
		States: []StorageCommitmentState{StorageCommitmentStateAcceptancePrepared, StorageCommitmentStateAccepted, StorageCommitmentStateProcessing},
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		if item.State == StorageCommitmentStateAcceptancePrepared {
			var promoteErr error
			item, promoteErr = w.recoverCommitterAcceptance(ctx, item.TransactionUID)
			if errors.Is(promoteErr, ErrStorageCommitmentConcurrentUpdate) {
				continue
			}
			if promoteErr != nil {
				errs = append(errs, promoteErr)
				continue
			}
			if w.processor == nil {
				continue
			}
		}
		if _, err := w.process(ctx, item.TransactionUID, true); err != nil && !errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			if errors.Is(err, ErrStorageCommitmentTransactionConflict) && w.processingAlreadyAdvanced(ctx, item.TransactionUID) {
				continue
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *StorageCommitmentWorkflow) receiveResult(ctx context.Context, assoc *ul.Association, result StorageCommitmentResult) error {
	transaction, err := w.storeGet(ctx, result.TransactionUID)
	if err != nil {
		return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	callingAE := ""
	calledAE := ""
	if assoc != nil {
		callingAE = strings.TrimSpace(assoc.CallingAETitle)
		calledAE = strings.TrimSpace(assoc.CalledAETitle)
	}
	separateCallback := callingAE == transaction.CommitterAETitle && calledAE == transaction.RequestorAETitle
	originalAssociation := callingAE == transaction.RequestorAETitle && calledAE == transaction.CommitterAETitle
	if transaction.Direction != StorageCommitmentDirectionRequestor || assoc == nil ||
		(!separateCallback && !originalAssociation) || !storageCommitmentAssociationRolesValid(assoc, transaction) {
		return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProtocol, ErrStorageCommitmentInvalidResult)
	}
	stored, err := w.setResult(ctx, result, "", true)
	if err != nil {
		return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	if stored.State == StorageCommitmentStateResultReceived {
		return nil
	}
	if w.consumer != nil {
		if err := callStorageCommitmentConsumer(ctx, w.consumer, stored); err != nil {
			return sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
		}
	}
	_, err = w.updateState(ctx, stored.TransactionUID, []StorageCommitmentState{StorageCommitmentStateResultReady}, StorageCommitmentStateResultReceived, func(next *StorageCommitmentTransaction) {
		next.CompletedAt = w.now()
	})
	return err
}

func (w *StorageCommitmentWorkflow) processingAlreadyAdvanced(ctx context.Context, transactionUID string) bool {
	current, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return false
	}
	switch current.State {
	case StorageCommitmentStateResultReady, StorageCommitmentStateDelivering,
		StorageCommitmentStateDeliveryFailed, StorageCommitmentStateDeliveryExhausted,
		StorageCommitmentStateDelivered:
		return true
	default:
		return false
	}
}

func callStorageCommitmentConsumer(ctx context.Context, consumer StorageCommitmentResultConsumer, transaction StorageCommitmentTransaction) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("storage commitment result consumer panic")
		}
	}()
	return consumer.ConsumeStorageCommitmentResult(ctx, cloneStorageCommitmentTransaction(transaction))
}

func (w *StorageCommitmentWorkflow) claim(ctx context.Context, transactionUID string, allowed []StorageCommitmentState, state StorageCommitmentState, requireDue bool) (StorageCommitmentTransaction, error) {
	token, err := newStorageCommitmentLeaseToken()
	if err != nil {
		return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	for attempts := 0; attempts < 8; attempts++ {
		current, err := w.storeGet(ctx, transactionUID)
		if err != nil {
			return StorageCommitmentTransaction{}, err
		}
		if !containsStorageCommitmentState(allowed, current.State) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
		}
		if requireDue && !transactionDue(current, w.now()) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
		}
		if (current.State == StorageCommitmentStateProcessing || current.State == StorageCommitmentStateDelivering) && current.LeaseUntil.After(w.now()) {
			return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
		}
		next := cloneStorageCommitmentTransaction(current)
		next.State = state
		next.LeaseToken = token
		next.LeaseUntil = w.now().Add(w.leaseDuration)
		next.UpdatedAt = w.now()
		claimed, err := w.storeCompareAndSwap(ctx, current.TransactionUID, current.Version, next)
		if errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			continue
		}
		return claimed, err
	}
	return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
}

// DeliverCallback performs one persisted callback attempt. Retry scheduling is
// stored in the transaction; DeliverDue resumes it after restart.
func (w *StorageCommitmentWorkflow) DeliverCallback(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	return w.deliverCallback(ctx, transactionUID, false)
}

func (w *StorageCommitmentWorkflow) deliverCallback(ctx context.Context, transactionUID string, requireDue bool) (StorageCommitmentTransaction, error) {
	ready, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if ready.DeliveryAttempts >= w.retry.MaxAttempts {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentDeliveryExhausted
	}
	if ready.Direction != StorageCommitmentDirectionCommitter || ready.DeliveryMode != StorageCommitmentDeliveryCallback || ready.Result == nil {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	claimed, err := w.claim(ctx, transactionUID, []StorageCommitmentState{
		StorageCommitmentStateResultReady, StorageCommitmentStateDeliveryFailed, StorageCommitmentStateDelivering,
	}, StorageCommitmentStateDelivering, requireDue)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	deliveryErr := w.callbackAttempt(ctx, claimed)
	if deliveryErr == nil {
		persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
		defer cancelPersist()
		return w.finishDelivery(persistCtx, claimed, nil)
	}
	persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
	defer cancelPersist()
	updated, updateErr := w.finishDelivery(persistCtx, claimed, deliveryErr)
	if updateErr != nil {
		return StorageCommitmentTransaction{}, updateErr
	}
	return updated, deliveryErr
}

func (w *StorageCommitmentWorkflow) callbackAttempt(ctx context.Context, transaction StorageCommitmentTransaction) error {
	if w.resolver == nil {
		return newStorageCommitmentDeliveryError(ErrStorageCommitmentCallbackUnknown, 0)
	}
	target, err := w.resolver.ResolveStorageCommitmentCallback(ctx, StorageCommitmentCallbackRequest{
		TransactionUID:   transaction.TransactionUID,
		RequestorAETitle: transaction.RequestorAETitle,
		CommitterAETitle: transaction.CommitterAETitle,
	})
	if err != nil {
		return newStorageCommitmentDeliveryError(err, 0)
	}
	target.Address = strings.TrimSpace(target.Address)
	options := cloneStoreDialOptions(target.DialOptions)
	if target.Address == "" {
		return newStorageCommitmentDeliveryError(ErrStorageCommitmentCallbackUnknown, 0)
	}
	if options.CallingAETitle == "" {
		options.CallingAETitle = transaction.CommitterAETitle
	}
	if options.CalledAETitle == "" {
		options.CalledAETitle = transaction.RequestorAETitle
	}
	if strings.TrimSpace(options.CallingAETitle) != transaction.CommitterAETitle || strings.TrimSpace(options.CalledAETitle) != transaction.RequestorAETitle {
		return newStorageCommitmentDeliveryError(ErrStorageCommitmentCallbackUnknown, 0)
	}
	options.Contexts = []ul.PresentationContext{{
		AbstractSyntaxUID:  StorageCommitmentPushModelSOPClassUID,
		TransferSyntaxUIDs: []string{ul.ExplicitVRLittleEndian, ul.ImplicitVRLittleEndian},
	}}
	options.RoleSelections = []ul.RoleSelectionItem{{SopClassUID: StorageCommitmentPushModelSOPClassUID, SCPRole: true}}
	assoc, err := w.dialer.DialStorageCommitment(ctx, target.Address, options)
	if err != nil {
		return newStorageCommitmentDeliveryError(err, 0)
	}
	acknowledged, sendErr := w.sendResultOnAssociation(ctx, assoc, transaction)
	if acknowledged {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = assoc.Release(cleanupCtx)
		return sendErr
	}
	cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_ = assoc.AbortWithContext(cleanupCtx, ul.AbortReasonNotSpecified)
	return sendErr
}

// DeliverOnAssociation sends a ready result over a borrowed, exclusively-read
// association. It neither releases nor closes the association. The caller must
// not run ServeAssociation or Dispatcher concurrently on the same connection.
func (w *StorageCommitmentWorkflow) DeliverOnAssociation(ctx context.Context, transactionUID string, assoc *ul.Association) (StorageCommitmentTransaction, error) {
	ready, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if ready.Direction != StorageCommitmentDirectionCommitter || ready.DeliveryMode != StorageCommitmentDeliverySameAssociation || ready.Result == nil ||
		assoc == nil || assoc.CallingAETitle != ready.RequestorAETitle || assoc.CalledAETitle != ready.CommitterAETitle ||
		!storageCommitmentAssociationRolesValid(assoc, ready) {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	claimed, err := w.claim(ctx, transactionUID, []StorageCommitmentState{
		StorageCommitmentStateResultReady, StorageCommitmentStateDeliveryFailed, StorageCommitmentStateDelivering,
	}, StorageCommitmentStateDelivering, false)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	_, deliveryErr := w.sendResultOnAssociation(ctx, assoc, claimed)
	if deliveryErr == nil {
		persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
		defer cancelPersist()
		return w.finishDelivery(persistCtx, claimed, nil)
	}
	persistCtx, cancelPersist := storageCommitmentPersistenceContext(ctx)
	defer cancelPersist()
	updated, updateErr := w.finishDelivery(persistCtx, claimed, deliveryErr)
	if updateErr != nil {
		return StorageCommitmentTransaction{}, updateErr
	}
	return updated, deliveryErr
}

// CompleteManualDelivery marks an externally delivered result terminal. It is
// valid only for a committer-side manual transaction with a persisted result
// and is idempotent after completion.
func (w *StorageCommitmentWorkflow) CompleteManualDelivery(ctx context.Context, transactionUID string) (StorageCommitmentTransaction, error) {
	transaction, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, sanitizedStorageCommitmentWorkflowError(StorageCommitmentFailureProcessing, err)
	}
	if transaction.Direction != StorageCommitmentDirectionCommitter || transaction.DeliveryMode != StorageCommitmentDeliveryManual || transaction.Result == nil {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentTransactionConflict
	}
	return w.updateState(ctx, transactionUID, []StorageCommitmentState{StorageCommitmentStateResultReady}, StorageCommitmentStateDelivered, func(next *StorageCommitmentTransaction) {
		next.CompletedAt = w.now()
		next.NextAttemptAt = time.Time{}
		next.LastFailure = StorageCommitmentFailureNone
	})
}

func (w *StorageCommitmentWorkflow) sendResultOnAssociation(ctx context.Context, assoc *ul.Association, transaction StorageCommitmentTransaction) (bool, error) {
	if assoc == nil || transaction.Result == nil || !storageCommitmentAssociationRolesValid(assoc, transaction) {
		return false, newStorageCommitmentDeliveryError(ErrStorageCommitmentTransactionConflict, 0)
	}
	dataSet, err := BuildStorageCommitmentEventInformation(transaction.TransactionUID, transaction.Result.ReferencedSOPs, transaction.Result.FailedSOPs)
	if err != nil {
		return false, newStorageCommitmentDeliveryError(ErrStorageCommitmentInvalidResult, 0)
	}
	eventType := StorageCommitmentEventTypeSuccess
	if len(transaction.Result.FailedSOPs) > 0 {
		eventType = StorageCommitmentEventTypeFailures
	}
	result, err := NewNormalizedClient(assoc).EventReport(ctx, NormalizedEventReportRequest{
		AffectedSOPClassUID:    StorageCommitmentPushModelSOPClassUID,
		AffectedSOPInstanceUID: StorageCommitmentPushModelSOPInstanceUID,
		CommandDataSetType:     DataSetPresent, EventTypeID: eventType,
	}, dataSet)
	if err != nil {
		var statusErr *NormalizedStatusError
		if result.Response != nil && errors.As(err, &statusErr) {
			return true, newStorageCommitmentDeliveryError(&StorageCommitmentStatusError{
				Op: "N-EVENT-REPORT-RSP", Status: result.Response.Status,
				Class: ClassifyStorageCommitmentStatus(result.Response.Status),
			}, result.Response.Status)
		}
		return false, newStorageCommitmentDeliveryError(err, 0)
	}
	if result.Response == nil {
		return false, newStorageCommitmentDeliveryError(ErrStorageCommitmentInvalidResult, 0)
	}
	if err := CheckStorageCommitmentStatus("N-EVENT-REPORT-RSP", result.Response.Status); err != nil {
		return true, newStorageCommitmentDeliveryError(err, result.Response.Status)
	}
	return true, nil
}

func (w *StorageCommitmentWorkflow) finishDelivery(ctx context.Context, claimed StorageCommitmentTransaction, deliveryErr error) (StorageCommitmentTransaction, error) {
	current, err := w.storeGet(ctx, claimed.TransactionUID)
	if err != nil {
		return StorageCommitmentTransaction{}, err
	}
	if current.State != StorageCommitmentStateDelivering || current.LeaseToken != claimed.LeaseToken {
		return StorageCommitmentTransaction{}, ErrStorageCommitmentConcurrentUpdate
	}
	next := cloneStorageCommitmentTransaction(current)
	next.DeliveryAttempts++
	next.LeaseToken = ""
	next.LeaseUntil = time.Time{}
	next.UpdatedAt = w.now()
	if deliveryErr == nil {
		next.State = StorageCommitmentStateDelivered
		next.CompletedAt = next.UpdatedAt
		next.NextAttemptAt = time.Time{}
		next.LastFailure = StorageCommitmentFailureNone
		next.LastDIMSEStatus = 0
	} else {
		failure := newStorageCommitmentDeliveryError(deliveryErr, 0)
		var typed *StorageCommitmentDeliveryError
		if errors.As(deliveryErr, &typed) {
			failure = typed
		}
		next.LastFailure = failure.Class
		next.LastDIMSEStatus = failure.Status
		if failure.Retryable && next.DeliveryAttempts < w.retry.MaxAttempts {
			next.State = StorageCommitmentStateDeliveryFailed
			next.NextAttemptAt = next.UpdatedAt.Add(storageCommitmentBackoff(w.retry, next.DeliveryAttempts))
		} else {
			next.State = StorageCommitmentStateDeliveryExhausted
			next.NextAttemptAt = time.Time{}
		}
	}
	return w.storeCompareAndSwap(ctx, current.TransactionUID, current.Version, next)
}

// DeliverDue performs one attempt for each due callback transaction in a
// finite page. Concurrent processes contend through the persisted lease.
func (w *StorageCommitmentWorkflow) DeliverDue(ctx context.Context, limit int) error {
	if limit <= 0 || limit > w.limits.MaxListBatch {
		return ErrStorageCommitmentResourceLimit
	}
	items, err := w.storeList(ctx, StorageCommitmentTransactionQuery{
		DueAt: w.now(), Limit: limit,
		States: []StorageCommitmentState{StorageCommitmentStateResultReady, StorageCommitmentStateDeliveryFailed, StorageCommitmentStateDelivering},
	})
	if err != nil {
		return err
	}
	var errs []error
	for _, item := range items {
		if item.DeliveryMode != StorageCommitmentDeliveryCallback || item.DeliveryAttempts >= w.retry.MaxAttempts || item.NextAttemptAt.IsZero() && item.State == StorageCommitmentStateDeliveryFailed {
			continue
		}
		if _, err := w.deliverCallback(ctx, item.TransactionUID, true); err != nil && !errors.Is(err, ErrStorageCommitmentConcurrentUpdate) {
			if errors.Is(err, ErrStorageCommitmentTransactionConflict) && w.deliveryAlreadyAdvanced(ctx, item.TransactionUID) {
				continue
			}
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (w *StorageCommitmentWorkflow) deliveryAlreadyAdvanced(ctx context.Context, transactionUID string) bool {
	current, err := w.storeGet(ctx, transactionUID)
	if err != nil {
		return false
	}
	return current.State == StorageCommitmentStateDelivered || current.State == StorageCommitmentStateDeliveryExhausted
}

func storageCommitmentBackoff(policy StorageCommitmentRetryPolicy, attempt int) time.Duration {
	if attempt <= 1 {
		return policy.InitialBackoff
	}
	backoff := policy.InitialBackoff
	for i := 1; i < attempt; i++ {
		if backoff >= policy.MaxBackoff/2 {
			return policy.MaxBackoff
		}
		backoff *= 2
	}
	if backoff > policy.MaxBackoff {
		return policy.MaxBackoff
	}
	return backoff
}

func storageCommitmentRequestorCanActAsSCU(assoc *ul.Association) bool {
	if assoc == nil {
		return false
	}
	for _, role := range assoc.AcceptedRoleSelections {
		if role.SopClassUID == StorageCommitmentPushModelSOPClassUID {
			return role.SCURole
		}
	}
	return true
}

// storageCommitmentAssociationRolesValid interprets role selection from the
// association-requestor's perspective. On the original association that peer
// is the Storage Commitment SCU; on a callback association it is the Storage
// Commitment SCP and must explicitly negotiate the non-default SCP role.
func storageCommitmentAssociationRolesValid(assoc *ul.Association, transaction StorageCommitmentTransaction) bool {
	if assoc == nil {
		return false
	}
	originalAssociation := assoc.CallingAETitle == transaction.RequestorAETitle && assoc.CalledAETitle == transaction.CommitterAETitle
	separateCallback := assoc.CallingAETitle == transaction.CommitterAETitle && assoc.CalledAETitle == transaction.RequestorAETitle
	for _, role := range assoc.AcceptedRoleSelections {
		if role.SopClassUID != StorageCommitmentPushModelSOPClassUID {
			continue
		}
		if originalAssociation {
			return role.SCURole
		}
		if separateCallback {
			return role.SCPRole
		}
	}
	return originalAssociation
}

func newStorageCommitmentDeliveryError(err error, status uint16) *StorageCommitmentDeliveryError {
	if err == nil {
		return nil
	}
	failure := &StorageCommitmentDeliveryError{Class: StorageCommitmentFailureCallbackOffline, Retryable: true, Status: status, Err: err}
	var existing *StorageCommitmentDeliveryError
	if errors.As(err, &existing) {
		copy := *existing
		if status != 0 {
			copy.Status = status
		}
		return &copy
	}
	var rejection *ul.RejectionError
	var unknownAuthority x509.UnknownAuthorityError
	var certificateInvalid x509.CertificateInvalidError
	var hostnameError x509.HostnameError
	var certificateVerification *tls.CertificateVerificationError
	var recordHeaderError tls.RecordHeaderError
	var netError net.Error
	var statusError *StorageCommitmentStatusError
	switch {
	case errors.Is(err, ErrStorageCommitmentCallbackUnknown):
		failure.Class = StorageCommitmentFailureCallbackUnknown
	case errors.Is(err, context.Canceled):
		failure.Class = StorageCommitmentFailureCanceled
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ul.ErrAssociationTimeout):
		failure.Class = StorageCommitmentFailureTimeout
	case errors.As(err, &unknownAuthority), errors.As(err, &certificateInvalid), errors.As(err, &hostnameError), errors.As(err, &certificateVerification), errors.As(err, &recordHeaderError):
		failure.Class = StorageCommitmentFailureTLS
	case errors.As(err, &rejection):
		failure.Class = StorageCommitmentFailureAssociationRejected
	case errors.As(err, &statusError):
		failure.Class = StorageCommitmentFailureDIMSEStatus
		failure.Status = statusError.Status
	case errors.Is(err, ul.ErrUnexpectedPDU), errors.Is(err, ErrPresentationContextMismatch):
		failure.Class = StorageCommitmentFailureProtocol
		failure.Retryable = false
	case errors.As(err, &netError):
		failure.Class = StorageCommitmentFailureCallbackOffline
	}
	return failure
}

func sanitizedStorageCommitmentWorkflowError(class StorageCommitmentFailureClass, cause error) error {
	return &StorageCommitmentDeliveryError{Class: class, Retryable: false, Err: cause}
}

func storageCommitmentPersistenceContext(context.Context) (context.Context, context.CancelFunc) {
	// Once the peer response, processor result, or delivery attempt is
	// definitive, persist its state with a short independent cleanup context.
	// Reusing the operation context would strand a durable lease or pending
	// transaction when its deadline expires at the wire boundary.
	return context.WithTimeout(context.Background(), time.Second)
}
