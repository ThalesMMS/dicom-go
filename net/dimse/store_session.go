package dimse

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type StoreOutcome string

const (
	StoreOutcomeNotSent  StoreOutcome = "not_sent"
	StoreOutcomeSuccess  StoreOutcome = "success"
	StoreOutcomeWarning  StoreOutcome = "warning"
	StoreOutcomeFailure  StoreOutcome = "failure"
	StoreOutcomeUnknown  StoreOutcome = "unknown"
	StoreOutcomeCanceled StoreOutcome = "canceled"
)

type StoreItemResult struct {
	SourceIndex                 int
	Descriptor                  StoreDescriptor
	AssociationIndex            int
	Attempt                     int
	Attempted                   bool
	Status                      uint16
	StatusSet                   bool
	NegotiatedTransferSyntaxUID string
	Outcome                     StoreOutcome
	Err                         error
}

type StoreBatchResult struct {
	Plan         StorePlan
	Items        []StoreItemResult
	Associations int
	Succeeded    int
	Warnings     int
	Failed       int
	Unknown      int
	Canceled     int
	Complete     bool
}

// StoreProgress is deliberately origin-, UID-, and value-free. SourceIndex is
// the stable caller-owned correlation key.
type StoreProgress struct {
	SourceIndex      int
	Completed        int
	Total            int
	AssociationIndex int
	Attempt          int
	Outcome          StoreOutcome
	Status           uint16
	StatusSet        bool
}

type StoreProgressHandler func(context.Context, StoreProgress) error

type StoreSessionOptions struct {
	DialOptions ul.DialOptions
	PlanOptions StorePlanOptions

	ContinueOnError        bool
	DisableReconnect       bool
	RetryUncertain         bool
	MaxAssociationAttempts int
	MaxStoreAttempts       int
	MaxInFlightBytes       int64

	Priority                     uint16
	MoveOriginatorAETitle        string
	MoveOriginatorMessageIDOrNil *uint16
	ReleaseTimeout               time.Duration
	CleanupTimeout               time.Duration
	OnProgress                   StoreProgressHandler
}

type storeDialFunc func(context.Context, string, ul.DialOptions) (*ul.Association, error)

// StoreSession owns associations created for one or more StoreBatch calls.
// Calls are serialized; each association carries one outstanding operation.
type StoreSession struct {
	address string
	options StoreSessionOptions
	dial    storeDialFunc
	runGate chan struct{}

	mu     sync.Mutex
	closed bool
	active *ul.Association
}

func NewStoreSession(address string, options StoreSessionOptions) (*StoreSession, error) {
	if address == "" {
		return nil, ErrStoreInvalidOptions
	}
	options = cloneStoreSessionOptions(options)
	if options.Priority > 2 || options.MaxAssociationAttempts < 0 || options.MaxStoreAttempts < 0 || options.MaxInFlightBytes < 0 || options.ReleaseTimeout < 0 || options.CleanupTimeout < 0 {
		return nil, ErrStoreInvalidOptions
	}
	if options.MaxAssociationAttempts == 0 {
		options.MaxAssociationAttempts = 1
	}
	if options.MaxStoreAttempts == 0 {
		options.MaxStoreAttempts = 1
	}
	if options.ReleaseTimeout == 0 {
		options.ReleaseTimeout = 5 * time.Second
	}
	if options.CleanupTimeout == 0 {
		options.CleanupTimeout = time.Second
	}
	if options.MaxInFlightBytes > 0 && (options.PlanOptions.Limits.MaxItemBytes == 0 || options.PlanOptions.Limits.MaxItemBytes > options.MaxInFlightBytes) {
		options.PlanOptions.Limits.MaxItemBytes = options.MaxInFlightBytes
	}
	limits, err := normalizeStoreLimits(options.PlanOptions.Limits)
	if err != nil {
		return nil, err
	}
	options.PlanOptions.Limits = limits
	session := &StoreSession{
		address: address,
		options: options,
		dial: func(ctx context.Context, address string, dialOptions ul.DialOptions) (*ul.Association, error) {
			return ul.DialContext(ctx, address, dialOptions)
		},
		runGate: make(chan struct{}, 1),
	}
	session.runGate <- struct{}{}
	return session, nil
}

