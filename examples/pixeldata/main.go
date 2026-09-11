package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/pixeldata/builtin"
)

func main() {
	args := normalizeArgs(os.Args[1:])
	if len(args) != 1 {
		fmt.Fprintf(os.Stderr, "usage: go run ./examples/pixeldata -- <file.dcm>\n")
		os.Exit(2)
	}

	file, err := object.OpenFile(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "open DICOM file: %v\n", err)
		os.Exit(1)
	}
	if file.Dataset == nil {
		fmt.Fprintln(os.Stderr, "file has no dataset")
		os.Exit(1)
	}

	meta, err := pixeldata.ExtractMetadata(file.Dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extract pixel metadata: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%dx%d samples=%d bits=%d frames=%d photometric=%s\n",
		meta.Columns, meta.Rows, meta.SamplesPerPixel, meta.BitsAllocated,
		meta.NumberOfFrames, meta.PhotometricInterpretation)

	allPixels, err := pixeldata.ExtractAll(file.Dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extract pixel data locations: %v\n", err)
		os.Exit(1)
	}
	for _, pixels := range allPixels {
		fmt.Printf("pixel data path=%s context=%s encapsulated=%v\n", pixels.Path, pixels.Context, pixels.Data.Encapsulated)
	}

	// Native frame extraction covers uncompressed pixel data. Encapsulated
	// transfer syntaxes require a registered codec; this example registers the
	// built-in JPEG Baseline/Extended, JPEG Lossless, and RLE Lossless baseline.
	// JPEG-LS, JPEG 2000, JPEG XL, MPEG and HEVC are recognized in the transfer
	// registry but are not decoded.
	native, err := pixeldata.ExtractNativeFrames(file.Dataset)
	if err == nil {
		for i, frame := range native.Data {
			fmt.Printf("native frame %d: %d bytes\n", i, len(frame))
		}
		return
	}
	if !errors.Is(err, pixeldata.ErrEncapsulatedPixelData) {
		fmt.Fprintf(os.Stderr, "extract native frames: %v\n", err)
		os.Exit(1)
	}

	if err := builtin.RegisterDefault(); err != nil {
		fmt.Fprintf(os.Stderr, "register built-in pixel codecs: %v\n", err)
		os.Exit(1)
	}
	pixels, err := pixeldata.Extract(file.Dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "extract pixel data: %v\n", err)
		os.Exit(1)
	}
	frames, err := pixeldata.DecodeFrames(file.TransferSyntax.UID, pixels, file.Dataset)
	if err != nil {
		fmt.Fprintf(os.Stderr, "decode encapsulated frames: %v\n", err)
		os.Exit(1)
	}
	for i, frame := range frames.Data {
		fmt.Printf("decoded frame %d: %d bytes\n", i, len(frame))
	}
}

func normalizeArgs(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}
