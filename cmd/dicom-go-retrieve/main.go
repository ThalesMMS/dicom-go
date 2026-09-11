package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/internal/clisignal"
	"github.com/ThalesMMS/dicom-go/internal/dimsecli"
	"github.com/ThalesMMS/dicom-go/internal/netstore"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const (
	studyRootMoveUID           = dimse.StudyRootMoveSOPClassUID
	retrieveMethodMove         = "move"
	retrieveMethodGet          = "get"
	defaultRetrieveCancelDrain = 5 * time.Second
)

var errRetrieveUsage = errors.New("usage error")

type retrieveOptions struct {
	method          string
	model           dimse.QueryRetrieveModel
	remote          string
	callingAET      string
	calledAET       string
	moveDestination string
	outputDir       string
	level           string
	patientID       string
	studyUID        string
	seriesUID       string
	sopInstanceUID  string
	timeout         time.Duration
}

func main() {
	ctx, stop := clisignal.NotifyInterruptContext(context.Background())
	defer stop()
	os.Exit(runWithContext(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithContext(context.Background(), args, stdout, stderr)
}

func runWithContext(parent context.Context, args []string, stdout, stderr io.Writer) int {
	opts, err := parseRetrieveArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	identifierObj, err := buildMoveIdentifier(opts)
	if err != nil {
		fmt.Fprintf(stderr, "dicom-go-retrieve: usage: %v\n", err)
		return 2
	}
	if opts.method == retrieveMethodGet {
		opts.outputDir = filepath.Clean(opts.outputDir)
		if err := os.MkdirAll(opts.outputDir, 0o755); err != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
			return 1
		}
	}

	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(parent, opts.timeout)
	defer cancel()
	sopClassUID, contexts, roles := retrieveNegotiation(opts.method, opts.model)
	if opts.method == retrieveMethodGet {
		stdout = &synchronizedWriter{writer: stdout}
		stderr = &synchronizedWriter{writer: stderr}
	}
	_, _ = fmt.Fprintf(stdout, "Information model=%s method=%s\n", retrieveModelLabel(opts.model), opts.method)

	assoc, err := dimse.DialRetrieveSCU(ctx, dimse.RetrieveDialOptions{
		Address:        opts.remote,
		CallingAETitle: opts.callingAET,
		CalledAETitle:  opts.calledAET,
		Contexts:       contexts,
		RoleSelections: roles,
	})
	if err != nil {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		if dimsecli.IsCanceled(parent, err) {
			return dimsecli.ExitCanceled
		}
		return 1
	}
	defer func() { _ = assoc.Close() }()

	pc, err := dimse.AcceptedContextForSOPClass(assoc, sopClassUID)
	if err != nil {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		return 1
	}
	sessionOptions := dimse.AsyncSessionOptions{CancelDrainTimeout: defaultRetrieveCancelDrain}
	if opts.method == retrieveMethodGet {
		sessionOptions.Handlers = map[uint16]dimse.AsyncRequestHandler{
			dimse.CStoreRQ: newCGetStoreHandler(assoc, opts, stdout, stderr),
		}
	}
	session, err := dimse.NewAsyncSession(assoc, sessionOptions)
	if err != nil {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		return 1
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), opts.timeout)
		defer releaseCancel()
		if err := session.Release(releaseCtx, dimse.AsyncReleaseWait); err != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
			_ = session.Close()
		}
	}()

	if opts.method == retrieveMethodGet {
		return runGet(ctx, session, pc.ID, opts, identifierObj, stdout, stderr)
	}
	return runMove(ctx, session, pc.ID, opts, identifierObj, stdout, stderr)
}

