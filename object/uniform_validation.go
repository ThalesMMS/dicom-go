package object

import (
	"compress/flate"
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

// ValidateDataSet runs the uniform validation engine over this object's
// retained elements. Object uses last-wins tag semantics, so callers that need
// duplicate diagnostics should validate the core.DataSet before constructing
// an Object or use parser.NewReaderWithValidation.
func (o *Object) ValidateDataSet(ctx context.Context, opts validation.Options) (validation.Result, error) {
	if o == nil {
		return validation.Result{}, fmt.Errorf("dicom: nil object")
	}
	if opts.Dictionary == nil {
		opts.Dictionary = o.dict
	}
	if opts.ByteOrder == nil {
		opts.ByteOrder = o.ValueByteOrder()
	}
	return validation.ValidateDataSet(ctx, core.DataSet{Elements: o.Elements()}, opts)
}

// ValidateFile runs dataset rules and File Meta consistency checks without
// mutating the File or rebuilding its meta elements.
func (f *File) ValidateFile(ctx context.Context, opts validation.Options) (validation.Report, error) {
	if f == nil {
		return validation.Report{}, ErrNilFile
	}
	var meta, dataset core.DataSet
	if f.Meta != nil {
		meta.Elements = f.Meta.Elements()
	}
	if f.Dataset != nil {
		dataset.Elements = f.Dataset.Elements()
		if opts.Dictionary == nil {
			opts.Dictionary = f.Dataset.dict
		}
		if opts.ByteOrder == nil {
			opts.ByteOrder = f.Dataset.ValueByteOrder()
		}
	}
	return validation.ValidateFile(ctx, meta, dataset, f.TransferSyntax, opts)
}

// ReadDataSetWithValidation parses a raw data set with lifecycle hooks and
// returns the bounded report even when strict validation rejects the result.
func ReadDataSetWithValidation(ctx context.Context, source io.Reader, syntax transfer.Syntax, readOpts ReadFileOptions, validationOpts validation.Options) (*Object, validation.Report, error) {
	syntax, err := validateReadableSyntax(syntax)
	if err != nil {
		return nil, validation.Report{}, err
	}
	_, seekable := source.(io.ReadSeeker)
	if (readOpts.DeferPixelData || readOpts.DeferWaveformData) && !seekable {
		return nil, validation.Report{}, ErrDeferredValueRequiresSeekable
	}
	return readDataSetObjectWithValidation(ctx, source, syntax, readOpts, 0, seekable, validationOpts)
}

func OpenDataSetWithValidation(ctx context.Context, path string, syntax transfer.Syntax, readOpts ReadFileOptions, validationOpts validation.Options) (result *Object, report validation.Report, err error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, report, err
	}
	closeFile := true
	defer func() {
		if !closeFile {
			return
		}
		if closeErr := file.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}()
	result, report, err = ReadDataSetWithValidation(ctx, file, syntax, readOpts, validationOpts)
	if err == nil && result != nil && result.valueProvider != nil && result.deferredCount > 0 {
		result.source = file
		closeFile = false
	}
	return result, report, err
}

func readDataSetObjectWithValidation(ctx context.Context, source io.Reader, syntax transfer.Syntax, opts ReadFileOptions, baseOffset int64, streamValues bool, validationOpts validation.Options) (*Object, validation.Report, error) {
	readerSource := source
	if syntax.Deflated {
		if opts.DeferPixelData || opts.DeferWaveformData {
			return nil, validation.Report{}, errDeflatedDeferredValue()
		}
		inflated := flate.NewReader(source)
		defer func() { _ = inflated.Close() }()
		readerSource = inflated
		streamValues = false
	}
	readerOpts := opts.datasetParserReaderOptions(baseOffset, streamValues)
	if validationOpts.Dictionary == nil {
		validationOpts.Dictionary = readerOpts.Dictionary
	}
	reader, err := parser.NewReaderWithValidation(ctx, readerSource, syntax, readerOpts, validationOpts)
	if err != nil {
		return nil, validation.Report{}, err
	}
	dataset, readErr := reader.ReadDataSet()
	report := reader.ValidationReport()
	if readErr != nil && !errors.Is(readErr, validation.ErrValidationFailed) {
		return nil, report, readErr
	}
	obj := fromParsedDataSetWithTextOptions(dataset, readerOpts.Dictionary, opts.TextOptions)
	obj.SetValueByteOrder(syntax.ByteOrder)
	if streamValues && obj.deferredCount > 0 &&
		(readerOptionsNeedValueProvider(readerOpts) || readerHasRecordedLocation(reader, obj.Elements())) {
		obj.setValueProvider(&readerValueProvider{reader: reader})
	}
	return obj, report, readErr
}