func (s *StoreSession) Store(ctx context.Context, source StoreSource) (StoreItemResult, error) {
	result, err := s.StoreBatch(ctx, []StoreSource{source})
	if len(result.Items) == 0 {
		return StoreItemResult{}, err
	}
	if err == nil {
		err = result.Items[0].Err
	}
	return result.Items[0], err
}

func (s *StoreSession) StorePath(ctx context.Context, path string) (StoreItemResult, error) {
	return s.Store(ctx, NewPathStoreSource(path))
}

func (s *StoreSession) StoreBatch(ctx context.Context, sources []StoreSource) (result StoreBatchResult, returnErr error) {
	if s == nil {
		return StoreBatchResult{}, ErrStoreInvalidOptions
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.acquireRun(ctx); err != nil {
		return StoreBatchResult{}, err
	}
	defer s.releaseRun()
	if s.isClosed() {
		return StoreBatchResult{}, ErrStoreSessionClosed
	}
	sources = append([]StoreSource(nil), sources...)

	plan, err := PlanStoreBatch(ctx, sources, s.options.PlanOptions)
	result = newStoreBatchResult(plan)
	defer func() {
		result.Complete = storeBatchComplete(result.Items)
	}()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			s.cancelAllNotSent(&result, plan, err)
			result.Complete = true
		}
		return result, err
	}
	completed := 0
	for _, failure := range plan.Failures {
		item := &result.Items[failure.SourceIndex]
		item.Outcome = StoreOutcomeFailure
		item.Err = failure.Err
		result.Failed++
		completed++
		if progressErr := s.reportProgress(ctx, *item, completed, len(sources)); progressErr != nil {
			if ctx.Err() != nil {
				s.cancelAllNotSent(&result, plan, ctx.Err())
				result.Complete = storeBatchComplete(result.Items)
			}
			return result, progressErr
		}
	}
	if len(plan.Failures) > 0 && !s.options.ContinueOnError {
		result.Complete = storeBatchComplete(result.Items)
		return result, plan.Failures[0].Err
	}
	remainingTotalBytes := s.options.PlanOptions.Limits.MaxTotalBytes

	for associationIndex := range plan.Associations {
		associationPlan := plan.Associations[associationIndex]
		position := 0
		for position < len(associationPlan.Items) {
			if err := s.contextOrClosed(ctx); err != nil {
				s.cancelAllNotSent(&result, plan, err)
				result.Complete = storeBatchComplete(result.Items)
				return result, err
			}
			assoc, dialAttempt, dialErr := s.dialAssociation(ctx, associationPlan.Contexts)
			if dialErr != nil {
				if err := s.contextOrClosed(ctx); err != nil {
					s.cancelAllNotSent(&result, plan, err)
					result.Complete = storeBatchComplete(result.Items)
					return result, err
				}
				for _, planned := range associationPlan.Items[position:] {
					item := &result.Items[planned.SourceIndex]
					item.Descriptor = cloneStoreDescriptor(planned.Descriptor)
					item.AssociationIndex = associationIndex
					item.Attempt = dialAttempt
					item.Outcome = StoreOutcomeFailure
					cause := error(ErrStoreAssociation)
					if errors.Is(dialErr, ul.ErrNoAcceptedPresentationContexts) {
						cause = ErrStorePresentationContextRejected
					}
					item.Err = newStoreError("associate", planned.SourceIndex, cause, false)
					result.Failed++
					completed++
					if progressErr := s.reportProgress(ctx, *item, completed, len(sources)); progressErr != nil {
						if ctx.Err() != nil {
							s.cancelAllNotSent(&result, plan, ctx.Err())
							result.Complete = storeBatchComplete(result.Items)
						}
						return result, progressErr
					}
				}
				if !s.options.ContinueOnError {
					return result, result.Items[associationPlan.Items[position].SourceIndex].Err
				}
				break
			}
			result.Associations++
			s.setActive(assoc)
			associationUsable := true
			client := NewStoreClient(assoc)
			for position < len(associationPlan.Items) {
				planned := associationPlan.Items[position]
				item := &result.Items[planned.SourceIndex]
				item.Descriptor = cloneStoreDescriptor(planned.Descriptor)
				item.AssociationIndex = associationIndex
				item.Attempt++
				if err := s.contextOrClosed(ctx); err != nil {
					associationUsable = false
					s.abortAssociation(assoc)
					s.clearActive(assoc)
					s.cancelAllNotSent(&result, plan, err)
					result.Complete = storeBatchComplete(result.Items)
					return result, err
				}
				if !storeContextAccepted(assoc, planned) {
					item.Outcome = StoreOutcomeFailure
					item.Err = newStoreError("negotiate", planned.SourceIndex, ErrStorePresentationContextRejected, false)
					result.Failed++
					completed++
					position++
					if err := s.reportProgress(ctx, *item, completed, len(sources)); err != nil {
						_ = s.releaseAssociation(assoc)
						s.clearActive(assoc)
						if ctx.Err() != nil {
							s.cancelAllNotSent(&result, plan, ctx.Err())
							result.Complete = storeBatchComplete(result.Items)
						}
						return result, err
					}
					if !s.options.ContinueOnError {
						_ = s.releaseAssociation(assoc)
						s.clearActive(assoc)
						return result, item.Err
					}
					continue
				}
				opened, openErr := openStoreSource(ctx, sources[planned.SourceIndex])
				if openErr == nil {
					openErr = validateOpenedStoreSource(planned.Descriptor, opened)
				}
				if openErr != nil {
					if opened.Close != nil {
						_ = closeStoreSource(opened.Close)
					}
					item.Outcome = StoreOutcomeFailure
					item.Err = newStoreError("open", planned.SourceIndex, openErr, false)
					result.Failed++
					completed++
					position++
					if err := s.reportProgress(ctx, *item, completed, len(sources)); err != nil {
						_ = s.releaseAssociation(assoc)
						s.clearActive(assoc)
						if ctx.Err() != nil {
							s.cancelAllNotSent(&result, plan, ctx.Err())
							result.Complete = storeBatchComplete(result.Items)
						}
						return result, err
					}
					if !s.options.ContinueOnError {
						_ = s.releaseAssociation(assoc)
						s.clearActive(assoc)
						return result, item.Err
					}
					continue
				}

				item.Attempted = true
				writeDataSet := s.boundedStoreWriter(opened.WriteDataSet, &remainingTotalBytes)
				storeResult, storeErr := client.StoreEncodedWithOptions(ctx, writeDataSet, CStoreOptions{
					AffectedSOPClassUID:          planned.Descriptor.SOPClassUID,
					AffectedSOPInstanceUID:       planned.Descriptor.SOPInstanceUID,
					TransferSyntaxUIDs:           planned.Descriptor.WritableTransferSyntaxUIDs,
					Priority:                     s.options.Priority,
					MoveOriginatorAETitle:        s.options.MoveOriginatorAETitle,
					MoveOriginatorMessageIDOrNil: s.options.MoveOriginatorMessageIDOrNil,
				})
				closeErr := closeStoreSource(opened.Close)
				item.NegotiatedTransferSyntaxUID = storeResult.PresentationContext.TransferSyntaxUID
				if storeResult.Response != nil {
					item.Status = storeResult.Response.Status
					item.StatusSet = true
				}
				uncertain := errors.Is(storeErr, ErrAssociationStateUncertain)
				if uncertain && !s.options.DisableReconnect && s.options.RetryUncertain && item.Attempt < s.options.MaxStoreAttempts {
					associationUsable = false
					s.abortAssociation(assoc)
					s.clearActive(assoc)
					item.Outcome = StoreOutcomeNotSent
					item.Err = nil
					break
				}
				switch {
				case uncertain:
					item.Outcome = StoreOutcomeUnknown
					cause := error(ErrStoreUncertain)
					if errors.Is(storeErr, ErrStoreResourceLimit) {
						cause = ErrStoreResourceLimit
					}
					item.Err = newStoreError("transfer", planned.SourceIndex, cause, true)
					result.Unknown++
					associationUsable = false
				case storeErr != nil:
					item.Outcome = StoreOutcomeFailure
					item.Err = newStoreError("response", planned.SourceIndex, ErrStoreRemoteFailure, false)
					result.Failed++
				case closeErr != nil:
					item.Outcome = StoreOutcomeFailure
					item.Err = newStoreError("close", planned.SourceIndex, ErrStoreInvalidSource, false)
					result.Failed++
				case IsCStoreWarningStatus(item.Status):
					item.Outcome = StoreOutcomeWarning
					result.Succeeded++
					result.Warnings++
				default:
					item.Outcome = StoreOutcomeSuccess
					result.Succeeded++
				}
				completed++
				position++
				if ctx.Err() != nil {
					associationUsable = false
					s.abortAssociation(assoc)
					s.clearActive(assoc)
					s.cancelAllNotSent(&result, plan, ctx.Err())
					result.Complete = storeBatchComplete(result.Items)
					return result, ctx.Err()
				}
				if err := s.reportProgress(ctx, *item, completed, len(sources)); err != nil {
					_ = s.releaseAssociation(assoc)
					s.clearActive(assoc)
					if ctx.Err() != nil {
						s.cancelAllNotSent(&result, plan, ctx.Err())
						result.Complete = storeBatchComplete(result.Items)
					}
					return result, err
				}
				if item.Err != nil && !s.options.ContinueOnError {
					if associationUsable {
						_ = s.releaseAssociation(assoc)
					} else {
						s.abortAssociation(assoc)
					}
					s.clearActive(assoc)
					return result, item.Err
				}
				if !associationUsable {
					s.abortAssociation(assoc)
					s.clearActive(assoc)
					if s.options.DisableReconnect || position >= len(associationPlan.Items) {
						for _, remaining := range associationPlan.Items[position:] {
							remainingResult := &result.Items[remaining.SourceIndex]
							remainingResult.Descriptor = cloneStoreDescriptor(remaining.Descriptor)
							remainingResult.Outcome = StoreOutcomeFailure
							remainingResult.Err = newStoreError("reconnect", remaining.SourceIndex, ErrStoreAssociation, false)
							result.Failed++
							completed++
						}
						position = len(associationPlan.Items)
					}
					break
				}
			}
			if associationUsable {
				releaseErr := s.releaseAssociation(assoc)
				s.clearActive(assoc)
				if releaseErr != nil {
					return result, newStoreError("release", -1, ErrStoreAssociation, false)
				}
			}
		}
	}
	result.Complete = storeBatchComplete(result.Items)
	return result, nil
}

