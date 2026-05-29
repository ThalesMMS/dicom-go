package object

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dcmtime"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrMissingPreamble                 = errors.New("dicom: missing Part 10 preamble or DICM marker")
	ErrMissingTransferSyntax           = errors.New("dicom: missing TransferSyntaxUID in file meta information")
	ErrMissingSOPClassUID              = errors.New("dicom: missing SOP Class UID")
	ErrMissingSOPInstanceUID           = errors.New("dicom: missing SOP Instance UID")
	ErrNilFile                         = errors.New("dicom: nil file")
	ErrFileMeta                        = errors.New("dicom: file meta error")
	ErrDataSet                         = errors.New("dicom: data set error")
	ErrDeferredSequenceValueWrite      = errors.New("dicom: writing deferred values nested in sequences is unsupported")
	ErrInvalidFileMetaGroupLength      = errors.New("dicom: invalid file meta group length element")
	ErrInvalidFileMetaGroupLengthValue = errors.New("dicom: invalid file meta group length value")
	ErrInvalidPreambleLength           = errors.New("dicom: invalid Part 10 preamble length")
	ErrDeferredValueRequiresSeekable   = errors.New("dicom: deferred value reading requires an io.ReadSeeker source")
)

const mediaStorageDirectoryStorageUID = "1.2.840.10008.1.3.10"

type File struct {
	Preamble       []byte
	Meta           *Object
	Dataset        *Object
	TransferSyntax transfer.Syntax

	source                   io.Closer
	transferSyntaxResolution *TransferSyntaxResolution
}

type WriteFileOptions struct {
	// Preamble provides the 128-byte Part 10 preamble. A nil value writes a
	// zero-filled preamble.
	Preamble []byte
	// DefaultMissingTransferSyntax enables a controlled fallback for incomplete
	// files that have neither File.TransferSyntax nor file meta
	// TransferSyntaxUID. When enabled, WriteFileWithOptions writes the dataset
	// using Implicit VR Little Endian. The zero value keeps strict validation.
	DefaultMissingTransferSyntax bool
	// OverrideMissingTransferSyntax selects an explicit fallback for incomplete
	// files that have neither File.TransferSyntax nor file meta
	// TransferSyntaxUID. When set, it is resolved through the transfer syntax
	// registry and takes precedence over DefaultMissingTransferSyntax.
	OverrideMissingTransferSyntax string
	// OmitReconciledDataSetSOPUIDs preserves the absence of SOP Class UID and
	// SOP Instance UID in the encoded data set while still requiring them in
	// File Meta Information. This is intended for the Basic Directory IOD,
	// whose File-set UID is conveyed as the Media Storage SOP Instance UID.
	// The zero value retains the historical reconciliation behavior.
	OmitReconciledDataSetSOPUIDs bool
}

var (
	tagFileMetaInformationGroupLength = core.NewTag(0x0002, 0x0000)
	tagFileMetaInformationVersion     = core.NewTag(0x0002, 0x0001)
	tagMediaStorageSOPClassUID        = core.NewTag(0x0002, 0x0002)
	tagMediaStorageSOPInstanceUID     = core.NewTag(0x0002, 0x0003)
	tagTransferSyntaxUID              = core.NewTag(0x0002, 0x0010)
	tagImplementationClassUID         = core.NewTag(0x0002, 0x0012)

	tagSOPClassUID    = core.NewTag(0x0008, 0x0016)
	tagSOPInstanceUID = core.NewTag(0x0008, 0x0018)
	tagWaveformData   = core.NewTag(0x5400, 0x1010)
)

const (
	part10PreambleLength   = 128
	part10Magic            = "DICM"
	implementationClassUID = "1.2.826.0.1.3680043.10.543.1"
)

func (f *File) Get(tag core.Tag) (core.Element, bool) {
	return f.objectForTag(tag).Get(tag)
}

func (f *File) Has(tag core.Tag) bool {
	return f.objectForTag(tag).Has(tag)
}

func (f *File) GetRaw(tag core.Tag) ([]byte, bool) {
	return f.objectForTag(tag).GetRaw(tag)
}

func (f *File) GetString(tag core.Tag) (string, bool) {
	return f.objectForTag(tag).GetString(tag)
}

func (f *File) GetStrings(tag core.Tag) ([]string, bool) {
	return f.objectForTag(tag).GetStrings(tag)
}

func (f *File) LookupString(tag core.Tag) (string, error) {
	return f.objectForTag(tag).LookupString(tag)
}

func (f *File) LookupStrings(tag core.Tag) ([]string, error) {
	return f.objectForTag(tag).LookupStrings(tag)
}

func (f *File) CharacterSet() (encoding.SpecificCharacterSet, error) {
	if f == nil {
		return encoding.SpecificCharacterSet{}, fmt.Errorf("dicom: nil file")
	}
	if f.Dataset == nil {
		return encoding.SpecificCharacterSet{}, fmt.Errorf("dicom: file has no dataset")
	}
	return f.Dataset.CharacterSet()
}

func (f *File) GetPersonName(tag core.Tag) (core.PersonName, bool) {
	return f.objectForTag(tag).GetPersonName(tag)
}

func (f *File) GetPersonNames(tag core.Tag) ([]core.PersonName, bool) {
	return f.objectForTag(tag).GetPersonNames(tag)
}

