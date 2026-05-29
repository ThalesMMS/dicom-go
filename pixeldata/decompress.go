package pixeldata

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrUnsupportedDecompression = errors.New("dicom: unsupported still-image decompression")
	ErrDecompressResourceLimit  = errors.New("dicom: decompression resource limit exceeded")
	ErrInvalidDecompressLimits  = errors.New("dicom: invalid decompression limits")
)

const (
	defaultDecompressMaxFrames            = 100_000
	defaultDecompressMaxPixels      int64 = 256 * 1024 * 1024
	defaultDecompressMaxNativeBytes       = 512 << 20
	defaultDecompressMaxInputBytes        = 512 << 20
	defaultDecompressExpansionRatio       = 1024
)

var (
	tagFileMetaTransferSyntaxUID             = core.NewTag(0x0002, 0x0010)
	tagExtendedOffsetTable                   = core.NewTag(0x7FE0, 0x0001)
	tagExtendedOffsetTableLengths            = core.NewTag(0x7FE0, 0x0002)
	tagEncapsulatedPixelDataValueTotalLength = core.NewTag(0x7FE0, 0x0003)
)

type DecompressOptions struct {
	// Registry supplies optional still-image pixel codecs. When nil, the package
	// DefaultRegistry is used for compressed transfer syntaxes. Native and
	// Encapsulated Uncompressed inputs do not require a registry.
	Registry Registry
	// TargetTransferSyntax selects the native uncompressed transfer syntax for
	// the returned dataset/file. The zero value defaults to Explicit VR Little
	// Endian. Supported targets are Explicit VR Little Endian and Implicit VR
	// Little Endian.
	TargetTransferSyntax transfer.Syntax
	// Limits bounds work and allocations. Zero-valued fields use finite defaults.
	Limits DecompressLimits
}

// DecompressLimits bounds still-image decompression. Zero-valued fields use
// the finite values returned by DefaultDecompressLimits.
type DecompressLimits struct {
	// MaxFrames bounds Number of Frames.
	MaxFrames int
	// MaxPixels bounds Rows * Columns * Number of Frames, excluding samples.
	MaxPixels int64
	// MaxNativeBytes bounds the predicted decoded Pixel Data payload.
	MaxNativeBytes int64
	// MaxInputBytes bounds native bytes or the encapsulated BOT plus fragments.
	MaxInputBytes int64
	// MaxExpansionRatio bounds ceil(predicted native bytes / input bytes) for
	// compressed encapsulated inputs.
	MaxExpansionRatio int64
}

// DefaultDecompressLimits returns the finite limits used by zero-value options.
func DefaultDecompressLimits() DecompressLimits {
	return DecompressLimits{
		MaxFrames:         defaultDecompressMaxFrames,
		MaxPixels:         defaultDecompressMaxPixels,
		MaxNativeBytes:    defaultDecompressMaxNativeBytes,
		MaxInputBytes:     defaultDecompressMaxInputBytes,
		MaxExpansionRatio: defaultDecompressExpansionRatio,
	}
}

// DecompressResourceLimitError describes a bounded decompression rejection.
// Limit is one of the fixed DecompressLimits field names and contains no DICOM
// metadata, path, or patient information.
type DecompressResourceLimitError struct {
	Limit   string
	Actual  int64
	Maximum int64
}

func (e *DecompressResourceLimitError) Error() string {
	if e == nil {
		return ErrDecompressResourceLimit.Error()
	}
	return fmt.Sprintf("%s: %s actual=%d maximum=%d", ErrDecompressResourceLimit, e.Limit, e.Actual, e.Maximum)
}

func (e *DecompressResourceLimitError) Unwrap() error {
	return ErrDecompressResourceLimit
}

// ContextCodec is an optional extension implemented by codecs that can honor
// cancellation while decoding. Existing Codec implementations remain valid;
// context-aware decompression prefers this interface when it is available.
type ContextCodec interface {
	DecodeContext(context.Context, PixelData, *object.Object) (Frames, error)
}

// DecompressFile returns a new File whose Pixel Data is native uncompressed
// bytes. Supported still-image compressed syntaxes are decoded through the
// supplied registry; native and Encapsulated Uncompressed inputs are normalized
// without a pixel codec. Compression/recompression is intentionally out of
// scope.
func DecompressFile(file *object.File, opts DecompressOptions) (*object.File, error) {
	return DecompressFileContext(context.Background(), file, opts)
}

