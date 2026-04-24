package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
)

const timeout = 10 * time.Second

func main() {
	args := normalizeArgs(os.Args[1:])
	if len(args) != 2 || (args[0] != "scu" && args[0] != "scp") {
		fmt.Fprintf(os.Stderr, "usage: go run ./examples/echo -- scu <host:port>\n")
		fmt.Fprintf(os.Stderr, "       go run ./examples/echo -- scp <host:port>\n")
		os.Exit(2)
	}

	var err error
	if args[0] == "scp" {
		err = runSCP(args[1])
	} else {
		err = runSCU(args[1])
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", args[0], err)
		os.Exit(1)
	}
}

func runSCU(address string) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	assoc, err := ul.Dial(address, ul.DialOptions{
		CalledAETitle:  "ECHOSCP",
		CallingAETitle: "ECHOSCU",
		Context:        ctx,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  dimse.VerificationSOPClassUID,
			TransferSyntaxUIDs: []string{ul.ImplicitVRLittleEndian},
		}},
	})
	if err != nil {
		return err
	}
	released := false
	defer func() {
		if !released {
			if err := assoc.Close(); err != nil {
				fmt.Fprintf(os.Stderr, "close SCU association: %v\n", err)
			}
		}
	}()

	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		return fmt.Errorf("no accepted Verification presentation context")
	}
	response, err := dimse.SendCEcho(assoc, pc.ID, 1)
	if err != nil {
		return err
	}
	fmt.Printf("C-ECHO success status=0x%04X\n", response.Status)

	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRelease()
	if err := assoc.Release(releaseCtx); err != nil {
		return err
	}
	released = true
	return nil
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func runSCP(address string) error {
	listener, err := ul.Listen(ul.ListenOptions{Address: address})
	if err != nil {
		return err
	}
	defer func() {
		if err := listener.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close listener: %v\n", err)
		}
	}()
	fmt.Printf("listening on %s\n", listener.Addr())

	assoc, err := listener.AcceptAssociation(ul.AcceptOptions{
		AETitle:                   "ECHOSCP",
		SupportedAbstractSyntaxes: []string{dimse.VerificationSOPClassUID},
		SupportedTransferSyntaxes: []string{ul.ImplicitVRLittleEndian},
	})
	if err != nil {
		return err
	}
	defer func() {
		if err := assoc.Close(); err != nil {
			fmt.Fprintf(os.Stderr, "close association: %v\n", err)
		}
	}()
	fmt.Printf("association from %s\n", assoc.Conn.RemoteAddr())

	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		return fmt.Errorf("no accepted Verification presentation context")
	}
	messageID, err := dimse.ReceiveCEcho(assoc, pc.ID)
	if err != nil {
		return err
	}
	if err := dimse.SendCEchoResponse(assoc, pc.ID, messageID, dimse.StatusSuccess); err != nil {
		return err
	}
	pdu, err := assoc.ReadPDU()
	if err != nil {
		return err
	}
	if _, ok := pdu.(*ul.ReleaseRQ); !ok {
		return fmt.Errorf("%w: got %T while waiting for A-RELEASE-RQ", ul.ErrUnexpectedPDU, pdu)
	}
	if err := assoc.WritePDU(&ul.ReleaseRP{}); err != nil {
		return err
	}
	return nil
}
