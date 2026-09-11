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
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/internal/clisignal"
	"github.com/ThalesMMS/dicom-go/internal/dcmdump"
	"github.com/ThalesMMS/dicom-go/internal/dimsecli"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func main() {
	ctx, stop := clisignal.NotifyInterruptContext(context.Background())
	defer stop()
	os.Exit(runWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithContext(context.Background(), args, stdout, stderr)
}

func runWithContext(parent context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "findscu", err)
		return 1
	}

	if err := runFind(parent, opts, stdout, stderr); err != nil {
		clidiag.Fprintln(stderr, "findscu", err)
		if dimsecli.IsCanceled(parent, err) {
			return dimsecli.ExitCanceled
		}
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	model                dimse.QueryRetrieveModel
	host                 string
	port                 int
	calledAE             string
	callingAE            string
	level                string
	timeout              time.Duration
	maxResults           int
	keys                 []string
	output               string
	jsonBulkData         string
	jsonMaxResponseBytes int64
	jsonSummary          bool
}

const (
	defaultTimeout              = 10 * time.Second
	defaultReleaseTimeout       = 5 * time.Second
	defaultJSONMaxResponseBytes = int64(16 << 20)
	maxJSONResponseBytes        = int64(256 << 20)
	findOutputText              = "text"
	findOutputJSONL             = "jsonl"
	jsonBulkDataOmit            = "omit"
	jsonBulkDataInline          = "inline"
)

type stringSliceFlag []string

func (s *stringSliceFlag) String() string {
	return strings.Join(*s, ",")
}

func (s *stringSliceFlag) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func dumpIdentifier(w io.Writer, obj *object.Object, syntax transfer.Syntax) error {
	if obj == nil {
		return nil
	}
	formatter := dcmdump.NewFormatter(w, 200)
	return formatter.DumpObject(obj, syntax)
}

func formatFinalStatus(status uint16, errorComment string) string {
	if errorComment != "" {
		return fmt.Sprintf("Final status=0x%04X (%s)", status, errorComment)
	}
	return fmt.Sprintf("Final status=0x%04X", status)
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	opts := options{
		model:                dimse.QueryRetrieveModelStudyRoot,
		host:                 "localhost",
		port:                 11112,
		calledAE:             "ANY-SCP",
		callingAE:            "FINDSCU",
		level:                "study",
		timeout:              defaultTimeout,
		maxResults:           0,
		output:               findOutputText,
		jsonBulkData:         jsonBulkDataOmit,
		jsonMaxResponseBytes: defaultJSONMaxResponseBytes,
		jsonSummary:          true,
	}

	fs := flag.NewFlagSet("findscu", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modelName := "study-root"
	fs.StringVar(&modelName, "model", modelName, "information model: study-root or patient-root")
	fs.StringVar(&opts.callingAE, "calling", opts.callingAE, "calling AE title")
	fs.StringVar(&opts.calledAE, "called", opts.calledAE, "called AE title")
	fs.StringVar(&opts.host, "host", opts.host, "SCP host name or IP address")
	fs.IntVar(&opts.port, "port", opts.port, "SCP TCP port")
	fs.StringVar(&opts.level, "level", opts.level, "query retrieve level: patient, study, series, or image")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "dial/find timeout")
	fs.IntVar(&opts.maxResults, "max-results", opts.maxResults, "stop after printing N pending matches (0 means no limit)")
	fs.StringVar(&opts.output, "output", opts.output, "output format: text or jsonl")
	fs.StringVar(&opts.jsonBulkData, "json-bulk-data", opts.jsonBulkData, "JSONL bulk data policy: omit or inline")
	fs.Int64Var(&opts.jsonMaxResponseBytes, "json-max-response-bytes", opts.jsonMaxResponseBytes, "maximum bytes per JSONL response dataset")
	fs.BoolVar(&opts.jsonSummary, "json-summary", opts.jsonSummary, "write JSONL final summary to stderr")

	var keys stringSliceFlag
	fs.Var(&keys, "k", "query key in the form Keyword=value (repeatable)")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nSend a DICOM C-FIND request using Study Root or Patient Root.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
		_, _ = fmt.Fprintln(stderr, "\nExamples:\n  findscu -host 127.0.0.1 -port 11112 -called ORTHANC -calling FINDSCU -level study -k PatientID=1234")
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

	opts.keys = append([]string(nil), keys...)
	model, err := dimse.ParseQueryRetrieveModel(modelName)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "-model must be 'study-root' or 'patient-root'")
		fs.Usage()
		return opts, errUsage
	}
	opts.model = model

	if opts.port <= 0 || opts.port > 65535 {
		_, _ = fmt.Fprintln(stderr, "-port must be between 1 and 65535")
		fs.Usage()
		return opts, errUsage
	}
	level := strings.ToLower(strings.TrimSpace(opts.level))
	if _, err := dimse.QueryRetrieveRequiredKeys(opts.model, strings.ToUpper(level)); err != nil {
		_, _ = fmt.Fprintf(stderr, "-level %q is invalid for -model %s\n", level, modelName)
		fs.Usage()
		return opts, errUsage
	}
	opts.level = level
	if opts.timeout <= 0 {
		_, _ = fmt.Fprintln(stderr, "-timeout must be positive")
		fs.Usage()
		return opts, errUsage
	}
	if opts.maxResults < 0 {
		_, _ = fmt.Fprintln(stderr, "-max-results must be >= 0")
		fs.Usage()
		return opts, errUsage
	}
	opts.output = strings.ToLower(strings.TrimSpace(opts.output))
	if opts.output != findOutputText && opts.output != findOutputJSONL {
		_, _ = fmt.Fprintln(stderr, "-output must be 'text' or 'jsonl'")
		fs.Usage()
		return opts, errUsage
	}
	opts.jsonBulkData = strings.ToLower(strings.TrimSpace(opts.jsonBulkData))
	if opts.jsonBulkData != jsonBulkDataOmit && opts.jsonBulkData != jsonBulkDataInline {
		_, _ = fmt.Fprintln(stderr, "-json-bulk-data must be 'omit' or 'inline'")
		fs.Usage()
		return opts, errUsage
	}
	if opts.jsonMaxResponseBytes <= 0 || opts.jsonMaxResponseBytes > maxJSONResponseBytes {
		_, _ = fmt.Fprintf(stderr, "-json-max-response-bytes must be between 1 and %d\n", maxJSONResponseBytes)
		fs.Usage()
		return opts, errUsage
	}
	for _, kv := range opts.keys {
		if !strings.Contains(kv, "=") {
			_, _ = fmt.Fprintf(stderr, "-k must be Keyword=value, got %q\n", kv)
			fs.Usage()
			return opts, errUsage
		}
	}

	return opts, nil
}

