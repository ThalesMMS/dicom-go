package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	priorityMedium = 0
	dialTimeout    = 10 * time.Second
	releaseTimeout = 5 * time.Second
)

var (
	tagMediaStorageSOPClassUID = core.NewTag(0x0002, 0x0002)
	tagTransferSyntaxUID       = core.NewTag(0x0002, 0x0010)
	tagSOPClassUID             = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID          = core.NewTag(0x0008, 0x0018)
)

func main() {
	args := normalizeArgs(os.Args[1:])
	if len(args) != 2 {
		fmt.Fprintf(os.Stderr, "usage: go run ./examples/store -- <host:port> <file.dcm>\n")
		os.Exit(2)
	}
	if err := sendStore(args[0], args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "C-STORE: %v\n", err)
		os.Exit(1)
	}
}

func sendStore(address, filePath string) error {
	file, err := object.OpenFile(filePath)
	if err != nil {
		return err
	}
	if file.Dataset == nil {
		return fmt.Errorf("file has no dataset")
	}
	sopClassUID, err := sopClassUID(file)
	if err != nil {
		return err
	}
	sopInstanceUID, ok := file.Dataset.GetUID(tagSOPInstanceUID)
	if !ok || sopInstanceUID == "" {
		return object.ErrMissingSOPInstanceUID
	}
	fileTransferSyntaxUID, err := transferSyntaxUID(file)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), dialTimeout)
	defer cancel()
	assoc, err := ul.Dial(address, ul.DialOptions{
		CalledAETitle:  "STORESCP",
		CallingAETitle: "STORESCU",
		Context:        ctx,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  sopClassUID,
			TransferSyntaxUIDs: transfer.ProposedStoreTransferSyntaxUIDs(fileTransferSyntaxUID, transfer.NativeStoreSourceFirst),
		}},
	})
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			_ = assoc.Close()
		}
	}()

	pc, err := dimse.AcceptedContextForSOPClass(assoc, sopClassUID)
	if err != nil {
		return err
	}
	negotiatedSyntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID)
	if !ok {
		return fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, pc.TransferSyntaxUID)
	}

	messageID := uint16(1)
	if err := dimse.SendCStoreRequest(assoc, pc.ID, dimse.CStoreRequest{
		AffectedSOPClassUID:    sopClassUID,
		MessageID:              messageID,
		Priority:               priorityMedium,
		AffectedSOPInstanceUID: sopInstanceUID,
	}); err != nil {
		return err
	}
	if err := dimse.SendDataSet(assoc, pc.ID, file.Dataset, negotiatedSyntax); err != nil {
		return err
	}
	response, err := dimse.ReceiveCStoreResponse(assoc, pc.ID)
	if err != nil {
		return err
	}
	if response.MessageIDBeingRespondedTo != messageID {
		return fmt.Errorf("response message ID %d, want %d", response.MessageIDBeingRespondedTo, messageID)
	}
	if response.Status != dimse.StatusSuccess {
		return fmt.Errorf("remote C-STORE status=0x%04X", response.Status)
	}
	fmt.Printf("C-STORE success sop-instance=%s syntax=%s\n", sopInstanceUID, negotiatedSyntax.UID)

	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancelRelease()
	if err := assoc.Release(releaseCtx); err != nil {
		return err
	}
	released = true
	return nil
}

func sopClassUID(file *object.File) (string, error) {
	if uid, ok := file.GetUID(tagMediaStorageSOPClassUID); ok && uid != "" {
		return uid, nil
	}
	if uid, ok := file.Dataset.GetUID(tagSOPClassUID); ok && uid != "" {
		return uid, nil
	}
	return "", object.ErrMissingSOPClassUID
}

func transferSyntaxUID(file *object.File) (string, error) {
	if uid, ok := file.GetUID(tagTransferSyntaxUID); ok && uid != "" {
		return uid, nil
	}
	if file.TransferSyntax.UID != "" {
		return file.TransferSyntax.UID, nil
	}
	return "", object.ErrMissingTransferSyntax
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
