// Package storetranscode adapts explicitly requested pixel transformations to
// dimse.StoreSource. It composes the existing transcoder and StoreSession without
// adding codec dependencies to the DIMSE transport package.
package storetranscode

import (
	"context"
	"errors"
	"io"
	"os"
	"slices"
	"sync"

	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/net/dimse"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrOptions          = errors.New("dicom: invalid store transcode options")
	ErrPreparation      = errors.New("dicom: store representation preparation failed")
	ErrNoRepresentation = errors.New("dicom: no requested store representation is producible")
	ErrResourceLimit    = errors.New("dicom: store transcode spool limit exceeded")
)

// Error redacts source, runtime and filesystem details while retaining causes
// for errors.Is. Report contains stable, value-free exclusion reasons.
type Error struct{ Cause error }

func (e *Error) Error() string { return "dicom: store transcode preparation failed" }
func (e *Error) Unwrap() error { return e.Cause }

// Options explicitly selects one target. A zero Target delegates unchanged to
// the original StoreSource, including its ordinary native syntax preferences.
// FallbackOriginal permits the original bytes if transformation is unavailable
// or the peer selects the original syntax. It is incompatible with AllowLossy:
// a newly lossy representation has a different SOP Instance identity.
type Options struct {
	Target           transfer.Syntax
	FallbackOriginal bool
	Transcode        pixeldata.TranscodeOptions
	// Dictionary optionally resolves implicit VR, including scoped private
	// definitions, while parsing the original representation for preparation.
	Dictionary     dictionary.DataDictionary
	SpoolDirectory string
	// MaxSpoolBytes bounds aggregate input and output temporary dataset bytes
	// per preparation/open handle. Zero selects 2 GiB; negative is invalid.
	MaxSpoolBytes int64
}

type Candidate struct {
	TransferSyntaxUID string
	Operation         string
	Producible        bool
	Reason            string
	RuntimeMissing    bool
	Lossy             bool
}

// Report contains no instance UIDs, origins, paths or backend error text.
// SelectedTransferSyntaxUID records writer selection, not confirmed delivery.
// StoreSession's item outcome remains the authority on delivery and retry.
type Report struct {
	OriginalTransferSyntaxUID string
	Candidates                []Candidate
	SelectedTransferSyntaxUID string
	Negotiation               string
}

type Source struct {
	base   dimse.StoreSource
	opts   Options
	gate   chan struct{}
	plan   *preparationPlan
	mu     sync.Mutex
	report Report
}

var _ dimse.StoreSource = (*Source)(nil)

func NewSource(base dimse.StoreSource, opts Options) (*Source, error) {
	if base == nil || opts.MaxSpoolBytes < 0 || opts.FallbackOriginal && opts.Transcode.AllowLossy {
		return nil, ErrOptions
	}
	if opts.Target.UID != "" {
		syntax, ok := transfer.DefaultRegistry.Get(transfer.NormalizeUID(opts.Target.UID))
		if !ok {
			return nil, ErrOptions
		}
		opts.Target = syntax
	} else if opts.FallbackOriginal || opts.Transcode.AllowLossy || opts.Transcode.ForceReencode {
		return nil, ErrOptions
	}
	if opts.MaxSpoolBytes == 0 {
		opts.MaxSpoolBytes = 2 << 30
	}
	limits, err := pixeldata.ResolveTranscodeLimits(opts.Transcode.Limits)
	if err != nil {
		return nil, err
	}
	opts.Transcode.Limits = limits
	return &Source{base: base, opts: opts, gate: make(chan struct{}, 1)}, nil
}

