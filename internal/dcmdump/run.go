package dcmdump

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/internal/clidiag"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const defaultMaxValueLen = 64

func Run(args []string, stdout, stderr io.Writer) int {
	opts, path, err := parseArgs(args, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		if errors.Is(err, errUsage) {
			return 2
		}
		clidiag.Fprintln(stderr, "dcmdump", err)
		return 2
	}

	file, err := openFileForDump(path, opts.recoverTransferSyntax)
	if err != nil {
		clidiag.Fprintln(stderr, "dcmdump", err)
		return 1
	}
	defer func() { _ = file.Close() }()
	if resolution, ok := file.TransferSyntaxResolution(); ok && resolution.Inferred() {
		_, _ = fmt.Fprintf(
			stderr,
			"dcmdump: warning: inferred transfer syntax %s (%s), source=%s, confidence=%.3f\n",
			resolution.Syntax.UID,
			resolution.Syntax.Name,
			resolution.Source,
			resolution.Confidence,
		)
	}

	if opts.json {
		if err := dumpFileJSON(stdout, file); err != nil {
			clidiag.Fprintln(stderr, "dcmdump", err)
			return 1
		}
		return 0
	}
	if opts.showOffsets {
		if err := dumpFileTextWithOffsets(stdout, path, file, opts.maxValueLen); err != nil {
			clidiag.Fprintln(stderr, "dcmdump", err)
			return 1
		}
		return 0
	}

	formatter := NewFormatter(stdout, opts.maxValueLen)
	if err := formatter.DumpFile(file); err != nil {
		clidiag.Fprintln(stderr, "dcmdump", err)
		return 1
	}
	return 0
}

var errUsage = errors.New("usage error")

type options struct {
	json                  bool
	maxValueLen           int
	showOffsets           bool
	recoverTransferSyntax bool
}

func parseArgs(args []string, stderr io.Writer) (options, string, error) {
	opts := options{maxValueLen: defaultMaxValueLen}

	fs := flag.NewFlagSet("dcmdump", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.BoolVar(&opts.json, "json", false, "write combined file metadata and dataset as JSON")
	fs.IntVar(&opts.maxValueLen, "max-value", defaultMaxValueLen, "maximum bytes/characters to render for each value preview")
	fs.BoolVar(&opts.showOffsets, "show-offsets", false, "prefix text dump lines with the byte offset of each element header")
	fs.BoolVar(&opts.recoverTransferSyntax, "recover-transfer-syntax", false, "opt in to bounded, diagnostic transfer syntax recovery for malformed or raw files")
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
	if opts.recoverTransferSyntax && opts.showOffsets {
		_, _ = fmt.Fprintln(stderr, "-recover-transfer-syntax cannot be combined with -show-offsets")
		fs.Usage()
		return opts, "", errUsage
	}
	if fs.NArg() != 1 {
		fs.Usage()
		return opts, "", errUsage
	}

	return opts, fs.Arg(0), nil
}

func openFileForDump(path string, recoverTransferSyntax bool) (*object.File, error) {
	if recoverTransferSyntax {
		file, _, err := object.OpenFileWithTransferSyntaxRecovery(
			path,
			object.ReadFileOptions{DeferPixelData: true},
			object.TransferSyntaxRecoveryOptions{
				AllowMissingPreamble:          true,
				AllowMissingFileMeta:          true,
				AllowMissingTransferSyntaxUID: true,
				AllowUnknownTransferSyntaxUID: true,
				AllowDeclaredMismatch:         true,
			},
		)
		return file, err
	}
	file, err := object.OpenFileWithOptions(path, object.ReadFileOptions{
		DeferPixelData: true,
	})
	if errors.Is(err, object.ErrDeferredValueRequiresSeekable) {
		return object.OpenFile(path)
	}
	return file, err
}

func dumpFileTextWithOffsets(w io.Writer, path string, file *object.File, maxValueLen int) error {
	if file == nil {
		return fmt.Errorf("dicom: nil file")
	}
	if file.TransferSyntax.Deflated {
		return fmt.Errorf("dcmdump: --show-offsets is not available for deflated transfer syntax %q", file.TransferSyntax.UID)
	}

	source, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = source.Close() }()

	br := bufio.NewReader(source)
	prefix := make([]byte, 132)
	if _, err := io.ReadFull(br, prefix); err != nil {
		return err
	}
	if string(prefix[128:132]) != "DICM" {
		return object.ErrMissingPreamble
	}

	formatter := NewFormatterWithOptions(w, maxValueLen, FormatterOptions{ShowOffsets: true})
	if _, err := fmt.Fprintf(w, "Transfer Syntax: %s (%s)\n\n# File Meta\n", file.TransferSyntax.Name, file.TransferSyntax.UID); err != nil {
		return err
	}
	datasetOffset, err := dumpFileMetaWithOffsets(formatter, br)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "\n%s\n# Dataset\n", sectionSeparator); err != nil {
		return err
	}
	datasetReader := parser.NewReader(br, file.TransferSyntax, parser.ReaderOptions{
		Dictionary:     std.Dictionary,
		BaseOffset:     datasetOffset,
		DeferPixelData: true,
	})
	return formatter.DumpTokenStream(datasetReader, file.TransferSyntax, 0)
}

