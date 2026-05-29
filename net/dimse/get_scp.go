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

var ErrCGetCanceled = errors.New("dicom dimse: C-GET canceled")

// CGetRequestContext is the decoded request passed to a C-GET SCP handler.
type CGetRequestContext struct {
	Request               CGetRequest
	Identifier            *object.Object
	QueryRetrieveLevel    string
	PresentationContextID byte
	IdentifierSyntax      transfer.Syntax
}

// CGetSubOperation represents one C-STORE sub-operation sent on the C-GET
// association.
type CGetSubOperation struct {
	AffectedSOPClassUID    string
	AffectedSOPInstanceUID string
	LoadDataSet            func(context.Context) (*object.Object, error)
}

// CGetSubOperationResult reports the final status of one same-association
// C-STORE sub-operation.
type CGetSubOperationResult struct {
	Status uint16
	Err    error
}

// CGetHandler resolves matching instances for a C-GET request.
type CGetHandler interface {
	Get(context.Context, CGetRequestContext) ([]CGetSubOperation, error)
}

// CGetHandlerFunc adapts a function to CGetHandler.
type CGetHandlerFunc func(context.Context, CGetRequestContext) ([]CGetSubOperation, error)

func (f CGetHandlerFunc) Get(ctx context.Context, req CGetRequestContext) ([]CGetSubOperation, error) {
	if f == nil {
		return nil, fmt.Errorf("dicom dimse: nil C-GET handler")
	}
	return f(ctx, req)
}

// CGetSCPError lets handlers choose a final C-GET status and Error Comment.
type CGetSCPError struct {
	Status       uint16
	ErrorComment string
	Err          error
}

func NewCGetSCPError(status uint16, comment string, err error) *CGetSCPError {
	return &CGetSCPError{Status: status, ErrorComment: comment, Err: err}
}

func (e *CGetSCPError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err != nil && e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-GET SCP status 0x%04X: %s: %v", e.Status, e.ErrorComment, e.Err)
	}
	if e.Err != nil {
		return fmt.Sprintf("dicom dimse: C-GET SCP status 0x%04X: %v", e.Status, e.Err)
	}
	if e.ErrorComment != "" {
		return fmt.Sprintf("dicom dimse: C-GET SCP status 0x%04X: %s", e.Status, e.ErrorComment)
	}
	return fmt.Sprintf("dicom dimse: C-GET SCP status 0x%04X", e.Status)
}

func (e *CGetSCPError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func ServeStudyRootCGet(ctx context.Context, assoc *ul.Association, pcID byte, handler CGetHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-GET handler")
	}
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return err
	}
	return serveStudyRootCGetCommand(ctx, assoc, pcID, command, handler)
}

func ServePatientRootCGet(ctx context.Context, assoc *ul.Association, pcID byte, handler CGetHandler) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-GET handler")
	}
	command, err := ReceiveCommandSet(assoc, pcID)
	if err != nil {
		return err
	}
	return servePatientRootCGetCommand(ctx, assoc, pcID, command, handler)
}

func serveStudyRootCGetCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CGetHandler) error {
	return serveCGetCommand(ctx, assoc, pcID, command, handler, cGetSCPModel{
		Name:        "Study Root GET",
		SOPClassUID: StudyRootGetSOPClassUID,
		Level:       studyRootCGetLevel,
	})
}

func servePatientRootCGetCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CGetHandler) error {
	return serveCGetCommand(ctx, assoc, pcID, command, handler, cGetSCPModel{
		Name:        "Patient Root GET",
		SOPClassUID: PatientRootGetSOPClassUID,
		Level:       patientRootCGetLevel,
	})
}

type cGetSCPModel struct {
	Name        string
	SOPClassUID string
	Level       func(*object.Object) (string, error)
}

