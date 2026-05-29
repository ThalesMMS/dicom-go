package dimse

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// TestAsyncSessionAgainstPynetdicomNegotiation is opt-in because it executes
// an independently implemented Python peer. pynetdicom can negotiate the
// 0x53 item but deliberately performs DIMSE synchronously; this direction
// verifies a finite asymmetric offer/acceptance plus a real C-ECHO message.
func TestAsyncSessionAgainstPynetdicomNegotiation(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	serverDone := make(chan error, 1)
	go func() {
		association, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "DICOMGO", Context: ctx,
			SupportedAbstractSyntaxes:    []string{VerificationSOPClassUID},
			SupportedTransferSyntaxes:    []string{transfer.ImplicitVRLittleEndian.UID},
			AsynchronousOperationsWindow: &ul.AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3},
		})
		if err != nil {
			serverDone <- err
			return
		}
		if got := association.EffectiveAsynchronousOperationsWindow(); got != (ul.AsynchronousOperationsWindow{MaximumInvoked: 2, MaximumPerformed: 3}) {
			serverDone <- errors.New("unexpected negotiated server window")
			_ = association.Close()
			return
		}
		handler := func(handlerCtx context.Context, handlerSession *AsyncSession, message AsyncMessage) error {
			return handlerSession.Respond(handlerCtx, message, (CEchoResponse{Status: StatusSuccess}).CommandSet(), nil)
		}
		session, err := NewAsyncSession(association, AsyncSessionOptions{Handlers: map[uint16]AsyncRequestHandler{CEchoRQ: handler}})
		if err != nil {
			serverDone <- err
			return
		}
		<-session.Done()
		if err := session.Err(); err != nil && !errors.Is(err, ErrAssociationReleased) {
			serverDone <- err
			return
		}
		serverDone <- nil
	}()

	host, port, err := splitInteropAddress(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(ctx, python, "-c", pynetdicomAsyncSCU, host, port)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("pynetdicom asynchronous negotiation: %v:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "WINDOW 3 2") || !strings.Contains(output.String(), "ECHO 0000") {
		t.Fatalf("pynetdicom output:\n%s", output.String())
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func splitInteropAddress(address string) (string, string, error) {
	for index := len(address) - 1; index >= 0; index-- {
		if address[index] == ':' {
			return address[:index], address[index+1:], nil
		}
	}
	return "", "", errors.New("invalid listener address")
}

const pynetdicomAsyncSCU = `
import sys
from pynetdicom import AE
from pynetdicom.pdu_primitives import AsynchronousOperationsWindowNegotiation
from pynetdicom.sop_class import Verification

ae = AE(ae_title="PY_ASYNC")
ae.add_requested_context(Verification)
item = AsynchronousOperationsWindowNegotiation()
item.maximum_number_operations_invoked = 4
item.maximum_number_operations_performed = 3
assoc = ae.associate(sys.argv[1], int(sys.argv[2]), ae_title="DICOMGO", ext_neg=[item])
if not assoc.is_established:
    raise RuntimeError("association rejected")
invoked, performed = assoc.acceptor.asynchronous_operations
print(f"WINDOW {invoked} {performed}")
status = assoc.send_c_echo()
print(f"ECHO {int(status.Status):04X}")
assoc.release()
`
