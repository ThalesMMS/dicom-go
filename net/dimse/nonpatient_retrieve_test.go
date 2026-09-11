package dimse

import (
	"context"
	"errors"
	"testing"
)

type npBatchTestHandler struct {
	batch  CMoveBatch
	called bool
}

func (h *npBatchTestHandler) Move(context.Context, CMoveRequestContext) ([]CMoveSubOperation, error) {
	return nil, errors.New("batch adapter fell back to Move")
}
func (h *npBatchTestHandler) PrepareCMoveBatch(context.Context, CMoveRequestContext) (CMoveBatch, error) {
	h.called = true
	return h.batch, nil
}

func TestNonPatientMoveBatchPreservesContractAndSelection(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		storage, _, _, _, _ := model.SOPClasses()
		data := npInstance(model, 1)
		uid, _ := data.GetString(npTag(8, 0x18))
		for _, tc := range []struct {
			name, class, instance string
			duplicate             bool
			valid                 bool
		}{{"valid", storage, uid, false, true}, {"wrong-class", "1.2.3", uid, false, false}, {"unselected", storage, "1.2.3", false, false}, {"duplicate", storage, uid, true, false}} {
			t.Run(tc.name, func(t *testing.T) {
				base := &npBatchTestHandler{batch: CMoveBatch{SubOperations: []CMoveSubOperationDescriptor{{AffectedSOPClassUID: tc.class, AffectedSOPInstanceUID: tc.instance}}, Run: func(ctx context.Context, yield func(CMoveSubOperationResult) error) error {
					return yield(CMoveSubOperationResult{Status: StatusSuccess})
				}}}
				if tc.duplicate {
					base.batch.SubOperations = append(base.batch.SubOperations, base.batch.SubOperations[0])
				}
				handler := nonPatientMoveBatchHandler{nonPatientMoveHandler: nonPatientMoveHandler{model: model, base: base}, batch: base}
				batch, err := handler.PrepareCMoveBatch(context.Background(), CMoveRequestContext{Identifier: npUIDQuery(data)})
				if !base.called || (err == nil) != tc.valid {
					t.Fatalf("batch validation: called=%v err=%v", base.called, err)
				}
				if tc.valid {
					calls := 0
					if err := batch.Run(context.Background(), func(CMoveSubOperationResult) error { calls++; return nil }); err != nil || calls != 1 {
						t.Fatalf("batch execution: %d %v", calls, err)
					}
				}
			})
		}
	}
}
