package pixeldata

import (
	"context"
	"fmt"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

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
