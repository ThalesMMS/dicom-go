package object

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var (
	ErrTransferSyntaxRecoveryBufferExceeded = errors.New("dicom: transfer syntax recovery non-seekable buffer limit exceeded")
	ErrTransferSyntaxRecoveryPolicy         = errors.New("dicom: invalid transfer syntax recovery policy")
)

const defaultTransferSyntaxRecoveryNonSeekableBytes = 16 << 20

// TransferSyntaxRecoveryOptions authorizes specific non-conformant read
// recoveries. Every flag is false by default, and the existing ReadFile and
// ReadDataSet APIs remain strict and do not consult this policy.
type TransferSyntaxRecoveryOptions struct {
	AllowMissingPreamble          bool
	AllowMissingFileMeta          bool
	AllowMissingTransferSyntaxUID bool
	AllowUnknownTransferSyntaxUID bool
	AllowDeclaredMismatch         bool

	Probe parser.TransferSyntaxProbeOptions

	// MaxNonSeekableBytes bounds replay when recovery needs more than one pass
	// over an io.Reader that is not seekable. Zero uses a 16 MiB limit. The
	// complete source must fit; seekable sources do not use this buffer.
	MaxNonSeekableBytes int64
}

// TransferSyntaxResolutionSource records why the effective transfer syntax was
// selected. Values are fixed diagnostic codes and contain no dataset values.
type TransferSyntaxResolutionSource string

const (
	TransferSyntaxSourceDeclared                TransferSyntaxResolutionSource = "declared"
	TransferSyntaxSourceDeclaredMissingPreamble TransferSyntaxResolutionSource = "declared-missing-preamble"
	TransferSyntaxSourceInferredRawDataSet      TransferSyntaxResolutionSource = "inferred-raw-dataset"
	TransferSyntaxSourceInferredMissingPreamble TransferSyntaxResolutionSource = "inferred-missing-preamble"
	TransferSyntaxSourceInferredMissingFileMeta TransferSyntaxResolutionSource = "inferred-missing-file-meta"
	TransferSyntaxSourceInferredMissingUID      TransferSyntaxResolutionSource = "inferred-missing-transfer-syntax-uid"
	TransferSyntaxSourceInferredUnknownUID      TransferSyntaxResolutionSource = "inferred-unknown-transfer-syntax-uid"
	TransferSyntaxSourceRecoveredMismatch       TransferSyntaxResolutionSource = "recovered-declared-mismatch"
)

// TransferSyntaxResolution is the auditable result of a dedicated recovery
// read. DeclaredUID is populated only for a recognized registered syntax; an
// unknown File Meta value remains available in File.Meta but is never copied
// into diagnostics. Probe diagnostics are structural and value-free by
// contract.
type TransferSyntaxResolution struct {
	Syntax      transfer.Syntax
	Source      TransferSyntaxResolutionSource
	DeclaredUID string
	Confidence  float64
	Probe       parser.TransferSyntaxProbeReport
}

// Inferred reports whether the effective syntax came from bounded inference.
func (r TransferSyntaxResolution) Inferred() bool {
	switch r.Source {
	case TransferSyntaxSourceInferredRawDataSet,
		TransferSyntaxSourceInferredMissingPreamble,
		TransferSyntaxSourceInferredMissingFileMeta,
		TransferSyntaxSourceInferredMissingUID,
		TransferSyntaxSourceInferredUnknownUID,
		TransferSyntaxSourceRecoveredMismatch:
		return true
	default:
		return false
	}
}

// Clone returns a resolution whose diagnostic slices do not alias the source.
func (r TransferSyntaxResolution) Clone() TransferSyntaxResolution {
	r.Probe = r.Probe.Clone()
	return r
}

// String formats only syntax metadata and fixed structural diagnostics.
func (r TransferSyntaxResolution) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "source=%s", r.Source)
	if r.Syntax.UID != "" {
		fmt.Fprintf(&b, " syntax=%s", r.Syntax.UID)
	}
	if r.Confidence != 0 {
		fmt.Fprintf(&b, " confidence=%.3f", r.Confidence)
	}
	if r.Probe.Outcome != "" {
		fmt.Fprintf(&b, " probe={%s}", r.Probe.String())
	}
	return b.String()
}

