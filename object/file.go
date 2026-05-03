package object

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/ThalesMMS/dicom-go/core"
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
	ErrFileMeta                        = errors.New("dicom: file meta error")
	ErrDataSet                         = errors.New("dicom: data set error")
	ErrInvalidFileMetaGroupLength      = errors.New("dicom: invalid file meta group length element")
	ErrInvalidFileMetaGroupLengthValue = errors.New("dicom: invalid file meta group length value")
	ErrInvalidPreambleLength           = errors.New("dicom: invalid Part 10 preamble length")
)

type File struct {
	Preamble       []byte
	Meta           *Object
	Dataset        *Object
	TransferSyntax transfer.Syntax

	source io.Closer
}

type WriteFileOptions struct {
	// Preamble provides the 128-byte Part 10 preamble. A nil value writes a
	// zero-filled preamble.
	Preamble []byte
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
	//
	// A zero value preserves historical behavior of always materializing
	// defined-length values (subject to MaxElementBytes).
	InlineValueBytesThreshold int64
	// MaxTotalBytes limits the total number of bytes read from the source.
	// A zero value means unlimited.
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
	br := bufio.NewReader(r)
	prefix := make([]byte, 132)
	if _, err := io.ReadFull(br, prefix); err != nil {
		return nil, err
	}
	if string(prefix[128:132]) != "DICM" {
		return nil, ErrMissingPreamble
	}

	meta, syntax, datasetOffset, err := readFileMeta(br, int64(len(prefix)), opts)
	if err != nil {
		return nil, wrapFileMetaError(err)
	}
	readerSource := io.Reader(br)
	readerSourceSeekable := false
	if rs, ok := r.(io.ReadSeeker); ok {
		if _, err := rs.Seek(datasetOffset, io.SeekStart); err != nil {
			return nil, wrapDataSetError(err)
		}
		readerSource = rs
		readerSourceSeekable = true
	}
	readerReadOpts := opts
	if !readerSourceSeekable {
		readerReadOpts.InlineValueBytesThreshold = 0
	}
	readerOpts := readerReadOpts.parserReaderOptions(datasetOffset)
	reader := parser.NewReader(readerSource, syntax, readerOpts)
	dataset, err := reader.ReadDataSet()
	if err != nil {
		return nil, wrapDataSetError(err)
	}
	file := &File{
		Preamble:       prefix[:128],
		Meta:           meta,
		Dataset:        FromDataSet(dataset, readerOpts.Dictionary),
		TransferSyntax: syntax,
	}
	if readerOpts.InlineValueBytesThreshold > 0 && readerSourceSeekable && file.Dataset.deferredCount > 0 {
		file.Dataset.setValueProvider(&readerValueProvider{reader: reader})
	}
	return file, nil
}

func readFileMeta(r io.Reader, baseOffset int64, opts ReadFileOptions) (*Object, transfer.Syntax, int64, error) {
	metaOpts := opts
	metaOpts.InlineValueBytesThreshold = 0
	if opts.FileMetaDictionary != nil {
		metaOpts.Dictionary = opts.FileMetaDictionary
	}
	metaReaderOpts := metaOpts.parserReaderOptions(baseOffset)
	metaReader := parser.NewReader(r, transfer.ExplicitVRLittleEndian, metaReaderOpts)
	first, err := metaReader.Next()
	if err != nil {
		return nil, transfer.Syntax{}, 0, err
	}
	if first.Element.Tag() != core.NewTag(0x0002, 0x0000) {
		return nil, transfer.Syntax{}, 0, ErrInvalidFileMetaGroupLength
	}
	raw, ok := first.Element.RawBytes()
	if !ok || len(raw) != 4 {
		return nil, transfer.Syntax{}, 0, ErrInvalidFileMetaGroupLengthValue
	}
	metaLen := binary.LittleEndian.Uint32(raw)
	metaElements := []core.Element{first.Element}
	metaReader = parser.NewReader(io.LimitReader(r, int64(metaLen)), transfer.ExplicitVRLittleEndian, metaOpts.parserReaderOptions(metaReader.Position()))
	more, err := metaReader.ReadAll()
	if err != nil {
		return nil, transfer.Syntax{}, 0, err
	}
	metaElements = append(metaElements, more...)
	meta := FromDataSet(core.DataSet{Elements: metaElements}, metaReaderOpts.Dictionary)

	uid, ok := meta.GetString(core.NewTag(0x0002, 0x0010))
	if !ok {
		return nil, transfer.Syntax{}, 0, ErrMissingTransferSyntax
	}
	uid = transfer.NormalizeUID(uid)
	if uid == "" {
		return nil, transfer.Syntax{}, 0, ErrMissingTransferSyntax
	}
	syntax, ok := transfer.DefaultRegistry.Get(uid)
	if !ok {
		return nil, transfer.Syntax{}, 0, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, uid)
	}
	if !syntax.Supported {
		return nil, transfer.Syntax{}, 0, unsupportedTransferSyntaxError("reader", syntax)
	}
	return meta, syntax, metaReader.Position(), nil
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
	readerOpts := opts.datasetParserReaderOptions(0, seekable)
	reader := parser.NewReader(r, syntax, readerOpts)
	dataset, err := reader.ReadDataSet()
	if err != nil {
		return nil, err
	}
	obj := FromDataSet(dataset, readerOpts.Dictionary)
	if readerOpts.InlineValueBytesThreshold > 0 && obj.deferredCount > 0 {
		obj.setValueProvider(&readerValueProvider{reader: reader})
	}
	return obj, nil
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