func (f *File) GetUID(tag core.Tag) (string, bool) {
	return f.objectForTag(tag).GetUID(tag)
}

func (f *File) GetUIDs(tag core.Tag) ([]string, bool) {
	return f.objectForTag(tag).GetUIDs(tag)
}

func (f *File) GetInt(tag core.Tag) (int64, error) {
	return f.objectForTag(tag).GetInt(tag)
}

func (f *File) GetInts(tag core.Tag) ([]int64, error) {
	return f.objectForTag(tag).GetInts(tag)
}

func (f *File) GetFloat(tag core.Tag) (float64, error) {
	return f.objectForTag(tag).GetFloat(tag)
}

func (f *File) GetFloats(tag core.Tag) ([]float64, error) {
	return f.objectForTag(tag).GetFloats(tag)
}

func (f *File) LookupDate(tag core.Tag) (dcmtime.Date, error) {
	return f.objectForTag(tag).LookupDate(tag)
}

func (f *File) LookupTime(tag core.Tag) (dcmtime.Time, error) {
	return f.objectForTag(tag).LookupTime(tag)
}

func (f *File) LookupDateTime(tag core.Tag) (dcmtime.Datetime, error) {
	return f.objectForTag(tag).LookupDateTime(tag)
}

func (f *File) GetSequence(tag core.Tag) ([]*Object, bool) {
	return f.objectForTag(tag).GetSequence(tag)
}

func (f *File) SetTextOptions(opts TextOptions) {
	if f == nil {
		return
	}
	if f.Meta != nil {
		f.Meta.SetTextOptions(opts)
	}
	if f.Dataset != nil {
		f.Dataset.SetTextOptions(opts)
	}
}

// ReadFileOptions configures high-level Part 10 reading.
//
// Example:
//
//	file, err := object.ReadFileWithOptions(r, object.ReadFileOptions{
//		MaxElementBytes: 16 << 20,
//		MaxTotalBytes:   128 << 20,
//		MaxSequenceDepth: 32,
//		MaxElements:     100000,
//		MaxFragments:    10000,
//	})
//
// All numeric limit fields default to zero, which means unlimited.
type ReadFileOptions struct {
	// Dictionary controls the dictionary used when parsing the main dataset.
	// A nil value defaults to dictionary/std.Dictionary.
	Dictionary dictionary.DataDictionary
	// FileMetaDictionary controls the dictionary used when parsing the Part 10
	// file meta information. A nil value defaults to dictionary/std.Dictionary.
	FileMetaDictionary dictionary.DataDictionary

	// MaxElementBytes limits a single element value allocation in bytes.
	// A zero value means unlimited.
	MaxElementBytes int64
	// InlineValueBytesThreshold controls when the underlying parser will stop
	// materializing defined-length primitive element values into memory.
	//
	// If > 0 and an element value length is strictly greater than this
	// threshold, its bytes will be consumed but the returned core.Element will
	// have a nil Value (metadata-only for that element).
	// CopyValueTo can stream those skipped bytes only when the reader has a
	// seekable source with an attached value provider. ReadFileWithOptions
	// disables this threshold for non-seekable Part 10 inputs and materializes
	// values instead.
	// General SQ replay is intentionally unsupported: sequence item values are
	// materialized even when this threshold is set.
	//
	// A zero value preserves historical behavior of always materializing
	// defined-length values (subject to MaxElementBytes).
	InlineValueBytesThreshold int64
	// MaxTotalBytes limits the absolute logical extent consumed by each parser
	// pass. A zero value means unlimited. Explicit multi-pass recovery can reread
	// a seekable prefix; its separate probe remains capped by this value and by
	// TransferSyntaxProbeOptions.MaxProbeBytes.
	MaxTotalBytes int64
	// MaxSequenceDepth limits combined sequence and item nesting depth.
	// A zero value means unlimited.
	MaxSequenceDepth int
	// MaxElements limits the number of primitive elements read by each internal
	// parser.Reader used by ReadFileWithOptions: one for file meta and one for
	// the main data set. A zero value means unlimited. MaxTotalBytes remains
	// absolute across both readers because each countingReader starts from
	// BaseOffset.
	MaxElements int
	// MaxFragments limits encapsulated Pixel Data fragments, excluding the Basic
	// Offset Table, per internal parser.Reader instance. In practice this means
	// the main data set reader enforces it, while the file meta reader never
	// encounters fragments. A zero value means unlimited.
	MaxFragments int
	// SkipPixelData consumes Pixel Data values without materializing their bytes.
	// The Pixel Data element remains present with a nil Value.
	SkipPixelData bool
	// DeferPixelData consumes integer, Float, and Double Float Pixel Data values
	// without materializing their bytes and keeps the original seekable source
	// available for CopyValueTo/WriteFile round-trips. This option requires an
	// io.ReadSeeker source.
	DeferPixelData bool
	// DeferWaveformData consumes Waveform Data (5400,1010) values without
	// materializing their bytes, including values nested in Waveform Sequence
	// items. File.ValueLocations returns their source offsets in occurrence
	// order together with the nearest enclosing item offset. This option
	// requires an io.ReadSeeker source.
	DeferWaveformData bool
	// FrameSink receives native Pixel Data frames while the parser reads them.
	// Native Pixel Data is not materialized when a sink is configured.
	FrameSink FrameSink
	// SkipProcessingPixelDataValue is kept for ParseOption source compatibility.
	//
	// Deprecated: this option has no effect. dicom-go preserves raw Pixel Data
	// bytes by default. Use SkipPixelData, DeferPixelData or FrameSink when the
	// reader should skip, defer or stream Pixel Data values.
	SkipProcessingPixelDataValue bool
	// AllowMissingMetaElementGroupLength accepts Part 10 file meta information
	// that starts with other group 0002 elements instead of (0002,0000).
	AllowMissingMetaElementGroupLength bool
	// AllowMismatchPixelDataLength is kept for ParseOption source compatibility.
	//
	// Deprecated: this option has no effect. dicom-go preserves raw Pixel Data
	// during reading and validates native frame sizes only when callers
	// explicitly extract or stream frames.
	AllowMismatchPixelDataLength bool
	// SkipMetadataReadOnNewParserInit is kept for ParseOption source compatibility.
	// Part 10 readers still require file meta information.
	SkipMetadataReadOnNewParserInit bool
	// TextOptions configures decoding of textual values in the returned objects.
	TextOptions TextOptions
	// StrictReservedBytes rejects explicit VR reserved bytes that are not zero.
	// The zero value keeps permissive behavior.
	StrictReservedBytes bool
	// OddLengthPolicy controls how odd element lengths are handled.
	// The zero value uses the parser default.
	OddLengthPolicy parser.OddLengthPolicy
}

