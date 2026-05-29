package object

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

// Part10Header is the parsed prelude of a DICOM Part 10 stream. DataSetOffset
// is the absolute seeker position of the first dataset byte.
type Part10Header struct {
	Preamble       []byte
	Meta           *Object
	TransferSyntax transfer.Syntax
	DataSetOffset  int64
}

// ReadPart10Header reads the Part 10 preamble and File Meta Information from
// the source's current position. It leaves source positioned at the first
// dataset byte and never closes the caller-owned source.
func ReadPart10Header(source io.ReadSeeker, opts ReadFileOptions) (Part10Header, error) {
	if source == nil {
		return Part10Header{}, fmt.Errorf("dicom: read Part 10 header: nil source")
	}
	if opts.FileMetaDictionary == nil {
		opts.FileMetaDictionary = std.Dictionary
	}
	if opts.Dictionary == nil {
		opts.Dictionary = std.Dictionary
	}
	streamBase, err := source.Seek(0, io.SeekCurrent)
	if err != nil {
		return Part10Header{}, err
	}
	prefix := make([]byte, part10PreambleLength+len(part10Magic))
	prefixSource := io.Reader(source)
	var limitedPrefix *io.LimitedReader
	if opts.MaxTotalBytes > 0 {
		limitedPrefix = &io.LimitedReader{R: source, N: opts.MaxTotalBytes}
		prefixSource = limitedPrefix
	}
	if _, err := io.ReadFull(prefixSource, prefix); err != nil {
		if limitedPrefix != nil && limitedPrefix.N == 0 &&
			(errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF)) {
			return Part10Header{}, parser.ErrMaxTotalBytesExceeded
		}
		return Part10Header{}, err
	}
	if string(prefix[part10PreambleLength:]) != part10Magic {
		return Part10Header{}, ErrMissingPreamble
	}

	meta, relativeDataSetOffset, err := readPart10FileMetaElements(source, int64(len(prefix)), opts)
	if err != nil {
		return Part10Header{}, wrapFileMetaError(err)
	}
	syntax, err := resolveFileMetaTransferSyntax(meta)
	if err != nil {
		return Part10Header{}, wrapFileMetaError(err)
	}
	absoluteDataSetOffset := streamBase + relativeDataSetOffset
	if _, err := source.Seek(absoluteDataSetOffset, io.SeekStart); err != nil {
		return Part10Header{}, err
	}
	return Part10Header{
		Preamble:       append([]byte(nil), prefix[:part10PreambleLength]...),
		Meta:           meta,
		TransferSyntax: syntax,
		DataSetOffset:  absoluteDataSetOffset,
	}, nil
}

// readerOnly prevents parser.Reader's sized-source optimization from reading
// beyond the File Meta Information while retaining exact read errors.
type readerOnly struct {
	io.Reader
}

func readPart10FileMetaElements(source io.ReadSeeker, baseOffset int64, opts ReadFileOptions) (*Object, int64, error) {
	metaOpts := opts
	metaOpts.InlineValueBytesThreshold = 0
	metaOpts.SkipPixelData = false
	metaOpts.DeferPixelData = false
	metaOpts.DeferWaveformData = false
	metaOpts.FrameSink = nil
	if opts.FileMetaDictionary != nil {
		metaOpts.Dictionary = opts.FileMetaDictionary
	}
	readerOpts := metaOpts.parserReaderOptions(baseOffset)
	metaReader := parser.NewReader(readerOnly{source}, transfer.ExplicitVRLittleEndian, readerOpts)
	if opts.AllowMissingMetaElementGroupLength {
		return readPart10FileMetaElementsByGroup(source, metaReader, readerOpts)
	}

	first, err := metaReader.Next()
	if err != nil {
		return nil, 0, err
	}
	if first.Kind != parser.TokenElement || first.Element.Tag() != tagFileMetaInformationGroupLength {
		return nil, 0, ErrInvalidFileMetaGroupLength
	}
	raw, ok := first.Element.RawBytes()
	if !ok || len(raw) != 4 {
		return nil, 0, ErrInvalidFileMetaGroupLengthValue
	}
	metaLength := binary.LittleEndian.Uint32(raw)
	metaElements := []core.Element{first.Element}
	moreReader := parser.NewReader(
		io.LimitReader(readerOnly{source}, int64(metaLength)),
		transfer.ExplicitVRLittleEndian,
		metaOpts.parserReaderOptions(metaReader.Position()),
	)
	more, err := moreReader.ReadAll()
	if err != nil {
		return nil, 0, err
	}
	metaElements = append(metaElements, more...)
	return FromDataSet(core.DataSet{Elements: metaElements}, readerOpts.Dictionary), moreReader.Position(), nil
}

func readPart10FileMetaElementsByGroup(source io.ReadSeeker, metaReader *parser.Reader, readerOpts parser.ReaderOptions) (*Object, int64, error) {
	var metaElements []core.Element
	for {
		position, err := source.Seek(0, io.SeekCurrent)
		if err != nil {
			return nil, 0, err
		}
		var tagBytes [4]byte
		_, readErr := io.ReadFull(source, tagBytes[:])
		if _, err := source.Seek(position, io.SeekStart); err != nil {
			return nil, 0, err
		}
		if errors.Is(readErr, io.EOF) || errors.Is(readErr, io.ErrUnexpectedEOF) {
			break
		}
		if readErr != nil {
			return nil, 0, readErr
		}
		if binary.LittleEndian.Uint16(tagBytes[:2]) != tagFileMetaInformationGroupLength.Group {
			break
		}
		token, err := metaReader.Next()
		if err != nil {
			return nil, 0, err
		}
		if token.Kind != parser.TokenElement {
			return nil, 0, ErrInvalidFileMetaGroupLength
		}
		metaElements = append(metaElements, token.Element)
	}
	if len(metaElements) == 0 {
		return nil, 0, ErrInvalidFileMetaGroupLength
	}
	return FromDataSet(core.DataSet{Elements: metaElements}, readerOpts.Dictionary), metaReader.Position(), nil
}
