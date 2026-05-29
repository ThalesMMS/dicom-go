package dimse

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	pynetdicomInteropEnv = "DICOMGO_PYNETDICOM_INTEGRATION"
	pynetdicomPythonEnv  = "DICOMGO_PYTHON"
	mppsSOPClassUID      = "1.2.840.10008.3.1.2.3.3"
	mppsSOPInstanceUID   = "1.2.826.0.1.3680043.10.543.618.1"
)

var (
	modalityTag                     = core.NewTag(0x0008, 0x0060)
	performedProcedureStepStatusTag = core.NewTag(0x0040, 0x0252)
)

// These opt-in tests use pynetdicom as an independently implemented peer.
// Enable with:
//
//	DICOMGO_PYNETDICOM_INTEGRATION=1 go test ./net/dimse -run Pynetdicom -count=1 -v
//
// DICOMGO_PYTHON may name a Python interpreter from a virtual environment.
func TestNormalizedSCUAgainstPynetdicomMPPS(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, python, "-u", "-c", pynetdicomMPPSSCP)
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
		t.Fatalf("start pynetdicom MPPS SCP: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	})

	portLine := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.HasPrefix(scanner.Text(), "PORT ") {
				portLine <- strings.TrimSpace(strings.TrimPrefix(scanner.Text(), "PORT "))
				return
			}
		}
		close(portLine)
	}()

	var port string
	select {
	case port = <-portLine:
		if port == "" {
			t.Fatalf("pynetdicom MPPS SCP exited before reporting a port: %s", stderr.String())
		}
	case <-ctx.Done():
		t.Fatalf("pynetdicom MPPS SCP did not become ready: %v: %s", ctx.Err(), stderr.String())
	}

	assoc, err := ul.DialContext(ctx, "127.0.0.1:"+port, ul.DialOptions{
		CalledAETitle:  "PY_MPPS",
		CallingAETitle: "DICOMGO",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID: mppsSOPClassUID,
			TransferSyntaxUIDs: []string{
				transfer.ExplicitVRLittleEndian.UID,
				transfer.ImplicitVRLittleEndian.UID,
			},
		}},
	})
	if err != nil {
		t.Fatalf("associate with pynetdicom MPPS SCP: %v: %s", err, stderr.String())
	}
	defer assoc.Close()

	client := NewNormalizedClient(assoc)
	createResult, err := client.Create(ctx, NormalizedCreateRequest{
		AffectedSOPClassUID:    mppsSOPClassUID,
		CommandDataSetType:     DataSetPresent,
		AffectedSOPInstanceUID: mppsSOPInstanceUID,
	}, normalizedInteropDataSet(
		stringElement(performedProcedureStepStatusTag, core.VRCS, "IN PROGRESS"),
		stringElement(modalityTag, core.VRCS, "CT"),
	))
	if err != nil {
		t.Fatalf("N-CREATE against pynetdicom: %v: %s", err, stderr.String())
	}
	assertNormalizedInteropString(t, createResult.DataSet, performedProcedureStepStatusTag, "IN PROGRESS")
	assertNormalizedInteropString(t, createResult.DataSet, modalityTag, "CT")

	setResult, err := client.Set(ctx, NormalizedSetRequest{
		RequestedSOPClassUID:    mppsSOPClassUID,
		RequestedSOPInstanceUID: mppsSOPInstanceUID,
	}, normalizedInteropDataSet(
		stringElement(performedProcedureStepStatusTag, core.VRCS, "COMPLETED"),
	))
	if err != nil {
		t.Fatalf("N-SET against pynetdicom: %v: %s", err, stderr.String())
	}
	assertNormalizedInteropString(t, setResult.DataSet, performedProcedureStepStatusTag, "COMPLETED")

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("release pynetdicom association: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("stop pynetdicom SCP: %v", err)
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("pynetdicom MPPS SCP: %v: %s", err, stderr.String())
	}
}

