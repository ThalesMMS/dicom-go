package dimse

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestCStoreRequestCommandSet(t *testing.T) {
	req := CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              9,
		Priority:               1,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}
	reqObj, err := DecodeCommandSet(mustEncodeCommandSet(t, req.CommandSet()))
	if err != nil {
		t.Fatalf("DecodeCommandSet(request) error = %v", err)
	}
	parsed, err := ParseCStoreRequest(reqObj)
	if err != nil {
		t.Fatalf("ParseCStoreRequest() error = %v", err)
	}
	if *parsed != req {
		t.Fatalf("ParseCStoreRequest() = %#v, want %#v", parsed, req)
	}
}

func TestCStoreResponseCommandSet(t *testing.T) {
	resp := CStoreResponse{
		AffectedSOPClassUID:       dicomtest.TestSOPClassUID,
		MessageIDBeingRespondedTo: 9,
		AffectedSOPInstanceUID:    dicomtest.TestSOPInstanceUID,
		Status:                    StatusSuccess,
	}
	respObj, err := DecodeCommandSet(mustEncodeCommandSet(t, resp.CommandSet()))
	if err != nil {
		t.Fatalf("DecodeCommandSet(response) error = %v", err)
	}
	parsed, err := ParseCStoreResponse(respObj)
	if err != nil {
		t.Fatalf("ParseCStoreResponse() error = %v", err)
	}
	if *parsed != resp {
		t.Fatalf("ParseCStoreResponse() = %#v, want %#v", parsed, resp)
	}
}

func TestCStoreSCUConversationsWithLocalSCP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	data, err := dicomtest.ExplicitVRFile()
	if err != nil {
		t.Fatalf("ExplicitVRFile() error = %v", err)
	}
	file, err := object.ReadFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}

	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	defer closeOrFail(t, "listener", listener)

	serverDone := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                 "STORESCP",
			Context:                 ctx,
			AcceptAnyAbstractSyntax: true,
			SupportedTransferSyntaxes: []string{
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		if err != nil {
			serverDone <- err
			return
		}
		defer closeOrFail(t, "server association", assoc)

		pc, err := AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
		if err != nil {
			serverDone <- err
			return
		}
		req, err := ReceiveCStoreRequest(assoc, pc.ID)
		if err != nil {
			serverDone <- err
			return
		}
		if req.MessageID != 42 {
			serverDone <- errors.New("server received wrong C-STORE message ID")
			return
		}
		dataset, err := ReceiveDataSet(assoc, pc.ID, transfer.ExplicitVRLittleEndian)
		if err != nil {
			serverDone <- err
			return
		}
		if got, ok := dataset.GetUID(core.NewTag(0x0008, 0x0018)); !ok || got != dicomtest.TestSOPInstanceUID {
			serverDone <- errors.New("server received wrong SOP Instance UID")
			return
		}
		if err := SendCStoreResponse(assoc, pc.ID, CStoreResponse{
			AffectedSOPClassUID:       req.AffectedSOPClassUID,
			MessageIDBeingRespondedTo: req.MessageID,
			AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
			Status:                    StatusSuccess,
		}); err != nil {
			serverDone <- err
			return
		}
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
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dicomtest.TestSOPClassUID,
			TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialContext() error = %v", err)
	}

	pc, err := AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatalf("AcceptedContextForSOPClass() error = %v", err)
	}
	if err := SendCStoreRequest(assoc, pc.ID, CStoreRequest{
		AffectedSOPClassUID:    dicomtest.TestSOPClassUID,
		MessageID:              42,
		Priority:               0,
		AffectedSOPInstanceUID: dicomtest.TestSOPInstanceUID,
	}); err != nil {
		t.Fatalf("SendCStoreRequest() error = %v", err)
	}
	if err := SendDataSet(assoc, pc.ID, file.Dataset, transfer.ExplicitVRLittleEndian); err != nil {
		t.Fatalf("SendDataSet() error = %v", err)
	}
	response, err := ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		t.Fatalf("ReceiveCStoreResponse() error = %v", err)
	}
	if response.Status != StatusSuccess || response.MessageIDBeingRespondedTo != 42 {
		t.Fatalf("response = %#v", response)
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}
