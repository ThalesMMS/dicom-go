package dicomwebcli

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

type retrieveOptions struct {
	common           commonOptions
	studyUID         string
	seriesUID        string
	instanceUID      string
	maxParts         int
	transferSyntaxes stringList
}

func runRetrieve(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	_ = stdout
	opts := retrieveOptions{common: defaultCommonOptions(), maxParts: 10_000}
	fs := flag.NewFlagSet("dicomweb retrieve", flag.ContinueOnError)
	addCommonFlags(fs, &opts.common)
	fs.StringVar(&opts.studyUID, "study-uid", "", "study instance UID")
	fs.StringVar(&opts.seriesUID, "series-uid", "", "series instance UID")
	fs.StringVar(&opts.instanceUID, "instance-uid", "", "SOP instance UID")
	fs.IntVar(&opts.maxParts, "max-parts", opts.maxParts, "maximum response parts")
	fs.Var(&opts.transferSyntaxes, "transfer-syntax", "preferred transfer syntax UID (repeatable)")
	configureUsage(fs, stderr, "dicomweb retrieve [flags] <study|series|instance>")
	if err := parseFlags(fs, args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return invalidInput("retrieve requires one resource")
	}
	if opts.maxParts <= 0 {
		return invalidInput("-max-parts must be positive")
	}
	if err := validateTransferSyntaxes(opts.transferSyntaxes); err != nil {
		return err
	}
	ref, err := scopedRef(fs.Arg(0), opts.studyUID, opts.seriesUID, opts.instanceUID)
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
	writer := &partWriter{ctx: ctx, directory: opts.common.output, maximum: opts.maxParts, extension: ".dcm"}
	retrieveOptions := dicomweb.RetrieveOptions{TransferSyntaxUIDs: append([]string(nil), opts.transferSyntaxes...)}
	switch fs.Arg(0) {
	case "study":
		err = client.RetrieveStudyStreamWithOptions(ctx, ref.StudyInstanceUID, retrieveOptions, func(part dicomweb.LocatedObjectPartStream) error {
			return wrapOutputCallbackError(writer.write(part.Part.Reader))
		})
	case "series":
		err = client.RetrieveSeriesStreamWithOptions(ctx, ref.StudyInstanceUID, ref.SeriesInstanceUID, retrieveOptions, func(part dicomweb.LocatedObjectPartStream) error {
			return wrapOutputCallbackError(writer.write(part.Part.Reader))
		})
	case "instance":
		err = client.RetrieveInstanceStreamWithOptions(ctx, ref, retrieveOptions, func(part dicomweb.ObjectPartStream) error {
			return wrapOutputCallbackError(writer.write(part.Reader))
		})
	}
	if err != nil {
		removeCreated(writer.created)
		return classifyRetrieveStreamError(err)
	}
	return nil
}

type outputCallbackError struct {
	err error
}

func (e *outputCallbackError) Error() string { return "retrieve output failed" }
func (e *outputCallbackError) Unwrap() error { return e.err }

func wrapOutputCallbackError(err error) error {
	if err == nil {
		return nil
	}
	var readErr *retrieveStreamReadError
	var protocolErr *dicomweb.Error
	if errors.As(err, &readErr) || errors.As(err, &protocolErr) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return &outputCallbackError{err: err}
}

func classifyRetrieveStreamError(err error) error {
	var outputErr *outputCallbackError
	if errors.As(err, &outputErr) {
		return outputErr.err
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	var protocolErr *dicomweb.Error
	if errors.As(err, &protocolErr) {
		return err
	}
	var readErr *retrieveStreamReadError
	if errors.As(err, &readErr) {
		return &dicomweb.Error{Kind: dicomweb.ErrorKindDecodeResponse, Err: readErr.err}
	}
	return &dicomweb.Error{Kind: dicomweb.ErrorKindDecodeResponse, Err: err}
}

type retrieveStreamReadError struct {
	err error
}

func (e *retrieveStreamReadError) Error() string { return "retrieve response stream failed" }
func (e *retrieveStreamReadError) Unwrap() error { return e.err }

func scopedRef(resource, study, series, instance string) (dicomweb.InstanceRef, error) {
	ref := dicomweb.InstanceRef{
		StudyInstanceUID: strings.TrimSpace(study), SeriesInstanceUID: strings.TrimSpace(series), SOPInstanceUID: strings.TrimSpace(instance),
	}
	if !core.IsValidUID(ref.StudyInstanceUID) {
		return dicomweb.InstanceRef{}, invalidInput("a valid -study-uid is required")
	}
	switch resource {
	case "study":
		if ref.SeriesInstanceUID != "" || ref.SOPInstanceUID != "" {
			return dicomweb.InstanceRef{}, invalidInput("series and instance UIDs are not valid for study retrieval")
		}
	case "series":
		if !core.IsValidUID(ref.SeriesInstanceUID) || ref.SOPInstanceUID != "" {
			return dicomweb.InstanceRef{}, invalidInput("series retrieval requires only valid study and series UIDs")
		}
	case "instance":
		if !core.IsValidUID(ref.SeriesInstanceUID) || !core.IsValidUID(ref.SOPInstanceUID) {
			return dicomweb.InstanceRef{}, invalidInput("instance retrieval requires valid study, series, and instance UIDs")
		}
	default:
		return dicomweb.InstanceRef{}, invalidInput("retrieve resource must be study, series, or instance")
	}
	return ref, nil
}

type partWriter struct {
	ctx       context.Context
	directory string
	maximum   int
	extension string
	count     int
	created   []string
}

func (w *partWriter) write(reader io.Reader) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if w.count >= w.maximum {
		return invalidInput("response exceeds -max-parts")
	}
	w.count++
	path := filepath.Join(w.directory, fmt.Sprintf("part-%06d%s", w.count, w.extension))
	if err := writeAtomicFile(path, func(output io.Writer) error {
		return copyRetrievePart(w.ctx, output, reader)
	}); err != nil {
		return err
	}
	w.created = append(w.created, path)
	return nil
}

func copyRetrievePart(ctx context.Context, destination io.Writer, source io.Reader) error {
	buffer := make([]byte, 64<<10)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		read, readErr := source.Read(buffer)
		if read > 0 {
			written, writeErr := destination.Write(buffer[:read])
			if writeErr != nil {
				return writeErr
			}
			if written != read {
				return io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			var protocolErr *dicomweb.Error
			if errors.As(readErr, &protocolErr) {
				return readErr
			}
			return &retrieveStreamReadError{err: readErr}
		}
	}
}
