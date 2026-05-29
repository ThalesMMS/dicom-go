package dimse

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCMoveHandlerFuncAndSCPErrorFormatting(t *testing.T) {
	var nilHandler CMoveHandlerFunc
	if _, err := nilHandler.Move(context.Background(), CMoveRequestContext{}); err == nil {
		t.Fatal("nil CMoveHandlerFunc.Move() error = nil, want error")
	}

	called := false
	handler := CMoveHandlerFunc(func(ctx context.Context, req CMoveRequestContext) ([]CMoveSubOperation, error) {
		called = true
		if req.QueryRetrieveLevel != QueryRetrieveLevelStudy {
			t.Fatalf("handler QueryRetrieveLevel = %q, want STUDY", req.QueryRetrieveLevel)
		}
		return []CMoveSubOperation{{AffectedSOPInstanceUID: "1.2.3"}}, nil
	})
	ops, err := handler.Move(context.Background(), CMoveRequestContext{QueryRetrieveLevel: QueryRetrieveLevelStudy})
	if err != nil {
		t.Fatalf("handler.Move() error = %v", err)
	}
	if !called || len(ops) != 1 || ops[0].AffectedSOPInstanceUID != "1.2.3" {
		t.Fatalf("handler.Move() = called %v ops %+v", called, ops)
	}

	cause := errors.New("boom")
	errWithCause := NewCMoveSCPError(0xA700, "out of resources", cause)
	if !errors.Is(errWithCause, cause) {
		t.Fatalf("NewCMoveSCPError unwrap = %v, want %v", errWithCause, cause)
	}
	for _, want := range []string{"0xA700", "out of resources", "boom"} {
		if !strings.Contains(errWithCause.Error(), want) {
			t.Fatalf("CMoveSCPError.Error() = %q, want substring %q", errWithCause.Error(), want)
		}
	}

	for _, tc := range []struct {
		name string
		err  *CMoveSCPError
		want string
	}{
		{name: "nil", err: nil, want: "<nil>"},
		{name: "cause only", err: NewCMoveSCPError(0xC000, "", cause), want: "boom"},
		{name: "comment only", err: NewCMoveSCPError(0xC000, "bad move", nil), want: "bad move"},
		{name: "status only", err: NewCMoveSCPError(0xC000, "", nil), want: "0xC000"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); !strings.Contains(got, tc.want) {
				t.Fatalf("Error() = %q, want substring %q", got, tc.want)
			}
		})
	}
}
