package dimse

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func npString(tag core.Tag, vr core.VR, values ...string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue(values)}
}
func npObject(elements ...core.Element) *object.Object {
	return object.FromElements(elements, std.Dictionary)
}
func npInstance(model NonPatientModel, suffix int) *object.Object {
	storage, _, _, _, _ := model.SOPClasses()
	data := npObject(npString(npTag(8, 0x16), core.VRUI, storage), npString(npTag(8, 0x18), core.VRUI, fmt.Sprintf("1.2.826.0.1.3680043.10.543.918.%d.%d", model, suffix)))
	if model == HangingProtocolModel {
		data.Put(npString(npTag(0x72, 2), core.VRSH, "SYNTHETIC"))
		data.Put(npString(npTag(0x72, 6), core.VRCS, "SITE"))
		data.Put(core.Element{Header: core.ElementHeader{Tag: npTag(0x72, 0x14), VR: core.VRUS}, Value: core.Uint16Value{2}})
		data.Put(npSequence(npTag(0x72, 0xc), npString(npTag(8, 0x60), core.VRCS, "CT"), npString(npTag(0x20, 0x60), core.VRCS, "L")))
	} else {
		data.Put(npString(npTag(0x70, 0x80), core.VRCS, "SYNTHETIC"))
	}
	return data
}
func npSequence(tag core.Tag, elements ...core.Element) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{{Elements: elements}}}}
}
func npQuery(model NonPatientModel, value string) *object.Object {
	tag, vr := npTag(0x72, 2), core.VRSH
	if model == ColorPaletteModel {
		tag, vr = npTag(0x70, 0x80), core.VRCS
	}
	return npObject(npString(tag, vr, value))
}
func npUIDQuery(data *object.Object) *object.Object {
	e, _ := data.Get(npTag(8, 0x18))
	return npObject(e)
}

func TestNonPatientProfilesMatchAndRejectInvalidKeys(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		t.Run(fmt.Sprint(model), func(t *testing.T) {
			candidate := npInstance(model, 1)
			for _, value := range []string{"SYN*", "SYNTHETIC", ""} {
				projection, match, err := model.Match(context.Background(), npQuery(model, value), candidate)
				if err != nil || !match || len(projection.Elements()) != 3 {
					t.Fatalf("match %q: %v %v %v", value, projection, match, err)
				}
			}
			if _, match, err := model.Match(context.Background(), npQuery(model, "NO"), candidate); err != nil || match {
				t.Fatalf("no-match: %v %v", match, err)
			}
			for _, element := range []core.Element{
				npString(npTag(8, 0x52), core.VRCS, "IMAGE"), npString(npTag(0x10, 0x20), core.VRLO, ""), npString(npTag(0x20, 0xd), core.VRUI, "1.2.3"),
				npString(npTag(8, 0x18), core.VRUI, "1.2.*"), npString(npTag(8, 0x18), core.VRUI, "1.2.3", "1.2.4"), npString(npTag(8, 0x16), core.VRLO, "1.2.3"),
				npString(npTag(8, 5), core.VRCS, "ISO_IR 100"), npString(npTag(0x11, 0x10), core.VRLO, ""),
			} {
				if err := model.ValidateFindIdentifier(npObject(element)); err == nil {
					t.Fatalf("accepted invalid %v", element.Tag())
				}
			}
			returnTag := npTag(0x72, 4)
			if model == ColorPaletteModel {
				returnTag = npTag(0x70, 0x81)
			}
			if err := model.ValidateFindIdentifier(npObject(npString(returnTag, core.VRLO, "constraint"))); err == nil {
				t.Fatal("matched return-only key")
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, _, err := model.Match(ctx, npQuery(model, "*"), candidate); !errors.Is(err, context.Canceled) {
				t.Fatalf("cancel: %v", err)
			}
			wrong := npInstance(model, 2)
			wrong.Put(npString(npTag(8, 0x16), core.VRUI, "1.2.3"))
			if _, _, err := model.Match(context.Background(), npQuery(model, "*"), wrong); !errors.Is(err, ErrNonPatientProvider) {
				t.Fatalf("wrong storage: %v", err)
			}
		})
	}
}

