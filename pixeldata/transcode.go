package pixeldata

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrTranscodeUnsupported       = errors.New("dicom: transfer syntax transcode is unsupported")
	ErrTranscodeResourceLimit     = errors.New("dicom: transcode resource limit exceeded")
	ErrTranscodeLossyDisallowed   = errors.New("dicom: lossy transcode requires explicit opt-in")
	ErrTranscodeSourceChanged     = errors.New("dicom: transcode source changed")
	ErrTranscodeDestinationUnsafe = errors.New("dicom: transcode destination is unsafe")
	ErrTranscodeTransaction       = errors.New("dicom: transcode transaction failed")
)

var (
	tagFloatPixelData              = core.NewTag(0x7FE0, 0x0008)
	tagDoubleFloatPixelData        = core.NewTag(0x7FE0, 0x0009)
	tagLossyImageCompression       = core.NewTag(0x0028, 0x2110)
	tagLossyImageCompressionRatio  = core.NewTag(0x0028, 0x2112)
	tagLossyImageCompressionMethod = core.NewTag(0x0028, 0x2114)
	tagImageType                   = core.NewTag(0x0008, 0x0008)
	tagSOPInstanceUID              = core.NewTag(0x0008, 0x0018)
	tagMediaStorageSOPInstanceUID  = core.NewTag(0x0002, 0x0003)
)

const (
	defaultTranscodeMaxFrames      = 10000
	defaultTranscodeMaxPixels      = uint64(1) << 30
	defaultTranscodeMaxInputBytes  = int64(1) << 30
	defaultTranscodeMaxOutputBytes = int64(1) << 30
	defaultTranscodeMaxFragments   = 100000
	defaultTranscodeMaxElements    = 1000000
	defaultTranscodeMaxDepth       = 64
	defaultTranscodeMaxExpansion   = int64(1024)
	defaultTranscodeMaxDuration    = 5 * time.Minute
)

// TranscodeLimits bound all input traversal, frame processing, and output
// construction. Zero fields use finite defaults; the public API has no
// implicit unlimited mode.
type TranscodeLimits struct {
	MaxFrames         int
	MaxPixels         uint64
	MaxInputBytes     int64
	MaxOutputBytes    int64
	MaxFragments      int
	MaxElements       int
	MaxDepth          int
	MaxExpansionRatio int64
	MaxDuration       time.Duration
}

// TranscodeOptions supplies explicit codec registries and safety policy.
// DecoderRegistry is required for compressed inputs and EncoderRegistry is
// required for encoded outputs. Neither falls back to a package global.
type TranscodeOptions struct {
	DecoderRegistry Registry
	EncoderRegistry EncoderRegistry
	Limits          TranscodeLimits
	AllowLossy      bool
	// ForceReencode disables the equivalent-transfer-syntax pixel fast path.
	ForceReencode bool
}

// TranscodeReport contains value-free operational metadata.
type TranscodeReport struct {
	Frames             int
	NativeBytes        int64
	EncodedBytes       int64
	PixelDataPreserved bool
	Lossy              bool
}

// TranscodeError identifies a stable stage without echoing paths, patient
// values, or backend-controlled text.
type TranscodeError struct {
	Stage string
	Err   error
}

func (e *TranscodeError) Error() string {
	if e == nil || e.Stage == "" {
		return "dicom: transcode failed"
	}
	return "dicom: transcode failed during " + e.Stage
}