// DecompressFileContext is DecompressFile with cancellation support.
func DecompressFileContext(ctx context.Context, file *object.File, opts DecompressOptions) (*object.File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if file == nil {
		return nil, fmt.Errorf("%w: file is nil", ErrMissingMetadata)
	}
	source, err := sourceTransferSyntax(file)
	if err != nil {
		return nil, err
	}
	dataset, target, err := DecompressDataSetContext(ctx, file.Dataset, source, opts)
	if err != nil {
		return nil, err
	}
	return &object.File{
		Preamble:       core.CloneBytes(file.Preamble),
		Meta:           fileMetaWithTransferSyntax(file.Meta, target),
		Dataset:        dataset,
		TransferSyntax: target,
	}, nil
}

// DecompressDataSet returns a new dataset and native uncompressed target
// transfer syntax. The input source syntax describes how Pixel Data should be
// decoded; the returned syntax defaults to Explicit VR Little Endian.
func DecompressDataSet(dataset *object.Object, source transfer.Syntax, opts DecompressOptions) (*object.Object, transfer.Syntax, error) {
	return DecompressDataSetContext(context.Background(), dataset, source, opts)
}

// DecompressDataSetContext is DecompressDataSet with cancellation support.
func DecompressDataSetContext(ctx context.Context, dataset *object.Object, source transfer.Syntax, opts DecompressOptions) (*object.Object, transfer.Syntax, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, transfer.Syntax{}, err
	}
	if dataset == nil {
		return nil, transfer.Syntax{}, fmt.Errorf("%w: dataset is nil", ErrMissingMetadata)
	}
	limits, err := normalizedDecompressLimits(opts.Limits)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	source, err = canonicalSourceTransferSyntax(source)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	if !source.Supported && !source.RequiresCodec() {
		return nil, transfer.Syntax{}, fmt.Errorf("%w: source transfer syntax %q is not supported", ErrUnsupportedDecompression, source.UID)
	}
	target, err := decompressionTargetTransferSyntax(opts.TargetTransferSyntax)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}

	pixel, err := ExtractView(dataset)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	inputBytes, ok := decompressionInputBytes(pixel)
	if !ok {
		return nil, transfer.Syntax{}, decompressLimitError("MaxInputBytes", math.MaxInt64, limits.MaxInputBytes)
	}
	if inputBytes > limits.MaxInputBytes {
		return nil, transfer.Syntax{}, decompressLimitError("MaxInputBytes", inputBytes, limits.MaxInputBytes)
	}
	metadata, err := ExtractMetadata(dataset)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	if metadata.Rows == 0 || metadata.Columns == 0 || metadata.SamplesPerPixel == 0 || metadata.BitsAllocated == 0 || metadata.NumberOfFrames <= 0 {
		return nil, transfer.Syntax{}, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
			ErrInvalidMetadata,
			metadata.Rows,
			metadata.Columns,
			metadata.SamplesPerPixel,
			metadata.BitsAllocated,
			metadata.NumberOfFrames,
		)
	}
	if metadata.NumberOfFrames > limits.MaxFrames {
		return nil, transfer.Syntax{}, decompressLimitError("MaxFrames", int64(metadata.NumberOfFrames), int64(limits.MaxFrames))
	}
	pixels, ok := checkedDecompressProduct(int64(metadata.Rows), int64(metadata.Columns), int64(metadata.NumberOfFrames))
	if !ok {
		return nil, transfer.Syntax{}, decompressLimitError("MaxPixels", math.MaxInt64, limits.MaxPixels)
	}
	if pixels > limits.MaxPixels {
		return nil, transfer.Syntax{}, decompressLimitError("MaxPixels", pixels, limits.MaxPixels)
	}
	nativeBytes, ok := estimatedDecompressNativeBytes(metadata, source)
	if !ok {
		return nil, transfer.Syntax{}, decompressLimitError("MaxNativeBytes", math.MaxInt64, limits.MaxNativeBytes)
	}
	if nativeBytes > limits.MaxNativeBytes {
		return nil, transfer.Syntax{}, decompressLimitError("MaxNativeBytes", nativeBytes, limits.MaxNativeBytes)
	}
	if source.Encapsulated && source.UID != transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID {
		ratio := decompressionExpansionRatio(nativeBytes, inputBytes)
		if ratio > limits.MaxExpansionRatio {
			return nil, transfer.Syntax{}, decompressLimitError("MaxExpansionRatio", ratio, limits.MaxExpansionRatio)
		}
	}
	frames, err := decodeForDecompressionContext(ctx, source, pixel, dataset, opts.Registry)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	outputMetadata := decompressedOutputMetadata(metadata, source, frames)
	frames, err = littleEndianOutputFramesContext(ctx, outputMetadata, source, frames)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	raw, err := nativeFrameBytesContext(ctx, outputMetadata, frames)
	if err != nil {
		return nil, transfer.Syntax{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, transfer.Syntax{}, err
	}

	return datasetWithNativePixelData(dataset, outputMetadata, raw, source), target, nil
}

