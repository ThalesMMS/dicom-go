package dicom

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	dicomenc "github.com/ThalesMMS/dicom-go/encoding"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type File = object.File
type Object = object.Object
type Dataset = core.DataSet
type Element = core.Element
type ReadFileOptions = object.ReadFileOptions
type Frame = object.Frame
type FrameMetadata = object.FrameMetadata
type FrameSink = object.FrameSink
type ParseOption func(*ReadFileOptions)

var ErrFrameChannelUnsupported = errors.New("dicom: unsupported compatibility Parse frame channel")

func AllowMismatchPixelDataLength() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.AllowMismatchPixelDataLength = true
	}
}

func AllowMissingMetaElementGroupLength() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.AllowMissingMetaElementGroupLength = true
	}
}

func AllowUnknownSpecificCharacterSet() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.TextOptions.AllowUnsupportedCharsetFallback = true
		opts.TextOptions.FallbackCharacterSet = dicomenc.DefaultCharacterSet
	}
}

func SkipMetadataReadOnNewParserInit() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.SkipMetadataReadOnNewParserInit = true
	}
}

func SkipPixelData() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.SkipPixelData = true
	}
}

func SkipProcessingPixelDataValue() ParseOption {
	return func(opts *ReadFileOptions) {
		opts.SkipProcessingPixelDataValue = true
	}
}

func StreamFramesTo(sink FrameSink) ParseOption {
	return func(opts *ReadFileOptions) {
		opts.FrameSink = sink
	}
}

func OpenFile(path string) (*File, error) {
	return object.OpenFile(path)
}

func OpenFileWithOptions(path string, opts ReadFileOptions) (*File, error) {
	return object.OpenFileWithOptions(path, opts)
}

func ReadFileWithOptions(r io.Reader, opts ReadFileOptions) (*File, error) {
	return object.ReadFileWithOptions(r, opts)
}

// Parse reads a Part 10 stream through the compatibility facade. frameChan may
// be nil, a chan Frame, or a FrameSink. Frame channels receive native Pixel Data
// frames synchronously and are closed when reading finishes.
func Parse(r io.Reader, bytesToRead int64, frameChan any, opts ...ParseOption) (Dataset, error) {
	readOpts, err := parseReadFileOptionsWithFrameChannel(frameChan, opts)
	if err != nil {
		return Dataset{}, err
	}
	reader := r
	if bytesToRead >= 0 {
		reader = io.LimitReader(r, bytesToRead)
	}
	file, err := object.ReadFileWithOptions(reader, readOpts)
	if err != nil {
		return Dataset{}, err
	}
	return datasetFromFile(file), nil
}

// ParseUntilEOF reads a Part 10 stream until EOF through the compatibility
// facade. frameChan follows the same rules as Parse.
func ParseUntilEOF(r io.Reader, frameChan any, opts ...ParseOption) (Dataset, error) {
	readOpts, err := parseReadFileOptionsWithFrameChannel(frameChan, opts)
	if err != nil {
		return Dataset{}, err
	}
	file, err := object.ReadFileWithOptions(r, readOpts)
	if err != nil {
		return Dataset{}, err
	}
	return datasetFromFile(file), nil
}

// ParseFile reads a Part 10 file from path through the compatibility facade.
// frameChan follows the same rules as Parse.
func ParseFile(path string, frameChan any, opts ...ParseOption) (Dataset, error) {
	readOpts, err := parseReadFileOptionsWithFrameChannel(frameChan, opts)
	if err != nil {
		return Dataset{}, err
	}
	file, err := object.OpenFileWithOptions(path, readOpts)
	if err != nil {
		return Dataset{}, err
	}
	if file != nil {
		defer file.Close()
	}
	return datasetFromFile(file), nil
}

func ReadDataSet(r io.Reader, syntax transfer.Syntax) (*Object, error) {
	return object.ReadDataSet(r, syntax)
}

func ReadDataSetWithOptions(r io.Reader, syntax transfer.Syntax, opts ReadFileOptions) (*Object, error) {
	return object.ReadDataSetWithOptions(r, syntax, opts)
}

func OpenDataSet(path string, syntax transfer.Syntax) (*Object, error) {
	return object.OpenDataSet(path, syntax)
}

func OpenDataSetWithOptions(path string, syntax transfer.Syntax, opts ReadFileOptions) (*Object, error) {
	return object.OpenDataSetWithOptions(path, syntax, opts)
}

func parseReadFileOptions(opts []ParseOption) ReadFileOptions {
	readOpts := ReadFileOptions{}
	applyParseOptions(&readOpts, opts)
	return readOpts
}

func parseReadFileOptionsWithFrameChannel(frameChan any, opts []ParseOption) (ReadFileOptions, error) {
	readOpts := ReadFileOptions{}
	if frameChan != nil {
		sink, err := compatibilityFrameSink(frameChan)
		if err != nil {
			return ReadFileOptions{}, err
		}
		readOpts.FrameSink = sink
	}
	applyParseOptions(&readOpts, opts)
	return readOpts, nil
}

func applyParseOptions(readOpts *ReadFileOptions, opts []ParseOption) {
	for _, opt := range opts {
		if opt != nil {
			opt(readOpts)
		}
	}
}

func compatibilityFrameSink(frameChan any) (FrameSink, error) {
	switch ch := frameChan.(type) {
	case chan Frame:
		return object.NewFrameChannelSink(ch), nil
	case FrameSink:
		return ch, nil
	default:
		return nil, fmt.Errorf("%w: expected chan dicom.Frame or dicom.FrameSink, got %T", ErrFrameChannelUnsupported, frameChan)
	}
}

func datasetFromFile(file *File) Dataset {
	if file == nil {
		return Dataset{}
	}
	var elements []Element
	if file.Meta != nil {
		elements = append(elements, file.Meta.Elements()...)
	}
	if file.Dataset != nil {
		elements = append(elements, file.Dataset.Elements()...)
	}
	return Dataset{Elements: elements}
}
