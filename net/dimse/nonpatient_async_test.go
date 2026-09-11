package dimse

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func TestNonPatientAsyncCancellationLimitsAndRoles(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		t.Run(fmt.Sprint(model), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			data := npInstance(model, 1)
			storage, find, move, get, _ := model.SOPClasses()
			uid, _ := data.GetString(npTag(8, 0x18))
			moveEntered := make(chan struct{})
			getEntered := make(chan struct{})
			route, err := model.FindRoute(func(ctx context.Context, yield func(*object.Object) error) error {
				if err := yield(data); err != nil {
					return err
				}
				<-ctx.Done()
				return ctx.Err()
			}, StreamingCFindLimits{MaxMatches: 1})
			if err != nil {
				t.Fatal(err)
			}
			router, err := NewStreamingCFindRouter(nil, route)
			if err != nil {
				t.Fatal(err)
			}
			assoc, done := npServe(t, ctx, model, AssociationSCPOptions{CFindHandler: router, CMoveHandler: CMoveHandlerFunc(func(ctx context.Context, _ CMoveRequestContext) ([]CMoveSubOperation, error) {
				close(moveEntered)
				<-ctx.Done()
				return nil, ctx.Err()
			}), CGetHandler: CGetHandlerFunc(func(ctx context.Context, request CGetRequestContext) ([]CGetSubOperation, error) {
				requested, _ := request.Identifier.GetString(npTag(8, 0x18))
				if requested != uid {
					close(getEntered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return []CGetSubOperation{{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: uid, LoadDataSet: func(context.Context) (*object.Object, error) { return data, nil }}}, nil
			})})
			session, err := NewAsyncSession(assoc, AsyncSessionOptions{CGetStorageSOPClassUIDs: []string{storage}, Handlers: map[uint16]AsyncRequestHandler{CStoreRQ: func(ctx context.Context, s *AsyncSession, message AsyncMessage) error {
				request, err := ParseCStoreRequest(message.Command)
				if err != nil {
					return err
				}
				got, _ := message.DataSet.GetString(npTag(8, 0x18))
				if got != uid {
					return errors.New("wrong reverse storage dataset")
				}
				return s.Respond(ctx, message, (CStoreResponse{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: uid, Status: StatusSuccess, MessageIDBeingRespondedTo: request.MessageID}).CommandSet(), nil)
			}}})
			if err != nil {
				t.Fatal(err)
			}
			defer session.Close()
			pc, err := AcceptedContextForSOPClass(assoc, find)
			if err != nil {
				t.Fatal(err)
			}
			op, err := model.StartCFind(ctx, session, pc.ID, CFindRequest{}, npQuery(model, "*"))
			if err != nil {
				t.Fatal(err)
			}
			pending, err := op.Next(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rsp, err := ParseCFindResponse(pending.Command)
			if err != nil || rsp.Status != StatusPending {
				t.Fatalf("pending %v %v", rsp, err)
			}
			if err := op.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			terminal, err := op.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			rsp, err = ParseCFindResponse(terminal.Command)
			if err != nil || rsp.Status != CFindStatusCancel {
				t.Fatalf("cancel %v %v", rsp, err)
			}
			pc, err = AcceptedContextForSOPClass(assoc, move)
			if err != nil {
				t.Fatal(err)
			}
			op, err = model.StartCMove(ctx, session, pc.ID, CMoveRequest{MoveDestination: "DEST"}, npUIDQuery(data))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-moveEntered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := op.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			terminal, err = op.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			mrsp, err := ParseCMoveResponse(terminal.Command)
			if err != nil || mrsp.Status != 0xFE00 {
				t.Fatalf("move cancel %v %v", mrsp, err)
			}
			pc, err = AcceptedContextForSOPClass(assoc, get)
			if err != nil {
				t.Fatal(err)
			}
			op, err = model.StartCGet(ctx, session, pc.ID, CGetRequest{}, npUIDQuery(data))
			if err != nil {
				t.Fatal(err)
			}
			terminal, err = op.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			grsp, err := ParseCGetResponse(terminal.Command)
			if err != nil {
				t.Fatal(err)
			}
			assertCGetCounts(t, "async get", grsp, StatusSuccess, 0, 1, 0, 0)
			op, err = model.StartCGet(ctx, session, pc.ID, CGetRequest{}, npUIDQuery(npInstance(model, 2)))
			if err != nil {
				t.Fatal(err)
			}
			select {
			case <-getEntered:
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if err := op.Cancel(ctx); err != nil {
				t.Fatal(err)
			}
			terminal, err = op.Wait(ctx)
			if err != nil {
				t.Fatal(err)
			}
			grsp, err = ParseCGetResponse(terminal.Command)
			if err != nil {
				t.Fatal(err)
			}
			assertCGetCounts(t, "async get canceled", grsp, 0xFE00, 0, 0, 0, 0)
			if err := session.Release(ctx, AsyncReleaseWait); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestNonPatientGetRequiresMatchingStorageRole(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		storage, _, _, get, _ := model.SOPClasses()
		for _, storageClass := range []string{storage, "1.2.840.10008.5.1.4.1.1.2"} {
			assoc := &ul.Association{AcceptedContexts: []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: get, TransferSyntaxUID: ul.ExplicitVRLittleEndian}, {ID: 3, AbstractSyntaxUID: storageClass, TransferSyntaxUID: ul.ExplicitVRLittleEndian}}}
			session := &AsyncSession{assoc: assoc}
			if storageClass != storage {
				assoc.AcceptedRoleSelections = []ul.RoleSelectionItem{{SopClassUID: storageClass, SCPRole: true}}
			}
			if _, err := model.StartCGet(context.Background(), session, 1, CGetRequest{}, npUIDQuery(npInstance(model, 1))); !errors.Is(err, ErrCGetStorageRoleNotAccepted) {
				t.Fatalf("wrong/missing role: %v", err)
			}
		}
	}
}

func TestNonPatientFindLimitStatus(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		route, err := model.FindRoute(func(ctx context.Context, yield func(*object.Object) error) error {
			for i := 1; i <= 2; i++ {
				if err := yield(npInstance(model, i)); err != nil {
					return err
				}
			}
			return nil
		}, StreamingCFindLimits{MaxMatches: 1})
		if err != nil {
			t.Fatal(err)
		}
		router, err := NewStreamingCFindRouter(nil, route)
		if err != nil {
			t.Fatal(err)
		}
		assoc, done := npServe(t, ctx, model, AssociationSCPOptions{CFindHandler: router})
		_, find, _, _, _ := model.SOPClasses()
		client, err := NewStreamingCFindClient(assoc, find, "")
		if err != nil {
			t.Fatal(err)
		}
		_, err = client.Find(ctx, npQuery(model, "*"), func(uint16, *object.Object) error { return nil })
		var status *StreamingCFindStatusError
		if !errors.As(err, &status) || status.Status != CFindStatusOutOfResources {
			t.Fatalf("limit status: %v", err)
		}
		if err := assoc.Release(ctx); err != nil {
			t.Fatal(err)
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		cancel()
	}
}