func newStoreBatchResult(plan StorePlan) StoreBatchResult {
	items := make([]StoreItemResult, plan.SourceCount)
	for i := range items {
		items[i] = StoreItemResult{SourceIndex: i, AssociationIndex: -1, Outcome: StoreOutcomeNotSent}
	}
	return StoreBatchResult{Plan: plan, Items: items}
}

func (s *StoreSession) dialAssociation(ctx context.Context, contexts []ul.PresentationContext) (*ul.Association, int, error) {
	var lastErr error
	for attempt := 1; attempt <= s.options.MaxAssociationAttempts; attempt++ {
		if err := s.contextOrClosed(ctx); err != nil {
			return nil, attempt, err
		}
		opts := cloneStoreDialOptions(s.options.DialOptions)
		opts.Context = ctx
		opts.Contexts = cloneStorePresentationContexts(contexts)
		assoc, err := s.dial(ctx, s.address, opts)
		if err == nil {
			return assoc, attempt, nil
		}
		lastErr = err
	}
	return nil, s.options.MaxAssociationAttempts, lastErr
}

func storeContextAccepted(assoc *ul.Association, planned StorePlannedItem) bool {
	if assoc == nil {
		return false
	}
	for _, syntaxUID := range planned.Descriptor.WritableTransferSyntaxUIDs {
		for _, accepted := range assoc.AcceptedContexts {
			if accepted.AbstractSyntaxUID == planned.Descriptor.SOPClassUID && accepted.TransferSyntaxUID == syntaxUID {
				return true
			}
		}
	}
	return false
}