func runFind(parent context.Context, opts options, stdout, stderr io.Writer) error {
	if opts.output == "" {
		opts.output = findOutputText
	}
	if opts.jsonBulkData == "" {
		opts.jsonBulkData = jsonBulkDataOmit
	}
	if opts.jsonMaxResponseBytes == 0 {
		opts.jsonMaxResponseBytes = defaultJSONMaxResponseBytes
	}
	address := net.JoinHostPort(opts.host, strconv.Itoa(opts.port))

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, opts.timeout)
	defer cancel()

	findSOPClassUID := dimse.StudyRootFindSOPClassUID
	findContext := dimse.StudyRootFindPresentationContext()
	modelLabel := "study-root"
	if opts.model == dimse.QueryRetrieveModelPatientRoot {
		findSOPClassUID = dimse.PatientRootFindSOPClassUID
		findContext = dimse.PatientRootFindPresentationContext()
		modelLabel = "patient-root"
	}
	assoc, err := ul.Dial(address, ul.DialOptions{
		CalledAETitle:  opts.calledAE,
		CallingAETitle: opts.callingAE,
		Context:        ctx,
		Contexts:       []ul.PresentationContext{findContext},
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

	pc, err := dimse.AcceptedContextForSOPClass(assoc, findSOPClassUID)
	if err != nil {
		return fmt.Errorf("no accepted %s FIND presentation context: %w", modelLabel, err)
	}

	queryKeys := make(map[string]string)
	for _, kv := range opts.keys {
		k, v, _ := strings.Cut(kv, "=")
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if k == "" {
			return fmt.Errorf("invalid -k %q: empty keyword", kv)
		}
		queryKeys[k] = v
	}

	var identifierElements []core.Element
	if opts.model == dimse.QueryRetrieveModelPatientRoot {
		switch opts.level {
		case "patient":
			identifierElements, err = dimse.BuildPatientRootPatientFindKeys(queryKeys)
		case "study":
			identifierElements, err = dimse.BuildPatientRootStudyFindKeys(queryKeys)
		case "series":
			identifierElements, err = dimse.BuildPatientRootSeriesFindKeys(queryKeys)
		default:
			identifierElements, err = dimse.BuildPatientRootImageFindKeys(queryKeys)
		}
	} else {
		switch opts.level {
		case "study":
			identifierElements, err = dimse.BuildStudyRootStudyFindKeys(queryKeys)
		case "series":
			identifierElements, err = dimse.BuildStudyRootSeriesFindKeys(queryKeys)
		default:
			identifierElements, err = dimse.BuildStudyRootImageFindKeys(queryKeys)
		}
	}
	if err != nil {
		return err
	}
	identifier := object.FromElements(identifierElements, std.Dictionary)

	req := dimse.CFindRequest{
		MessageID:           1,
		AffectedSOPClassUID: findSOPClassUID,
		Priority:            0,
	}

	identifierSyntax := transfer.ImplicitVRLittleEndian
	if pc.TransferSyntaxUID != "" {
		identifierSyntax = transfer.Syntax{UID: pc.TransferSyntaxUID}
	}

	if err := writeFindPreamble(stdout, modelLabel, opts); err != nil {
		return err
	}
	sessionOptions := findSessionOptions(opts)
	session, err := dimse.NewAsyncSession(assoc, sessionOptions)
	if err != nil {
		return err
	}
	sessionReleased := false
	defer func() {
		if !sessionReleased {
			_ = session.Close()
		}
	}()
	operation, err := session.StartCFind(ctx, pc.ID, req, identifier)
	if err != nil {
		return err
	}
	reader := dimsecli.NewOperationReader(ctx, operation, defaultReleaseTimeout)
	defer reader.Close()

	printed := 0
	suppressIdentifiers := false
	var finalStatus uint16
	var finalComment string

	for {
		message, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return nextErr
		}
		response, parseErr := dimse.ParseCFindResponse(message.Command)
		if parseErr != nil {
			return parseErr
		}
		r := dimse.FindResult{Response: response, Identifier: message.DataSet}
		finalStatus = r.Status()
		finalComment = r.ErrorComment()
		if dimse.CategorizeCFindStatus(finalStatus) == dimse.CFindStatusPending {
			if !suppressIdentifiers {
				printed++
				if err := writeFindIdentifier(stdout, r.Identifier, identifierSyntax, opts); err != nil {
					return err
				}
				if opts.maxResults > 0 && printed >= opts.maxResults {
					suppressIdentifiers = true
				}
			}
		}
	}

	if err := writeFindFinal(stdout, stderr, modelLabel, printed, finalStatus, finalComment, opts); err != nil {
		return err
	}
	var operationErr error
	if errors.Is(reader.Cause(), context.DeadlineExceeded) {
		operationErr = reader.Cause()
	} else if finalStatus == dimse.CFindStatusCancel || errors.Is(reader.Cause(), context.Canceled) {
		operationErr = context.Canceled
	} else if reader.Cause() != nil {
		operationErr = reader.Cause()
	} else if category := dimse.CategorizeCFindStatus(finalStatus); category != dimse.CFindStatusSuccess {
		operationErr = dimse.CFindStatusError(&dimse.CFindResponse{Status: finalStatus, ErrorComment: finalComment})
	}

	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), defaultReleaseTimeout)
	defer cancelRelease()
	if err := session.Release(releaseCtx, dimse.AsyncReleaseWait); err != nil {
		return err
	}
	sessionReleased = true
	released = true
	return operationErr
}

