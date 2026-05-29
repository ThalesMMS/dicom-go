package dimse

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// TestStorageCommitmentWorkflowAgainstPynetdicom exercises the complete Push
// Model against an independently implemented peer: pynetdicom accepts the
// N-ACTION, releases the original association, then opens a callback
// association and sends a partial-failure N-EVENT-REPORT.
func TestStorageCommitmentWorkflowAgainstPynetdicom(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("listen for pynetdicom callback: %v", err)
	}
	defer listener.Close()
	callbackHost, callbackPort, ok := strings.Cut(listener.Addr().String(), ":")
	if !ok {
		t.Fatalf("callback address %q has no port", listener.Addr())
	}

	cmd := exec.CommandContext(ctx, python, "-u", "-c", pynetdicomStorageCommitmentPeer, callbackHost, callbackPort)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("pynetdicom stdin pipe: %v", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("pynetdicom stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start pynetdicom Storage Commitment peer: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	ready := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "PORT ") {
				ready <- strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "PORT "))
				return
			}
		}
		close(ready)
	}()
	var actionPort string
	select {
	case actionPort = <-ready:
		if actionPort == "" {
			t.Fatalf("pynetdicom peer exited before reporting a port: %s", stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("pynetdicom peer did not become ready: %v: %s", ctx.Err(), stderr.String())
	}

	store := newStorageCommitmentTestStore(t)
	workflow := newStorageCommitmentTestWorkflow(t, StorageCommitmentWorkflowOptions{Store: store})
	callbackDone := make(chan error, 1)
	handlerError := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "GO_REQUESTOR", Context: ctx, RequireMatchingCalledAE: true,
			SupportedAbstractSyntaxes: []string{StorageCommitmentPushModelSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
			RoleSelections: []ul.RoleSelectionItem{{
				SopClassUID: StorageCommitmentPushModelSOPClassUID, SCURole: true, SCPRole: true,
			}},
		})
		if err == nil {
			options := workflow.NormalizedOptions(assoc)
			eventHandler := options.EventReportHandler
			options.EventReportHandler = func(handlerCtx context.Context, request NormalizedEventReportRequest, dataSet *object.Object) (NormalizedEventReportSCPResult, error) {
				var diagnostic error
				if commandErr := validateStorageCommitmentEventCommand(request); commandErr != nil {
					diagnostic = fmt.Errorf("command %#v: %w", request, commandErr)
				} else if information, parseErr := ParseStorageCommitmentEventInformation(dataSet); parseErr != nil {
					diagnostic = fmt.Errorf("dataset: %w", parseErr)
				} else if _, semanticErr := storageCommitmentResultFromEvent(request, information); semanticErr != nil {
					diagnostic = fmt.Errorf("semantics: %w", semanticErr)
				}
				result, handlerErr := eventHandler(handlerCtx, request, dataSet)
				if handlerErr != nil {
					if diagnostic == nil {
						diagnostic = fmt.Errorf("context: %w", handlerErr)
					}
					select {
					case handlerError <- diagnostic:
					default:
					}
				}
				return result, handlerErr
			}
			err = ServeAssociation(ctx, assoc, AssociationSCPOptions{NormalizedSCP: &options})
			_ = assoc.Close()
		}
		callbackDone <- err
	}()

	assoc, err := ul.DialContext(ctx, "127.0.0.1:"+actionPort, ul.DialOptions{
		CalledAETitle: "PY_COMMIT", CallingAETitle: "GO_REQUESTOR",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  StorageCommitmentPushModelSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("associate with pynetdicom Storage Commitment SCP: %v: %s", err, stderr.String())
	}
	transaction, err := workflow.Request(ctx, assoc, storageCommitmentTestReferences, StorageCommitmentRequestOptions{
		TransactionUID: "2.25.62101", DeliveryMode: StorageCommitmentDeliveryCallback,
	})
	if err != nil {
		_ = assoc.Close()
		t.Fatalf("N-ACTION against pynetdicom: %v: %s", err, stderr.String())
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("release original association: %v", err)
	}

	select {
	case err := <-callbackDone:
		if err != nil {
			select {
			case handlerErr := <-handlerError:
				t.Fatalf("pynetdicom callback handler: %v: %s", handlerErr, stderr.String())
			default:
				t.Fatalf("receive pynetdicom callback: %v: %s", err, stderr.String())
			}
		}
	case <-ctx.Done():
		select {
		case handlerErr := <-handlerError:
			t.Fatalf("pynetdicom callback handler: %v: %s", handlerErr, stderr.String())
		default:
			t.Fatalf("pynetdicom callback timed out: %v: %s", ctx.Err(), stderr.String())
		}
	}
	received, err := store.Get(ctx, transaction.TransactionUID)
	if err != nil || received.State != StorageCommitmentStateResultReceived || received.Result == nil || len(received.Result.ReferencedSOPs) != 1 || len(received.Result.FailedSOPs) != 1 || received.Result.FailedSOPs[0].FailureReason != 0x0112 {
		t.Fatalf("pynetdicom result = %#v, %v", received, err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("stop pynetdicom peer: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pynetdicom Storage Commitment peer: %v: %s", err, stderr.String())
	}
}

const pynetdicomStorageCommitmentPeer = `
import copy
import sys
import threading
from pydicom.dataset import Dataset
from pynetdicom import AE, evt, ALL_TRANSFER_SYNTAXES, build_role
from pynetdicom.sop_class import StorageCommitmentPushModel

WELL_KNOWN_INSTANCE = "1.2.840.10008.1.20.1.1"
received = threading.Event()
action = None

def on_action(event):
    global action
    assert event.action_type == 1
    action = copy.deepcopy(event.action_information)
    received.set()
    return 0x0000, None

ae = AE(ae_title="PY_COMMIT")
ae.add_supported_context(StorageCommitmentPushModel, ALL_TRANSFER_SYNTAXES)
server = ae.start_server(("127.0.0.1", 0), block=False,
                         evt_handlers=[(evt.EVT_N_ACTION, on_action)])
print("PORT %d" % server.server_address[1], flush=True)
if not received.wait(15):
    raise RuntimeError("N-ACTION not received")

event_info = Dataset()
event_info.TransactionUID = action.TransactionUID
event_info.ReferencedSOPSequence = [copy.deepcopy(action.ReferencedSOPSequence[0])]
failed = copy.deepcopy(action.ReferencedSOPSequence[1])
failed.FailureReason = 0x0112
event_info.FailedSOPSequence = [failed]

callback = AE(ae_title="PY_COMMIT")
callback.add_requested_context(StorageCommitmentPushModel)
role = build_role(StorageCommitmentPushModel, scu_role=True, scp_role=True)
assoc = callback.associate(sys.argv[1], int(sys.argv[2]), ae_title="GO_REQUESTOR", ext_neg=[role])
if not assoc.is_established:
    raise RuntimeError("callback association rejected")
status, reply = assoc.send_n_event_report(event_info, 2, StorageCommitmentPushModel,
                                          WELL_KNOWN_INSTANCE)
if status.Status != 0x0000:
    assoc.abort()
    server.shutdown()
    raise RuntimeError("N-EVENT-REPORT status 0x%04X" % status.Status)
assoc.release()
server.shutdown()
sys.stdin.readline()
`
