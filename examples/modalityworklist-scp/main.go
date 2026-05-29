// Command modalityworklist-scp serves a synthetic, in-memory Modality
// Worklist. It is intended for local interoperability testing only.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	patientNameTag     = core.NewTag(0x0010, 0x0010)
	patientIDTag       = core.NewTag(0x0010, 0x0020)
	accessionNumberTag = core.NewTag(0x0008, 0x0050)
	modalityTag        = core.NewTag(0x0008, 0x0060)
	requestedDescTag   = core.NewTag(0x0032, 0x1060)
	stationAETitleTag  = core.NewTag(0x0040, 0x0001)
	startDateTag       = core.NewTag(0x0040, 0x0002)
	startTimeTag       = core.NewTag(0x0040, 0x0003)
	performingNameTag  = core.NewTag(0x0040, 0x0006)
	stepDescriptionTag = core.NewTag(0x0040, 0x0007)
	stepIDTag          = core.NewTag(0x0040, 0x0009)
	stationNameTag     = core.NewTag(0x0040, 0x0010)
	stepLocationTag    = core.NewTag(0x0040, 0x0011)
	stepSequenceTag    = core.NewTag(0x0040, 0x0100)
	requestedIDTag     = core.NewTag(0x0040, 0x1001)
)

func main() {
	address := flag.String("listen", "127.0.0.1:11112", "TCP address to listen on")
	aeTitle := flag.String("aet", "MWLSCP", "called AE title")
	flag.Parse()

	handler, err := dimse.NewInMemoryModalityWorklistHandler([]*object.Object{
		record("SYNTHETIC^ALPHA", "SYNTH-001", "ACC-001", "CT", "CT_AE", "20260810", "090000", "STEP-001"),
		record("SYNTHETIC^BETA", "SYNTH-002", "ACC-002", "MR", "MR_AE", "20260810", "103000", "STEP-002"),
	})
	if err != nil {
		log.Fatal(err)
	}
	router, err := dimse.NewModalityWorklistCFindRouter(nil, handler, dimse.ModalityWorklistSCPOptions{})
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	listener, err := ul.Listen(ul.ListenOptions{Address: *address, Context: ctx})
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	fmt.Printf("synthetic MWL SCP listening on %s as %s\n", listener.Addr(), *aeTitle)

	for ctx.Err() == nil {
		association, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   *aeTitle,
			RequireMatchingCalledAE:   true,
			Context:                   ctx,
			SupportedAbstractSyntaxes: []string{dimse.ModalityWorklistFindSOPClassUID},
			SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID},
		})
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			log.Printf("association rejected: %v", err)
			continue
		}
		go func() {
			defer association.Close()
			if err := dimse.ServeAssociation(ctx, association, dimse.AssociationSCPOptions{CFindHandler: router}); err != nil && ctx.Err() == nil {
				log.Printf("association ended: %v", err)
			}
		}()
	}
}

func record(patientName, patientID, accession, modality, stationAE, date, timeValue, stepID string) *object.Object {
	step := core.DataSet{Elements: []core.Element{
		stringElement(stationAETitleTag, core.VRAE, stationAE),
		stringElement(modalityTag, core.VRCS, modality),
		stringElement(startDateTag, core.VRDA, date),
		stringElement(startTimeTag, core.VRTM, timeValue),
		stringElement(performingNameTag, core.VRPN, ""),
		stringElement(stepDescriptionTag, core.VRLO, "SYNTHETIC PROCEDURE"),
		stringElement(stepIDTag, core.VRSH, stepID),
		stringElement(stationNameTag, core.VRSH, ""),
		stringElement(stepLocationTag, core.VRSH, ""),
	}}
	return object.FromElements([]core.Element{
		stringElement(patientNameTag, core.VRPN, patientName),
		stringElement(patientIDTag, core.VRLO, patientID),
		stringElement(accessionNumberTag, core.VRSH, accession),
		stringElement(requestedIDTag, core.VRSH, "REQ-"+stepID),
		stringElement(requestedDescTag, core.VRLO, "SYNTHETIC REQUEST"),
		{Header: core.ElementHeader{Tag: stepSequenceTag, VR: core.VRSQ}, Value: core.SequenceValue{Items: []core.DataSet{step}}},
	}, std.Dictionary)
}

func stringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{Header: core.ElementHeader{Tag: tag, VR: vr}, Value: core.StringValue{value}}
}
