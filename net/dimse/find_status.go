package dimse

import (
	"errors"
	"fmt"
)

// C-FIND response status codes used for flow control.
//
// DICOM PS3.7 defines two Pending status codes (0xFF00 / 0xFF01) and Success
// (0x0000). Other values indicate warning/failure/cancel/etc.
const (
	StatusPending        uint16 = 0xFF00
	StatusPendingWarning uint16 = 0xFF01
	CFindStatusCancel    uint16 = 0xFE00
	// CFindStatusOutOfResources is a common C-FIND refusal/failure status.
	CFindStatusOutOfResources uint16 = 0xA700
	// CFindStatusUnableToProcess is a common C-FIND processing failure status.
	CFindStatusUnableToProcess uint16 = 0xC000
)

// CFindStatusCategory categorizes a C-FIND-RSP status value for receive-loop
// control.
type CFindStatusCategory int

const (
	CFindStatusInvalid CFindStatusCategory = iota
	// CFindStatusPending indicates a pending match; caller should continue reading.
	CFindStatusPending
	// CFindStatusSuccess indicates final success; caller should stop reading.
	CFindStatusSuccess
	// CFindStatusFailure indicates a final non-success (warning/failure/cancel/etc).
	CFindStatusFailure
)

func (c CFindStatusCategory) String() string {
	switch c {
	case CFindStatusPending:
		return "pending"
	case CFindStatusSuccess:
		return "success"
	case CFindStatusFailure:
		return "failure"
	default:
		return "invalid"
	}
}

// ErrCFindInvalidStatus identifies a C-FIND-RSP status that is not a valid
// pending, success, cancel, warning, or failure status for C-FIND flow control.
var ErrCFindInvalidStatus = errors.New("dicom dimse: invalid C-FIND status")

// CFindInvalidStatusError reports an invalid C-FIND-RSP status value.
type CFindInvalidStatusError struct {
	Status uint16
}

func (e *CFindInvalidStatusError) Error() string {
	if e == nil {
		return ErrCFindInvalidStatus.Error()
	}
	return fmt.Sprintf("%s 0x%04X", ErrCFindInvalidStatus, e.Status)
}

func (e *CFindInvalidStatusError) Unwrap() error {
	return ErrCFindInvalidStatus
}

// CategorizeCFindStatus returns how a C-FIND SCU should interpret status values.
func CategorizeCFindStatus(status uint16) CFindStatusCategory {
	switch status {
	case StatusPending, StatusPendingWarning:
		return CFindStatusPending
	case StatusSuccess:
		return CFindStatusSuccess
	case CFindStatusCancel, 0x0001,
		0x0122, 0x0124,
		0x0210, 0x0211, 0x0212:
		return CFindStatusFailure
	}
	if (status >= 0xA000 && status <= 0xAFFF) ||
		(status >= 0xB000 && status <= 0xBFFF) ||
		(status >= 0xC000 && status <= 0xCFFF) {
		return CFindStatusFailure
	}
	return CFindStatusInvalid
}

// CFindStatusError formats a final non-success response as an error.
//
// It returns nil for pending/success.
func CFindStatusError(resp *CFindResponse) error {
	if resp == nil {
		return fmt.Errorf("dicom dimse: nil C-FIND response")
	}
	cat := CategorizeCFindStatus(resp.Status)
	if cat == CFindStatusPending || cat == CFindStatusSuccess {
		return nil
	}
	if cat == CFindStatusInvalid {
		return &CFindInvalidStatusError{Status: resp.Status}
	}
	if resp.ErrorComment != "" {
		return fmt.Errorf("dicom dimse: C-FIND failed with status 0x%04X: %s", resp.Status, resp.ErrorComment)
	}
	return fmt.Errorf("dicom dimse: C-FIND failed with status 0x%04X", resp.Status)
}