func OpenFile(path string) (*File, error) {
	return OpenFileWithOptions(path, ReadFileOptions{})
}

func OpenFileWithOptions(path string, opts ReadFileOptions) (result *File, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	closeFile := true
	defer func() {
		if !closeFile {
			return
		}
		if cerr := f.Close(); cerr != nil {
			if err == nil {
				err = cerr
			} else {
				err = errors.Join(err, cerr)
			}
		}
	}()
	result, err = ReadFileWithOptions(f, opts)
	if err == nil && result != nil && result.Dataset != nil && result.Dataset.valueProvider != nil && result.Dataset.deferredCount > 0 {
		result.source = f
		closeFile = false
	}
	return result, err
}

// Close releases any source handle kept alive for deferred value streaming.
func (f *File) Close() error {
	if f == nil || f.source == nil {
		return nil
	}
	err := f.source.Close()
	f.source = nil
	return err
}

// ValueLocations returns all deferred raw value locations for tag in dataset
// source order. The returned slice is a copy and can be mutated by the caller.
func (f *File) ValueLocations(tag core.Tag) []parser.ValueLocation {
	if f == nil || f.Dataset == nil {
		return nil
	}
	provider, ok := f.Dataset.valueProvider.(*readerValueProvider)
	if !ok || provider == nil || provider.reader == nil {
		return nil
	}
	return provider.reader.ValueLocations(tag)
}

func ReadFile(r io.Reader) (*File, error) {
	return ReadFileWithOptions(r, ReadFileOptions{})
}

func ReadFileWithOptions(r io.Reader, opts ReadFileOptions) (*File, error) {
	// Preserve ReadFile's historical zero-value behavior.
	if opts.FileMetaDictionary == nil {
		opts.FileMetaDictionary = std.Dictionary
	}
	if opts.Dictionary == nil {
		opts.Dictionary = std.Dictionary
	}
	seekableSource, readerSourceSeekable := r.(io.ReadSeeker)
	if opts.DeferPixelData || opts.DeferWaveformData {
		if !readerSourceSeekable {
			return nil, ErrDeferredValueRequiresSeekable
		}
	}

	var (
		preamble      []byte
		meta          *Object
		syntax        transfer.Syntax
		datasetOffset int64
		readerSource  io.Reader
	)
	if readerSourceSeekable {
		streamBase, err := seekableSource.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, err
		}
		header, err := ReadPart10Header(seekableSource, opts)
		if err != nil {
			return nil, err
		}
		preamble = header.Preamble
		meta = header.Meta
		syntax = header.TransferSyntax
		datasetOffset = header.DataSetOffset - streamBase
		readerSource = seekableSource
	} else {
		br := bufio.NewReader(r)
		prefix := make([]byte, part10PreambleLength+len(part10Magic))
		if _, err := io.ReadFull(br, prefix); err != nil {
			return nil, err
		}
		if string(prefix[part10PreambleLength:]) != part10Magic {
			return nil, ErrMissingPreamble
		}
		var err error
		meta, syntax, datasetOffset, err = readFileMeta(br, int64(len(prefix)), opts)
		if err != nil {
			return nil, wrapFileMetaError(err)
		}
		preamble = prefix[:part10PreambleLength]
		readerSource = br
	}
	readerReadOpts := opts
	if syntax.Deflated {
		if readerReadOpts.DeferPixelData || readerReadOpts.DeferWaveformData {
			return nil, wrapDataSetError(errDeflatedDeferredValue())
		}
		inflated := flate.NewReader(readerSource)
		defer func() { _ = inflated.Close() }()
		readerSource = inflated
		readerSourceSeekable = false
	}
	if !readerSourceSeekable {
		readerReadOpts.InlineValueBytesThreshold = 0
	}
	readerOpts := readerReadOpts.parserReaderOptions(datasetOffset)
	reader := parser.NewReader(readerSource, syntax, readerOpts)
	dataset, err := reader.ReadDataSet()
	if err != nil {
		return nil, wrapDataSetError(err)
	}
	datasetObject := fromParsedDataSetWithTextOptions(dataset, readerOpts.Dictionary, opts.TextOptions)
	datasetObject.SetValueByteOrder(syntax.ByteOrder)
	file := &File{
		Preamble:       preamble,
		Meta:           meta,
		Dataset:        datasetObject,
		TransferSyntax: syntax,
	}
	if readerOptionsNeedValueProvider(readerOpts) && readerSourceSeekable && file.Dataset.deferredCount > 0 {
		file.Dataset.setValueProvider(&readerValueProvider{reader: reader})
	}
	return file, nil
}

