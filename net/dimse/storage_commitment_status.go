package dimse

import "fmt"

const (
	// StatusStorageCommitmentProcessingFailure is the generic DIMSE processing
	// failure status used when a Storage Commitment SCP cannot accept an
	// N-ACTION request.
	StatusStorageCommitmentProcessingFailure uint16 = 0x0110
)

// StorageCommitmentStatus represents the high-level class of a Storage
// Commitment response status.
//
// This is used for both N-ACTION-RSP and N-EVENT-REPORT-RSP status values.
//
// Ref: DICOM PS3.4 Storage Commitment (Status values) and PS3.7.
//
// Note: Storage Commitment result semantics (success/failure for individual SOP
// instances) are carried in the accompanying dataset of the N-EVENT-REPORT-RQ.
// This classification only applies to DIMSE-level statuses.
//
// This implementation is intentionally minimal; it classifies status 0x0000 as
// success and every non-success status as failure.
//
//nolint:revive // DICOM naming.
type StorageCommitmentStatus int

const (
	StorageCommitmentStatusSuccess StorageCommitmentStatus = iota
	StorageCommitmentStatusFailure
)

// ClassifyStorageCommitmentStatus classifies a DIMSE Status for Storage
// Commitment messages.
func ClassifyStorageCommitmentStatus(status uint16) StorageCommitmentStatus {
	if status == 0x0000 {
		return StorageCommitmentStatusSuccess
	}
	return StorageCommitmentStatusFailure
}

// StorageCommitmentStatusError wraps a DIMSE Status for Storage Commitment.
//
// Callers can use errors.As to detect this error and inspect the status.
//
//nolint:revive // DICOM naming.
type StorageCommitmentStatusError struct {
	Op     string
	Status uint16
	Class  StorageCommitmentStatus
}

func (e *StorageCommitmentStatusError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("dicom dimse: %s: storage commitment status 0x%04X (%s)", e.Op, e.Status, e.Class)
}

func (s StorageCommitmentStatus) String() string {
	switch s {
	case StorageCommitmentStatusSuccess:
		return "success"
	case StorageCommitmentStatusFailure:
		return "failure"
	default:
		return "unknown"
	}
}

// CheckStorageCommitmentStatus returns a typed error for non-success statuses.
// Success returns nil.
func CheckStorageCommitmentStatus(op string, status uint16) error {
	cls := ClassifyStorageCommitmentStatus(status)
	if cls == StorageCommitmentStatusSuccess {
		return nil
	}
	return &StorageCommitmentStatusError{Op: op, Status: status, Class: cls}
}