func normalizedDecompressLimits(limits DecompressLimits) (DecompressLimits, error) {
	if limits.MaxFrames < 0 || limits.MaxPixels < 0 || limits.MaxNativeBytes < 0 || limits.MaxInputBytes < 0 || limits.MaxExpansionRatio < 0 {
		return DecompressLimits{}, ErrInvalidDecompressLimits
	}
	defaults := DefaultDecompressLimits()
	if limits.MaxFrames == 0 {
		limits.MaxFrames = defaults.MaxFrames
	}
	if limits.MaxPixels == 0 {
		limits.MaxPixels = defaults.MaxPixels
	}
	if limits.MaxNativeBytes == 0 {
		limits.MaxNativeBytes = defaults.MaxNativeBytes
	}
	if limits.MaxInputBytes == 0 {
		limits.MaxInputBytes = defaults.MaxInputBytes
	}
	if limits.MaxExpansionRatio == 0 {
		limits.MaxExpansionRatio = defaults.MaxExpansionRatio
	}
	return limits, nil
}

func decompressLimitError(limit string, actual, maximum int64) error {
	return &DecompressResourceLimitError{Limit: limit, Actual: actual, Maximum: maximum}
}

func checkedDecompressProduct(values ...int64) (int64, bool) {
	product := int64(1)
	for _, value := range values {
		if value <= 0 || product > math.MaxInt64/value {
			return 0, false
		}
		product *= value
	}
	return product, true
}

func estimatedDecompressNativeBytes(metadata Metadata, source transfer.Syntax) (int64, bool) {
	nativeBytes := metadata.TotalSize()
	if nativeBytes <= 0 {
		return 0, false
	}
	if normalizedPhotometric(metadata.PhotometricInterpretation) != "YBR_FULL_422" {
		return nativeBytes, true
	}
	switch transfer.NormalizeUID(source.UID) {
	case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID:
	default:
		return nativeBytes, true
	}
	if metadata.BitsAllocated < 8 {
		return nativeBytes, true
	}
	expandedBytes, ok := checkedDecompressProduct(
		int64(metadata.Rows),
		int64(metadata.Columns),
		int64(metadata.SamplesPerPixel),
		int64(metadata.BytesPerSample()),
		int64(metadata.NumberOfFrames),
	)
	if !ok {
		return 0, false
	}
	if expandedBytes > nativeBytes {
		return expandedBytes, true
	}
	return nativeBytes, true
}

func decompressionInputBytes(pixel PixelData) (int64, bool) {
	if !pixel.Encapsulated {
		return int64(len(pixel.Raw)), true
	}
	total := int64(len(pixel.Sequence.OffsetTable))
	for _, fragment := range pixel.Sequence.Fragments {
		length := int64(len(fragment))
		if total > math.MaxInt64-length {
			return 0, false
		}
		total += length
	}
	return total, true
}

func decompressionExpansionRatio(nativeBytes, inputBytes int64) int64 {
	if nativeBytes <= 0 {
		return 0
	}
	if inputBytes <= 0 {
		return math.MaxInt64
	}
	ratio := nativeBytes / inputBytes
	if nativeBytes%inputBytes != 0 {
		ratio++
	}
	return ratio
}

func sourceTransferSyntax(file *object.File) (transfer.Syntax, error) {
	if file == nil {
		return transfer.Syntax{}, fmt.Errorf("%w: file is nil", ErrMissingMetadata)
	}
	fileUID := transfer.NormalizeUID(file.TransferSyntax.UID)
	var metaUID string
	if file.Meta != nil {
		if uid, ok := file.Meta.GetString(tagFileMetaTransferSyntaxUID); ok {
			metaUID = transfer.NormalizeUID(uid)
		}
	}
	if fileUID != "" && metaUID != "" && fileUID != metaUID {
		return transfer.Syntax{}, fmt.Errorf("%w: file transfer syntax does not match File Meta Information", ErrIncompatiblePixelData)
	}
	if fileUID != "" {
		return canonicalSourceTransferSyntax(file.TransferSyntax)
	}
	if metaUID != "" {
		return canonicalSourceTransferSyntax(transfer.Syntax{UID: metaUID})
	}
	return transfer.Syntax{}, fmt.Errorf("%w: source transfer syntax is empty", ErrIncompatiblePixelData)
}

