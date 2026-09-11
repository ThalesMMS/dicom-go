// Command nonpatientqr discovers and retrieves synthetic non-patient objects
// over a loopback DICOM association. It does not install a persistent catalog.
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

func main() {
	for _, model := range []dimse.NonPatientModel{dimse.HangingProtocolModel, dimse.ColorPaletteModel} {
		if err := run(model); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func textElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}

func run(model dimse.NonPatientModel) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	storage, find, _, get, err := model.SOPClasses()
	if err != nil {
		return err
	}
	classTag, uidTag := core.NewTag(8, 0x16), core.NewTag(8, 0x18)
	nameTag, nameVR := core.NewTag(0x72, 2), core.VRSH
	if model == dimse.ColorPaletteModel {
		nameTag, nameVR = core.NewTag(0x70, 0x80), core.VRCS
	}
	uid := fmt.Sprintf("1.2.826.0.1.3680043.10.543.918.%d.1", model)
	data := object.FromElements([]core.Element{textElement(classTag, core.VRUI, storage), textElement(uidTag, core.VRUI, uid), textElement(nameTag, nameVR, "SYNTHETIC")}, std.Dictionary)
	route, err := model.FindRoute(func(_ context.Context, yield func(*object.Object) error) error { return yield(data) }, dimse.StreamingCFindLimits{MaxMatches: 1})
	if err != nil {
		return err
	}
	router, err := dimse.NewStreamingCFindRouter(nil, route)
	if err != nil {
		return err
	}
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		return err
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "NONPATIENT", Context: ctx, SupportedAbstractSyntaxes: []string{find, get, storage}, SupportedTransferSyntaxes: []string{ul.ExplicitVRLittleEndian}, RoleSelections: []ul.RoleSelectionItem{{SopClassUID: storage, SCPRole: true}}})
		if err != nil {
			done <- err
			return
		}
		defer assoc.Close()
		done <- dimse.ServeAssociation(ctx, assoc, dimse.AssociationSCPOptions{CFindHandler: router, CGetHandler: dimse.CGetHandlerFunc(func(_ context.Context, request dimse.CGetRequestContext) ([]dimse.CGetSubOperation, error) {
			uids, err := model.RetrieveUIDs(request.Identifier)
			if err != nil {
				return nil, err
			}
			for _, selected := range uids {
				if selected == uid {
					return []dimse.CGetSubOperation{{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: uid, LoadDataSet: func(context.Context) (*object.Object, error) { return data, nil }}}, nil
				}
			}
			return nil, nil
		})})
	}()
	contexts, err := model.PresentationContexts()
	if err != nil {
		return err
	}
	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{CalledAETitle: "NONPATIENT", CallingAETitle: "EXAMPLE", Contexts: contexts, RoleSelections: []ul.RoleSelectionItem{{SopClassUID: storage, SCPRole: true}}})
	if err != nil {
		return err
	}
	defer assoc.Close()
	stored := make(chan string, 1)
	session, err := dimse.NewAsyncSession(assoc, dimse.AsyncSessionOptions{CGetStorageSOPClassUIDs: []string{storage}, Handlers: map[uint16]dimse.AsyncRequestHandler{dimse.CStoreRQ: func(ctx context.Context, s *dimse.AsyncSession, message dimse.AsyncMessage) error {
		request, err := dimse.ParseCStoreRequest(message.Command)
		if err != nil {
			return err
		}
		if message.DataSet == nil {
			return fmt.Errorf("missing retrieved dataset")
		}
		got, _ := message.DataSet.GetString(uidTag)
		name, _ := message.DataSet.GetString(nameTag)
		class, _ := message.DataSet.GetString(classTag)
		if got != uid || name != "SYNTHETIC" || class != storage || request.AffectedSOPInstanceUID != uid || request.AffectedSOPClassUID != storage {
			return fmt.Errorf("retrieved object differs from catalog")
		}
		select {
		case stored <- got:
		default:
			return fmt.Errorf("duplicate storage suboperation")
		}
		return s.Respond(ctx, message, (dimse.CStoreResponse{AffectedSOPClassUID: storage, AffectedSOPInstanceUID: uid, Status: dimse.StatusSuccess}).CommandSet(), nil)
	}}})
	if err != nil {
		return err
	}
	defer session.Close()
	pc, err := dimse.AcceptedContextForSOPClass(assoc, find)
	if err != nil {
		return err
	}
	query := object.FromElements([]core.Element{textElement(nameTag, nameVR, "SYN*")}, std.Dictionary)
	op, err := model.StartCFind(ctx, session, pc.ID, dimse.CFindRequest{}, query)
	if err != nil {
		return err
	}
	match, err := op.Next(ctx)
	if err != nil {
		return err
	}
	if match.DataSet == nil {
		return fmt.Errorf("no discovered object")
	}
	discovered, ok := match.DataSet.Get(uidTag)
	if !ok {
		return fmt.Errorf("missing discovered identity")
	}
	final, err := op.Wait(ctx)
	if err != nil {
		return err
	}
	findResponse, err := dimse.ParseCFindResponse(final.Command)
	if err != nil {
		return err
	}
	if findResponse.Status != dimse.StatusSuccess {
		return fmt.Errorf("find status %04x", findResponse.Status)
	}
	pc, err = dimse.AcceptedContextForSOPClass(assoc, get)
	if err != nil {
		return err
	}
	op, err = model.StartCGet(ctx, session, pc.ID, dimse.CGetRequest{}, object.FromElements([]core.Element{discovered}, std.Dictionary))
	if err != nil {
		return err
	}
	final, err = op.Wait(ctx)
	if err != nil {
		return err
	}
	response, err := dimse.ParseCGetResponse(final.Command)
	if err != nil {
		return err
	}
	if response.Status != dimse.StatusSuccess {
		return fmt.Errorf("get status %04x", response.Status)
	}
	select {
	case got := <-stored:
		fmt.Printf("model=%d discovered=1 retrieved=1 verified_uid=%s\n", model, got)
	default:
		return fmt.Errorf("no storage suboperation")
	}
	if err := session.Release(ctx, dimse.AsyncReleaseWait); err != nil {
		return err
	}
	return <-done
}
