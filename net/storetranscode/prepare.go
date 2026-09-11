package storetranscode

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type preparationPlan struct {
	input, output dimse.StoreDescriptor
	digest        [32]byte
	derivedUID    string
}
type prepared struct {
	plan  preparationPlan
	dir   string
	paths map[string]string
}

func (p *prepared) close() error { return os.RemoveAll(p.dir) }

// Preparation owns one private temporary directory, never a source path. It
// retains no descriptors across Inspect calls and publishes no network bytes.
func (s *Source) prepare(parent context.Context, expected *preparationPlan) (result *prepared, report Report, err error) {
	limits := s.opts.Transcode.Limits
	ctx, cancel := context.WithTimeout(parent, limits.MaxDuration)
	defer cancel()
	input, err := s.base.Inspect(ctx)
	if err != nil {
		return nil, report, err
	}
	original, ok := transfer.DefaultRegistry.Get(transfer.NormalizeUID(input.TransferSyntaxUID))
	if !ok || !core.IsValidUID(input.SOPClassUID) || !core.IsValidUID(input.SOPInstanceUID) {
		return nil, report, dimse.ErrStoreInvalidSource
	}
	report.OriginalTransferSyntaxUID = original.UID
	report.Negotiation = "not-negotiated"
	if expected != nil && !sameDescriptor(expected.input, input) {
		return nil, report, dimse.ErrStoreSourceChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, report, err
	}
	dir, err := os.MkdirTemp(s.opts.SpoolDirectory, "dicom-store-transcode-")
	if err != nil {
		return nil, report, err
	}
	p := &prepared{dir: dir, paths: map[string]string{}, plan: preparationPlan{input: cloneDescriptor(input)}}
	defer func() {
		if result == nil || err != nil {
			err = errors.Join(err, p.close())
			result = nil
		}
	}()
	budget := spoolBudget{remaining: s.opts.MaxSpoolBytes}
	inputPath := filepath.Join(dir, "source.dataset")
	inputFile, err := os.OpenFile(inputPath, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
	if err != nil {
		return nil, report, err
	}
	defer func() { err = errors.Join(err, inputFile.Close()) }()
	opened, openErr := s.base.Open(ctx)
	if openErr != nil || opened.WriteDataSet == nil || opened.Close == nil {
		if opened.Close != nil {
			openErr = errors.Join(openErr, opened.Close())
		}
		return nil, report, errors.Join(dimse.ErrStoreInvalidSource, openErr)
	}
	copyErr := func() (err error) {
		defer func() { err = errors.Join(err, opened.Close()) }()
		if !sameDescriptor(input, opened.Descriptor) {
			return dimse.ErrStoreSourceChanged
		}
		w := &spoolWriter{ctx: ctx, w: inputFile, budget: &budget, remaining: limits.MaxInputBytes}
		return errors.Join(opened.WriteDataSet(ctx, w, original), w.err)
	}()
	if copyErr != nil {
		return nil, report, copyErr
	}
	inputSize, err := inputFile.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, report, err
	}
	if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
		return nil, report, err
	}
	hash := sha256.New()
	if _, err := copyContext(ctx, hash, inputFile); err != nil {
		return nil, report, err
	}
	copy(p.plan.digest[:], hash.Sum(nil))
	if expected != nil && expected.digest != p.plan.digest {
		return nil, report, dimse.ErrStoreSourceChanged
	}
	if _, err := inputFile.Seek(0, io.SeekStart); err != nil {
		return nil, report, err
	}
	readOpts := object.ReadFileOptions{
		Dictionary:    s.opts.Dictionary,
		MaxTotalBytes: limits.MaxInputBytes, MaxElementBytes: limits.MaxInputBytes,
		MaxPixelDataBytes: limits.MaxInputBytes, MaxElements: limits.MaxElements,
		MaxFragments: limits.MaxFragments, MaxSequenceDepth: limits.MaxDepth,
		// The existing transcoder accepts materialized pixels. Opaque pass-
		// through can keep them deferred; both paths retain the input bounds.
		DeferPixelData: !original.Deflated && s.opts.Target.UID == original.UID && !s.opts.Transcode.ForceReencode,
	}
	dataset, err := object.ReadDataSetWithOptions(&contextReadSeeker{ctx: ctx, ReadSeeker: inputFile}, original, readOpts)
	if err != nil {
		return nil, report, errors.Join(ErrPreparation, err)
	}
	defer func() { err = errors.Join(err, dataset.Close()) }()
	actual, err := dimse.NewFileStoreSource(&object.File{Dataset: dataset, TransferSyntax: original}, inputSize).Inspect(ctx)
	if err != nil || actual.SOPClassUID != input.SOPClassUID || actual.SOPInstanceUID != input.SOPInstanceUID {
		return nil, report, errors.Join(dimse.ErrStoreSourceChanged, err)
	}
	actual.Origin = input.Origin
	p.paths[original.UID] = inputPath
	target := s.opts.Target
	candidate := Candidate{TransferSyntaxUID: target.UID}
	var transformed *object.Object
	var transformReport pixeldata.TranscodeReport
	if target.UID == original.UID && !s.opts.Transcode.ForceReencode {
		candidate.Operation = "pass-through"
		candidate.Producible = true
		p.plan.output = actual
	} else {
		candidate.Operation = "native-reencode"
		switch {
		case original.RequiresCodec() && target.RequiresCodec():
			candidate.Operation = "decode-encode"
		case original.RequiresCodec():
			candidate.Operation = "decode"
		case target.RequiresCodec():
			candidate.Operation = "encode"
		}
		transformed, transformReport, err = pixeldata.TranscodeDataSet(ctx, dataset, original, target, s.opts.Transcode)
		if err == nil {
			defer func() { err = errors.Join(err, transformed.Close()) }()
			candidate.Lossy = transformReport.Lossy
			if transformReport.Lossy {
				if !s.opts.Transcode.AllowLossy || s.opts.FallbackOriginal {
					return nil, report, pixeldata.ErrTranscodeLossyDisallowed
				}
				uid, ok := transformed.GetUID(tags.SOPInstanceUID)
				if !ok || !core.IsValidUID(uid) || uid == input.SOPInstanceUID {
					return nil, report, dimse.ErrStoreInvalidSource
				}
				// The transcoder creates a new identity. Keep the first verified
				// identity stable across preflight, open and explicit retries.
				if expected != nil {
					if expected.derivedUID == "" {
						return nil, report, dimse.ErrStoreSourceChanged
					}
					uid = expected.derivedUID
					transformed.Put(core.Element{Header: core.ElementHeader{Tag: tags.SOPInstanceUID, VR: core.VRUI}, Value: core.StringValue{uid}})
				}
				p.plan.derivedUID = uid
			}
			outputPath := filepath.Join(dir, "target.dataset")
			var outputSize int64
			outputSize, err = writeRepresentation(ctx, outputPath, transformed, target, &budget, limits.MaxOutputBytes)
			if err == nil {
				p.plan.output, err = dimse.NewFileStoreSource(&object.File{Dataset: transformed, TransferSyntax: target}, outputSize).Inspect(ctx)
			}
			if err == nil {
				p.paths[target.UID] = outputPath
				candidate.Producible = true
			}
		}
		if err != nil {
			candidate.Reason, candidate.RuntimeMissing = exclusionReason(err)
			report.Candidates = append(report.Candidates, candidate)
			if ctx.Err() != nil {
				return nil, report, ctx.Err()
			}
			// Limits are operation failures, not reasons to continue allocating
			// or silently alter the policy after an incomplete preparation.
			if errors.Is(err, ErrResourceLimit) || errors.Is(err, pixeldata.ErrTranscodeResourceLimit) {
				return nil, report, err
			}
			if !s.opts.FallbackOriginal {
				return nil, report, errors.Join(ErrNoRepresentation, err)
			}
			p.plan.output = actual
			err = nil
		}
	}
	if candidate.Producible {
		report.Candidates = append(report.Candidates, candidate)
		p.plan.output.WritableTransferSyntaxUIDs = []string{target.UID}
	} else {
		p.plan.output.WritableTransferSyntaxUIDs = nil
	}
	if s.opts.FallbackOriginal && (target.UID != original.UID || !candidate.Producible) {
		report.Candidates = append(report.Candidates, Candidate{TransferSyntaxUID: original.UID, Operation: "pass-through", Producible: true})
		p.plan.output.WritableTransferSyntaxUIDs = append(p.plan.output.WritableTransferSyntaxUIDs, original.UID)
		if inputSize > p.plan.output.Size {
			p.plan.output.Size = inputSize
		}
	} else if target.UID != original.UID {
		delete(p.paths, original.UID)
	}
	p.plan.output.Origin = input.Origin
	if p.plan.output.SOPClassUID != input.SOPClassUID || (!candidate.Lossy && p.plan.output.SOPInstanceUID != input.SOPInstanceUID) {
		return nil, report, dimse.ErrStoreSourceChanged
	}
	if expected != nil && (!sameDescriptor(expected.output, p.plan.output) || expected.derivedUID != p.plan.derivedUID) {
		return nil, report, dimse.ErrStoreSourceChanged
	}
	if err := ctx.Err(); err != nil {
		return nil, report, err
	}
	return p, report, nil
}

