package dicomutil

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/object"
	pixelframe "github.com/ThalesMMS/dicom-go/pixeldata/frame"
	"github.com/ThalesMMS/dicom-go/pixeldata/jpeg"
	"github.com/ThalesMMS/dicom-go/pixeldata/rle"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func Run(args []string, stdout, stderr io.Writer) int {
	opts, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "dicomutil", err)
		return 2
	}

	if opts.json {
		if err := writeJSON(stdout, opts.path); err != nil {
			clidiag.Fprintln(stderr, "dicomutil", err)
			return 1
		}
		return 0
	}

	if err := extractImages(stdout, opts.path, opts.outDir); err != nil {
		clidiag.Fprintln(stderr, "dicomutil", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	path          string
	json          bool
	extractImages bool
	outDir        string
}

func parseArgs(args []string, stderr io.Writer) (options, error) {
	var opts options

	fs := flag.NewFlagSet("dicomutil", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.path, "path", "", "path to a DICOM Part 10 file")
	fs.BoolVar(&opts.json, "json", false, "write combined file metadata and dataset as JSON")
	fs.BoolVar(&opts.extractImages, "extract-images", false, "extract Pixel Data frames as PNG images")
	fs.StringVar(&opts.outDir, "out-dir", "", "directory for extracted image files")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s -path file.dcm (-json | -extract-images -out-dir out)\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, err
		}
		return opts, errUsage
	}
	switch {
	case fs.NArg() != 0:
		fs.Usage()
		return opts, errUsage
	case opts.path == "":
		_, _ = fmt.Fprintln(stderr, "-path is required")
		fs.Usage()
		return opts, errUsage
	case opts.json == opts.extractImages:
		_, _ = fmt.Fprintln(stderr, "choose exactly one of -json or -extract-images")
		fs.Usage()
		return opts, errUsage
	case opts.extractImages && opts.outDir == "":
		_, _ = fmt.Fprintln(stderr, "-out-dir is required with -extract-images")
		fs.Usage()
		return opts, errUsage
	}

	return opts, nil
}

type fileJSON struct {
	TransferSyntax transferSyntaxJSON `json:"transferSyntax"`
	FileMeta       map[string]any     `json:"fileMeta"`
	DataSet        map[string]any     `json:"dataSet"`
}

type transferSyntaxJSON struct {
	UID        string `json:"uid"`
	Name       string `json:"name"`
	ExplicitVR bool   `json:"explicitVR"`
	Endianness string `json:"endianness"`
}

func writeJSON(w io.Writer, path string) error {
	file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{
		DeferPixelData: true,
	})
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	meta, err := marshalJSONObject(file.Meta)
	if err != nil {
		return err
	}
	dataset, err := marshalJSONObject(file.Dataset)
	if err != nil {
		return err
	}

	data, err := json.MarshalIndent(fileJSON{
		TransferSyntax: transferSyntaxJSON{
			UID:        file.TransferSyntax.UID,
			Name:       file.TransferSyntax.Name,
			ExplicitVR: file.TransferSyntax.ExplicitVR,
			Endianness: endiannessForSyntax(file.TransferSyntax),
		},
		FileMeta: meta,
		DataSet:  dataset,
	}, "", "  ")
	if err != nil {
		return err
	}
	_, err = w.Write(data)
	return err
}

func marshalJSONObject(obj *object.Object) (map[string]any, error) {
	if obj == nil {
		return map[string]any{}, nil
	}
	data, err := dicomjson.MarshalObject(obj)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, nil
}

func extractImages(stdout io.Writer, path, outDir string) error {
	if err := registerDefaultCodecs(); err != nil {
		return err
	}

	file, err := object.OpenFile(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	frames, err := pixelframe.ExtractFrames(file)
	if err != nil {
		return err
	}
	if len(frames) == 0 {
		return errors.New("dicom: no Pixel Data frames found")
	}

	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	for i, frame := range frames {
		img, err := frame.GetImage()
		if err != nil {
			return fmt.Errorf("render frame %d: %w", i+1, err)
		}
		outputPath := filepath.Join(outDir, fmt.Sprintf("image_%04d.png", i+1))
		if err := writePNG(outputPath, img); err != nil {
			return fmt.Errorf("write frame %d: %w", i+1, err)
		}
		_, _ = fmt.Fprintln(stdout, outputPath)
	}
	return nil
}

func registerDefaultCodecs() error {
	return errors.Join(
		jpeg.RegisterDefault(),
		rle.RegisterDefault(),
	)
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	return png.Encode(f, img)
}

func endiannessForSyntax(syntax transfer.Syntax) string {
	if syntax.IsLittleEndian() {
		return "little"
	}
	return "big"
}