func canonicalSourceTransferSyntax(syntax transfer.Syntax) (transfer.Syntax, error) {
	uid := transfer.NormalizeUID(syntax.UID)
	if uid == "" {
		return transfer.Syntax{}, fmt.Errorf("%w: source transfer syntax is empty", ErrIncompatiblePixelData)
	}
	if got, ok := transfer.DefaultRegistry.Get(uid); ok {
		return got, nil
	}
	return transfer.Syntax{}, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, uid)
}

func decompressionTargetTransferSyntax(syntax transfer.Syntax) (transfer.Syntax, error) {
	uid := transfer.NormalizeUID(syntax.UID)
	if uid == "" {
		return transfer.ExplicitVRLittleEndian, nil
	}
	target, ok := transfer.DefaultRegistry.Get(uid)
	if !ok {
		return transfer.Syntax{}, fmt.Errorf("%w: %q", transfer.ErrUnknownTransferSyntax, uid)
	}
	switch target.UID {
	case transfer.ExplicitVRLittleEndian.UID, transfer.ImplicitVRLittleEndian.UID:
		return target, nil
	default:
		return transfer.Syntax{}, fmt.Errorf("%w: target transfer syntax %q is not a supported native little-endian syntax", ErrUnsupportedDecompression, target.UID)
	}
}

func decodeForDecompressionContext(ctx context.Context, source transfer.Syntax, pixel PixelData, dataset *object.Object, registry Registry) (Frames, error) {
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}
	switch {
	case source.Encapsulated && !pixel.Encapsulated:
		return Frames{}, fmt.Errorf("%w: transfer syntax %q expects encapsulated pixel data", ErrIncompatiblePixelData, source.UID)
	case !source.Encapsulated && pixel.Encapsulated:
		return Frames{}, fmt.Errorf("%w: transfer syntax %q expects native pixel data", ErrIncompatiblePixelData, source.UID)
	}
	if registry == nil {
		if !source.Encapsulated || source.UID == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID {
			registry = NewMemoryRegistry()
		} else {
			registry = DefaultRegistry
		}
	}
	if registry == nil {
		return Frames{}, codecAvailabilityError(ErrCodecRegistryNil, source.UID, nil)
	}
	if codec, ok := registry.GetCodec(source.UID); ok {
		if contextual, ok := codec.(ContextCodec); ok {
			frames, err := contextual.DecodeContext(ctx, pixel, dataset)
			if ctxErr := ctx.Err(); ctxErr != nil {
				return Frames{}, ctxErr
			}
			if err != nil {
				return Frames{}, codecDecodeError(source.UID, err)
			}
			return frames, nil
		}
	}
	frames, err := registry.DecodeFrames(source.UID, pixel, dataset)
	if ctxErr := ctx.Err(); ctxErr != nil {
		return Frames{}, ctxErr
	}
	return frames, err
}

func nativeFrameBytesContext(ctx context.Context, metadata Metadata, frames Frames) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if frames.Rows != int(metadata.Rows) || frames.Columns != int(metadata.Columns) {
		return nil, fmt.Errorf(
			"%w: decoded frame geometry=%dx%d metadata=%dx%d",
			ErrPixelDataSizeMismatch,
			frames.Rows,
			frames.Columns,
			metadata.Rows,
			metadata.Columns,
		)
	}
	if len(frames.Data) != metadata.NumberOfFrames {
		return nil, fmt.Errorf(
			"%w: decoded frame count=%d metadata NumberOfFrames=%d",
			ErrPixelDataSizeMismatch,
			len(frames.Data),
			metadata.NumberOfFrames,
		)
	}

	frameSize := metadata.FrameSize()
	totalSize := metadata.TotalSize()
	maxInt := int64(^uint(0) >> 1)
	if frameSize <= 0 || totalSize <= 0 || frameSize > maxInt || totalSize > maxInt {
		return nil, fmt.Errorf(
			"%w: rows=%d columns=%d samples_per_pixel=%d bits_allocated=%d number_of_frames=%d",
			ErrInvalidMetadata,
			metadata.Rows,
			metadata.Columns,
			metadata.SamplesPerPixel,
			metadata.BitsAllocated,
			metadata.NumberOfFrames,
		)
	}

	raw := bytes.NewBuffer(make([]byte, 0, int(totalSize)))
	for i, frame := range frames.Data {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(frame) != int(frameSize) {
			return nil, fmt.Errorf(
				"%w: decoded frame %d has %d bytes, want %d",
				ErrPixelDataSizeMismatch,
				i,
				len(frame),
				frameSize,
			)
		}
		raw.Write(frame)
	}
	return raw.Bytes(), nil
}

