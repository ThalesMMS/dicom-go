package dimse

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ThalesMMS/dicom-go/net/telemetry"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

// AssociationSCPOptions wires the DIMSE services handled by ServeAssociation.
type AssociationSCPOptions struct {
	StorageSCPOptions
	Controls      SCPControls
	CFindHandler  CFindHandler
	CMoveHandler  CMoveHandler
	CGetHandler   CGetHandler
	NormalizedSCP *NormalizedSCPOptions
}

// ServeAssociation dispatches DIMSE requests received on one accepted
// association until the peer releases it or a transport/protocol error occurs.
func ServeAssociation(ctx context.Context, assoc *ul.Association, opts AssociationSCPOptions) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if assoc == nil {
		return fmt.Errorf("dicom dimse: nil association")
	}
	releaseOperation, err := beginAssociationOperation(assoc)
	if err != nil {
		return err
	}
	defer releaseOperation()
	if err := opts.Controls.validate(); err != nil {
		return err
	}
	ctx = withSCPControls(ctx, opts.Controls)

	for {
		pcID, command, released, err := receiveStorageCommandOrRelease(commandReadContext(ctx), assoc)
		if err != nil || released {
			return err
		}
		field, err := CommandUint16(command, CommandField)
		if err != nil {
			return err
		}
		started := time.Now()
		assoc.RecordOperationObservation(telemetry.OperationObservation{
			PresentationContextID: pcID,
			CommandField:          field,
		})
		operationCtx := ctx
		cancelOperation := func() {}
		if opts.Controls.OperationTimeout > 0 {
			operationCtx, cancelOperation = context.WithTimeout(ctx, opts.Controls.OperationTimeout)
		}
		operationCtx = withSCPResponseContext(operationCtx, ctx)
		_, err = runSCPHandler(operationCtx, assoc, opts.Controls.CancelGrace, func(handlerCtx context.Context) (struct{}, error) {
			return struct{}{}, serveAssociationCommand(handlerCtx, assoc, pcID, command, field, opts)
		})
		cancelOperation()
		assoc.RecordOperationObservation(telemetry.OperationObservation{
			Completed:             true,
			PresentationContextID: pcID,
			CommandField:          field,
			Duration:              time.Since(started),
			ErrorClass:            associationOperationErrorClass(err),
		})
		if err == nil {
			continue
		}
		if isHandledSCPStatusError(err) || isHandledNormalizedSCPError(err) {
			continue
		}
		return err
	}
}

func serveAssociationCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, field uint16, opts AssociationSCPOptions) error {
	switch field {
	case CEchoRQ:
		return serveStorageCEcho(ctx, assoc, pcID, command)
	case CStoreRQ:
		if opts.StoreHandler == nil {
			return fmt.Errorf("dicom dimse: missing C-STORE handler")
		}
		return serveStorageCStore(ctx, assoc, pcID, command, opts.StorageSCPOptions)
	case CFindRQ:
		if opts.CFindHandler == nil {
			return fmt.Errorf("dicom dimse: missing C-FIND handler")
		}
		return serveAssociationCFindCommand(ctx, assoc, pcID, command, opts.CFindHandler)
	case CMoveRQ:
		if opts.CMoveHandler == nil {
			return fmt.Errorf("dicom dimse: missing C-MOVE handler")
		}
		return serveAssociationCMoveCommand(ctx, assoc, pcID, command, opts.CMoveHandler)
	case CGetRQ:
		if opts.CGetHandler == nil {
			return fmt.Errorf("dicom dimse: missing C-GET handler")
		}
		return serveAssociationCGetCommand(ctx, assoc, pcID, command, opts.CGetHandler)
	case NEventReportRQ, NGetRQ, NSetRQ, NActionRQ, NCreateRQ, NDeleteRQ:
		options := NormalizedSCPOptions{}
		if opts.NormalizedSCP != nil {
			options = *opts.NormalizedSCP
		}
		return ServeNormalizedCommand(ctx, assoc, pcID, command, options)
	default:
		return fmt.Errorf("dicom dimse: unsupported DIMSE SCP command field 0x%04X", field)
	}
}