func retrieveNegotiation(method string, model dimse.QueryRetrieveModel) (string, []ul.PresentationContext, []ul.RoleSelectionItem) {
	if method != retrieveMethodGet {
		if model == dimse.QueryRetrieveModelPatientRoot {
			return dimse.PatientRootMoveSOPClassUID, []ul.PresentationContext{dimse.PatientRootMovePresentationContext()}, nil
		}
		return dimse.StudyRootMoveSOPClassUID, []ul.PresentationContext{dimse.StudyRootMovePresentationContext()}, nil
	}
	getSOPClassUID := dimse.StudyRootGetSOPClassUID
	getContext := dimse.StudyRootGetPresentationContext()
	if model == dimse.QueryRetrieveModelPatientRoot {
		getSOPClassUID = dimse.PatientRootGetSOPClassUID
		getContext = dimse.PatientRootGetPresentationContext()
	}
	contexts := []ul.PresentationContext{getContext}
	storageUIDs := dimse.DefaultStorageSOPClassUIDs()
	roles := make([]ul.RoleSelectionItem, 0, len(storageUIDs))
	for _, sopClassUID := range storageUIDs {
		contexts = append(contexts, ul.PresentationContext{
			AbstractSyntaxUID: sopClassUID,
			TransferSyntaxUIDs: []string{
				transfer.ImplicitVRLittleEndian.UID,
				transfer.ExplicitVRLittleEndian.UID,
			},
		})
		roles = append(roles, ul.RoleSelectionItem{SopClassUID: sopClassUID, SCPRole: true})
	}
	return getSOPClassUID, contexts, roles
}

func runMove(ctx context.Context, session *dimse.AsyncSession, pcID byte, opts retrieveOptions, identifier *object.Object, stdout, stderr io.Writer) int {
	sopClassUID, _, _ := retrieveNegotiation(retrieveMethodMove, opts.model)
	operation, err := session.StartCMove(ctx, pcID, dimse.CMoveRequest{
		AffectedSOPClassUID: sopClassUID,
		MessageID:           1,
		Priority:            dimse.PriorityMedium,
		MoveDestination:     opts.moveDestination,
	}, identifier)
	if err != nil {
		return printRetrieveOperationError(ctx, ctx.Err(), err, stderr)
	}
	reader := dimsecli.NewOperationReader(ctx, operation, defaultRetrieveCancelDrain)
	defer reader.Close()
	var final *dimse.CMoveResponse
	for {
		message, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return printRetrieveOperationError(ctx, reader.Cause(), nextErr, stderr)
		}
		response, parseErr := dimse.ParseCMoveResponse(message.Command)
		if parseErr != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve", parseErr)
			return 1
		}
		response.Identifier = message.DataSet
		if dimse.ClassifyCMoveStatus(response.Status) != dimse.CMoveStatusPending {
			final = response
			continue
		}
		printRetrieveProgress(stdout, "C-MOVE", response.Status,
			response.NumberOfRemainingSuboperationsOrNil,
			response.NumberOfCompletedSuboperationsOrNil,
			response.NumberOfFailedSuboperationsOrNil,
			response.NumberOfWarningSuboperationsOrNil,
		)
	}
	if final == nil {
		fmt.Fprintln(stderr, "dicom-go-retrieve: network: C-MOVE returned no final response")
		return 1
	}
	exitCode := printRetrieveFinal(stdout, "C-MOVE", final.Status,
		final.NumberOfRemainingSuboperationsOrNil,
		final.NumberOfCompletedSuboperationsOrNil,
		final.NumberOfFailedSuboperationsOrNil,
		final.NumberOfWarningSuboperationsOrNil,
	)
	if reader.Cause() != nil && !dimsecli.IsCanceled(ctx, reader.Cause()) {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", reader.Cause())
		return 1
	}
	return exitCode
}

