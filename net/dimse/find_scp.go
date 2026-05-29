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

var ErrCFindCanceled = errors.New("dicom dimse: C-FIND canceled")

// CFindRequestContext is the decoded request passed to a C-FIND SCP handler.
type CFindRequestContext struct {
	Request               CFindRequest
	Identifier            *object.Object
	QueryRetrieveLevel    string
	PresentationContextID byte
	IdentifierSyntax      transfer.Syntax
}

// CFindHandler provides matching Identifier datasets for a C-FIND request.
type CFindHandler interface {
	Find(context.Context, CFindRequestContext) ([]*object.Object, error)
}

// CFindHandlerFunc adapts a function to CFindHandler.
type CFindHandlerFunc func(context.Context, CFindRequestContext) ([]*object.Object, error)

func (f CFindHandlerFunc) Find(ctx context.Context, req CFindRequestContext) ([]*object.Object, error) {
	if f == nil {
		return nil, fmt.Errorf("dicom dimse: nil C-FIND handler")
	}
	return f(ctx, req)
}

// CFindSCPError lets handlers choose a final C-FIND status and Error Comment.
type CFindSCPError struct {
	Status       uint16
	ErrorComment string
	Err          error
}

func NewCFindSCPError(status uint16, comment string, err error) *CFindSCPError {
	return &CFindSCPError{Status: status, ErrorComment: comment, Err: err}
}

func (e *CFindSCPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil && e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-FIND SCP status 0x%04X: %s: %v", e.Status, e.ErrorComment, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("dicom dimse: C-FIND SCP status 0x%04X: %v", e.Status, e.Err)
	}
	if e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-FIND SCP status 0x%04X: %s", e.Status, e.ErrorComment)
	}
	return fmt.Sprintf("dicom dimse: C-FIND SCP status 0x%04X", e.Status)
}

func (e *CFindSCPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ReceiveCFindRequest(assoc *ul.Association, pcID byte, identifierSyntax transfer.Syntax) (*CFindRequest, *object.Object, error) {
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return nil, nil, err
	}
	req, err := ParseCFindRequest(command)
	if err != nil {
		return nil, nil, err
	}
	identifier, err := receiveIdentifierDataSetWithContext(nil, assoc, pcID, identifierSyntax)
	if err != nil {
		return nil, nil, err
	}
	return req, identifier, nil
}

func SendCFindResponse(assoc *ul.Association, pcID byte, rsp CFindResponse, identifier *object.Object, identifierSyntax transfer.Syntax) error {
	return SendCFindResponseWithContext(nil, assoc, pcID, rsp, identifier, identifierSyntax)
}

func SendCFindResponseWithContext(ctx context.Context, assoc *ul.Association, pcID byte, rsp CFindResponse, identifier *object.Object, identifierSyntax transfer.Syntax) error {
	if rsp.AffectedSOPClassUID == "" {
		return fmt.Errorf("dicom dimse: C-FIND response AffectedSOPClassUID is required")
	}
	if identifier == nil {
		rsp.CommandDataSetType = NoDataSet
	} else {
		rsp.CommandDataSetType = DataSetPresent
	}
	if err := SendCommandSetWithContext(ctx, assoc, pcID, rsp.CommandSet()); err != nil {
		return err
	}
	if identifier == nil {
		return nil
	}
	return SendDataSetWithContext(ctx, assoc, pcID, identifier, identifierSyntax)
}

func ServeStudyRootCFind(ctx context.Context, assoc *ul.Association, pcID byte, handler CFindHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-FIND handler")
	}
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return err
	}
	return serveStudyRootCFindCommand(ctx, assoc, pcID, command, handler)
}

func ServePatientRootCFind(ctx context.Context, assoc *ul.Association, pcID byte, handler CFindHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-FIND handler")
	}
	command, err := receiveCommandSetWithContext(ctx, assoc, pcID)
	if err != nil {
		return err
	}
	return servePatientRootCFindCommand(ctx, assoc, pcID, command, handler)
}

func serveStudyRootCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CFindHandler) error {
	return serveCFindCommand(ctx, assoc, pcID, command, handler, cFindSCPModel{
		Name:        "Study Root FIND",
		SOPClassUID: StudyRootFindSOPClassUID,
		Level:       studyRootCFindLevel,
	})
}

func servePatientRootCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CFindHandler) error {
	return serveCFindCommand(ctx, assoc, pcID, command, handler, cFindSCPModel{
		Name:        "Patient Root FIND",
		SOPClassUID: PatientRootFindSOPClassUID,
		Level:       patientRootCFindLevel,
	})
}

type cFindSCPModel struct {
	Name        string
	SOPClassUID string
	Level       func(*object.Object) (string, error)
}

func serveCFindCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CFindHandler, model cFindSCPModel) error {
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return err
	}
	req, err := ParseCFindRequest(command)
	if err != nil {
		return err
	}
	identifier, err := receiveIdentifierDataSetWithContext(ctx, assoc, pcID, syntax)
	if err != nil {
		return err
	}
	if req.AffectedSOPClassUID != model.SOPClassUID {
		err := fmt.Errorf("dicom dimse: C-FIND SCP SOP Class UID %q, want %s %q", req.AffectedSOPClassUID, model.Name, model.SOPClassUID)
		_ = sendCFindFinal(ctx, assoc, pcID, *req, model.SOPClassUID, CFindStatusUnableToProcess, err.Error(), syntax)
		return err
	}
	level, err := model.Level(identifier)
	if err != nil {
		_ = sendCFindFinal(ctx, assoc, pcID, *req, model.SOPClassUID, CFindStatusUnableToProcess, err.Error(), syntax)
		return err
	}

	monitor := startSCPCancelMonitor(ctx, assoc, pcID, req.MessageID, ErrCFindCanceled, false)
	defer monitor.Stop()
	operationCtx := monitor.Context()
	_, err = runSCPHandler(operationCtx, assoc, scpControlsFromContext(ctx).CancelGrace, func(handlerCtx context.Context) (struct{}, error) {
		matches, handlerErr := handler.Find(handlerCtx, CFindRequestContext{
			Request:               *req,
			Identifier:            identifier,
			QueryRetrieveLevel:    level,
			PresentationContextID: pcID,
			IdentifierSyntax:      syntax,
		})
		if handlerErr != nil {
			return struct{}{}, handlerErr
		}
		for _, match := range matches {
			if operationErr := monitor.OperationError(); operationErr != nil {
				return struct{}{}, operationErr
			}
			if match == nil {
				return struct{}{}, fmt.Errorf("dicom dimse: C-FIND handler returned nil match")
			}
			// Keep a command+dataset response on the association context so a
			// C-CANCEL cannot interrupt it between the two wire messages. The
			// outer runSCPHandler enforces cancel grace and aborts a blocked send.
			if sendErr := sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
				return SendCFindResponseWithContext(responseCtx, assoc, pcID, CFindResponse{
					AffectedSOPClassUID:       model.SOPClassUID,
					MessageIDBeingRespondedTo: req.MessageID,
					Status:                    StatusPending,
				}, match, syntax)
			}); sendErr != nil {
				return struct{}{}, sendErr
			}
		}
		return struct{}{}, monitor.OperationError()
	})
	if monitorErr := monitor.Stop(); monitorErr != nil {
		err = errors.Join(err, monitorErr)
	}
	if err != nil {
		return sendCFindOperationError(operationCtx, assoc, pcID, *req, model.SOPClassUID, syntax, err)
	}
	return sendCFindFinal(ctx, assoc, pcID, *req, model.SOPClassUID, StatusSuccess, "", syntax)
}

func sendCFindOperationError(ctx context.Context, assoc *ul.Association, pcID byte, req CFindRequest, sopClassUID string, syntax transfer.Syntax, err error) error {
	status, comment := cFindSCPStatusForError(err)
	_ = sendCFindFinal(ctx, assoc, pcID, req, sopClassUID, status, comment, syntax)
	return err
}

func studyRootCFindLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-FIND Identifier")
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

func patientRootCFindLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-FIND Identifier")
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

func cFindSCPStatusForError(err error) (uint16, string) {
	var statusErr *CFindSCPError
	if errors.As(err, &statusErr) {
		comment := statusErr.ErrorComment
		if comment == "" && statusErr.Err != nil {
			comment = statusErr.Err.Error()
		}
		return statusErr.Status, comment
	}
	// A deadline may race with the cancel used only to stop the interleaved
	// command monitor. Preserve the operational timeout classification.
	if errors.Is(err, context.DeadlineExceeded) {
		return CFindStatusUnableToProcess, err.Error()
	}
	if errors.Is(err, ErrCFindCanceled) || errors.Is(err, context.Canceled) {
		return CFindStatusCancel, "C-FIND canceled"
	}
	return CFindStatusUnableToProcess, err.Error()
}

func sendStudyRootCFindFinal(assoc *ul.Association, pcID byte, req CFindRequest, status uint16, comment string, syntax transfer.Syntax) error {
	return sendCFindFinal(nil, assoc, pcID, req, StudyRootFindSOPClassUID, status, comment, syntax)
}

func sendCFindFinal(ctx context.Context, assoc *ul.Association, pcID byte, req CFindRequest, sopClassUID string, status uint16, comment string, syntax transfer.Syntax) error {
	return sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
		return SendCFindResponseWithContext(responseCtx, assoc, pcID, CFindResponse{
			AffectedSOPClassUID:       sopClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    status,
			ErrorComment:              comment,
		}, nil, syntax)
	})
}