func datasetWithNativePixelData(dataset *object.Object, metadata Metadata, raw []byte, source transfer.Syntax) *object.Object {
	elements := dataset.Elements()
	if source.ByteOrder == binary.BigEndian {
		elements = transcodeElementsToLittleEndian(elements)
	}
	if source.Encapsulated {
		elements = removeElements(elements,
			tagExtendedOffsetTable,
			tagExtendedOffsetTableLengths,
			tagEncapsulatedPixelDataValueTotalLength,
		)
	}
	replacements := []core.Element{
		core.NewRawElement(core.TagPixelData, nativePixelDataVR(metadata), raw),
		photometricInterpretationElement(metadata.PhotometricInterpretation),
	}
	if metadata.PlanarConfigurationPresent {
		replacements = append(replacements, uint16RawElement(tagPlanarConfiguration, metadata.PlanarConfiguration))
	}
	for _, replacement := range replacements {
		elements = replaceElement(elements, replacement)
	}
	return object.FromElements(elements, nil)
}

func removeElements(elements []core.Element, tags ...core.Tag) []core.Element {
	removed := make(map[core.Tag]struct{}, len(tags))
	for _, tag := range tags {
		removed[tag] = struct{}{}
	}
	out := make([]core.Element, 0, len(elements))
	for _, element := range elements {
		if _, ok := removed[element.Tag()]; ok {
			continue
		}
		out = append(out, element)
	}
	return out
}

func nativePixelDataVR(metadata Metadata) core.VR {
	if metadata.BitsAllocated > 8 {
		return core.VROW
	}
	return core.VROB
}

func littleEndianOutputFramesContext(ctx context.Context, metadata Metadata, source transfer.Syntax, frames Frames) (Frames, error) {
	if err := ctx.Err(); err != nil {
		return Frames{}, err
	}
	if source.ByteOrder != binary.BigEndian || metadata.BytesPerSample() <= 1 {
		return frames, nil
	}
	sampleBytes := metadata.BytesPerSample()
	out := Frames{
		Rows:    frames.Rows,
		Columns: frames.Columns,
		Data:    make([][]byte, len(frames.Data)),
	}
	for i, frame := range frames.Data {
		if err := ctx.Err(); err != nil {
			return Frames{}, err
		}
		if len(frame)%sampleBytes != 0 {
			return Frames{}, fmt.Errorf(
				"%w: decoded frame %d has %d bytes, not a multiple of %d-byte samples",
				ErrPixelDataSizeMismatch,
				i,
				len(frame),
				sampleBytes,
			)
		}
		out.Data[i] = reverseFixedWidthValues(frame, sampleBytes)
	}
	return out, nil
}

func decompressedOutputMetadata(metadata Metadata, source transfer.Syntax, frames Frames) Metadata {
	if metadata.SamplesPerPixel != 3 {
		return metadata
	}
	switch transfer.NormalizeUID(source.UID) {
	case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID:
		switch normalizedPhotometric(metadata.PhotometricInterpretation) {
		case "YBR_FULL":
			metadata.PhotometricInterpretation = "RGB"
			metadata.PlanarConfiguration = 0
			metadata.PlanarConfigurationPresent = true
		case "YBR_FULL_422":
			if decodedFramesUseExpandedRGB(metadata, frames) {
				metadata.PhotometricInterpretation = "RGB"
				metadata.PlanarConfiguration = 0
				metadata.PlanarConfigurationPresent = true
			}
		}
	}
	return metadata
}

func decodedFramesUseExpandedRGB(metadata Metadata, frames Frames) bool {
	frameSize, ok := expandedRGBFrameSize(metadata)
	if !ok || len(frames.Data) == 0 {
		return false
	}
	if frameSize > int64(^uint(0)>>1) {
		return false
	}
	for _, frame := range frames.Data {
		if len(frame) != int(frameSize) {
			return false
		}
	}
	return true
}