func readerHasRecordedLocation(reader *parser.Reader, elements []core.Element) bool {
	for _, element := range elements {
		if element.Value == nil {
			if _, ok := reader.ValueLocation(element.Tag()); ok {
				return true
			}
		}
		if sequence, ok := element.Value.(core.SequenceValue); ok {
			for _, item := range sequence.Items {
				if readerHasRecordedLocation(reader, item.Elements) {
					return true
				}
			}
		}
	}
	return false
}

type WriteValidationResult struct {
	Report       validation.Report
	BytesWritten int64
	Complete     bool
}

type WriteValidationError struct {
	Result WriteValidationResult
	Err    error
}

func (e *WriteValidationError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("dicom validated data set write failed after %d committed bytes (complete=%t): %v", e.Result.BytesWritten, e.Result.Complete, e.Err)
}

func (e *WriteValidationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

type validationCommittedWriter struct {
	destination io.Writer
	written     int64
}

func (w *validationCommittedWriter) Write(data []byte) (int, error) {
	n, err := w.destination.Write(data)
	w.written += int64(n)
	return n, err
}

type lazyValidationDeflater struct {
	destination io.Writer
	writer      *flate.Writer
}

func (w *lazyValidationDeflater) Write(data []byte) (int, error) {
	if w.writer == nil {
		writer, err := flate.NewWriter(w.destination, flate.DefaultCompression)
		if err != nil {
			return 0, err
		}
		w.writer = writer
	}
	return w.writer.Write(data)
}

func (w *lazyValidationDeflater) Close(commitEmptyStream bool) error {
	if w == nil {
		return nil
	}
	if w.writer == nil {
		if !commitEmptyStream {
			return nil
		}
		writer, err := flate.NewWriter(w.destination, flate.DefaultCompression)
		if err != nil {
			return err
		}
		w.writer = writer
	}
	return w.writer.Close()
}

// WriteDataSetWithValidation validates and serializes an Object through the
// opt-in writer lifecycle. Existing WriteDataSet remains unchanged.
func WriteDataSetWithValidation(ctx context.Context, destination io.Writer, obj *Object, syntax transfer.Syntax, opts validation.Options) (WriteValidationResult, error) {
	if obj == nil {
		return WriteValidationResult{}, fmt.Errorf("dicom: nil object passed to WriteDataSetWithValidation")
	}
	if hasDeferredSequenceValue(obj) {
		return WriteValidationResult{}, ErrDeferredSequenceValueWrite
	}
	characterSet, err := obj.characterSetForVR(core.VRLO)
	if err != nil {
		return WriteValidationResult{}, fmt.Errorf("dicom: resolve dataset Specific Character Set for writing: %w", err)
	}
	syntax, err = validateWritableSyntax(syntax)
	if err != nil {
		return WriteValidationResult{}, err
	}
	if opts.Dictionary == nil {
		opts.Dictionary = obj.dict
	}
	if opts.ByteOrder == nil {
		opts.ByteOrder = obj.ValueByteOrder()
	}
	committed := &validationCommittedWriter{destination: destination}
	writerDestination := io.Writer(committed)
	var deflater *lazyValidationDeflater
	if syntax.Deflated {
		deflater = &lazyValidationDeflater{destination: writerDestination}
		writerDestination = deflater
	}
	writer, err := parser.NewWriterWithValidation(ctx, writerDestination, syntax, parser.WriterOptions{CharacterSet: characterSet}, opts)
	if err != nil {
		return WriteValidationResult{}, err
	}
	dataset := core.DataSet{Elements: obj.SortedElements()}
	writeErr := writer.WriteDataSetWithDeferred(dataset, func(element core.Element, target io.Writer) (int64, error) {
		if obj.valueProvider == nil {
			return 0, fmt.Errorf("dicom: deferred element %s requires value provider", element.Tag())
		}
		return obj.valueProvider.CopyValueTo(element.Tag(), target)
	})
	var closeErr error
	var parserErr *parser.ValidationWriteError
	logicallyComplete := writeErr == nil || (errors.As(writeErr, &parserErr) && parserErr.Complete)
	if deflater != nil {
		closeErr = deflater.Close(logicallyComplete)
		writeErr = errors.Join(writeErr, closeErr)
	}
	result := WriteValidationResult{Report: writer.ValidationReport(), BytesWritten: committed.written}
	if writeErr == nil {
		result.Complete = true
		return result, nil
	}
	if logicallyComplete && closeErr == nil {
		result.Complete = true
	}
	return result, &WriteValidationError{Result: result, Err: writeErr}
}
