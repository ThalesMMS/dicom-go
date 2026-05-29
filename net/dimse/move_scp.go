package dimse

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var ErrCMoveCanceled = errors.New("dicom dimse: C-MOVE canceled")

const maxCMoveSubOperations = 1<<16 - 1

// CMoveRequestContext is the decoded request passed to a C-MOVE SCP handler.
type CMoveRequestContext struct {
	Request               CMoveRequest
	Identifier            *object.Object
	QueryRetrieveLevel    string
	PresentationContextID byte
	IdentifierSyntax      transfer.Syntax
}

// CMoveSubOperation represents one storage sub-operation opened by a C-MOVE
// SCP handler.
type CMoveSubOperation struct {
	AffectedSOPClassUID    string
	AffectedSOPInstanceUID string
	Store                  func(context.Context) CMoveSubOperationResult
}

// CMoveSubOperationResult reports the final status of one C-STORE sub-operation.
type CMoveSubOperationResult struct {
	Status uint16
	Err    error
}

// CMoveHandler resolves matching instances for a C-MOVE request and returns
// storage sub-operations that deliver them to the requested Move Destination.
type CMoveHandler interface {
	Move(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error)
}

// CMoveHandlerFunc adapts a function to CMoveHandler.
type CMoveHandlerFunc func(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error)

func (f CMoveHandlerFunc) Move(ctx context.Context, req CMoveRequestContext) ([]CMoveSubOperation, error) {
	if f == nil {
		return nil, fmt.Errorf("dicom dimse: nil C-MOVE handler")
	}
	return f(ctx, req)
}

// CMoveSCPError lets handlers choose a final C-MOVE status and Error Comment.
type CMoveSCPError struct {
	Status       uint16
	ErrorComment string
	Err          error
}

func NewCMoveSCPError(status uint16, comment string, err error) *CMoveSCPError {
	return &CMoveSCPError{Status: status, ErrorComment: comment, Err: err}
}

func (e *CMoveSCPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil && e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-MOVE SCP status 0x%04X: %s: %v", e.Status, e.ErrorComment, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("dicom dimse: C-MOVE SCP status 0x%04X: %v", e.Status, e.Err)
	}
	if e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-MOVE SCP status 0x%04X: %s", e.Status, e.ErrorComment)
	}
	return fmt.Sprintf("dicom dimse: C-MOVE SCP status 0x%04X", e.Status)
}

func (e *CMoveSCPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ReceiveCMoveRequestIdentifier(assoc *ul.Association, pcID byte, identifierSyntax transfer.Syntax) (*CMoveRequest, *object.Object, error) {
	req, err := ReceiveCMoveRequest(assoc, pcID)
	if err != nil {
		return nil, nil, err
	}
	identifier, err := receiveIdentifierDataSetWithContext(nil, assoc, pcID, identifierSyntax)
	if err != nil {
		return nil, nil, err
	}
	return req, identifier, nil
}

func ServeStudyRootCMove(ctx context.Context, assoc *ul.Association, pcID byte, handler CMoveHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-MOVE handler")
	}
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return err
	}
	return serveStudyRootCMoveCommand(ctx, assoc, pcID, command, handler)
}

func ServePatientRootCMove(ctx context.Context, assoc *ul.Association, pcID byte, handler CMoveHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-MOVE handler")
	}
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return err
	}
	return servePatientRootCMoveCommand(ctx, assoc, pcID, command, handler)
}

func serveStudyRootCMoveCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CMoveHandler) error {
	return serveCMoveCommand(ctx, assoc, pcID, command, handler, cMoveSCPModel{
		Name:        "Study Root MOVE",
		SOPClassUID: StudyRootMoveSOPClassUID,
		Level:       studyRootCMoveLevel,
	})
}

func servePatientRootCMoveCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CMoveHandler) error {
	return serveCMoveCommand(ctx, assoc, pcID, command, handler, cMoveSCPModel{
		Name:        "Patient Root MOVE",
		SOPClassUID: PatientRootMoveSOPClassUID,
		Level:       patientRootCMoveLevel,
	})
}

type cMoveSCPModel struct {
	Name        string
	SOPClassUID string
	Level       func(*object.Object) (string, error)
}

func serveCMoveCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CMoveHandler, model cMoveSCPModel) error {
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return err
	}
	req, err := ParseCMoveRequest(command)
	if err != nil {
		return err
	}
	identifier, err := receiveIdentifierDataSetWithContext(ctx, assoc, pcID, syntax)
	if err != nil {
		return err
	}
	if req.AffectedSOPClassUID != model.SOPClassUID {
		err := fmt.Errorf("dicom dimse: C-MOVE SCP SOP Class UID %q, want %s %q", req.AffectedSOPClassUID, model.Name, model.SOPClassUID)
		_ = sendCMoveResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusCMoveUnableToProcess, err.Error(), cMoveSubOperationCounts{})
		return err
	}
	level, err := model.Level(identifier)
	if err != nil {
		_ = sendCMoveResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusCMoveUnableToProcess, err.Error(), cMoveSubOperationCounts{})
		return err
	}
	if strings.TrimSpace(req.MoveDestination) == "" {
		err := fmt.Errorf("dicom dimse: C-MOVE request MoveDestination is required")
		_ = sendCMoveResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusCMoveMoveDestinationUnknown, err.Error(), cMoveSubOperationCounts{})
		return err
	}

	monitor := startSCPCancelMonitor(ctx, assoc, pcID, req.MessageID, ErrCMoveCanceled, false)
	defer monitor.Stop()
	operationCtx := monitor.Context()
	counts, err := runSCPHandler(operationCtx, assoc, scpControlsFromContext(ctx).CancelGrace, func(handlerCtx context.Context) (cMoveSubOperationCounts, error) {
		ops, handlerErr := handler.Move(handlerCtx, CMoveRequestContext{
			Request:               *req,
			Identifier:            identifier,
			QueryRetrieveLevel:    level,
			PresentationContextID: pcID,
			IdentifierSyntax:      syntax,
		})
		if handlerErr != nil {
			return cMoveSubOperationCounts{}, handlerErr
		}
		if validationErr := validateCMoveSubOperations(ops); validationErr != nil {
			return cMoveSubOperationCounts{}, validationErr
		}

		counts := cMoveSubOperationCounts{remaining: uint16(len(ops))}
		for i, op := range ops {
			if operationErr := monitor.OperationError(); operationErr != nil {
				return counts, operationErr
			}
			result := op.Store(handlerCtx)
			if errors.Is(result.Err, ErrCMoveCanceled) || errors.Is(result.Err, context.Canceled) {
				return counts, result.Err
			}
			counts.remaining--
			countCMoveSubOperationResult(&counts, result)
			if operationErr := monitor.OperationError(); operationErr != nil {
				return counts, operationErr
			}
			if i < len(ops)-1 {
				if sendErr := sendCMoveResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusPending, "", counts); sendErr != nil {
					return counts, sendErr
				}
			}
		}
		return counts, monitor.OperationError()
	})
	if monitorErr := monitor.Stop(); monitorErr != nil {
		err = errors.Join(err, monitorErr)
	}
	if err != nil {
		return sendCMoveOperationError(operationCtx, assoc, pcID, *req, model.SOPClassUID, counts, err)
	}

	status := StatusSuccess
	if counts.failed > 0 || counts.warning > 0 {
		status = StatusCMoveSubOperationsCompleteOneOrMoreFailures
	}
	return sendCMoveResponse(ctx, assoc, pcID, *req, model.SOPClassUID, status, "", counts)
}

func sendCMoveOperationError(ctx context.Context, assoc *ul.Association, pcID byte, req CMoveRequest, sopClassUID string, counts cMoveSubOperationCounts, err error) error {
	status, comment := cMoveSCPStatusForError(err)
	_ = sendCMoveResponse(ctx, assoc, pcID, req, sopClassUID, status, comment, counts)
	return err
}

type cMoveSubOperationCounts struct {
	remaining             uint16
	completed             uint16
	failed                uint16
	warning               uint16
	failedSOPInstanceUIDs []string
}

func studyRootCMoveLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-MOVE Identifier")
	}
	level, ok := identifier.GetString(core.NewTag(0x0008, 0x0052))
	if !ok || strings.TrimSpace(level) == "" {
		return "", fmt.Errorf("dicom dimse: missing QueryRetrieveLevel")
	}
	level = strings.ToUpper(strings.TrimSpace(level))
	switch level {
	case QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage:
		return level, nil
	default:
		return "", fmt.Errorf("dicom dimse: unsupported QueryRetrieveLevel %q", level)
	}
}

func patientRootCMoveLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-MOVE Identifier")
	}
	level, ok := identifier.GetString(core.NewTag(0x0008, 0x0052))
	if !ok || strings.TrimSpace(level) == "" {
		return "", fmt.Errorf("dicom dimse: missing QueryRetrieveLevel")
	}
	level = strings.ToUpper(strings.TrimSpace(level))
	switch level {
	case QueryRetrieveLevelPatient, QueryRetrieveLevelStudy, QueryRetrieveLevelSeries, QueryRetrieveLevelImage:
		return level, nil
	default:
		return "", fmt.Errorf("dicom dimse: unsupported QueryRetrieveLevel %q", level)
	}
}

func cMoveSCPStatusForError(err error) (uint16, string) {
	var statusErr *CMoveSCPError
	if errors.As(err, &statusErr) {
		comment := statusErr.ErrorComment
		if comment == "" && statusErr.Err != nil {
			comment = statusErr.Err.Error()
		}
		return statusErr.Status, comment
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusCMoveUnableToProcess, err.Error()
	}
	if errors.Is(err, ErrCMoveCanceled) || errors.Is(err, context.Canceled) {
		return StatusCMoveCancel, "C-MOVE canceled"
	}
	return StatusCMoveUnableToProcess, err.Error()
}

func validateCMoveSubOperations(ops []CMoveSubOperation) error {
	if len(ops) > maxCMoveSubOperations {
		return fmt.Errorf("dicom dimse: too many C-MOVE sub-operations: %d exceeds %d", len(ops), maxCMoveSubOperations)
	}
	for i, op := range ops {
		if op.AffectedSOPClassUID == "" {
			return fmt.Errorf("dicom dimse: C-MOVE sub-operation %d missing Affected SOP Class UID", i)
		}
		if op.AffectedSOPInstanceUID == "" {
			return fmt.Errorf("dicom dimse: C-MOVE sub-operation %d missing Affected SOP Instance UID", i)
		}
		if op.Store == nil {
			return fmt.Errorf("dicom dimse: C-MOVE sub-operation %d missing Store callback", i)
		}
	}
	return nil
}

func countCMoveSubOperationResult(counts *cMoveSubOperationCounts, result CMoveSubOperationResult) {
	if result.Err != nil {
		counts.failed++
		return
	}
	switch ClassifyCMoveStatus(result.Status) {
	case CMoveStatusSuccess:
		counts.completed++
	case CMoveStatusWarning:
		counts.warning++
	default:
		counts.failed++
	}
}

func sendStudyRootCMoveResponse(assoc *ul.Association, pcID byte, req CMoveRequest, status uint16, comment string, counts cMoveSubOperationCounts) error {
	return sendCMoveResponse(nil, assoc, pcID, req, StudyRootMoveSOPClassUID, status, comment, counts)
}

func sendCMoveResponse(ctx context.Context, assoc *ul.Association, pcID byte, req CMoveRequest, sopClassUID string, status uint16, comment string, counts cMoveSubOperationCounts) error {
	return sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
		return SendCMoveResponseWithContext(responseCtx, assoc, pcID, CMoveResponse{
			AffectedSOPClassUID:                 sopClassUID,
			MessageIDBeingRespondedTo:           req.MessageID,
			Status:                              status,
			ErrorComment:                        comment,
			NumberOfRemainingSuboperationsOrNil: remainingSuboperationsPtr(status, counts.remaining),
			NumberOfCompletedSuboperationsOrNil: uint16Ptr(counts.completed),
			NumberOfFailedSuboperationsOrNil:    uint16Ptr(counts.failed),
			NumberOfWarningSuboperationsOrNil:   uint16Ptr(counts.warning),
		})
	})
}

func uint16Ptr(v uint16) *uint16 {
	return &v
}

func remainingSuboperationsPtr(status, remaining uint16) *uint16 {
	if status != StatusPending && status != StatusCMoveCancel {
		return nil
	}
	return uint16Ptr(remaining)
}
