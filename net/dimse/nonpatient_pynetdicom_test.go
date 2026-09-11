package dimse

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

// This independent peer supplies real profile abstract syntaxes, identifiers,
// role negotiation and reverse/destination C-STORE. It compares all supplied
// metadata, including the nested HP definition, not just DIMSE success.
func TestNonPatientProfilesAgainstPynetdicom(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, port, err := net.SplitHostPort(listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, python, "-u", "-c", nonPatientPythonPeer, port)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr integrityPeerLog
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() || !strings.HasPrefix(scanner.Text(), "READY ") {
		t.Fatalf("peer startup: %s", stderr.String())
	}
	moveAddress := strings.TrimPrefix(scanner.Text(), "READY ")
	var classes []string
	var roles []ul.RoleSelectionItem
	var routes []StreamingCFindRoute
	instances := map[string]*object.Object{}
	for _, model := range []NonPatientModel{HangingProtocolModel, ColorPaletteModel} {
		data := npInstance(model, 1)
		uid, _ := data.GetString(npTag(8, 0x18))
		instances[uid] = data
		storage, find, move, get, _ := model.SOPClasses()
		classes = append(classes, storage, find, move, get)
		roles = append(roles, ul.RoleSelectionItem{SopClassUID: storage, SCPRole: true})
		route, err := model.FindRoute(func(_ context.Context, yield func(*object.Object) error) error { return yield(data) }, StreamingCFindLimits{})
		if err != nil {
			t.Fatal(err)
		}
		routes = append(routes, route)
	}
	router, err := NewStreamingCFindRouter(nil, routes...)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "NONPATIENT", Context: ctx, SupportedAbstractSyntaxes: classes, SupportedTransferSyntaxes: []string{ul.ExplicitVRLittleEndian}, RoleSelections: roles})
		if err != nil {
			done <- err
			return
		}
		defer assoc.Close()
		done <- ServeAssociation(ctx, assoc, AssociationSCPOptions{CFindHandler: router, CGetHandler: CGetHandlerFunc(func(_ context.Context, request CGetRequestContext) ([]CGetSubOperation, error) {
			model, _ := nonPatientRetrieveModel(request.Request.AffectedSOPClassUID)
			uids, err := model.RetrieveUIDs(request.Identifier)
			if err != nil {
				return nil, err
			}
			var ops []CGetSubOperation
			for _, uid := range uids {
				if data := instances[uid]; data != nil {
					class, _ := data.GetString(npTag(8, 0x16))
					ops = append(ops, CGetSubOperation{AffectedSOPClassUID: class, AffectedSOPInstanceUID: uid, LoadDataSet: func(context.Context) (*object.Object, error) { return data, nil }})
				}
			}
			return ops, nil
		}), CMoveHandler: CMoveHandlerFunc(func(_ context.Context, request CMoveRequestContext) ([]CMoveSubOperation, error) {
			if request.Request.MoveDestination != "DEST" {
				return nil, NewCMoveSCPError(0xA801, "Unknown destination", nil)
			}
			model, _ := nonPatientRetrieveModel(request.Request.AffectedSOPClassUID)
			uids, err := model.RetrieveUIDs(request.Identifier)
			if err != nil {
				return nil, err
			}
			var ops []CMoveSubOperation
			for _, uid := range uids {
				if data := instances[uid]; data != nil {
					class, _ := data.GetString(npTag(8, 0x16))
					ops = append(ops, CMoveSubOperation{AffectedSOPClassUID: class, AffectedSOPInstanceUID: uid, Store: func(ctx context.Context) CMoveSubOperationResult {
						storeAssoc, err := ul.DialContext(ctx, moveAddress, ul.DialOptions{CalledAETitle: "DEST", CallingAETitle: "NONPATIENT", Contexts: []ul.PresentationContext{{AbstractSyntaxUID: class, TransferSyntaxUIDs: []string{ul.ExplicitVRLittleEndian}}}})
						if err != nil {
							return CMoveSubOperationResult{Err: err}
						}
						defer storeAssoc.Close()
						rsp, err := NewStoreClient(storeAssoc).Store(ctx, data)
						if err != nil {
							return CMoveSubOperationResult{Err: err}
						}
						if err := storeAssoc.Release(ctx); err != nil {
							return CMoveSubOperationResult{Err: err}
						}
						return CMoveSubOperationResult{Status: rsp.Status}
					}})
				}
			}
			return ops, nil
		})})
	}()
	var output strings.Builder
	for scanner.Scan() {
		fmt.Fprintln(&output, scanner.Text())
	}
	if err := cmd.Wait(); err != nil {
		t.Fatalf("peer: %v\n%s\n%s", err, output.String(), stderr.String())
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "PASS models=2 find=4 get=2 move=2 identity=full") {
		t.Fatalf("missing peer evidence: %s", output.String())
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

const nonPatientPythonPeer = `
import sys
from pydicom.dataset import Dataset
from pynetdicom import AE, evt, build_role
from pydicom.uid import ExplicitVRLittleEndian

models = [(38, 1, 'HangingProtocolName'), (39, 2, 'ContentLabel')]
received = {'get': [], 'move': []}
def check(ds, number, model):
    expected = Dataset()
    expected.SOPClassUID = f'1.2.840.10008.5.1.4.{number}.1'
    expected.SOPInstanceUID = f'1.2.826.0.1.3680043.10.543.918.{model}.1'
    if number == 38:
        expected.HangingProtocolName = 'SYNTHETIC'
        expected.HangingProtocolLevel = 'SITE'
        expected.NumberOfPriorsReferenced = 2
        item = Dataset(); item.Modality = 'CT'; item.Laterality = 'L'
        expected.HangingProtocolDefinitionSequence = [item]
    else:
        expected.ContentLabel = 'SYNTHETIC'
    assert ds == expected, (ds, expected)
def store(kind):
    def callback(event):
        ds = event.dataset
        number = 38 if str(ds.SOPClassUID).endswith('.38.1') else 39
        model = 1 if number == 38 else 2
        assert str(event.request.AffectedSOPClassUID) == str(ds.SOPClassUID)
        assert str(event.request.AffectedSOPInstanceUID) == str(ds.SOPInstanceUID)
        check(ds, number, model)
        received[kind].append(str(ds.SOPInstanceUID))
        return 0
    return callback
destination = AE(ae_title='DEST')
for number, _, _ in models:
    destination.add_supported_context(f'1.2.840.10008.5.1.4.{number}.1', ExplicitVRLittleEndian)
server = destination.start_server(('127.0.0.1', 0), block=False, evt_handlers=[(evt.EVT_C_STORE, store('move'))])
print(f'READY 127.0.0.1:{server.server_address[1]}', flush=True)
ae = AE(ae_title='INDEPENDENT'); roles = []
ae.acse_timeout = ae.dimse_timeout = ae.network_timeout = 8
for number, _, _ in models:
    for suffix in [1, 2, 3, 4]:
        ae.add_requested_context(f'1.2.840.10008.5.1.4.{number}.{suffix}', ExplicitVRLittleEndian)
    roles.append(build_role(f'1.2.840.10008.5.1.4.{number}.1', scu_role=False, scp_role=True))
assoc = ae.associate('127.0.0.1', int(sys.argv[1]), ae_title='NONPATIENT', ext_neg=roles, evt_handlers=[(evt.EVT_C_STORE, store('get'))])
assert assoc.is_established
for number, model, name in models:
    root = f'1.2.840.10008.5.1.4.{number}'
    uid = f'1.2.826.0.1.3680043.10.543.918.{model}.1'
    for pattern, count in [('SYN*', 1), ('NO', 0)]:
        query = Dataset(); setattr(query, name, pattern)
        responses = list(assoc.send_c_find(query, root + '.2'))
        assert responses[-1][0].Status == 0
        matches = [ds for status, ds in responses if status.Status == 0xFF00]
        assert len(matches) == count
        for ds in matches:
            assert ds.SOPInstanceUID == uid and ds.SOPClassUID == root + '.1'
            assert 'QueryRetrieveLevel' not in ds and 'PatientID' not in ds
    invalid = Dataset(); invalid.PatientID = ''
    assert list(assoc.send_c_find(invalid, root + '.2'))[-1][0].Status == 0xA900
    identifier = Dataset(); identifier.SOPInstanceUID = uid
    result = list(assoc.send_c_get(identifier, root + '.4'))[-1][0]
    assert result.Status == 0 and result.NumberOfCompletedSuboperations == 1
    result = list(assoc.send_c_move(identifier, 'DEST', root + '.3'))[-1][0]
    assert result.Status == 0 and result.NumberOfCompletedSuboperations == 1
    assert list(assoc.send_c_move(identifier, 'UNKNOWN', root + '.3'))[-1][0].Status == 0xA801
    identifier.QueryRetrieveLevel = 'IMAGE'
    assert list(assoc.send_c_get(identifier, root + '.4'))[-1][0].Status == 0xA900
assert len(received['get']) == 2 and len(received['move']) == 2
assoc.release(); server.shutdown()
print('PASS models=2 find=4 get=2 move=2 identity=full', flush=True)
`
