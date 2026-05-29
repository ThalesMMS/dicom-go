package dimse

import (
	"bufio"
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestModalityWorklistSCUAgainstPynetdicom(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	command := exec.CommandContext(ctx, python, "-u", "-c", pynetdicomModalityWorklistSCP)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("pynetdicom stdin pipe: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("pynetdicom stdout pipe: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start pynetdicom MWL SCP: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
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
	var port string
	select {
	case port = <-ready:
		if port == "" {
			t.Fatalf("pynetdicom MWL SCP exited before reporting a port: %s", stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("pynetdicom MWL SCP did not become ready: %v: %s", ctx.Err(), stderr.String())
	}

	association, err := ul.DialContext(ctx, "127.0.0.1:"+port, ul.DialOptions{
		CalledAETitle: "PY_MWL", CallingAETitle: "GO_MWL",
		Contexts: []ul.PresentationContext{ModalityWorklistFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("associate with pynetdicom MWL SCP: %v: %s", err, stderr.String())
	}
	client, err := NewModalityWorklistClient(association)
	if err != nil {
		t.Fatalf("NewModalityWorklistClient() error = %v", err)
	}
	var got []string
	result, err := client.Find(ctx, ModalityWorklistQuery{
		PatientName: MWLMatch("INTEROP*"),
		ScheduledProcedureStep: &ModalityWorklistScheduledProcedureStep{
			Modality:                        MWLReturnKey(),
			ScheduledProcedureStepStartDate: MWLMatch("20260810-20260811"),
		},
	}, func(match *object.Object) error {
		name, _ := match.GetString(tagMWLPatientName)
		steps, ok := match.GetSequence(tagMWLScheduledProcedureStepSequence)
		if !ok || len(steps) != 1 {
			t.Fatalf("pynetdicom match SPS items = %d, present %t", len(steps), ok)
		}
		modality, _ := steps[0].GetString(tagMWLModality)
		got = append(got, name+"/"+modality)
		return nil
	})
	if err != nil {
		_ = association.Close()
		t.Fatalf("MWL query against pynetdicom: %v: %s", err, stderr.String())
	}
	if result.MatchCount != 2 || strings.Join(got, ",") != "INTEROP^ONE/CT,INTEROP^TWO/MR" {
		t.Fatalf("pynetdicom MWL result = %#v, matches %v", result, got)
	}
	if err := association.Release(ctx); err != nil {
		t.Fatalf("release pynetdicom MWL association: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("stop pynetdicom MWL SCP: %v", err)
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("pynetdicom MWL SCP: %v: %s", err, stderr.String())
	}
}

func TestModalityWorklistSCPAgainstPynetdicom(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("listen for pynetdicom MWL SCU: %v", err)
	}
	defer listener.Close()

	handler, err := NewInMemoryModalityWorklistHandler([]*object.Object{
		modalityWorklistTestRecord("PYNET^ONE", "CT_AE", "CT", "20260810", "090000"),
		modalityWorklistTestRecord("OTHER^PATIENT", "MR_AE", "MR", "20260811", "100000"),
	})
	if err != nil {
		t.Fatalf("NewInMemoryModalityWorklistHandler() error = %v", err)
	}
	router, err := NewModalityWorklistCFindRouter(nil, handler, ModalityWorklistSCPOptions{})
	if err != nil {
		t.Fatalf("NewModalityWorklistCFindRouter() error = %v", err)
	}
	serverDone := make(chan error, 1)
	go func() {
		association, acceptErr := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle: "GO_MWL", Context: ctx, RequireMatchingCalledAE: true,
			SupportedAbstractSyntaxes: []string{ModalityWorklistFindSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
		})
		if acceptErr != nil {
			serverDone <- acceptErr
			return
		}
		defer association.Close()
		serverDone <- ServeAssociation(ctx, association, AssociationSCPOptions{CFindHandler: router})
	}()

	host, port, ok := strings.Cut(listener.Addr().String(), ":")
	if !ok {
		t.Fatalf("listener address %q has no port", listener.Addr())
	}
	command := exec.CommandContext(ctx, python, "-c", pynetdicomModalityWorklistSCU, host, port)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("pynetdicom MWL SCU: %v:\n%s", err, output.String())
	}
	if !strings.Contains(output.String(), "MATCH PYNET^ONE CT") || !strings.Contains(output.String(), "FINAL 0000 1") {
		t.Fatalf("pynetdicom MWL output:\n%s", output.String())
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("Go MWL SCP: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("Go MWL SCP did not finish: %v", ctx.Err())
	}
}

const pynetdicomModalityWorklistSCP = `
import sys
from pydicom.dataset import Dataset
from pynetdicom import AE, evt, ALL_TRANSFER_SYNTAXES
from pynetdicom.sop_class import ModalityWorklistInformationFind

def record(name, modality, date, time_value):
    item = Dataset()
    item.Modality = modality
    item.ScheduledProcedureStepStartDate = date
    item.ScheduledProcedureStepStartTime = time_value
    result = Dataset()
    result.PatientName = name
    result.ScheduledProcedureStepSequence = [item]
    return result

def on_find(event):
    identifier = event.identifier
    assert str(identifier.PatientName) == "INTEROP*"
    assert len(identifier.ScheduledProcedureStepSequence) == 1
    assert identifier.ScheduledProcedureStepSequence[0].ScheduledProcedureStepStartDate == "20260810-20260811"
    yield 0xFF00, record("INTEROP^ONE", "CT", "20260810", "090000")
    yield 0xFF00, record("INTEROP^TWO", "MR", "20260811", "100000")

ae = AE(ae_title="PY_MWL")
ae.add_supported_context(ModalityWorklistInformationFind, ALL_TRANSFER_SYNTAXES)
server = ae.start_server(("127.0.0.1", 0), block=False, evt_handlers=[(evt.EVT_C_FIND, on_find)])
print("PORT %d" % server.server_address[1], flush=True)
sys.stdin.readline()
server.shutdown()
`

const pynetdicomModalityWorklistSCU = `
import sys
from pydicom.dataset import Dataset
from pynetdicom import AE
from pynetdicom.sop_class import ModalityWorklistInformationFind

query = Dataset()
query.PatientName = "PYNET*"
step = Dataset()
step.Modality = ""
step.ScheduledProcedureStepStartDate = ""
query.ScheduledProcedureStepSequence = [step]

ae = AE(ae_title="PY_MWL")
ae.add_requested_context(ModalityWorklistInformationFind)
assoc = ae.associate(sys.argv[1], int(sys.argv[2]), ae_title="GO_MWL")
if not assoc.is_established:
    raise RuntimeError("association rejected")
count = 0
final = None
for status, identifier in assoc.send_c_find(query, ModalityWorklistInformationFind):
    if status is None:
        raise RuntimeError("connection lost")
    final = status.Status
    if status.Status in (0xFF00, 0xFF01):
        count += 1
        step = identifier.ScheduledProcedureStepSequence[0]
        print("MATCH %s %s" % (identifier.PatientName, step.Modality))
print("FINAL %04X %d" % (final, count))
assoc.release()
`
