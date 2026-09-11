package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"os/signal"
	"time"

	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Main starts the storescp DICOM storage SCP server.
func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	os.Exit(runWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithContext(context.Background(), args, stdout, stderr)
}

func runWithContext(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "storescp", err)
		return 1
	}
	if err := runServerWithContext(ctx, opts, stdout, stderr); err != nil {
		clidiag.Fprintln(stderr, "storescp", err)
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
	limits  storescpLimits
}

type storescpLimits struct {
	maxAssociations       int
	maxStores             int
	storeQueueDepth       int
	maxPDU                uint
	maxCommandBytes       int64
	maxDataSetBytes       int64
	maxElementBytes       int64
	maxElements           int
	maxSequenceDepth      int
	maxPixelDataBytes     int64
	maxPixelDataFragments int
	negotiationTimeout    time.Duration
	idleTimeout           time.Duration
	readProgressTimeout   time.Duration
	writeProgressTimeout  time.Duration
	releaseTimeout        time.Duration
	shutdownTimeout       time.Duration
}

const (
	defaultListenAddress      = "127.0.0.1:11112"
	defaultMaxAssociations    = 64
	defaultMaxStores          = 8
	defaultStoreQueueDepth    = 32
	defaultMaxDataSetBytes    = int64(1 << 30)
	defaultMaxElementBytes    = int64(64 << 20)
	defaultMaxPixelDataBytes  = int64(768 << 20)
	defaultMaxElements        = 100_000
	defaultMaxSequenceDepth   = 64
	defaultMaxPixelFragments  = 100_000
	statusOutOfResources      = 0xA700
	statusDataSetDoesNotMatch = 0xA900
	statusCannotUnderstand    = 0xC000
)

var (
	errUnsupportedDIMSECommand = errors.New("storescp: unsupported DIMSE command")
	errStoreQueueFull          = errors.New("storescp: C-STORE queue is full")
)

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		address: defaultListenAddress,
		aeTitle: "STORESCP",
		outDir:  ".",
		limits:  defaultStorescpLimits(),
	}

	fs := flag.NewFlagSet("storescp", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.address, "address", opts.address, "address to listen on")
	fs.StringVar(&opts.aeTitle, "aetitle", opts.aeTitle, "SCP AE title")
	fs.StringVar(&opts.outDir, "output", opts.outDir, "directory for received Part 10 files")
	fs.BoolVar(&opts.single, "single", false, "handle one association and exit")
	fs.IntVar(&opts.limits.maxAssociations, "max-associations", opts.limits.maxAssociations, "maximum concurrent negotiating and established associations")
	fs.IntVar(&opts.limits.maxStores, "max-stores", opts.limits.maxStores, "maximum C-STORE operations processed concurrently")
	fs.IntVar(&opts.limits.storeQueueDepth, "store-queue-depth", opts.limits.storeQueueDepth, "maximum C-STORE operations waiting for a processing slot")
	fs.UintVar(&opts.limits.maxPDU, "max-pdu-bytes", opts.limits.maxPDU, "maximum inbound PDU body bytes")
	fs.Int64Var(&opts.limits.maxCommandBytes, "max-command-bytes", opts.limits.maxCommandBytes, "maximum reassembled DIMSE command bytes")
	fs.Int64Var(&opts.limits.maxDataSetBytes, "max-dataset-bytes", opts.limits.maxDataSetBytes, "maximum encoded dataset bytes")
	fs.Int64Var(&opts.limits.maxElementBytes, "max-element-bytes", opts.limits.maxElementBytes, "maximum non-Pixel-Data element value bytes")
	fs.IntVar(&opts.limits.maxElements, "max-elements", opts.limits.maxElements, "maximum primitive elements per dataset")
	fs.IntVar(&opts.limits.maxSequenceDepth, "max-sequence-depth", opts.limits.maxSequenceDepth, "maximum combined sequence/item nesting depth")
	fs.Int64Var(&opts.limits.maxPixelDataBytes, "max-pixel-data-bytes", opts.limits.maxPixelDataBytes, "maximum native or cumulative encapsulated Pixel Data bytes")
	fs.IntVar(&opts.limits.maxPixelDataFragments, "max-pixel-fragments", opts.limits.maxPixelDataFragments, "maximum encapsulated Pixel Data fragments")
	fs.DurationVar(&opts.limits.negotiationTimeout, "negotiation-timeout", opts.limits.negotiationTimeout, "association negotiation timeout")
	fs.DurationVar(&opts.limits.idleTimeout, "idle-timeout", opts.limits.idleTimeout, "idle association timeout")
	fs.DurationVar(&opts.limits.readProgressTimeout, "read-progress-timeout", opts.limits.readProgressTimeout, "maximum time without inbound byte progress")
	fs.DurationVar(&opts.limits.writeProgressTimeout, "write-progress-timeout", opts.limits.writeProgressTimeout, "maximum time without outbound byte progress")
	fs.DurationVar(&opts.limits.releaseTimeout, "release-timeout", opts.limits.releaseTimeout, "association release timeout")
	fs.DurationVar(&opts.limits.shutdownTimeout, "shutdown-timeout", opts.limits.shutdownTimeout, "grace period before active associations are aborted")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nServe DICOM C-STORE requests, answer C-ECHO verification, and save received Part 10 files.\nUnsupported DIMSE commands abort only the affected association.\n\nFlags:\n", fs.Name())
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
	if err := validateStorescpLimits(opts.limits); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		fs.Usage()
		return opts, errUsage
	}
	return opts, nil
}

