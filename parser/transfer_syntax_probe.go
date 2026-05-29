package parser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrTransferSyntaxProbeAmbiguous  = errors.New("dicom: transfer syntax probe is ambiguous")
	ErrTransferSyntaxProbeImpossible = errors.New("dicom: transfer syntax probe found no viable candidate")
	ErrTransferSyntaxProbePolicy     = errors.New("dicom: invalid transfer syntax probe policy")
)

// TransferSyntaxProbeOutcome describes whether a bounded probe selected a
// syntax, found competing candidates, or found no plausible candidate.
type TransferSyntaxProbeOutcome string

const (
	TransferSyntaxProbeSelected   TransferSyntaxProbeOutcome = "selected"
	TransferSyntaxProbeAmbiguous  TransferSyntaxProbeOutcome = "ambiguous"
	TransferSyntaxProbeImpossible TransferSyntaxProbeOutcome = "impossible"
)

// TransferSyntaxProbeReason is a value-free diagnostic code. It intentionally
// does not contain raw input, decoded values, or wrapped parser error strings.
type TransferSyntaxProbeReason string

const (
	TransferSyntaxProbeReasonComplete          TransferSyntaxProbeReason = "complete"
	TransferSyntaxProbeReasonPrefixCoherent    TransferSyntaxProbeReason = "prefix-coherent"
	TransferSyntaxProbeReasonParseError        TransferSyntaxProbeReason = "parse-error"
	TransferSyntaxProbeReasonNoEvidence        TransferSyntaxProbeReason = "no-evidence"
	TransferSyntaxProbeReasonCandidateBudget   TransferSyntaxProbeReason = "candidate-byte-budget"
	TransferSyntaxProbeReasonSafetyBudget      TransferSyntaxProbeReason = "candidate-safety-budget"
	TransferSyntaxProbeReasonPixelDataBoundary TransferSyntaxProbeReason = "pixel-data-boundary"
	TransferSyntaxProbeReasonCandidateTimeout  TransferSyntaxProbeReason = "candidate-time-budget"
	TransferSyntaxProbeReasonSharedTimeout     TransferSyntaxProbeReason = "shared-time-budget"
)

// TransferSyntaxProbeWarning is a value-free warning about bounded evidence.
type TransferSyntaxProbeWarning string

const (
	TransferSyntaxProbeWarningInputTruncated TransferSyntaxProbeWarning = "input-truncated-to-shared-byte-budget"
	TransferSyntaxProbeWarningTimeLimited    TransferSyntaxProbeWarning = "shared-time-budget-exhausted"
)

// TransferSyntaxProbeFailureClass lets policy layers distinguish malformed
// encoding from truncation, cooperative timeouts, and probe safety limits.
// Values are fixed codes and never contain parser error text or input values.
type TransferSyntaxProbeFailureClass string

const (
	TransferSyntaxProbeFailureNone           TransferSyntaxProbeFailureClass = ""
	TransferSyntaxProbeFailureMalformed      TransferSyntaxProbeFailureClass = "malformed"
	TransferSyntaxProbeFailureTruncated      TransferSyntaxProbeFailureClass = "truncated"
	TransferSyntaxProbeFailureEvidenceBudget TransferSyntaxProbeFailureClass = "evidence-budget"
	TransferSyntaxProbeFailureSafetyBudget   TransferSyntaxProbeFailureClass = "safety-budget"
	TransferSyntaxProbeFailureTimeout        TransferSyntaxProbeFailureClass = "timeout"
)

// TransferSyntaxProbeStrictnessViolation identifies a conformance check that
// the ordinary parser can explicitly relax. It lets recovery policy preserve a
// caller-authorized declared syntax instead of treating strict probe evidence
// as proof of a transfer-syntax mismatch.
type TransferSyntaxProbeStrictnessViolation string

