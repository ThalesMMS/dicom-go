package pixeldata

import (
	"context"
	"fmt"
	"math"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func validateUniqueCoreDataSet(ctx context.Context, dataset core.DataSet, depth int, limits TranscodeLimits, structuralNodes *int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if depth > limits.MaxDepth {
		return transcodeLimitError("depth")
	}
	if len(dataset.Elements) > limits.MaxElements-*structuralNodes {
		return transcodeLimitError("elements")
	}
	*structuralNodes += len(dataset.Elements)
	tags := make(map[core.Tag]struct{}, len(dataset.Elements))
	for _, element := range dataset.Elements {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, exists := tags[element.Tag()]; exists {
			return fmt.Errorf("%w: duplicate data element", ErrTranscodeUnsupported)
		}
		tags[element.Tag()] = struct{}{}
		if values, ok := element.Value.(core.StringValue); ok {
			if len(values) > limits.MaxElements-*structuralNodes {
				return transcodeLimitError("elements")
			}
			*structuralNodes += len(values)
		}
		sequence, ok := element.Value.(core.SequenceValue)
		if !ok {
			continue
		}
		if len(sequence.Items) > limits.MaxElements-*structuralNodes {
			return transcodeLimitError("elements")
		}
		*structuralNodes += len(sequence.Items)
		for _, item := range sequence.Items {
			if err := validateUniqueCoreDataSet(ctx, item, depth+1, limits, structuralNodes); err != nil {
				return err
			}
		}
	}
	return nil
}

type transcodeObjectUsage struct {
	elements  int
	fragments int
	bytes     int64
}

func measureTranscodeObject(ctx context.Context, source *object.Object, limits TranscodeLimits) (transcodeObjectUsage, error) {
	if source == nil {
		return transcodeObjectUsage{}, nil
	}
	usage := transcodeObjectUsage{}
	var walk func([]core.Element, int) error
	walk = func(elements []core.Element, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > limits.MaxDepth || len(elements) > limits.MaxElements-usage.elements {
			return transcodeLimitError("elements")
		}
		usage.elements += len(elements)
		addBytes := func(value int64) error {
			if value < 0 || value > limits.MaxOutputBytes-usage.bytes {
				return transcodeLimitError("output_bytes")
			}
			usage.bytes += value
			return nil
		}
		for _, element := range elements {
			if err := ctx.Err(); err != nil {
				return err
			}
			if element.Value == nil {
				return fmt.Errorf("%w: deferred value is unavailable", ErrTranscodeUnsupported)
			}
			switch value := element.Value.(type) {
			case core.StringValue:
				if len(value) > limits.MaxElements-usage.elements {
					return transcodeLimitError("elements")
				}
				usage.elements += len(value)
				length, ok := value.EncodedLength()
				if !ok || uint64(length) > math.MaxInt64 {
					return fmt.Errorf("%w: unsupported element value", ErrTranscodeUnsupported)
				}
				if err := addBytes(int64(length)); err != nil {
					return err
				}
			case core.SequenceValue:
				if len(value.Items) > limits.MaxElements-usage.elements {
					return transcodeLimitError("elements")
				}
				usage.elements += len(value.Items)
				for _, item := range value.Items {
					if err := walk(item.Elements, depth+1); err != nil {
						return err
					}
				}
			case core.FragmentSequence:
				if len(value.Fragments) > limits.MaxFragments-usage.fragments {
					return transcodeLimitError("fragments")
				}
				usage.fragments += len(value.Fragments)
				if err := addBytes(int64(len(value.OffsetTable))); err != nil {
					return err
				}
				for _, fragment := range value.Fragments {
					if err := addBytes(int64(len(fragment))); err != nil {
						return err
					}
				}
			case core.BulkDataValue:
				if err := addBytes(int64(len(value.URI))); err != nil {
					return err
				}
			default:
				length, ok := element.Value.EncodedLength()
				if !ok || uint64(length) > math.MaxInt64 {
					return fmt.Errorf("%w: unsupported element value", ErrTranscodeUnsupported)
				}
				if err := addBytes(int64(length)); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(source.Elements(), 1); err != nil {
		return transcodeObjectUsage{}, err
	}
	return usage, nil
}

func validateTranscodePixelData(dataset *object.Object, source transfer.Syntax, limits TranscodeLimits) (Metadata, PixelData, error) {
	integerPixel, hasInteger := dataset.Get(core.TagPixelData)
	_, hasFloat := dataset.Get(tagFloatPixelData)
	_, hasDouble := dataset.Get(tagDoubleFloatPixelData)
	present := 0
	for _, ok := range []bool{hasInteger, hasFloat, hasDouble} {
		if ok {
			present++
		}
	}
	if present != 1 || !hasInteger {
		return Metadata{}, PixelData{}, fmt.Errorf("%w: integer Pixel Data is required and Float/Double Float Pixel Data are unsupported", ErrTranscodeUnsupported)
	}
	pixel, err := pixelDataViewFromElement(integerPixel)
	if err != nil {
		return Metadata{}, PixelData{}, err
	}
	if source.Encapsulated != pixel.Encapsulated {
		return Metadata{}, PixelData{}, fmt.Errorf("%w: transfer syntax and Pixel Data representation differ", ErrIncompatiblePixelData)
	}
	metadata, err := ExtractMetadata(dataset)
	if err != nil {
		return Metadata{}, PixelData{}, err
	}
	if metadata.NumberOfFrames <= 0 || metadata.NumberOfFrames > limits.MaxFrames {
		return Metadata{}, PixelData{}, transcodeLimitError("frames")
	}
	pixels := uint64(metadata.Rows) * uint64(metadata.Columns)
	if pixels > 0 && uint64(metadata.NumberOfFrames) > math.MaxUint64/pixels {
		return Metadata{}, PixelData{}, transcodeLimitError("pixels")
	}
	pixels *= uint64(metadata.NumberOfFrames)
	if pixels > limits.MaxPixels {
		return Metadata{}, PixelData{}, transcodeLimitError("pixels")
	}
	if len(pixel.Sequence.Fragments) > limits.MaxFragments || pixelPayloadBytes(pixel) > limits.MaxInputBytes {
		return Metadata{}, PixelData{}, transcodeLimitError("input_bytes")
	}
	return metadata, pixel, nil
}

func validateNativeTranscodeInput(dataset *object.Object, source transfer.Syntax, limits TranscodeLimits) (Metadata, int64, bool, error) {
	integerElement, hasInteger := dataset.Get(core.TagPixelData)
	floatElement, hasFloat := dataset.Get(tagFloatPixelData)
	doubleElement, hasDouble := dataset.Get(tagDoubleFloatPixelData)
	present := 0
	for _, ok := range []bool{hasInteger, hasFloat, hasDouble} {
		if ok {
			present++
		}
	}
	if present == 0 {
		return Metadata{}, 0, false, nil
	}
	if present != 1 {
		return Metadata{}, 0, false, fmt.Errorf("%w: multiple Pixel Data representations", ErrTranscodeUnsupported)
	}
	if hasInteger {
		pixel, err := pixelDataViewFromElement(integerElement)
		if err != nil {
			return Metadata{}, 0, false, err
		}
		if pixel.Encapsulated != source.Encapsulated {
			return Metadata{}, 0, false, fmt.Errorf("%w: transfer syntax and Pixel Data representation differ", ErrIncompatiblePixelData)
		}
		pixelBytes := pixelPayloadBytes(pixel)
		if pixelBytes > limits.MaxInputBytes {
			return Metadata{}, 0, false, transcodeLimitError("input_bytes")
		}
		metadata, metadataErr := ExtractMetadata(dataset)
		if metadataErr != nil {
			// Native-to-native conversion does not interpret or allocate frames.
			// Preserve incomplete legacy objects safely under the byte/structure
			// budgets rather than widening product validation as a side effect.
			return Metadata{}, pixelBytes, false, nil
		}
		if err := validateTranscodeMetadataBounds(metadata, limits); err != nil {
			return Metadata{}, 0, false, err
		}
		return metadata, pixelBytes, true, nil
	}

	element := floatElement
	if hasDouble {
		element = doubleElement
	}
	raw, ok := element.RawBytes()
	if !ok || source.Encapsulated {
		return Metadata{}, 0, false, fmt.Errorf("%w: Float Pixel Data must use a native value", ErrIncompatiblePixelData)
	}
	if int64(len(raw)) > limits.MaxInputBytes {
		return Metadata{}, 0, false, transcodeLimitError("input_bytes")
	}
	metadata, err := ExtractMetadata(dataset)
	if err != nil {
		return Metadata{}, int64(len(raw)), false, nil
	}
	if err := validateTranscodeMetadataBounds(metadata, limits); err != nil {
		return Metadata{}, 0, false, err
	}
	return metadata, int64(len(raw)), true, nil
}

func validateTranscodeMetadataBounds(metadata Metadata, limits TranscodeLimits) error {
	if metadata.NumberOfFrames <= 0 || metadata.NumberOfFrames > limits.MaxFrames {
		return transcodeLimitError("frames")
	}
	pixels := uint64(metadata.Rows) * uint64(metadata.Columns)
	if pixels > 0 && uint64(metadata.NumberOfFrames) > math.MaxUint64/pixels {
		return transcodeLimitError("pixels")
	}
	if pixels*uint64(metadata.NumberOfFrames) > limits.MaxPixels {
		return transcodeLimitError("pixels")
	}
	return nil
}

func validateEndianConversion(dataset *object.Object, limits TranscodeLimits) error {
	if dataset == nil {
		return nil
	}
	type frame struct {
		elements []core.Element
		depth    int
	}
	stack := []frame{{elements: dataset.Elements(), depth: 1}}
	seen := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth > limits.MaxDepth || len(current.elements) > limits.MaxElements-seen {
			return transcodeLimitError("elements")
		}
		seen += len(current.elements)
		for _, element := range current.elements {
			if element.VR() == core.VRUN {
				if raw, ok := element.RawBytes(); ok && len(raw) > 0 {
					return fmt.Errorf("%w: endian conversion of VR UN", ErrTranscodeUnsupported)
				}
			}
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok {
				continue
			}
			if len(sequence.Items) > limits.MaxElements-seen-len(stack) {
				return transcodeLimitError("elements")
			}
			for i := len(sequence.Items) - 1; i >= 0; i-- {
				stack = append(stack, frame{elements: sequence.Items[i].Elements, depth: current.depth + 1})
			}
		}
	}
	return nil
}

func hasNestedPixelData(dataset *object.Object, limits TranscodeLimits) (bool, error) {
	if dataset == nil {
		return false, nil
	}
	type frame struct {
		elements []core.Element
		depth    int
		nested   bool
	}
	stack := []frame{{elements: dataset.Elements(), depth: 1}}
	seen := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if current.depth > limits.MaxDepth || len(current.elements) > limits.MaxElements-seen {
			return false, transcodeLimitError("elements")
		}
		seen += len(current.elements)
		for _, element := range current.elements {
			if current.nested && (element.Tag() == core.TagPixelData || element.Tag() == tagFloatPixelData || element.Tag() == tagDoubleFloatPixelData) {
				return true, nil
			}
			sequence, ok := element.Value.(core.SequenceValue)
			if !ok {
				continue
			}
			if len(sequence.Items) > limits.MaxElements-seen-len(stack) {
				return false, transcodeLimitError("elements")
			}
			for i := len(sequence.Items) - 1; i >= 0; i-- {
				stack = append(stack, frame{elements: sequence.Items[i].Elements, depth: current.depth + 1, nested: true})
			}
		}
	}
	return false, nil
}

func hasAnyPixelData(dataset *object.Object) bool {
	if dataset == nil {
		return false
	}
	for _, tag := range []core.Tag{core.TagPixelData, tagFloatPixelData, tagDoubleFloatPixelData} {
		if _, ok := dataset.Get(tag); ok {
			return true
		}
	}
	return false
}

func strictFileTransferSyntax(file *object.File) (transfer.Syntax, error) {
	// Constructed files may legitimately carry the source syntax in either the
	// File field or File Meta. sourceTransferSyntax accepts either form while
	// still rejecting a disagreement when both are present.
	return sourceTransferSyntax(file)
}

func canonicalTargetTransferSyntax(syntax transfer.Syntax) (transfer.Syntax, error) {
	uid := transfer.NormalizeUID(syntax.UID)
	if uid == "" {
		return transfer.Syntax{}, fmt.Errorf("%w: target transfer syntax is empty", ErrTranscodeUnsupported)
	}
	got, ok := transfer.DefaultRegistry.Get(uid)
	if !ok {
		return transfer.Syntax{}, fmt.Errorf("%w: target transfer syntax is unknown", ErrTranscodeUnsupported)
	}
	if !got.Supported && !got.RequiresCodec() {
		return transfer.Syntax{}, fmt.Errorf("%w: target transfer syntax is unsupported", ErrTranscodeUnsupported)
	}
	return got, nil
}

func normalizeTranscodeLimits(limits TranscodeLimits) (TranscodeLimits, error) {
	defaults := DefaultTranscodeLimits()
	if limits.MaxFrames == 0 {
		limits.MaxFrames = defaults.MaxFrames
	}
	if limits.MaxPixels == 0 {
		limits.MaxPixels = defaults.MaxPixels
	}
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = defaults.MaxInputBytes
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = defaults.MaxOutputBytes
	}
	if limits.MaxFragments == 0 {
		limits.MaxFragments = defaults.MaxFragments
	}
	if limits.MaxElements == 0 {
		limits.MaxElements = defaults.MaxElements
	}
	if limits.MaxDepth == 0 {
		limits.MaxDepth = defaults.MaxDepth
	}
	if limits.MaxExpansionRatio == 0 {
		limits.MaxExpansionRatio = defaults.MaxExpansionRatio
	}
	if limits.MaxDuration == 0 {
		limits.MaxDuration = defaults.MaxDuration
	}
	if limits.MaxFrames < 0 || limits.MaxPixels == 0 || limits.MaxPixels > math.MaxInt64 || limits.MaxInputBytes < 0 || limits.MaxOutputBytes < 0 || limits.MaxFragments < 0 || limits.MaxElements < 0 || limits.MaxDepth < 0 || limits.MaxExpansionRatio < 0 || limits.MaxDuration < 0 {
		return TranscodeLimits{}, fmt.Errorf("%w: invalid limits", ErrTranscodeResourceLimit)
	}
	return limits, nil
}

func pixelPayloadBytes(pixel PixelData) int64 {
	if !pixel.Encapsulated {
		return int64(len(pixel.Raw))
	}
	total := int64(len(pixel.Sequence.OffsetTable))
	for _, fragment := range pixel.Sequence.Fragments {
		if int64(len(fragment)) > math.MaxInt64-total {
			return math.MaxInt64
		}
		total += int64(len(fragment))
	}
	return total
}

func transcodeLimitError(field string) error {
	return fmt.Errorf("%w: %s", ErrTranscodeResourceLimit, field)
}
