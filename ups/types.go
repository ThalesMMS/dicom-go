// Package ups implements the DICOM Unified Procedure Step workflows defined
// by PS3.4 Annex CC. Transport adapters are built on net/dimse, while the
// state machine and persistence boundaries remain usable without a network.
package ups

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
)

const (
	PushSOPClassUID  = "1.2.840.10008.5.1.4.34.6.1"
	WatchSOPClassUID = "1.2.840.10008.5.1.4.34.6.2"
	PullSOPClassUID  = "1.2.840.10008.5.1.4.34.6.3"
	EventSOPClassUID = "1.2.840.10008.5.1.4.34.6.4"
	QuerySOPClassUID = "1.2.840.10008.5.1.4.34.6.5"

	GlobalSubscriptionSOPInstanceUID         = "1.2.840.10008.5.1.4.34.5"
	FilteredGlobalSubscriptionSOPInstanceUID = "1.2.840.10008.5.1.4.34.5.1"
)

// State is the normative UPS Procedure Step State value.
type State string

const (
	StateScheduled  State = "SCHEDULED"
	StateInProgress State = "IN PROGRESS"
	StateCompleted  State = "COMPLETED"
	StateCanceled   State = "CANCELED"
)

const (
	StatusSuccess                       uint16 = 0x0000
	StatusRequestedOptionalUnsupported  uint16 = 0x0001
	StatusNoSuchAttribute               uint16 = 0x0105
	StatusInvalidAttributeValue         uint16 = 0x0106
	StatusDuplicateSOPInstance          uint16 = 0x0111
	StatusNoSuchSOPInstance             uint16 = 0x0112
	StatusInvalidObjectInstance         uint16 = 0x0117
	StatusMissingAttribute              uint16 = 0x0120
	StatusMissingAttributeValue         uint16 = 0x0121
	StatusNotAuthorized                 uint16 = 0x0124
	StatusDeletionLockNotGranted        uint16 = 0xB301
	StatusAlreadyCanceled               uint16 = 0xB304
	StatusCoercedInvalidValues          uint16 = 0xB305
	StatusAlreadyCompleted              uint16 = 0xB306
	StatusMayNoLongerBeUpdated          uint16 = 0xC300
	StatusIncorrectTransactionUID       uint16 = 0xC301
	StatusAlreadyInProgress             uint16 = 0xC302
	StatusOnlyScheduledViaCreate        uint16 = 0xC303
	StatusFinalStateRequirementsNotMet  uint16 = 0xC304
	StatusUPSNotFound                   uint16 = 0xC307
	StatusReceivingAEUnknown            uint16 = 0xC308
	StatusCreateStateNotScheduled       uint16 = 0xC309
	StatusNotInProgress                 uint16 = 0xC310
	StatusAlreadyCompletedCancelRequest uint16 = 0xC311
	StatusPerformerCannotBeContacted    uint16 = 0xC312
	StatusPerformerChoosesNotToCancel   uint16 = 0xC313
	StatusActionNotAppropriate          uint16 = 0xC314
	StatusEventReportsUnsupported       uint16 = 0xC315
)

var (
	ErrNotFound         = errors.New("dicom ups: record not found")
	ErrConflict         = errors.New("dicom ups: record conflict")
	ErrConcurrentUpdate = errors.New("dicom ups: concurrent update")
	ErrResourceLimit    = errors.New("dicom ups: resource limit")
	ErrInvalidDataSet   = errors.New("dicom ups: invalid dataset")
	ErrInvalidState     = errors.New("dicom ups: invalid state")
	ErrDeliveryFailed   = errors.New("dicom ups: event delivery failed")
	ErrRepository       = errors.New("dicom ups: repository operation failed")
)

// RepositoryError keeps backend diagnostics available to callers through
// errors.Is/As without placing backend text (which may contain PHI) in Error.
type RepositoryError struct{ Err error }

func (e *RepositoryError) Error() string { return ErrRepository.Error() }

