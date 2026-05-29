package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/net/ul"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const studyRootMoveUID = "1.2.840.10008.5.1.4.1.2.2.2"

var errRetrieveUsage = errors.New("usage error")

type retrieveOptions struct {
	remote          string
	callingAET      string
	calledAET       string
	moveDestination string
	level           string
	studyUID        string
	seriesUID       string
	sopInstanceUID  string
	timeout         time.Duration
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
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

	ctx, cancel := context.WithTimeout(context.Background(), opts.timeout)
	defer cancel()

	assoc, err := dimse.DialRetrieveSCU(ctx, dimse.RetrieveDialOptions{
		Address:        opts.remote,
		CallingAETitle: opts.callingAET,
		CalledAETitle:  opts.calledAET,
		Contexts: []ul.PresentationContext{{
			AbstractSyntaxUID:  studyRootMoveUID,
			TransferSyntaxUIDs: []string{transfer.ImplicitVRLittleEndian.UID},
		}},
	})
	if err != nil {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		return 1
	}
	defer func() {
		releaseCtx, releaseCancel := context.WithTimeout(context.Background(), opts.timeout)
		defer releaseCancel()
		if err := assoc.Release(releaseCtx); err != nil {
			clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		}
	}()

	// Use the first accepted presentation context for Study Root MOVE.
	var pcid byte
	for _, ac := range assoc.AcceptedContexts {
		if ac.AbstractSyntaxUID == studyRootMoveUID {
			pcid = ac.ID
			break
		}
	}
	if pcid == 0 {
		fmt.Fprintln(stderr, "dicom-go-retrieve: network: no accepted presentation context for Study Root MOVE")
		return 1
	}

	// Identifier transfer syntax should match the transfer syntax accepted for this presentation context.
	// For this interop harness, we default to Implicit VR Little Endian.
	client := dimse.NewRetrieveClient(assoc, pcid, transfer.ImplicitVRLittleEndian)

	resp, err := client.Move(ctx, dimse.CMoveRequest{
		AffectedSOPClassUID: studyRootMoveUID,
		MessageID:           1,
		Priority:            0,
		MoveDestination:     opts.moveDestination,
	}, identifierObj)
	if err != nil {
		clidiag.Fprintln(stderr, "dicom-go-retrieve", err)
		return 1
	}

	fmt.Fprintf(stdout, "C-MOVE completed with status=0x%04X\n", resp.Status)
	return 0
}

func parseRetrieveArgs(args []string, stderr io.Writer) (retrieveOptions, error) {
	opts := retrieveOptions{
		remote:          "127.0.0.1:4242",
		callingAET:      "DICOMGO",
		calledAET:       "ORTHANC",
		moveDestination: "DICOMSTORE",
		level:           "STUDY",
		timeout:         30 * time.Second,
	}

	fs := flag.NewFlagSet("dicom-go-retrieve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.remote, "remote", opts.remote, "Remote DICOM address host:port")
	fs.StringVar(&opts.callingAET, "calling-aet", opts.callingAET, "Calling AE title")
	fs.StringVar(&opts.calledAET, "called-aet", opts.calledAET, "Called AE title")
	fs.StringVar(&opts.moveDestination, "move-destination", opts.moveDestination, "C-MOVE destination AE title")
	fs.StringVar(&opts.level, "level", opts.level, "QueryRetrieveLevel: STUDY, SERIES, or IMAGE")
	fs.StringVar(&opts.studyUID, "study-uid", "", "StudyInstanceUID to retrieve")
	fs.StringVar(&opts.seriesUID, "series-uid", "", "SeriesInstanceUID to retrieve for -level=SERIES or -level=IMAGE")
	fs.StringVar(&opts.sopInstanceUID, "sop-instance-uid", "", "SOPInstanceUID to retrieve for -level=IMAGE")
	fs.DurationVar(&opts.timeout, "timeout", opts.timeout, "dial, C-MOVE, and release timeout")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags]\n\nSend a DICOM C-MOVE request using the Study Root Query/Retrieve Information Model.\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
		_, _ = fmt.Fprintln(stderr, "\nExamples:\n  dicom-go-retrieve -remote 127.0.0.1:4242 -move-destination DICOMSTORE -level SERIES -study-uid 1.2.3 -series-uid 1.2.3.4")
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
	opts.level = strings.ToUpper(strings.TrimSpace(opts.level))
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
	"StudyInstanceUID":  {tag: core.NewTag(0x0020, 0x000D), vr: core.VRUI, flagName: "-study-uid"},
	"SeriesInstanceUID": {tag: core.NewTag(0x0020, 0x000E), vr: core.VRUI, flagName: "-series-uid"},
	"SOPInstanceUID":    {tag: core.NewTag(0x0008, 0x0018), vr: core.VRUI, flagName: "-sop-instance-uid"},
}

func buildMoveIdentifier(opts retrieveOptions) (*object.Object, error) {
	level := strings.ToUpper(strings.TrimSpace(opts.level))
	required, err := dimse.QueryRetrieveRequiredKeys(dimse.QueryRetrieveModelStudyRoot, level)
	if err != nil {
		return nil, fmt.Errorf("-level must be one of STUDY, SERIES, IMAGE")
	}
	values := map[string]string{
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
