// Package dicominspect provides bounded, presentation-neutral summaries of
// DICOM Part 10 files.
package dicominspect

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	dicomtags "github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

const (
	DefaultMaxTotalBytes    int64 = 512 << 20
	DefaultMaxElementBytes  int64 = 32 << 20
	DefaultMaxElements            = 200000
	DefaultMaxSequenceDepth       = 64
	DefaultMaxFindings            = 256
	DefaultTimeout                = 15 * time.Second
)

var (
	ErrInvalidOptions = errors.New("dicom inspection options are invalid")
	ErrResourceLimit  = errors.New("dicom inspection resource limit exceeded")
	ErrMalformed      = errors.New("dicom input is malformed or unsupported")
	errMissingDataset = errors.New("dicom input does not contain a data set")
)

// Options bounds parsing, recursive validation, reporting, and elapsed time.
// Zero values use the package defaults. Negative values are invalid.
type Options struct {
	MaxTotalBytes    int64
	MaxElementBytes  int64
	MaxElements      int
	MaxSequenceDepth int
	MaxFindings      int
	Timeout          time.Duration
}

type Severity = validation.Severity

const (
	SeverityInfo    = validation.SeverityInfo
	SeverityWarning = validation.SeverityWarning
	SeverityError   = validation.SeverityError
)

// Source identifies the independently parsed portion that produced a finding
// or element row. SourceFile is reserved for operational diagnostics emitted
// before a trustworthy data set is available.
type Source string

const (
	SourceFile    Source = "file"
	SourceMeta    Source = "meta"
	SourceDataset Source = "dataset"
)

const (
	CodeCanceled      = "inspection.canceled"
	CodeTimeout       = "inspection.timeout"
	CodeResourceLimit = "inspection.resource_limit"
	CodeMalformed     = "inspection.malformed"
)

// Finding is a PHI-redacted diagnostic. Message is selected from fixed
// library text and never contains an element value or wrapped error text.
type Finding struct {
	Severity Severity `json:"severity"`
	Source   Source   `json:"source"`
	Tag      string   `json:"tag,omitempty"`
	Path     string   `json:"path,omitempty"`
	Code     string   `json:"code"`
	Message  string   `json:"message"`
}

// Summary is a JSON-friendly, presentation-neutral description of a DICOM
// file. Existing metadata fields remain stable; findings and paths are
// additive. Elements contains recursively flattened sequence rows.
type Summary struct {
	FileName          string `json:"fileName,omitempty"`
	TransferSyntaxUID string `json:"transferSyntaxUID"`
	TransferSyntax    string `json:"transferSyntax"`
	PayloadKind       string `json:"payloadKind,omitempty"`
	MediaType         string `json:"mediaType,omitempty"`
	PatientName       string `json:"patientName,omitempty"`
	PatientID         string `json:"patientID,omitempty"`
	PatientBirthDate  string `json:"patientBirthDate,omitempty"`
	InstitutionName   string `json:"institutionName,omitempty"`
	StudyDate         string `json:"studyDate,omitempty"`
	StudyTime         string `json:"studyTime,omitempty"`
	SeriesDate        string `json:"seriesDate,omitempty"`
	SeriesTime        string `json:"seriesTime,omitempty"`
	StudyDescription  string `json:"studyDescription,omitempty"`
	Modality          string `json:"modality,omitempty"`
	AccessionNumber   string `json:"accessionNumber,omitempty"`
	SeriesDescription string `json:"seriesDescription,omitempty"`
	ProtocolName      string `json:"protocolName,omitempty"`
	StudyInstanceUID  string `json:"studyInstanceUID,omitempty"`
	SeriesInstanceUID string `json:"seriesInstanceUID,omitempty"`
	SeriesNumber      string `json:"seriesNumber,omitempty"`
	SOPClassUID       string `json:"sopClassUID,omitempty"`
	SOPInstanceUID    string `json:"sopInstanceUID,omitempty"`
	InstanceNumber    string `json:"instanceNumber,omitempty"`

	StudyID                 string `json:"studyID,omitempty"`
	BodyPartExamined        string `json:"bodyPartExamined,omitempty"`
	ReferringPhysicianName  string `json:"referringPhysicianName,omitempty"`
	PerformingPhysicianName string `json:"performingPhysicianName,omitempty"`

	ElementCount      int              `json:"elementCount"`
	Elements          []ElementSummary `json:"elements"`
	FindingCount      int              `json:"findingCount,omitempty"`
	Findings          []Finding        `json:"findings,omitempty"`
	FindingsTruncated bool             `json:"findingsTruncated,omitempty"`
	DroppedFindings   int              `json:"droppedFindings,omitempty"`
}