func (e *RepositoryError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func (e *RepositoryError) Is(target error) bool { return target == ErrRepository }

// StatusError is deliberately PHI-free. Err remains available to the direct
// caller through errors.Is/As but is not interpolated into Error().
type StatusError struct {
	Operation string
	Status    uint16
	Err       error
}

func (e *StatusError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("dicom ups: %s failed with status 0x%04X", e.Operation, e.Status)
}

func (e *StatusError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func IsStatus(err error, status uint16) bool {
	var target *StatusError
	return errors.As(err, &target) && target.Status == status
}

func statusError(operation string, status uint16, err error) error {
	return &StatusError{Operation: operation, Status: status, Err: err}
}

// Step is a detached, value-only UPS snapshot. Store implementations must
// deep-clone Attributes on both input and output.
type Step struct {
	SOPInstanceUID string
	State          State
	TransactionUID string
	Attributes     core.DataSet
	Version        uint64
	Sequence       uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

type StepQuery struct {
	AfterSequence uint64
	Limit         int
	States        []State
}

type StepStore interface {
	GetStep(context.Context, string) (Step, error)
	ListSteps(context.Context, StepQuery) ([]Step, error)
}

type EventType uint16

const (
	EventStateReport     EventType = 1
	EventCancelRequested EventType = 2
	EventProgressReport  EventType = 3
	EventSCPStatusChange EventType = 4
	EventUPSAssigned     EventType = 5
)

// Event is the immutable audit/outbox intent committed with a UPS mutation.
type Event struct {
	ID                string
	Type              EventType
	SOPInstanceUID    string
	StepVersion       uint64
	Information       core.DataSet
	DirectReceivingAE string
	CreatedAt         time.Time
}

type EventQuery struct {
	SOPInstanceUID string
	AfterID        string
	Limit          int
}

type EventStore interface {
	ListEvents(context.Context, EventQuery) ([]Event, error)
}

// ReceivedEvent describes an inbound UPS N-EVENT-REPORT. Information is
// borrowed and is valid only until HandleUPSEvent returns.
type ReceivedEvent struct {
	SOPInstanceUID string
	Type           EventType
	Information    *object.Object
}

// EventHandler consumes inbound UPS event reports synchronously.
type EventHandler interface {
	HandleUPSEvent(context.Context, ReceivedEvent) error
}

// EventHandlerFunc adapts a function to EventHandler.
type EventHandlerFunc func(context.Context, ReceivedEvent) error

func (handler EventHandlerFunc) HandleUPSEvent(ctx context.Context, event ReceivedEvent) error {
	return handler(ctx, event)
}

// StepMutation and CommitRequest form the atomic state-plus-outbox boundary.
// ExpectedVersion zero creates a new step; a non-zero value performs CAS.
type StepMutation struct {
	ExpectedVersion uint64
	Next            Step
}

type CommitRequest struct {
	Step         *StepMutation
	Subscription *SubscriptionMutation
	Events       []Event
}

type CommitResult struct {
	Step         *Step
	Subscription *Subscription
}

type AtomicCommitter interface {
	CommitUPS(context.Context, CommitRequest) (CommitResult, error)
}

// Store is the minimum atomic repository required by Service. Subscription and
// delivery facets are added by the Watch implementation without weakening the
// state-plus-outbox transaction.
type Store interface {
	StepStore
	SubscriptionStore
	EventStore
	DeliveryStore
	AtomicCommitter
}

type MemoryStoreOptions struct {
	MaxSteps         int
	MaxSubscriptions int
	MaxEvents        int
	MaxDeliveries    int
	MaxFanOut        int
}

type Limits struct {
	MaxDataSetBytes     int64
	MaxDataSetElements  int
	MaxDataSetDepth     int
	MaxCASAttempts      int
	MaxStatusRecipients int
}

type ServiceOptions struct {
	Limits                    Limits
	Clock                     func() time.Time
	DefaultWorklistLabel      string
	CallbackResolver          CallbackResolver
	AssociationDialer         AssociationDialer
	EventHandler              EventHandler
	CallbackCallingAE         string
	DeliveryLimits            DeliveryLimits
	RefuseDeletionLocks       bool
	FallbackReceivingAETitles []string
}

type SCPStatus string

const (
	SCPStatusRestarted SCPStatus = "RESTARTED"
	SCPStatusGoingDown SCPStatus = "GOING DOWN"
)

type ListStatus string

const (
	ListStatusWarmStart ListStatus = "WARM START"
	ListStatusColdStart ListStatus = "COLD START"
)

type SCPStatusChange struct {
	Status                 SCPStatus
	SubscriptionListStatus ListStatus
	UPSListStatus          ListStatus
}

type CreateRequest struct {
	SOPInstanceUID string
	Attributes     *object.Object
}

type SetRequest struct {
	SOPInstanceUID string
	TransactionUID string
	Modifications  *object.Object
}

type ChangeStateRequest struct {
	SOPInstanceUID string
	State          State
	TransactionUID string
}

type CancelRequest struct {
	SOPInstanceUID    string
	RequestingAETitle string
	Information       *object.Object
}
