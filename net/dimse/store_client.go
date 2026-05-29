package dimse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// StoreClient provides a small, stable API surface for C-STORE (Storage SCU)
// workflows. It mirrors RetrieveClient: one client per established association,
// driving a single outstanding operation at a time.
//
// Unlike RetrieveClient, Store normally derives its presentation context from
// the dataset's SOP Class UID (0008,0016). Callers that already know which
// transfer syntaxes they can encode may constrain that lookup with
// CStoreOptions.TransferSyntaxUIDs. The accepted transfer syntax is then used to
// encode the dataset, keeping the SCU honest: it always sends in a syntax the
// SCP agreed to during association negotiation.
//
// The caller is responsible for:
//   - Establishing the UL association with a presentation context whose Abstract
//     Syntax is the dataset's SOP Class UID.
//   - Releasing the association when done.
//
// Limitations:
//   - One outstanding operation per association (no multiplexing).
//   - No C-CANCEL; context cancellation during command, payload, or response
//     leaves the association state uncertain and requires abort/cleanup.
type StoreClient struct {
	Assoc *ul.Association

	// messageID is incremented per Store call so successive stores on one
	// association carry distinct Message IDs.
	messageID uint16
}

// CStoreOptions configures one C-STORE exchange.
type CStoreOptions struct {
	AffectedSOPClassUID    string
	AffectedSOPInstanceUID string

	// TransferSyntaxUIDs optionally constrains presentation context selection
	// to syntaxes the caller can encode without transcoding, in preference
	// order. Empty preserves the historical first SOP-class match behavior.
	TransferSyntaxUIDs []string

	MessageID                    uint16
	Priority                     uint16
	MoveOriginatorAETitle        string
	MoveOriginatorMessageIDOrNil *uint16
}

// CStoreResult describes the negotiated context and response for one C-STORE
// exchange.
type CStoreResult struct {
	Request             CStoreRequest
	Response            *CStoreResponse
	PresentationContext ul.AcceptedContext
	TransferSyntax      transfer.Syntax
}

// CStoreStatusError reports a non-success, non-warning C-STORE response status.
type CStoreStatusError struct {
	Response *CStoreResponse
}

func (e *CStoreStatusError) Error() string {
	if e == nil || e.Response == nil {
		return "dicom dimse: C-STORE failed"
	}
	if e.Response.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-STORE failed with status 0x%04X: %s", e.Response.Status, e.Response.ErrorComment)
	}
	return fmt.Sprintf("dicom dimse: C-STORE failed with status 0x%04X", e.Response.Status)
}

// NewStoreClient constructs a StoreClient bound to an established association.
func NewStoreClient(assoc *ul.Association) *StoreClient {
	return &StoreClient{Assoc: assoc}
}

// Store executes a complete C-STORE exchange for one dataset and returns the
// final response. The dataset must carry SOP Class UID (0008,0016) and SOP
// Instance UID (0008,0018); both are echoed into the C-STORE request so the SCP
// can validate them against the encoded dataset.
func (c *StoreClient) Store(ctx context.Context, dataset *object.Object) (*CStoreResponse, error) {
	result, err := c.StoreWithOptions(ctx, dataset, CStoreOptions{})
	var statusErr *CStoreStatusError
	if errors.As(err, &statusErr) {
		return result.Response, nil
	}
	return result.Response, err
}

// StoreWithOptions executes a complete C-STORE exchange for one dataset and
// returns the negotiated presentation context, transfer syntax, request, and
// response. Affected SOP UID options override values read from the dataset.
func (c *StoreClient) StoreWithOptions(ctx context.Context, dataset *object.Object, opts CStoreOptions) (CStoreResult, error) {
	return c.storeWithOptions(ctx, dataset, opts, ReceiveCStoreResponseWithContext)
}

type cStoreResponseReceiver func(context.Context, *ul.Association, byte) (*CStoreResponse, error)

// CStoreDataSetWriter writes one complete DIMSE data set in the negotiated
// transfer syntax. Implementations may encode an object or stream existing
// Part 10 data set bytes verbatim. They must not write a Part 10 preamble or
// File Meta Information.
type CStoreDataSetWriter func(context.Context, io.Writer, transfer.Syntax) error

func (c *StoreClient) storeWithOptions(ctx context.Context, dataset *object.Object, opts CStoreOptions, receiveResponse cStoreResponseReceiver) (CStoreResult, error) {
	if c == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: store client is nil")
	}
	if opts.Priority > 2 {
		return CStoreResult{}, ErrStoreInvalidOptions
	}
	if c.Assoc == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: store client association is nil")
	}
	if dataset == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: store client dataset is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CStoreResult{}, err
	}
	sopClassUID := strings.TrimSpace(opts.AffectedSOPClassUID)
	if sopClassUID == "" {
		uid, ok := dataset.GetUID(core.NewTag(0x0008, 0x0016))
		if !ok || uid == "" {
			return CStoreResult{}, fmt.Errorf("dicom dimse: dataset missing SOP Class UID (0008,0016)")
		}
		sopClassUID = uid
	}
	sopInstanceUID := strings.TrimSpace(opts.AffectedSOPInstanceUID)
	if sopInstanceUID == "" {
		uid, ok := dataset.GetUID(core.NewTag(0x0008, 0x0018))
		if !ok || uid == "" {
			return CStoreResult{}, fmt.Errorf("dicom dimse: dataset missing SOP Instance UID (0008,0018)")
		}
		sopInstanceUID = uid
	}
	opts.AffectedSOPClassUID = sopClassUID
	opts.AffectedSOPInstanceUID = sopInstanceUID
	return c.storeEncodedWithOptions(ctx, func(_ context.Context, destination io.Writer, syntax transfer.Syntax) error {
		return object.WriteDataSet(destination, dataset, syntax)
	}, opts, receiveResponse)
}