// ElementSummary is a bounded, display-ready description of one element.
// Path is stable within the inspected file and includes zero-based sequence
// item indexes, for example "(0008,1111)[0]/(0008,1155)".
type ElementSummary struct {
	Source  Source `json:"source"`
	Path    string `json:"path,omitempty"`
	Tag     string `json:"tag"`
	VR      string `json:"vr"`
	Keyword string `json:"keyword,omitempty"`
	Name    string `json:"name,omitempty"`
	Length  string `json:"length"`
	Value   string `json:"value,omitempty"`
	Private bool   `json:"private"`
}

func DefaultOptions() Options {
	return Options{
		MaxTotalBytes:    DefaultMaxTotalBytes,
		MaxElementBytes:  DefaultMaxElementBytes,
		MaxElements:      DefaultMaxElements,
		MaxSequenceDepth: DefaultMaxSequenceDepth,
		MaxFindings:      DefaultMaxFindings,
		Timeout:          DefaultTimeout,
	}
}

// ValidateOptions rejects negative resource bounds. Zero values are valid and
// select safe defaults.
func ValidateOptions(opts Options) error {
	if opts.MaxTotalBytes < 0 || opts.MaxElementBytes < 0 || opts.MaxElements < 0 ||
		opts.MaxSequenceDepth < 0 || opts.MaxFindings < 0 || opts.Timeout < 0 {
		return ErrInvalidOptions
	}
	return nil
}

// InspectReader reads a DICOM file and returns a bounded summary. Use
// InspectReaderContext when the operation must be cancellable.
func InspectReader(fileName string, r io.Reader, opts Options) (Summary, error) {
	return InspectReaderContext(context.Background(), fileName, r, opts)
}

// InspectReaderContext is the cancellable variant of InspectReader.
func InspectReaderContext(ctx context.Context, fileName string, r io.Reader, opts Options) (Summary, error) {
	if ctx == nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: nil context")
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: %w", err)
	}
	ctx, cancel := withTimeout(ctx, normalized.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return failureSummary(fileName, err)
	}
	if r == nil {
		return failureSummary(fileName, errMissingDataset)
	}
	file, err := object.ReadFileWithOptions(readerWithContext(ctx, r), readOptions(normalized))
	if err != nil {
		return failureSummary(fileName, err)
	}
	defer func() { _ = file.Close() }()
	return summarizeFileContext(ctx, fileName, file, normalized)
}

// InspectFile opens and inspects the DICOM file at path.
func InspectFile(path string, opts Options) (Summary, error) {
	return InspectFileContext(context.Background(), path, opts)
}

// InspectFileContext is the cancellable variant of InspectFile.
func InspectFileContext(ctx context.Context, path string, opts Options) (Summary, error) {
	if ctx == nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: nil context")
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: %w", err)
	}
	ctx, cancel := withTimeout(ctx, normalized.Timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return failureSummary(filepath.Base(path), err)
	}
	file, err := os.Open(path)
	if err != nil {
		return Summary{}, fmt.Errorf("open DICOM file: %w", err)
	}
	defer func() { _ = file.Close() }()
	parsed, err := object.ReadFileWithOptions(readerWithContext(ctx, file), readOptions(normalized))
	if err != nil {
		return failureSummary(filepath.Base(path), err)
	}
	defer func() { _ = parsed.Close() }()
	return summarizeFileContext(ctx, filepath.Base(path), parsed, normalized)
}

// SummarizeFile returns a bounded summary of an already parsed DICOM file.
func SummarizeFile(fileName string, file *object.File, opts Options) (Summary, error) {
	return SummarizeFileContext(context.Background(), fileName, file, opts)
}

// SummarizeFileContext validates the complete object graph before producing a
// recursively flattened summary. Conformance findings never reject inspection
// in preserve mode; operational failures still return an error.
func SummarizeFileContext(ctx context.Context, fileName string, file *object.File, opts Options) (Summary, error) {
	if ctx == nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: nil context")
	}
	if file == nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: %w", object.ErrNilFile)
	}
	normalized, err := normalizeOptions(opts)
	if err != nil {
		return Summary{}, fmt.Errorf("inspect DICOM file: %w", err)
	}
	ctx, cancel := withTimeout(ctx, normalized.Timeout)
	defer cancel()
	return summarizeFileContext(ctx, fileName, file, normalized)
}