func newCGetStoreHandler(assoc *ul.Association, opts retrieveOptions, stdout, stderr io.Writer) dimse.AsyncRequestHandler {
	return func(ctx context.Context, session *dimse.AsyncSession, message dimse.AsyncMessage) error {
		request, err := dimse.ParseCStoreRequest(message.Command)
		status := dimse.StatusSuccess
		if err != nil {
			err = errors.New("invalid C-STORE command")
			clidiag.Fprintln(stderr, "dicom-go-retrieve C-GET store", err)
			return err
		}
		pc, err := dimse.AcceptedContextByID(assoc, message.PresentationContextID)
		if err != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve C-GET store", err)
			status = dimse.StatusCStoreCannotUnderstand
		}
		if status == dimse.StatusSuccess && message.DataSet == nil {
			err = errors.New("missing C-STORE dataset")
			clidiag.Fprintln(stderr, "dicom-go-retrieve C-GET store", err)
			status = dimse.StatusCStoreCannotUnderstand
		}
		if status == dimse.StatusSuccess {
			err = netstore.ValidateCStoreDataSet(request.AffectedSOPClassUID, request.AffectedSOPInstanceUID, pc, message.DataSet)
		}
		if status == dimse.StatusSuccess && err != nil {
			status = dimse.StatusCStoreDataSetDoesNotMatch
		}
		if status == dimse.StatusSuccess {
			syntax, syntaxErr := dimse.TransferSyntaxForAcceptedContext(pc)
			if syntaxErr != nil {
				err = syntaxErr
				status = dimse.StatusCStoreCannotUnderstand
			} else {
				_, saveErr := netstore.SavePart10WithContext(ctx, opts.outputDir, message.DataSet, syntax)
				if saveErr != nil {
					err = saveErr
					status = dimse.StatusCStoreOutOfResources
				} else {
					_, _ = fmt.Fprintln(stdout, "C-GET stored DICOM instance")
				}
			}
		}
		if err != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve C-GET store", err)
		}
		return session.Respond(ctx, message, (dimse.CStoreResponse{
			AffectedSOPClassUID:    request.AffectedSOPClassUID,
			AffectedSOPInstanceUID: request.AffectedSOPInstanceUID,
			Status:                 status,
		}).CommandSet(), nil)
	}
}

func runGet(ctx context.Context, session *dimse.AsyncSession, pcID byte, opts retrieveOptions, identifier *object.Object, stdout, stderr io.Writer) int {
	sopClassUID, _, _ := retrieveNegotiation(retrieveMethodGet, opts.model)
	operation, err := session.StartCGet(ctx, pcID, dimse.CGetRequest{
		AffectedSOPClassUID: sopClassUID,
		MessageID:           1,
		Priority:            dimse.PriorityMedium,
	}, identifier)
	if err != nil {
		return printRetrieveOperationError(ctx, ctx.Err(), err, stderr)
	}
	reader := dimsecli.NewOperationReader(ctx, operation, defaultRetrieveCancelDrain)
	defer reader.Close()
	var final *dimse.CGetResponse
	for {
		message, nextErr := reader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			return printRetrieveOperationError(ctx, reader.Cause(), nextErr, stderr)
		}
		response, parseErr := dimse.ParseCGetResponse(message.Command)
		if parseErr != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve", parseErr)
			return 1
		}
		response.Identifier = message.DataSet
		if dimse.ClassifyCMoveStatus(response.Status) != dimse.CMoveStatusPending {
			final = response
			continue
		}
		printRetrieveProgress(stdout, "C-GET", response.Status,
			response.NumberOfRemainingSuboperationsOrNil,
			response.NumberOfCompletedSuboperationsOrNil,
			response.NumberOfFailedSuboperationsOrNil,
			response.NumberOfWarningSuboperationsOrNil,
		)
	}
	if final == nil {
		fmt.Fprintln(stderr, "dicom-go-retrieve: network: C-GET returned no final response")
		return 1
	}
	exitCode := printRetrieveFinal(stdout, "C-GET", final.Status,
		final.NumberOfRemainingSuboperationsOrNil,
		final.NumberOfCompletedSuboperationsOrNil,
		final.NumberOfFailedSuboperationsOrNil,
		final.NumberOfWarningSuboperationsOrNil,
	)
	if reader.Cause() != nil && !dimsecli.IsCanceled(ctx, reader.Cause()) {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", reader.Cause())
		return 1
	}
	return exitCode
}

func printRetrieveOperationError(ctx context.Context, cause, err error, stderr io.Writer) int {
	if dimsecli.IsCanceled(ctx, cause) {
		fmt.Fprintln(stderr, "dicom-go-retrieve: canceled")
		return dimsecli.ExitCanceled
	}
	clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
	return 1
}

type synchronizedWriter struct {
	mu     sync.Mutex
	writer io.Writer
}