func readFileMeta(r *bufio.Reader, baseOffset int64, opts ReadFileOptions) (*Object, transfer.Syntax, int64, error) {
	meta, datasetOffset, err := readFileMetaElements(r, baseOffset, opts)
	if err != nil {
		return nil, transfer.Syntax{}, 0, err
	}
	syntax, err := resolveFileMetaTransferSyntax(meta)
	if err != nil {
		return nil, transfer.Syntax{}, 0, err
	}
	return meta, syntax, datasetOffset, nil
}

// readFileMetaElements parses only the Part 10 File Meta structure. Keeping
// syntax resolution separate lets the explicit recovery API preserve the
// original meta object and exact dataset offset when the UID itself is absent
// or unknown. The strict reader above intentionally retains its historical
// return shape and errors.
func readFileMetaElements(r *bufio.Reader, baseOffset int64, opts ReadFileOptions) (*Object, int64, error) {
	metaOpts := opts
	metaOpts.InlineValueBytesThreshold = 0
	metaOpts.SkipPixelData = false
	metaOpts.DeferPixelData = false
	metaOpts.DeferWaveformData = false
	metaOpts.FrameSink = nil
	if opts.FileMetaDictionary != nil {
		metaOpts.Dictionary = opts.FileMetaDictionary
	}
	metaReaderOpts := metaOpts.parserReaderOptions(baseOffset)
	metaReader := parser.NewReader(r, transfer.ExplicitVRLittleEndian, metaReaderOpts)
	if opts.AllowMissingMetaElementGroupLength {
		return readFileMetaElementsByGroup(r, metaReader, metaReaderOpts)
	}
	first, err := metaReader.Next()
	if err != nil {
		return nil, 0, err
	}
	if first.Element.Tag() != core.NewTag(0x0002, 0x0000) {
		return nil, 0, ErrInvalidFileMetaGroupLength
	}
	raw, ok := first.Element.RawBytes()
	if !ok || len(raw) != 4 {
		return nil, 0, ErrInvalidFileMetaGroupLengthValue
	}
	metaLen := binary.LittleEndian.Uint32(raw)
	metaElements := []core.Element{first.Element}
	metaReader = parser.NewReader(io.LimitReader(r, int64(metaLen)), transfer.ExplicitVRLittleEndian, metaOpts.parserReaderOptions(metaReader.Position()))
	more, err := metaReader.ReadAll()
	if err != nil {
		return nil, 0, err
	}
	metaElements = append(metaElements, more...)
	meta := FromDataSet(core.DataSet{Elements: metaElements}, metaReaderOpts.Dictionary)
	return meta, metaReader.Position(), nil
}

func readFileMetaElementsByGroup(r *bufio.Reader, metaReader *parser.Reader, metaReaderOpts parser.ReaderOptions) (*Object, int64, error) {
	var metaElements []core.Element
	for {
		next, err := r.Peek(4)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, 0, err
		}
		if binary.LittleEndian.Uint16(next[:2]) != 0x0002 {
			break
		}
		tok, err := metaReader.Next()
		if err != nil {
			return nil, 0, err
		}
		if tok.Kind != parser.TokenElement {
			return nil, 0, fmt.Errorf("dicom: unexpected file meta token %s", tok.Kind)
		}
		metaElements = append(metaElements, tok.Element)
	}
	if len(metaElements) == 0 {
		return nil, 0, ErrInvalidFileMetaGroupLength
	}
	meta := FromDataSet(core.DataSet{Elements: metaElements}, metaReaderOpts.Dictionary)
	return meta, metaReader.Position(), nil
}

func resolveFileMetaTransferSyntax(meta *Object) (transfer.Syntax, error) {
	uid, ok := meta.GetString(core.NewTag(0x0002, 0x0010))
	if !ok {
		return transfer.Syntax{}, ErrMissingTransferSyntax
	}
	uid = transfer.NormalizeUID(uid)
	if uid == "" {
		return transfer.Syntax{}, ErrMissingTransferSyntax
	}
	syntax, ok := transfer.DefaultRegistry.Get(uid)
	if !ok {
		return transfer.Syntax{}, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, uid)
	}
	if !syntax.Supported {
		return transfer.Syntax{}, unsupportedTransferSyntaxError("reader", syntax)
	}
	return syntax, nil
}

func ReadDataSet(r io.Reader, syntax transfer.Syntax) (*Object, error) {
	return ReadDataSetWithOptions(r, syntax, ReadFileOptions{})
}

func ReadDataSetWithOptions(r io.Reader, syntax transfer.Syntax, opts ReadFileOptions) (*Object, error) {
	syntax, err := validateReadableSyntax(syntax)
	if err != nil {
		return nil, err
	}
	_, seekable := r.(io.ReadSeeker)
	if (opts.DeferPixelData || opts.DeferWaveformData) && !seekable {
		return nil, ErrDeferredValueRequiresSeekable
	}
	obj, _, err := readDataSetObject(r, syntax, opts, 0, seekable)
	return obj, err
}

