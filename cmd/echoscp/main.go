package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"

	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
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
	port    int
	aeTitle string
	single  bool
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		port:    11112,
		aeTitle: "ECHOSCP",
	}

	fs := flag.NewFlagSet("echoscp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&opts.port, "port", opts.port, "listen TCP port")
	fs.StringVar(&opts.aeTitle, "ae-title", opts.aeTitle, "SCP AE title")
	fs.BoolVar(&opts.single, "single", false, "handle one association and exit")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nServe DICOM Verification C-ECHO requests.\n\nFlags:\n", fs.Name())
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
	if opts.port < 0 || opts.port > 65535 {
		_, _ = fmt.Fprintln(stderr, "-port must be between 0 and 65535")
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func runServer(opts options, stdout, stderr io.Writer) error {
	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(opts.port))
	listener, err := ul.Listen(ul.ListenOptions{Address: address})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	_, _ = fmt.Fprintf(stdout, "echoscp listening on %s\n", listener.Addr())

	for {
		assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
			AETitle:                   opts.aeTitle,
			SupportedAbstractSyntaxes: []string{dimse.VerificationSOPClassUID},
			SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
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
			if err := handleAssociation(assoc, stdout); err != nil {
				return err
			}
			return nil
		}
		go func() {
			if err := handleAssociation(assoc, stdout); err != nil {
				_, _ = fmt.Fprintln(stderr, err)
			}
		}()
	}
}

func handleAssociation(assoc *ul.Association, stdout io.Writer) error {
	defer func() { _ = assoc.Close() }()
	_, _ = fmt.Fprintf(stdout, "association accepted from %s\n", assoc.Conn.RemoteAddr())

	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		return errors.New("no accepted Verification SOP Class presentation context")
	}
	messageID, err := dimse.ReceiveCEcho(assoc, pc.ID)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "C-ECHO received message-id=%d\n", messageID)
	if err := dimse.SendCEchoResponse(assoc, pc.ID, messageID, dimse.StatusSuccess); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "C-ECHO response sent status=0x%04X\n", dimse.StatusSuccess)
	return waitForRelease(assoc)
}

func waitForRelease(assoc *ul.Association) error {
	pdu, err := assoc.ReadPDU()
	if err != nil {
		return err
	}
	if _, ok := pdu.(*ul.ReleaseRQ); !ok {
		return fmt.Errorf("%w: got %T while waiting for A-RELEASE-RQ", ul.ErrUnexpectedPDU, pdu)
	}
	return assoc.WritePDU(&ul.ReleaseRP{})
}
