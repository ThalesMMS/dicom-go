package dimse

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestNormalizedClientAllServices(t *testing.T) {
	const (
		classUID    = "1.2.840.10008.5.1.4.34.6.1"
		instanceUID = "2.25.123456789"
	)

	tests := []struct {
		name   string
		serve  func(*ul.Association, byte, transfer.Syntax) error
		invoke func(context.Context, *NormalizedClient) error
	}{
		{
			name: "N-EVENT-REPORT",
			serve: func(peer *ul.Association, pcID byte, syntax transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedEventReportRequest(command)
				if err != nil {
					return err
				}
				if _, err := ReceiveDataSet(peer, pcID, syntax); err != nil {
					return err
				}
				typeID := request.EventTypeID
				if err := SendCommandSet(peer, pcID, (NormalizedEventReportResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					CommandDataSetType: 0x0102, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
					EventTypeIDOrNil: &typeID,
				}).CommandSet()); err != nil {
					return err
				}
				return SendDataSet(peer, pcID, normalizedTestDataSet("event reply"), syntax)
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.EventReport(ctx, NormalizedEventReportRequest{
					AffectedSOPClassUID: classUID, CommandDataSetType: DataSetPresent,
					AffectedSOPInstanceUID: instanceUID, EventTypeID: 3,
				}, normalizedTestDataSet("event information"))
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
		{
			name: "N-GET",
			serve: func(peer *ul.Association, pcID byte, syntax transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedGetRequest(command)
				if err != nil {
					return err
				}
				if err := SendCommandSet(peer, pcID, (NormalizedGetResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					CommandDataSetType: DataSetPresent, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
				}).CommandSet()); err != nil {
					return err
				}
				return SendDataSet(peer, pcID, normalizedTestDataSet("attribute list"), syntax)
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.Get(ctx, NormalizedGetRequest{
					RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID,
					AttributeIdentifierList: []core.Tag{core.NewTag(0x0010, 0x0010)},
				})
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
		{
			name: "N-SET",
			serve: func(peer *ul.Association, pcID byte, syntax transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedSetRequest(command)
				if err != nil {
					return err
				}
				if _, err := ReceiveDataSet(peer, pcID, syntax); err != nil {
					return err
				}
				return SendCommandSet(peer, pcID, (NormalizedSetResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					CommandDataSetType: NoDataSet, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
				}).CommandSet())
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.Set(ctx, NormalizedSetRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID}, normalizedTestDataSet("modification"))
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
		{
			name: "N-ACTION",
			serve: func(peer *ul.Association, pcID byte, syntax transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedActionRequest(command)
				if err != nil {
					return err
				}
				if _, err := ReceiveDataSet(peer, pcID, syntax); err != nil {
					return err
				}
				typeID := request.ActionTypeID
				if err := SendCommandSet(peer, pcID, (NormalizedActionResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					CommandDataSetType: DataSetPresent, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
					ActionTypeIDOrNil: &typeID,
				}).CommandSet()); err != nil {
					return err
				}
				return SendDataSet(peer, pcID, normalizedTestDataSet("action reply"), syntax)
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.Action(ctx, NormalizedActionRequest{
					RequestedSOPClassUID: classUID, CommandDataSetType: DataSetPresent,
					RequestedSOPInstanceUID: instanceUID, ActionTypeID: 9,
				}, normalizedTestDataSet("action information"))
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
		{
			name: "N-CREATE",
			serve: func(peer *ul.Association, pcID byte, _ transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedCreateRequest(command)
				if err != nil {
					return err
				}
				return SendCommandSet(peer, pcID, (NormalizedCreateResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					CommandDataSetType: NoDataSet, Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
				}).CommandSet())
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.Create(ctx, NormalizedCreateRequest{AffectedSOPClassUID: classUID, CommandDataSetType: NoDataSet}, nil)
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
		{
			name: "N-DELETE",
			serve: func(peer *ul.Association, pcID byte, _ transfer.Syntax) error {
				command, err := ReceiveCommandSet(peer, pcID)
				if err != nil {
					return err
				}
				request, err := ParseNormalizedDeleteRequest(command)
				if err != nil {
					return err
				}
				return SendCommandSet(peer, pcID, (NormalizedDeleteResponse{
					AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
					Status: StatusSuccess, AffectedSOPInstanceUID: instanceUID,
				}).CommandSet())
			},
			invoke: func(ctx context.Context, client *NormalizedClient) error {
				result, err := client.Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID})
				if err != nil {
					return err
				}
				return requireNormalizedResult(result.Response != nil, result.DataSet, result.PresentationContext)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contexts := []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}}
			peer, local := testPipeAssociations(t, contexts)
			peer.PeerMaxPDU = 64
			local.PeerMaxPDU = 64
			serverDone := make(chan error, 1)
			go func() { serverDone <- test.serve(peer, 1, transfer.ImplicitVRLittleEndian) }()

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := test.invoke(ctx, NewNormalizedClient(local)); err != nil {
				t.Fatalf("client operation error = %v", err)
			}
			if err := <-serverDone; err != nil {
				t.Fatalf("server error = %v", err)
			}
		})
	}
}

