package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/ThalesMMS/dicom-go/internal/netstore"
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
	if err := runServer(opts, stdout, stderr); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	address string
	aeTitle string
	outDir  string
	single  bool
}

const (
	defaultListenAddress      = "127.0.0.1:11112"
	statusOutOfResources      = 0xA700
	statusDataSetDoesNotMatch = 0xA900
	statusCannotUnderstand    = 0xC000
)

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		address: defaultListenAddress,
		aeTitle: "STORESCP",
		outDir:  ".",
	}

	fs := flag.NewFlagSet("storescp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.address, "address", opts.address, "address to listen on")
	fs.StringVar(&opts.aeTitle, "aetitle", opts.aeTitle, "SCP AE title")
	fs.StringVar(&opts.outDir, "output", opts.outDir, "directory for received Part 10 files")
	fs.BoolVar(&opts.single, "single", false, "handle one association and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nServe DICOM C-STORE requests and save received Part 10 files.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errUsage
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return opts, errUsage
	}
	if opts.address == "" {
		_, _ = fmt.Fprintln(stderr, "-address must not be empty")
		fs.Usage()
		return opts, errUsage
	}
	if opts.outDir == "" {
		_, _ = fmt.Fprintln(stderr, "-output must not be empty")
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func runServer(opts options, stdout, stderr io.Writer) error {
	if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
		return err
	}
	listener, err := ul.Listen(ul.ListenOptions{Address: opts.address})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	_, _ = fmt.Fprintf(stdout, "storescp listening on %s\n", listener.Addr())

	for {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   opts.aeTitle,
			SupportedAbstractSyntaxes: acceptedAbstractSyntaxes(),
			SupportedTransferSyntaxes: supportedTransferSyntaxUIDs(),
		})
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			if opts.single {
				return err
			}
			_, _ = fmt.Fprintln(stderr, err)
			continue
		}

		if opts.single {
			if err := handleAssociation(assoc, opts.outDir, stdout); err != nil {
				return err
			}
			return nil
		}
		go func() {
			if err := handleAssociation(assoc, opts.outDir, stdout); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
			}
		}()
	}
}

func handleAssociation(assoc *ul.Association, outDir string, stdout io.Writer) error {
	defer func() { _ = assoc.Close() }()
	_, _ = fmt.Fprintf(stdout, "association accepted from %s\n", assoc.Conn.RemoteAddr())

	for {
		incoming, err := receiveCommandOrRelease(assoc)
		if err != nil {
			return err
		}
		if incoming.release {
			return assoc.WritePDU(&ul.ReleaseRP{})
		}

		field, err := dimse.CommandUint16(incoming.command, dimse.CommandField)
		if err != nil {
			return err
		}
		switch field {
		case dimse.CEchoRQ:
			if err := handleCEchoCommand(assoc, incoming, stdout); err != nil {
				return err
			}
		case dimse.CStoreRQ:
			if err := handleCStoreCommand(assoc, incoming, outDir, stdout); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported DIMSE command field 0x%04X", field)
		}
	}
}

func handleCEchoCommand(assoc *ul.Association, incoming incomingCommand, stdout io.Writer) error {
	messageID, err := dimse.CommandUint16(incoming.command, dimse.MessageID)
	if err != nil {
		return err
	}
	dataSetType, err := dimse.CommandUint16(incoming.command, dimse.CommandDataSetType)
	if err != nil {
		return err
	}
	if dataSetType != dimse.NoDataSet {
		return fmt.Errorf("C-ECHO request dataset type 0x%04X, want no dataset", dataSetType)
	}
	if err := dimse.SendCEchoResponse(assoc, incoming.pcID, messageID, dimse.StatusSuccess); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "C-ECHO response sent status=0x%04X\n", dimse.StatusSuccess)
	return nil
}

func handleCStoreCommand(assoc *ul.Association, incoming incomingCommand, outDir string, stdout io.Writer) error {
	req, err := dimse.ParseCStoreRequest(incoming.command)
	if err != nil {
		return err
	}
	pc, err := acceptedContextByID(assoc, incoming.pcID)
	if err != nil {
		return err
	}
	syntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID)
	if !ok {
		return fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, pc.TransferSyntaxUID)
	}

	status := uint16(dimse.StatusSuccess)
	dataset, err := receiveDataSet(assoc, incoming, syntax)
	if err != nil {
		status = statusCannotUnderstand
	} else if err := netstore.ValidateCStoreDataSet(req.AffectedSOPClassUID, req.AffectedSOPInstanceUID, pc, dataset); err != nil {
		status = statusDataSetDoesNotMatch
	} else {
		path, saveErr := netstore.SavePart10(outDir, dataset, syntax)
		if saveErr != nil {
			status = statusOutOfResources
			_, _ = fmt.Fprintf(stdout, "store failed sop-instance=%s sop-class=%s error=%v\n", req.AffectedSOPInstanceUID, req.AffectedSOPClassUID, saveErr)
		} else {
			_, _ = fmt.Fprintf(stdout, "stored %s sop-instance=%s sop-class=%s\n", path, req.AffectedSOPInstanceUID, req.AffectedSOPClassUID)
		}
	}

	rsp := dimse.CStoreResponse{
		AffectedSOPClassUID:       req.AffectedSOPClassUID,
		MessageIDBeingRespondedTo: req.MessageID,
		AffectedSOPInstanceUID:    req.AffectedSOPInstanceUID,
		Status:                    status,
	}
	if rspErr := dimse.SendCStoreResponse(assoc, incoming.pcID, rsp); rspErr != nil && err == nil {
		err = rspErr
	}
	_, _ = fmt.Fprintf(stdout, "C-STORE response sent status=0x%04X\n", status)
	return err
}

