package dimse

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata/codecfixture"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Locked and bounded because failure paths may inspect stderr while the child
// is still writing. Only controlled synthetic-peer diagnostics reach this log.
type integrityPeerLog struct {
	sync.Mutex
	data bytes.Buffer
}

func (b *integrityPeerLog) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	n := len(p)
	if len(p) > 8192-b.data.Len() {
		p = p[:8192-b.data.Len()]
	}
	_, _ = b.data.Write(p)
	return n, nil
}
func (b *integrityPeerLog) String() string { b.Lock(); defer b.Unlock(); return b.data.String() }

func TestStoreCGetAndCMoveFullContentAgainstPynetdicom(t *testing.T) {
	python := requirePynetdicomInterop(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	dir := t.TempDir()
	fixture := codecfixture.NativeLarge()
	const storageUID = "1.2.840.10008.5.1.4.1.1.2"
	const moveUID = "1.2.840.10008.5.1.4.1.2.2.2"
	moveListener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer moveListener.Close()
	moveHost, movePort, err := net.SplitHostPort(moveListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	movedPath := filepath.Join(dir, "moved.dcm")
	movedDone := make(chan error, 1)
	movedFinished := make(chan struct{})
	go func() {
		defer close(movedFinished)
		association, err := moveListener.AcceptAssociation(ul.AcceptOptions{AETitle: "DICOMGO_MOVE", Context: ctx, MaxPDU: 4096, SupportedAbstractSyntaxes: []string{storageUID}, SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID}})
		if err != nil {
			movedDone <- err
			return
		}
		defer association.Close()
		var count atomic.Int32
		session, err := NewAsyncSession(association, AsyncSessionOptions{Handlers: map[uint16]AsyncRequestHandler{CStoreRQ: func(handlerCtx context.Context, s *AsyncSession, m AsyncMessage) error {
			request, err := ParseCStoreRequest(m.Command)
			if err != nil {
				return err
			}
			status, err := persistIntegrityDataSet(movedPath, m.DataSet, transfer.ExplicitVRLittleEndian, fixture.Object())
			if err != nil {
				return err
			}
			count.Add(1)
			return s.Respond(handlerCtx, m, (CStoreResponse{AffectedSOPClassUID: request.AffectedSOPClassUID, AffectedSOPInstanceUID: request.AffectedSOPInstanceUID, Status: status}).CommandSet(), nil)
		}}})
		if err != nil {
			movedDone <- err
			return
		}
		defer session.Close()
		select {
		case <-session.Done():
		case <-ctx.Done():
			movedDone <- ctx.Err()
			return
		}
		if err := session.Err(); err != nil && !errors.Is(err, ErrAssociationReleased) {
			movedDone <- err
			return
		}
		if count.Load() != 1 {
			movedDone <- fmt.Errorf("C-MOVE persisted %d stores, want 1", count.Load())
			return
		}
		movedDone <- nil
	}()
	t.Cleanup(func() {
		cancel()
		_ = moveListener.Close()
		select {
		case <-movedFinished:
		case <-time.After(time.Second):
			t.Error("C-MOVE callback did not stop")
		}
	})
	input, err := fixture.Part10Bytes()
	if err != nil {
		t.Fatal(err)
	}
	inputPath, storedPath, retrievedPath := filepath.Join(dir, "input.dcm"), filepath.Join(dir, "stored.dcm"), filepath.Join(dir, "retrieved.dcm")
	if err := os.WriteFile(inputPath, input, 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.CommandContext(ctx, python, "-u", "-c", pynetdicomIntegrityPeer, inputPath, storedPath, moveHost, movePort)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr integrityPeerLog
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var stopOnce sync.Once
	var stopErr error
	stop := func() error {
		stopOnce.Do(func() {
			_ = stdin.Close()
			select {
			case stopErr = <-done:
			case <-time.After(3 * time.Second):
				_ = cmd.Process.Kill()
				stopErr = <-done
			}
		})
		return stopErr
	}
	t.Cleanup(func() { _ = stop() })
	portLine := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			portLine <- scanner.Text()
		}
		close(portLine)
	}()
	var port string
	select {
	case line := <-portLine:
		if !strings.HasPrefix(line, "PORT ") {
			t.Fatalf("peer startup: %s", stderr.String())
		}
		port = strings.TrimPrefix(line, "PORT ")
	case <-ctx.Done():
		t.Fatal("peer startup deadline", stderr.String())
	}
	assoc, err := ul.DialContext(ctx, "127.0.0.1:"+port, ul.DialOptions{CalledAETitle: "PY_INTEGRITY", CallingAETitle: "DICOMGO", MaxPDU: 4096, Contexts: []ul.PresentationContext{
		{AbstractSyntaxUID: storageUID, TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}},
		{AbstractSyntaxUID: StudyRootGetSOPClassUID, TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}},
		{AbstractSyntaxUID: moveUID, TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}},
	}, RoleSelections: []ul.RoleSelectionItem{{SopClassUID: storageUID, SCURole: true, SCPRole: true}}})
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	defer assoc.Close()
	if response, err := NewStoreClient(assoc).Store(ctx, fixture.Object()); err != nil || response == nil || response.Status != StatusSuccess {
		t.Fatalf("independent store: %v", err)
	}
	// The Python handler checks every element and every byte, saves and rereads
	// its file before returning success. A status alone is insufficient here.
	assertIntegrityFile(t, storedPath, fixture.Object())
	uid, _ := fixture.Object().GetString(core.NewTag(0x0008, 0x0018))
	query := normalizedInteropDataSet(stringElement(core.NewTag(0x0008, 0x0052), core.VRCS, "IMAGE"), stringElement(core.NewTag(0x0008, 0x0018), core.VRUI, uid))
	count := 0
	store := CGetStoreHandlerFunc(func(storeCtx context.Context, req CGetStoreRequestContext) (uint16, error) {
		count++
		return persistIntegrityDataSet(retrievedPath, req.DataSet, req.DataSetSyntax, fixture.Object())
	})
	var getContext byte
	for _, pc := range assoc.AcceptedContexts {
		if pc.AbstractSyntaxUID == StudyRootGetSOPClassUID {
			getContext = pc.ID
		}
	}
	if getContext == 0 {
		t.Fatal("C-GET context missing")
	}
	response, err := SendCGet(ctx, assoc, getContext, CGetRequest{AffectedSOPClassUID: StudyRootGetSOPClassUID, MessageID: 42}, query, transfer.ExplicitVRLittleEndian, store)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	if response == nil || response.Status != StatusSuccess || count != 1 || response.NumberOfCompletedSuboperationsOrNil == nil || *response.NumberOfCompletedSuboperationsOrNil != 1 {
		t.Fatal("C-GET succeeded without exactly one completed persisted suboperation")
	}
	assertIntegrityFile(t, retrievedPath, fixture.Object())
	var moveContext byte
	for _, pc := range assoc.AcceptedContexts {
		if pc.AbstractSyntaxUID == moveUID {
			moveContext = pc.ID
		}
	}
	if moveContext == 0 {
		t.Fatal("C-MOVE context missing")
	}
	moveResponse, err := SendCMove(ctx, assoc, moveContext, CMoveRequest{AffectedSOPClassUID: moveUID, MessageID: 43, MoveDestination: "DICOMGO_MOVE"}, query, transfer.ExplicitVRLittleEndian)
	if err != nil {
		t.Fatal(err, stderr.String())
	}
	if moveResponse == nil || moveResponse.Status != StatusSuccess || moveResponse.NumberOfCompletedSuboperationsOrNil == nil || *moveResponse.NumberOfCompletedSuboperationsOrNil != 1 {
		t.Fatal("C-MOVE did not complete one suboperation")
	}
	select {
	case err := <-movedDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("C-MOVE callback deadline")
	}
	assertIntegrityFile(t, movedPath, fixture.Object())
	if err := assoc.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal("independent content verification", err, stderr.String())
	}
}