func openStoreSource(ctx context.Context, source StoreSource) (opened OpenedStoreSource, err error) {
	if source == nil {
		return OpenedStoreSource{}, ErrStoreInvalidSource
	}
	defer func() {
		if recover() != nil {
			opened = OpenedStoreSource{}
			err = ErrStoreInvalidSource
		}
	}()
	return source.Open(ctx)
}

func validateOpenedStoreSource(planned StoreDescriptor, opened OpenedStoreSource) error {
	if opened.WriteDataSet == nil || opened.Close == nil {
		return ErrStoreInvalidSource
	}
	actual, err := normalizeStoreDescriptor(opened.Descriptor)
	if err != nil {
		return err
	}
	if actual.SOPClassUID != planned.SOPClassUID || actual.SOPInstanceUID != planned.SOPInstanceUID || actual.TransferSyntaxUID != planned.TransferSyntaxUID || (planned.Size > 0 && actual.Size != planned.Size) {
		return ErrStoreSourceChanged
	}
	if !equalStoreSyntaxUIDs(actual.WritableTransferSyntaxUIDs, planned.WritableTransferSyntaxUIDs) {
		return ErrStoreSourceChanged
	}
	if planned.PixelDataHeaderSet && (!actual.PixelDataHeaderSet || actual.PixelDataHeader != planned.PixelDataHeader) {
		return ErrStoreSourceChanged
	}
	return nil
}