func defaultStorescpLimits() storescpLimits {
	return storescpLimits{
		maxAssociations:       defaultMaxAssociations,
		maxStores:             defaultMaxStores,
		storeQueueDepth:       defaultStoreQueueDepth,
		maxPDU:                uint(ul.DefaultMaxPDU),
		maxCommandBytes:       dimse.MaxCommandSetBytes,
		maxDataSetBytes:       defaultMaxDataSetBytes,
		maxElementBytes:       defaultMaxElementBytes,
		maxElements:           defaultMaxElements,
		maxSequenceDepth:      defaultMaxSequenceDepth,
		maxPixelDataBytes:     defaultMaxPixelDataBytes,
		maxPixelDataFragments: defaultMaxPixelFragments,
		negotiationTimeout:    15 * time.Second,
		idleTimeout:           2 * time.Minute,
		readProgressTimeout:   30 * time.Second,
		writeProgressTimeout:  30 * time.Second,
		releaseTimeout:        10 * time.Second,
		shutdownTimeout:       10 * time.Second,
	}
}

func validateStorescpLimits(limits storescpLimits) error {
	if limits.maxAssociations <= 0 || limits.maxStores <= 0 || limits.storeQueueDepth < 0 {
		return errors.New("-max-associations and -max-stores must be positive; -store-queue-depth must not be negative")
	}
	if limits.maxStores > limits.maxAssociations || limits.storeQueueDepth > limits.maxAssociations-limits.maxStores {
		return errors.New("-max-stores plus -store-queue-depth must not exceed -max-associations")
	}
	if limits.maxPDU == 0 || uint64(limits.maxPDU) > math.MaxUint32 {
		return errors.New("-max-pdu-bytes must be between 1 and 4294967295")
	}
	if limits.maxCommandBytes <= 0 || limits.maxDataSetBytes <= 0 || limits.maxElementBytes <= 0 ||
		limits.maxElements <= 0 || limits.maxSequenceDepth <= 0 || limits.maxPixelDataBytes <= 0 ||
		limits.maxPixelDataFragments <= 0 {
		return errors.New("all storescp byte, element, depth, and fragment limits must be positive")
	}
	if limits.maxElementBytes > limits.maxDataSetBytes || limits.maxPixelDataBytes > limits.maxDataSetBytes {
		return errors.New("element and Pixel Data limits must not exceed -max-dataset-bytes")
	}
	if limits.negotiationTimeout <= 0 || limits.idleTimeout <= 0 || limits.readProgressTimeout <= 0 ||
		limits.writeProgressTimeout <= 0 || limits.releaseTimeout <= 0 || limits.shutdownTimeout <= 0 {
		return errors.New("all storescp timeouts must be positive")
	}
	return nil
}

func runServer(opts options, stdout, stderr io.Writer) error {
	return runServerWithContext(context.Background(), opts, stdout, stderr)
}