func TestNormalizedClientReturnsTypedWarningWithStatusFields(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	serverDone := make(chan error, 1)
	go func() {
		command, err := ReceiveCommandSet(peer, 1)
		if err != nil {
			serverDone <- err
			return
		}
		request, err := ParseNormalizedDeleteRequest(command)
		if err != nil {
			serverDone <- err
			return
		}
		errorID := uint16(9)
		serverDone <- SendCommandSet(peer, 1, (NormalizedDeleteResponse{
			MessageIDBeingRespondedTo: request.MessageID,
			Status:                    0xB000,
			StatusFields: &NormalizedStatusFields{
				ErrorComment: "warning detail",
				ErrorIDOrNil: &errorID,
			},
		}).CommandSet())
	}()

	result, err := NewNormalizedClient(local).Delete(context.Background(), NormalizedDeleteRequest{
		RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: instanceUID,
	})
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) || statusErr.Class != NormalizedStatusWarning || statusErr.Fields.ErrorComment != "warning detail" {
		t.Fatalf("Delete() error = %#v, result = %#v", err, result)
	}
	if result.Response == nil || result.Response.Status != 0xB000 {
		t.Fatalf("Delete() response = %#v", result.Response)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestNormalizedClientPreservesFailureDetailDataSet(t *testing.T) {
	const classUID = "1.2.3"
	const instanceUID = "1.2.3.4"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	serverDone := make(chan error, 1)
	go func() {
		command, err := ReceiveCommandSet(peer, 1)
		if err != nil {
			serverDone <- err
			return
		}
		request, err := ParseNormalizedActionRequest(command)
		if err != nil {
			serverDone <- err
			return
		}
		typeID := request.ActionTypeID
		if err := SendCommandSet(peer, 1, (NormalizedActionResponse{
			AffectedSOPClassUID: classUID, MessageIDBeingRespondedTo: request.MessageID,
			CommandDataSetType: DataSetPresent, Status: 0x0115, AffectedSOPInstanceUID: instanceUID,
			ActionTypeIDOrNil: &typeID,
		}).CommandSet()); err != nil {
			serverDone <- err
			return
		}
		serverDone <- SendDataSet(peer, 1, normalizedTestDataSet("invalid argument detail"), transfer.ImplicitVRLittleEndian)
	}()

	result, err := NewNormalizedClient(local).Action(context.Background(), NormalizedActionRequest{
		RequestedSOPClassUID: classUID, CommandDataSetType: NoDataSet,
		RequestedSOPInstanceUID: instanceUID, ActionTypeID: 4,
	}, nil)
	var statusErr *NormalizedStatusError
	if !errors.As(err, &statusErr) || statusErr.Status != 0x0115 || statusErr.Class != NormalizedStatusFailure {
		t.Fatalf("Action() error = %#v, want typed 0x0115 failure", err)
	}
	if result.Response == nil || result.DataSet == nil {
		t.Fatalf("Action() failed to preserve response/detail dataset: %#v", result)
	}
	if err := <-serverDone; err != nil {
		t.Fatalf("server error = %v", err)
	}
}

func TestNormalizedClientPresentationContextPolicy(t *testing.T) {
	const metaUID = "1.2.840.meta"
	const commandUID = "1.2.840.component"
	_, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 3, AbstractSyntaxUID: metaUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	client := NewNormalizedClient(local)

	_, err := client.DeleteWithOptions(NormalizedOperationOptions{PresentationContextID: 3}, NormalizedDeleteRequest{
		RequestedSOPClassUID: commandUID, RequestedSOPInstanceUID: "1.2.3",
	})
	if !errors.Is(err, ErrNormalizedPresentationContext) {
		t.Fatalf("DeleteWithOptions() error = %v, want ErrNormalizedPresentationContext", err)
	}

	pc, syntax, err := normalizedClientPresentationContext(local, commandUID, NormalizedOperationOptions{
		PresentationContextAbstractSyntaxUID: metaUID,
	})
	if err != nil {
		t.Fatalf("normalizedClientPresentationContext(meta) error = %v", err)
	}
	if pc.ID != 3 || syntax.UID != transfer.ImplicitVRLittleEndian.UID {
		t.Fatalf("meta context = %#v syntax=%#v", pc, syntax)
	}
}

func TestNormalizedClientHonorsCanceledContextBeforeSend(t *testing.T) {
	const classUID = "1.2.3"
	_, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := NewNormalizedClient(local).Delete(ctx, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: "1.2.3.4"})
	if !errors.Is(err, ErrOperationCanceled) {
		t.Fatalf("Delete() error = %v, want ErrOperationCanceled", err)
	}
}