const (
	TransferSyntaxProbeStrictnessNone             TransferSyntaxProbeStrictnessViolation = ""
	TransferSyntaxProbeStrictnessReservedBytes    TransferSyntaxProbeStrictnessViolation = "nonzero-reserved-bytes"
	TransferSyntaxProbeStrictnessOddElementLength TransferSyntaxProbeStrictnessViolation = "odd-element-length"
)

const (
	// DefaultTransferSyntaxProbeBytes is the shared prefix limit used when
	// TransferSyntaxProbeOptions.MaxProbeBytes is zero.
	DefaultTransferSyntaxProbeBytes             = 1 << 20
	defaultTransferSyntaxProbeElements          = 256
	defaultTransferSyntaxProbeDepth             = 16
	defaultTransferSyntaxProbeFragments         = 64
	defaultTransferSyntaxProbeCandidates        = 8
	defaultTransferSyntaxProbeMinimumElements   = 2
	defaultTransferSyntaxProbeTokens            = 1024
	defaultTransferSyntaxProbeDuration          = time.Second
	defaultTransferSyntaxCandidateProbeDuration = 250 * time.Millisecond
	defaultTransferSyntaxProbeConfidence        = 0.75
	defaultTransferSyntaxProbeGap               = 0.10
)

// TransferSyntaxProbeOptions bounds and configures inference over an in-memory
// data-set prefix. Zero-valued limits use conservative defaults. Candidates
// default to the three native DICOM transfer syntaxes; compressed, deflated,
// media-payload, and unsupported candidates are rejected because their payload
// bytes are not safe evidence for syntax inference.
type TransferSyntaxProbeOptions struct {
	Candidates []transfer.Syntax
	Dictionary dictionary.DataDictionary

	MinimumConfidence    float64
	MinimumConfidenceGap float64

	// MaxProbeBytes bounds the one shared input prefix considered by all
	// candidates. MaxCandidateBytes independently bounds each parser pass.
	MaxProbeBytes     int64
	MaxCandidateBytes int64
	MaxElements       int
	MaxSequenceDepth  int
	MaxFragments      int
	MaxCandidates     int
	MinimumElements   int
	MaxTokens         int

	// MaxDuration is shared by the complete probe. MaxCandidateDuration is an
	// independent cooperative deadline checked between parser tokens. The input
	// is already in memory, so no background goroutine is needed or abandoned.
	MaxDuration          time.Duration
	MaxCandidateDuration time.Duration
}

// TransferSyntaxCandidateReport contains structural evidence only. It never
// contains element values or arbitrary strings derived from the input.
type TransferSyntaxCandidateReport struct {
	Syntax                 transfer.Syntax
	Score                  int
	Confidence             float64
	Reason                 TransferSyntaxProbeReason
	FailureClass           TransferSyntaxProbeFailureClass
	StrictnessViolation    TransferSyntaxProbeStrictnessViolation
	BytesConsumed          int64
	Tokens                 int
	Elements               int
	KnownElements          int
	PrivateElements        int
	OrderedElements        int
	PlausibleLengths       int
	DictionaryVRMatches    int
	DictionaryVRMismatches int
	DictionaryVMMatches    int
	Complete               bool
}

// TransferSyntaxProbeReport records the selected syntax and every bounded
// candidate evaluation. Candidates are sorted from strongest to weakest.
type TransferSyntaxProbeReport struct {
	Outcome    TransferSyntaxProbeOutcome
	Selected   transfer.Syntax
	Confidence float64
	Candidates []TransferSyntaxCandidateReport
	Warnings   []TransferSyntaxProbeWarning
}

// SelectedCandidate returns the diagnostic associated with Selected.
func (r TransferSyntaxProbeReport) SelectedCandidate() (TransferSyntaxCandidateReport, bool) {
	for _, candidate := range r.Candidates {
		if candidate.Syntax.UID == r.Selected.UID && r.Selected.UID != "" {
			return candidate, true
		}
	}
	return TransferSyntaxCandidateReport{}, false
}