func TestNonPatientSequenceAndBinaryKeys(t *testing.T) {
	model := HangingProtocolModel
	candidate := npInstance(model, 1)
	for _, tc := range []struct {
		modality, laterality string
		match                bool
	}{{"CT", "L", true}, {"CT", "R", false}, {"MR", "L", false}} {
		query := npObject(npSequence(npTag(0x72, 0xc), npString(npTag(8, 0x60), core.VRCS, tc.modality), npString(npTag(0x20, 0x60), core.VRCS, tc.laterality)))
		if _, match, err := model.Match(context.Background(), query, candidate); err != nil || match != tc.match {
			t.Fatalf("sequence %+v: %v %v", tc, match, err)
		}
	}
	// A candidate whose constraints occur in different items must not match.
	candidate.Put(core.Element{Header: core.ElementHeader{Tag: npTag(0x72, 0xc), VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{
		{Elements: []core.Element{npString(npTag(8, 0x60), core.VRCS, "CT"), npString(npTag(0x20, 0x60), core.VRCS, "R")}},
		{Elements: []core.Element{npString(npTag(8, 0x60), core.VRCS, "MR"), npString(npTag(0x20, 0x60), core.VRCS, "L")}},
	}}})
	query := npObject(npSequence(npTag(0x72, 0xc), npString(npTag(8, 0x60), core.VRCS, "CT"), npString(npTag(0x20, 0x60), core.VRCS, "L")))
	if _, match, err := model.Match(context.Background(), query, candidate); err != nil || match {
		t.Fatalf("cross-item match: %v %v", match, err)
	}
	partialQuery := npObject(npSequence(npTag(0x72, 0xc), npString(npTag(8, 0x60), core.VRCS, "CT")))
	projection, match, err := model.Match(context.Background(), partialQuery, candidate)
	if err != nil || !match {
		t.Fatalf("sequence projection: %v %v", match, err)
	}
	items, _ := projection.GetSequence(npTag(0x72, 0xc))
	if len(items) != 1 || len(items[0].Elements()) != 1 {
		t.Fatalf("projection leaked nonmatching items: %v", items)
	}
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		for _, number := range []uint16{2, 3} {
			value := make([]byte, 2)
			order.PutUint16(value, number)
			query := npObject(core.NewRawElement(npTag(0x72, 0x14), core.VRUS, value))
			query.SetValueByteOrder(order)
			if _, match, err := model.Match(context.Background(), query, candidate); err != nil || match != (number == 2) {
				t.Fatalf("binary %v %d: %v %v", order, number, match, err)
			}
		}
	}
	query = npObject(npSequence(npTag(0x72, 0xc), npString(npTag(8, 0x60), core.VRCS, "C*")))
	if err := model.ValidateFindIdentifier(query); err == nil {
		t.Fatal("accepted wildcard on single-only Modality")
	}
}

func TestNonPatientRetrieveIdentifiersAndBounds(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		for _, values := range [][]string{{"1.2.3"}, {"1.2.3", "1.2.4"}} {
			if got, err := model.RetrieveUIDs(npObject(npString(npTag(8, 0x18), core.VRUI, values...))); err != nil || len(got) != len(values) {
				t.Fatalf("UID list %v %v", got, err)
			}
		}
		for _, values := range [][]string{nil, {""}, {"1.2.*"}, {"1.2.03"}, {"1.2.3", "1.2.3"}, make([]string, 129)} {
			if _, err := model.RetrieveUIDs(npObject(npString(npTag(8, 0x18), core.VRUI, values...))); err == nil {
				t.Fatal("accepted invalid UID list")
			}
		}
		q := npUIDQuery(npInstance(model, 1))
		q.Put(npString(npTag(8, 0x52), core.VRCS, ""))
		if _, err := model.RetrieveUIDs(q); err == nil {
			t.Fatal("accepted QueryRetrieveLevel")
		}
		if err := model.ValidateFindIdentifier(npQuery(model, string(bytes.Repeat([]byte{'A'}, 1025)))); err == nil {
			t.Fatal("accepted long key")
		}
	}
}

func npServe(t *testing.T, ctx context.Context, model NonPatientModel, options AssociationSCPOptions) (*ul.Association, <-chan error) {
	t.Helper()
	storage, find, move, get, _ := model.SOPClasses()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "NONPATIENT", Context: ctx, SupportedAbstractSyntaxes: []string{storage, find, move, get}, SupportedTransferSyntaxes: []string{ul.ExplicitVRLittleEndian}, RoleSelections: []ul.RoleSelectionItem{{SopClassUID: storage, SCPRole: true}}})
		if err != nil {
			done <- err
			return
		}
		defer assoc.Close()
		done <- ServeAssociation(ctx, assoc, options)
	}()
	contexts, _ := model.PresentationContexts()
	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{CalledAETitle: "NONPATIENT", CallingAETitle: "TEST", Contexts: contexts, RoleSelections: []ul.RoleSelectionItem{{SopClassUID: storage, SCPRole: true}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = assoc.Close() })
	return assoc, done
}

