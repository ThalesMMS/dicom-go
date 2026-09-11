// stream-native-frames demonstrates bounded delivery and early consumer exit.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"

	"github.com/ThalesMMS/dicom-go/object"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: stream-native-frames <file.dcm> <maximum-frames>")
		os.Exit(2)
	}
	limit, err := strconv.Atoi(args[1])
	if err != nil || limit < 1 {
		fmt.Fprintln(os.Stderr, "maximum-frames must be a positive integer")
		os.Exit(2)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := streamFrames(ctx, args[0], limit, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func streamFrames(parent context.Context, path string, limit int, out io.Writer) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	frames := make(chan object.Frame, 2)
	sink := object.NewFrameChannelSinkContext(ctx, frames)
	done := make(chan error, 1)
	go func() {
		file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{FrameSink: sink})
		if file != nil {
			err = errors.Join(err, file.Close())
		}
		done <- err
	}()
	count := 0
	var outputErr error
	for frame := range frames {
		_, outputErr = fmt.Fprintf(out, "frame=%d rows=%d columns=%d bytes=%d\n", frame.Index, frame.Metadata.Rows, frame.Metadata.Columns, len(frame.Data))
		count++
		if outputErr != nil || count == limit {
			cancel()
			break
		} // Never close frames as a consumer.
	}
	// One parser goroutine, no goroutine per send. Regular file reads are used
	// here; a network reader needs its own I/O deadline/interrupt mechanism.
	err := <-done
	if outputErr != nil {
		return outputErr
	}
	if parent.Err() != nil {
		return parent.Err()
	}
	if count == limit && errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
