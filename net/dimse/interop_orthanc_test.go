package dimse

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/testutil"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// This is an opt-in integration test that runs against a real Orthanc instance.
//
// Enable with:
//
//	DICOMGO_INTEGRATION=1 ORTHANC_HOST=127.0.0.1 ORTHANC_PORT=4242 STUDY_UID=... go test ./... -run TestInteropOrthancCMoveStudy
//
// Requires a C-STORE SCP listening under MOVE_DEST_AET (see docs/INTEROP_ORTHANC.md).
func TestInteropOrthancCMoveStudy(t *testing.T) {
	testutil.SkipIfIntegration(t)

	orthancHost := getenvDefault("ORTHANC_HOST", "127.0.0.1")
	orthancPort := getenvDefault("ORTHANC_PORT", "4242")
	remoteAddr := fmt.Sprintf("%s:%s", orthancHost, orthancPort)

	callingAET := getenvDefault("CALLING_AET", "DICOMGO")
	calledAET := getenvDefault("CALLED_AET", "ORTHANC")
	moveDestAET := getenvDefault("MOVE_DEST_AET", "DICOMSTORE")

	studyUID := os.Getenv("STUDY_UID")
	if studyUID == "" {
		t.Fatalf("STUDY_UID env var is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	assoc, err := DialRetrieveSCU(ctx, RetrieveDialOptions{
		Address:        remoteAddr,
		CallingAETitle: callingAET,
		CalledAETitle:  calledAET,
		MaxPDU:         ul.DefaultMaxPDU,
		Contexts: []ul.PresentationContext{{
			ID:                 1,
			AbstractSyntaxUID:  "1.2.840.10008.5.1.4.1.2.2.2", // Study Root Query/Retrieve Information Model - MOVE
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		t.Fatalf("DialRetrieveSCU: %v", err)
	}
	defer func() {
		if err := assoc.Release(ctx); err != nil {
			t.Errorf("assoc.Release failed: %v", err)
		}
	}()

	var pcID byte
	for _, ac := range assoc.AcceptedContexts {
		if ac.AbstractSyntaxUID == "1.2.840.10008.5.1.4.1.2.2.2" {
			pcID = ac.ID
			break
		}
	}
	if pcID == 0 {
		t.Fatalf("no accepted presentation context for Study Root MOVE")
	}

	client := NewRetrieveClient(assoc, pcID, transfer.ImplicitVRLittleEndian)

	// Minimal Identifier dataset.
	id := object.New(std.Dictionary)
	id.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS}, Value: core.StringValue{"STUDY"}})  // QueryRetrieveLevel
	id.Put(core.Element{Header: core.ElementHeader{Tag: core.NewTag(0x0020, 0x000D), VR: core.VRUI}, Value: core.StringValue{studyUID}}) // StudyInstanceUID

	final, err := client.Move(ctx, CMoveRequest{
		MessageID:           1,
		AffectedSOPClassUID: "1.2.840.10008.5.1.4.1.2.2.2",
		MoveDestination:     moveDestAET,
		Priority:            0,
	}, id)
	if err != nil {
		t.Fatalf("CMove: %v", err)
	}
	if final.Status != StatusSuccess {
		t.Fatalf("expected final status success, got 0x%04X", uint16(final.Status))
	}
}

func getenvDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
