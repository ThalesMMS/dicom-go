package main

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestStorescpPersistenceUIDBoundaryAndPrivateLogs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	listener, err := ul.Listen(ul.ListenOptions{Address: "127.0.0.1:0", Context: ctx})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dir := t.TempDir()
	var log bytes.Buffer
	done := make(chan error, 1)
	go func() {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{AETitle: "STORESCP", Context: ctx, AcceptAnyAbstractSyntax: true, SupportedTransferSyntaxes: []string{transfer.ExplicitVRLittleEndian.UID}})
		if err != nil {
			done <- err
			return
		}
		done <- handleAssociation(assoc, dir, &log)
	}()
	assoc, err := ul.DialContext(ctx, listener.Addr().String(), ul.DialOptions{CalledAETitle: "STORESCP", CallingAETitle: "SYNTHETIC", Contexts: []ul.PresentationContext{{AbstractSyntaxUID: dicomtest.TestSOPClassUID, TransferSyntaxUIDs: []string{transfer.ExplicitVRLittleEndian.UID}}}})
	if err != nil {
		t.Fatal(err)
	}
	defer assoc.Close()
	pc, err := dimse.AcceptedContextForSOPClass(assoc, dicomtest.TestSOPClassUID)
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		command, dataset string
		want             uint16
	}{
		{"../escape", "../escape", statusDataSetDoesNotMatch},
		{"CON", "CON", statusDataSetDoesNotMatch},
		{"1.2/3", "1.2/3", statusDataSetDoesNotMatch},
		{"1.02.3", "1.02.3", statusDataSetDoesNotMatch},
		{"1." + strings.Repeat("2", 63), "1." + strings.Repeat("2", 63), statusDataSetDoesNotMatch},
		{"1.2.3", "1.2.4", statusDataSetDoesNotMatch},
		{dicomtest.TestSOPInstanceUID, dicomtest.TestSOPInstanceUID, dimse.StatusSuccess},
		{dicomtest.TestSOPInstanceUID, dicomtest.TestSOPInstanceUID, dimse.StatusSuccess},
	}
	for i, test := range cases {
		ds := object.FromElements([]core.Element{
			{Header: core.ElementHeader{Tag: dicomtags.SOPClassUID, VR: core.VRUI}, Value: core.StringValue{dicomtest.TestSOPClassUID}},
			{Header: core.ElementHeader{Tag: dicomtags.SOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{test.dataset}},
		}, std.Dictionary)
		if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{AffectedSOPClassUID: dicomtest.TestSOPClassUID, AffectedSOPInstanceUID: test.command, MessageID: uint16(i + 1)}); err != nil {
			t.Fatal(err)
		}
		if err := dimse.SendDataSet(assoc, pc.ID, ds, transfer.ExplicitVRLittleEndian); err != nil {
			t.Fatal(err)
		}
		rsp, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
		if err != nil {
			t.Fatal(err)
		}
		if rsp.Status != test.want {
			t.Fatalf("case %d: status %04X want %04X", i, rsp.Status, test.want)
		}
	}
	if err := assoc.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("unexpected persisted files: %v %v", entries, err)
	}
	for _, secret := range []string{dir, dicomtest.TestSOPInstanceUID, dicomtest.TestSOPClassUID, "escape", "CON"} {
		if strings.Contains(log.String(), secret) {
			t.Fatalf("private value in default logs: %q", log.String())
		}
	}
}