func readDataSetObject(r io.Reader, syntax transfer.Syntax, opts ReadFileOptions, baseOffset int64, streamValues bool) (*Object, *parser.Reader, error) {
	readerSource := r
	if syntax.Deflated {
		if opts.DeferPixelData || opts.DeferWaveformData {
			return nil, nil, errDeflatedDeferredValue()
		}
		inflated := flate.NewReader(r)
		defer func() { _ = inflated.Close() }()
		readerSource = inflated
		streamValues = false
	}
	readerOpts := opts.datasetParserReaderOptions(baseOffset, streamValues)
	reader := parser.NewReader(readerSource, syntax, readerOpts)
	dataset, err := reader.ReadDataSet()
	if err != nil {
		return nil, reader, err
	}
	obj := fromParsedDataSetWithTextOptions(dataset, readerOpts.Dictionary, opts.TextOptions)
	obj.SetValueByteOrder(syntax.ByteOrder)
	if readerOptionsNeedValueProvider(readerOpts) && streamValues && obj.deferredCount > 0 {
		obj.setValueProvider(&readerValueProvider{reader: reader})
	}
	return obj, reader, nil
}

func OpenDataSet(path string, syntax transfer.Syntax) (*Object, error) {
	return OpenDataSetWithOptions(path, syntax, ReadFileOptions{})
}

func OpenDataSetWithOptions(path string, syntax transfer.Syntax, opts ReadFileOptions) (result *Object, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	closeFile := true
	defer func() {
		if !closeFile {
			return
		}
		if cerr := f.Close(); cerr != nil {
			if err == nil {
				err = cerr
			} else {
				err = errors.Join(err, cerr)
			}
		}
	}()
	result, err = ReadDataSetWithOptions(f, syntax, opts)
	if err == nil && result != nil && result.valueProvider != nil && result.deferredCount > 0 {
		result.source = f
		closeFile = false
	}
	return result, err
}

func WriteFile(w io.Writer, file *File) error {
	return WriteFileWithOptions(w, file, WriteFileOptions{})
}

// RebuildFileMeta resynchronizes the file-meta object with the current dataset
// and transfer syntax without copying the dataset or reparsing the whole file.
func (f *File) RebuildFileMeta() error {
	if f == nil {
		return ErrNilFile
	}
	syntax, err := resolveWriteTransferSyntax(f)
	if err != nil {
		return err
	}
	meta, err := buildFileMetaElements(f, syntax)
	if err != nil {
		return err
	}
	f.Meta = FromElements(meta, std.Dictionary)
	f.TransferSyntax = syntax
	return nil
}

func WriteFileWithOptions(w io.Writer, file *File, opts WriteFileOptions) error {
	if file == nil {
		return fmt.Errorf("dicom: nil file")
	}
	if opts.Preamble != nil && len(opts.Preamble) != part10PreambleLength {
		return ErrInvalidPreambleLength
	}

	syntax, err := resolveWriteTransferSyntaxWithOptions(file, opts)
	if err != nil {
		return err
	}
	meta, err := prepareFileMeta(file, syntax)
	if err != nil {
		return err
	}
	dataset, err := prepareWritableDataSet(file, meta.elements, opts.OmitReconciledDataSetSOPUIDs)
	if err != nil {
		return err
	}

	preamble := opts.Preamble
	if preamble == nil {
		switch len(file.Preamble) {
		case 0:
			preamble = make([]byte, part10PreambleLength)
		case part10PreambleLength:
			preamble = file.Preamble
		default:
			return ErrInvalidPreambleLength
		}
	}
	buffered := bufio.NewWriter(w)
	if err := writeAll(buffered, preamble); err != nil {
		return err
	}
	if err := writeAll(buffered, []byte(part10Magic)); err != nil {
		return err
	}

	if err := writeAll(buffered, meta.encoded); err != nil {
		return err
	}

	if err := writeDataSet(buffered, dataset, syntax); err != nil {
		return err
	}
	return buffered.Flush()
}

func WriteDataSet(w io.Writer, obj *Object, syntax transfer.Syntax) (err error) {
	buffered := bufio.NewWriter(w)
	if err := writeDataSet(buffered, obj, syntax); err != nil {
		return err
	}
	return buffered.Flush()
}

func writeDataSet(w io.Writer, obj *Object, syntax transfer.Syntax) (err error) {
	if obj == nil {
		return fmt.Errorf("dicom: nil object passed to WriteDataSet")
	}
	if hasDeferredSequenceValue(obj) {
		return ErrDeferredSequenceValueWrite
	}
	characterSet, err := obj.characterSetForVR(core.VRLO)
	if err != nil {
		return fmt.Errorf("dicom: resolve dataset Specific Character Set for writing: %w", err)
	}
	syntax, err = validateWritableSyntax(syntax)
	if err != nil {
		return err
	}
	if syntax.Deflated {
		deflater, err := flate.NewWriter(w, flate.DefaultCompression)
		if err != nil {
			return err
		}
		defer func() {
			if cerr := deflater.Close(); cerr != nil {
				if err == nil {
					err = cerr
				} else {
					err = errors.Join(err, cerr)
				}
			}
		}()
		w = deflater
	}
	writer := parser.NewWriterWithOptions(w, syntax, parser.WriterOptions{CharacterSet: characterSet})
	for _, el := range obj.SortedElements() {
		if el.Value == nil {
			if err := writeDeferredElement(writer, obj, el); err != nil {
				return err
			}
			continue
		}
		if err := writer.WriteElement(el); err != nil {
			return err
		}
	}
	return nil
}

