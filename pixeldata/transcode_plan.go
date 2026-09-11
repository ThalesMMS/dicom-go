package pixeldata

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type transcodeMode uint8

const (
	transcodeNativeClone transcodeMode = iota
	transcodeEquivalentClone
	transcodeDecodeNative
	transcodeDecodeEncapsulatedUncompressed
	transcodeDecodeEncode
	transcodeMissingPixelData
)

type transcodePlan struct {
	source        transfer.Syntax
	target        transfer.Syntax
	mode          transcodeMode
	forceReencode bool
}

type validatedTranscodeInput struct {
	plan            transcodePlan
	dataset         *object.Object
	metadata        Metadata
	pixel           PixelData
	pixelBytes      int64
	metadataPresent bool
}

func buildTranscodePlan(source, target transfer.Syntax, forceReencode, hasPixel bool) transcodePlan {
	plan := transcodePlan{source: source, target: target, forceReencode: forceReencode}
	switch {
	case !source.Encapsulated && !target.Encapsulated:
		plan.mode = transcodeNativeClone
	case !hasPixel:
		plan.mode = transcodeMissingPixelData
	case source.UID == target.UID && !forceReencode:
		plan.mode = transcodeEquivalentClone
	case target.UID == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID:
		plan.mode = transcodeDecodeEncapsulatedUncompressed
	case target.UID == transfer.ImplicitVRLittleEndian.UID,
		target.UID == transfer.ExplicitVRLittleEndian.UID,
		target.UID == transfer.DeflatedExplicitVRLittleEndian.UID,
		target.UID == transfer.ExplicitVRBigEndian.UID:
		plan.mode = transcodeDecodeNative
	default:
		plan.mode = transcodeDecodeEncode
	}
	return plan
}

func validateTranscodeInput(dataset *object.Object, plan transcodePlan, limits TranscodeLimits) (validatedTranscodeInput, error) {
	validated := validatedTranscodeInput{plan: plan, dataset: dataset}
	if (plan.source.UID != plan.target.UID || plan.forceReencode) && (plan.source.Encapsulated || plan.target.Encapsulated) {
		nested, err := hasNestedPixelData(dataset, limits)
		if err != nil {
			return validatedTranscodeInput{}, err
		}
		if nested {
			return validatedTranscodeInput{}, fmt.Errorf("%w: nested Pixel Data transcode", ErrTranscodeUnsupported)
		}
	}

	if plan.mode == transcodeNativeClone {
		if plan.source.ByteOrder != plan.target.ByteOrder {
			if err := validateEndianConversion(dataset, limits); err != nil {
				return validatedTranscodeInput{}, err
			}
		}
		metadata, pixelBytes, metadataPresent, err := validateNativeTranscodeInput(dataset, plan.source, limits)
		if err != nil {
			return validatedTranscodeInput{}, err
		}
		validated.metadata = metadata
		validated.pixelBytes = pixelBytes
		validated.metadataPresent = metadataPresent
		return validated, nil
	}
	if plan.mode == transcodeMissingPixelData {
		return validatedTranscodeInput{}, ErrPixelDataNotFound
	}

	metadata, pixel, err := validateTranscodePixelData(dataset, plan.source, limits)
	if err != nil {
		return validatedTranscodeInput{}, err
	}
	validated.metadata = metadata
	validated.pixel = pixel
	validated.pixelBytes = pixelPayloadBytes(pixel)
	validated.metadataPresent = true
	if plan.mode != transcodeEquivalentClone && metadata.BitsAllocated < 8 {
		return validatedTranscodeInput{}, fmt.Errorf("%w: per-frame transcode with BitsAllocated below 8", ErrTranscodeUnsupported)
	}
	return validated, nil
}

func cloneNativeTranscode(ctx context.Context, input validatedTranscodeInput, limits TranscodeLimits) (*object.Object, TranscodeReport, error) {
	clone, err := cloneDetachedObject(ctx, input.dataset, equivalentCloneLimits(limits))
	if err != nil {
		return nil, TranscodeReport{}, err
	}
	if input.plan.source.ByteOrder != input.plan.target.ByteOrder && input.plan.source.ByteOrder != nil && input.plan.target.ByteOrder != nil {
		clone = object.FromElements(transcodeElementsToLittleEndian(clone.Elements()), nil)
	}
	clone.SetValueByteOrder(input.plan.target.ByteOrder)
	report := TranscodeReport{PixelDataPreserved: true, EncodedBytes: input.pixelBytes}
	if input.metadataPresent {
		report.Frames = input.metadata.NumberOfFrames
		report.NativeBytes = input.metadata.TotalSize()
	}
	return clone, report, nil
}