// Clone returns a report whose slices do not alias the receiver.
func (r TransferSyntaxProbeReport) Clone() TransferSyntaxProbeReport {
	r.Candidates = append([]TransferSyntaxCandidateReport(nil), r.Candidates...)
	r.Warnings = append([]TransferSyntaxProbeWarning(nil), r.Warnings...)
	return r
}

// String formats only bounded structural metadata and fixed diagnostic codes.
func (r TransferSyntaxProbeReport) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "outcome=%s confidence=%.3f", r.Outcome, r.Confidence)
	if r.Selected.UID != "" {
		fmt.Fprintf(&b, " selected=%s", r.Selected.UID)
	}
	for _, candidate := range r.Candidates {
		fmt.Fprintf(&b, " candidate=%s score=%d confidence=%.3f reason=%s failure=%s strictness=%s bytes=%d tokens=%d elements=%d",
			candidate.Syntax.UID, candidate.Score, candidate.Confidence, candidate.Reason,
			candidate.FailureClass, candidate.StrictnessViolation,
			candidate.BytesConsumed, candidate.Tokens, candidate.Elements)
	}
	for _, warning := range r.Warnings {
		fmt.Fprintf(&b, " warning=%s", warning)
	}
	return b.String()
}

type transferSyntaxProbeError struct {
	kind error
}

func (e *transferSyntaxProbeError) Error() string { return e.kind.Error() }
func (e *transferSyntaxProbeError) Unwrap() error { return e.kind }

// ProbeTransferSyntax evaluates a bounded data-set prefix with the production
// parser. It never chooses by candidate order: both an absolute confidence and
// a confidence gap over the runner-up are required.
func ProbeTransferSyntax(data []byte, options TransferSyntaxProbeOptions) (TransferSyntaxProbeReport, error) {
	opts, err := normalizeTransferSyntaxProbeOptions(options)
	if err != nil {
		return TransferSyntaxProbeReport{Outcome: TransferSyntaxProbeImpossible}, &transferSyntaxProbeError{kind: err}
	}

	sharedLimit := minInt64(int64(len(data)), opts.MaxProbeBytes)
	shared := data[:sharedLimit]
	sharedTruncated := int64(len(data)) > sharedLimit
	report := TransferSyntaxProbeReport{}
	if sharedTruncated {
		report.Warnings = append(report.Warnings, TransferSyntaxProbeWarningInputTruncated)
	}
	sharedDeadline := time.Now().Add(opts.MaxDuration)
	for _, syntax := range opts.Candidates {
		candidateTruncated := sharedTruncated
		candidateLimit := minInt64(int64(len(shared)), opts.MaxCandidateBytes)
		if int64(len(shared)) > candidateLimit {
			candidateTruncated = true
		}
		if !time.Now().Before(sharedDeadline) {
			report.Warnings = appendWarningOnce(report.Warnings, TransferSyntaxProbeWarningTimeLimited)
			report.Candidates = append(report.Candidates, TransferSyntaxCandidateReport{
				Syntax:       syntax,
				Reason:       TransferSyntaxProbeReasonSharedTimeout,
				FailureClass: TransferSyntaxProbeFailureTimeout,
			})
			continue
		}
		candidate := probeTransferSyntaxCandidate(
			shared[:candidateLimit],
			syntax,
			opts,
			candidateTruncated,
			sharedDeadline,
		)
		report.Candidates = append(report.Candidates, candidate)
	}

	sort.SliceStable(report.Candidates, func(i, j int) bool {
		left, right := report.Candidates[i], report.Candidates[j]
		if left.Score != right.Score {
			return left.Score > right.Score
		}
		return left.Syntax.UID < right.Syntax.UID
	})
	if probeTimedOut(report.Candidates) {
		report.Outcome = TransferSyntaxProbeAmbiguous
		if len(report.Candidates) > 0 {
			report.Confidence = report.Candidates[0].Confidence
		}
		return report, &transferSyntaxProbeError{kind: ErrTransferSyntaxProbeAmbiguous}
	}
	if len(report.Candidates) == 0 || report.Candidates[0].Elements == 0 || report.Candidates[0].Confidence == 0 {
		report.Outcome = TransferSyntaxProbeImpossible
		return report, &transferSyntaxProbeError{kind: ErrTransferSyntaxProbeImpossible}
	}

	winner := report.Candidates[0]
	secondConfidence := 0.0
	if len(report.Candidates) > 1 {
		secondConfidence = report.Candidates[1].Confidence
	}
	if winner.Elements < opts.MinimumElements || winner.Confidence < opts.MinimumConfidence || winner.Confidence-secondConfidence < opts.MinimumConfidenceGap {
		report.Outcome = TransferSyntaxProbeAmbiguous
		report.Confidence = winner.Confidence
		return report, &transferSyntaxProbeError{kind: ErrTransferSyntaxProbeAmbiguous}
	}

	report.Outcome = TransferSyntaxProbeSelected
	report.Selected = winner.Syntax
	report.Confidence = winner.Confidence
	return report, nil
}