// TransferSyntaxResolution returns an immutable copy of the recovery decision
// stored on a file. Ordinary strict APIs return false; the dedicated recovery
// API also records an authoritative declared decision when no inference was
// needed.
func (f *File) TransferSyntaxResolution() (TransferSyntaxResolution, bool) {
	if f == nil || f.transferSyntaxResolution == nil {
		return TransferSyntaxResolution{}, false
	}
	return f.transferSyntaxResolution.Clone(), true
}

// TransferSyntaxResolution returns an immutable copy of the recovery decision
// stored on a raw dataset object.
func (o *Object) TransferSyntaxResolution() (TransferSyntaxResolution, bool) {
	if o == nil || o.transferSyntaxResolution == nil {
		return TransferSyntaxResolution{}, false
	}
	return o.transferSyntaxResolution.Clone(), true
}

// ReadDataSetWithTransferSyntaxRecovery infers a syntax for a raw dataset and
// then performs one final parse with the caller's normal ReadFileOptions. This
// dedicated API deliberately does not reinterpret transfer.Syntax{} in the
// historical ReadDataSet APIs.
func ReadDataSetWithTransferSyntaxRecovery(
	r io.Reader,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
) (*Object, TransferSyntaxResolution, error) {
	source, start, originallySeekable, err := prepareTransferSyntaxRecoverySource(r, readOptions, recoveryOptions)
	if err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	report, err := probeTransferSyntaxAt(source, start, recoveryOptions.Probe, readOptions.MaxTotalBytes, 0)
	resolution := resolutionFromProbe(report, TransferSyntaxSourceInferredRawDataSet, "")
	if err != nil {
		return nil, resolution, err
	}
	if _, err := source.Seek(start, io.SeekStart); err != nil {
		return nil, resolution, err
	}
	obj, _, err := readDataSetObject(source, report.Selected, readOptions, 0, originallySeekable)
	if err != nil {
		return nil, resolution, err
	}
	stored := resolution.Clone()
	obj.transferSyntaxResolution = &stored
	return obj, resolution, nil
}

