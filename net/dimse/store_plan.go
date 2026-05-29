package dimse

import (
	"context"
	"errors"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

var (
	ErrStoreInvalidOptions              = errors.New("dicom dimse: invalid C-STORE session options")
	ErrStoreInvalidSource               = errors.New("dicom dimse: invalid C-STORE source")
	ErrStoreResourceLimit               = errors.New("dicom dimse: C-STORE resource limit exceeded")
	ErrStoreSourceChanged               = errors.New("dicom dimse: C-STORE source changed")
	ErrStoreTransferSyntax              = errors.New("dicom dimse: C-STORE transfer syntax is not writable")
	ErrStorePresentationContextRejected = errors.New("dicom dimse: C-STORE presentation context was not accepted")
	ErrStoreAssociation                 = errors.New("dicom dimse: C-STORE association failed")
	ErrStoreRemoteFailure               = errors.New("dicom dimse: remote C-STORE failure")
	ErrStoreUncertain                   = errors.New("dicom dimse: C-STORE outcome is uncertain")
	ErrStoreSessionClosed               = errors.New("dicom dimse: C-STORE session is closed")
	ErrStoreCallback                    = errors.New("dicom dimse: C-STORE progress callback failed")
)

const (
	DefaultStoreMaxItems                  = 100_000
	DefaultStoreMaxItemBytes        int64 = 64 << 30
	DefaultStoreMaxTotalBytes       int64 = 1 << 40
	DefaultStoreMaxAssociations           = 1024
	DefaultStoreMaxTransferSyntaxes       = 16
)

// StoreError is a value- and path-free error suitable for logs. SourceIndex is
// the only implicit correlation key; the underlying sentinel remains available
// through errors.Is.
type StoreError struct {
	Stage       string
	SourceIndex int
	Uncertain   bool
	err         error
}

func (e *StoreError) Error() string {
	if e == nil {
		return "dicom dimse: C-STORE batch failed"
	}
	if e.Stage == "" {
		return "dicom dimse: C-STORE batch failed"
	}
	return "dicom dimse: C-STORE batch " + e.Stage + " failed"
}

func (e *StoreError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

// StoreLimits bound planning work and the number/size of replayable sources.
// Zero values select finite defaults; negative values are invalid.
type StoreLimits struct {
	MaxItems            int
	MaxItemBytes        int64
	MaxTotalBytes       int64
	MaxAssociations     int
	MaxTransferSyntaxes int
}

type StorePlanOptions struct {
	Limits StoreLimits
}

// StorePlannedItem binds one input occurrence to a presentation context in a
// specific planned association.
type StorePlannedItem struct {
	SourceIndex           int
	Descriptor            StoreDescriptor
	PresentationContextID byte
}

type StoreAssociationPlan struct {
	Contexts []ul.PresentationContext
	Items    []StorePlannedItem
}

type StorePlanFailure struct {
	SourceIndex int
	Err         error
}

type StorePlan struct {
	Associations []StoreAssociationPlan
	Failures     []StorePlanFailure
	SourceCount  int
	TotalBytes   int64
}

// PlanStoreBatch inspects every source before any dial, preserving first
// occurrence order. Source-local inspection failures are recorded in Failures;
// invalid options, cancellation and global resource limits return an error.
func PlanStoreBatch(ctx context.Context, sources []StoreSource, options StorePlanOptions) (StorePlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := normalizeStoreLimits(options.Limits)
	if err != nil {
		return StorePlan{}, err
	}
	plan := StorePlan{SourceCount: len(sources)}
	if len(sources) > limits.MaxItems {
		return plan, newStoreError("plan", -1, ErrStoreResourceLimit, false)
	}

	var current *StoreAssociationPlan
	contextIDs := map[string]byte{}
	for sourceIndex, source := range sources {
		if err := ctx.Err(); err != nil {
			return plan, err
		}
		if source == nil {
			plan.Failures = append(plan.Failures, StorePlanFailure{SourceIndex: sourceIndex, Err: newStoreError("inspect", sourceIndex, ErrStoreInvalidSource, false)})
			continue
		}
		descriptor, inspectErr := inspectStoreSource(ctx, source)
		if inspectErr != nil {
			if err := ctx.Err(); err != nil {
				return plan, err
			}
			plan.Failures = append(plan.Failures, StorePlanFailure{SourceIndex: sourceIndex, Err: newStoreError("inspect", sourceIndex, ErrStoreInvalidSource, false)})
			continue
		}
		if descriptor.Size > limits.MaxItemBytes || descriptor.Size > math.MaxInt64-plan.TotalBytes || plan.TotalBytes+descriptor.Size > limits.MaxTotalBytes {
			return plan, newStoreError("plan", sourceIndex, ErrStoreResourceLimit, false)
		}
		if len(descriptor.WritableTransferSyntaxUIDs) > limits.MaxTransferSyntaxes {
			return plan, newStoreError("plan", sourceIndex, ErrStoreResourceLimit, false)
		}
		plan.TotalBytes += descriptor.Size

		key := storePresentationContextKey(descriptor)
		pcID, exists := contextIDs[key]
		if current == nil || (!exists && len(current.Contexts) == ul.MaxPresentationContexts) {
			if len(plan.Associations) >= limits.MaxAssociations {
				return plan, newStoreError("plan", sourceIndex, ErrStoreResourceLimit, false)
			}
			plan.Associations = append(plan.Associations, StoreAssociationPlan{})
			current = &plan.Associations[len(plan.Associations)-1]
			contextIDs = map[string]byte{}
			exists = false
		}
		if !exists {
			pcID = byte(2*len(current.Contexts) + 1)
			contextIDs[key] = pcID
			current.Contexts = append(current.Contexts, ul.PresentationContext{
				ID:                 pcID,
				AbstractSyntaxUID:  descriptor.SOPClassUID,
				TransferSyntaxUIDs: append([]string(nil), descriptor.WritableTransferSyntaxUIDs...),
			})
		}
		current.Items = append(current.Items, StorePlannedItem{
			SourceIndex:           sourceIndex,
			Descriptor:            cloneStoreDescriptor(descriptor),
			PresentationContextID: pcID,
		})
	}
	return plan, nil
}

func inspectStoreSource(ctx context.Context, source StoreSource) (descriptor StoreDescriptor, err error) {
	defer func() {
		if recover() != nil {
			descriptor = StoreDescriptor{}
			err = ErrStoreInvalidSource
		}
	}()
	descriptor, err = source.Inspect(ctx)
	if err != nil {
		return StoreDescriptor{}, err
	}
	return normalizeStoreDescriptor(descriptor)
}

func normalizeStoreLimits(limits StoreLimits) (StoreLimits, error) {
	if limits.MaxItems < 0 || limits.MaxItemBytes < 0 || limits.MaxTotalBytes < 0 || limits.MaxAssociations < 0 || limits.MaxTransferSyntaxes < 0 {
		return StoreLimits{}, ErrStoreInvalidOptions
	}
	if limits.MaxItems == 0 {
		limits.MaxItems = DefaultStoreMaxItems
	}
	if limits.MaxItemBytes == 0 {
		limits.MaxItemBytes = DefaultStoreMaxItemBytes
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = DefaultStoreMaxTotalBytes
	}
	if limits.MaxAssociations == 0 {
		limits.MaxAssociations = DefaultStoreMaxAssociations
	}
	if limits.MaxTransferSyntaxes == 0 {
		limits.MaxTransferSyntaxes = DefaultStoreMaxTransferSyntaxes
	}
	return limits, nil
}

func storePresentationContextKey(descriptor StoreDescriptor) string {
	return descriptor.SOPClassUID + "\x00" + strings.Join(descriptor.WritableTransferSyntaxUIDs, "\x00")
}

func newStoreError(stage string, sourceIndex int, cause error, uncertain bool) error {
	if cause == nil {
		cause = ErrStoreInvalidSource
	}
	safe := cause
	switch {
	case errors.Is(cause, context.Canceled):
		safe = context.Canceled
	case errors.Is(cause, context.DeadlineExceeded):
		safe = context.DeadlineExceeded
	case errors.Is(cause, ErrStoreInvalidOptions):
		safe = ErrStoreInvalidOptions
	case errors.Is(cause, ErrStoreResourceLimit):
		safe = ErrStoreResourceLimit
	case errors.Is(cause, ErrStoreSourceChanged):
		safe = ErrStoreSourceChanged
	case errors.Is(cause, ErrStoreTransferSyntax):
		safe = ErrStoreTransferSyntax
	case errors.Is(cause, ErrStorePresentationContextRejected):
		safe = ErrStorePresentationContextRejected
	case errors.Is(cause, ErrStoreAssociation):
		safe = ErrStoreAssociation
	case errors.Is(cause, ErrStoreRemoteFailure):
		safe = ErrStoreRemoteFailure
	case errors.Is(cause, ErrStoreUncertain):
		safe = ErrStoreUncertain
	case errors.Is(cause, ErrStoreSessionClosed):
		safe = ErrStoreSessionClosed
	case errors.Is(cause, ErrStoreCallback):
		safe = ErrStoreCallback
	default:
		safe = ErrStoreInvalidSource
	}
	return &StoreError{Stage: stage, SourceIndex: sourceIndex, Uncertain: uncertain, err: safe}
}
