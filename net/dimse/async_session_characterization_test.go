package dimse

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestAsyncSessionFacadeConcurrentLifecycleCharacterization(t *testing.T) {
	client, server := newAsyncSessionPair(t, 8, 8, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: VerificationSOPClassUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}})
	server.Handle(CEchoRQ, func(ctx context.Context, session *AsyncSession, message AsyncMessage) error {
		return session.Respond(ctx, message, (CEchoResponse{
			MessageIDBeingRespondedTo: message.MessageID,
			Status:                    StatusSuccess,
		}).CommandSet(), nil)
	})

	const operationCount = 8
	operations := make([]*AsyncOperation, operationCount)
	messageIDs := make(map[uint16]struct{}, operationCount)
	for index := range operations {
		operation, err := client.StartCEcho(testContext(t))
		if err != nil {
			t.Fatalf("StartCEcho(%d): %v", index, err)
		}
		if _, duplicate := messageIDs[operation.MessageID()]; duplicate || operation.MessageID() == 0 {
			t.Fatalf("invalid or duplicate message ID %d", operation.MessageID())
		}
		messageIDs[operation.MessageID()] = struct{}{}
		operations[index] = operation
	}
	for index, operation := range operations {
		response, err := operation.Wait(testContext(t))
		if err != nil {
			t.Fatalf("Wait(%d): %v", index, err)
		}
		status, err := CommandUint16(response.Command, Status)
		if err != nil || status != StatusSuccess {
			t.Fatalf("response(%d) status = 0x%04X, err = %v", index, status, err)
		}
	}
	if metrics := client.Snapshot(); metrics.ActiveInvoked != 0 {
		t.Fatalf("active operations after completion = %+v", metrics)
	}

	closed := make(chan error, 4)
	for range cap(closed) {
		go func() { closed <- client.Close() }()
	}
	for range cap(closed) {
		select {
		case <-closed:
		case <-time.After(2 * time.Second):
			t.Fatal("concurrent Close did not finish within the lifecycle bound")
		}
	}
	select {
	case <-client.Done():
	default:
		t.Fatal("Close returned before Done was closed")
	}
}