func exclusionReason(err error) (string, bool) {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled", false
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline", false
	case errors.Is(err, dimse.ErrStoreSourceChanged):
		return "source-changed", false
	case errors.Is(err, dimse.ErrStoreInvalidSource):
		return "identity-or-source-invalid", false
	case errors.Is(err, pixeldata.ErrEncoderRegistryNil), errors.Is(err, pixeldata.ErrEncoderNotFound):
		return "encoder-unavailable", true
	case errors.Is(err, pixeldata.ErrCodecRegistryNil), errors.Is(err, pixeldata.ErrCodecNotFound):
		return "decoder-unavailable", true
	case errors.Is(err, os.ErrNotExist):
		return "runtime-unavailable", true
	case errors.Is(err, pixeldata.ErrTranscodeLossyDisallowed):
		return "lossy-disallowed", false
	case errors.Is(err, ErrResourceLimit), errors.Is(err, pixeldata.ErrTranscodeResourceLimit):
		return "resource-limit", false
	case errors.Is(err, pixeldata.ErrTranscodeUnsupported), errors.Is(err, pixeldata.ErrUnsupportedEncoderMetadata):
		return "profile-unsupported", false
	default:
		return "preparation-failed", false
	}
}

type spoolBudget struct{ remaining int64 }
type spoolWriter struct {
	ctx       context.Context
	w         io.Writer
	budget    *spoolBudget
	remaining int64
	err       error
}