func TestNormalizedSCPAgainstPynetdicomMPPS(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("listen for pynetdicom MPPS SCU: %v", err)
	}
	defer listener.Close()

	events := make(chan string, 2)
	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "DICOMGO",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{mppsSOPClassUID},
			SupportedTransferSyntaxes: []string{
				transfer.ExplicitVRLittleEndian.UID,
				transfer.ImplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer assoc.Close()
		serverDone <- ServeAssociation(ctx, assoc, AssociationSCPOptions{NormalizedSCP: &NormalizedSCPOptions{
			CreateHandler: func(_ context.Context, request NormalizedCreateRequest, dataSet *object.Object) (NormalizedCreateSCPResult, error) {
				status, _ := dataSet.GetString(performedProcedureStepStatusTag)
				events <- "CREATE " + status
				return NormalizedCreateSCPResult{DataSet: dataSet}, nil
			},
			SetHandler: func(_ context.Context, request NormalizedSetRequest, dataSet *object.Object) (NormalizedSetSCPResult, error) {
				status, _ := dataSet.GetString(performedProcedureStepStatusTag)
				events <- "SET " + status
				return NormalizedSetSCPResult{DataSet: dataSet}, nil
			},
		}})
	}()

	host, port, ok := strings.Cut(listener.Addr().String(), ":")
	if !ok {
		t.Fatalf("listener address %q has no port", listener.Addr())
	}
	cmd := exec.CommandContext(ctx, python, "-c", pynetdicomMPPSSCU, host, port)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		t.Fatalf("pynetdicom MPPS SCU: %v:\n%s", err, output.String())
	}

	for _, want := range []string{"CREATE IN PROGRESS", "SET COMPLETED"} {
		select {
		case got := <-events:
			if strings.TrimSpace(got) != want {
				t.Errorf("normalized SCP event = %q, want %q", got, want)
			}
		case <-ctx.Done():
			t.Fatalf("normalized SCP did not receive %q: %v", want, ctx.Err())
		}
	}
	select {
	case err := <-serverDone:
		if err != nil {
			t.Fatalf("normalized SCP: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("normalized SCP did not finish after release: %v", ctx.Err())
	}
}

func requirePynetdicomInterop(t *testing.T) string {
	t.Helper()
	if os.Getenv(pynetdicomInteropEnv) == "" {
		t.Skipf("set %s=1 to run independent N-DIMSE interoperability tests", pynetdicomInteropEnv)
	}
	python := os.Getenv(pynetdicomPythonEnv)
	if python == "" {
		python = "python3"
	}
	if err := exec.Command(python, "-c", "import pydicom, pynetdicom").Run(); err != nil {
		t.Fatalf("%s is set but %q cannot import pydicom and pynetdicom: %v", pynetdicomInteropEnv, python, err)
	}
	return python
}

func normalizedInteropDataSet(elements ...core.Element) *object.Object {
	return object.FromElements(elements, std.Dictionary)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}

func assertNormalizedInteropString(t *testing.T, dataSet *object.Object, tag core.Tag, want string) {
	t.Helper()
	if dataSet == nil {
		t.Fatalf("response dataset is nil; want %s=%q", tag, want)
	}
	got, ok := dataSet.GetString(tag)
	if !ok || strings.TrimSpace(got) != want {
		t.Fatalf("response %s = %q ok=%v, want %q", tag, got, ok, want)
	}
}

const pynetdicomMPPSSCP = `
import sys
from pynetdicom import AE, evt, ALL_TRANSFER_SYNTAXES
from pynetdicom.sop_class import ModalityPerformedProcedureStep

def on_create(event):
    return 0x0000, event.attribute_list

def on_set(event):
    return 0x0000, event.modification_list

ae = AE(ae_title="PY_MPPS")
ae.add_supported_context(ModalityPerformedProcedureStep, ALL_TRANSFER_SYNTAXES)
server = ae.start_server(("127.0.0.1", 0), block=False,
                         evt_handlers=[(evt.EVT_N_CREATE, on_create), (evt.EVT_N_SET, on_set)])
print("PORT %d" % server.server_address[1], flush=True)
sys.stdin.readline()
server.shutdown()
`

const pynetdicomMPPSSCU = `
import sys
from pydicom.dataset import Dataset
from pynetdicom import AE
from pynetdicom.sop_class import ModalityPerformedProcedureStep

ae = AE(ae_title="PY_MPPS")
ae.add_requested_context(ModalityPerformedProcedureStep)
assoc = ae.associate(sys.argv[1], int(sys.argv[2]), ae_title="DICOMGO")
if not assoc.is_established:
    raise RuntimeError("association rejected")

created = Dataset()
created.PerformedProcedureStepStatus = "IN PROGRESS"
created.Modality = "CT"
status, reply = assoc.send_n_create(created, ModalityPerformedProcedureStep, "` + mppsSOPInstanceUID + `")
assert status.Status == 0x0000, "N-CREATE status 0x%04X" % status.Status
assert reply.PerformedProcedureStepStatus == "IN PROGRESS"

modified = Dataset()
modified.PerformedProcedureStepStatus = "COMPLETED"
status, reply = assoc.send_n_set(modified, ModalityPerformedProcedureStep, "` + mppsSOPInstanceUID + `")
assert status.Status == 0x0000, "N-SET status 0x%04X" % status.Status
assert reply.PerformedProcedureStepStatus == "COMPLETED"
assoc.release()
`