func persistIntegrityDataSet(path string, ds *object.Object, syntax transfer.Syntax, want *object.Object) (uint16, error) {
	if ds == nil {
		return 0xA700, fmt.Errorf("store missing dataset")
	}
	if err := compareIntegrityDataSet(ds, want); err != nil {
		return 0xA700, err
	}
	file, err := os.Create(path)
	if err != nil {
		return 0xA700, err
	}
	err = object.WriteFile(file, &object.File{Dataset: ds, TransferSyntax: syntax})
	closeErr := file.Close()
	if err != nil {
		return 0xA700, err
	}
	if closeErr != nil {
		return 0xA700, closeErr
	}
	return StatusSuccess, nil
}

func TestDIMSEIntegrityRejectsLatePixelCorruptionAndUnpersistedStore(t *testing.T) {
	fixture := codecfixture.NativeLarge()
	got := fixture.Object()
	pixel, ok := got.Get(core.TagPixelData)
	if !ok {
		t.Fatal("fixture has no pixels")
	}
	raw := append(core.RawValue(nil), pixel.Value.(core.RawValue)...)
	raw[len(raw)-3] ^= 0xff
	pixel.Value = raw
	got.Put(pixel)
	if err := compareIntegrityDataSet(got, fixture.Object()); err == nil {
		t.Fatal("late pixel corruption accepted")
	}
	// A directory cannot serve as the required received Part10 file.
	if status, err := persistIntegrityDataSet(t.TempDir(), fixture.Object(), transfer.ExplicitVRLittleEndian, fixture.Object()); err == nil || status == StatusSuccess {
		t.Fatal("unpersisted store acknowledged as success")
	}
}

