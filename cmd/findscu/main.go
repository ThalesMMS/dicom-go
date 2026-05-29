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
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/internal/dcmdump"
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
		clidiag.Fprintln(stderr, "findscu", err)
		return 1
	}

	if err := runFind(opts, stdout); err != nil {
		clidiag.Fprintln(stderr, "findscu", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	host       string
	port       int
	calledAE   string
	callingAE  string
	level      string
	timeout    time.Duration
	maxResults int
	keys       []string
}

const (
	defaultTimeout        = 10 * time.Second
	defaultReleaseTimeout = 5 * time.Second
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
		host:       "localhost",
		port:       11112,
		calledAE:   "ANY-SCP",
		callingAE:  "FINDSCU",
		level:      "study",
		timeout:    defaultTimeout,
		maxResults: 0,
	}

	fs := flag.NewFlagSet("findscu", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.callingAE, "calling", opts.callingAE, "calling AE title")
	fs.StringVar(&opts.calledAE, "called", opts.calledAE, "called AE title")
	fs.StringVar(&opts.host, "host", opts.host, "SCP host name or IP address")
	fs.IntVar(&opts.port, "port", opts.port, "SCP TCP port")
	fs.StringVar(&opts.level, "level", opts.level, "query retrieve level: study or series")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "dial/find timeout")
	fs.IntVar(&opts.maxResults, "max-results", opts.maxResults, "stop after printing N pending matches (0 means no limit)")

	var keys stringSliceFlag
	fs.Var(&keys, "k", "query key in the form Keyword=value (repeatable)")

	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nSend a DICOM C-FIND request using the Study Root Query/Retrieve Information Model.\n\nFlags:\n", fs.Name())
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

	if opts.port <= 0 || opts.port > 65535 {
		_, _ = fmt.Fprintln(stderr, "-port must be between 1 and 65535")
		fs.Usage()
		return opts, errUsage
	}
	level := strings.ToLower(strings.TrimSpace(opts.level))
	if level != "study" && level != "series" {
		_, _ = fmt.Fprintln(stderr, "-level must be either 'study' or 'series'")
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
	for _, kv := range opts.keys {
		if !strings.Contains(kv, "=") {
			_, _ = fmt.Fprintf(stderr, "-k must be Keyword=value, got %q\n", kv)
			fs.Usage()
			return opts, errUsage
		}
	}

	return opts, nil
}

func runFind(opts options, stdout io.Writer) error {
	address := net.JoinHostPort(opts.host, strconv.Itoa(opts.port))

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	assoc, err := ul.Dial(address, ul.DialOptions{
		CalledAETitle:  opts.calledAE,
		CallingAETitle: opts.callingAE,
		Context:        ctx,
		Contexts:       []ul.PresentationContext{dimse.StudyRootFindPresentationContext()},
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

	pc, err := dimse.AcceptedContextForSOPClass(assoc, dimse.StudyRootFindSOPClassUID)
	if err != nil {
		return fmt.Errorf("no accepted Study Root FIND presentation context: %w", err)
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
	if opts.level == "study" {
		identifierElements, err = dimse.BuildStudyRootStudyFindKeys(queryKeys)
	} else {
		identifierElements, err = dimse.BuildStudyRootSeriesFindKeys(queryKeys)
	}
	if err != nil {
		return err
	}
	identifier := object.FromElements(identifierElements, std.Dictionary)

	req := dimse.CFindRequest{
		MessageID:           1,
		AffectedSOPClassUID: dimse.StudyRootFindSOPClassUID,
		Priority:            0,
	}

	identifierSyntax := transfer.ImplicitVRLittleEndian
	if pc.TransferSyntaxUID != "" {
		identifierSyntax = transfer.Syntax{UID: pc.TransferSyntaxUID}
	}

	results, errs := dimse.Find(ctx, assoc, pc.ID, req, identifier, identifierSyntax)

	printed := 0
	suppressIdentifiers := false
	var finalStatus uint16
	var finalComment string

	for r := range results {
		finalStatus = r.Status()
		finalComment = r.ErrorComment()
		if dimse.CategorizeCFindStatus(finalStatus) == dimse.CFindStatusPending {
			if !suppressIdentifiers {
				printed++
				if err := dumpIdentifier(stdout, r.Identifier, identifierSyntax); err != nil {
					return err
				}
				_, _ = fmt.Fprintln(stdout)
				if opts.maxResults > 0 && printed >= opts.maxResults {
					suppressIdentifiers = true
				}
			}
		}
	}

	if err := <-errs; err != nil {
		return err
	}

	_, _ = fmt.Fprintln(stdout, formatFinalStatus(finalStatus, finalComment))

	releaseCtx, cancelRelease := context.WithTimeout(context.Background(), defaultReleaseTimeout)
	defer cancelRelease()
	if err := assoc.Release(releaseCtx); err != nil {
		return err
	}
	released = true
	return nil
}
