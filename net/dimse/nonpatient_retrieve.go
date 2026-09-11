package dimse

import (
	"context"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func nonPatientRetrieveModel(sop string) (NonPatientModel, bool) {
	switch sop {
	case HangingProtocolMoveSOPClassUID, HangingProtocolGetSOPClassUID:
		return HangingProtocolModel, true
	case ColorPaletteMoveSOPClassUID, ColorPaletteGetSOPClassUID:
		return ColorPaletteModel, true
	default:
		return 0, false
	}
}

// The generic SCP still owns receiving, cancel monitoring, suboperation status
// accounting and C-STORE transport. Profile adapters validate before invoking
// the application's existing handler; no catalog is installed globally.
func serveNonPatientCGet(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, model NonPatientModel, base CGetHandler) error {
	storage, _, _, sop, _ := model.SOPClasses()
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	if pc.AbstractSyntaxUID != sop {
		return ErrNonPatientIdentifier
	}
	handler := CGetHandlerFunc(func(ctx context.Context, request CGetRequestContext) ([]CGetSubOperation, error) {
		uids, err := model.RetrieveUIDs(request.Identifier)
		if err != nil {
			return nil, NewCGetSCPError(0xA900, "Invalid non-patient Identifier", err)
		}
		ops, err := base.Get(ctx, request)
		if err != nil {
			return nil, err
		}
		seen := make(map[string]bool, len(uids))
		if len(ops) > len(uids) {
			return nil, NewCGetSCPError(StatusCGetUnableToProcess, "Invalid catalog result", ErrNonPatientProvider)
		}
		ops = append([]CGetSubOperation(nil), ops...)
		for i, op := range ops {
			if !nonPatientSelected(storage, uids, seen, op.AffectedSOPClassUID, op.AffectedSOPInstanceUID) || op.LoadDataSet == nil {
				return nil, NewCGetSCPError(StatusCGetUnableToProcess, "Invalid catalog identity", ErrNonPatientProvider)
			}
			load := op.LoadDataSet
			instance := op.AffectedSOPInstanceUID
			ops[i].LoadDataSet = func(ctx context.Context) (*object.Object, error) {
				data, err := load(ctx)
				if err != nil {
					return nil, err
				}
				if data == nil {
					return nil, ErrNonPatientProvider
				}
				class, _ := data.GetString(npTag(8, 0x16))
				uid, _ := data.GetString(npTag(8, 0x18))
				if class != storage || uid != instance {
					return nil, ErrNonPatientProvider
				}
				return data, nil
			}
		}
		return ops, nil
	})
	return serveCGetCommand(ctx, assoc, pcID, command, handler, cGetSCPModel{Name: "non-patient GET", SOPClassUID: sop, Level: nonPatientLevel})
}

func nonPatientLevel(*object.Object) (string, error) { return "", nil }

func nonPatientSelected(storage string, uids []string, seen map[string]bool, class, instance string) bool {
	if class != storage || seen[instance] {
		return false
	}
	for _, uid := range uids {
		if uid == instance {
			seen[instance] = true
			return true
		}
	}
	return false
}

type nonPatientMoveHandler struct {
	model NonPatientModel
	base  CMoveHandler
}

func (handler nonPatientMoveHandler) Move(ctx context.Context, request CMoveRequestContext) ([]CMoveSubOperation, error) {
	uids, err := handler.model.RetrieveUIDs(request.Identifier)
	if err != nil {
		return nil, NewCMoveSCPError(0xA900, "Invalid non-patient Identifier", err)
	}
	ops, err := handler.base.Move(ctx, request)
	if err != nil {
		return nil, err
	}
	storage, _, _, _, _ := handler.model.SOPClasses()
	seen := make(map[string]bool, len(uids))
	if len(ops) > len(uids) {
		return nil, NewCMoveSCPError(StatusCMoveUnableToProcess, "Invalid catalog result", ErrNonPatientProvider)
	}
	for _, op := range ops {
		if !nonPatientSelected(storage, uids, seen, op.AffectedSOPClassUID, op.AffectedSOPInstanceUID) || op.Store == nil {
			return nil, NewCMoveSCPError(StatusCMoveUnableToProcess, "Invalid catalog identity", ErrNonPatientProvider)
		}
	}
	return ops, nil
}

type nonPatientMoveBatchHandler struct {
	nonPatientMoveHandler
	batch CMoveBatchHandler
}

func (handler nonPatientMoveBatchHandler) PrepareCMoveBatch(ctx context.Context, request CMoveRequestContext) (CMoveBatch, error) {
	uids, err := handler.model.RetrieveUIDs(request.Identifier)
	if err != nil {
		return CMoveBatch{}, NewCMoveSCPError(0xA900, "Invalid non-patient Identifier", err)
	}
	batch, err := handler.batch.PrepareCMoveBatch(ctx, request)
	if err != nil {
		return CMoveBatch{}, err
	}
	storage, _, _, _, _ := handler.model.SOPClasses()
	seen := make(map[string]bool, len(uids))
	if len(batch.SubOperations) > len(uids) {
		return CMoveBatch{}, NewCMoveSCPError(StatusCMoveUnableToProcess, "Invalid catalog result", ErrNonPatientProvider)
	}
	for _, op := range batch.SubOperations {
		if !nonPatientSelected(storage, uids, seen, op.AffectedSOPClassUID, op.AffectedSOPInstanceUID) {
			return CMoveBatch{}, NewCMoveSCPError(StatusCMoveUnableToProcess, "Invalid catalog identity", ErrNonPatientProvider)
		}
	}
	return batch, nil
}

func serveNonPatientCMove(ctx context.Context, assoc *ul.Association, pcID byte, command *object.Object, model NonPatientModel, base CMoveHandler) error {
	_, _, sop, _, _ := model.SOPClasses()
	pc, err := AcceptedContextByID(assoc, pcID)
	if err != nil {
		return err
	}
	if pc.AbstractSyntaxUID != sop {
		return ErrNonPatientIdentifier
	}
	adapter := nonPatientMoveHandler{model: model, base: base}
	var handler CMoveHandler = adapter
	if batch, ok := base.(CMoveBatchHandler); ok {
		handler = nonPatientMoveBatchHandler{nonPatientMoveHandler: adapter, batch: batch}
	}
	return serveCMoveCommand(ctx, assoc, pcID, command, handler, cMoveSCPModel{Name: "non-patient MOVE", SOPClassUID: sop, Level: nonPatientLevel})
}