func hasDeferredSequenceValue(obj *Object) bool {
	if obj == nil {
		return false
	}
	provider, ok := obj.valueProvider.(*readerValueProvider)
	if !ok || provider == nil || provider.reader == nil {
		return false
	}
	hasNestedLocation := false
	for _, location := range provider.reader.ValueLocations(tagWaveformData) {
		if location.ItemOffsetSet {
			hasNestedLocation = true
			break
		}
	}
	return hasNestedLocation && hasNilSequenceTag(obj.Elements(), tagWaveformData)
}

func hasNilSequenceTag(elements []core.Element, tag core.Tag) bool {
	for _, element := range elements {
		sequence, ok := element.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for _, item := range sequence.Items {
			for _, child := range item.Elements {
				if child.Tag() == tag && child.Value == nil {
					return true
				}
				if hasNilSequenceTag([]core.Element{child}, tag) {
					return true
				}
			}
		}
	}
	return false
}

func (f *File) objectForTag(tag core.Tag) *Object {
	if f == nil {
		return nil
	}
	if tag.Group == 0x0002 {
		return f.Meta
	}
	return f.Dataset
}

func (o ReadFileOptions) parserReaderOptions(baseOffset int64) parser.ReaderOptions {
	dict := std.Dictionary
	if o.Dictionary != nil {
		dict = o.Dictionary
	}
	return parser.ReaderOptions{
		Dictionary:                dict,
		MaxElementBytes:           o.MaxElementBytes,
		MaxTotalBytes:             o.MaxTotalBytes,
		BaseOffset:                baseOffset,
		MaxSequenceDepth:          o.MaxSequenceDepth,
		MaxElements:               o.MaxElements,
		MaxFragments:              o.MaxFragments,
		InlineValueBytesThreshold: o.InlineValueBytesThreshold,
		StrictReservedBytes:       o.StrictReservedBytes,
		OddLengthPolicy:           o.OddLengthPolicy,
		SkipPixelData:             o.SkipPixelData,
		DeferPixelData:            o.DeferPixelData,
		DeferWaveformData:         o.DeferWaveformData,
		FrameSink:                 o.FrameSink,
	}
}

func (o ReadFileOptions) datasetParserReaderOptions(baseOffset int64, streamValues bool) parser.ReaderOptions {
	opts := o.parserReaderOptions(baseOffset)
	if !streamValues {
		opts.InlineValueBytesThreshold = 0
	}
	return opts
}

type preparedFileMeta struct {
	elements []core.Element
	encoded  []byte
}

func prepareFileMeta(file *File, syntax transfer.Syntax) (preparedFileMeta, error) {
	elements, err := buildFileMetaElements(file, syntax)
	if err != nil {
		return preparedFileMeta{}, err
	}
	var encoded bytes.Buffer
	metaWriter := parser.NewWriter(&encoded, transfer.ExplicitVRLittleEndian)
	for _, el := range elements {
		if err := metaWriter.WriteElement(el); err != nil {
			return preparedFileMeta{}, err
		}
	}
	return preparedFileMeta{elements: elements, encoded: encoded.Bytes()}, nil
}

func buildFileMetaElements(file *File, syntax transfer.Syntax) ([]core.Element, error) {
	metaByTag := make(map[core.Tag]core.Element)
	if file != nil && file.Meta != nil {
		for _, el := range file.Meta.Elements() {
			if el.Tag().Group != 0x0002 {
				continue
			}
			metaByTag[el.Tag()] = el
		}
	}

	sopClassUID, err := resolveFileMetaUID(file, metaByTag, tagSOPClassUID, tagMediaStorageSOPClassUID, ErrMissingSOPClassUID)
	if err != nil {
		return nil, err
	}
	sopInstanceUID, err := resolveFileMetaUID(file, metaByTag, tagSOPInstanceUID, tagMediaStorageSOPInstanceUID, ErrMissingSOPInstanceUID)
	if err != nil {
		return nil, err
	}

	metaByTag[tagMediaStorageSOPClassUID] = newStringElement(tagMediaStorageSOPClassUID, core.VRUI, sopClassUID)
	metaByTag[tagMediaStorageSOPInstanceUID] = newStringElement(tagMediaStorageSOPInstanceUID, core.VRUI, sopInstanceUID)
	metaByTag[tagTransferSyntaxUID] = newStringElement(tagTransferSyntaxUID, core.VRUI, syntax.UID)

	implementationUID := implementationClassUID
	if existing := normalizedElementString(metaByTag[tagImplementationClassUID]); existing != "" {
		implementationUID = existing
	}
	metaByTag[tagImplementationClassUID] = newStringElement(tagImplementationClassUID, core.VRUI, implementationUID)

	if _, ok := metaByTag[tagFileMetaInformationVersion]; !ok {
		metaByTag[tagFileMetaInformationVersion] = core.NewRawElement(tagFileMetaInformationVersion, core.VROB, []byte{0x00, 0x01})
	}

	meta := make([]core.Element, 0, len(metaByTag))
	for tag, el := range metaByTag {
		if tag == tagFileMetaInformationGroupLength {
			continue
		}
		meta = append(meta, el)
	}
	sortElementsByTag(meta)

	var groupLength uint64
	for _, el := range meta {
		valueLength, ok := el.CalculatedLength()
		if !ok || valueLength.IsUndefined() {
			return nil, fmt.Errorf("dicom: file meta element %s has no defined length", el.Tag())
		}
		headerLength := uint64(8)
		if el.VR().UsesLongExplicitLength() {
			headerLength = 12
		} else if uint64(valueLength) > uint64(^uint16(0)) {
			return nil, fmt.Errorf("dicom: file meta element %s length %d exceeds uint16", el.Tag(), valueLength)
		}
		groupLength += headerLength + uint64(valueLength)
	}
	if groupLength > uint64(^uint32(0)) {
		return nil, fmt.Errorf("dicom: file meta group length %d exceeds uint32", groupLength)
	}

	groupLengthElement := ulElementLittleEndian(tagFileMetaInformationGroupLength, uint32(groupLength))
	elements := make([]core.Element, len(meta)+1)
	elements[0] = groupLengthElement
	copy(elements[1:], meta)

	return elements, nil
}

