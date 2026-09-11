// streamencapsulated reports complete encoded JPEG/JPEG-LS frames while reading
// a Part 10 file. It deliberately discards Pixel Data after delivery.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sync"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/pixeldata/encapsulated"
)

func run(ctx context.Context, input io.Reader, output io.Writer) error {
	sink := parser.EncodedFrameSinkFunc(func(f parser.EncodedFrame) error {
		_, err := fmt.Fprintf(output, "frame=%d encoded_bytes=%d syntax=%s\n", f.Index, len(f.Data), f.TransferSyntax.UID)
		return err
	})
	stream, err := encapsulated.NewStream(ctx, sink, encapsulated.Limits{MaxFrameBytes: 64 << 20})
	if err != nil {
		return err
	}
	file, err := object.ReadFileWithOptions(input, object.ReadFileOptions{
		EncapsulatedSink: stream, MaxElementBytes: 1 << 20, MaxPixelDataBytes: 512 << 20,
		MaxTotalBytes: 600 << 20, MaxElements: 200000, MaxSequenceDepth: 32,
	})
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = fmt.Fprintln(output, "file_complete=true pixel_data_discarded=true")
	return err
}
func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: streamencapsulated input.dcm")
		os.Exit(2)
	}
	input, err := os.Open(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, "open failed")
		os.Exit(1)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	var once sync.Once
	closeInput := func() { once.Do(func() { _ = input.Close() }) }
	stop := context.AfterFunc(ctx, closeInput)
	err = run(ctx, input, os.Stdout)
	stop()
	closeInput()
	if err != nil {
		fmt.Fprintln(os.Stderr, "stream failed; previously delivered frames are provisional")
		os.Exit(1)
	}
}
