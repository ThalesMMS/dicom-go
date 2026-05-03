package dimse

import (
	"context"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/ul"
)

// RetrieveDialOptions describes how to establish an association suitable for
// scoped Query/Retrieve (C-MOVE) operations.
//
// This is a convenience wrapper around ul.DialContext.
//
// Notes:
//   - Timeouts should be enforced by the provided context.
//   - Only one operation at a time is supported on a single association.
//   - Presentation contexts must include the appropriate Query/Retrieve SOP
//     Class UID(s).
type RetrieveDialOptions struct {
	Address string

	CalledAETitle  string
	CallingAETitle string

	MaxPDU uint32

	// Contexts are the proposed presentation contexts.
	Contexts []ul.PresentationContext

	// Implementation identifiers are passed through to UL negotiation.
	ImplementationClassUID    string
	ImplementationVersionName string
}

// DialRetrieveSCU establishes a UL association using the supplied options.
func DialRetrieveSCU(ctx context.Context, opts RetrieveDialOptions) (*ul.Association, error) {
	address := strings.TrimSpace(opts.Address)
	if address == "" {
		return nil, fmt.Errorf("dicom dimse: missing address")
	}
	if len(opts.Contexts) == 0 {
		return nil, fmt.Errorf("dicom dimse: no presentation contexts provided")
	}
	assoc, err := ul.DialContext(ctx, address, ul.DialOptions{
		CalledAETitle:             opts.CalledAETitle,
		CallingAETitle:            opts.CallingAETitle,
		MaxPDU:                    opts.MaxPDU,
		Contexts:                  opts.Contexts,
		ImplementationClassUID:    opts.ImplementationClassUID,
		ImplementationVersionName: opts.ImplementationVersionName,
	})
	if err != nil {
		return nil, fmt.Errorf("dicom dimse: dial retrieve SCU address=%q calledAET=%q callingAET=%q maxPDU=%d contexts=%d: %w", address, opts.CalledAETitle, opts.CallingAETitle, opts.MaxPDU, len(opts.Contexts), err)
	}
	return assoc, nil
}