func (w *spoolWriter) Write(p []byte) (n int, err error) {
	if w.err != nil {
		return 0, w.err
	}
	defer func() {
		if err != nil {
			w.err = err
		}
	}()
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining || int64(len(p)) > w.budget.remaining {
		return 0, ErrResourceLimit
	}
	n, err = w.w.Write(p)
	w.remaining -= int64(n)
	w.budget.remaining -= int64(n)
	if n != len(p) && err == nil {
		err = io.ErrShortWrite
	}
	return n, err
}

type contextReadSeeker struct {
	ctx context.Context
	io.ReadSeeker
}

func (r *contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Read(p)
}
func (r *contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.ReadSeeker.Seek(offset, whence)
}
func writeRepresentation(ctx context.Context, path string, dataset *object.Object, syntax transfer.Syntax, budget *spoolBudget, limit int64) (size int64, err error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	w := &spoolWriter{ctx: ctx, w: f, budget: budget, remaining: limit}
	err = object.WriteDataSet(w, dataset, syntax)
	return limit - w.remaining, err
}
func copyContext(ctx context.Context, dst io.Writer, src io.Reader) (int64, error) {
	buffer := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if err := ctx.Err(); err != nil {
				return total, err
			}
			written, err := dst.Write(buffer[:n])
			total += int64(written)
			if err != nil {
				return total, err
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if errors.Is(readErr, io.EOF) {
			return total, nil
		}
		if readErr != nil {
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}