func (w *synchronizedWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.Write(data)
}

func printRetrieveProgress(out io.Writer, method string, status uint16, remaining, completed, failed, warning *uint16) {
	_, _ = fmt.Fprintf(out, "%s progress status=0x%04X remaining=%d completed=%d warning=%d failed=%d\n",
		method, status, retrieveCount(remaining), retrieveCount(completed), retrieveCount(warning), retrieveCount(failed))
}

func printRetrieveFinal(out io.Writer, method string, status uint16, remaining, completed, failed, warning *uint16) int {
	outcome := "failed"
	exitCode := 1
	switch dimse.ClassifyCMoveStatus(status) {
	case dimse.CMoveStatusSuccess:
		outcome, exitCode = "completed", 0
	case dimse.CMoveStatusWarning:
		outcome, exitCode = "warning", 0
	case dimse.CMoveStatusCancel:
		outcome, exitCode = "canceled", dimsecli.ExitCanceled
	}
	_, _ = fmt.Fprintf(out, "%s %s with status=0x%04X remaining=%d completed=%d warning=%d failed=%d\n",
		method, outcome, status, retrieveCount(remaining), retrieveCount(completed), retrieveCount(warning), retrieveCount(failed))
	return exitCode
}

func retrieveCount(value *uint16) uint16 {
	if value == nil {
		return 0
	}
	return *value
}

func parseRetrieveArgs(args []string, stderr io.Writer) (retrieveOptions, error) {
	opts := retrieveOptions{
		method:          retrieveMethodMove,
		model:           dimse.QueryRetrieveModelStudyRoot,
		remote:          "127.0.0.1:4242",
		callingAET:      "DICOMGO",
		calledAET:       "ORTHANC",
		moveDestination: "DICOMSTORE",
		outputDir:       ".",
		level:           "STUDY",
		timeout:         30 * time.Second,
	}

	fs := flag.NewFlagSet("dicom-go-retrieve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	modelName := "study-root"
	fs.StringVar(&opts.method, "method", opts.method, "Retrieve method: move or get")
	fs.StringVar(&modelName, "model", modelName, "information model: study-root or patient-root")
	fs.StringVar(&opts.remote, "remote", opts.remote, "Remote DICOM address host:port")
	fs.StringVar(&opts.callingAET, "calling-aet", opts.callingAET, "Calling AE title")
	fs.StringVar(&opts.calledAET, "called-aet", opts.calledAET, "Called AE title")
	fs.StringVar(&opts.moveDestination, "move-destination", opts.moveDestination, "C-MOVE destination AE title")
	fs.StringVar(&opts.outputDir, "output", opts.outputDir, "Directory for Part 10 files received by C-GET")
	fs.StringVar(&opts.level, "level", opts.level, "QueryRetrieveLevel: PATIENT, STUDY, SERIES, or IMAGE (model-dependent)")
	fs.StringVar(&opts.patientID, "patient-id", "", "PatientID required by Patient Root retrieve levels")
	fs.StringVar(&opts.studyUID, "study-uid", "", "StudyInstanceUID to retrieve")
	fs.StringVar(&opts.seriesUID, "series-uid", "", "SeriesInstanceUID to retrieve for -level=SERIES or -level=IMAGE")
	fs.StringVar(&opts.sopInstanceUID, "sop-instance-uid", "", "SOPInstanceUID to retrieve for -level=IMAGE")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "dial, retrieve, and release timeout")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nSend a DICOM C-MOVE or C-GET request using the Study Root Query/Retrieve Information Model. C-MOVE remains the default.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
		_, _ = fmt.Fprintln(stderr, "\nExamples:\n  dicom-go-retrieve -remote 127.0.0.1:4242 -move-destination DICOMSTORE -level SERIES -study-uid 1.2.3 -series-uid 1.2.3.4\n  dicom-go-retrieve -method get -remote 127.0.0.1:4242 -output ./received -level STUDY -study-uid 1.2.3")
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errRetrieveUsage
	}
	if fs.NArg() != 0 {
		fs.Usage()
		return opts, errRetrieveUsage
	}
	opts.method = strings.ToLower(strings.TrimSpace(opts.method))
	if opts.method != retrieveMethodMove && opts.method != retrieveMethodGet {
		fmt.Fprintln(stderr, "dicom-go-retrieve: usage: -method must be move or get")
		return opts, errRetrieveUsage
	}
	model, err := dimse.ParseQueryRetrieveModel(modelName)
	if err != nil {
		fmt.Fprintln(stderr, "dicom-go-retrieve: usage: -model must be study-root or patient-root")
		return opts, errRetrieveUsage
	}
	opts.model = model
	opts.outputDir = strings.TrimSpace(opts.outputDir)
	if opts.method == retrieveMethodGet && opts.outputDir == "" {
		fmt.Fprintln(stderr, "dicom-go-retrieve: usage: -output must not be empty for -method=get")
		return opts, errRetrieveUsage
	}
	opts.level = strings.ToUpper(strings.TrimSpace(opts.level))
	opts.patientID = strings.TrimSpace(opts.patientID)
	opts.studyUID = strings.TrimSpace(opts.studyUID)
	opts.seriesUID = strings.TrimSpace(opts.seriesUID)
	opts.sopInstanceUID = strings.TrimSpace(opts.sopInstanceUID)
	if opts.timeout <= 0 {
		fmt.Fprintln(stderr, "dicom-go-retrieve: usage: -timeout must be positive")
		return opts, errRetrieveUsage
	}
	return opts, nil
}

