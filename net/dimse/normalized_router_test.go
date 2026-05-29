package dimse

import (
	"context"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCombineNormalizedSCPOptionsRoutesByCommandSOPClass(t *testing.T) {
	t.Parallel()
	firstCalled := false
	secondCalled := false
	combined, err := CombineNormalizedSCPOptions(
		NormalizedSCPRoute{SOPClassUID: "1.2.3", Options: NormalizedSCPOptions{
			MaxDataSetBytes: 1024,
			ActionHandler: func(context.Context, NormalizedActionRequest, *object.Object) (NormalizedActionSCPResult, error) {
				firstCalled = true
				return NormalizedActionSCPResult{}, nil
			},
		}},
		NormalizedSCPRoute{SOPClassUID: "1.2.4", Options: NormalizedSCPOptions{
			MaxDataSetBytes: 2048,
			ActionHandler: func(context.Context, NormalizedActionRequest, *object.Object) (NormalizedActionSCPResult, error) {
				secondCalled = true
				return NormalizedActionSCPResult{}, nil
			},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if combined.MaxDataSetBytes != 2048 {
		t.Fatalf("MaxDataSetBytes = %d", combined.MaxDataSetBytes)
	}
	if _, err := combined.ActionHandler(context.Background(), NormalizedActionRequest{RequestedSOPClassUID: "1.2.4"}, nil); err != nil {
		t.Fatal(err)
	}
	if firstCalled || !secondCalled {
		t.Fatalf("route calls first=%v second=%v", firstCalled, secondCalled)
	}
	if err := combined.PresentationContextPolicy(ul.AcceptedContext{AbstractSyntaxUID: "1.2.4"}, "1.2.9"); err == nil {
		t.Fatal("unknown command SOP Class accepted")
	}
}

func TestNormalizedRequestInfoRoundTrip(t *testing.T) {
	t.Parallel()
	want := NormalizedRequestInfo{
		PresentationContext: ul.AcceptedContext{ID: 7, AbstractSyntaxUID: "1.2.3"},
		TransferSyntax:      transfer.ExplicitVRLittleEndian,
	}
	ctx := withNormalizedRequestInfo(context.Background(), want)
	got, ok := NormalizedRequestInfoFromContext(ctx)
	if !ok || got.PresentationContext != want.PresentationContext || got.TransferSyntax.UID != want.TransferSyntax.UID {
		t.Fatalf("request info = %#v, %v", got, ok)
	}
}

func TestNormalizedDataSetStructuralLimitDrainsBeforeNextOperation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID: 1, AbstractSyntaxUID: "1.2.3", TransferSyntaxUID: transfer.ExplicitVRLittleEndian.UID,
	}})
	defer func() { _ = peer.Close() }()
	defer func() { _ = local.Close() }()

	first := object.New(nil)
	first.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0010), VR: core.VRLO}, Value: core.StringValue{"one"}})
	first.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0012), VR: core.VRLO}, Value: core.StringValue{"two"}})
	second := object.New(nil)
	second.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x7777, 0x0014), VR: core.VRLO}, Value: core.StringValue{"next"}})
	sendDone := make(chan error, 1)
	go func() {
		if err := SendDataSetWithContext(ctx, peer, 1, first, transfer.ExplicitVRLittleEndian); err != nil {
			sendDone <- err
			return
		}
		sendDone <- SendDataSetWithContext(ctx, peer, 1, second, transfer.ExplicitVRLittleEndian)
	}()

	if _, err := receiveDataSetWithContextAndLimits(ctx, local, 1, transfer.ExplicitVRLittleEndian, 1<<20, 1, 8); err == nil {
		t.Fatal("dataset exceeding MaxElements unexpectedly succeeded")
	}
	got, err := receiveDataSetWithContextAndLimits(ctx, local, 1, transfer.ExplicitVRLittleEndian, 1<<20, 1, 8)
	if err != nil || got.Len() != 1 {
		t.Fatalf("next dataset = %#v, %v", got, err)
	}
	if err := <-sendDone; err != nil {
		t.Fatal(err)
	}
}
