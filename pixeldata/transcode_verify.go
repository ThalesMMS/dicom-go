package pixeldata

import (
	"bytes"
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

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