func TestNormalizedClientTimeoutAbortsAndReleasesOperationSlot(t *testing.T) {
	const classUID = "1.2.3"
	peer, local := testPipeAssociations(t, []ul.AcceptedContext{{ID: 1, AbstractSyntaxUID: classUID, TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID}})
	peerDone := make(chan error, 1)
	go func() {
		if _, err := ReceiveCommandSet(peer, 1); err != nil {
			peerDone <- err
			return
		}
		pdu, err := peer.ReadPDU()
		if err != nil {
			var abortErr *ul.AbortError
			if errors.As(err, &abortErr) {
				peerDone <- nil
				return
			}
			peerDone <- err
			return
		}
		if _, ok := pdu.(*ul.AbortRQ); !ok {
			peerDone <- fmt.Errorf("cleanup PDU = %T, want *ul.AbortRQ", pdu)
			return
		}
		peerDone <- nil
	}()

	_, err := NewNormalizedClient(local).DeleteWithOptions(NormalizedOperationOptions{
		OperationOptions: OperationOptions{ResponseTimeout: 20 * time.Millisecond},
	}, NormalizedDeleteRequest{RequestedSOPClassUID: classUID, RequestedSOPInstanceUID: "1.2.3.4"})
	if !errors.Is(err, ErrOperationTimeout) || !errors.Is(err, ErrAssociationStateUncertain) {
		t.Fatalf("DeleteWithOptions() error = %v, want timeout and uncertain state", err)
	}
	if err := <-peerDone; err != nil {
		t.Fatalf("peer cleanup error = %v", err)
	}
	release, guardErr := beginAssociationOperation(local)
	if guardErr != nil {
		t.Fatalf("operation slot remained held: %v", guardErr)
	}
	release()
}

func normalizedTestDataSet(value string) *object.Object {
	return object.FromElements([]core.Element{{
		Header: core.ElementHeader{Tag: core.NewTag(0x0010, 0x0010), VR: core.VRPN},
		Value:  core.StringValue{value},
	}}, std.Dictionary)
}

func requireNormalizedResult(hasResponse bool, dataSet *object.Object, pc ul.AcceptedContext) error {
	if !hasResponse {
		return fmt.Errorf("missing response")
	}
	if pc.ID != 1 {
		return fmt.Errorf("presentation context ID = %d, want 1", pc.ID)
	}
	_ = dataSet
	return nil
}