func runServerWithContext(ctx context.Context, opts options, stdout, stderr io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := os.MkdirAll(opts.outDir, 0o755); err != nil {
		return err
	}
	listener, err := ul.Listen(ul.ListenOptions{Address: opts.address})
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()
	_, _ = fmt.Fprintf(stdout, "storescp listening on %s\n", listener.Addr())

	stores := newStoreAdmission(opts.limits.maxStores, opts.limits.storeQueueDepth)
	serveCtx, cancelServe := context.WithCancel(context.Background())
	defer cancelServe()
	var singleResult chan error
	if opts.single {
		singleResult = make(chan error, 1)
	}
	maxAssociations := opts.limits.maxAssociations
	if opts.single {
		maxAssociations = 1
	}
	server, err := ul.NewAssociationServer(listener, ul.AssociationServerOptions{
		Accept:                    storescpAcceptOptions(opts),
		MaxConcurrentAssociations: maxAssociations,
		SaturationPolicy:          ul.SaturationReject,
		Handler: func(handlerCtx context.Context, assoc *ul.Association) error {
			handlerErr := handleAssociationWithRuntime(handlerCtx, assoc, opts.outDir, stdout, opts.limits, stores)
			if opts.single {
				singleResult <- handlerErr
				cancelServe()
			} else if handlerErr != nil && !errors.Is(handlerErr, context.Canceled) {
				clidiag.Fprintln(stderr, "storescp", handlerErr)
			}
			return handlerErr
		},
	})
	if err != nil {
		return err
	}

	serveFinished := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), opts.limits.shutdownTimeout)
			defer cancelShutdown()
			if shutdownErr := server.Shutdown(shutdownCtx); shutdownErr != nil && !errors.Is(shutdownErr, context.DeadlineExceeded) {
				clidiag.Fprintln(stderr, "storescp shutdown", shutdownErr)
			}
		case <-serveFinished:
		}
	}()
	serveErr := server.Serve(serveCtx)
	close(serveFinished)
	if opts.single {
		select {
		case handlerErr := <-singleResult:
			return handlerErr
		default:
		}
	}
	if ctx.Err() != nil {
		return nil
	}
	return serveErr
}

func storescpAcceptOptions(opts options) ul.AcceptOptions {
	return ul.AcceptOptions{
		AETitle:                   opts.aeTitle,
		MaxPDU:                    uint32(opts.limits.maxPDU),
		SupportedAbstractSyntaxes: acceptedAbstractSyntaxes(),
		SupportedTransferSyntaxes: supportedTransferSyntaxUIDs(),
		NegotiationTimeout:        opts.limits.negotiationTimeout,
		IdleTimeout:               opts.limits.idleTimeout,
		ReadProgressTimeout:       opts.limits.readProgressTimeout,
		WriteProgressTimeout:      opts.limits.writeProgressTimeout,
		ReleaseTimeout:            opts.limits.releaseTimeout,
	}
}

func handleAssociation(assoc *ul.Association, outDir string, stdout io.Writer) error {
	return handleAssociationWithLimits(assoc, outDir, stdout, defaultStorescpLimits())
}

func handleAssociationWithLimits(assoc *ul.Association, outDir string, stdout io.Writer, limits storescpLimits) error {
	return handleAssociationWithRuntime(context.Background(), assoc, outDir, stdout, limits, newStoreAdmission(limits.maxStores, limits.storeQueueDepth))
}

func handleAssociationWithRuntime(ctx context.Context, assoc *ul.Association, outDir string, stdout io.Writer, limits storescpLimits, stores *storeAdmission) error {
	defer func() { _ = assoc.Close() }()
	_, _ = fmt.Fprintf(stdout, "association accepted from %s\n", assoc.Conn.RemoteAddr())

	for {
		incoming, err := receiveCommandOrReleaseWithLimits(assoc, limits)
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
			if err := handleCStoreCommandWithRuntime(ctx, assoc, incoming, outDir, stdout, limits, stores); err != nil {
				return err
			}
		default:
			return handleUnsupportedDIMSECommand(assoc, field, stdout)
		}
	}
}

func handleUnsupportedDIMSECommand(assoc *ul.Association, field uint16, stdout io.Writer) error {
	command := dimseCommandName(field)
	_, _ = fmt.Fprintf(stdout, "unsupported DIMSE command %s (0x%04X); storescp supports only C-ECHO-RQ and C-STORE-RQ; aborting association\n", command, field)
	if err := assoc.Abort(ul.AbortReasonNotSpecified); err != nil {
		return fmt.Errorf("%w: %s (0x%04X); abort failed: %v", errUnsupportedDIMSECommand, command, field, err)
	}
	return fmt.Errorf("%w: %s (0x%04X); storescp supports only C-ECHO-RQ and C-STORE-RQ; association aborted", errUnsupportedDIMSECommand, command, field)
}