type normalizedTransferSyntaxProbeOptions struct {
	TransferSyntaxProbeOptions
}

func normalizeTransferSyntaxProbeOptions(options TransferSyntaxProbeOptions) (normalizedTransferSyntaxProbeOptions, error) {
	if options.MinimumConfidence < 0 || options.MinimumConfidenceGap < 0 ||
		options.MaxProbeBytes < 0 || options.MaxCandidateBytes < 0 ||
		options.MaxElements < 0 || options.MaxSequenceDepth < 0 || options.MaxFragments < 0 ||
		options.MaxCandidates < 0 || options.MinimumElements < 0 ||
		options.MaxTokens < 0 ||
		options.MaxDuration < 0 || options.MaxCandidateDuration < 0 {
		return normalizedTransferSyntaxProbeOptions{}, fmt.Errorf("%w: limits and thresholds must not be negative", ErrTransferSyntaxProbePolicy)
	}
	if options.Dictionary == nil {
		options.Dictionary = std.Dictionary
	}
	if len(options.Candidates) == 0 {
		options.Candidates = []transfer.Syntax{
			transfer.ImplicitVRLittleEndian,
			transfer.ExplicitVRLittleEndian,
			transfer.ExplicitVRBigEndian,
		}
	} else {
		options.Candidates = append([]transfer.Syntax(nil), options.Candidates...)
	}
	if options.MinimumConfidence == 0 {
		options.MinimumConfidence = defaultTransferSyntaxProbeConfidence
	}
	if options.MinimumConfidenceGap == 0 {
		options.MinimumConfidenceGap = defaultTransferSyntaxProbeGap
	}
	if options.MaxProbeBytes == 0 {
		options.MaxProbeBytes = DefaultTransferSyntaxProbeBytes
	}
	if options.MaxCandidateBytes == 0 {
		options.MaxCandidateBytes = options.MaxProbeBytes
	}
	if options.MaxElements == 0 {
		options.MaxElements = defaultTransferSyntaxProbeElements
	}
	if options.MaxSequenceDepth == 0 {
		options.MaxSequenceDepth = defaultTransferSyntaxProbeDepth
	}
	if options.MaxFragments == 0 {
		options.MaxFragments = defaultTransferSyntaxProbeFragments
	}
	if options.MaxCandidates == 0 {
		options.MaxCandidates = defaultTransferSyntaxProbeCandidates
	}
	if options.MinimumElements == 0 {
		options.MinimumElements = defaultTransferSyntaxProbeMinimumElements
	}
	if options.MaxTokens == 0 {
		options.MaxTokens = defaultTransferSyntaxProbeTokens
	}
	if options.MaxDuration == 0 {
		options.MaxDuration = defaultTransferSyntaxProbeDuration
	}
	if options.MaxCandidateDuration == 0 {
		options.MaxCandidateDuration = defaultTransferSyntaxCandidateProbeDuration
	}

	if len(options.Candidates) > options.MaxCandidates {
		return normalizedTransferSyntaxProbeOptions{}, fmt.Errorf("%w: %d candidates exceed limit %d", ErrTransferSyntaxProbePolicy, len(options.Candidates), options.MaxCandidates)
	}
	seen := make(map[string]struct{}, len(options.Candidates))
	for i, candidate := range options.Candidates {
		candidate = canonicalProbeCandidate(candidate)
		if candidate.UID == "" || candidate.ByteOrder == nil || !candidate.Supported {
			return normalizedTransferSyntaxProbeOptions{}, fmt.Errorf("%w: candidate %d is not a supported native syntax", ErrTransferSyntaxProbePolicy, i)
		}
		if candidate.Encapsulated || candidate.Deflated || candidate.MediaPayload {
			return normalizedTransferSyntaxProbeOptions{}, fmt.Errorf("%w: candidate %q requires an explicit transfer syntax hint", ErrTransferSyntaxProbePolicy, candidate.UID)
		}
		if _, ok := seen[candidate.UID]; ok {
			return normalizedTransferSyntaxProbeOptions{}, fmt.Errorf("%w: duplicate candidate %q", ErrTransferSyntaxProbePolicy, candidate.UID)
		}
		seen[candidate.UID] = struct{}{}
		options.Candidates[i] = candidate
	}
	return normalizedTransferSyntaxProbeOptions{TransferSyntaxProbeOptions: options}, nil
}

