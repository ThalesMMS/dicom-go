package dicomwebcli

import (
	"context"
	"flag"
	"io"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

type framesOptions struct {
	common           commonOptions
	studyUID         string
	seriesUID        string
	instanceUID      string
	frames           string
	maxParts         int
	transferSyntaxes stringList
}

func runFrames(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	_ = stdout
	opts := framesOptions{common: defaultCommonOptions(), maxParts: 10_000}
	fs := flag.NewFlagSet("dicomweb frames", flag.ContinueOnError)
	addCommonFlags(fs, &opts.common)
	fs.StringVar(&opts.studyUID, "study-uid", "", "study instance UID")
	fs.StringVar(&opts.seriesUID, "series-uid", "", "series instance UID")
	fs.StringVar(&opts.instanceUID, "instance-uid", "", "SOP instance UID")
	fs.StringVar(&opts.frames, "frames", "", "comma-separated one-based frame numbers")
	fs.IntVar(&opts.maxParts, "max-parts", opts.maxParts, "maximum response parts")
	fs.Var(&opts.transferSyntaxes, "transfer-syntax", "preferred transfer syntax UID (repeatable)")
	configureUsage(fs, stderr, "dicomweb frames [flags]")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return invalidInput("frames accepts no positional arguments")
	}
	if opts.maxParts <= 0 {
		return invalidInput("-max-parts must be positive")
	}
	if err := validateTransferSyntaxes(opts.transferSyntaxes); err != nil {
		return err
	}
	frameNumbers, err := parseFrames(opts.frames, opts.maxParts)
	if err != nil {
		return err
	}
	ref, err := scopedRef("instance", opts.studyUID, opts.seriesUID, opts.instanceUID)
	if err != nil {
		return err
	}
	client, err := opts.common.client()
	if err != nil {
		return err
	}
	if err := ensureOutputDirectory(opts.common.output); err != nil {
		return err
	}
	writer := &partWriter{ctx: ctx, directory: opts.common.output, maximum: opts.maxParts, extension: ".bin"}
	err = client.RetrieveFramesStreamWithOptions(ctx, ref, frameNumbers,
		dicomweb.RetrieveOptions{TransferSyntaxUIDs: append([]string(nil), opts.transferSyntaxes...)},
		func(part dicomweb.FramePartStream) error { return writer.write(part.Reader) })
	if err != nil {
		removeCreated(writer.created)
		return err
	}
	return nil
}

func parseFrames(value string, maximum int) ([]int, error) {
	items := strings.Split(strings.TrimSpace(value), ",")
	if len(items) == 1 && items[0] == "" {
		return nil, invalidInput("-frames is required")
	}
	if len(items) > maximum {
		return nil, invalidInput("frame count exceeds -max-parts")
	}
	seen := make(map[int]bool, len(items))
	frames := make([]int, 0, len(items))
	for _, item := range items {
		frame, err := strconv.Atoi(strings.TrimSpace(item))
		if err != nil || frame <= 0 || seen[frame] {
			return nil, invalidInput("-frames must contain unique positive integers")
		}
		seen[frame] = true
		frames = append(frames, frame)
	}
	return frames, nil
}
