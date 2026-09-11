package pixeldata

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

// ResolveTranscodeLimits validates limits and replaces zero fields with the
// transcoder's finite defaults. Adapters can use the same bounds for preparation
// and input parsing without duplicating transcode policy.
func ResolveTranscodeLimits(limits TranscodeLimits) (TranscodeLimits, error) {
	return normalizeTranscodeLimits(limits)
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
	plan := buildTranscodePlan(source, target, options.ForceReencode, hasAnyPixelData(dataset))
	validated, err := validateTranscodeInput(dataset, plan, limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if plan.mode == transcodeNativeClone {
		return cloneNativeTranscode(ctx, validated, limits)
	}
	metadata, pixel := validated.metadata, validated.pixel
	if plan.mode == transcodeEquivalentClone {
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

	switch plan.mode {
	case transcodeDecodeNative:
		if target.UID == transfer.ExplicitVRBigEndian.UID {
			elements := transcodeElementsToLittleEndian(decompressed.Elements())
			result := object.FromElements(elements, nil)
			result.SetValueByteOrder(binary.BigEndian)
			result, cloneErr := cloneDetachedObject(ctx, result, outputCloneLimits(limits))
			if cloneErr != nil {
				return nil, TranscodeReport{}, cloneErr
			}
			return result, report, nil
		}
		result, cloneErr := cloneDetachedObject(ctx, decompressed, outputCloneLimits(limits))
		if cloneErr != nil {
			return nil, TranscodeReport{}, cloneErr
		}
		result.SetValueByteOrder(binary.LittleEndian)
		return result, report, nil
	case transcodeDecodeEncapsulatedUncompressed:
		frames, frameErr := ExtractNativeFramesView(decompressed)
		if frameErr != nil {
			return nil, TranscodeReport{}, frameErr
		}
		fragments := cloneFrames(frames.Data)
		draft, encodedBytes, encodeErr := rewriteEncodedDataSet(ctx, encodedRewriteInput{
			source: decompressed, metadata: metadata, fragments: fragments, limits: limits,
		})
		if encodeErr != nil {
			return nil, TranscodeReport{}, encodeErr
		}
		result, encodeErr := detachDataSet(ctx, draft, limits)
		if encodeErr != nil {
			return nil, TranscodeReport{}, encodeErr
		}
		report.EncodedBytes = encodedBytes
		return result.object, report, nil
	case transcodeDecodeEncode:
		return transcodeWithEncoder(ctx, decompressed, metadata, target, options, limits, report)
	default:
		panic("pixeldata: invalid transcode plan")
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
