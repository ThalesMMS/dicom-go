// Command modalityworklist-scu queries a Modality Worklist SCP using the
// package's typed, streaming client.
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
)

var (
	patientNameTag  = core.NewTag(0x0010, 0x0010)
	modalityTag     = core.NewTag(0x0008, 0x0060)
	startDateTag    = core.NewTag(0x0040, 0x0002)
	stepSequenceTag = core.NewTag(0x0040, 0x0100)
)

func main() {
	address := flag.String("address", "127.0.0.1:11112", "MWL SCP TCP address")
	calledAE := flag.String("called", "MWLSCP", "called AE title")
	callingAE := flag.String("calling", "MWLSCU", "calling AE title")
	patientName := flag.String("patient-name", "*", "PN wildcard matching value")
	modality := flag.String("modality", "", "exact Modality matching value; empty requests it as a return key")
	dateRange := flag.String("date", "", "DA value or range; empty requests it as a return key")
	timeout := flag.Duration("timeout", 15*time.Second, "association and query timeout")
	flag.Parse()

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	association, err := ul.DialContext(ctx, *address, ul.DialOptions{
		CalledAETitle: *calledAE, CallingAETitle: *callingAE,
		Contexts: []ul.PresentationContext{dimse.ModalityWorklistFindPresentationContext()},
	})
	if err != nil {
		log.Fatal(err)
	}
	defer association.Close()

	client, err := dimse.NewModalityWorklistClient(association)
	if err != nil {
		log.Fatal(err)
	}
	step := &dimse.ModalityWorklistScheduledProcedureStep{
		Modality:                        queryOrReturn(*modality),
		ScheduledProcedureStepStartDate: queryOrReturn(*dateRange),
	}
	result, err := client.Find(ctx, dimse.ModalityWorklistQuery{
		PatientName:            dimse.MWLMatch(*patientName),
		ScheduledProcedureStep: step,
	}, printMatch)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("complete: %d match(es), status 0x%04X\n", result.MatchCount, result.FinalResponse.Status)
	if err := association.Release(ctx); err != nil {
		log.Fatal(err)
	}
}

func queryOrReturn(value string) dimse.MWLKey {
	if value == "" {
		return dimse.MWLReturnKey()
	}
	return dimse.MWLMatch(value)
}

func printMatch(match *object.Object) error {
	name, _ := match.GetString(patientNameTag)
	steps, ok := match.GetSequence(stepSequenceTag)
	if !ok || len(steps) != 1 {
		return fmt.Errorf("MWL response does not contain exactly one Scheduled Procedure Step item")
	}
	modality, _ := steps[0].GetString(modalityTag)
	date, _ := steps[0].GetString(startDateTag)
	fmt.Printf("patient=%q modality=%q date=%q\n", name, modality, date)
	return nil
}
