package dimse

import "fmt"

// C-FIND response status codes used for flow control.
//
// DICOM PS3.7 defines two Pending status codes (0xFF00 / 0xFF01) and Success
// (0x0000). Other values indicate warning/failure/cancel/etc.
const (
	StatusPending        uint16 = 0xFF00
	StatusPendingWarning uint16 = 0xFF01
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

// CategorizeCFindStatus returns how a C-FIND SCU should interpret status values.
func CategorizeCFindStatus(status uint16) CFindStatusCategory {
	switch status {
	case StatusPending, StatusPendingWarning:
		return CFindStatusPending
	case StatusSuccess:
		return CFindStatusSuccess
	default:
		return CFindStatusFailure
	}
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
	if resp.ErrorComment != "" {
		return fmt.Errorf("dicom dimse: C-FIND failed with status 0x%04X: %s", resp.Status, resp.ErrorComment)
	}
	return fmt.Errorf("dicom dimse: C-FIND failed with status 0x%04X", resp.Status)
}