func prepareWritableDataSet(file *File, meta []core.Element, omitReconciledSOPUIDs bool) (*Object, error) {
	dict := std.Dictionary
	if file != nil && file.Dataset != nil && file.Dataset.dict != nil {
		dict = file.Dataset.dict
	}

	dataset := New(dict)
	if file != nil && file.Dataset != nil {
		dataset = cloneObjectForWrite(file.Dataset, dict)
	}

	metaObject := FromElements(meta, dict)
	if omitReconciledSOPUIDs {
		mediaClass, ok := metaObject.GetUID(tagMediaStorageSOPClassUID)
		if !ok || transfer.NormalizeUID(mediaClass) != mediaStorageDirectoryStorageUID {
			return nil, fmt.Errorf("dicom: omission of reconciled dataset SOP UIDs requires Media Storage Directory Storage")
		}
		return dataset, nil
	}

	injected := false
	for _, pair := range []struct {
		datasetTag core.Tag
		metaTag    core.Tag
	}{
		{datasetTag: tagSOPClassUID, metaTag: tagMediaStorageSOPClassUID},
		{datasetTag: tagSOPInstanceUID, metaTag: tagMediaStorageSOPInstanceUID},
	} {
		if value, ok := dataset.GetString(pair.datasetTag); ok && transfer.NormalizeUID(value) != "" {
			continue
		}
		value, ok := metaObject.GetString(pair.metaTag)
		if !ok || transfer.NormalizeUID(value) == "" {
			return nil, fmt.Errorf("dicom: missing reconciled dataset value for %s", pair.datasetTag)
		}
		dataset.Put(newStringElement(pair.datasetTag, core.VRUI, transfer.NormalizeUID(value)))
		injected = true
	}

	if injected {
		sorted := FromElementsWithTextOptions(dataset.SortedElements(), dict, dataset.text)
		sorted.SetValueByteOrder(dataset.ValueByteOrder())
		sorted.valueProvider = dataset.valueProvider
		return sorted, nil
	}
	return dataset, nil
}

func cloneObjectForWrite(source *Object, dict dictionary.DataDictionary) *Object {
	if source == nil {
		return New(dict)
	}
	clone := FromElementsWithTextOptions(source.Elements(), dict, source.text)
	clone.SetValueByteOrder(source.ValueByteOrder())
	clone.valueProvider = source.valueProvider
	return clone
}

func writeDeferredElement(writer *parser.Writer, obj *Object, el core.Element) error {
	if obj == nil || obj.valueProvider == nil {
		return fmt.Errorf("dicom: element %s has deferred value but no value provider", el.Tag())
	}
	return writer.WriteDeferredElement(el, func(w io.Writer) (int64, error) {
		return obj.CopyValueTo(el.Tag(), w)
	})
}

func readerOptionsNeedValueProvider(opts parser.ReaderOptions) bool {
	return opts.InlineValueBytesThreshold > 0 || opts.DeferPixelData || opts.DeferWaveformData || opts.FrameSink != nil
}

func errDeflatedDeferredValue() error {
	return fmt.Errorf("dicom: deflated datasets cannot preserve encoded value offsets for deferred value reading: %w", ErrDeferredValueRequiresSeekable)
}

type readStageError struct {
	stage error
	err   error
}

func (e *readStageError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s: %v", e.stage, e.err)
}

func (e *readStageError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return []error{e.stage, e.err}
}

func wrapFileMetaError(err error) error {
	if err == nil || errors.Is(err, ErrFileMeta) {
		return err
	}
	if errors.Is(err, ErrMissingTransferSyntax) ||
		errors.Is(err, transfer.ErrUnknownTransferSyntax) ||
		errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
		return err
	}
	return &readStageError{stage: ErrFileMeta, err: err}
}

func wrapDataSetError(err error) error {
	if err == nil || errors.Is(err, ErrDataSet) {
		return err
	}
	return &readStageError{stage: ErrDataSet, err: err}
}