func serveCGetCommand(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, handler CGetHandler, model cGetSCPModel) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if handler == nil {
		return fmt.Errorf("dicom dimse: nil C-GET handler")
	}
	syntax, err := acceptedTransferSyntax(assoc, pcID)
	if err != nil {
		return err
	}
	req, err := ParseCGetRequest(command)
	if err != nil {
		return err
	}
	identifier, err := receiveIdentifierDataSetWithContext(ctx, assoc, pcID, syntax)
	if err != nil {
		return err
	}
	if req.AffectedSOPClassUID != model.SOPClassUID {
		err := fmt.Errorf("dicom dimse: C-GET SCP SOP Class UID %q, want %s %q", req.AffectedSOPClassUID, model.Name, model.SOPClassUID)
		_ = sendCGetResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusCGetUnableToProcess, err.Error(), cMoveSubOperationCounts{})
		return err
	}
	level, err := model.Level(identifier)
	if err != nil {
		_ = sendCGetResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusCGetUnableToProcess, err.Error(), cMoveSubOperationCounts{})
		return err
	}

	monitor := startSCPCancelMonitor(ctx, assoc, pcID, req.MessageID, ErrCGetCanceled, true)
	defer monitor.Stop()
	operationCtx := monitor.Context()
	counts, err := runSCPHandler(operationCtx, assoc, scpControlsFromContext(ctx).CancelGrace, func(handlerCtx context.Context) (cMoveSubOperationCounts, error) {
		ops, handlerErr := handler.Get(handlerCtx, CGetRequestContext{
			Request:               *req,
			Identifier:            identifier,
			QueryRetrieveLevel:    level,
			PresentationContextID: pcID,
			IdentifierSyntax:      syntax,
		})
		if handlerErr != nil {
			return cMoveSubOperationCounts{}, handlerErr
		}
		if validationErr := validateCGetSubOperations(assoc, ops); validationErr != nil {
			return cMoveSubOperationCounts{}, validationErr
		}

		counts := cMoveSubOperationCounts{remaining: uint16(len(ops))}
		client := NewStoreClient(assoc)
		for i, op := range ops {
			if operationErr := monitor.OperationError(); operationErr != nil {
				return counts, operationErr
			}
			// Keep the response receive on the association/server context so an
			// already-issued C-STORE can be drained after C-CANCEL. The outer
			// runSCPHandler still enforces cancel grace and aborts a silent peer.
			result := runCGetSubOperation(handlerCtx, ctx, client, monitor, op, uint16(i+1), req.Priority)
			if errors.Is(result.Err, ErrCGetCanceled) || errors.Is(result.Err, context.Canceled) {
				return counts, result.Err
			}
			counts.remaining--
			countCGetSubOperationResult(&counts, result, op.AffectedSOPInstanceUID)
			if operationErr := monitor.OperationError(); operationErr != nil {
				return counts, operationErr
			}
			if i < len(ops)-1 {
				if sendErr := sendCGetResponse(ctx, assoc, pcID, *req, model.SOPClassUID, StatusPending, "", counts); sendErr != nil {
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
		return sendCGetOperationError(operationCtx, assoc, pcID, *req, model.SOPClassUID, counts, err)
	}

	status := StatusSuccess
	if counts.failed > 0 || counts.warning > 0 {
		status = StatusCGetSubOperationsCompleteOneOrMoreFailures
	}
	return sendCGetResponse(ctx, assoc, pcID, *req, model.SOPClassUID, status, "", counts)
}

func sendCGetOperationError(ctx context.Context, assoc *ul.Association, pcID byte, req CGetRequest, sopClassUID string, counts cMoveSubOperationCounts, err error) error {
	status, comment := cGetSCPStatusForError(err)
	_ = sendCGetResponse(ctx, assoc, pcID, req, sopClassUID, status, comment, counts)
	return err
}

func validateCGetSubOperations(assoc *ul.Association, ops []CGetSubOperation) error {
	if len(ops) > maxCMoveSubOperations {
		return fmt.Errorf("dicom dimse: too many C-GET sub-operations: %d exceeds %d", len(ops), maxCMoveSubOperations)
	}
	for i, op := range ops {
		if strings.TrimSpace(op.AffectedSOPClassUID) == "" {
			return fmt.Errorf("dicom dimse: C-GET sub-operation %d missing Affected SOP Class UID", i)
		}
		if strings.TrimSpace(op.AffectedSOPInstanceUID) == "" {
			return fmt.Errorf("dicom dimse: C-GET sub-operation %d missing Affected SOP Instance UID", i)
		}
		if op.LoadDataSet == nil {
			return fmt.Errorf("dicom dimse: C-GET sub-operation %d missing LoadDataSet callback", i)
		}
	}
	return nil
}

func runCGetSubOperation(loadCtx, responseCtx context.Context, client *StoreClient, monitor *scpCancelMonitor, op CGetSubOperation, messageID, priority uint16) CGetSubOperationResult {
	if client == nil || client.Assoc == nil {
		return CGetSubOperationResult{Status: StatusCGetUnableToProcess, Err: fmt.Errorf("dicom dimse: C-GET store client association is nil")}
	}
	if _, err := AcceptedContextForSOPClass(client.Assoc, op.AffectedSOPClassUID); err != nil {
		return CGetSubOperationResult{Status: StatusCGetUnableToProcess, Err: err}
	}
	if !AcceptedStorageSCPRole(client.Assoc, op.AffectedSOPClassUID) {
		return CGetSubOperationResult{
			Status: StatusCGetUnableToProcess,
			Err:    fmt.Errorf("dicom dimse: C-GET storage SCP role not accepted for SOP Class UID %q", op.AffectedSOPClassUID),
		}
	}
	dataset, err := op.LoadDataSet(loadCtx)
	if err != nil {
		return CGetSubOperationResult{Status: StatusCGetUnableToProcess, Err: err}
	}
	if err := context.Cause(loadCtx); err != nil {
		return CGetSubOperationResult{Status: StatusCGetUnableToProcess, Err: err}
	}
	if dataset == nil {
		return CGetSubOperationResult{Status: StatusCGetUnableToProcess, Err: fmt.Errorf("dicom dimse: C-GET sub-operation dataset is nil")}
	}
	result, err := client.storeWithOptions(responseCtx, dataset, CStoreOptions{
		AffectedSOPClassUID:    op.AffectedSOPClassUID,
		AffectedSOPInstanceUID: op.AffectedSOPInstanceUID,
		MessageID:              messageID,
		Priority:               priority,
	}, monitor.receiveCStoreResponse)
	status := StatusSuccess
	if result.Response != nil {
		status = result.Response.Status
	}
	return CGetSubOperationResult{Status: status, Err: err}
}

func countCGetSubOperationResult(counts *cMoveSubOperationCounts, result CGetSubOperationResult, sopInstanceUID string) {
	if result.Err != nil {
		counts.failed++
		counts.failedSOPInstanceUIDs = append(counts.failedSOPInstanceUIDs, sopInstanceUID)
		return
	}
	switch {
	case result.Status == StatusSuccess:
		counts.completed++
	case IsCStoreWarningStatus(result.Status):
		counts.warning++
	default:
		counts.failed++
		counts.failedSOPInstanceUIDs = append(counts.failedSOPInstanceUIDs, sopInstanceUID)
	}
}

func studyRootCGetLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-GET Identifier")
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

func patientRootCGetLevel(identifier *object.Object) (string, error) {
	if identifier == nil {
		return "", fmt.Errorf("dicom dimse: missing C-GET Identifier")
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

func cGetSCPStatusForError(err error) (uint16, string) {
	var statusErr *CGetSCPError
	if errors.As(err, &statusErr) {
		comment := statusErr.ErrorComment
		if comment == "" && statusErr.Err != nil {
			comment = statusErr.Err.Error()
		}
		return statusErr.Status, comment
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return StatusCGetUnableToProcess, err.Error()
	}
	if errors.Is(err, ErrCGetCanceled) || errors.Is(err, context.Canceled) {
		return StatusCGetCancel, "C-GET canceled"
	}
	return StatusCGetUnableToProcess, err.Error()
}

func sendStudyRootCGetResponse(assoc *ul.Association, pcID byte, req CGetRequest, status uint16, comment string, counts cMoveSubOperationCounts) error {
	return sendCGetResponse(nil, assoc, pcID, req, StudyRootGetSOPClassUID, status, comment, counts)
}

func sendCGetResponse(ctx context.Context, assoc *ul.Association, pcID byte, req CGetRequest, sopClassUID string, status uint16, comment string, counts cMoveSubOperationCounts) error {
	return sendWithSCPResponseContext(ctx, assoc, func(responseCtx context.Context) error {
		return SendCGetResponseWithContext(responseCtx, assoc, pcID, CGetResponse{
			AffectedSOPClassUID:                 sopClassUID,
			MessageIDBeingRespondedTo:           req.MessageID,
			Status:                              status,
			ErrorComment:                        comment,
			NumberOfRemainingSuboperationsOrNil: remainingSuboperationsPtr(status, counts.remaining),
			NumberOfCompletedSuboperationsOrNil: uint16Ptr(counts.completed),
			NumberOfFailedSuboperationsOrNil:    uint16Ptr(counts.failed),
			NumberOfWarningSuboperationsOrNil:   uint16Ptr(counts.warning),
			FailedSOPInstanceUIDListOrNil:       counts.failedSOPInstanceUIDs,
		})
	})
}