// OpenDataSetWithTransferSyntaxRecovery is the file-backed counterpart of
// ReadDataSetWithTransferSyntaxRecovery. It keeps the file open only when a
// deferred value provider needs it.
func OpenDataSetWithTransferSyntaxRecovery(
	path string,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
) (result *Object, resolution TransferSyntaxResolution, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	closeFile := true
	defer func() {
		if !closeFile {
			return
		}
		if closeErr := f.Close(); closeErr != nil {
			if err == nil {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	result, resolution, err = ReadDataSetWithTransferSyntaxRecovery(f, readOptions, recoveryOptions)
	if err == nil && result != nil && result.valueProvider != nil && result.deferredCount > 0 {
		result.source = f
		closeFile = false
	}
	return result, resolution, err
}

// ReadFileWithTransferSyntaxRecovery inspects only Part 10 structure before
// deciding whether an authorized probe is necessary. Dataset values and
// caller callbacks are processed exactly once by the final parse.
func ReadFileWithTransferSyntaxRecovery(
	r io.Reader,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
) (*File, TransferSyntaxResolution, error) {
	if !recoveryOptions.enablesFileRecovery() {
		file, err := ReadFileWithOptions(r, readOptions)
		if err != nil {
			return nil, TransferSyntaxResolution{}, err
		}
		resolution := declaredTransferSyntaxResolution(file.TransferSyntax)
		stored := resolution.Clone()
		file.transferSyntaxResolution = &stored
		return file, resolution, nil
	}

	source, streamBase, originallySeekable, err := prepareTransferSyntaxRecoverySource(r, readOptions, recoveryOptions)
	if err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	if _, err := source.Seek(streamBase, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	prefix := make([]byte, part10PreambleLength+len(part10Magic))
	n, prefixErr := io.ReadFull(source, prefix)
	if prefixErr != nil || n < len(prefix) || string(prefix[part10PreambleLength:]) != part10Magic {
		if !recoveryOptions.AllowMissingPreamble {
			return readStrictFileAt(source, streamBase, readOptions)
		}
		return recoverFileWithoutPart10Preamble(source, streamBase, readOptions, recoveryOptions, originallySeekable)
	}

	datasetOffset := int64(len(prefix))
	if _, err := source.Seek(streamBase+datasetOffset, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	var firstTag [4]byte
	if _, err := io.ReadFull(source, firstTag[:]); err != nil {
		return readStrictFileAt(source, streamBase, readOptions)
	}
	if firstTag[0] != 0x02 || firstTag[1] != 0x00 {
		if !recoveryOptions.AllowMissingFileMeta {
			return readStrictFileAt(source, streamBase, readOptions)
		}
		return recoverFileDataSetAt(
			source,
			streamBase,
			datasetOffset,
			prefix[:part10PreambleLength],
			nil,
			TransferSyntaxSourceInferredMissingFileMeta,
			"",
			readOptions,
			recoveryOptions,
			originallySeekable,
		)
	}

	if _, err := source.Seek(streamBase+int64(len(prefix)), io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	metaReader := bufio.NewReader(source)
	meta, datasetOffset, metaErr := readFileMetaElements(metaReader, int64(len(prefix)), readOptions)
	if metaErr != nil {
		return readStrictFileAt(source, streamBase, readOptions)
	}
	declaredUID, _ := meta.GetString(tagTransferSyntaxUID)
	declaredUID = transfer.NormalizeUID(declaredUID)
	declaredSyntax, resolveErr := resolveFileMetaTransferSyntax(meta)

	switch {
	case errors.Is(resolveErr, ErrMissingTransferSyntax) && recoveryOptions.AllowMissingTransferSyntaxUID:
		return recoverFileDataSetAt(
			source, streamBase, datasetOffset, prefix[:part10PreambleLength], meta,
			TransferSyntaxSourceInferredMissingUID, declaredUID,
			readOptions, recoveryOptions, originallySeekable,
		)
	case errors.Is(resolveErr, transfer.ErrUnknownTransferSyntax) && recoveryOptions.AllowUnknownTransferSyntaxUID:
		return recoverFileDataSetAt(
			source, streamBase, datasetOffset, prefix[:part10PreambleLength], meta,
			TransferSyntaxSourceInferredUnknownUID, declaredUID,
			readOptions, recoveryOptions, originallySeekable,
		)
	case resolveErr != nil:
		return readStrictFileAt(source, streamBase, readOptions)
	}
	return readDeclaredOrMismatchAt(
		source, streamBase, datasetOffset, prefix[:part10PreambleLength], meta,
		declaredSyntax, TransferSyntaxSourceDeclared, declaredUID,
		readOptions, recoveryOptions, originallySeekable,
	)
}

func recoverFileWithoutPart10Preamble(
	source io.ReadSeeker,
	streamBase int64,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
	originallySeekable bool,
) (*File, TransferSyntaxResolution, error) {
	if _, err := source.Seek(streamBase, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	var first [4]byte
	n, err := io.ReadFull(source, first[:])
	if err != nil || n < len(first) {
		return recoverFileDataSetAt(
			source, streamBase, 0, nil, nil,
			TransferSyntaxSourceInferredMissingPreamble, "",
			readOptions, recoveryOptions, originallySeekable,
		)
	}

	metaOffset := int64(-1)
	if string(first[:]) == part10Magic {
		metaOffset = int64(len(part10Magic))
	} else if first[0] == 0x02 && first[1] == 0x00 {
		metaOffset = 0
	}
	if metaOffset < 0 {
		return recoverFileDataSetAt(
			source, streamBase, 0, nil, nil,
			TransferSyntaxSourceInferredMissingPreamble, "",
			readOptions, recoveryOptions, originallySeekable,
		)
	}

	if _, err := source.Seek(streamBase+metaOffset, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	if metaOffset > 0 {
		var firstTag [4]byte
		if _, err := io.ReadFull(source, firstTag[:]); err != nil {
			return readStrictFileAt(source, streamBase, readOptions)
		}
		if firstTag[0] != 0x02 || firstTag[1] != 0x00 {
			if !recoveryOptions.AllowMissingFileMeta {
				return readStrictFileAt(source, streamBase, readOptions)
			}
			return recoverFileDataSetAt(
				source, streamBase, metaOffset, nil, nil,
				TransferSyntaxSourceInferredMissingFileMeta, "",
				readOptions, recoveryOptions, originallySeekable,
			)
		}
	}

	if _, err := source.Seek(streamBase+metaOffset, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	meta, datasetOffset, metaErr := readFileMetaElements(bufio.NewReader(source), metaOffset, readOptions)
	if metaErr != nil {
		return readStrictFileAt(source, streamBase, readOptions)
	}
	declaredUID, _ := meta.GetString(tagTransferSyntaxUID)
	declaredUID = transfer.NormalizeUID(declaredUID)
	declaredSyntax, resolveErr := resolveFileMetaTransferSyntax(meta)
	switch {
	case resolveErr == nil:
		return readDeclaredOrMismatchAt(
			source, streamBase, datasetOffset, nil, meta,
			declaredSyntax, TransferSyntaxSourceDeclaredMissingPreamble, declaredUID,
			readOptions, recoveryOptions, originallySeekable,
		)
	case errors.Is(resolveErr, ErrMissingTransferSyntax) && recoveryOptions.AllowMissingTransferSyntaxUID:
		return recoverFileDataSetAt(
			source, streamBase, datasetOffset, nil, meta,
			TransferSyntaxSourceInferredMissingUID, declaredUID,
			readOptions, recoveryOptions, originallySeekable,
		)
	case errors.Is(resolveErr, transfer.ErrUnknownTransferSyntax) && recoveryOptions.AllowUnknownTransferSyntaxUID:
		return recoverFileDataSetAt(
			source, streamBase, datasetOffset, nil, meta,
			TransferSyntaxSourceInferredUnknownUID, declaredUID,
			readOptions, recoveryOptions, originallySeekable,
		)
	default:
		return readStrictFileAt(source, streamBase, readOptions)
	}
}

func readDeclaredOrMismatchAt(
	source io.ReadSeeker,
	streamBase int64,
	datasetOffset int64,
	preamble []byte,
	meta *Object,
	declaredSyntax transfer.Syntax,
	declaredSource TransferSyntaxResolutionSource,
	declaredUID string,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
	originallySeekable bool,
) (*File, TransferSyntaxResolution, error) {
	if recoveryOptions.AllowDeclaredMismatch && syntaxEligibleForMismatchRecovery(declaredSyntax) {
		report, probeErr := probeTransferSyntaxAt(
			source,
			streamBase+datasetOffset,
			recoveryOptions.Probe,
			readOptions.MaxTotalBytes,
			datasetOffset,
		)
		declaredCandidate, declaredCandidateFound := declaredCandidateReport(report, declaredSyntax.UID)
		if probeErr == nil && report.Selected.UID != declaredSyntax.UID && declaredCandidateFound && declaredCandidate.FailureClass == parser.TransferSyntaxProbeFailureMalformed {
			if callerAllowsProbeStrictnessViolation(readOptions, declaredCandidate.StrictnessViolation) {
				file, resolution, declaredErr := readDeclaredFileDataSetWithProbeAt(
					source, streamBase, datasetOffset, preamble, meta,
					declaredSyntax, declaredSource, declaredUID,
					readOptions, originallySeekable, report,
				)
				if declaredErr == nil || readOptions.FrameSink != nil {
					return file, resolution, declaredErr
				}
				recovered := resolutionFromProbe(report, TransferSyntaxSourceRecoveredMismatch, declaredUID)
				file, resolution, recoveryErr := readRecoveredFileFromReport(
					source, streamBase, datasetOffset, preamble, meta,
					recovered, readOptions, originallySeekable,
				)
				if recoveryErr != nil {
					return nil, resolution, errors.Join(declaredErr, recoveryErr)
				}
				return file, resolution, nil
			}
			resolution := resolutionFromProbe(report, TransferSyntaxSourceRecoveredMismatch, declaredUID)
			return readRecoveredFileFromReport(
				source, streamBase, datasetOffset, preamble, meta,
				resolution, readOptions, originallySeekable,
			)
		}
		return readDeclaredFileDataSetWithProbeAt(
			source, streamBase, datasetOffset, preamble, meta,
			declaredSyntax, declaredSource, declaredUID,
			readOptions, originallySeekable, report, probeErr,
		)
	}
	return readDeclaredFileDataSetAt(
		source, streamBase, datasetOffset, preamble, meta,
		declaredSyntax, declaredSource, declaredUID,
		readOptions, originallySeekable,
	)
}

func readDeclaredFileDataSetWithProbeAt(
	source io.ReadSeeker,
	streamBase int64,
	datasetOffset int64,
	preamble []byte,
	meta *Object,
	syntax transfer.Syntax,
	sourceCode TransferSyntaxResolutionSource,
	declaredUID string,
	readOptions ReadFileOptions,
	originallySeekable bool,
	report parser.TransferSyntaxProbeReport,
	probeErrors ...error,
) (*File, TransferSyntaxResolution, error) {
	file, resolution, err := readDeclaredFileDataSetAt(
		source, streamBase, datasetOffset, preamble, meta,
		syntax, sourceCode, declaredUID, readOptions, originallySeekable,
	)
	resolution.Probe = report.Clone()
	if file != nil {
		stored := resolution.Clone()
		file.transferSyntaxResolution = &stored
	}
	if err == nil {
		return file, resolution, nil
	}
	joined := []error{err}
	for _, probeErr := range probeErrors {
		if probeErr != nil {
			joined = append(joined, probeErr)
		}
	}
	return nil, resolution, errors.Join(joined...)
}

func readDeclaredFileDataSetAt(
	source io.ReadSeeker,
	streamBase int64,
	datasetOffset int64,
	preamble []byte,
	meta *Object,
	syntax transfer.Syntax,
	sourceCode TransferSyntaxResolutionSource,
	declaredUID string,
	readOptions ReadFileOptions,
	originallySeekable bool,
) (*File, TransferSyntaxResolution, error) {
	resolution := TransferSyntaxResolution{
		Syntax:      syntax,
		Source:      sourceCode,
		DeclaredUID: safeDeclaredSyntaxUID(declaredUID),
		Confidence:  1,
	}
	if _, err := source.Seek(streamBase+datasetOffset, io.SeekStart); err != nil {
		return nil, resolution, err
	}
	dataset, _, err := readDataSetObject(source, syntax, readOptions, datasetOffset, originallySeekable)
	if err != nil {
		return nil, resolution, wrapDataSetError(err)
	}
	return newResolvedFile(preamble, meta, dataset, resolution), resolution, nil
}

func readStrictFileAt(source io.ReadSeeker, streamBase int64, readOptions ReadFileOptions) (*File, TransferSyntaxResolution, error) {
	if _, err := source.Seek(streamBase, io.SeekStart); err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	file, err := ReadFileWithOptions(source, readOptions)
	if err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	resolution := declaredTransferSyntaxResolution(file.TransferSyntax)
	stored := resolution.Clone()
	file.transferSyntaxResolution = &stored
	return file, resolution, nil
}

func declaredCandidateReport(report parser.TransferSyntaxProbeReport, declaredUID string) (parser.TransferSyntaxCandidateReport, bool) {
	for _, candidate := range report.Candidates {
		if candidate.Syntax.UID == declaredUID {
			return candidate, true
		}
	}
	return parser.TransferSyntaxCandidateReport{}, false
}

func callerAllowsProbeStrictnessViolation(readOptions ReadFileOptions, violation parser.TransferSyntaxProbeStrictnessViolation) bool {
	switch violation {
	case parser.TransferSyntaxProbeStrictnessReservedBytes:
		return !readOptions.StrictReservedBytes
	case parser.TransferSyntaxProbeStrictnessOddElementLength:
		return readOptions.OddLengthPolicy == parser.AcceptOddLength
	default:
		return false
	}
}

func syntaxEligibleForMismatchRecovery(syntax transfer.Syntax) bool {
	return syntax.Supported && !syntax.Encapsulated && !syntax.Deflated && !syntax.MediaPayload
}

// OpenFileWithTransferSyntaxRecovery is the file-backed counterpart of
// ReadFileWithTransferSyntaxRecovery.
func OpenFileWithTransferSyntaxRecovery(
	path string,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
) (result *File, resolution TransferSyntaxResolution, err error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, TransferSyntaxResolution{}, err
	}
	closeFile := true
	defer func() {
		if !closeFile {
			return
		}
		if closeErr := f.Close(); closeErr != nil {
			if err == nil {
				err = closeErr
			} else {
				err = errors.Join(err, closeErr)
			}
		}
	}()
	result, resolution, err = ReadFileWithTransferSyntaxRecovery(f, readOptions, recoveryOptions)
	if err == nil && result != nil && result.Dataset != nil && result.Dataset.valueProvider != nil && result.Dataset.deferredCount > 0 {
		result.source = f
		closeFile = false
	}
	return result, resolution, err
}

func recoverFileDataSetAt(
	source io.ReadSeeker,
	streamBase int64,
	datasetOffset int64,
	preamble []byte,
	meta *Object,
	sourceCode TransferSyntaxResolutionSource,
	declaredUID string,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
	originallySeekable bool,
) (*File, TransferSyntaxResolution, error) {
	report, probeErr := probeTransferSyntaxAt(
		source,
		streamBase+datasetOffset,
		recoveryOptions.Probe,
		readOptions.MaxTotalBytes,
		datasetOffset,
	)
	resolution := resolutionFromProbe(report, sourceCode, declaredUID)
	if probeErr != nil {
		return nil, resolution, probeErr
	}
	return readRecoveredFileFromReport(
		source, streamBase, datasetOffset, preamble, meta,
		resolution, readOptions, originallySeekable,
	)
}

func readRecoveredFileFromReport(
	source io.ReadSeeker,
	streamBase int64,
	datasetOffset int64,
	preamble []byte,
	meta *Object,
	resolution TransferSyntaxResolution,
	readOptions ReadFileOptions,
	originallySeekable bool,
) (*File, TransferSyntaxResolution, error) {
	if _, err := source.Seek(streamBase+datasetOffset, io.SeekStart); err != nil {
		return nil, resolution, err
	}
	dataset, _, err := readDataSetObject(source, resolution.Syntax, readOptions, datasetOffset, originallySeekable)
	if err != nil {
		return nil, resolution, wrapDataSetError(err)
	}
	return newResolvedFile(preamble, meta, dataset, resolution), resolution, nil
}

func newResolvedFile(preamble []byte, meta, dataset *Object, resolution TransferSyntaxResolution) *File {
	file := &File{
		Preamble:       append([]byte(nil), preamble...),
		Meta:           meta,
		Dataset:        dataset,
		TransferSyntax: resolution.Syntax,
	}
	stored := resolution.Clone()
	file.transferSyntaxResolution = &stored
	return file
}

func probeTransferSyntaxAt(
	source io.ReadSeeker,
	absoluteOffset int64,
	options parser.TransferSyntaxProbeOptions,
	maxTotalBytes int64,
	baseOffset int64,
) (parser.TransferSyntaxProbeReport, error) {
	if _, err := source.Seek(absoluteOffset, io.SeekStart); err != nil {
		return parser.TransferSyntaxProbeReport{}, err
	}
	limit := options.MaxProbeBytes
	if limit == 0 {
		limit = parser.DefaultTransferSyntaxProbeBytes
	}
	if limit < 0 {
		return parser.ProbeTransferSyntax(nil, options)
	}
	readLimit := limit + 1
	if maxTotalBytes > 0 {
		remaining := maxTotalBytes - baseOffset
		if remaining <= 0 {
			return parser.TransferSyntaxProbeReport{}, fmt.Errorf(
				"%w: base offset %d reaches limit %d",
				parser.ErrMaxTotalBytesExceeded,
				baseOffset,
				maxTotalBytes,
			)
		}
		if remaining <= limit {
			readLimit = remaining
		}
	}
	data, err := io.ReadAll(io.LimitReader(source, readLimit))
	if err != nil {
		return parser.TransferSyntaxProbeReport{}, err
	}
	if _, err := source.Seek(absoluteOffset, io.SeekStart); err != nil {
		return parser.TransferSyntaxProbeReport{}, err
	}
	return parser.ProbeTransferSyntax(data, options)
}

func prepareTransferSyntaxRecoverySource(
	r io.Reader,
	readOptions ReadFileOptions,
	recoveryOptions TransferSyntaxRecoveryOptions,
) (io.ReadSeeker, int64, bool, error) {
	if seeker, ok := r.(io.ReadSeeker); ok {
		start, err := seeker.Seek(0, io.SeekCurrent)
		return seeker, start, true, err
	}
	if readOptions.DeferPixelData || readOptions.DeferWaveformData {
		return nil, 0, false, ErrDeferredValueRequiresSeekable
	}
	limit := recoveryOptions.MaxNonSeekableBytes
	if limit == 0 {
		limit = defaultTransferSyntaxRecoveryNonSeekableBytes
	}
	if limit < 0 {
		return nil, 0, false, fmt.Errorf("%w: MaxNonSeekableBytes must not be negative", ErrTransferSyntaxRecoveryPolicy)
	}
	hardTotalLimit := false
	if readOptions.MaxTotalBytes > 0 && readOptions.MaxTotalBytes < limit {
		limit = readOptions.MaxTotalBytes
		hardTotalLimit = true
	} else if readOptions.MaxTotalBytes > 0 && readOptions.MaxTotalBytes == limit {
		hardTotalLimit = true
	}
	readLimit := limit + 1
	if hardTotalLimit {
		// MaxTotalBytes promises a hard source-read bound. At the exact boundary
		// we conservatively report exhaustion instead of reading one extra byte to
		// distinguish EOF, matching parser.Reader's boundary semantics.
		readLimit = limit
	}
	data, err := io.ReadAll(io.LimitReader(r, readLimit))
	if err != nil {
		return nil, 0, false, err
	}
	if hardTotalLimit && int64(len(data)) == limit {
		return nil, 0, false, errors.Join(
			fmt.Errorf("%w: limit %d", ErrTransferSyntaxRecoveryBufferExceeded, limit),
			fmt.Errorf("%w: read %d bytes, limit %d", parser.ErrMaxTotalBytesExceeded, len(data), limit),
		)
	}
	if int64(len(data)) > limit {
		return nil, 0, false, fmt.Errorf("%w: limit %d", ErrTransferSyntaxRecoveryBufferExceeded, limit)
	}
	return bytes.NewReader(data), 0, false, nil
}

func resolutionFromProbe(
	report parser.TransferSyntaxProbeReport,
	source TransferSyntaxResolutionSource,
	declaredUID string,
) TransferSyntaxResolution {
	return TransferSyntaxResolution{
		Syntax:      report.Selected,
		Source:      source,
		DeclaredUID: safeDeclaredSyntaxUID(declaredUID),
		Confidence:  report.Confidence,
		Probe:       report.Clone(),
	}
}

func safeDeclaredSyntaxUID(uid string) string {
	uid = transfer.NormalizeUID(uid)
	if uid == "" {
		return ""
	}
	if _, ok := transfer.DefaultRegistry.Get(uid); !ok {
		return ""
	}
	return uid
}

func declaredTransferSyntaxResolution(syntax transfer.Syntax) TransferSyntaxResolution {
	return TransferSyntaxResolution{
		Syntax:      syntax,
		Source:      TransferSyntaxSourceDeclared,
		DeclaredUID: syntax.UID,
		Confidence:  1,
	}
}

func (o TransferSyntaxRecoveryOptions) enablesFileRecovery() bool {
	return o.AllowMissingPreamble ||
		o.AllowMissingFileMeta ||
		o.AllowMissingTransferSyntaxUID ||
		o.AllowUnknownTransferSyntaxUID ||
		o.AllowDeclaredMismatch
}
