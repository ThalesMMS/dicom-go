package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const studyRootMoveUID = "1.2.840.10008.5.1.4.1.2.2.2"

func main() {
	var remote string
	var callingAET string
	var calledAET string
	var moveDestination string
	var level string
	var studyUID string

	flag.StringVar(&remote, "remote", "127.0.0.1:4242", "Remote DICOM address host:port")
	flag.StringVar(&callingAET, "calling-aet", "DICOMGO", "Calling AE title")
	flag.StringVar(&calledAET, "called-aet", "ORTHANC", "Called AE title")
	flag.StringVar(&moveDestination, "move-destination", "DICOMSTORE", "C-MOVE destination AE title")
	flag.StringVar(&level, "level", "STUDY", "QueryRetrieveLevel: STUDY (only supported value for now)")
	flag.StringVar(&studyUID, "study-uid", "", "StudyInstanceUID to retrieve")
	flag.Parse()

	if studyUID == "" {
		fmt.Fprintln(os.Stderr, "-study-uid is required")
		os.Exit(2)
	}
	if level != "STUDY" {
		fmt.Fprintln(os.Stderr, "only -level=STUDY is supported")
		os.Exit(2)
	}

	// Build a minimal C-MOVE identifier dataset.
	identifierDS := core.DataSet{Elements: []core.Element{
		{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{level}},    // QueryRetrieveLevel
		{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{studyUID}}, // StudyInstanceUID
	}}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	assoc, err := dimse.DialRetrieveSCU(ctx, dimse.RetrieveDialOptions{
		Address:        remote,
		CallingAETitle: callingAET,
		CalledAETitle:  calledAET,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  studyRootMoveUID,
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "dial retrieve scu failed:", err)
		os.Exit(1)
	}
	defer func() {
		if err := assoc.Release(context.Background()); err != nil {
			fmt.Fprintln(os.Stderr, "release association failed:", err)
		}
	}()

	// Use the first accepted presentation context for Study Root MOVE.
	var pcid byte
	for _, ac := range assoc.AcceptedContexts {
		if ac.AbstractSyntaxUID == studyRootMoveUID {
			pcid = ac.ID
			break
		}
	}
	if pcid == 0 {
		fmt.Fprintln(os.Stderr, "no accepted presentation context for Study Root MOVE")
		os.Exit(1)
	}

	// Identifier transfer syntax should match the transfer syntax accepted for this presentation context.
	// For this interop harness, we default to Implicit VR Little Endian.
	client := dimse.NewRetrieveClient(assoc, pcid, transfer.ImplicitVRLittleEndian)
	identifierObj := object.FromDataSet(identifierDS, std.Dictionary)

	resp, err := client.Move(ctx, dimse.CMoveRequest{
		AffectedSOPClassUID: studyRootMoveUID,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     moveDestination,
	}, identifierObj)
	if err != nil {
		fmt.Fprintln(os.Stderr, "c-move failed:", err)
		os.Exit(1)
	}

	fmt.Printf("C-MOVE completed with status=0x%04X\n", resp.Status)
}