func summarizeFileContext(ctx context.Context, fileName string, file *object.File, opts Options) (Summary, error) {
	if file.Dataset == nil {
		return failureSummary(fileName, errMissingDataset)
	}
	if err := validateTraversal(ctx, file, opts); err != nil {
		return failureSummary(fileName, err)
	}

	summary := Summary{
		FileName:                fileName,
		TransferSyntaxUID:       file.TransferSyntax.UID,
		TransferSyntax:          file.TransferSyntax.Name,
		PayloadKind:             payloadKind(file.TransferSyntax),
		MediaType:               mediaType(file.TransferSyntax),
		PatientName:             getString(file, dicomtags.PatientName),
		PatientID:               getString(file, dicomtags.PatientID),
		PatientBirthDate:        getString(file, dicomtags.PatientBirthDate),
		InstitutionName:         getString(file, dicomtags.InstitutionName),
		StudyDate:               getString(file, dicomtags.StudyDate),
		StudyTime:               getString(file, dicomtags.StudyTime),
		SeriesDate:              getString(file, dicomtags.SeriesDate),
		SeriesTime:              getString(file, dicomtags.SeriesTime),
		StudyDescription:        getString(file, dicomtags.StudyDescription),
		Modality:                getString(file, dicomtags.Modality),
		AccessionNumber:         getString(file, dicomtags.AccessionNumber),
		SeriesDescription:       getString(file, dicomtags.SeriesDescription),
		ProtocolName:            getString(file, core.NewTag(0x0018, 0x1030)),
		StudyInstanceUID:        getString(file, dicomtags.StudyInstanceUID),
		SeriesInstanceUID:       getString(file, dicomtags.SeriesInstanceUID),
		SeriesNumber:            getString(file, dicomtags.SeriesNumber),
		SOPClassUID:             getString(file, dicomtags.SOPClassUID),
		SOPInstanceUID:          getString(file, dicomtags.SOPInstanceUID),
		InstanceNumber:          getString(file, dicomtags.InstanceNumber),
		StudyID:                 getString(file, dicomtags.StudyID),
		BodyPartExamined:        getString(file, dicomtags.BodyPartExamined),
		ReferringPhysicianName:  getString(file, dicomtags.ReferringPhysicianName),
		PerformingPhysicianName: getString(file, dicomtags.PerformingPhysicianName),
	}

	visited := 0
	rows, err := summarizeObjectContext(ctx, SourceMeta, file.Meta, "", 0, opts, &visited)
	if err != nil {
		return failureSummary(fileName, err)
	}
	summary.Elements = append(summary.Elements, rows...)
	rows, err = summarizeObjectContext(ctx, SourceDataset, file.Dataset, "", 0, opts, &visited)
	if err != nil {
		return failureSummary(fileName, err)
	}
	summary.Elements = append(summary.Elements, rows...)
	summary.ElementCount = len(summary.Elements)

	if err := appendConformanceFindings(ctx, file, opts, &summary); err != nil {
		operational, wrapped := failureSummary(fileName, err)
		appendOperationalFinding(&summary, operational.Findings[0], opts.MaxFindings)
		return summary, wrapped
	}
	return summary, nil
}

func readOptions(opts Options) object.ReadFileOptions {
	return object.ReadFileOptions{
		MaxTotalBytes:      opts.MaxTotalBytes,
		MaxElementBytes:    opts.MaxElementBytes,
		MaxElements:        opts.MaxElements,
		MaxSequenceDepth:   opts.MaxSequenceDepth,
		SkipPixelData:      true,
		Dictionary:         std.Dictionary,
		FileMetaDictionary: std.Dictionary,
	}
}