func canonicalProbeCandidate(candidate transfer.Syntax) transfer.Syntax {
	uid := transfer.NormalizeUID(candidate.UID)
	if registered, ok := transfer.DefaultRegistry.Get(uid); ok {
		return registered
	}
	candidate.UID = uid
	return candidate
}

func probeTransferSyntaxCandidate(
	data []byte,
	syntax transfer.Syntax,
	opts normalizedTransferSyntaxProbeOptions,
	inputTruncated bool,
	sharedDeadline time.Time,
) TransferSyntaxCandidateReport {
	report := TransferSyntaxCandidateReport{Syntax: syntax}
	reader := NewReader(bytes.NewReader(data), syntax, ReaderOptions{
		Dictionary:          opts.Dictionary,
		MaxTotalBytes:       int64(len(data)) + 1,
		MaxSequenceDepth:    opts.MaxSequenceDepth,
		MaxElements:         opts.MaxElements,
		MaxFragments:        opts.MaxFragments,
		StrictReservedBytes: true,
		OddLengthPolicy:     RejectOddLength,
		SkipPixelData:       true,
	})
	reader.stopBeforePixelData = true
	candidateDeadline := time.Now().Add(opts.MaxCandidateDuration)
	if sharedDeadline.Before(candidateDeadline) {
		candidateDeadline = sharedDeadline
	}
	depth := 0
	lastTags := make(map[int]core.Tag)
	var parseErr error
	for {
		if report.Tokens >= opts.MaxTokens {
			report.Reason = TransferSyntaxProbeReasonCandidateBudget
			report.FailureClass = TransferSyntaxProbeFailureEvidenceBudget
			break
		}
		if !time.Now().Before(candidateDeadline) {
			if !time.Now().Before(sharedDeadline) {
				report.Reason = TransferSyntaxProbeReasonSharedTimeout
			} else {
				report.Reason = TransferSyntaxProbeReasonCandidateTimeout
			}
			report.FailureClass = TransferSyntaxProbeFailureTimeout
			break
		}
		token, err := reader.Next()
		if errors.Is(err, io.EOF) {
			report.Complete = !inputTruncated
			if report.Complete {
				report.Reason = TransferSyntaxProbeReasonComplete
			} else if report.Elements > 0 {
				report.Reason = TransferSyntaxProbeReasonPrefixCoherent
				report.FailureClass = TransferSyntaxProbeFailureEvidenceBudget
			} else {
				report.Reason = TransferSyntaxProbeReasonCandidateBudget
			}
			break
		}
		if err != nil {
			parseErr = err
			switch {
			case errors.Is(err, ErrNonZeroReservedBytes):
				report.StrictnessViolation = TransferSyntaxProbeStrictnessReservedBytes
			case errors.Is(err, ErrOddElementLength):
				report.StrictnessViolation = TransferSyntaxProbeStrictnessOddElementLength
			}
			report.FailureClass = classifyTransferSyntaxProbeFailure(err, inputTruncated && reader.Position() >= int64(len(data)))
			switch report.FailureClass {
			case TransferSyntaxProbeFailureEvidenceBudget:
				report.Reason = TransferSyntaxProbeReasonCandidateBudget
			case TransferSyntaxProbeFailureSafetyBudget:
				report.Reason = TransferSyntaxProbeReasonSafetyBudget
			default:
				report.Reason = TransferSyntaxProbeReasonParseError
			}
			break
		}
		report.Tokens++
		switch token.Kind {
		case TokenEndItem, TokenEndSequence:
			delete(lastTags, depth)
			if depth > 0 {
				depth--
			}
			continue
		case TokenStartItem:
			depth++
			continue
		}

		report.Elements++
		tag := token.Header.Tag
		if tag.IsPrivate() {
			report.PrivateElements++
		}
		if entry, ok := opts.Dictionary.ByTag(tag); ok {
			report.KnownElements++
			if !syntax.ExplicitVR || entry.VR == token.Header.VR || entry.VR == core.VRUN {
				report.DictionaryVRMatches++
			} else {
				report.DictionaryVRMismatches++
			}
			if count, ok := fixedWidthMultiplicity(token.Header); ok && vmAllowsCount(entry.VM, count) {
				report.DictionaryVMMatches++
			}
		}
		if plausibleElementLength(token.Header) {
			report.PlausibleLengths++
		}
		if previous, ok := lastTags[depth]; !ok || previous.Compare(tag) <= 0 {
			report.OrderedElements++
			lastTags[depth] = tag
		}
		if token.Header.Tag == core.TagPixelData || token.Kind == TokenStartPixelSequence {
			report.Reason = TransferSyntaxProbeReasonPixelDataBoundary
			report.FailureClass = TransferSyntaxProbeFailureEvidenceBudget
			break
		}
		if token.Kind == TokenStartSequence || token.Kind == TokenStartPixelSequence {
			depth++
		}
	}
	report.BytesConsumed = reader.Position()
	if report.Elements == 0 {
		report.Reason = TransferSyntaxProbeReasonNoEvidence
		report.Confidence = 0
		report.Score = 0
		return report
	}
	report.Confidence = candidateConfidence(report, len(data), parseErr)
	report.Score = int(report.Confidence*1000 + 0.5)
	return report
}