func (e *TranscodeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// DefaultTranscodeLimits returns the finite limits used by zero-valued
// TranscodeOptions.
func DefaultTranscodeLimits() TranscodeLimits {
	return TranscodeLimits{
		MaxFrames:         defaultTranscodeMaxFrames,
		MaxPixels:         defaultTranscodeMaxPixels,
		MaxInputBytes:     defaultTranscodeMaxInputBytes,
		MaxOutputBytes:    defaultTranscodeMaxOutputBytes,
		MaxFragments:      defaultTranscodeMaxFragments,
		MaxElements:       defaultTranscodeMaxElements,
		MaxDepth:          defaultTranscodeMaxDepth,
		MaxExpansionRatio: defaultTranscodeMaxExpansion,
		MaxDuration:       defaultTranscodeMaxDuration,
	}
}

// TranscodeDataSet returns a detached data set encoded for target. Input is
// borrowed and never mutated.
func TranscodeDataSet(ctx context.Context, dataset *object.Object, source, target transfer.Syntax, options TranscodeOptions) (*object.Object, TranscodeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, TranscodeReport{}, err
	}
	if dataset == nil {
		return nil, TranscodeReport{}, fmt.Errorf("%w: dataset is nil", ErrMissingMetadata)
	}
	limits, err := normalizeTranscodeLimits(options.Limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	inputLimits := limits
	inputLimits.MaxOutputBytes = limits.MaxInputBytes
	if _, err := measureTranscodeObject(ctx, dataset, inputLimits); err != nil {
		return nil, TranscodeReport{}, err
	}
	source, err = canonicalSourceTransferSyntax(source)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	target, err = canonicalTargetTransferSyntax(target)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if (source.UID != target.UID || options.ForceReencode) && (source.Encapsulated || target.Encapsulated) {
		nested, nestedErr := hasNestedPixelData(dataset, limits)
		if nestedErr != nil {
			return nil, TranscodeReport{}, nestedErr
		}
		if nested {
			return nil, TranscodeReport{}, fmt.Errorf("%w: nested Pixel Data transcode", ErrTranscodeUnsupported)
		}
	}
	if !source.Encapsulated && !target.Encapsulated {
		if source.ByteOrder != target.ByteOrder {
			if endianErr := validateEndianConversion(dataset, limits); endianErr != nil {
				return nil, TranscodeReport{}, endianErr
			}
		}
		metadata, pixelBytes, metadataPresent, validateErr := validateNativeTranscodeInput(dataset, source, limits)
		if validateErr != nil {
			return nil, TranscodeReport{}, validateErr
		}
		clone, cloneErr := cloneDetachedObject(ctx, dataset, equivalentCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		if source.ByteOrder != target.ByteOrder && source.ByteOrder != nil && target.ByteOrder != nil {
			clone = object.FromElements(transcodeElementsToLittleEndian(clone.Elements()), nil)
		}
		clone.SetValueByteOrder(target.ByteOrder)
		report := TranscodeReport{PixelDataPreserved: true, EncodedBytes: pixelBytes}
		if metadataPresent {
			report.Frames = metadata.NumberOfFrames
			report.NativeBytes = metadata.TotalSize()
		}
		return clone, report, nil
	}
	if !hasAnyPixelData(dataset) {
		if source.Encapsulated || target.Encapsulated {
			return nil, TranscodeReport{}, ErrPixelDataNotFound
		}
		clone, cloneErr := cloneDetachedObject(ctx, dataset, equivalentCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		if source.ByteOrder != target.ByteOrder && source.ByteOrder != nil && target.ByteOrder != nil {
			clone = object.FromElements(transcodeElementsToLittleEndian(clone.Elements()), nil)
		}
		clone.SetValueByteOrder(target.ByteOrder)
		return clone, TranscodeReport{PixelDataPreserved: true}, nil
	}
	metadata, pixel, err := validateTranscodePixelData(dataset, source, limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}

	if source.UID == target.UID && !options.ForceReencode {
		clone, cloneErr := cloneDetachedObject(ctx, dataset, equivalentCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		return clone, TranscodeReport{
			Frames:             metadata.NumberOfFrames,
			NativeBytes:        metadata.TotalSize(),
			EncodedBytes:       pixelPayloadBytes(pixel),
			PixelDataPreserved: true,
		}, nil
	}
	if metadata.BitsAllocated < 8 {
		return nil, TranscodeReport{}, fmt.Errorf("%w: per-frame transcode with BitsAllocated below 8", ErrTranscodeUnsupported)
	}

	if source.Encapsulated && source.UID != transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID && options.DecoderRegistry == nil {
		return nil, TranscodeReport{}, codecAvailabilityError(ErrCodecRegistryNil, source.UID, nil)
	}
	decompressed, _, err := DecompressDataSetContext(ctx, dataset, source, DecompressOptions{
		Registry:             options.DecoderRegistry,
		TargetTransferSyntax: transfer.ExplicitVRLittleEndian,
		Limits: DecompressLimits{
			MaxFrames:         limits.MaxFrames,
			MaxPixels:         int64(limits.MaxPixels),
			MaxInputBytes:     limits.MaxInputBytes,
			MaxNativeBytes:    limits.MaxOutputBytes,
			MaxExpansionRatio: limits.MaxExpansionRatio,
		},
	})
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	metadata, err = ExtractMetadata(decompressed)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	nativeBytes := metadata.TotalSize()
	if nativeBytes <= 0 || nativeBytes > limits.MaxOutputBytes {
		return nil, TranscodeReport{}, transcodeLimitError("native_bytes")
	}
	report := TranscodeReport{Frames: metadata.NumberOfFrames, NativeBytes: nativeBytes}

	switch target.UID {
	case transfer.ImplicitVRLittleEndian.UID, transfer.ExplicitVRLittleEndian.UID, transfer.DeflatedExplicitVRLittleEndian.UID:
		result, cloneErr := cloneDetachedObject(ctx, decompressed, outputCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		result.SetValueByteOrder(binary.LittleEndian)
		return result, report, nil
	case transfer.ExplicitVRBigEndian.UID:
		elements := transcodeElementsToLittleEndian(decompressed.Elements())
		result := object.FromElements(elements, nil)
		result.SetValueByteOrder(binary.BigEndian)
		result, cloneErr := cloneDetachedObject(ctx, result, outputCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		return result, report, nil
	case transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID:
		frames, frameErr := ExtractNativeFramesView(decompressed)
		if frameErr != nil {
			return nil, TranscodeReport{}, frameErr
		}
		fragments := cloneFrames(frames.Data)
		result, encodedBytes, encodeErr := datasetWithEncodedFrames(ctx, decompressed, metadata, fragments, nil, limits)
		if encodeErr != nil {
			return nil, TranscodeReport{}, encodeErr
		}
		report.EncodedBytes = encodedBytes
		return result, report, nil
	default:
		return transcodeWithEncoder(ctx, decompressed, metadata, target, options, limits, report)
	}
}

// TranscodeCoreDataSet is the core.DataSet adapter for TranscodeDataSet. The
// returned data set is detached from the input, including nested values.
func TranscodeCoreDataSet(ctx context.Context, dataset core.DataSet, source, target transfer.Syntax, options TranscodeOptions) (core.DataSet, TranscodeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	limits, err := normalizeTranscodeLimits(options.Limits)
	if err != nil {
		return core.DataSet{}, TranscodeReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	structuralNodes := 0
	if err := validateUniqueCoreDataSet(ctx, dataset, 1, limits, &structuralNodes); err != nil {
		return core.DataSet{}, TranscodeReport{}, err
	}
	result, report, err := TranscodeDataSet(ctx, object.FromDataSet(dataset, nil), source, target, options)
	if err != nil {
		return core.DataSet{}, TranscodeReport{}, err
	}
	return result.ToDataSet(), report, nil
}

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

// TranscodeFile returns a detached Part 10 file and keeps File Meta Transfer
// Syntax synchronized with the output payload.
func TranscodeFile(ctx context.Context, file *object.File, target transfer.Syntax, options TranscodeOptions) (*object.File, TranscodeReport, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, TranscodeReport{}, err
	}
	if file == nil {
		return nil, TranscodeReport{}, fmt.Errorf("%w: file is nil", ErrMissingMetadata)
	}
	limits, err := normalizeTranscodeLimits(options.Limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	if len(file.Preamble) != 0 && len(file.Preamble) != 128 {
		return nil, TranscodeReport{}, fmt.Errorf("%w: invalid Part 10 preamble", ErrTranscodeUnsupported)
	}
	inputLimits := limits
	inputLimits.MaxOutputBytes = limits.MaxInputBytes
	inputUsage, err := measureTranscodeObject(ctx, file.Dataset, inputLimits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if int64(len(file.Preamble)) > limits.MaxInputBytes-inputUsage.bytes {
		return nil, TranscodeReport{}, transcodeLimitError("input_bytes")
	}
	inputUsage.bytes += int64(len(file.Preamble))
	inputMetaLimits := inputLimits
	inputMetaLimits.MaxOutputBytes = limits.MaxInputBytes - inputUsage.bytes
	inputMetaLimits.MaxElements = limits.MaxElements - inputUsage.elements
	if _, err := measureTranscodeObject(ctx, file.Meta, inputMetaLimits); err != nil {
		return nil, TranscodeReport{}, err
	}
	source, err := strictFileTransferSyntax(file)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	dataset, report, err := TranscodeDataSet(ctx, file.Dataset, source, target, options)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	target, err = canonicalTargetTransferSyntax(target)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	meta := fileMetaWithTransferSyntax(file.Meta, target)
	if report.Lossy {
		if sopUID, ok := dataset.GetString(tagSOPInstanceUID); ok {
			meta.Put(core.Element{Header: core.ElementHeader{Tag: tagMediaStorageSOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{sopUID}})
		}
	}
	usage, err := measureTranscodeObject(ctx, dataset, limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if int64(len(file.Preamble)) > limits.MaxOutputBytes-usage.bytes {
		return nil, TranscodeReport{}, transcodeLimitError("output_bytes")
	}
	usage.bytes += int64(len(file.Preamble))
	metaLimits := limits
	metaLimits.MaxInputBytes = limits.MaxOutputBytes - usage.bytes
	metaLimits.MaxElements = limits.MaxElements - usage.elements
	meta, err = cloneDetachedObject(ctx, meta, metaLimits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return nil, TranscodeReport{}, err
	}
	return &object.File{
		Preamble:       core.CloneBytes(file.Preamble),
		Meta:           meta,
		Dataset:        dataset,
		TransferSyntax: target,
	}, report, nil
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

func transcodeWithEncoder(ctx context.Context, dataset *object.Object, metadata Metadata, target transfer.Syntax, options TranscodeOptions, limits TranscodeLimits, report TranscodeReport) (*object.Object, TranscodeReport, error) {
	if target.MediaPayload || IsJPIPReferencedTransferSyntax(target.UID) || target.UID == transfer.DeflatedImageFrameCompression.UID {
		return nil, TranscodeReport{}, fmt.Errorf("%w: target is not a still-image encoder syntax", ErrTranscodeUnsupported)
	}
	if options.EncoderRegistry == nil {
		return nil, TranscodeReport{}, encoderAvailabilityError(ErrEncoderRegistryNil, target.UID, nil)
	}
	encoder, ok := options.EncoderRegistry.GetEncoder(target.UID)
	if !ok {
		return nil, TranscodeReport{}, CheckEncoderAvailability(options.EncoderRegistry, target.UID)
	}
	capabilities, err := encoderCapabilities(encoder)
	if err != nil || transfer.NormalizeUID(capabilities.TransferSyntaxUID) != target.UID || !validEncoderCapabilities(capabilities) {
		return nil, TranscodeReport{}, ErrEncoderCapabilitiesInvalid
	}
	if !capabilities.Lossless && !options.AllowLossy {
		return nil, TranscodeReport{}, ErrTranscodeLossyDisallowed
	}
	if metadataErr := validateEncoderMetadata(capabilities, metadata); metadataErr != nil {
		return nil, TranscodeReport{}, metadataErr
	}
	frames, err := ExtractNativeFramesView(dataset)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	fragments := make([][]byte, len(frames.Data))
	var encodedTotal int64
	var outputPI string
	var outputPlanar *uint16
	for i, frame := range frames.Data {
		if err := ctx.Err(); err != nil {
			return nil, TranscodeReport{}, err
		}
		encoded, encodeErr := callFrameEncoder(ctx, encoder, frame, metadata)
		if encodeErr != nil {
			return nil, TranscodeReport{}, &EncoderEncodeError{Err: encodeErr, TransferSyntaxUID: target.UID}
		}
		if len(encoded.Data) == 0 {
			return nil, TranscodeReport{}, ErrEncoderOutputInvalid
		}
		encoded, encodeErr = validateEncodedMetadataDelta(metadata, capabilities, encoded)
		if encodeErr != nil {
			return nil, TranscodeReport{}, encodeErr
		}
		if int64(len(encoded.Data)) > limits.MaxOutputBytes-encodedTotal {
			return nil, TranscodeReport{}, transcodeLimitError("output_bytes")
		}
		encodedTotal += int64(len(encoded.Data))
		if i == 0 {
			outputPI = encoded.PhotometricInterpretation
			if encoded.PlanarConfiguration != nil {
				value := *encoded.PlanarConfiguration
				outputPlanar = &value
			}
		} else if outputPI != encoded.PhotometricInterpretation || !sameOptionalUint16(outputPlanar, encoded.PlanarConfiguration) {
			return nil, TranscodeReport{}, fmt.Errorf("%w: inconsistent encoder metadata", ErrEncoderOutputInvalid)
		}
		fragments[i] = core.CloneBytes(encoded.Data)
	}
	result, encodedBytes, err := datasetWithEncodedFrames(ctx, dataset, metadata, fragments, &encodedMetadataDelta{photometric: outputPI, planar: outputPlanar}, limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	report.EncodedBytes = encodedBytes
	report.Lossy = !capabilities.Lossless
	if report.Lossy {
		if err := applyLossyTranscodeMetadata(result, metadata.TotalSize(), encodedBytes, capabilities.LossyMethod); err != nil {
			return nil, TranscodeReport{}, err
		}
	}
	return result, report, nil
}

type encodedMetadataDelta struct {
	photometric string
	planar      *uint16
}

func datasetWithEncodedFrames(ctx context.Context, dataset *object.Object, metadata Metadata, fragments [][]byte, delta *encodedMetadataDelta, limits TranscodeLimits) (*object.Object, int64, error) {
	if len(fragments) != metadata.NumberOfFrames || len(fragments) > limits.MaxFrames || len(fragments) > limits.MaxFragments {
		return nil, 0, transcodeLimitError("fragments")
	}
	lengths := make([]uint64, len(fragments))
	var total uint64
	for i, fragment := range fragments {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		lengths[i] = uint64(len(fragment))
		if total > math.MaxUint64-lengths[i] {
			return nil, 0, transcodeLimitError("output_bytes")
		}
		total += lengths[i]
		if total > uint64(limits.MaxOutputBytes) {
			return nil, 0, transcodeLimitError("output_bytes")
		}
	}
	bot, eot, eotLengths, err := frameOffsetTables(lengths)
	if err != nil {
		return nil, 0, err
	}
	elements := removeElements(dataset.Elements(), tagExtendedOffsetTable, tagExtendedOffsetTableLengths, tagEncapsulatedPixelDataValueTotalLength)
	elements = replaceElement(elements, core.Element{
		Header: core.ElementHeader{Tag: core.TagPixelData, VR: core.VROB, Length: core.UndefinedLength, LengthSet: true},
		Value:  core.FragmentSequence{OffsetTable: bot, Fragments: fragments},
	})
	if len(eot) > 0 {
		elements = replaceElement(elements, core.NewRawElement(tagExtendedOffsetTable, core.VROV, eot))
		elements = replaceElement(elements, core.NewRawElement(tagExtendedOffsetTableLengths, core.VROV, eotLengths))
	}
	if delta != nil {
		if strings.TrimSpace(delta.photometric) != "" {
			elements = replaceElement(elements, photometricInterpretationElement(delta.photometric))
		}
		if delta.planar != nil {
			elements = replaceElement(elements, uint16RawElement(tagPlanarConfiguration, *delta.planar))
		}
	}
	result := object.FromElements(elements, nil)
	result.SetValueByteOrder(binary.LittleEndian)
	result, err = cloneDetachedObject(ctx, result, outputCloneLimits(limits))
	if err != nil {
		return nil, 0, err
	}
	return result, int64(total), nil
}

func frameOffsetTables(lengths []uint64) (bot, eot, eotLengths []byte, err error) {
	if len(lengths) == 0 {
		return nil, nil, nil, nil
	}
	offsets := make([]uint64, len(lengths))
	var next uint64
	useEOT := false
	for i, length := range lengths {
		offsets[i] = next
		if next > math.MaxUint32 {
			useEOT = true
		}
		padded := length
		if padded%2 != 0 {
			padded++
		}
		if next > math.MaxUint64-8-padded {
			return nil, nil, nil, transcodeLimitError("offset_table")
		}
		next += 8 + padded
	}
	if !useEOT {
		bot = make([]byte, len(offsets)*4)
		for i, offset := range offsets {
			binary.LittleEndian.PutUint32(bot[i*4:], uint32(offset))
		}
		return bot, nil, nil, nil
	}
	eot = make([]byte, len(offsets)*8)
	eotLengths = make([]byte, len(lengths)*8)
	for i := range offsets {
		binary.LittleEndian.PutUint64(eot[i*8:], offsets[i])
		binary.LittleEndian.PutUint64(eotLengths[i*8:], lengths[i])
	}
	return nil, eot, eotLengths, nil
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

func validateEncodedMetadataDelta(metadata Metadata, capabilities EncoderCapabilities, encoded EncodedFrame) (EncodedFrame, error) {
	photometric := normalizedPhotometric(encoded.PhotometricInterpretation)
	if photometric != "" {
		if photometric != normalizedPhotometric(metadata.PhotometricInterpretation) && !containsPhotometric(capabilities.OutputPhotometricInterpretations, photometric) {
			return EncodedFrame{}, fmt.Errorf("%w: PhotometricInterpretation transform is undeclared", ErrEncoderOutputInvalid)
		}
		switch metadata.SamplesPerPixel {
		case 1:
			if photometric != "MONOCHROME1" && photometric != "MONOCHROME2" && photometric != "PALETTE COLOR" {
				return EncodedFrame{}, fmt.Errorf("%w: PhotometricInterpretation", ErrEncoderOutputInvalid)
			}
		case 3:
			switch photometric {
			case "RGB", "YBR_FULL", "YBR_FULL_422", "YBR_RCT", "YBR_ICT":
			default:
				return EncodedFrame{}, fmt.Errorf("%w: PhotometricInterpretation", ErrEncoderOutputInvalid)
			}
		default:
			return EncodedFrame{}, fmt.Errorf("%w: SamplesPerPixel", ErrEncoderOutputInvalid)
		}
		encoded.PhotometricInterpretation = photometric
	}
	if encoded.PlanarConfiguration != nil {
		if metadata.SamplesPerPixel == 1 || *encoded.PlanarConfiguration > 1 {
			return EncodedFrame{}, fmt.Errorf("%w: PlanarConfiguration", ErrEncoderOutputInvalid)
		}
		if !metadata.PlanarConfigurationPresent || *encoded.PlanarConfiguration != metadata.PlanarConfiguration && !containsUint16(capabilities.OutputPlanarConfigurations, *encoded.PlanarConfiguration) {
			return EncodedFrame{}, fmt.Errorf("%w: PlanarConfiguration transform is undeclared", ErrEncoderOutputInvalid)
		}
		value := *encoded.PlanarConfiguration
		encoded.PlanarConfiguration = &value
	}
	return encoded, nil
}

func containsPhotometric(values []string, want string) bool {
	for _, value := range values {
		if normalizedPhotometric(value) == want {
			return true
		}
	}
	return false
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

func equivalentCloneLimits(limits TranscodeLimits) TranscodeLimits {
	if limits.MaxOutputBytes < limits.MaxInputBytes {
		limits.MaxInputBytes = limits.MaxOutputBytes
	}
	return limits
}

func outputCloneLimits(limits TranscodeLimits) TranscodeLimits {
	limits.MaxInputBytes = limits.MaxOutputBytes
	return limits
}

func cloneDetachedObject(ctx context.Context, source *object.Object, limits TranscodeLimits) (*object.Object, error) {
	if source == nil {
		return nil, nil
	}
	state := cloneState{ctx: ctx, limits: limits}
	elements, err := state.cloneElements(source.Elements(), 1)
	if err != nil {
		return nil, err
	}
	clone := object.FromElements(elements, nil)
	clone.SetValueByteOrder(source.ValueByteOrder())
	return clone, nil
}

type cloneState struct {
	ctx       context.Context
	limits    TranscodeLimits
	elements  int
	fragments int
	bytes     int64
}

func (s *cloneState) cloneElements(elements []core.Element, depth int) ([]core.Element, error) {
	if depth > s.limits.MaxDepth || len(elements) > s.limits.MaxElements-s.elements {
		return nil, transcodeLimitError("elements")
	}
	s.elements += len(elements)
	out := make([]core.Element, len(elements))
	for i, element := range elements {
		if err := s.ctx.Err(); err != nil {
			return nil, err
		}
		out[i] = element
		value, err := s.cloneValue(element.Value, depth)
		if err != nil {
			return nil, err
		}
		out[i].Value = value
	}
	return out, nil
}

func (s *cloneState) cloneValue(value core.Value, depth int) (core.Value, error) {
	if value == nil {
		return nil, fmt.Errorf("%w: deferred value is unavailable", ErrTranscodeUnsupported)
	}
	addBytes := func(n int) error {
		if n < 0 || int64(n) > s.limits.MaxInputBytes-s.bytes {
			return transcodeLimitError("input_bytes")
		}
		s.bytes += int64(n)
		return nil
	}
	switch v := value.(type) {
	case core.RawValue:
		if err := addBytes(len(v)); err != nil {
			return nil, err
		}
		return core.RawValue(core.CloneBytes(v)), nil
	case core.StringValue:
		if len(v) > s.limits.MaxElements-s.elements {
			return nil, transcodeLimitError("elements")
		}
		s.elements += len(v)
		length, ok := v.EncodedLength()
		if !ok || uint64(length) > math.MaxInt {
			return nil, transcodeLimitError("input_bytes")
		}
		if err := addBytes(int(length)); err != nil {
			return nil, err
		}
		return append(core.StringValue(nil), v...), nil
	case core.Uint16Value:
		if err := addBytes(len(v) * 2); err != nil {
			return nil, err
		}
		return append(core.Uint16Value(nil), v...), nil
	case core.Int16Value:
		if err := addBytes(len(v) * 2); err != nil {
			return nil, err
		}
		return append(core.Int16Value(nil), v...), nil
	case core.Uint32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Uint32Value(nil), v...), nil
	case core.Int32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Int32Value(nil), v...), nil
	case core.Uint64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Uint64Value(nil), v...), nil
	case core.Int64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Int64Value(nil), v...), nil
	case core.Float32Value:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.Float32Value(nil), v...), nil
	case core.Float64Value:
		if err := addBytes(len(v) * 8); err != nil {
			return nil, err
		}
		return append(core.Float64Value(nil), v...), nil
	case core.TagValue:
		if err := addBytes(len(v) * 4); err != nil {
			return nil, err
		}
		return append(core.TagValue(nil), v...), nil
	case core.SequenceValue:
		if len(v.Items) > s.limits.MaxElements-s.elements {
			return nil, transcodeLimitError("elements")
		}
		s.elements += len(v.Items)
		items := make([]core.DataSet, len(v.Items))
		for i, item := range v.Items {
			elements, err := s.cloneElements(item.Elements, depth+1)
			if err != nil {
				return nil, err
			}
			items[i] = item
			items[i].Elements = elements
		}
		return core.SequenceValue{Items: items}, nil
	case core.FragmentSequence:
		if len(v.Fragments) > s.limits.MaxFragments-s.fragments {
			return nil, transcodeLimitError("fragments")
		}
		s.fragments += len(v.Fragments)
		if err := addBytes(len(v.OffsetTable)); err != nil {
			return nil, err
		}
		out := core.FragmentSequence{OffsetTable: core.CloneBytes(v.OffsetTable), Fragments: make([][]byte, len(v.Fragments))}
		for i, fragment := range v.Fragments {
			if err := addBytes(len(fragment)); err != nil {
				return nil, err
			}
			out.Fragments[i] = core.CloneBytes(fragment)
		}
		return out, nil
	case core.BulkDataValue:
		if err := addBytes(len(v.URI)); err != nil {
			return nil, err
		}
		return v, nil
	default:
		return nil, fmt.Errorf("%w: unsupported element value", ErrTranscodeUnsupported)
	}
}

func applyLossyTranscodeMetadata(dataset *object.Object, nativeBytes, encodedBytes int64, method string) error {
	if dataset == nil || nativeBytes <= 0 || encodedBytes <= 0 || strings.TrimSpace(method) == "" {
		return fmt.Errorf("%w: invalid lossy encoder result", ErrEncoderOutputInvalid)
	}
	ratios, err := lossyHistoryStrings(dataset, tagLossyImageCompressionRatio, core.VRDS)
	if err != nil {
		return err
	}
	methods, err := lossyHistoryStrings(dataset, tagLossyImageCompressionMethod, core.VRCS)
	if err != nil {
		return err
	}
	if len(ratios) != len(methods) {
		return fmt.Errorf("%w: lossy history multiplicity", ErrEncoderOutputInvalid)
	}
	if !validLossyCompressionMethod(method) {
		return fmt.Errorf("%w: lossy method", ErrEncoderOutputInvalid)
	}
	for _, existing := range methods {
		if !validLossyCompressionMethod(existing) {
			return fmt.Errorf("%w: lossy history method", ErrEncoderOutputInvalid)
		}
	}
	for _, existing := range ratios {
		if !validLossyCompressionRatio(existing) {
			return fmt.Errorf("%w: lossy history ratio", ErrEncoderOutputInvalid)
		}
	}
	dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompression, VR: core.VRCS}, Value: core.StringValue{"01"}})
	ratios = append(ratios, strconv.FormatFloat(float64(nativeBytes)/float64(encodedBytes), 'g', 8, 64))
	dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionRatio, VR: core.VRDS}, Value: core.StringValue(ratios)})
	methods = append(methods, strings.TrimSpace(method))
	dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagLossyImageCompressionMethod, VR: core.VRCS}, Value: core.StringValue(methods)})
	imageTypes := elementStrings(dataset, tagImageType)
	if len(imageTypes) == 0 {
		imageTypes = []string{"DERIVED", "PRIMARY"}
	} else {
		imageTypes[0] = "DERIVED"
	}
	dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagImageType, VR: core.VRCS}, Value: core.StringValue(imageTypes)})
	uid, err := newTranscodeUID()
	if err != nil {
		return &TranscodeError{Stage: "uid", Err: ErrTranscodeTransaction}
	}
	dataset.Put(core.Element{Header: core.ElementHeader{Tag: tagSOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{uid}})
	return nil
}