func validateReadableSyntax(syntax transfer.Syntax) (transfer.Syntax, error) {
	syntax, known, err := canonicalizeTransferSyntax(syntax)
	if err != nil {
		return transfer.Syntax{}, err
	}
	if syntax.Supported || !known {
		return syntax, nil
	}
	return transfer.Syntax{}, unsupportedTransferSyntaxError("reader", syntax)
}

func validateWritableSyntax(syntax transfer.Syntax) (transfer.Syntax, error) {
	syntax, known, err := canonicalizeTransferSyntax(syntax)
	if err != nil {
		return transfer.Syntax{}, err
	}
	if transfer.NormalizeUID(syntax.UID) == "" {
		return transfer.Syntax{}, ErrMissingTransferSyntax
	}
	if syntax.Supported || !known {
		return syntax, nil
	}
	return transfer.Syntax{}, unsupportedTransferSyntaxError("writer", syntax)
}

func resolveWriteTransferSyntax(file *File) (transfer.Syntax, error) {
	return resolveWriteTransferSyntaxWithOptions(file, WriteFileOptions{})
}

func resolveWriteTransferSyntaxWithOptions(file *File, opts WriteFileOptions) (transfer.Syntax, error) {
	if file != nil {
		if uid := transfer.NormalizeUID(file.TransferSyntax.UID); uid != "" {
			syntax := file.TransferSyntax
			syntax.UID = uid
			return validateWritableSyntax(syntax)
		}
	}

	var uid string
	if file != nil && file.Meta != nil {
		if value, ok := file.Meta.GetString(tagTransferSyntaxUID); ok {
			uid = transfer.NormalizeUID(value)
		}
	}
	if uid == "" {
		if override := transfer.NormalizeUID(opts.OverrideMissingTransferSyntax); override != "" {
			return validateWritableSyntax(transfer.Syntax{UID: override})
		}
		if opts.OverrideMissingTransferSyntax != "" {
			return transfer.Syntax{}, fmt.Errorf("%w: empty OverrideMissingTransferSyntax", ErrMissingTransferSyntax)
		}
		if opts.DefaultMissingTransferSyntax {
			return transfer.ImplicitVRLittleEndian, nil
		}
		return transfer.Syntax{}, ErrMissingTransferSyntax
	}
	return validateWritableSyntax(transfer.Syntax{UID: uid})
}

func resolveFileMetaUID(file *File, metaByTag map[core.Tag]core.Element, datasetTag, metaTag core.Tag, missing error) (string, error) {
	if file != nil && file.Dataset != nil {
		if value, ok := file.Dataset.GetString(datasetTag); ok {
			if uid := transfer.NormalizeUID(value); uid != "" {
				return uid, nil
			}
		}
	}
	if value := normalizedElementString(metaByTag[metaTag]); value != "" {
		return value, nil
	}
	return "", missing
}

func normalizedElementString(el core.Element) string {
	return transfer.NormalizeUID(el.StringValue())
}

func newStringElement(tag core.Tag, vr core.VR, value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tag, VR: vr},
		Value:  core.StringValue{value},
	}
}

func ulElementLittleEndian(tag core.Tag, value uint32) core.Element {
	var raw [4]byte
	binary.LittleEndian.PutUint32(raw[:], value)
	return core.NewRawElement(tag, core.VRUL, raw[:])
}

func sortElementsByTag(elements []core.Element) {
	sort.Slice(elements, func(i, j int) bool {
		if elements[i].Tag().Group != elements[j].Tag().Group {
			return elements[i].Tag().Group < elements[j].Tag().Group
		}
		return elements[i].Tag().Element < elements[j].Tag().Element
	})
}

func writeAll(w io.Writer, buf []byte) error {
	for len(buf) > 0 {
		n, err := w.Write(buf)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
	}
	return nil
}

func canonicalizeTransferSyntax(syntax transfer.Syntax) (transfer.Syntax, bool, error) {
	uid := transfer.NormalizeUID(syntax.UID)
	if uid != "" {
		if resolved, ok := transfer.DefaultRegistry.Get(uid); ok {
			return resolved, true, nil
		}
		syntax.UID = uid
		if syntax.Deflated || !syntaxHasNativeEncodingHints(syntax) {
			return transfer.Syntax{}, false, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, uid)
		}
	}
	if syntax.ByteOrder == nil {
		syntax.ByteOrder = binary.LittleEndian
	}
	return syntax, false, nil
}

// syntaxHasNativeEncodingHints requires unknown transfer.Syntax values to carry
// enough native encoding information to proceed locally. ExplicitVR=true may
// omit ByteOrder because this implementation defaults explicit-VR syntaxes to
// LittleEndian, and the only explicit-VR BigEndian UID is retired. When
// ExplicitVR=false, callers must provide ByteOrder because an unknown Syntax
// UID does not let the reader infer endianness; ImplicitVR with ByteOrder==nil
// is rejected.
func syntaxHasNativeEncodingHints(syntax transfer.Syntax) bool {
	return syntax.ExplicitVR || syntax.ByteOrder != nil
}

func unsupportedTransferSyntaxError(operation string, syntax transfer.Syntax) error {
	if syntax.Name != "" {
		return fmt.Errorf("%w: transfer syntax %q (%s) is registered but not supported by this %s", transfer.ErrUnsupportedTransferSyntax, syntax.UID, syntax.Name, operation)
	}
	return fmt.Errorf("%w: transfer syntax %q is registered but not supported by this %s", transfer.ErrUnsupportedTransferSyntax, syntax.UID, operation)
}
