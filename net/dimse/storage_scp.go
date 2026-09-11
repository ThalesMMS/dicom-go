package dimse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/telemetry"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	StatusCStoreOutOfResources      uint16 = 0xA700
	StatusCStoreDataSetDoesNotMatch uint16 = 0xA900
	StatusCStoreCannotUnderstand    uint16 = 0xC000
)

// CStoreRequestContext describes one validated C-STORE request and decoded
// dataset delivered to a Storage SCP handler.
type CStoreRequestContext struct {
	Request               CStoreRequest
	PresentationContextID byte
	PresentationContext   ul.AcceptedContext
	DataSet               *object.Object
	DataSetSyntax         transfer.Syntax
}

type CStoreHandler interface {
	Store(context.Context, CStoreRequestContext) (uint16, error)
}

type CStoreHandlerFunc func(context.Context, CStoreRequestContext) (uint16, error)

func (f CStoreHandlerFunc) Store(ctx context.Context, req CStoreRequestContext) (uint16, error) {
	if f == nil {
		return StatusCStoreCannotUnderstand, errors.New("dicom dimse: nil C-STORE handler")
	}
	return f(ctx, req)
}

// CStoreSCPError lets handlers choose the C-STORE response status and Error
// Comment while retaining an underlying application error for errors.Is and
// errors.As. A zero Status leaves the handler's separately returned status in
// effect.
type CStoreSCPError struct {
	Status       uint16
	ErrorComment string
	Err          error
}

func NewCStoreSCPError(status uint16, comment string, err error) *CStoreSCPError {
	return &CStoreSCPError{Status: status, ErrorComment: comment, Err: err}
}

func (e *CStoreSCPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil && e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-STORE SCP status 0x%04X: %s: %v", e.Status, e.ErrorComment, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("dicom dimse: C-STORE SCP status 0x%04X: %v", e.Status, e.Err)
	}
	if e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-STORE SCP status 0x%04X: %s", e.Status, e.ErrorComment)
	}
	return fmt.Sprintf("dicom dimse: C-STORE SCP status 0x%04X", e.Status)
}