func lossyHistoryStrings(dataset *object.Object, tag core.Tag, vr core.VR) ([]string, error) {
	element, ok := dataset.Get(tag)
	if !ok {
		return nil, nil
	}
	if element.VR() != vr {
		return nil, fmt.Errorf("%w: lossy history VR", ErrEncoderOutputInvalid)
	}
	return append([]string(nil), element.StringValues()...), nil
}

func validLossyCompressionRatio(value string) bool {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" || len(value) > 16 || strings.Contains(value, "\\") {
		return false
	}
	index := 0
	if trimmed[index] == '+' || trimmed[index] == '-' {
		index++
		if index == len(trimmed) {
			return false
		}
	}
	digits := 0
	for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
		index++
		digits++
	}
	if index < len(trimmed) && trimmed[index] == '.' {
		index++
		for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
			index++
			digits++
		}
	}
	if digits == 0 {
		return false
	}
	if index < len(trimmed) && (trimmed[index] == 'E' || trimmed[index] == 'e') {
		index++
		if index < len(trimmed) && (trimmed[index] == '+' || trimmed[index] == '-') {
			index++
		}
		exponentDigits := 0
		for index < len(trimmed) && trimmed[index] >= '0' && trimmed[index] <= '9' {
			index++
			exponentDigits++
		}
		if exponentDigits == 0 {
			return false
		}
	}
	if index != len(trimmed) {
		return false
	}
	ratio, err := strconv.ParseFloat(trimmed, 64)
	return err == nil && ratio > 0 && !math.IsInf(ratio, 0) && !math.IsNaN(ratio)
}