func TestNonPatientFindRetrieveWireAndIdentity(t *testing.T) {
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		t.Run(fmt.Sprint(model), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			data := npInstance(model, 1)
			storage, find, move, get, _ := model.SOPClasses()
			instance, _ := data.GetString(npTag(8, 0x18))
			route, err := model.FindRoute(func(ctx context.Context, yield func(*object.Object) error) error { return yield(data) }, StreamingCFindLimits{MaxMatches: 2})
			if err != nil {
				t.Fatal(err)
			}
			router, err := NewStreamingCFindRouter(nil, route)
			if err != nil {
				t.Fatal(err)
			}
			var wrongIdentity atomic.Bool
			assoc, done := npServe(t, ctx, model, AssociationSCPOptions{CFindHandler: router, CGetHandler: CGetHandlerFunc(func(_ context.Context, request CGetRequestContext) ([]CGetSubOperation, error) {
				if request.QueryRetrieveLevel != "" {
					return nil, errors.New("patient hierarchy leaked")
				}
				return []CGetSubOperation{{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: instance, LoadDataSet: func(context.Context) (*object.Object, error) {
					if wrongIdentity.Load() {
						return npInstance(model, 2), nil
					}
					return data, nil
				}}}, nil
			}), CMoveHandler: CMoveHandlerFunc(func(_ context.Context, request CMoveRequestContext) ([]CMoveSubOperation, error) {
				if request.QueryRetrieveLevel != "" {
					return nil, errors.New("patient hierarchy leaked")
				}
				if request.Request.MoveDestination != "DEST" {
					return nil, NewCMoveSCPError(0xA801, "Unknown destination", nil)
				}
				return []CMoveSubOperation{{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: instance, Store: func(context.Context) CMoveSubOperationResult { return CMoveSubOperationResult{Status: StatusSuccess} }}}, nil
			})})
			client, err := NewStreamingCFindClient(assoc, find, "")
			if err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"SYN*", "NO"} {
				count := 0
				result, err := client.Find(ctx, npQuery(model, value), func(_ uint16, match *object.Object) error {
					count++
					uid, _ := match.GetString(npTag(8, 0x18))
					if uid != instance {
						return errors.New("wrong discovered UID")
					}
					return nil
				})
				if err != nil || result.FinalResponse.Status != StatusSuccess || (value == "NO" && count != 0) || (value != "NO" && count != 1) {
					t.Fatalf("find: %+v %d %v", result, count, err)
				}
			}
			_, err = client.Find(ctx, npObject(npString(npTag(0x10, 0x20), core.VRLO, "")), func(uint16, *object.Object) error { return nil })
			var statusErr *StreamingCFindStatusError
			if !errors.As(err, &statusErr) || statusErr.Status != 0xA900 {
				t.Fatalf("invalid find status: %v", err)
			}
			pc, err := AcceptedContextForSOPClass(assoc, get)
			if err != nil {
				t.Fatal(err)
			}
			stores := 0
			store := CGetStoreHandlerFunc(func(_ context.Context, request CGetStoreRequestContext) (uint16, error) {
				stores++
				if request.Request.AffectedSOPClassUID != storage || request.Request.AffectedSOPInstanceUID != instance {
					return 0xA900, errors.New("wrong identity")
				}
				return StatusSuccess, nil
			})
			for _, wrong := range []bool{false, true} {
				wrongIdentity.Store(wrong)
				final, err := SendCGet(ctx, assoc, pc.ID, CGetRequest{AffectedSOPClassUID: get, MessageID: 1}, npUIDQuery(data), transfer.ExplicitVRLittleEndian, store)
				if err != nil {
					t.Fatal(err)
				}
				if !wrong {
					assertCGetCounts(t, "get", final, StatusSuccess, 0, 1, 0, 0)
				} else {
					assertCGetCounts(t, "wrong loaded identity", final, StatusCGetSubOperationsCompleteOneOrMoreFailures, 0, 0, 1, 0)
				}
			}
			if stores != 1 {
				t.Fatalf("stored invalid object, stores=%d", stores)
			}
			invalid := npUIDQuery(data)
			invalid.Put(npString(npTag(8, 0x52), core.VRCS, "IMAGE"))
			final, err := SendCGet(ctx, assoc, pc.ID, CGetRequest{AffectedSOPClassUID: get, MessageID: 2}, invalid, transfer.ExplicitVRLittleEndian, store)
			if err != nil || final.Status != 0xA900 {
				t.Fatalf("invalid get: %v %v", final, err)
			}
			moveClient, err := NewRetrieveClientForSOPClass(assoc, move)
			if err != nil {
				t.Fatal(err)
			}
			for _, dest := range []string{"DEST", "UNKNOWN"} {
				response, err := moveClient.Move(ctx, CMoveRequest{AffectedSOPClassUID: move, MessageID: 3, MoveDestination: dest}, npUIDQuery(data))
				if err != nil {
					t.Fatal(err)
				}
				want := uint16(StatusSuccess)
				if dest == "UNKNOWN" {
					want = 0xA801
				}
				if response.Status != want {
					t.Fatalf("move: %+v", response)
				}
			}
			if err := assoc.Release(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