func dimseCommandName(field uint16) string {
	switch field {
	case dimse.CStoreRQ:
		return "C-STORE-RQ"
	case dimse.CStoreRSP:
		return "C-STORE-RSP"
	case dimse.CEchoRQ:
		return "C-ECHO-RQ"
	case dimse.CEchoRSP:
		return "C-ECHO-RSP"
	case dimse.CFindRQ:
		return "C-FIND-RQ"
	case dimse.CFindRSP:
		return "C-FIND-RSP"
	case dimse.CGetRQ:
		return "C-GET-RQ"
	case dimse.CGetRSP:
		return "C-GET-RSP"
	case dimse.CMoveRQ:
		return "C-MOVE-RQ"
	case dimse.CMoveRSP:
		return "C-MOVE-RSP"
	case dimse.NActionRQ:
		return "N-ACTION-RQ"
	case dimse.NActionRSP:
		return "N-ACTION-RSP"
	case dimse.NEventReportRQ:
		return "N-EVENT-REPORT-RQ"
	case dimse.NEventReportRSP:
		return "N-EVENT-REPORT-RSP"
	case dimse.NGetRQ:
		return "N-GET-RQ"
	case dimse.NGetRSP:
		return "N-GET-RSP"
	case dimse.NSetRQ:
		return "N-SET-RQ"
	case dimse.NSetRSP:
		return "N-SET-RSP"
	case dimse.NCreateRQ:
		return "N-CREATE-RQ"
	case dimse.NCreateRSP:
		return "N-CREATE-RSP"
	case dimse.NDeleteRQ:
		return "N-DELETE-RQ"
	case dimse.NDeleteRSP:
		return "N-DELETE-RSP"
	default:
		return "unknown"
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
	return handleCStoreCommandWithLimits(assoc, incoming, outDir, stdout, defaultStorescpLimits())
}

func handleCStoreCommandWithLimits(assoc *ul.Association, incoming incomingCommand, outDir string, stdout io.Writer, limits storescpLimits) error {
	return handleCStoreCommandWithRuntime(context.Background(), assoc, incoming, outDir, stdout, limits, newStoreAdmission(limits.maxStores, limits.storeQueueDepth))
}

func handleCStoreCommandWithRuntime(ctx context.Context, assoc *ul.Association, incoming incomingCommand, outDir string, stdout io.Writer, limits storescpLimits, stores *storeAdmission) error {
	req, err := dimse.ParseCStoreRequest(incoming.command)
	if err != nil {
		return errors.New("storescp: invalid C-STORE command")
	}
	pc, err := acceptedContextByID(assoc, incoming.pcID)
	if err != nil {
		return err
	}
	syntax, ok := transfer.DefaultRegistry.Get(pc.TransferSyntaxUID)
	if !ok {
		return transfer.ErrUnknownTransferSyntax
	}

	status := uint16(dimse.StatusSuccess)
	var releaseStore func()
	if stores != nil {
		releaseStore, err = stores.acquire(ctx)
	}
	if errors.Is(err, errStoreQueueFull) {
		status = statusOutOfResources
		err = discardDataSetWithLimits(assoc, incoming, limits)
		_, _ = fmt.Fprintln(stdout, "store rejected: queue full")
	} else if err != nil {
		return err
	} else {
		if releaseStore != nil {
			defer releaseStore()
		}
		var dataset *object.Object
		dataset, err = receiveDataSetWithLimits(assoc, incoming, syntax, limits)
		if err != nil {
			status = dataSetErrorStatus(err)
		} else if validateErr := netstore.ValidateCStoreDataSet(req.AffectedSOPClassUID, req.AffectedSOPInstanceUID, pc, dataset); validateErr != nil {
			status = statusDataSetDoesNotMatch
		} else {
			_, saveErr := netstore.SavePart10WithContext(ctx, outDir, dataset, syntax)
			if saveErr != nil {
				status = statusOutOfResources
				_, _ = fmt.Fprintf(stdout, "store failed: %v\n", saveErr)
			} else {
				_, _ = fmt.Fprintln(stdout, "stored DICOM instance")
			}
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

type storeAdmission struct {
	admitted chan struct{}
	active   chan struct{}
}

func newStoreAdmission(maxActive, queueDepth int) *storeAdmission {
	return &storeAdmission{
		admitted: make(chan struct{}, maxActive+queueDepth),
		active:   make(chan struct{}, maxActive),
	}
}

func (a *storeAdmission) acquire(ctx context.Context) (func(), error) {
	if a == nil {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	select {
	case a.admitted <- struct{}{}:
	default:
		return nil, errStoreQueueFull
	}
	select {
	case a.active <- struct{}{}:
		return func() {
			<-a.active
			<-a.admitted
		}, nil
	case <-ctx.Done():
		<-a.admitted
		return nil, ctx.Err()
	}
}

type incomingCommand struct {
	release    bool
	pcID       byte
	command    *object.Object
	dataPrefix []byte
	dataLast   bool
	dataErr    error
}

func receiveCommandOrRelease(assoc *ul.Association) (incomingCommand, error) {
	return receiveCommandOrReleaseWithLimits(assoc, defaultStorescpLimits())
}

func receiveCommandOrReleaseWithLimits(assoc *ul.Association, limits storescpLimits) (incomingCommand, error) {
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
					if int64(command.Len()) > limits.maxCommandBytes-int64(len(value.Data)) {
						return incomingCommand{}, dimse.ErrCommandSetTooLarge
					}
					_, _ = command.Write(value.Data)
					if value.IsLast {
						commandDone = true
					}
					continue
				}
				if !commandDone {
					return incomingCommand{}, errors.New("dataset PDV received before complete command")
				}
				if out.dataErr == nil {
					if int64(len(out.dataPrefix)) > limits.maxDataSetBytes-int64(len(value.Data)) {
						out.dataErr = parser.ErrMaxTotalBytesExceeded
					} else {
						out.dataPrefix = append(out.dataPrefix, value.Data...)
					}
				}
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
	return receiveDataSetWithLimits(assoc, incoming, syntax, defaultStorescpLimits())
}

func receiveDataSetWithLimits(assoc *ul.Association, incoming incomingCommand, syntax transfer.Syntax, limits storescpLimits) (*object.Object, error) {
	if incoming.dataErr != nil {
		return nil, incoming.dataErr
	}
	readOptions := object.ReadFileOptions{
		MaxElementBytes:   limits.maxElementBytes,
		MaxTotalBytes:     limits.maxDataSetBytes,
		MaxElements:       limits.maxElements,
		MaxSequenceDepth:  limits.maxSequenceDepth,
		MaxPixelDataBytes: limits.maxPixelDataBytes,
		MaxFragments:      limits.maxPixelDataFragments,
	}
	if incoming.dataLast {
		return object.ReadDataSetWithOptions(bytes.NewReader(incoming.dataPrefix), syntax, readOptions)
	}
	if len(incoming.dataPrefix) == 0 {
		return object.ReadDataSetWithOptions(dimse.NewPDataReader(assoc, incoming.pcID), syntax, readOptions)
	}
	reader := io.MultiReader(bytes.NewReader(incoming.dataPrefix), dimse.NewPDataReader(assoc, incoming.pcID))
	return object.ReadDataSetWithOptions(reader, syntax, readOptions)
}

func discardDataSetWithLimits(assoc *ul.Association, incoming incomingCommand, limits storescpLimits) error {
	if incoming.dataErr != nil {
		return incoming.dataErr
	}
	if incoming.dataLast {
		return nil
	}
	remaining := limits.maxDataSetBytes - int64(len(incoming.dataPrefix))
	if remaining < 0 {
		return parser.ErrMaxTotalBytesExceeded
	}
	reader := dimse.NewPDataReader(assoc, incoming.pcID)
	if remaining > 0 {
		if _, err := io.CopyN(io.Discard, reader, remaining); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	var extra [1]byte
	n, err := reader.Read(extra[:])
	if n > 0 {
		return parser.ErrMaxTotalBytesExceeded
	}
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}

func dataSetErrorStatus(err error) uint16 {
	if errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxDepthExceeded) ||
		errors.Is(err, parser.ErrMaxPixelDataBytesExceeded) ||
		errors.Is(err, parser.ErrMaxFragmentsExceeded) {
		return statusOutOfResources
	}
	return statusCannotUnderstand
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
	uids = append(uids, dimse.DefaultStorageSOPClassUIDs()...)
	return uids
}

func supportedTransferSyntaxUIDs() []string {
	return []string{
		transfer.ImplicitVRLittleEndian.UID,
		transfer.ExplicitVRLittleEndian.UID,
	}
}