func validateTraversal(ctx context.Context, file *object.File, opts Options) error {
	visited := 0
	var totalBytes int64
	for _, obj := range []*object.Object{file.Meta, file.Dataset} {
		if obj == nil || len(obj.Elements()) == 0 {
			continue
		}
		err := obj.WalkContext(ctx, object.WalkOptions{
			MaxDepth: opts.MaxSequenceDepth, MaxElements: opts.MaxElements,
		}, func(_ []core.Tag, element core.Element) error {
			visited++
			if visited > opts.MaxElements {
				return parser.ErrMaxElementsExceeded
			}
			size, err := boundedElementBytes(element, opts.MaxElementBytes)
			if err != nil {
				return err
			}
			// Account for a conservative maximum explicit-VR header as well as
			// the retained value. This makes the in-memory seam bounded even
			// though it has no source stream whose byte position can be checked.
			if size > opts.MaxTotalBytes-totalBytes-12 {
				return parser.ErrMaxTotalBytesExceeded
			}
			totalBytes += size + 12
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func boundedElementBytes(element core.Element, max int64) (int64, error) {
	var size int64
	if element.Value != nil {
		if length, ok := element.Value.EncodedLength(); ok {
			size = int64(length)
		} else if fragments, ok := element.Value.(core.FragmentSequence); ok {
			size = int64(len(fragments.OffsetTable))
			for _, fragment := range fragments.Fragments {
				if int64(len(fragment)) > max-size {
					return 0, parser.ErrMaxElementBytesExceeded
				}
				size += int64(len(fragment))
			}
		}
	} else if length, ok := element.Header.Length.Value(); ok {
		size = int64(length)
	}
	if size > max {
		return 0, parser.ErrMaxElementBytesExceeded
	}
	return size, nil
}

func summarizeObjectContext(ctx context.Context, source Source, obj *object.Object, prefix string, depth int, opts Options, visited *int) ([]ElementSummary, error) {
	if obj == nil {
		return nil, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if depth > opts.MaxSequenceDepth {
		return nil, fmt.Errorf("%w: sequence depth exceeds configured maximum", object.ErrWalkResourceLimit)
	}
	elements := obj.SortedElements()
	rows := object.SummarizeElements(obj, object.SummaryOptions{Dictionary: std.Dictionary, Source: string(source)})
	result := make([]ElementSummary, 0, len(rows))
	for i, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		*visited = *visited + 1
		if *visited > opts.MaxElements {
			return nil, fmt.Errorf("%w: element count exceeds configured maximum", object.ErrWalkResourceLimit)
		}
		path := joinPath(prefix, row.TagString())
		result = append(result, ElementSummary{
			Source:  source,
			Path:    path,
			Tag:     row.TagString(),
			VR:      row.VRString(),
			Keyword: row.Keyword,
			Name:    row.Name,
			Length:  row.LengthString(),
			Value:   row.Value,
			Private: row.Private,
		})
		if i >= len(elements) {
			continue
		}
		sequence, ok := elements[i].Value.(core.SequenceValue)
		if !ok {
			continue
		}
		for itemIndex, item := range sequence.Items {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			child := object.FromElements(item.Elements, std.Dictionary)
			child.SetValueByteOrder(obj.ValueByteOrder())
			children, err := summarizeObjectContext(ctx, source, child, fmt.Sprintf("%s[%d]", path, itemIndex), depth+1, opts, visited)
			if err != nil {
				return nil, err
			}
			result = append(result, children...)
		}
	}
	return result, nil
}

func appendConformanceFindings(ctx context.Context, file *object.File, opts Options, summary *Summary) error {
	validationOpts := validation.Options{
		Mode:           validation.ModePreserve,
		Dictionary:     std.Dictionary,
		MaxFindings:    opts.MaxFindings,
		MaxDepth:       opts.MaxSequenceDepth,
		MaxElements:    opts.MaxElements,
		ByteOrder:      file.TransferSyntax.ByteOrder,
		TransferSyntax: file.TransferSyntax,
	}
	if file.Meta != nil {
		result, err := file.Meta.ValidateDataSet(ctx, validationOpts)
		appendValidationReport(summary, result.Report, SourceMeta, opts.MaxFindings)
		if err != nil {
			return err
		}
	}
	validationOpts.RequiredUIDs = []core.Tag{dicomtags.SOPClassUID, dicomtags.SOPInstanceUID}
	result, err := file.Dataset.ValidateDataSet(ctx, validationOpts)
	appendValidationReport(summary, result.Report, SourceDataset, opts.MaxFindings)
	if err != nil {
		return err
	}
	if file.Meta != nil {
		report := validation.ValidateFileMetaConsistency(file.Meta.ToDataSet(), file.Dataset.ToDataSet(), file.TransferSyntax)
		appendValidationReport(summary, report, SourceMeta, opts.MaxFindings)
	}
	return nil
}

func appendValidationReport(summary *Summary, report validation.Report, source Source, max int) {
	for _, finding := range report.Findings {
		if len(summary.Findings) >= max {
			summary.FindingsTruncated = true
			summary.DroppedFindings++
			continue
		}
		tag := ""
		if finding.Tag != (core.Tag{}) {
			tag = finding.Tag.String()
		}
		summary.Findings = append(summary.Findings, Finding{
			Severity: finding.Severity,
			Source:   source,
			Tag:      tag,
			Path:     finding.Path.String(),
			Code:     string(finding.Code),
			Message:  finding.Message,
		})
	}
	summary.FindingCount = len(summary.Findings)
	summary.FindingsTruncated = summary.FindingsTruncated || report.Truncated
	summary.DroppedFindings += report.Dropped
}

func appendOperationalFinding(summary *Summary, finding Finding, max int) {
	if len(summary.Findings) < max {
		summary.Findings = append(summary.Findings, finding)
	} else {
		// The operational failure is essential to interpreting a partial report.
		// Replace the final conformance finding rather than exceeding the bound.
		summary.Findings[max-1] = finding
		summary.FindingsTruncated = true
		summary.DroppedFindings++
	}
	summary.FindingCount = len(summary.Findings)
}

func failureSummary(fileName string, err error) (Summary, error) {
	finding, classified := classifyFailure(err)
	return Summary{FileName: fileName, Findings: []Finding{finding}, FindingCount: 1},
		fmt.Errorf("inspect DICOM file: %w", classified)
}

func classifyFailure(err error) (Finding, error) {
	finding := Finding{Severity: SeverityError, Source: SourceFile}
	switch {
	case errors.Is(err, context.Canceled):
		finding.Code, finding.Message = CodeCanceled, "inspection was canceled before completion"
		return finding, err
	case errors.Is(err, context.DeadlineExceeded):
		finding.Code, finding.Message = CodeTimeout, "inspection exceeded its time limit"
		return finding, err
	case isResourceLimit(err):
		finding.Code, finding.Message = CodeResourceLimit, "inspection stopped at a configured resource limit"
		return finding, errors.Join(ErrResourceLimit, err)
	default:
		finding.Code, finding.Message = CodeMalformed, "the input could not be parsed as a trustworthy DICOM data set"
		return finding, errors.Join(ErrMalformed, err)
	}
}

func isResourceLimit(err error) bool {
	return errors.Is(err, parser.ErrMaxTotalBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementBytesExceeded) ||
		errors.Is(err, parser.ErrMaxElementsExceeded) ||
		errors.Is(err, parser.ErrMaxDepthExceeded) ||
		errors.Is(err, parser.ErrMaxFragmentsExceeded) ||
		errors.Is(err, parser.ErrMaxPixelDataBytesExceeded) ||
		errors.Is(err, object.ErrWalkResourceLimit) || errors.Is(err, validation.ErrValidationLimit)
}

func payloadKind(syntax transfer.Syntax) string {
	if transfer.IsVideoTransferSyntax(syntax.UID) {
		return "media"
	}
	return "image"
}

func mediaType(syntax transfer.Syntax) string {
	if transfer.IsVideoTransferSyntax(syntax.UID) {
		return "video"
	}
	return ""
}

func normalizeOptions(opts Options) (Options, error) {
	if err := ValidateOptions(opts); err != nil {
		return Options{}, err
	}
	defaults := DefaultOptions()
	if opts.MaxTotalBytes == 0 {
		opts.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if opts.MaxElementBytes == 0 {
		opts.MaxElementBytes = defaults.MaxElementBytes
	}
	if opts.MaxElements == 0 {
		opts.MaxElements = defaults.MaxElements
	}
	if opts.MaxSequenceDepth == 0 {
		opts.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if opts.MaxFindings == 0 {
		opts.MaxFindings = defaults.MaxFindings
	}
	if opts.Timeout == 0 {
		opts.Timeout = defaults.Timeout
	}
	return opts, nil
}

func withTimeout(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, timeout)
}

func getString(file *object.File, tag core.Tag) string {
	value, ok := file.GetString(tag)
	if !ok {
		return ""
	}
	return value
}

func joinPath(prefix, tag string) string {
	if prefix == "" {
		return tag
	}
	return prefix + "/" + tag
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func readerWithContext(ctx context.Context, reader io.Reader) io.Reader {
	if seeker, ok := reader.(io.ReadSeeker); ok {
		return contextReadSeeker{ctx: ctx, source: seeker}
	}
	return contextReader{ctx: ctx, reader: reader}
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(p)
}

type contextReadSeeker struct {
	ctx    context.Context
	source io.ReadSeeker
}

func (r contextReadSeeker) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Read(p)
}

func (r contextReadSeeker) Seek(offset int64, whence int) (int64, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.source.Seek(offset, whence)
}
