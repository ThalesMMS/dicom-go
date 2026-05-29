package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/net/audit"
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
		clidiag.Fprintln(stderr, "echoscp", err)
		return 1
	}
	if err := runServer(opts, stdout, stderr); err != nil {
		clidiag.Fprintln(stderr, "echoscp", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	port                int
	aeTitle             string
	single              bool
	maxAssociations     int
	maxActiveOperations int
	queueDepth          int
	enqueueTimeout      time.Duration
	auditSink           audit.Sink
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
	fs.IntVar(&opts.maxAssociations, "max-associations", 0, "maximum active plus queued associations when queueing is enabled (0 unbounded)")
	fs.IntVar(&opts.maxActiveOperations, "max-active-operations", 0, "maximum active association handlers when queueing is enabled (0 derives from -max-associations or 1)")
	fs.IntVar(&opts.queueDepth, "queue-depth", 0, "maximum associations waiting for a handler")
	fs.DurationVar(&opts.enqueueTimeout, "enqueue-timeout", 0, "maximum time to wait for queue capacity (0 fails immediately)")
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
	if opts.maxAssociations < 0 {
		_, _ = fmt.Fprintln(stderr, "-max-associations must not be negative")
		fs.Usage()
		return opts, errUsage
	}
	if opts.maxActiveOperations < 0 {
		_, _ = fmt.Fprintln(stderr, "-max-active-operations must not be negative")
		fs.Usage()
		return opts, errUsage
	}
	if opts.queueDepth < 0 {
		_, _ = fmt.Fprintln(stderr, "-queue-depth must not be negative")
		fs.Usage()
		return opts, errUsage
	}
	if opts.enqueueTimeout < 0 {
		_, _ = fmt.Fprintln(stderr, "-enqueue-timeout must not be negative")
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func runServer(opts options, stdout, stderr io.Writer) error {
	var queue *dimse.ServiceQueue
	if !opts.single && opts.queueEnabled() {
		var err error
		queue, err = dimse.NewServiceQueue(opts.serviceQueueOptions())
		if err != nil {
			return err
		}
		defer func() { _ = queue.Close(context.Background()) }()
	}

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
			clidiag.Fprintln(stderr, "echoscp", err)
			continue
		}

		if opts.single {
			if err := handleAssociation(assoc, stdout, opts.auditSink); err != nil {
				return err
			}
			return nil
		}
		if queue != nil {
			assoc := assoc
			if err := queue.Enqueue(context.Background(), func(context.Context) {
				if err := handleAssociation(assoc, stdout, opts.auditSink); err != nil {
					clidiag.Fprintln(stderr, "echoscp", err)
				}
			}); err != nil {
				rejectAssociationForQueueBackpressure(assoc, stderr, err)
			}
			continue
		}
		go func() {
			if err := handleAssociation(assoc, stdout, opts.auditSink); err != nil {
				clidiag.Fprintln(stderr, "echoscp", err)
			}
		}()
	}
}

func (opts options) queueEnabled() bool {
	return opts.maxAssociations > 0 || opts.maxActiveOperations > 0 || opts.queueDepth > 0 || opts.enqueueTimeout > 0
}

func (opts options) serviceQueueOptions() dimse.ServiceQueueOptions {
	maxActive := opts.maxActiveOperations
	if maxActive == 0 {
		maxActive = opts.maxAssociations
	}
	if maxActive == 0 {
		maxActive = 1
	}
	return dimse.ServiceQueueOptions{
		MaxConcurrentAssociations: opts.maxAssociations,
		MaxActiveOperations:       maxActive,
		QueueDepth:                opts.queueDepth,
		EnqueueTimeout:            opts.enqueueTimeout,
	}
}

func rejectAssociationForQueueBackpressure(assoc *ul.Association, stderr io.Writer, err error) {
	clidiag.Fprintln(stderr, "echoscp", fmt.Errorf("service queue refused association: %w", err))
	if assoc == nil {
		return
	}
	if abortErr := assoc.Abort(ul.AbortReasonNotSpecified); abortErr != nil {
		clidiag.Fprintln(stderr, "echoscp", fmt.Errorf("abort after service queue refusal: %w", abortErr))
	}
}

func handleAssociation(assoc *ul.Association, stdout io.Writer, auditSink audit.Sink) error {
	defer func() { _ = assoc.Close() }()
	_, _ = fmt.Fprintf(stdout, "association accepted from %s\n", assoc.Conn.RemoteAddr())
	emitEchoAudit(assoc, auditSink, audit.Event{Kind: audit.AssociationAccepted})

	pc, ok := dimse.AcceptedVerificationContext(assoc)
	if !ok {
		err := errors.New("no accepted Verification SOP Class presentation context")
		emitEchoAudit(assoc, auditSink, audit.Event{
			Kind:       audit.OperationFailed,
			Service:    "C-ECHO",
			Command:    "C-ECHO-RQ",
			ErrorClass: "presentation-context",
		})
		return err
	}
	messageID, err := dimse.ReceiveCEcho(assoc, pc.ID)
	if err != nil {
		emitEchoAudit(assoc, auditSink, audit.Event{
			Kind:        audit.OperationFailed,
			Service:     "C-ECHO",
			SOPClassUID: dimse.VerificationSOPClassUID,
			Command:     "C-ECHO-RQ",
			ErrorClass:  "receive-command",
		})
		return err
	}
	emitEchoAudit(assoc, auditSink, audit.Event{
		Kind:        audit.CommandReceived,
		Service:     "C-ECHO",
		SOPClassUID: dimse.VerificationSOPClassUID,
		Command:     "C-ECHO-RQ",
	})
	_, _ = fmt.Fprintf(stdout, "C-ECHO received message-id=%d\n", messageID)
	if err := dimse.SendCEchoResponse(assoc, pc.ID, messageID, dimse.StatusSuccess); err != nil {
		emitEchoAudit(assoc, auditSink, audit.Event{
			Kind:        audit.OperationFailed,
			Service:     "C-ECHO",
			SOPClassUID: dimse.VerificationSOPClassUID,
			Command:     "C-ECHO-RSP",
			ErrorClass:  "send-response",
		})
		return err
	}
	emitEchoAudit(assoc, auditSink, audit.Event{
		Kind:        audit.ResponseSent,
		Service:     "C-ECHO",
		SOPClassUID: dimse.VerificationSOPClassUID,
		Command:     "C-ECHO-RSP",
		Status:      dimse.StatusSuccess,
		StatusSet:   true,
	})
	emitEchoAudit(assoc, auditSink, audit.Event{
		Kind:        audit.OperationSucceeded,
		Service:     "C-ECHO",
		SOPClassUID: dimse.VerificationSOPClassUID,
		Command:     "C-ECHO",
		Status:      dimse.StatusSuccess,
		StatusSet:   true,
	})
	_, _ = fmt.Fprintf(stdout, "C-ECHO response sent status=0x%04X\n", dimse.StatusSuccess)
	return waitForRelease(assoc)
}

func emitEchoAudit(assoc *ul.Association, sink audit.Sink, event audit.Event) {
	if event.Service == "" {
		event.Service = "C-ECHO"
	}
	if assoc != nil {
		event.LocalAETitle = assoc.CalledAETitle
		event.RemoteAETitle = assoc.CallingAETitle
		if assoc.Conn != nil && assoc.Conn.RemoteAddr() != nil {
			event.RemoteAddr = assoc.Conn.RemoteAddr().String()
		}
	}
	audit.Emit(associationContext(assoc), sink, event)
}

func associationContext(assoc *ul.Association) context.Context {
	if assoc == nil || assoc.Context == nil {
		return context.Background()
	}
	return assoc.Context
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
