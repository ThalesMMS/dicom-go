package pixeldata

import (
	"context"

	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/transfer"
)

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
	txn, err := beginTranscodePathTxn(sourcePath, destinationPath)
	if err != nil {
		return TranscodeReport{}, err
	}
	defer txn.close()
	readOptions := object.ReadFileOptions{
		MaxElementBytes:  limits.MaxInputBytes,
		MaxTotalBytes:    limits.MaxInputBytes,
		MaxSequenceDepth: limits.MaxDepth,
		MaxElements:      limits.MaxElements,
		MaxFragments:     limits.MaxFragments,
	}
	file, err := object.ReadFileWithOptions(&transcodeContextFile{File: txn.source, ctx: ctx}, readOptions)
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
	staged, err := stageTranscodeFile(ctx, txn, output, limits)
	if err != nil {
		return TranscodeReport{}, err
	}
	defer staged.cleanup(txn.parent)
	if err := validateStagedTranscode(ctx, staged, limits); err != nil {
		return TranscodeReport{}, err
	}
	if err := ctx.Err(); err != nil {
		return TranscodeReport{}, err
	}
	if err := precommitTranscode(txn, staged); err != nil {
		return TranscodeReport{}, err
	}
	reportAvailable, err := publishStagedTranscode(ctx, txn, staged)
	if err != nil {
		if reportAvailable {
			return report, err
		}
		return TranscodeReport{}, err
	}
	return report, nil
}
