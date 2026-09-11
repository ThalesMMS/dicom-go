package object

import (
	"errors"
	"fmt"
	"io"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/parser"
)

// ErrInvalidCloneOptions reports negative or otherwise invalid clone limits.
var ErrInvalidCloneOptions = errors.New("dicom: invalid clone options")

const (
	defaultCloneMaxBytes     int64 = 2 << 30
	defaultCloneMaxElements        = 100_000
	defaultCloneMaxDepth           = 64
	defaultCloneMaxItems           = 100_000
	defaultCloneMaxFragments       = 100_000
)

// CloneFileOptions bounds the serialization stream consumed while cloning.
// Zero-valued fields select the corresponding conservative default returned by
// DefaultCloneFileOptions; negative values are invalid.
type CloneFileOptions struct {
	MaxElementBytes   int64
	MaxPixelDataBytes int64
	MaxTotalBytes     int64
	MaxSequenceDepth  int
	MaxElements       int
	MaxItems          int
	MaxFragments      int
}

// DefaultCloneFileOptions returns the limits used by CloneFile.
func DefaultCloneFileOptions() CloneFileOptions {
	return CloneFileOptions{
		MaxElementBytes:   defaultCloneMaxBytes,
		MaxPixelDataBytes: defaultCloneMaxBytes,
		MaxTotalBytes:     defaultCloneMaxBytes,
		MaxSequenceDepth:  defaultCloneMaxDepth,
		MaxElements:       defaultCloneMaxElements,
		MaxItems:          defaultCloneMaxItems,
		MaxFragments:      defaultCloneMaxFragments,
	}
}

// CloneFile returns an independent, resource-bounded copy of src. Values are
// serialized through a bounded pipe and reparsed directly into the clone, so
// the encoded file is never materialized as a second whole-file buffer.
func CloneFile(src *File) (*File, error) {
	return CloneFileWithOptions(src, CloneFileOptions{})
}

// CloneFileWithOptions returns an independent copy of src using caller-selected
// resource limits. Limits are checked against the source structure before the
// streaming round-trip and are also enforced by the parser before value
// allocations. The clone never shares mutable value buffers with src.
func CloneFileWithOptions(src *File, opts CloneFileOptions) (*File, error) {
	if src == nil {
		return nil, ErrNilFile
	}
	normalized, err := normalizeCloneFileOptions(opts)
	if err != nil {
		return nil, err
	}
	if err := validateCloneFileStructure(src, normalized); err != nil {
		return nil, err
	}

	reader, writer := io.Pipe()
	writeDone := make(chan error, 1)
	go func() {
		writeErr := WriteFile(writer, src)
		_ = writer.CloseWithError(writeErr)
		writeDone <- writeErr
	}()

	clone, readErr := ReadFileWithOptions(reader, ReadFileOptions{
		MaxElementBytes:   normalized.MaxElementBytes,
		MaxPixelDataBytes: normalized.MaxPixelDataBytes,
		MaxTotalBytes:     normalized.MaxTotalBytes,
		MaxSequenceDepth:  normalized.MaxSequenceDepth,
		MaxElements:       normalized.MaxElements,
		MaxFragments:      normalized.MaxFragments,
	})
	if readErr != nil {
		_ = reader.CloseWithError(readErr)
	} else {
		_ = reader.Close()
	}
	writeErr := <-writeDone
	if writeErr != nil {
		return nil, writeErr
	}
	if readErr != nil {
		return nil, readErr
	}
	return clone, nil
}

