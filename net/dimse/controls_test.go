package dimse

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestRunSCPHandlerCancelGraceAbortsNonCooperativeHandler(t *testing.T) {
	local, peer := net.Pipe()
	defer func() { _ = local.Close() }()
	defer func() { _ = peer.Close() }()
	assoc := &ul.Association{Conn: local, Context: context.Background()}
	ctx, cancel := context.WithCancelCause(context.Background())
	releaseHandler := make(chan struct{})
	handlerStarted := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := runSCPHandler(ctx, assoc, 20*time.Millisecond, func(context.Context) (struct{}, error) {
			close(handlerStarted)
			<-releaseHandler
			return struct{}{}, nil
		})
		done <- err
	}()
	<-handlerStarted
	cancel(ErrCFindCanceled)

	select {
	case err := <-done:
		if !errors.Is(err, ErrCFindCanceled) || !errors.Is(err, ErrCancelGraceExceeded) {
			t.Fatalf("runSCPHandler() error = %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("runSCPHandler() exceeded cancel grace bound")
	}
	close(releaseHandler)
	if assoc.Conn != nil {
		t.Fatal("association remained open after cancel grace expired")
	}
}

func TestRunSCPHandlerRecoversPanickingHandlerWhenGraceIsEnabled(t *testing.T) {
	for _, grace := range []time.Duration{0, time.Second} {
		_, err := runSCPHandler(context.Background(), nil, grace, func(context.Context) (struct{}, error) {
			panic("clinical callback value must not escape")
		})
		if !errors.Is(err, ErrSCPHandlerPanic) {
			t.Fatalf("runSCPHandler(grace=%v) error = %v, want ErrSCPHandlerPanic", grace, err)
		}
	}
}

func TestSCPControlsRejectNegativeDurations(t *testing.T) {
	controls := []SCPControls{
		{CommandProgressTimeout: -1},
		{DataSetProgressTimeout: -1},
		{OperationTimeout: -1},
		{CancelGrace: -1},
	}
	for _, control := range controls {
		if err := control.validate(); err == nil {
			t.Fatalf("validate(%+v) succeeded", control)
		}
	}
}

func TestLegacyDIMSEIOInheritsCanceledAssociationContext(t *testing.T) {
	newAssociation := func(t *testing.T) *ul.Association {
		t.Helper()
		local, peer := net.Pipe()
		t.Cleanup(func() { _ = local.Close() })
		t.Cleanup(func() { _ = peer.Close() })
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		return &ul.Association{
			Conn:       local,
			Context:    ctx,
			MaxPDU:     ul.DefaultMaxPDU,
			PeerMaxPDU: ul.DefaultMaxPDU,
		}
	}
	tests := []struct {
		name string
		run  func(*ul.Association) error
	}{
		{name: "send command", run: func(assoc *ul.Association) error {
			return SendCommandSet(assoc, 1, CEchoRequest{MessageID: 1}.CommandSet())
		}},
		{name: "receive command", run: func(assoc *ul.Association) error {
			_, err := ReceiveCommandSet(assoc, 1)
			return err
		}},
		{name: "send dataset", run: func(assoc *ul.Association) error {
			return SendDataSet(assoc, 1, object.FromElements(nil, nil), transfer.ImplicitVRLittleEndian)
		}},
		{name: "receive dataset", run: func(assoc *ul.Association) error {
			_, err := ReceiveDataSet(assoc, 1, transfer.ImplicitVRLittleEndian)
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.run(newAssociation(t)); !errors.Is(err, context.Canceled) {
				t.Fatalf("legacy DIMSE I/O error = %v, want context.Canceled", err)
			}
		})
	}
}

func TestOptionalCommandWaitUsesProgressTimeoutAfterFirstFragment(t *testing.T) {
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: VerificationSOPClassUID,
		TransferSyntaxUID: ul.ImplicitVRLittleEndian,
	}})
	command, err := EncodeCommandSet(CEchoRequest{MessageID: 1}.CommandSet())
	if err != nil {
		t.Fatalf("EncodeCommandSet() error = %v", err)
	}
	ctx := withSCPControls(ul.WithoutIdleTimeout(context.Background()), SCPControls{CommandProgressTimeout: 20 * time.Millisecond})
	receiveDone := make(chan error, 1)
	go func() {
		_, _, err := ReceiveCommandSetAnyWithContext(ctx, local)
		receiveDone <- err
	}()
	if err := peer.Send(context.Background(), &ul.PDataTF{Values: []ul.PDataValue{{
		PresentationContextID: 1,
		IsCommand:             true,
		IsLast:                false,
		Data:                  command[:len(command)/2],
	}}}); err != nil {
		t.Fatalf("peer.Send(first command fragment) error = %v", err)
	}
	select {
	case err := <-receiveDone:
		if !errors.Is(err, ul.ErrAssociationTimeout) {
			t.Fatalf("ReceiveCommandSetAnyWithContext() error = %v, want ErrAssociationTimeout", err)
		}
	case <-time.After(time.Second):
		t.Fatal("partial optional command did not honor command progress timeout")
	}
}
