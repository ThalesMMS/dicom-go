package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const defaultMaxValueLen = 64

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	opts, path, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		_, _ = fmt.Fprintln(stderr, err)
		return 2
	}

	file, err := object.OpenFile(path)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}

	if opts.json {
		if err := dumpFileJSON(stdout, file); err != nil {
			_, _ = fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}

	formatter := NewFormatter(stdout, opts.maxValueLen)
	if err := formatter.DumpFile(file); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	json        bool
	maxValueLen int
	showOffsets bool
}

func parseArgs(args []string, stderr io.Writer) (options, string, error) {
	opts := options{maxValueLen: defaultMaxValueLen}

	fs := flag.NewFlagSet("dcmdump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.json, "json", false, "write combined file metadata and dataset as JSON")
	fs.IntVar(&opts.maxValueLen, "max-value", defaultMaxValueLen, "maximum bytes/characters to render for each value preview")
	fs.BoolVar(&opts.showOffsets, "show-offsets", false, "reserve space for future offset-aware dumps; currently has no effect")
	fs.Usage = func() {
		_, _ = fmt.Fprintf(stderr, "Usage: %s [flags] <file.dcm>\n\nFlags:\n", fs.Name())
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return opts, "", err
		}
		return opts, "", errUsage
	}
	if opts.maxValueLen <= 0 {
		_, _ = fmt.Fprintln(stderr, "-max-value must be greater than zero")
		fs.Usage()
		return opts, "", errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return opts, "", errUsage
	}

	return opts, fs.Arg(0), nil
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

func dumpFileJSON(w io.Writer, file *object.File) error {
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
	for _, elem := range obj.SortedElements() {
		if elem.VR() == core.VRUI {
			entry, ok := out[elem.Tag().HexString()].(map[string]any)
			if ok {
				addUIDNames(entry, elem.StringValues())
			}
		}
	}
	return out, nil
}

func addUIDNames(entry map[string]any, values []string) {
	if len(values) == 0 {
		return
	}

	names := make([]any, 0, len(values))
	for _, value := range values {
		name := nameForUID(value)
		if name == "" {
			continue
		}
		names = append(names, name)
	}
	if len(names) == 0 {
		return
	}
	entry["ValueNames"] = names
}

func endiannessForSyntax(syntax transfer.Syntax) string {
	if syntax.IsLittleEndian() {
		return "little"
	}
	return "big"
}
