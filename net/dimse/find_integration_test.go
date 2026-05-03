package dimse

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCFindSCUConversationsWithLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
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

		// Receive request command + identifier.
		rqCmdObj, err := ReceiveCommandSet(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		rq, err := ParseCFindRequest(rqCmdObj)
		if err != nil {
			serverDone <- err
			return
		}
		if rq.AffectedSOPClassUID != StudyRootFindSOPClassUID {
			serverDone <- errors.New("unexpected AffectedSOPClassUID")
			return
		}

		rqIdentifier, err := receiveIdentifierObject(assoc, pc.ID, pc.TransferSyntaxUID)
		if err != nil {
			serverDone <- err
			return
		}
		level, ok := rqIdentifier.Get(core.NewTag(0x0008, 0x0052))
		val := level.StringValue()
		if !ok || val != "STUDY" {
			serverDone <- errors.New("request identifier missing QueryRetrieveLevel=STUDY")
			return
		}

		// Send one pending match.
		pendingCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: rq.MessageID,
			Status:                    StatusPending,
			CommandDataSetType:        DataSetPresent,
		}
		if err := SendCommandSet(assoc, pc.ID, pendingCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}
		matchElems := []core.Element{
			{Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0020), VR: core.VRLO}, Value: core.StringValue{"P1"}},
			{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{"1.2.3"}},
		}
		match := object.FromElements(matchElems, std.Dictionary)
		if err := sendIdentifierObject(assoc, pc.ID, match, pc.TransferSyntaxUID); err != nil {
			serverDone <- err
			return
		}

		// Send final success.
		finalCmd := CFindResponse{
			AffectedSOPClassUID:       StudyRootFindSOPClassUID,
			MessageIDBeingRespondedTo: rq.MessageID,
			Status:                    StatusSuccess,
			CommandDataSetType:        NoDataSet,
		}
		if err := SendCommandSet(assoc, pc.ID, finalCmd.CommandSet()); err != nil {
			serverDone <- err
			return
		}

		// Expect release.
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

	pc, err := AcceptedContextForSOPClass(assoc, StudyRootFindSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}

	identifierElems, err := BuildStudyRootStudyFindKeys(nil, "PatientID", "StudyInstanceUID")
	if err != nil {
		t.Fatalf("BuildStudyRootStudyFindKeys() error = %v", err)
	}
	identifierObj := object.FromElements(identifierElems, std.Dictionary)

	results, errs := Find(ctx, assoc, pc.ID, CFindRequest{
		AffectedSOPClassUID: StudyRootFindSOPClassUID,
		MessageID:           1,
		Priority:            PriorityMedium,
	}, identifierObj, transfer.ImplicitVRLittleEndian)

	var gotMatches int
	var finalStatus uint16
	for results != nil || errs != nil {
		select {
		case r, ok := <-results:
			if !ok {
				results = nil
				continue
			}
			finalStatus = r.Response.Status
			if r.Response.Status == StatusPending {
				if r.Identifier == nil {
					t.Fatalf("pending response missing identifier")
				}
				pid, ok := r.Identifier.Get(core.NewTag(0x0010, 0x0020))
				val := pid.StringValue()
				if !ok || val != "P1" {
					t.Fatalf("unexpected pending identifier PatientID: %q", val)
				}
				gotMatches++
			}
		case err, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if err != nil {
				t.Fatalf("Find() error = %v", err)
			}
		}
	}

	if gotMatches != 1 {
		t.Fatalf("matches = %d, want 1", gotMatches)
	}
	if finalStatus != StatusSuccess {
		t.Fatalf("final status = 0x%04X, want 0x%04X", finalStatus, StatusSuccess)
	}

	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func receiveIdentifierObject(assoc *ul.Association, pcID byte, syntaxUID string) (*object.Object, error) {
	syntax, ok := transfer.DefaultRegistry.Get(syntaxUID)
	if !ok {
		return nil, transfer.ErrUnknownTransferSyntax
	}
	obj, err := ReceiveDataSet(assoc, pcID, syntax)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

func sendIdentifierObject(assoc *ul.Association, pcID byte, obj *object.Object, syntaxUID string) error {
	syntax, ok := transfer.DefaultRegistry.Get(syntaxUID)
	if !ok {
		return transfer.ErrUnknownTransferSyntax
	}
	return SendDataSet(assoc, pcID, obj, syntax)
}