func expandedRGBFrameSize(metadata Metadata) (int64, bool) {
	if metadata.BitsAllocated < 8 {
		return 0, false
	}
	return int64(metadata.Rows) * int64(metadata.Columns) * 3 * int64(metadata.BytesPerSample()), true
}

func photometricInterpretationElement(value string) core.Element {
	return core.Element{
		Header: core.ElementHeader{Tag: tagPhotometricInterpretation, VR: core.VRCS},
		Value:  core.StringValue{value},
	}
}

func uint16RawElement(tag core.Tag, value uint16) core.Element {
	var raw [2]byte
	binary.LittleEndian.PutUint16(raw[:], value)
	return core.NewRawElement(tag, core.VRUS, raw[:])
}

func normalizedPhotometric(value string) string {
	return strings.Trim(strings.ToUpper(value), " \x00")
}

func transcodeElementsToLittleEndian(elements []core.Element) []core.Element {
	// dataset.Elements() already returned an owned top-level slice. Update that
	// slice in place and let nested sequences use copy-on-write so the source
	// object remains immutable without cloning unaffected sequence trees.
	for i := range elements {
		if value, changed := transcodeValueToLittleEndian(elements[i].VR(), elements[i].Value); changed {
			elements[i].Value = value
		}
	}
	return elements
}

func transcodeValueToLittleEndian(vr core.VR, value core.Value) (core.Value, bool) {
	switch v := value.(type) {
	case core.RawValue:
		width := byteOrderValueWidth(vr)
		if width <= 1 || len(v) < width {
			return value, false
		}
		return core.RawValue(reverseFixedWidthValues(v.Bytes(), width)), true
	case core.SequenceValue:
		var items []core.DataSet
		for i, item := range v.Items {
			elements, changed := transcodeElementsToLittleEndianCopy(item.Elements)
			if !changed {
				continue
			}
			if items == nil {
				items = append([]core.DataSet(nil), v.Items...)
			}
			items[i] = item
			items[i].Elements = elements
		}
		if items == nil {
			return value, false
		}
		return core.SequenceValue{Items: items}, true
	default:
		return value, false
	}
}

func transcodeElementsToLittleEndianCopy(elements []core.Element) ([]core.Element, bool) {
	var out []core.Element
	for i, element := range elements {
		value, changed := transcodeValueToLittleEndian(element.VR(), element.Value)
		if !changed {
			continue
		}
		if out == nil {
			out = append([]core.Element(nil), elements...)
		}
		out[i].Value = value
	}
	if out == nil {
		return elements, false
	}
	return out, true
}

func byteOrderValueWidth(vr core.VR) int {
	switch vr {
	case core.VRAT, core.VROW, core.VRSS, core.VRUS:
		return 2
	case core.VRFL, core.VROF, core.VROL, core.VRSL, core.VRUL:
		return 4
	case core.VRFD, core.VROD, core.VROV, core.VRSV, core.VRUV:
		return 8
	default:
		return 1
	}
}

func reverseFixedWidthValues(data []byte, width int) []byte {
	out := core.CloneBytes(data)
	if width <= 1 {
		return out
	}
	for offset := 0; offset+width <= len(out); offset += width {
		for left, right := offset, offset+width-1; left < right; left, right = left+1, right-1 {
			out[left], out[right] = out[right], out[left]
		}
	}
	return out
}

func fileMetaWithTransferSyntax(meta *object.Object, syntax transfer.Syntax) *object.Object {
	var elements []core.Element
	if meta != nil {
		elements = append(elements, meta.Elements()...)
	}
	elements = replaceElement(elements, core.Element{
		Header: core.ElementHeader{Tag: tagFileMetaTransferSyntaxUID, VR: core.VRUI},
		Value:  core.StringValue{syntax.UID},
	})
	return object.FromElements(elements, nil)
}

func replaceElement(elements []core.Element, replacement core.Element) []core.Element {
	out := make([]core.Element, 0, len(elements)+1)
	replaced := false
	for _, element := range elements {
		if element.Tag() == replacement.Tag() {
			out = append(out, replacement)
			replaced = true
			continue
		}
		out = append(out, element)
	}
	if !replaced {
		out = append(out, replacement)
	}
	return out
}