func equalStoreSyntaxUIDs(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func closeStoreSource(closeFunc func() error) (err error) {
	if closeFunc == nil {
		return ErrStoreInvalidSource
	}
	defer func() {
		if recover() != nil {
			err = ErrStoreInvalidSource
		}
	}()
	return closeFunc()
}

func (s *StoreSession) reportProgress(ctx context.Context, item StoreItemResult, completed, total int) (err error) {
	if s.options.OnProgress == nil {
		return nil
	}
	progress := StoreProgress{
		SourceIndex: item.SourceIndex, Completed: completed, Total: total,
		AssociationIndex: item.AssociationIndex, Attempt: item.Attempt,
		Outcome: item.Outcome, Status: item.Status, StatusSet: item.StatusSet,
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			if ctx != nil && ctx.Err() != nil {
				err = ctx.Err()
			} else {
				err = newStoreError("progress", item.SourceIndex, ErrStoreCallback, false)
			}
		}
	}()
	err = s.options.OnProgress(ctx, progress)
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return newStoreError("progress", item.SourceIndex, ErrStoreCallback, false)
	}
	return nil
}

func (s *StoreSession) releaseAssociation(assoc *ul.Association) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.ReleaseTimeout)
	defer cancel()
	return assoc.Release(ctx)
}

func (s *StoreSession) abortAssociation(assoc *ul.Association) {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.CleanupTimeout)
	defer cancel()
	_ = assoc.AbortWithContext(ctx, ul.AbortReasonNotSpecified)
}

func (s *StoreSession) cancelAllNotSent(result *StoreBatchResult, plan StorePlan, cause error) {
	for _, association := range plan.Associations {
		for _, planned := range association.Items {
			result.Items[planned.SourceIndex].Descriptor = cloneStoreDescriptor(planned.Descriptor)
		}
	}
	for i := range result.Items {
		item := &result.Items[i]
		if item.Outcome != StoreOutcomeNotSent {
			continue
		}
		item.Outcome = StoreOutcomeCanceled
		item.Err = newStoreError("cancel", item.SourceIndex, cause, false)
		result.Canceled++
	}
}