func (e *CStoreSCPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type StorageSCPOptions struct {
	StoreHandler          CStoreHandler
	MaxDataSetBytes       int64
	MaxElementBytes       int64
	MaxElements           int
	MaxSequenceDepth      int
	MaxPixelDataBytes     int64
	MaxPixelDataFragments int
	OnCStoreResponse      func(context.Context, CStoreRequestContext, uint16)
}

// ServeStorageAssociation handles C-ECHO and C-STORE commands on one accepted
// association until the peer releases it or a transport/protocol error occurs.
func ServeStorageAssociation(ctx context.Context, assoc *ul.Association, opts StorageSCPOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	if opts.StoreHandler == nil {
		return fmt.Errorf("dicom dimse: missing C-STORE handler")
	}

	for {
		pcID, command, released, err := receiveStorageCommandOrRelease(ctx, assoc)
		if err != nil || released {
			return err
		}
		field, err := CommandUint16(command, CommandField)
		if err != nil {
			return err
		}
		switch field {
		case CEchoRQ:
			if err := serveStorageCEcho(ctx, assoc, pcID, command); err != nil {
				return err
			}
		case CStoreRQ:
			if err := serveStorageCStore(ctx, assoc, pcID, command, opts); err != nil {
				return err
			}
		default:
			return fmt.Errorf("dicom dimse: unsupported Storage SCP command field 0x%04X", field)
		}
	}
}

func ValidateCStoreDataSet(affectedSOPClassUID, affectedSOPInstanceUID string, pc ul.AcceptedContext, dataset *object.Object) error {
	if dataset == nil {
		return errors.New("missing dataset")
	}
	if affectedSOPClassUID == "" {
		return errors.New("missing affected SOP Class UID")
	}
	if affectedSOPInstanceUID == "" {
		return errors.New("missing affected SOP Instance UID")
	}
	if pc.AbstractSyntaxUID != affectedSOPClassUID {
		return fmt.Errorf("presentation context SOP Class UID %q does not match request %q", pc.AbstractSyntaxUID, affectedSOPClassUID)
	}
	if sopClassUID, ok := dataset.GetUID(core.NewTag(0x0008, 0x0016)); !ok || sopClassUID != affectedSOPClassUID {
		return errors.New("dataset SOP Class UID does not match request")
	}
	if sopInstanceUID, ok := dataset.GetUID(core.NewTag(0x0008, 0x0018)); !ok || sopInstanceUID != affectedSOPInstanceUID {
		return errors.New("dataset SOP Instance UID does not match request")
	}
	return nil
}

func receiveStorageCommandOrRelease(ctx context.Context, assoc *ul.Association) (byte, *object.Object, bool, error) {
	var (
		pcID     byte
		havePCID bool
		buf      []byte
	)
	for {
		values, released, err := receiveStoragePDataOrRelease(ctx, assoc)
		if err != nil || released {
			return 0, nil, released, err
		}
		for i, value := range values {
			if !value.IsCommand {
				return 0, nil, false, fmt.Errorf("dicom dimse: Storage SCP expected command P-DATA")
			}
			if !havePCID {
				pcID = value.PresentationContextID
				havePCID = true
			} else if value.PresentationContextID != pcID {
				return 0, nil, false, fmt.Errorf("%w: got %d, want %d", ErrPresentationContextMismatch, value.PresentationContextID, pcID)
			}
			buf, err = appendCommandSetFragment(buf, value.Data)
			if err != nil {
				return 0, nil, false, err
			}
			if value.IsLast {
				if err := storePDataCarryoverWithContext(ctx, assoc, values[i+1:]); err != nil {
					return 0, nil, false, err
				}
				command, err := DecodeCommandSet(buf)
				if err == nil {
					observeCommand(assoc, telemetry.Inbound, pcID, command, 0)
				}
				return pcID, command, false, err
			}
		}
		ctx = ul.WithActiveTransfer(ctx)
	}
}

func receiveStoragePDataOrRelease(ctx context.Context, assoc *ul.Association) ([]ul.PDataValue, bool, error) {
	values, err := takePDataCarryoverWithContext(ctx, assoc)
	if err != nil {
		return nil, false, err
	}
	if len(values) > 0 {
		return values, false, nil
	}
	pdu, err := assoc.Receive(ctx)
	if err != nil {
		return nil, false, err
	}
	switch pdu := pdu.(type) {
	case *ul.ReleaseRQ:
		return nil, true, assoc.WritePDU(&ul.ReleaseRP{})
	case *ul.PDataTF:
		return pdu.Values, false, nil
	default:
		return nil, false, fmt.Errorf("%w: got %T while waiting for P-DATA-TF or A-RELEASE-RQ", ul.ErrUnexpectedPDU, pdu)
	}
}

func serveStorageCEcho(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object) error {
	messageID, err := parseStorageCEchoRequest(command)
	if err != nil {
		return err
	}
	return sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
		return SendCEchoResponseWithContext(responseCtx, assoc, pcID, messageID, StatusSuccess)
	})
}

func parseStorageCEchoRequest(command *object.Object) (uint16, error) {
	if field, err := CommandUint16(command, CommandField); err != nil {
		return 0, err
	} else if field != CEchoRQ {
		return 0, fmt.Errorf("dicom dimse: command field 0x%04X, want C-ECHO-RQ 0x%04X", field, CEchoRQ)
	}
	dataSetType, err := CommandUint16(command, CommandDataSetType)
	if err != nil {
		return 0, err
	}
	if dataSetType != NoDataSet {
		return 0, fmt.Errorf("dicom dimse: C-ECHO request dataset type 0x%04X, want no dataset 0x%04X", dataSetType, NoDataSet)
	}
	return CommandUint16(command, MessageID)
}