// StoreEncodedWithOptions executes C-STORE for a caller-supplied data set
// writer. Both affected SOP UIDs are required in opts because this path does
// not materialize a dataset from which they could be derived.
func (c *StoreClient) StoreEncodedWithOptions(ctx context.Context, writeDataSet CStoreDataSetWriter, opts CStoreOptions) (CStoreResult, error) {
	return c.storeEncodedWithOptions(ctx, writeDataSet, opts, ReceiveCStoreResponseWithContext)
}

func (c *StoreClient) storeEncodedWithOptions(ctx context.Context, writeDataSet CStoreDataSetWriter, opts CStoreOptions, receiveResponse cStoreResponseReceiver) (CStoreResult, error) {
	if c == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: store client is nil")
	}
	if opts.Priority > 2 {
		return CStoreResult{}, ErrStoreInvalidOptions
	}
	if c.Assoc == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: store client association is nil")
	}
	if writeDataSet == nil {
		return CStoreResult{}, fmt.Errorf("dicom dimse: C-STORE dataset writer is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return CStoreResult{}, err
	}
	sopClassUID := strings.TrimSpace(opts.AffectedSOPClassUID)
	if sopClassUID == "" {
		return CStoreResult{}, fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Class UID")
	}
	sopInstanceUID := strings.TrimSpace(opts.AffectedSOPInstanceUID)
	if sopInstanceUID == "" {
		return CStoreResult{}, fmt.Errorf("dicom dimse: missing C-STORE Affected SOP Instance UID")
	}
	finishOperation, err := beginAssociationOperation(c.Assoc)
	if err != nil {
		return CStoreResult{}, err
	}
	defer finishOperation()

	var pc ul.AcceptedContext
	if len(opts.TransferSyntaxUIDs) > 0 {
		pc, err = AcceptedContextForSOPClassTransferSyntaxes(c.Assoc, sopClassUID, opts.TransferSyntaxUIDs)
	} else {
		pc, err = AcceptedContextForSOPClass(c.Assoc, sopClassUID)
	}
	if err != nil {
		return CStoreResult{}, err
	}
	syntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID)
	if !ok {
		return CStoreResult{PresentationContext: pc}, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, pc.TransferSyntaxUID)
	}

	messageID := opts.MessageID
	if messageID == 0 {
		c.messageID++
		if c.messageID == 0 {
			c.messageID = 1
		}
		messageID = c.messageID
	} else {
		c.messageID = messageID
	}
	req := CStoreRequest{
		AffectedSOPClassUID:          sopClassUID,
		MessageID:                    messageID,
		Priority:                     opts.Priority,
		AffectedSOPInstanceUID:       sopInstanceUID,
		MoveOriginatorAETitle:        opts.MoveOriginatorAETitle,
		MoveOriginatorMessageIDOrNil: opts.MoveOriginatorMessageIDOrNil,
	}
	result := CStoreResult{
		Request:             req,
		PresentationContext: pc,
		TransferSyntax:      syntax,
	}
	if err := SendCStoreRequestWithContext(ctx, c.Assoc, pc.ID, req); err != nil {
		return result, newOperationError("C-STORE", err, true)
	}
	writer := NewPDataWriterWithContext(dataSetWriteContext(ctx), c.Assoc, pc.ID, false, peerMaxPDUWithHeader(c.Assoc))
	if err := writeDataSet(ctx, writer, syntax); err != nil {
		return result, newOperationError("C-STORE", fmt.Errorf("send dataset: %w", err), true)
	}
	if err := writer.Finish(); err != nil {
		return result, newOperationError("C-STORE", fmt.Errorf("finish dataset P-DATA: %w", err), true)
	}
	if receiveResponse == nil {
		receiveResponse = ReceiveCStoreResponseWithContext
	}
	response, err := receiveResponse(ctx, c.Assoc, pc.ID)
	if err != nil {
		return result, newOperationError("C-STORE", err, true)
	}
	result.Response = response
	if response.MessageIDBeingRespondedTo != req.MessageID {
		return result, newOperationError("C-STORE", fmt.Errorf("response message ID mismatch"), true)
	}
	if response.AffectedSOPInstanceUID != req.AffectedSOPInstanceUID {
		return result, newOperationError("C-STORE", fmt.Errorf("response SOP Instance UID mismatch"), true)
	}
	if err := CheckCStoreStatus(response); err != nil {
		return result, err
	}
	return result, nil
}

// IsCStoreWarningStatus reports statuses that represent C-STORE warnings.
func IsCStoreWarningStatus(status uint16) bool {
	return status == 0x0001 || status == 0x0107 || status == 0x0116 || (status >= 0xB000 && status <= 0xBFFF)
}

// CheckCStoreStatus returns an error for non-success, non-warning C-STORE
// response statuses.
func CheckCStoreStatus(response *CStoreResponse) error {
	if response == nil {
		return fmt.Errorf("dicom dimse: nil C-STORE response")
	}
	if response.Status == StatusSuccess || IsCStoreWarningStatus(response.Status) {
		return nil
	}
	return &CStoreStatusError{Response: response}
}