func (s *Source) Report() Report {
	if s == nil {
		return Report{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.report
	r.Candidates = append([]Candidate(nil), r.Candidates...)
	return r
}

// RecordResult attaches the existing StoreSession negotiation/outcome to the
// report. This is optional observation; it never retries or changes a source.
func (s *Source) RecordResult(result dimse.StoreItemResult) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.report.SelectedTransferSyntaxUID = result.NegotiatedTransferSyntaxUID
	s.report.Negotiation = string(result.Outcome)
}

func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

// Inspect actually prepares and verifies the requested representation before it
// is advertised. All temporary files are removed before return; only detached
// metadata, the source digest and (for lossy output) its new identity are kept.
func (s *Source) Inspect(ctx context.Context) (dimse.StoreDescriptor, error) {
	if s == nil {
		return dimse.StoreDescriptor{}, ErrOptions
	}
	ctx = contextOrBackground(ctx)
	if s.opts.Target.UID == "" {
		return s.base.Inspect(ctx)
	}
	select {
	case s.gate <- struct{}{}:
		defer func() { <-s.gate }()
	case <-ctx.Done():
		return dimse.StoreDescriptor{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return dimse.StoreDescriptor{}, err
	}
	if s.plan != nil {
		return cloneDescriptor(s.plan.output), nil
	}
	p, report, err := s.prepare(ctx, nil)
	if err != nil {
		report = s.failedReport(report, err)
	}
	s.mu.Lock()
	s.report = report
	s.mu.Unlock()
	if err != nil {
		return dimse.StoreDescriptor{}, &Error{Cause: err}
	}
	if err := p.close(); err != nil {
		return dimse.StoreDescriptor{}, &Error{Cause: err}
	}
	s.plan = &p.plan
	return cloneDescriptor(p.plan.output), nil
}

// Open reopens and hashes the original source and prepares the representation
// again before a C-STORE command can be written. Runtime/profile changes fail
// here instead of creating a false capability or a lossy fallback during send.
// Each returned handle owns its temporary files; Close removes them once.
func (s *Source) Open(ctx context.Context) (dimse.OpenedStoreSource, error) {
	if s == nil {
		return dimse.OpenedStoreSource{}, ErrOptions
	}
	ctx = contextOrBackground(ctx)
	if s.opts.Target.UID == "" {
		return s.base.Open(ctx)
	}
	if _, err := s.Inspect(ctx); err != nil {
		return dimse.OpenedStoreSource{}, err
	}
	// Inspect publishes the immutable plan through the gate. Read it under the
	// same synchronization so independently opened handles may run concurrently.
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return dimse.OpenedStoreSource{}, ctx.Err()
	}
	plan := s.plan
	<-s.gate
	p, report, err := s.prepare(ctx, plan)
	if err != nil {
		report = s.failedReport(report, err)
		s.mu.Lock()
		s.report = report
		s.mu.Unlock()
		return dimse.OpenedStoreSource{}, &Error{Cause: err}
	}
	var mu sync.Mutex
	closed := false
	var closeErr error
	return dimse.OpenedStoreSource{
		Descriptor: cloneDescriptor(p.plan.output),
		WriteDataSet: func(writeCtx context.Context, dst io.Writer, syntax transfer.Syntax) error {
			mu.Lock()
			defer mu.Unlock()
			if closed {
				return dimse.ErrStoreInvalidSource
			}
			uid := transfer.NormalizeUID(syntax.UID)
			path, ok := p.paths[uid]
			if !ok {
				return dimse.ErrStoreTransferSyntax
			}
			f, err := os.Open(path)
			if err != nil {
				return &Error{Cause: err}
			}
			s.mu.Lock()
			s.report.SelectedTransferSyntaxUID = uid
			s.report.Negotiation = "selected"
			s.mu.Unlock()
			_, copyErr := copyContext(contextOrBackground(writeCtx), dst, f)
			if err := errors.Join(copyErr, f.Close()); err != nil {
				return &Error{Cause: err}
			}
			return nil
		},
		Close: func() error {
			mu.Lock()
			defer mu.Unlock()
			if !closed {
				closed = true
				closeErr = p.close()
			}
			if closeErr != nil {
				return &Error{Cause: closeErr}
			}
			return nil
		},
	}, nil
}

func (s *Source) failedReport(report Report, err error) Report {
	reason, runtimeMissing := exclusionReason(err)
	if len(report.Candidates) == 0 {
		report.Candidates = []Candidate{{TransferSyntaxUID: s.opts.Target.UID, Operation: "preparation"}}
	}
	for i := range report.Candidates {
		report.Candidates[i].Producible = false
		if report.Candidates[i].Reason == "" {
			report.Candidates[i].Reason = reason
			report.Candidates[i].RuntimeMissing = runtimeMissing
		}
	}
	return report
}

func cloneDescriptor(d dimse.StoreDescriptor) dimse.StoreDescriptor {
	d.WritableTransferSyntaxUIDs = append([]string(nil), d.WritableTransferSyntaxUIDs...)
	return d
}

func sameDescriptor(a, b dimse.StoreDescriptor) bool {
	return a.SOPClassUID == b.SOPClassUID && a.SOPInstanceUID == b.SOPInstanceUID &&
		a.TransferSyntaxUID == b.TransferSyntaxUID && a.Size == b.Size &&
		a.PixelDataHeaderSet == b.PixelDataHeaderSet && (!a.PixelDataHeaderSet || a.PixelDataHeader == b.PixelDataHeader) &&
		slices.Equal(a.WritableTransferSyntaxUIDs, b.WritableTransferSyntaxUIDs)
}