func normalizeCloneFileOptions(opts CloneFileOptions) (CloneFileOptions, error) {
	if opts.MaxElementBytes < 0 || opts.MaxPixelDataBytes < 0 || opts.MaxTotalBytes < 0 ||
		opts.MaxSequenceDepth < 0 || opts.MaxElements < 0 || opts.MaxItems < 0 || opts.MaxFragments < 0 {
		return CloneFileOptions{}, fmt.Errorf("%w: limits must not be negative", ErrInvalidCloneOptions)
	}
	defaults := DefaultCloneFileOptions()
	if opts.MaxElementBytes == 0 {
		opts.MaxElementBytes = defaults.MaxElementBytes
	}
	if opts.MaxPixelDataBytes == 0 {
		opts.MaxPixelDataBytes = defaults.MaxPixelDataBytes
	}
	if opts.MaxTotalBytes == 0 {
		opts.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if opts.MaxSequenceDepth == 0 {
		opts.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if opts.MaxElements == 0 {
		opts.MaxElements = defaults.MaxElements
	}
	if opts.MaxItems == 0 {
		opts.MaxItems = defaults.MaxItems
	}
	if opts.MaxFragments == 0 {
		opts.MaxFragments = defaults.MaxFragments
	}
	return opts, nil
}

type cloneStructureBudget struct {
	opts       CloneFileOptions
	elements   int
	items      int
	fragments  int
	valueBytes int64
}

func validateCloneFileStructure(src *File, opts CloneFileOptions) error {
	budget := cloneStructureBudget{opts: opts}
	if len(src.Preamble) > 0 {
		if err := budget.addValueBytes(int64(len(src.Preamble)), core.Tag{}); err != nil {
			return err
		}
	}
	if err := budget.visitObject(src.Meta, 0); err != nil {
		return err
	}
	return budget.visitObject(src.Dataset, 0)
}

func (budget *cloneStructureBudget) visitObject(obj *Object, depth int) error {
	if obj == nil {
		return nil
	}
	if depth > budget.opts.MaxSequenceDepth {
		return fmt.Errorf("%w: got %d, limit %d", parser.ErrMaxDepthExceeded, depth, budget.opts.MaxSequenceDepth)
	}
	if obj.Len() > budget.opts.MaxElements-budget.elements {
		return fmt.Errorf("%w: got more than %d elements", parser.ErrMaxElementsExceeded, budget.opts.MaxElements)
	}
	elements := obj.Elements()
	budget.elements += len(elements)
	for _, element := range elements {
		if err := budget.visitElement(element, depth); err != nil {
			return err
		}
	}
	return nil
}

func (budget *cloneStructureBudget) visitElement(element core.Element, depth int) error {
	switch value := element.Value.(type) {
	case core.SequenceValue:
		if depth >= budget.opts.MaxSequenceDepth && len(value.Items) > 0 {
			return fmt.Errorf("%w: got more than %d levels", parser.ErrMaxDepthExceeded, budget.opts.MaxSequenceDepth)
		}
		if len(value.Items) > budget.opts.MaxItems-budget.items {
			return fmt.Errorf("%w: got more than %d sequence items", parser.ErrMaxElementsExceeded, budget.opts.MaxItems)
		}
		budget.items += len(value.Items)
		for _, item := range value.Items {
			if err := budget.visitDataSet(item, depth+1); err != nil {
				return err
			}
		}
		return nil
	case core.FragmentSequence:
		if len(value.Fragments) > budget.opts.MaxFragments-budget.fragments {
			return fmt.Errorf("%w: got more than %d fragments", parser.ErrMaxFragmentsExceeded, budget.opts.MaxFragments)
		}
		budget.fragments += len(value.Fragments)
		elementBytes := int64(len(value.OffsetTable))
		for _, fragment := range value.Fragments {
			fragmentBytes := int64(len(fragment))
			if fragmentBytes > budget.opts.MaxPixelDataBytes-elementBytes {
				return fmt.Errorf("%w: element %s exceeds %d bytes", parser.ErrMaxPixelDataBytesExceeded, element.Tag(), budget.opts.MaxPixelDataBytes)
			}
			elementBytes += fragmentBytes
		}
		return budget.addElementBytes(element, elementBytes)
	case nil:
		if !element.Header.HasLength() || element.Length() == core.UndefinedLength {
			return nil
		}
		return budget.addElementBytes(element, int64(element.Length()))
	default:
		length, ok := element.CalculatedLength()
		if !ok || length == core.UndefinedLength {
			return nil
		}
		return budget.addElementBytes(element, int64(length))
	}
}

func (budget *cloneStructureBudget) visitDataSet(dataset core.DataSet, depth int) error {
	if depth > budget.opts.MaxSequenceDepth {
		return fmt.Errorf("%w: got %d, limit %d", parser.ErrMaxDepthExceeded, depth, budget.opts.MaxSequenceDepth)
	}
	if len(dataset.Elements) > budget.opts.MaxElements-budget.elements {
		return fmt.Errorf("%w: got more than %d elements", parser.ErrMaxElementsExceeded, budget.opts.MaxElements)
	}
	budget.elements += len(dataset.Elements)
	for _, element := range dataset.Elements {
		if err := budget.visitElement(element, depth); err != nil {
			return err
		}
	}
	return nil
}

func (budget *cloneStructureBudget) addElementBytes(element core.Element, size int64) error {
	limit := budget.opts.MaxElementBytes
	limitErr := parser.ErrMaxElementBytesExceeded
	if element.Tag() == core.TagPixelData {
		limit = budget.opts.MaxPixelDataBytes
		limitErr = parser.ErrMaxPixelDataBytesExceeded
	}
	if size > limit {
		return fmt.Errorf("%w: element %s has %d bytes, limit %d", limitErr, element.Tag(), size, limit)
	}
	return budget.addValueBytes(size, element.Tag())
}

func (budget *cloneStructureBudget) addValueBytes(size int64, tag core.Tag) error {
	if size < 0 || size > budget.opts.MaxTotalBytes-budget.valueBytes {
		return fmt.Errorf("%w: values through %s exceed %d bytes", parser.ErrMaxTotalBytesExceeded, tag, budget.opts.MaxTotalBytes)
	}
	budget.valueBytes += size
	return nil
}