func serveStorageCStore(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, opts StorageSCPOptions) error {
	req, err := ParseCStoreRequest(command)
	if err != nil {
		return err
	}
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	reqCtx := CStoreRequestContext{
		Request:               *req,
		PresentationContextID: pcID,
		PresentationContext:   pc,
	}
	syntax, err := TransferSyntaxForAcceptedContext(pc)
	if err != nil {
		if opts.OnCStoreResponse != nil {
			opts.OnCStoreResponse(ctx, reqCtx, StatusCStoreCannotUnderstand)
		}
		return sendStorageCStoreResponse(ctx, assoc, pcID, req, StatusCStoreCannotUnderstand, "")
	}
	reqCtx.DataSetSyntax = syntax

	var status uint16
	errorComment := ""
	dataset, err := receiveStorageDataSet(ctx, assoc, pcID, syntax, opts)
	if err != nil {
		if !shouldDrainDataSetPDataOnError(err) {
			return err
		}
		status = storageDataSetErrorStatus(err)
	} else if err := ValidateCStoreDataSet(req.AffectedSOPClassUID, req.AffectedSOPInstanceUID, pc, dataset); err != nil {
		reqCtx.DataSet = dataset
		status = StatusCStoreDataSetDoesNotMatch
	} else {
		reqCtx.DataSet = dataset
		status, err = opts.StoreHandler.Store(ctx, reqCtx)
		var scpErr *CStoreSCPError
		if errors.As(err, &scpErr) && scpErr != nil {
			if scpErr.Status != 0 {
				status = scpErr.Status
			}
			errorComment = scpErr.ErrorComment
		}
		if err != nil && (status == 0 || status == StatusSuccess) {
			status = StatusCStoreCannotUnderstand
		} else if status == 0 {
			status = StatusSuccess
		}
	}
	if opts.OnCStoreResponse != nil {
		opts.OnCStoreResponse(ctx, reqCtx, status)
	}

	return sendStorageCStoreResponse(ctx, assoc, pcID, req, status, errorComment)
}

func receiveStorageDataSet(ctx context.Context, assoc *ul.Association, pcID byte, syntax transfer.Syntax, opts StorageSCPOptions) (*object.Object, error) {
	ctx = dataSetReadContext(ctx, assoc)
	reader := newTypedPDataReaderWithContext(ctx, assoc, pcID, false)
	maxElementBytes := opts.MaxElementBytes
	if maxElementBytes == 0 {
		maxElementBytes = opts.MaxDataSetBytes
	}
	maxPixelDataBytes := opts.MaxPixelDataBytes
	if maxPixelDataBytes == 0 {
		maxPixelDataBytes = opts.MaxDataSetBytes
	}
	dataset, err := object.ReadDataSetWithOptions(reader, syntax, object.ReadFileOptions{
		MaxElementBytes:   maxElementBytes,
		MaxTotalBytes:     opts.MaxDataSetBytes,
		MaxElements:       opts.MaxElements,
		MaxSequenceDepth:  opts.MaxSequenceDepth,
		MaxPixelDataBytes: maxPixelDataBytes,
		MaxFragments:      opts.MaxPixelDataFragments,
	})
	if err != nil {
		if shouldDrainDataSetPDataOnError(err) {
			if drainErr := drainPDataReader(reader); drainErr != nil {
				return nil, drainErr
			}
		}
		return nil, err
	}
	return dataset, nil
}

func drainPDataReader(reader *PDataReader) error {
	_, err := io.Copy(io.Discard, reader)
	return err
}

func shouldDrainDataSetPDataOnError(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, net.ErrClosed) &&
		!errors.Is(err, ul.ErrAssociationAborted) &&
		!errors.Is(err, ul.ErrAssociationTimeout) &&
		!errors.Is(err, context.Canceled) &&
		!errors.Is(err, context.DeadlineExceeded)
}

func storageDataSetErrorStatus(err error) uint16 {
	if errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxDepthExceeded) ||
		errors.Is(err, parser.ErrMaxPixelDataBytesExceeded) ||
		errors.Is(err, parser.ErrMaxFragmentsExceeded) {
		return StatusCStoreOutOfResources
	}
	return StatusCStoreCannotUnderstand
}

func sendStorageCStoreResponse(ctx context.Context, assoc *ul.Association, pcID byte, req *CStoreRequest, status uint16, errorComment string) error {
	return sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
		return SendCommandSetWithContext(responseCtx, assoc, pcID, CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    status,
			ErrorComment:              errorComment,
		}.CommandSet())
	})
}
