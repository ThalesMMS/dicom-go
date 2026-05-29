package parser

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

type committedWriter struct {
	destination io.Writer
	base        int64
	written     int64
}

type pendingValidationPostWrite struct {
	path          validation.Path
	header        core.ElementHeader
	offset        int64
	bytesWritten  int64
	writeComplete bool
}

func (w *committedWriter) Write(data []byte) (int, error) {
	n, err := w.destination.Write(data)
	w.written += int64(n)
	return n, err
}

// ValidationWriteError reports whether an element was fully committed before
// a write or post-write hook failed. Complete lets callers avoid retrying bytes
// that have already been emitted successfully.
type ValidationWriteError struct {
	BytesWritten int64
	Complete     bool
	Err          error
}

func (e *ValidationWriteError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("dicom validation write failed after %d committed bytes (complete=%t): %v", e.BytesWritten, e.Complete, e.Err)
}

func (e *ValidationWriteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// ValidationWriter adds an explicit validation operation around parser.Writer.
// Ordinary Writer constructors remain unchanged and execute no hooks.
type ValidationWriter struct {
	writer           *Writer
	committed        *committedWriter
	operation        *validation.Operation
	opts             validation.Options
	ctx              context.Context
	dataSetValidated bool
}

// DeferredValueWriter streams one nil-valued element payload to destination.
type DeferredValueWriter func(element core.Element, destination io.Writer) (int64, error)

func NewWriterWithValidation(ctx context.Context, destination io.Writer, syntax transfer.Syntax, writerOpts WriterOptions, validationOpts validation.Options) (*ValidationWriter, error) {
	if destination == nil {
		return nil, fmt.Errorf("dicom: nil validation writer destination")
	}
	if validationOpts.ByteOrder == nil {
		validationOpts.ByteOrder = syntax.ByteOrder
	}
	validationOpts.TransferSyntax = syntax
	if ctx == nil {
		ctx = context.Background()
	}
	operation, err := validation.NewOperation(ctx, validationOpts)
	if err != nil {
		return nil, err
	}
	committed := &committedWriter{destination: destination}
	result := &ValidationWriter{
		writer: NewWriterWithOptions(committed, syntax, writerOpts), committed: committed,
		operation: operation, opts: validationOpts, ctx: ctx,
	}
	result.writer.validationWrite = result.writeElementFromWriter
	result.writer.validationCount = committed
	result.writer.validationPostWrite = result.flushValidationPostWrites
	return result, nil
}

func (w *ValidationWriter) WriteElement(element core.Element) error {
	if w == nil || w.writer == nil {
		return fmt.Errorf("dicom: nil validation writer")
	}
	return w.writer.writeValidationElement(element)
}

func (w *ValidationWriter) WriteDeferredElement(element core.Element, copyValueTo func(io.Writer) (int64, error)) error {
	path := validation.Path{{Tag: element.Tag(), ItemIndex: validation.NoItem}}
	return w.writeElement(w.writer, element, path, true, func(writer *Writer, current core.Element, currentPath validation.Path) error {
		if current.Value != nil {
			return writer.writeElementValidated(current, currentPath)
		}
		return writer.WriteDeferredElement(current, copyValueTo)
	})
}

func (w *ValidationWriter) WriteDataSet(dataset core.DataSet) error {
	return w.WriteDataSetWithDeferred(dataset, nil)
}

// WriteDataSetWithDeferred validates the complete data set once, then writes
// every top-level element. provider is used only for nil-valued elements.
func (w *ValidationWriter) WriteDataSetWithDeferred(dataset core.DataSet, provider DeferredValueWriter) error {
	if w == nil {
		return fmt.Errorf("dicom: nil validation writer")
	}
	result, err := w.operation.ValidateParsedDataSet(dataset)
	if err != nil {
		return err
	}
	start := w.committed.written
	previousDataSetValidated := w.dataSetValidated
	w.dataSetValidated = true
	defer func() { w.dataSetValidated = previousDataSetValidated }()
	for index, element := range result.DataSet.Elements {
		current := element
		path := validation.Path{{Tag: element.Tag(), ItemIndex: validation.NoItem}}
		write := func(writer *Writer, transformed core.Element, transformedPath validation.Path) error {
			if transformed.Value != nil {
				return writer.writeElementValidated(transformed, transformedPath)
			}
			if provider == nil {
				return writer.WriteDeferredElement(transformed, nil)
			}
			return writer.WriteDeferredElement(transformed, func(destination io.Writer) (int64, error) {
				return provider(current, destination)
			})
		}
		if err := w.writeElement(w.writer, element, path, false, write); err != nil {
			complete := false
			var operationErr *ValidationWriteError
			if errors.As(err, &operationErr) {
				complete = index == len(result.DataSet.Elements)-1 && operationErr.Complete
			}
			return &ValidationWriteError{BytesWritten: w.committed.written - start, Complete: complete, Err: err}
		}
	}
	return nil
}

func (w *ValidationWriter) ValidationReport() validation.Report {
	if w == nil || w.operation == nil {
		return validation.Report{}
	}
	return w.operation.Report()
}

func (w *ValidationWriter) BytesWritten() int64 {
	if w == nil || w.committed == nil {
		return 0
	}
	return w.committed.written
}

func (w *ValidationWriter) writeElementFromWriter(writer *Writer, element core.Element, path validation.Path) error {
	return w.writeElement(writer, element, path, !w.dataSetValidated, func(writer *Writer, current core.Element, currentPath validation.Path) error {
		return writer.writeElementValidated(current, currentPath)
	})
}

func (w *ValidationWriter) writeElement(writer *Writer, element core.Element, path validation.Path, validateRules bool, write func(*Writer, core.Element, validation.Path) error) error {
	if w == nil || w.writer == nil || w.operation == nil {
		return fmt.Errorf("dicom: nil validation writer")
	}
	original := element
	start := validationWriterPosition(writer)
	preResult, err := w.operation.Handle(validation.HookEvent{
		Point: validation.HookPreSerialization, Path: path, Header: &element.Header, Element: &element,
		Offset: start, OffsetSet: true,
	})
	if err != nil {
		return err
	}
	if preResult.Element != nil {
		element = *preResult.Element
		if original.Value == nil && element.Value == nil && element.Header != original.Header {
			return fmt.Errorf("%w: deferred element replacement without a value must preserve its header", validation.ErrHookAction)
		}
		path[len(path)-1].Tag = element.Tag()
	}
	if preResult.Filter {
		return w.finishPostWrite(writer, path, element, start, nil)
	}

	if validateRules {
		ruleOpts := w.opts
		ruleOpts.Hooks = nil
		ruleOpts.RequiredUIDs = nil
		report, validationErr := validation.ValidateElement(w.ctx, element, ruleOpts)
		w.operation.AddReport(report)
		if validationErr != nil {
			if errors.Is(validationErr, validation.ErrValidationFailed) {
				return &validation.ValidationError{Report: w.operation.Report()}
			}
			return validationErr
		}
	}

	writeErr := write(writer, element, path)
	return w.finishPostWrite(writer, path, element, start, writeErr)
}

func (w *ValidationWriter) finishPostWrite(writer *Writer, path validation.Path, element core.Element, start int64, writeErr error) error {
	delta := validationWriterPosition(writer) - start
	postWrite := pendingValidationPostWrite{
		path: path.Clone(), header: element.Header, offset: start,
		bytesWritten: delta, writeComplete: writeErr == nil,
	}
	if writer.validationPostWrites != nil {
		*writer.validationPostWrites = append(*writer.validationPostWrites, postWrite)
		if writeErr == nil {
			return nil
		}
		return &ValidationWriteError{BytesWritten: delta, Complete: false, Err: writeErr}
	}
	hookErr := w.flushValidationPostWrites([]pendingValidationPostWrite{postWrite})
	if writeErr == nil && hookErr == nil {
		return nil
	}
	return &ValidationWriteError{
		BytesWritten: delta,
		Complete:     writeErr == nil,
		Err:          errors.Join(writeErr, hookErr),
	}
}

func (w *ValidationWriter) flushValidationPostWrites(postWrites []pendingValidationPostWrite) error {
	var hookErrs []error
	for _, postWrite := range postWrites {
		header := postWrite.header
		_, hookErr := w.operation.Handle(validation.HookEvent{
			Point: validation.HookPostWrite, Path: postWrite.path, Header: &header,
			Offset: postWrite.offset, OffsetSet: true, BytesWritten: postWrite.bytesWritten,
			WriteComplete: postWrite.writeComplete,
		})
		if hookErr != nil {
			hookErrs = append(hookErrs, hookErr)
		}
	}
	return errors.Join(hookErrs...)
}

func validationWriterPosition(writer *Writer) int64 {
	if writer == nil || writer.validationCount == nil {
		return 0
	}
	return writer.validationCount.base + writer.validationCount.written
}