func candidateConfidence(report TransferSyntaxCandidateReport, inputBytes int, parseErr error) float64 {
	if report.FailureClass == TransferSyntaxProbeFailureSafetyBudget || report.FailureClass == TransferSyntaxProbeFailureTimeout {
		return 0
	}
	confidence := 0.0
	if report.Complete {
		confidence += 0.35
	} else if report.FailureClass == TransferSyntaxProbeFailureEvidenceBudget && report.Elements > 0 {
		confidence += 0.30
	}
	confidence += minFloat(0.20, float64(report.Elements)*0.04)
	confidence += 0.20 * ratio(report.KnownElements, report.Elements)
	confidence += 0.10 * ratio(report.PlausibleLengths, report.Elements)
	confidence += 0.05 * ratio(report.OrderedElements, report.Elements)
	if report.KnownElements > 0 {
		confidence += 0.10 * ratio(report.DictionaryVRMatches, report.KnownElements)
		confidence += 0.05 * ratio(report.DictionaryVMMatches, report.KnownElements)
	} else if report.Syntax.ExplicitVR {
		// A syntactically valid explicit VR remains useful evidence for private
		// datasets, but is weaker than a dictionary match.
		confidence += 0.05
	}
	if inputBytes > 0 {
		confidence += 0.10 * minFloat(1, float64(report.BytesConsumed)/float64(inputBytes))
	}
	confidence -= minFloat(0.25, float64(report.DictionaryVRMismatches)*0.10)
	if parseErr != nil && report.FailureClass == TransferSyntaxProbeFailureMalformed {
		confidence -= 0.20
	} else if parseErr != nil && report.FailureClass == TransferSyntaxProbeFailureTruncated {
		confidence -= 0.10
	}
	if confidence < 0 {
		return 0
	}
	if confidence > 1 {
		return 1
	}
	return confidence
}