func (s *StoreSession) boundedStoreWriter(writeDataSet CStoreDataSetWriter, remainingTotal *int64) CStoreDataSetWriter {
	limit := s.options.PlanOptions.Limits.MaxItemBytes
	return func(ctx context.Context, destination io.Writer, syntax transfer.Syntax) error {
		writer := &storeByteLimitWriter{destination: destination, remaining: limit, remainingTotal: remainingTotal}
		return writeDataSet(ctx, writer, syntax)
	}
}

type storeByteLimitWriter struct {
	destination    io.Writer
	remaining      int64
	remainingTotal *int64
}

func (w *storeByteLimitWriter) Write(data []byte) (int, error) {
	if w == nil || w.destination == nil || int64(len(data)) > w.remaining || w.remainingTotal == nil || int64(len(data)) > *w.remainingTotal {
		return 0, ErrStoreResourceLimit
	}
	n, err := w.destination.Write(data)
	w.remaining -= int64(n)
	*w.remainingTotal -= int64(n)
	return n, err
}

func storeBatchComplete(items []StoreItemResult) bool {
	for _, item := range items {
		if item.Outcome == StoreOutcomeNotSent {
			return false
		}
	}
	return true
}

func (s *StoreSession) acquireRun(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.runGate:
		return nil
	}
}

func (s *StoreSession) releaseRun() { s.runGate <- struct{}{} }

func (s *StoreSession) contextOrClosed(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if s.isClosed() {
		return ErrStoreSessionClosed
	}
	return nil
}

func (s *StoreSession) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closed
}

func (s *StoreSession) setActive(assoc *ul.Association) {
	s.mu.Lock()
	s.active = assoc
	s.mu.Unlock()
}

func (s *StoreSession) clearActive(assoc *ul.Association) {
	s.mu.Lock()
	if s.active == assoc {
		s.active = nil
	}
	s.mu.Unlock()
}

// Close prevents new batches. It waits for an active batch to finish and is
// idempotent; StoreBatch itself performs the protocol release.
func (s *StoreSession) Close(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.acquireRun(ctx); err != nil {
		return err
	}
	defer s.releaseRun()
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

// Abort prevents new batches and aborts the active association, if any.
func (s *StoreSession) Abort(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.mu.Lock()
	s.closed = true
	assoc := s.active
	s.mu.Unlock()
	if assoc == nil {
		return nil
	}
	return assoc.AbortWithContext(ctx, ul.AbortReasonNotSpecified)
}

func cloneStoreSessionOptions(options StoreSessionOptions) StoreSessionOptions {
	options.DialOptions = cloneStoreDialOptions(options.DialOptions)
	if options.MoveOriginatorMessageIDOrNil != nil {
		value := *options.MoveOriginatorMessageIDOrNil
		options.MoveOriginatorMessageIDOrNil = &value
	}
	return options
}

func cloneStoreDialOptions(options ul.DialOptions) ul.DialOptions {
	options.Contexts = cloneStorePresentationContexts(options.Contexts)
	options.RoleSelections = append([]ul.RoleSelectionItem(nil), options.RoleSelections...)
	options.ExtendedNegotiation = append([]ul.SopClassExtendedNegotiationItem(nil), options.ExtendedNegotiation...)
	for i := range options.ExtendedNegotiation {
		options.ExtendedNegotiation[i].Data = append([]byte(nil), options.ExtendedNegotiation[i].Data...)
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
	if options.TLSConfig != nil {
		options.TLSConfig = options.TLSConfig.Clone()
	}
	return options
}

func cloneStorePresentationContexts(contexts []ul.PresentationContext) []ul.PresentationContext {
	clone := append([]ul.PresentationContext(nil), contexts...)
	for i := range clone {
		clone[i].TransferSyntaxUIDs = append([]string(nil), clone[i].TransferSyntaxUIDs...)
	}
	return clone
}
