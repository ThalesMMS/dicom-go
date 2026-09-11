package pixeldata

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type resolvedFrameEncoder struct {
	encoder      FrameEncoder
	capabilities EncoderCapabilities
	target       transfer.Syntax
}

type frameTransformInput struct {
	frames         *NativeFrames
	metadata       Metadata
	encoder        resolvedFrameEncoder
	maxOutputBytes int64
}

type transformedFrames struct {
	fragments    [][]byte
	delta        encodedMetadataDelta
	encodedBytes int64
	lossless     bool
	lossyMethod  string
}

func resolveFrameEncoder(target transfer.Syntax, metadata Metadata, options TranscodeOptions) (resolvedFrameEncoder, error) {
	if target.MediaPayload || IsJPIPReferencedTransferSyntax(target.UID) || target.UID == transfer.DeflatedImageFrameCompression.UID {
		return resolvedFrameEncoder{}, fmt.Errorf("%w: target is not a still-image encoder syntax", ErrTranscodeUnsupported)
	}
	if options.EncoderRegistry == nil {
		return resolvedFrameEncoder{}, encoderAvailabilityError(ErrEncoderRegistryNil, target.UID, nil)
	}
	encoder, ok := options.EncoderRegistry.GetEncoder(target.UID)
	if !ok {
		return resolvedFrameEncoder{}, CheckEncoderAvailability(options.EncoderRegistry, target.UID)
	}
	capabilities, err := encoderCapabilities(encoder)
	if err != nil || transfer.NormalizeUID(capabilities.TransferSyntaxUID) != target.UID || !validEncoderCapabilities(capabilities) {
		return resolvedFrameEncoder{}, ErrEncoderCapabilitiesInvalid
	}
	if !capabilities.Lossless && !options.AllowLossy {
		return resolvedFrameEncoder{}, ErrTranscodeLossyDisallowed
	}
	if err := validateEncoderMetadata(capabilities, metadata); err != nil {
		return resolvedFrameEncoder{}, err
	}
	return resolvedFrameEncoder{encoder: encoder, capabilities: capabilities, target: target}, nil
}

func transcodeWithEncoder(ctx context.Context, dataset *object.Object, metadata Metadata, target transfer.Syntax, options TranscodeOptions, limits TranscodeLimits, report TranscodeReport) (*object.Object, TranscodeReport, error) {
	resolved, err := resolveFrameEncoder(target, metadata, options)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	frames, err := ExtractNativeFramesView(dataset)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	transformed, err := transformFrames(ctx, frameTransformInput{
		frames:         frames,
		metadata:       metadata,
		encoder:        resolved,
		maxOutputBytes: limits.MaxOutputBytes,
	})
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	draft, encodedBytes, err := rewriteEncodedDataSet(ctx, encodedRewriteInput{
		source: dataset, metadata: metadata, fragments: transformed.fragments, delta: &transformed.delta, limits: limits,
	})
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	result, err := detachDataSet(ctx, draft, limits)
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	report.EncodedBytes = encodedBytes
	report.Lossy = !transformed.lossless
	if report.Lossy {
		if err := finalizeLossyDataSet(result, metadata.TotalSize(), encodedBytes, transformed.lossyMethod); err != nil {
			return nil, TranscodeReport{}, err
		}
	}
	return result.object, report, nil
}

func transformFrames(ctx context.Context, input frameTransformInput) (transformedFrames, error) {
	fragments := make([][]byte, len(input.frames.Data))
	var encodedTotal int64
	var outputPI string
	var outputPlanar *uint16
	for i, frame := range input.frames.Data {
		if err := ctx.Err(); err != nil {
			return transformedFrames{}, err
		}
		encoded, err := callFrameEncoder(ctx, input.encoder.encoder, frame, input.metadata)
		if err != nil {
			return transformedFrames{}, &EncoderEncodeError{Err: err, TransferSyntaxUID: input.encoder.target.UID}
		}
		if len(encoded.Data) == 0 {
			return transformedFrames{}, ErrEncoderOutputInvalid
		}
		encoded, err = validateEncodedMetadataDelta(input.metadata, input.encoder.capabilities, encoded)
		if err != nil {
			return transformedFrames{}, err
		}
		if int64(len(encoded.Data)) > input.maxOutputBytes-encodedTotal {
			return transformedFrames{}, transcodeLimitError("output_bytes")
		}
		encodedTotal += int64(len(encoded.Data))
		if i == 0 {
			outputPI = encoded.PhotometricInterpretation
			if encoded.PlanarConfiguration != nil {
				value := *encoded.PlanarConfiguration
				outputPlanar = &value
			}
		} else if outputPI != encoded.PhotometricInterpretation || !sameOptionalUint16(outputPlanar, encoded.PlanarConfiguration) {
			return transformedFrames{}, fmt.Errorf("%w: inconsistent encoder metadata", ErrEncoderOutputInvalid)
		}
		fragments[i] = core.CloneBytes(encoded.Data)
	}
	return transformedFrames{
		fragments:    fragments,
		delta:        encodedMetadataDelta{photometric: outputPI, planar: outputPlanar},
		encodedBytes: encodedTotal,
		lossless:     input.encoder.capabilities.Lossless,
		lossyMethod:  input.encoder.capabilities.LossyMethod,
	}, nil
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

func sameOptionalUint16(a, b *uint16) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}
