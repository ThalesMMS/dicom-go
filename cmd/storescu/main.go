package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/dicomtags"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := runStore(opts, stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	address   string
	calledAE  string
	callingAE string
	timeout   time.Duration
	files     []string
}

const (
	defaultDialTimeout    = 10 * time.Second
	defaultReleaseTimeout = 5 * time.Second
	priorityMedium        = 0
)

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		calledAE:  "ANY-SCP",
		callingAE: "STORESCU",
		timeout:   defaultDialTimeout,
	}

	fs := flag.NewFlagSet("storescu", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.calledAE, "called", opts.calledAE, "called AE title")
	fs.StringVar(&opts.callingAE, "calling", opts.callingAE, "calling AE title")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "connection timeout")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags] address file\n\nSend one DICOM Part 10 file with C-STORE.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errUsage
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return opts, errUsage
	}
	opts.address = fs.Arg(0)
	opts.files = append([]string(nil), fs.Args()[1:]...)
	if opts.timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "-timeout must be positive")
		fs.Usage()
		return opts, errUsage
	}
	if len(opts.files) != 1 {
		_, _ = fmt.Fprintln(stderr, "storescu currently supports one file per invocation")
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func runStore(opts options, stdout, stderr io.Writer) error {
	_ = stderr
	filePath := opts.files[0]
	file, err := object.OpenFile(filePath)
	if err != nil {
		return err
	}
	if file.Dataset == nil {
		return errors.New("DICOM file has no dataset")
	}

	sopClassUID, err := fileSOPClassUID(file)
	if err != nil {
		return err
	}
	sopInstanceUID, ok := file.Dataset.GetUID(dicomtags.SOPInstanceUID)
	if !ok || sopInstanceUID == "" {
		return object.ErrMissingSOPInstanceUID
	}
	fileTransferSyntaxUID, err := fileTransferSyntaxUID(file)
	if err != nil {
		return err
	}

	dialCtx, cancelDial := context.WithTimeout(context.Background(), opts.timeout)
	defer cancelDial()

	assoc, err := ul.Dial(opts.address, ul.DialOptions{
		CalledAETitle:  opts.calledAE,
		CallingAETitle: opts.callingAE,
		Context:        dialCtx,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  sopClassUID,
			TransferSyntaxUIDs: proposedTransferSyntaxes(fileTransferSyntaxUID),
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
		return fmt.Errorf("C-STORE response message ID %d, want %d", response.MessageIDBeingRespondedTo, messageID)
	}
	if response.AffectedSOPInstanceUID != sopInstanceUID {
		return fmt.Errorf("C-STORE response SOP Instance UID %q, want %q", response.AffectedSOPInstanceUID, sopInstanceUID)
	}
	var responseErr error
	if response.Status != dimse.StatusSuccess {
		if isWarningStatus(response.Status) {
			_, _ = fmt.Fprintf(stdout, "C-STORE warning status=0x%04X sop-instance=%s\n", response.Status, sopInstanceUID)
		} else {
			responseErr = fmt.Errorf("C-STORE failed with status 0x%04X", response.Status)
			_, _ = fmt.Fprintf(stdout, "C-STORE failure status=0x%04X sop-instance=%s\n", response.Status, sopInstanceUID)
		}
	} else {
		_, _ = fmt.Fprintf(stdout, "C-STORE success status=0x%04X sop-instance=%s\n", response.Status, sopInstanceUID)
	}

	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), defaultReleaseTimeout)
	defer cancelRelease()
	if err := assoc.Release(releaseCtx); err != nil {
		return err
	}
	released = true
	return responseErr
}

func fileSOPClassUID(file *object.File) (string, error) {
	if uid, ok := file.GetUID(dicomtags.MediaStorageSOPClassUID); ok && uid != "" {
		return uid, nil
	}
	if file != nil && file.Dataset != nil {
		if uid, ok := file.Dataset.GetUID(dicomtags.SOPClassUID); ok && uid != "" {
			return uid, nil
		}
	}
	return "", object.ErrMissingSOPClassUID
}

func fileTransferSyntaxUID(file *object.File) (string, error) {
	if uid, ok := file.GetUID(dicomtags.TransferSyntaxUID); ok && uid != "" {
		return uid, nil
	}
	if file != nil && file.TransferSyntax.UID != "" {
		return file.TransferSyntax.UID, nil
	}
	return "", object.ErrMissingTransferSyntax
}

func proposedTransferSyntaxes(fileTransferSyntaxUID string) []string {
	seen := map[string]bool{}
	var uids []string
	for _, uid := range []string{
		fileTransferSyntaxUID,
		transfer.ExplicitVRLittleEndian.UID,
		transfer.ImplicitVRLittleEndian.UID,
	} {
		uid = transfer.NormalizeUID(uid)
		if uid == "" || seen[uid] {
			continue
		}
		seen[uid] = true
		uids = append(uids, uid)
	}
	return uids
}

func isWarningStatus(status uint16) bool {
	return status == 0x0001 || status == 0x0107 || status == 0x0116 || (status >= 0xB000 && status <= 0xBFFF)
}