func findSessionOptions(opts options) dimse.AsyncSessionOptions {
	sessionOptions := dimse.AsyncSessionOptions{CancelDrainTimeout: defaultReleaseTimeout}
	if opts.output != findOutputJSONL {
		return sessionOptions
	}
	sessionOptions.ResponseQueueDepth = 4
	sessionOptions.MaxDataSetBytes = opts.jsonMaxResponseBytes
	sessionOptions.MaxQueuedMessageBytes = 4*opts.jsonMaxResponseBytes + 4<<20
	return sessionOptions
}

func writeFindPreamble(stdout io.Writer, modelLabel string, opts options) error {
	if opts.output != findOutputText {
		return nil
	}
	_, err := fmt.Fprintf(stdout, "Information model=%s\n", modelLabel)
	return err
}

func writeFindFinal(stdout, stderr io.Writer, modelLabel string, matches int, status uint16, errorComment string, opts options) error {
	if opts.output == findOutputText {
		_, err := fmt.Fprintln(stdout, formatFinalStatus(status, errorComment))
		return err
	}
	if !opts.jsonSummary {
		return nil
	}
	_, err := fmt.Fprintf(stderr, "findscu: summary model=%s matches=%d status=0x%04X\n", modelLabel, matches, status)
	return err
}

func writeFindIdentifier(w io.Writer, identifier *object.Object, syntax transfer.Syntax, opts options) error {
	if opts.output == findOutputText {
		if err := dumpIdentifier(w, identifier, syntax); err != nil {
			return err
		}
		_, err := fmt.Fprintln(w)
		return err
	}
	jsonOptions := dicomjson.Options{OmitGroupLength: true, Pretty: false}
	if opts.jsonBulkData == jsonBulkDataOmit {
		jsonOptions.BulkDataURIFunc = func(tag core.Tag, _ core.VR, _ []byte) string {
			return "urn:dicom-go:bulk-data-omitted:" + tag.HexString()
		}
		jsonOptions.PixelDataBulkDataURIFunc = func(tag core.Tag, _ core.VR, _ func() (io.ReadCloser, error)) (string, error) {
			return "urn:dicom-go:bulk-data-omitted:" + tag.HexString(), nil
		}
	}
	encoded, err := dicomjson.Marshal(identifier, jsonOptions)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = w.Write(encoded)
	return err
}