func compareIntegrityDataSet(got, want *object.Object) error {
	a, err := dicomtest.SemanticDataSet(got.ToDataSet(), binary.LittleEndian)
	if err != nil {
		return err
	}
	b, err := dicomtest.SemanticDataSet(want.ToDataSet(), binary.LittleEndian)
	if err != nil {
		return err
	}
	if diff := dicomtest.DiffSemantic(a, b); diff != "" {
		return fmt.Errorf("dataset integrity: %s", diff)
	}
	return nil
}

func assertIntegrityFile(t *testing.T, path string, want *object.Object) {
	t.Helper()
	file, err := object.OpenFile(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := compareIntegrityDataSet(file.Dataset, want); err != nil {
		t.Fatal(err)
	}
}

const pynetdicomIntegrityPeer = `
import sys
from pathlib import Path
import pydicom
from pynetdicom import AE, evt
from pynetdicom.sop_class import CTImageStorage, StudyRootQueryRetrieveInformationModelGet, StudyRootQueryRetrieveInformationModelMove
from pydicom.uid import ExplicitVRLittleEndian

source = pydicom.dcmread(sys.argv[1])
assert source.Rows == 256 and source.Columns == 256
assert source.PixelData == bytes(range(256)) * 256
assert str(source.PatientName) == 'SYNTHETIC^CODECFIXTURE'
counts = {'store': 0, 'get': 0, 'move': 0}
failures = []
def on_store(event):
    try:
        ds = event.dataset
        assert ds == source, 'received dataset differs from independently parsed source'
        assert event.request.AffectedSOPInstanceUID == source.SOPInstanceUID
        ds.file_meta = event.file_meta
        ds.save_as(sys.argv[2], enforce_file_format=True)
        assert pydicom.dcmread(sys.argv[2]) == source
        counts['store'] += 1
        return 0x0000
    except Exception as exc:
        failures.append(type(exc).__name__)
        return 0xA700
def on_get(event):
    assert event.identifier.QueryRetrieveLevel == 'IMAGE'
    assert event.identifier.SOPInstanceUID == source.SOPInstanceUID
    counts['get'] += 1
    yield 1
    yield 0xFF00, source
def on_move(event):
    assert event.move_destination == 'DICOMGO_MOVE'
    assert event.identifier.SOPInstanceUID == source.SOPInstanceUID
    counts['move'] += 1
    yield sys.argv[3], int(sys.argv[4])
    yield 1
    yield 0xFF00, source
ae = AE(ae_title='PY_INTEGRITY')
ae.maximum_pdu_size = 4096
ae.add_supported_context(CTImageStorage, ExplicitVRLittleEndian, scu_role=True, scp_role=True)
ae.add_supported_context(StudyRootQueryRetrieveInformationModelGet, ExplicitVRLittleEndian)
ae.add_supported_context(StudyRootQueryRetrieveInformationModelMove, ExplicitVRLittleEndian)
ae.add_requested_context(CTImageStorage, ExplicitVRLittleEndian)
server = ae.start_server(('127.0.0.1', 0), block=False, evt_handlers=[(evt.EVT_C_STORE,on_store),(evt.EVT_C_GET,on_get),(evt.EVT_C_MOVE,on_move)])
print('PORT',server.server_address[1],flush=True)
try:
    sys.stdin.read()
finally:
    server.shutdown()
assert counts == {'store': 1, 'get': 1, 'move': 1} and not failures
assert Path(sys.argv[2]).is_file()
`
