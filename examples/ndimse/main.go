// Command ndimse demonstrates one generic N-GET exchange over an in-memory
// connection. Production applications establish the ul.Association over a
// network socket after negotiating the service's SOP Class presentation context.
package main

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	exampleSOPClassUID    = "1.2.826.0.1.3680043.10.543.1"
	exampleSOPInstanceUID = "2.25.123456789"
)

var patientName = core.NewTag(0x0010, 0x0010)

func main() {
	serverConn, clientConn := net.Pipe()
	contexts := []ul.AcceptedContext{{
		ID:                1,
		AbstractSyntaxUID: exampleSOPClassUID,
		TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
	}}
	server := &ul.Association{Conn: serverConn, AcceptedContexts: contexts, MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU}
	client := &ul.Association{Conn: clientConn, AcceptedContexts: contexts, MaxPDU: ul.DefaultMaxPDU, PeerMaxPDU: ul.DefaultMaxPDU}
	defer server.Close()
	defer client.Close()

	serverDone := make(chan error, 1)
	go func() {
		options := dimse.NormalizedSCPOptions{
			GetHandler: func(_ context.Context, request dimse.NormalizedGetRequest) (dimse.NormalizedGetSCPResult, error) {
				attributes := object.FromElements([]core.Element{{
					Header: core.ElementHeader{Tag: patientName, VR: core.VRPN},
					Value:  core.StringValue{"SYNTHETIC^PATIENT"},
				}}, std.Dictionary)
				return dimse.NormalizedGetSCPResult{DataSet: attributes}, nil
			},
		}
		serverDone <- dimse.ServeAssociation(context.Background(), server, dimse.AssociationSCPOptions{NormalizedSCP: &options})
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result, err := dimse.NewNormalizedClient(client).Get(ctx, dimse.NormalizedGetRequest{
		RequestedSOPClassUID:    exampleSOPClassUID,
		RequestedSOPInstanceUID: exampleSOPInstanceUID,
		AttributeIdentifierList: []core.Tag{patientName},
	})
	if err != nil {
		panic(err)
	}
	name, _ := result.DataSet.GetString(patientName)
	fmt.Printf("N-GET status=0x%04X PatientName=%s\n", result.Response.Status, name)

	if err := client.Release(ctx); err != nil {
		panic(err)
	}
	if err := <-serverDone; err != nil {
		panic(err)
	}
}