func plausibleElementLength(header core.ElementHeader) bool {
	if header.Length.IsUndefined() {
		return header.VR == core.VRSQ || header.VR == core.VRUN || header.Tag == core.TagPixelData
	}
	length := uint32(header.Length)
	width := uint32(1)
	switch header.VR {
	case core.VRUS, core.VRSS:
		width = 2
	case core.VRAT, core.VRFL, core.VRSL, core.VRUL:
		width = 4
	case core.VRFD, core.VROD, core.VROV, core.VRSV, core.VRUV:
		width = 8
	}
	return length%width == 0
}

func fixedWidthMultiplicity(header core.ElementHeader) (int, bool) {
	if header.Length.IsUndefined() {
		return 0, false
	}
	width := uint32(0)
	switch header.VR {
	case core.VRUS, core.VRSS:
		width = 2
	case core.VRAT, core.VRFL, core.VRSL, core.VRUL:
		width = 4
	case core.VRFD, core.VROD, core.VROV, core.VRSV, core.VRUV:
		width = 8
	default:
		return 0, false
	}
	length := uint32(header.Length)
	if width == 0 || length%width != 0 {
		return 0, false
	}
	return int(length / width), true
}

func vmAllowsCount(vm string, count int) bool {
	if count <= 0 || vm == "" {
		return false
	}
	parts := strings.Split(vm, "-")
	minimum, err := strconv.Atoi(parts[0])
	if err != nil {
		return false
	}
	if len(parts) == 1 {
		return count == minimum
	}
	if len(parts) != 2 || count < minimum {
		return false
	}
	upper := parts[1]
	if upper == "n" {
		return true
	}
	if strings.HasSuffix(upper, "n") {
		multiple, err := strconv.Atoi(strings.TrimSuffix(upper, "n"))
		return err == nil && multiple > 0 && count%multiple == 0
	}
	maximum, err := strconv.Atoi(upper)
	return err == nil && count <= maximum
}

func classifyTransferSyntaxProbeFailure(err error, atEvidenceBoundary bool) TransferSyntaxProbeFailureClass {
	if errors.Is(err, ErrMaxElementsExceeded) || errors.Is(err, ErrMaxTotalBytesExceeded) {
		return TransferSyntaxProbeFailureEvidenceBudget
	}
	if errors.Is(err, ErrMaxDepthExceeded) || errors.Is(err, ErrMaxFragmentsExceeded) || errors.Is(err, ErrMaxElementBytesExceeded) {
		return TransferSyntaxProbeFailureSafetyBudget
	}
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		if atEvidenceBoundary {
			return TransferSyntaxProbeFailureEvidenceBudget
		}
		return TransferSyntaxProbeFailureTruncated
	}
	return TransferSyntaxProbeFailureMalformed
}

func probeTimedOut(candidates []TransferSyntaxCandidateReport) bool {
	for _, candidate := range candidates {
		if candidate.FailureClass == TransferSyntaxProbeFailureTimeout {
			return true
		}
	}
	return false
}

func ratio(numerator, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator)
}

func minFloat(left, right float64) float64 {
	if left < right {
		return left
	}
	return right
}

func minInt64(left, right int64) int64 {
	if left < right {
		return left
	}
	return right
}

func appendWarningOnce(warnings []TransferSyntaxProbeWarning, warning TransferSyntaxProbeWarning) []TransferSyntaxProbeWarning {
	for _, existing := range warnings {
		if existing == warning {
			return warnings
		}
	}
	return append(warnings, warning)
}