type incomingCommand struct {
	release    bool
	pcID       byte
	command    *object.Object
	dataPrefix []byte
	dataLast   bool
}

func receiveCommandOrRelease(assoc *ul.Association) (incomingCommand, error) {
	var command bytes.Buffer
	var out incomingCommand
	commandDone := false

	for !commandDone {
		pdu, err := assoc.ReadPDU()
		if err != nil {
			return incomingCommand{}, err
		}
		switch pdu := pdu.(type) {
		case *ul.ReleaseRQ:
			return incomingCommand{release: true}, nil
		case *ul.PDataTF:
			for _, value := range pdu.Values {
				if out.pcID == 0 {
					out.pcID = value.PresentationContextID
				}
				if value.PresentationContextID != out.pcID {
					return incomingCommand{}, fmt.Errorf("%w: got %d, want %d", dimse.ErrPresentationContextMismatch, value.PresentationContextID, out.pcID)
				}
				if value.IsCommand {
					if commandDone {
						return incomingCommand{}, errors.New("unexpected command PDV after command completion")
					}
					command.Write(value.Data)
					if value.IsLast {
						commandDone = true
					}
					continue
				}
				if !commandDone {
					return incomingCommand{}, errors.New("dataset PDV received before complete command")
				}
				out.dataPrefix = append(out.dataPrefix, value.Data...)
				if value.IsLast {
					out.dataLast = true
				}
			}
		default:
			return incomingCommand{}, fmt.Errorf("%w: got %T while waiting for command P-DATA or A-RELEASE-RQ", ul.ErrUnexpectedPDU, pdu)
		}
	}

	obj, err := dimse.DecodeCommandSet(command.Bytes())
	if err != nil {
		return incomingCommand{}, err
	}
	out.command = obj
	return out, nil
}

func receiveDataSet(assoc *ul.Association, incoming incomingCommand, syntax transfer.Syntax) (*object.Object, error) {
	if incoming.dataLast {
		return object.ReadDataSet(bytes.NewReader(incoming.dataPrefix), syntax)
	}
	if len(incoming.dataPrefix) == 0 {
		return dimse.ReceiveDataSet(assoc, incoming.pcID, syntax)
	}
	reader := io.MultiReader(bytes.NewReader(incoming.dataPrefix), dimse.NewPDataReader(assoc, incoming.pcID))
	return object.ReadDataSet(reader, syntax)
}

func acceptedContextByID(assoc *ul.Association, pcID byte) (ul.AcceptedContext, error) {
	if assoc == nil {
		return ul.AcceptedContext{}, errors.New("nil association")
	}
	for _, pc := range assoc.AcceptedContexts {
		if pc.ID == pcID {
			return pc, nil
		}
	}
	return ul.AcceptedContext{}, fmt.Errorf("no accepted presentation context with ID %d", pcID)
}

func acceptedAbstractSyntaxes() []string {
	uids := []string{dimse.VerificationSOPClassUID}
	uids = append(uids, storageSOPClassUIDs()...)
	return uids
}

func storageSOPClassUIDs() []string {
	return []string{
		"1.2.840.10008.5.1.4.1.1.1",     // Computed Radiography Image Storage
		"1.2.840.10008.5.1.4.1.1.2",     // CT Image Storage
		"1.2.840.10008.5.1.4.1.1.2.1",   // Enhanced CT Image Storage
		"1.2.840.10008.5.1.4.1.1.4",     // MR Image Storage
		"1.2.840.10008.5.1.4.1.1.4.1",   // Enhanced MR Image Storage
		"1.2.840.10008.5.1.4.1.1.6.1",   // Ultrasound Image Storage
		"1.2.840.10008.5.1.4.1.1.7",     // Secondary Capture Image Storage
		"1.2.840.10008.5.1.4.1.1.7.1",   // Multi-frame Single Bit Secondary Capture Image Storage
		"1.2.840.10008.5.1.4.1.1.7.2",   // Multi-frame Grayscale Byte Secondary Capture Image Storage
		"1.2.840.10008.5.1.4.1.1.7.3",   // Multi-frame Grayscale Word Secondary Capture Image Storage
		"1.2.840.10008.5.1.4.1.1.7.4",   // Multi-frame True Color Secondary Capture Image Storage
		"1.2.840.10008.5.1.4.1.1.12.1",  // X-Ray Angiographic Image Storage
		"1.2.840.10008.5.1.4.1.1.12.2",  // X-Ray Radiofluoroscopic Image Storage
		"1.2.840.10008.5.1.4.1.1.20",    // Nuclear Medicine Image Storage
		"1.2.840.10008.5.1.4.1.1.128",   // Positron Emission Tomography Image Storage
		"1.2.840.10008.5.1.4.1.1.1.1",   // Digital X-Ray Image Storage - For Presentation
		"1.2.840.10008.5.1.4.1.1.1.1.1", // Digital X-Ray Image Storage - For Processing
	}
}

func supportedTransferSyntaxUIDs() []string {
	return []string{
		transfer.ImplicitVRLittleEndian.UID,
		transfer.ExplicitVRLittleEndian.UID,
	}
}