func newTranscodeUID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return "2.25." + new(big.Int).SetBytes(raw[:]).String(), nil
}

func elementStrings(dataset *object.Object, tag core.Tag) []string {
	element, ok := dataset.Get(tag)
	if !ok {
		return nil
	}
	return append([]string(nil), element.StringValues()...)
}

func cloneFrames(frames [][]byte) [][]byte {
	out := make([][]byte, len(frames))
	for i := range frames {
		out[i] = core.CloneBytes(frames[i])
	}
	return out
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

func sameOptionalUint16(a, b *uint16) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func transcodeLimitError(field string) error {
	return fmt.Errorf("%w: %s", ErrTranscodeResourceLimit, field)
}

// TranscodePath reads, transcodes, validates, and atomically publishes a Part
// 10 file. The source is never replaced and temporary files are private.
func TranscodePath(ctx context.Context, sourcePath, destinationPath string, target transfer.Syntax, options TranscodeOptions) (TranscodeReport, error) {
	if !transcodePathSupported() {
		return TranscodeReport{}, ErrTranscodeUnsupported
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return TranscodeReport{}, err
	}
	limits, err := normalizeTranscodeLimits(options.Limits)
	if err != nil {
		return TranscodeReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, limits.MaxDuration)
	defer cancel()
	sourceAbs, err := filepath.Abs(sourcePath)
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	sourceParent, err := filepath.EvalSymlinks(filepath.Dir(sourceAbs))
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	sourceAbs = filepath.Join(sourceParent, filepath.Base(sourceAbs))
	destinationAbs, err := filepath.Abs(destinationPath)
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "destination", Err: ErrTranscodeDestinationUnsafe}
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destinationAbs))
	if err != nil {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	destinationAbs = filepath.Join(parent, filepath.Base(destinationAbs))
	if sourceAbs == destinationAbs {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	sourceHandle, err := openTranscodeFileNoFollow(sourceAbs)
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	defer sourceHandle.Close()
	sourceInfo, err := sourceHandle.Stat()
	if err != nil || !sourceInfo.Mode().IsRegular() {
		return TranscodeReport{}, &TranscodeError{Stage: "source", Err: ErrTranscodeTransaction}
	}
	parentHandle, err := openTranscodeDirectoryNoFollow(parent)
	if err != nil {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	defer parentHandle.Close()
	parentInfo, err := parentHandle.Stat()
	if err != nil || !parentInfo.IsDir() {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	destinationName := filepath.Base(destinationAbs)
	previous, err := transcodeDestinationSnapshotAt(parentHandle, destinationName, sourceInfo)
	if err != nil {
		return TranscodeReport{}, err
	}
	if previous.exists && !transcodeCanReplaceExisting() {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	readOptions := object.ReadFileOptions{
		MaxElementBytes:  limits.MaxInputBytes,
		MaxTotalBytes:    limits.MaxInputBytes,
		MaxSequenceDepth: limits.MaxDepth,
		MaxElements:      limits.MaxElements,
		MaxFragments:     limits.MaxFragments,
	}
	file, err := object.ReadFileWithOptions(&transcodeContextFile{File: sourceHandle, ctx: ctx}, readOptions)
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return TranscodeReport{}, contextErr
		}
		return TranscodeReport{}, &TranscodeError{Stage: "read", Err: ErrTranscodeTransaction}
	}
	sourceSyntax, err := strictFileTransferSyntax(file)
	if err != nil {
		return TranscodeReport{}, err
	}
	targetSyntax, err := canonicalTargetTransferSyntax(target)
	if err != nil {
		return TranscodeReport{}, err
	}
	if err := validateTargetVerificationCodec(sourceSyntax, targetSyntax, options); err != nil {
		return TranscodeReport{}, err
	}
	output, report, err := TranscodeFile(ctx, file, target, options)
	if err != nil {
		return TranscodeReport{}, err
	}
	if !report.Lossy && !report.PixelDataPreserved {
		if err := verifyLosslessPixelData(ctx, file.Dataset, file.TransferSyntax, output.Dataset, output.TransferSyntax, options.DecoderRegistry, limits); err != nil {
			return TranscodeReport{}, err
		}
	} else if report.Lossy && !report.PixelDataPreserved {
		outputLimits := limits
		outputLimits.MaxInputBytes = limits.MaxOutputBytes
		if _, err := canonicalNativePixelBytes(ctx, output.Dataset, output.TransferSyntax, options.DecoderRegistry, outputLimits); err != nil {
			return TranscodeReport{}, err
		}
	}
	tempName, err := newTranscodeTempName()
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	temp, err := createTranscodeFileAt(parentHandle, tempName)
	if err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	tempOpen := true
	tempEntryOwned := true
	defer func() {
		var ownedInfo os.FileInfo
		if tempEntryOwned {
			ownedInfo, _ = temp.Stat()
		}
		if tempOpen {
			_ = temp.Close()
		}
		if tempEntryOwned && ownedInfo != nil && sameTranscodeEntryIdentityAt(parentHandle, tempName, ownedInfo) {
			_ = removeTranscodeFileAt(parentHandle, tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	writer := &transcodeBoundedWriter{ctx: ctx, writer: temp, remaining: limits.MaxOutputBytes}
	writeErr := object.WriteFile(writer, output)
	if writeErr == nil {
		writeErr = temp.Sync()
	}
	if writeErr == nil {
		_, writeErr = temp.Seek(0, 0)
	}
	if writeErr != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return TranscodeReport{}, contextErr
		}
		return TranscodeReport{}, &TranscodeError{Stage: "write", Err: ErrTranscodeTransaction}
	}
	readback, err := object.ReadFileWithOptions(&transcodeContextFile{File: temp, ctx: ctx}, object.ReadFileOptions{
		MaxElementBytes: limits.MaxOutputBytes, MaxTotalBytes: limits.MaxOutputBytes,
		MaxSequenceDepth: limits.MaxDepth, MaxElements: limits.MaxElements, MaxFragments: limits.MaxFragments,
	})
	if err != nil {
		if contextErr := ctx.Err(); contextErr != nil {
			return TranscodeReport{}, contextErr
		}
		return TranscodeReport{}, &TranscodeError{Stage: "readback", Err: ErrTranscodeTransaction}
	}
	_, validationErr := strictFileTransferSyntax(readback)
	if validationErr == nil && hasAnyPixelData(readback.Dataset) {
		validationErr = validateTranscodeReadback(readback.Dataset, readback.TransferSyntax, limits)
	} else if validationErr == nil && readback.TransferSyntax.Encapsulated {
		validationErr = ErrPixelDataNotFound
	}
	if validationErr != nil {
		return TranscodeReport{}, &TranscodeError{Stage: "readback", Err: ErrTranscodeTransaction}
	}
	if err := ctx.Err(); err != nil {
		return TranscodeReport{}, err
	}
	currentSource, err := openTranscodeFileNoFollow(sourceAbs)
	if err != nil {
		return TranscodeReport{}, ErrTranscodeSourceChanged
	}
	currentSourceInfo, statErr := currentSource.Stat()
	closeSourceErr := currentSource.Close()
	if statErr != nil || closeSourceErr != nil || !sameTranscodeFileInfo(sourceInfo, currentSourceInfo) {
		return TranscodeReport{}, ErrTranscodeSourceChanged
	}
	currentParent, err := openTranscodeDirectoryNoFollow(parent)
	if err != nil {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	currentParentInfo, statErr := currentParent.Stat()
	closeParentErr := currentParent.Close()
	if statErr != nil || closeParentErr != nil || !os.SameFile(parentInfo, currentParentInfo) {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	if !sameTranscodeDestinationSnapshotAt(parentHandle, destinationName, previous) {
		return TranscodeReport{}, ErrTranscodeDestinationUnsafe
	}
	tempInfo, err := temp.Stat()
	if err != nil || !sameTranscodeEntryAt(parentHandle, tempName, tempInfo) {
		return TranscodeReport{}, &TranscodeError{Stage: "temporary", Err: ErrTranscodeTransaction}
	}
	if err := ctx.Err(); err != nil {
		return TranscodeReport{}, err
	}
	if err := replaceTranscodedFileAt(parentHandle, temp, tempName, destinationName, previous); err != nil {
		if errors.Is(err, ErrTranscodeDestinationUnsafe) {
			return TranscodeReport{}, ErrTranscodeDestinationUnsafe
		}
		return TranscodeReport{}, &TranscodeError{Stage: "publish", Err: ErrTranscodeTransaction}
	}
	tempOpen = false
	tempEntryOwned = false
	if err := syncTranscodeDirectory(parentHandle); err != nil {
		return report, &TranscodeError{Stage: "durability", Err: ErrTranscodeTransaction}
	}
	return report, nil
}

func validateTargetVerificationCodec(source, target transfer.Syntax, options TranscodeOptions) error {
	if !target.Encapsulated || target.UID == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID || source.UID == target.UID && !options.ForceReencode {
		return nil
	}
	if options.EncoderRegistry == nil {
		return nil
	}
	_, ok := options.EncoderRegistry.GetEncoder(target.UID)
	if !ok {
		return nil
	}
	return CheckCodecAvailability(options.DecoderRegistry, target.UID)
}

func verifyLosslessPixelData(ctx context.Context, source *object.Object, sourceSyntax transfer.Syntax, output *object.Object, outputSyntax transfer.Syntax, registry Registry, limits TranscodeLimits) error {
	sourceElement, sourcePresent := source.Get(core.TagPixelData)
	outputElement, outputPresent := output.Get(core.TagPixelData)
	if sourcePresent != outputPresent {
		return fmt.Errorf("%w: Pixel Data presence changed", ErrIncompatiblePixelData)
	}
	if !sourcePresent {
		return nil
	}
	if sourceRaw, sourceOK := sourceElement.RawBytes(); sourceOK && !sourceSyntax.Encapsulated && sourceSyntax.IsLittleEndian() == outputSyntax.IsLittleEndian() {
		if outputRaw, outputOK := outputElement.RawBytes(); outputOK && !outputSyntax.Encapsulated {
			if !bytes.Equal(sourceRaw, outputRaw) {
				return fmt.Errorf("%w: lossless Pixel Data changed", ErrIncompatiblePixelData)
			}
			return nil
		}
	}
	sourceBytes, err := canonicalNativePixelBytes(ctx, source, sourceSyntax, registry, limits)
	if err != nil {
		return err
	}
	outputLimits := limits
	outputLimits.MaxInputBytes = limits.MaxOutputBytes
	outputBytes, err := canonicalNativePixelBytes(ctx, output, outputSyntax, registry, outputLimits)
	if err != nil {
		return err
	}
	if !bytes.Equal(sourceBytes, outputBytes) {
		return fmt.Errorf("%w: lossless Pixel Data changed", ErrIncompatiblePixelData)
	}
	return nil
}

func canonicalNativePixelBytes(ctx context.Context, dataset *object.Object, syntax transfer.Syntax, registry Registry, limits TranscodeLimits) ([]byte, error) {
	if syntax.Encapsulated && syntax.UID != transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID && registry == nil {
		return nil, codecAvailabilityError(ErrCodecRegistryNil, syntax.UID, nil)
	}
	native, _, err := DecompressDataSetContext(ctx, dataset, syntax, DecompressOptions{
		Registry: registry,
		Limits: DecompressLimits{
			MaxFrames: limits.MaxFrames, MaxPixels: int64(limits.MaxPixels), MaxInputBytes: limits.MaxInputBytes,
			MaxNativeBytes: limits.MaxOutputBytes, MaxExpansionRatio: limits.MaxExpansionRatio,
		},
	})
	if err != nil {
		return nil, err
	}
	element, ok := native.Get(core.TagPixelData)
	if !ok {
		return nil, ErrPixelDataNotFound
	}
	raw, ok := element.RawBytes()
	if !ok {
		return nil, ErrIncompatiblePixelData
	}
	return core.CloneBytes(raw), nil
}

func validateTranscodeReadback(dataset *object.Object, syntax transfer.Syntax, limits TranscodeLimits) error {
	_, hasInteger := dataset.Get(core.TagPixelData)
	_, hasFloat := dataset.Get(tagFloatPixelData)
	_, hasDouble := dataset.Get(tagDoubleFloatPixelData)
	if !hasInteger || hasFloat || hasDouble {
		return ErrTranscodeUnsupported
	}
	element, _ := dataset.Get(core.TagPixelData)
	pixel, err := pixelDataViewFromElement(element)
	if err != nil {
		return err
	}
	if pixel.Encapsulated != syntax.Encapsulated {
		return ErrIncompatiblePixelData
	}
	if len(pixel.Sequence.Fragments) > limits.MaxFragments || pixelPayloadBytes(pixel) > limits.MaxOutputBytes {
		return ErrTranscodeResourceLimit
	}
	if syntax.Encapsulated {
		_, err = ExtractMetadata(dataset)
	}
	return err
}

type transcodeFileSnapshot struct {
	exists bool
	info   os.FileInfo
}

func transcodeDestinationSnapshotAt(parent *os.File, name string, source os.FileInfo) (transcodeFileSnapshot, error) {
	file, err := openTranscodeFileAt(parent, name)
	if errors.Is(err, os.ErrNotExist) {
		return transcodeFileSnapshot{}, nil
	}
	if err != nil {
		return transcodeFileSnapshot{}, ErrTranscodeDestinationUnsafe
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	if statErr != nil || closeErr != nil || !info.Mode().IsRegular() || os.SameFile(source, info) {
		return transcodeFileSnapshot{}, ErrTranscodeDestinationUnsafe
	}
	return transcodeFileSnapshot{exists: true, info: info}, nil
}

func sameTranscodeDestinationSnapshotAt(parent *os.File, name string, previous transcodeFileSnapshot) bool {
	file, err := openTranscodeFileAt(parent, name)
	if !previous.exists {
		return errors.Is(err, os.ErrNotExist)
	}
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && info.Mode().IsRegular() && sameTranscodeFileInfo(previous.info, info)
}

func sameTranscodeEntryAt(parent *os.File, name string, expected os.FileInfo) bool {
	file, err := openTranscodeFileAt(parent, name)
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && sameTranscodeFileInfo(expected, info)
}

func sameTranscodeEntryIdentityAt(parent *os.File, name string, expected os.FileInfo) bool {
	file, err := openTranscodeFileAt(parent, name)
	if err != nil {
		return false
	}
	info, statErr := file.Stat()
	closeErr := file.Close()
	return statErr == nil && closeErr == nil && expected != nil && os.SameFile(expected, info)
}

func sameTranscodeFileInfo(expected, current os.FileInfo) bool {
	return expected != nil && current != nil && os.SameFile(expected, current) && expected.Size() == current.Size() && expected.ModTime().Equal(current.ModTime())
}

func newTranscodeTempName() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return ".dicom-transcode-" + hex.EncodeToString(value[:]), nil
}

type transcodeContextFile struct {
	*os.File
	ctx context.Context
}

func (f *transcodeContextFile) Read(data []byte) (int, error) {
	if err := f.ctx.Err(); err != nil {
		return 0, err
	}
	return f.File.Read(data)
}

type transcodeBoundedWriter struct {
	ctx       context.Context
	writer    *os.File
	remaining int64
}

func (w *transcodeBoundedWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(data)) > w.remaining {
		return 0, ErrTranscodeResourceLimit
	}
	n, err := w.writer.Write(data)
	w.remaining -= int64(n)
	return n, err
}