func associationOperationErrorClass(err error) string {
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, ul.ErrAssociationTimeout):
		return telemetry.ErrorClassTimeout
	case errors.Is(err, context.Canceled), errors.Is(err, ErrCFindCanceled), errors.Is(err, ErrCMoveCanceled), errors.Is(err, ErrCGetCanceled):
		return telemetry.ErrorClassCanceled
	case isHandledSCPStatusError(err), isHandledNormalizedSCPError(err):
		return telemetry.ErrorClassDIMSEStatus
	case errors.Is(err, ul.ErrUnexpectedPDU), errors.Is(err, ErrPresentationContextMismatch), errors.Is(err, ErrCommandSetTooLarge):
		return telemetry.ErrorClassProtocol
	default:
		return telemetry.ErrorClassHandlerOrTransport
	}
}

func serveAssociationCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CFindHandler) error {
	sopClassUID, err := commandUID(command, AffectedSOPClassUID)
	if err != nil {
		return err
	}
	switch sopClassUID {
	case PatientRootFindSOPClassUID:
		return servePatientRootCFindCommand(ctx, assoc, pcID, command, handler)
	case StudyRootFindSOPClassUID:
		return serveStudyRootCFindCommand(ctx, assoc, pcID, command, handler)
	case ModalityWorklistFindSOPClassUID:
		router, ok := handler.(interface {
			serveModalityWorklist(context.Context, *ul.Association, byte, *object.Object) error
		})
		if !ok {
			return fmt.Errorf("dicom dimse: Modality Worklist C-FIND handler is not configured")
		}
		return router.serveModalityWorklist(ctx, assoc, pcID, command)
	default:
		router, ok := handler.(interface {
			serveStreamingCFind(context.Context, *ul.Association, byte, *object.Object, string) (bool, error)
		})
		if ok {
			handled, routeErr := router.serveStreamingCFind(ctx, assoc, pcID, command, sopClassUID)
			if handled {
				return routeErr
			}
		}
		return fmt.Errorf("dicom dimse: unsupported C-FIND SOP Class")
	}
}

func serveAssociationCMoveCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CMoveHandler) error {
	sopClassUID, err := commandUID(command, AffectedSOPClassUID)
	if err != nil {
		return err
	}
	if sopClassUID == PatientRootMoveSOPClassUID {
		return servePatientRootCMoveCommand(ctx, assoc, pcID, command, handler)
	}
	return serveStudyRootCMoveCommand(ctx, assoc, pcID, command, handler)
}

func serveAssociationCGetCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CGetHandler) error {
	sopClassUID, err := commandUID(command, AffectedSOPClassUID)
	if err != nil {
		return err
	}
	if sopClassUID == PatientRootGetSOPClassUID {
		return servePatientRootCGetCommand(ctx, assoc, pcID, command, handler)
	}
	return serveStudyRootCGetCommand(ctx, assoc, pcID, command, handler)
}

func isHandledSCPStatusError(err error) bool {
	if errors.Is(err, ErrCFindCanceled) || errors.Is(err, ErrCMoveCanceled) || errors.Is(err, ErrCGetCanceled) ||
		errors.Is(err, ErrModalityWorklistIdentifier) || errors.Is(err, ErrModalityWorklistProvider) || errors.Is(err, ErrModalityWorklistResourceLimit) ||
		errors.Is(err, ErrStreamingCFindIdentifier) || errors.Is(err, ErrStreamingCFindProvider) || errors.Is(err, ErrStreamingCFindResourceLimit) {
		return true
	}
	var cfindErr *CFindSCPError
	if errors.As(err, &cfindErr) {
		return true
	}
	var cmoveErr *CMoveSCPError
	if errors.As(err, &cmoveErr) {
		return true
	}
	var cgetErr *CGetSCPError
	return errors.As(err, &cgetErr)
}