var moveIdentifierKeywords = map[string]struct {
	tag      core.Tag
	vr       core.VR
	flagName string
}{
	"PatientID":         {tag: core.NewTag(0x0010, 0x0020), vr: core.VRLO, flagName: "-patient-id"},
	"StudyInstanceUID":  {tag: core.NewTag(0x0020, 0x000D), vr: core.VRUI, flagName: "-study-uid"},
	"SeriesInstanceUID": {tag: core.NewTag(0x0020, 0x000E), vr: core.VRUI, flagName: "-series-uid"},
	"SOPInstanceUID":    {tag: core.NewTag(0x0008, 0x0018), vr: core.VRUI, flagName: "-sop-instance-uid"},
}

func buildMoveIdentifier(opts retrieveOptions) (*object.Object, error) {
	level := strings.ToUpper(strings.TrimSpace(opts.level))
	model := opts.model
	if model == "" {
		model = dimse.QueryRetrieveModelStudyRoot
	}
	required, err := dimse.QueryRetrieveRequiredKeys(model, level)
	if err != nil {
		levels, _ := dimse.QueryRetrieveLevels(model)
		return nil, fmt.Errorf("-level must be one of %s for -model %s", strings.Join(levels, ", "), retrieveModelLabel(model))
	}
	values := map[string]string{
		"PatientID":         strings.TrimSpace(opts.patientID),
		"StudyInstanceUID":  strings.TrimSpace(opts.studyUID),
		"SeriesInstanceUID": strings.TrimSpace(opts.seriesUID),
		"SOPInstanceUID":    strings.TrimSpace(opts.sopInstanceUID),
	}

	elements := []core.Element{
		{
			Header: core.ElementHeader{Tag: core.NewTag(0x0008, 0x0052), VR: core.VRCS},
			Value:  core.StringValue{level},
		},
	}
	for _, keyword := range required {
		spec := moveIdentifierKeywords[keyword]
		value := values[keyword]
		if value == "" {
			return nil, fmt.Errorf("%s is required for -level=%s", spec.flagName, level)
		}
		elements = append(elements, core.Element{
			Header: core.ElementHeader{Tag: spec.tag, VR: spec.vr},
			Value:  core.StringValue{value},
		})
	}
	sort.Slice(elements, func(i, j int) bool {
		return elements[i].Header.Tag.Less(elements[j].Header.Tag)
	})
	return object.FromDataSet(core.DataSet{Elements: elements}, std.Dictionary), nil
}

func retrieveModelLabel(model dimse.QueryRetrieveModel) string {
	if model == dimse.QueryRetrieveModelPatientRoot {
		return "patient-root"
	}
	return "study-root"
}
