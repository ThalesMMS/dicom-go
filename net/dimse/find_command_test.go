package dimse

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCFindRequestCommandSet_RoundTrip(t *testing.T) {
	req := CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           7,
		Priority:            PriorityHigh,
	}

	cs := req.CommandSet()
	obj := object.FromElements(cs, nil)
	parsed, err := ParseCFindRequest(obj)
	if err != nil {
		t.Fatalf("ParseCFindRequest: %v", err)
	}
	if parsed.AffectedSOPClassUID != req.AffectedSOPClassUID {
		t.Fatalf("AffectedSOPClassUID=%q want %q", parsed.AffectedSOPClassUID, req.AffectedSOPClassUID)
	}
	if parsed.MessageID != req.MessageID {
		t.Fatalf("MessageID=%d want %d", parsed.MessageID, req.MessageID)
	}
	if parsed.Priority != req.Priority {
		t.Fatalf("Priority=0x%04X want 0x%04X", parsed.Priority, req.Priority)
	}
}

func TestCFindRequestCommandSet_DefaultPriority(t *testing.T) {
	req := CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           7,
		Priority:            0,
	}
	obj := object.FromElements(req.CommandSet(), nil)
	parsed, err := ParseCFindRequest(obj)
	if err != nil {
		t.Fatalf("ParseCFindRequest: %v", err)
	}
	if parsed.Priority != PriorityMedium {
		t.Fatalf("Priority=0x%04X want default 0x%04X", parsed.Priority, PriorityMedium)
	}
}

func TestParseCFindRequest_DataSetTypeMustBePresent(t *testing.T) {
	req := CFindRequest{AffectedSOPClassUID: StudyRootFindSOPClassUID, MessageID: 1}
	cs := req.CommandSet()
	// overwrite dataset type to NoDataSet
	for i := range cs {
		if cs[i].Header.Tag == CommandDataSetType {
			cs[i].Value = core.RawValue{0x01, 0x01}
		}
	}
	obj := object.FromElements(cs, nil)
	if _, err := ParseCFindRequest(obj); err == nil {
		t.Fatalf("expected error")
	}
}

func TestCFindResponseCommandSet_RoundTrip_WithAndWithoutDatasetAndErrorComment(t *testing.T) {
	cases := []struct {
		name string
		rsp  CFindResponse
	}{
		{
			name: "pending_with_dataset",
			rsp: CFindResponse{
				AffectedSOPClassUID:       StudyRootFindSOPClassUID,
				MessageIDBeingRespondedTo: 9,
				Status:                    StatusPending,
				CommandDataSetType:        DataSetPresent,
			},
		},
		{
			name: "success_no_dataset_with_comment",
			rsp: CFindResponse{
				AffectedSOPClassUID:       StudyRootFindSOPClassUID,
				MessageIDBeingRespondedTo: 9,
				Status:                    StatusSuccess,
				CommandDataSetType:        NoDataSet,
				ErrorComment:              "some comment",
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := object.FromElements(tc.rsp.CommandSet(), nil)
			parsed, err := ParseCFindResponse(obj)
			if err != nil {
				t.Fatalf("ParseCFindResponse: %v", err)
			}
			if parsed.AffectedSOPClassUID != tc.rsp.AffectedSOPClassUID {
				t.Fatalf("AffectedSOPClassUID=%q want %q", parsed.AffectedSOPClassUID, tc.rsp.AffectedSOPClassUID)
			}
			if parsed.MessageIDBeingRespondedTo != tc.rsp.MessageIDBeingRespondedTo {
				t.Fatalf("MessageIDBeingRespondedTo=%d want %d", parsed.MessageIDBeingRespondedTo, tc.rsp.MessageIDBeingRespondedTo)
			}
			if parsed.Status != tc.rsp.Status {
				t.Fatalf("Status=0x%04X want 0x%04X", parsed.Status, tc.rsp.Status)
			}
			if parsed.CommandDataSetType != tc.rsp.CommandDataSetType {
				t.Fatalf("CommandDataSetType=0x%04X want 0x%04X", parsed.CommandDataSetType, tc.rsp.CommandDataSetType)
			}
			if parsed.ErrorComment != tc.rsp.ErrorComment {
				t.Fatalf("ErrorComment=%q want %q", parsed.ErrorComment, tc.rsp.ErrorComment)
			}
		})
	}
}

func TestReceiveCFindResponse_DataSetPresenceRule(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   "FINDSCP",
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{StudyRootFindSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}

		// Receive request command and identifier (ignore content)
		cmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := ParseCFindRequest(cmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		// Consume identifier dataset bytes (present in request).
		if _, err := io.Copy(io.Discard, newTypedPDataReader(assoc, pc.ID, false)); err != nil {
			serverDone <- err
			return
		}

		// Send response indicating dataset not present...
		rspCmd := CFindResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			Status:                    StatusSuccess,
			CommandDataSetType:        NoDataSet,
		}.CommandSet()
		if err := SendCommandSet(assoc, pc.ID, rspCmd); err != nil {
			serverDone <- err
			return
		}
		// ...but still (incorrectly) send a dataset PDV. Client should not read it.
		w := NewPDataWriter(assoc, pc.ID, false, peerMaxPDUWithHeader(assoc))
		_, _ = w.Write([]byte{0x01, 0x02, 0x03})
		if err := w.Finish(); err != nil {
			serverDone <- err
			return
		}

		// Expect client to release.
		pdu, err := assoc.ReadPDU()
		if err != nil {
			serverDone <- err
			return
		}
		if _, ok := pdu.(*ul.ReleaseRQ); !ok {
			serverDone <- errors.New("server expected A-RELEASE-RQ")
			return
		}
		serverDone <- assoc.WritePDU(&ul.ReleaseRP{})
	}()

	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{
		CalledAETitle:  "FINDSCP",
		CallingAETitle: "FINDSCU",
		Contexts:       []ul.PresentationContext{StudyRootFindPresentationContext()},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}
	defer closeOrFail(t, "client association", assoc)

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass: %v", err)
	}

	req := CFindRequest{AffectedSOPClassUID: StudyRootFindSOPClassUID, MessageID: 1}
	if err := SendCFindRequest(assoc, pc.ID, req, object.New(nil).Elements()); err != nil {
		t.Fatalf("SendCFindRequest: %v", err)
	}
	// Receive response; should not attempt to read the incorrectly-sent dataset.
	rsp, identifier, err := ReceiveCFindResponse(assoc, pc.ID, transfer.ImplicitVRLittleEndian)
	if err != nil {
		t.Fatalf("SendCFind: %v", err)
	}
	if rsp.Status != StatusSuccess {
		t.Fatalf("Status=0x%04X want 0x0000", rsp.Status)
	}
	if identifier != nil {
		t.Fatalf("expected nil identifier when CommandDataSetType indicates not present")
	}
	// Server sent an (incorrect) dataset PDV while claiming NoDataSet; that PDV
	// will still be queued on the association. Drain it so the UL release
	// handshake can proceed.
	_, _ = io.Copy(io.Discard, newTypedPDataReader(assoc, pc.ID, false))
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