func WriteFileWithOptions(w io.Writer, file *File, opts WriteFileOptions) error {
	if file == nil {
		return fmt.Errorf("dicom: nil file")
	}
	if opts.Preamble != nil && len(opts.Preamble) != part10PreambleLength {
		return ErrInvalidPreambleLength
	}

	syntax, err := resolveWriteTransferSyntax(file)
	if err != nil {
		return err
	}
	meta, err := prepareFileMeta(file, syntax)
	if err != nil {
		return err
	}
	dataset, err := prepareWritableDataSet(file, meta)
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
	if err := writeAll(w, preamble); err != nil {
		return err
	}
	if err := writeAll(w, []byte(part10Magic)); err != nil {
		return err
	}

	metaWriter := parser.NewWriter(w, transfer.ExplicitVRLittleEndian)
	for _, el := range meta {
		if err := metaWriter.WriteElement(el); err != nil {
			return err
		}
	}

	return WriteDataSet(w, dataset, syntax)
}

func WriteDataSet(w io.Writer, obj *Object, syntax transfer.Syntax) error {
	if obj == nil {
		return fmt.Errorf("dicom: nil object passed to WriteDataSet")
	}
	syntax, err := validateWritableSyntax(syntax)
	if err != nil {
		return err
	}
	writer := parser.NewWriter(w, syntax)
	for _, el := range obj.Elements() {
		if err := writer.WriteElement(el); err != nil {
			return err
		}
	}
	return nil
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
	}
}

func (o ReadFileOptions) datasetParserReaderOptions(baseOffset int64, streamValues bool) parser.ReaderOptions {
	opts := o.parserReaderOptions(baseOffset)
	if !streamValues {
		opts.InlineValueBytesThreshold = 0
	}
	return opts
}

func prepareFileMeta(file *File, syntax transfer.Syntax) ([]core.Element, error) {
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
		n, err := encodedElementLength(transfer.ExplicitVRLittleEndian, el)
		if err != nil {
			return nil, err
		}
		groupLength += uint64(n)
	}
	if groupLength > uint64(^uint32(0)) {
		return nil, fmt.Errorf("dicom: file meta group length %d exceeds uint32", groupLength)
	}

	meta = append(meta, ulElementLittleEndian(tagFileMetaInformationGroupLength, uint32(groupLength)))
	sortElementsByTag(meta)
	return meta, nil
}

func prepareWritableDataSet(file *File, meta []core.Element) (*Object, error) {
	dict := std.Dictionary
	if file != nil && file.Dataset != nil && file.Dataset.dict != nil {
		dict = file.Dataset.dict
	}

	dataset := New(dict)
	if file != nil && file.Dataset != nil {
		dataset = FromElements(file.Dataset.Elements(), dict)
	}

	metaObject := FromElements(meta, dict)
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
		return FromElements(dataset.SortedElements(), dict), nil
	}
	return dataset, nil
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

func encodedElementLength(syntax transfer.Syntax, el core.Element) (int, error) {
	countingWriter := countingWriter{}
	if err := parser.NewWriter(&countingWriter, syntax).WriteElement(el); err != nil {
		return 0, err
	}
	return countingWriter.n, nil
}

func sortElementsByTag(elements []core.Element) {
	sort.Slice(elements, func(i, j int) bool {
		if elements[i].Tag().Group != elements[j].Tag().Group {
			return elements[i].Tag().Group < elements[j].Tag().Group
		}
		return elements[i].Tag().Element < elements[j].Tag().Element
	})
}

type countingWriter struct {
	n int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += len(p)
	return len(p), nil
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
		return fmt.Errorf("%w: transfer syntax %q (%s) is registered but not supported by the scaffold %s", transfer.ErrUnsupportedTransferSyntax, syntax.UID, syntax.Name, operation)
	}
	return fmt.Errorf("%w: transfer syntax %q is registered but not supported by the scaffold %s", transfer.ErrUnsupportedTransferSyntax, syntax.UID, operation)
}
