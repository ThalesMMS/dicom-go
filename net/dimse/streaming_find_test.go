package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestStreamingCFindClientAndSCPStreamDistinctResponseClass(t *testing.T) {
	const requestClass = "1.2.826.0.1.3680043.10.543.619.1"
	const responseClass = "1.2.826.0.1.3680043.10.543.619.2"
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: requestClass, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	route := StreamingCFindRoute{
		SOPClassUID: requestClass, ResponseSOPClassUID: responseClass,
		Limits: StreamingCFindLimits{MaxMatches: 2},
		Handler: StreamingCFindHandlerFunc(func(_ context.Context, request StreamingCFindRequest, yield StreamingCFindYield) error {
			if request.Identifier == nil {
				return ErrStreamingCFindIdentifier
			}
			match := object.New(nil)
			match.Put(core.Element{
				Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
				Value:  core.StringValue{"TEST^MATCH"},
			})
			return yield(StatusPendingWarning, match)
		}),
	}
	limits, err := route.Limits.normalized()
	if err != nil {
		t.Fatal(err)
	}
	route.Limits = limits
	serverDone := make(chan error, 1)
	go func() {
		command, receiveErr := receiveCommandSetWithContext(ctx, local, 1)
		if receiveErr != nil {
			serverDone <- receiveErr
			return
		}
		serverDone <- serveStreamingCFindCommand(ctx, local, 1, command, route)
	}()

	client, err := NewStreamingCFindClient(peer, requestClass, responseClass)
	if err != nil {
		t.Fatal(err)
	}
	identifier := object.New(nil)
	matches := 0
	result, err := client.Find(ctx, identifier, func(status uint16, match *object.Object) error {
		if status != StatusPendingWarning || match == nil {
			t.Fatalf("pending = 0x%04X, nil %t", status, match == nil)
		}
		matches++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if matches != 1 || result.MatchCount != 1 || result.FinalResponse == nil || result.FinalResponse.Status != StatusSuccess {
		t.Fatalf("matches/result = %d/%#v", matches, result)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestStreamingCFindRouterRejectsDuplicateRouteAndLateYield(t *testing.T) {
	handler := StreamingCFindHandlerFunc(func(context.Context, StreamingCFindRequest, StreamingCFindYield) error { return nil })
	if _, err := NewStreamingCFindRouter(nil,
		StreamingCFindRoute{SOPClassUID: "1.2.3", Handler: handler},
		StreamingCFindRoute{SOPClassUID: "1.2.3", Handler: handler},
	); !errors.Is(err, ErrStreamingCFindProvider) {
		t.Fatalf("duplicate route error = %v", err)
	}
	emitter := streamingCFindEmitter{active: true, yield: func(uint16, *object.Object) error { return nil }}
	emitter.close()
	if err := emitter.emit(StatusPending, object.New(nil)); !errors.Is(err, ErrStreamingCFindProvider) {
		t.Fatalf("late yield error = %v", err)
	}
}

func TestStreamingCFindSCPDrainsRejectedMultiPDUIdentifier(t *testing.T) {
	const sopClass = "1.2.826.0.1.3680043.10.543.619.3"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: sopClass, TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	route := StreamingCFindRoute{
		SOPClassUID: sopClass,
		Limits:      StreamingCFindLimits{MaxIdentifierBytes: 1024},
		Handler: StreamingCFindHandlerFunc(func(context.Context, StreamingCFindRequest, StreamingCFindYield) error {
			return nil
		}),
	}
	serverDone := make(chan error, 1)
	go func() {
		for operation := 0; operation < 2; operation++ {
			command, err := receiveCommandSetWithContext(ctx, local, 1)
			if err != nil {
				serverDone <- err
				return
			}
			err = ServeStreamingCFindCommand(ctx, local, 1, command, route)
			if operation == 0 {
				if !errors.Is(err, ErrStreamingCFindResourceLimit) {
					serverDone <- err
					return
				}
				continue
			}
			serverDone <- err
		}
	}()

	client, err := NewStreamingCFindClient(peer, sopClass, sopClass)
	if err != nil {
		t.Fatal(err)
	}
	large := object.New(nil)
	large.Put(core.Element{
		Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VROB},
		Value:  core.RawValue(make([]byte, 64<<10)),
	})
	if _, err := client.Find(ctx, large, func(uint16, *object.Object) error { return nil }); err == nil {
		t.Fatal("oversized Identifier unexpectedly succeeded")
	} else {
		var statusErr *StreamingCFindStatusError
		if !errors.As(err, &statusErr) || statusErr.Status != CFindStatusOutOfResources {
			t.Fatalf("oversized Identifier error = %v", err)
		}
	}
	if _, err := client.Find(ctx, object.New(nil), func(uint16, *object.Object) error { return nil }); err != nil {
		t.Fatalf("association was not reusable after drained Identifier: %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}
