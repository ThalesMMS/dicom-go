package ups_test

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/ups"
)

func TestUPSClientPushPullWatchRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	contexts := []ul.AcceptedContext{
		{ID: 1, AbstractSyntaxUID: ups.PushSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
		{ID: 3, AbstractSyntaxUID: ups.PullSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
		{ID: 5, AbstractSyntaxUID: ups.WatchSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID},
	}
	peer, local := pipeUPSAssociations(t, contexts)
	store, _ := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	service, _ := ups.NewService(store, ups.ServiceOptions{CallbackResolver: ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
		return ups.CallbackTarget{Address: "127.0.0.1:11112"}, nil
	})})
	serverOptions := service.NormalizedOptions(local)
	serverDone := make(chan error, 1)
	go func() {
		for _, pcID := range []byte{1, 3, 5, 5} {
			command, err := dimse.ReceiveCommandSet(local, pcID)
			if err != nil {
				serverDone <- err
				return
			}
			if err := dimse.ServeNormalizedCommand(ctx, local, pcID, command, serverOptions); err != nil && !isHandledNormalizedTestError(err) {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()

	client := ups.NewClient(peer)
	const uid = "1.2.826.0.1.3680043.10.543.619.30"
	if result, err := client.Create(ctx, uid, scheduledAttributes(t)); err != nil || result.Status != ups.StatusSuccess {
		t.Fatalf("Create = %#v, %v", result, err)
	}
	const transactionUID = "1.2.826.0.1.3680043.10.543.619.31"
	if result, err := client.ChangeState(ctx, uid, ups.StateInProgress, transactionUID); err != nil || result.Status != ups.StatusSuccess {
		t.Fatalf("ChangeState = %#v, %v", result, err)
	}
	if result, err := client.Subscribe(ctx, uid, "WATCHER", true); err != nil || result.Status != ups.StatusSuccess {
		t.Fatalf("Subscribe = %#v, %v", result, err)
	}
	result, err := client.Get(ctx, ups.WatchSOPClassUID, uid, []core.Tag{ups.TagProcedureStepState})
	if err != nil || result.Status != ups.StatusSuccess || result.DataSet == nil {
		t.Fatalf("Get = %#v, %v", result, err)
	}
	if state, ok := result.DataSet.GetString(ups.TagProcedureStepState); !ok || state != string(ups.StateInProgress) {
		t.Fatalf("state = %q, present %t", state, ok)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestUPSQueryClientStreamsThroughAssociationRouter(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, local := pipeUPSAssociations(t, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: ups.QuerySOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}})
	store, _ := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	service, _ := ups.NewService(store, ups.ServiceOptions{})
	createScheduledStep(t, service, "1.2.826.0.1.3680043.10.543.619.41", "QUERY^MATCH")
	routes, err := service.QueryRoutes(ups.QuerySCPOptions{})
	if err != nil {
		t.Fatal(err)
	}
	router, err := dimse.NewStreamingCFindRouter(nil, routes...)
	if err != nil {
		t.Fatal(err)
	}
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- dimse.ServeAssociation(ctx, local, dimse.AssociationSCPOptions{CFindHandler: router})
	}()

	client, err := ups.NewQueryClient(peer, ups.QuerySOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	query := ups.Query{Keys: map[core.Tag]ups.QueryKey{
		ups.TagPatientName: ups.Match("QUERY*"), ups.TagSOPInstanceUID: ups.ReturnKey(),
	}}
	var gotUID string
	result, err := client.Find(ctx, query, func(match ups.QueryMatch) error {
		if match.Status != dimse.StatusPending || match.Identifier == nil {
			return errors.New("unexpected pending response")
		}
		gotUID, _ = match.Identifier.GetUID(ups.TagSOPInstanceUID)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if gotUID != "1.2.826.0.1.3680043.10.543.619.41" || result.MatchCount != 1 {
		t.Fatalf("UID/result = %q/%#v", gotUID, result)
	}
	_ = peer.Close()
	select {
	case <-serverDone:
	case <-ctx.Done():
		t.Fatal("ServeAssociation did not stop")
	}
}

func TestUPSAddressOnlyCallbackNegotiatesAndDeliversEvent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, local := pipeUPSAssociations(t, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: ups.EventSOPClassUID, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}})

	received := make(chan ups.ReceivedEvent, 1)
	receiverStore, _ := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	receiver, err := ups.NewService(receiverStore, ups.ServiceOptions{EventHandler: ups.EventHandlerFunc(func(_ context.Context, event ups.ReceivedEvent) error {
		received <- event
		return nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	receiverOptions := receiver.NormalizedOptions(local)
	serverDone := make(chan error, 1)
	go func() {
		serverDone <- dimse.ServeAssociation(ctx, local, dimse.AssociationSCPOptions{NormalizedSCP: &receiverOptions})
	}()

	var dialOptions ul.DialOptions
	store, _ := ups.NewMemoryStore(ups.MemoryStoreOptions{})
	sender, err := ups.NewService(store, ups.ServiceOptions{
		CallbackCallingAE: "UPS_SCP",
		CallbackResolver: ups.CallbackResolverFunc(func(context.Context, ups.CallbackRequest) (ups.CallbackTarget, error) {
			return ups.CallbackTarget{Address: "callback.invalid:11112"}, nil
		}),
		AssociationDialer: ups.AssociationDialerFunc(func(_ context.Context, _ string, options ul.DialOptions) (*ul.Association, error) {
			dialOptions = options
			return peer, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	const uid = "1.2.826.0.1.3680043.10.543.619.42"
	if _, err := sender.Create(ctx, ups.CreateRequest{SOPInstanceUID: uid, Attributes: scheduledAttributes(t)}); err != nil {
		t.Fatal(err)
	}
	if _, err := sender.Subscribe(ctx, ups.SubscribeRequest{SOPInstanceUID: uid, ReceivingAETitle: "WATCHER"}); err != nil {
		t.Fatal(err)
	}
	if err := sender.DeliverDue(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if dialOptions.CallingAETitle != "UPS_SCP" || dialOptions.CalledAETitle != "WATCHER" || len(dialOptions.Contexts) != 1 || dialOptions.Contexts[0].AbstractSyntaxUID != ups.EventSOPClassUID {
		t.Fatalf("callback DialOptions = %#v", dialOptions)
	}
	select {
	case event := <-received:
		if event.Type != ups.EventStateReport || event.SOPInstanceUID != uid {
			t.Fatalf("event = %#v", event)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func pipeUPSAssociations(t *testing.T, contexts []ul.AcceptedContext) (*ul.Association, *ul.Association) {
	t.Helper()
	peerConn, localConn := net.Pipe()
	clone := func() []ul.AcceptedContext { return append([]ul.AcceptedContext(nil), contexts...) }
	peer := &ul.Association{Conn: peerConn, MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU, AcceptedContexts: clone()}
	local := &ul.Association{Conn: localConn, MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU, AcceptedContexts: clone()}
	t.Cleanup(func() {
		_ = peer.Close()
		_ = local.Close()
	})
	return peer, local
}

func isHandledNormalizedTestError(err error) bool {
	var handlerErr *dimse.NormalizedSCPHandlerError
	return errors.As(err, &handlerErr)
}