func dumpFileMetaWithOffsets(formatter *Formatter, r *bufio.Reader) (int64, error) {
	readerOpts := parser.ReaderOptions{
		Dictionary: std.Dictionary,
		BaseOffset: 132,
	}
	metaReader := parser.NewReader(r, transfer.ExplicitVRLittleEndian, readerOpts)

	offset := metaReader.Position()
	tok, err := metaReader.Next()
	if err != nil {
		return 0, err
	}
	if tok.Kind != parser.TokenElement || tok.Element.Tag() != core.NewTag(0x0002, 0x0000) {
		return 0, object.ErrInvalidFileMetaGroupLength
	}
	raw, ok := tok.Element.RawBytes()
	if !ok || len(raw) != 4 {
		return 0, object.ErrInvalidFileMetaGroupLengthValue
	}
	if err := formatter.dumpElementWithOffset(tok.Element, 0, transfer.ExplicitVRLittleEndian, offset); err != nil {
		return 0, err
	}

	metaLen := binary.LittleEndian.Uint32(raw)
	rest := io.LimitReader(r, int64(metaLen))
	restReader := parser.NewReader(rest, transfer.ExplicitVRLittleEndian, parser.ReaderOptions{
		Dictionary: std.Dictionary,
		BaseOffset: metaReader.Position(),
	})
	if err := formatter.DumpTokenStream(restReader, transfer.ExplicitVRLittleEndian, 0); err != nil {
		return 0, err
	}
	return restReader.Position(), nil
}

type fileJSON struct {
	TransferSyntax transferSyntaxJSON `json:"transferSyntax"`
	FileMeta       map[string]any     `json:"fileMeta"`
	DataSet        map[string]any     `json:"dataSet"`
}

type transferSyntaxJSON struct {
	UID        string  `json:"uid"`
	Name       string  `json:"name"`
	ExplicitVR bool    `json:"explicitVR"`
	Endianness string  `json:"endianness"`
	Inferred   bool    `json:"inferred,omitempty"`
	Source     string  `json:"source,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
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

	transferSyntax := transferSyntaxJSON{
		UID:        file.TransferSyntax.UID,
		Name:       file.TransferSyntax.Name,
		ExplicitVR: file.TransferSyntax.ExplicitVR,
		Endianness: endiannessForSyntax(file.TransferSyntax),
	}
	if resolution, ok := file.TransferSyntaxResolution(); ok && resolution.Inferred() {
		transferSyntax.Inferred = true
		transferSyntax.Source = string(resolution.Source)
		transferSyntax.Confidence = resolution.Confidence
	}

	data, err := json.MarshalIndent(fileJSON{
		TransferSyntax: transferSyntax,
		FileMeta:       meta,
		DataSet:        dataset,
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
