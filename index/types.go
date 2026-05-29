// Package index extracts a bounded, detached metadata record from DICOM
// Part 10 files and explicitly configured raw data sets without materializing
// bulk payloads.
package index

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/parser"
	"github.com/ThalesMMS/dicom-go/transfer"
	"github.com/ThalesMMS/dicom-go/validation"
)

const (
	DefaultMaxTotalBytes     int64 = 512 << 20
	DefaultMaxSelectedBytes  int64 = 1 << 20
	DefaultMaxElements             = 200_000
	DefaultMaxSequenceDepth        = 64
	DefaultMaxTokens               = 1_000_000
	DefaultMaxFragments            = 200_000
	DefaultMaxSelectedValues       = 4096
	DefaultMaxDiagnostics          = 256
)

var (
	ErrInvalidOptions = errors.New("dicom index options are invalid")
	ErrRead           = errors.New("dicom index read failed")
	ErrClose          = errors.New("dicom index source close failed")
	ErrResourceLimit  = errors.New("dicom index resource limit exceeded")
	ErrSelector       = errors.New("dicom index selector failed")
)

// Profile selects built-in record fields. The zero value means ProfileCore;
// Patient and descriptive fields that can contain PHI require explicit opt-in.
type Profile uint32

const (
	ProfileCore Profile = 1 << iota
	ProfilePatient
	ProfileDescriptions
	ProfileGeometry
	// ProfileNone suppresses every built-in dataset field while retaining File
	// Meta and explicitly configured Selectors.
	ProfileNone Profile = 1 << 31

	ProfileAll = ProfileCore | ProfilePatient | ProfileDescriptions | ProfileGeometry
)

// SourceInfo identifies the input without making its origin part of errors or
// diagnostics. Origin can contain PHI and must not be logged implicitly.
type SourceInfo struct {
	Origin string `json:"origin,omitempty"`
	Size   int64  `json:"size"`
}

type Patient struct {
	Name      string `json:"name,omitempty"`
	ID        string `json:"id,omitempty"`
	BirthDate string `json:"birth_date,omitempty"`
	Sex       string `json:"sex,omitempty"`
}

type Study struct {
	InstanceUID     string `json:"instance_uid,omitempty"`
	AccessionNumber string `json:"accession_number,omitempty"`
	Date            string `json:"date,omitempty"`
	Time            string `json:"time,omitempty"`
	Description     string `json:"description,omitempty"`
}

type Series struct {
	InstanceUID string `json:"instance_uid,omitempty"`
	Modality    string `json:"modality,omitempty"`
	Number      string `json:"number,omitempty"`
	Date        string `json:"date,omitempty"`
	Time        string `json:"time,omitempty"`
	Description string `json:"description,omitempty"`
}

type Instance struct {
	SOPClassUID    string `json:"sop_class_uid,omitempty"`
	SOPInstanceUID string `json:"sop_instance_uid,omitempty"`
	Number         string `json:"number,omitempty"`
	NumberOfFrames int64  `json:"number_of_frames,omitempty"`
}

type Geometry struct {
	Rows                    int64     `json:"rows,omitempty"`
	Columns                 int64     `json:"columns,omitempty"`
	PixelSpacing            []float64 `json:"pixel_spacing,omitempty"`
	ImagePositionPatient    []float64 `json:"image_position_patient,omitempty"`
	ImageOrientationPatient []float64 `json:"image_orientation_patient,omitempty"`
	SliceThickness          float64   `json:"slice_thickness,omitempty"`
}

type FileMeta struct {
	TransferSyntaxUID          string `json:"transfer_syntax_uid,omitempty"`
	TransferSyntaxName         string `json:"transfer_syntax_name,omitempty"`
	MediaStorageSOPClassUID    string `json:"media_storage_sop_class_uid,omitempty"`
	MediaStorageSOPInstanceUID string `json:"media_storage_sop_instance_uid,omitempty"`
}

type Record struct {
	Source      SourceInfo                   `json:"source"`
	FileMeta    FileMeta                     `json:"file_meta"`
	Patient     *Patient                     `json:"patient,omitempty"`
	Study       Study                        `json:"study"`
	Series      Series                       `json:"series"`
	Instance    Instance                     `json:"instance"`
	Geometry    *Geometry                    `json:"geometry,omitempty"`
	Selected    map[string][]SelectedElement `json:"-"`
	Missing     []MissingField               `json:"missing,omitempty"`
	Diagnostics []Diagnostic                 `json:"diagnostics,omitempty"`
}

type Result struct {
	Record Record `json:"record"`
	// BytesScanned is the logical source position reached by the metadata
	// reader. Seek-skipped values count toward it; it is not a physical I/O
	// byte counter. For deflated input it measures inflated DICOM bytes.
	BytesScanned           int64 `json:"bytes_scanned"`
	StoppedBeforePixelData bool  `json:"stopped_before_pixel_data,omitempty"`
	// StoppedHeader is the value-free top-level Pixel Data header that caused
	// the metadata scan to stop.
	StoppedHeader    core.ElementHeader `json:"-"`
	StoppedHeaderSet bool               `json:"stopped_header_set,omitempty"`
	RawDataSet       bool               `json:"raw_dataset,omitempty"`
}

type Severity string

const (
	SeverityInfo    Severity = "info"
	SeverityWarning Severity = "warning"
	SeverityError   Severity = "error"
)

type DiagnosticCode string

const (
	CodeRawDataSet   DiagnosticCode = "raw_dataset"
	CodeMissingField DiagnosticCode = "missing_field"
	CodeInvalidValue DiagnosticCode = "invalid_value"
	CodeLimit        DiagnosticCode = "resource_limit"
)

// Diagnostic is deliberately value- and path-free. OffsetSet distinguishes a
// real offset zero from an unavailable offset.
type Diagnostic struct {
	Code      DiagnosticCode `json:"code"`
	Severity  Severity       `json:"severity"`
	Field     string         `json:"field,omitempty"`
	Tag       core.Tag       `json:"tag"`
	Offset    int64          `json:"offset,omitempty"`
	OffsetSet bool           `json:"offset_set,omitempty"`
	Message   string         `json:"message"`
}

type MissingField struct {
	Field string   `json:"field"`
	Tag   core.Tag `json:"tag"`
}

type Occurrence uint8

const (
	OccurrenceLast Occurrence = iota
	OccurrenceFirst
	OccurrenceAll
)

// SelectedElement is detached from the parser. Element values may contain PHI
// because the caller explicitly requested them.
type SelectedElement struct {
	Path    validation.Path
	Offset  int64
	Element core.Element
}

type SelectorHandler func(context.Context, SelectedElement) error

// Selector selects one primitive tag beneath an exact sequence-tag path. Item
// indexes in parsed paths are ignored, so a selector can match every item.
type Selector struct {
	ID            string
	Path          []core.Tag
	Tag           core.Tag
	Occurrence    Occurrence
	MaxValues     int
	MaxValueBytes int64
	Handle        SelectorHandler
}

type Limits struct {
	MaxTotalBytes         int64
	MaxSelectedValueBytes int64
	MaxElements           int
	MaxSequenceDepth      int
	MaxTokens             int
	MaxFragments          int
	MaxSelectedValues     int
	MaxDiagnostics        int
}

type RawDataSetOptions struct {
	// TransferSyntax is required. When RawDataSet is non-nil, Read treats the
	// source as a raw dataset from its first byte; it does not auto-detect or
	// fall back between raw and Part 10 interpretations. Only UID is
	// authoritative; the registered canonical syntax supplies encoding fields.
	TransferSyntax transfer.Syntax
}

type Options struct {
	Profile                            Profile
	Selectors                          []Selector
	Dictionary                         dictionary.DataDictionary
	FileMetaDictionary                 dictionary.DataDictionary
	TextOptions                        object.TextOptions
	Limits                             Limits
	RawDataSet                         *RawDataSetOptions
	StrictReservedBytes                bool
	OddLengthPolicy                    parser.OddLengthPolicy
	AllowMissingMetaElementGroupLength bool
}

func DefaultOptions() Options {
	return Options{
		Profile: ProfileCore,
		Limits: Limits{
			MaxTotalBytes:         DefaultMaxTotalBytes,
			MaxSelectedValueBytes: DefaultMaxSelectedBytes,
			MaxElements:           DefaultMaxElements,
			MaxSequenceDepth:      DefaultMaxSequenceDepth,
			MaxTokens:             DefaultMaxTokens,
			MaxFragments:          DefaultMaxFragments,
			MaxSelectedValues:     DefaultMaxSelectedValues,
			MaxDiagnostics:        DefaultMaxDiagnostics,
		},
	}
}

type Error struct {
	Stage     string
	Tag       core.Tag
	Offset    int64
	OffsetSet bool
	Code      DiagnosticCode
	err       error
}

func (e *Error) Error() string {
	if e == nil {
		return "dicom index read failed"
	}
	stage := strings.TrimSpace(e.Stage)
	if stage == "" {
		stage = "input"
	}
	return fmt.Sprintf("dicom index %s failed", stage)
}

func (e *Error) Unwrap() error {
	if e == nil || e.err == nil {
		return ErrRead
	}
	return errors.Join(ErrRead, redactedErrorCause{cause: e.err})
}

// redactedErrorCause preserves errors.Is classification without exposing a
// wrapped error through errors.As or a nested Error string. It is used at API
// boundaries where the original error may carry a path, UID, callback value,
// or panic-derived text.
type redactedErrorCause struct{ cause error }

func (e redactedErrorCause) Error() string { return "dicom index classified failure" }
func (e redactedErrorCause) Is(target error) bool {
	return e.cause != nil && errors.Is(e.cause, target)
}

var selectorIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

func normalizeOptions(options Options) (Options, error) {
	defaults := DefaultOptions()
	options.Selectors = append([]Selector(nil), options.Selectors...)
	if options.Profile == 0 {
		options.Profile = defaults.Profile
	}
	if options.Profile&^(ProfileAll|ProfileNone) != 0 || options.Profile&ProfileNone != 0 && options.Profile != ProfileNone {
		return Options{}, fmt.Errorf("%w: unknown profile bits", ErrInvalidOptions)
	}
	if options.Dictionary == nil {
		options.Dictionary = std.Dictionary
	}
	if options.FileMetaDictionary == nil {
		options.FileMetaDictionary = std.Dictionary
	}
	if err := normalizeLimits(&options.Limits, defaults.Limits); err != nil {
		return Options{}, err
	}
	if options.RawDataSet != nil {
		configured := options.RawDataSet.TransferSyntax
		syntax, ok := transfer.DefaultRegistry.Get(configured.UID)
		if !ok || !syntax.Supported || syntax.ByteOrder == nil {
			return Options{}, fmt.Errorf("%w: raw transfer syntax UID must identify a registered supported syntax", ErrInvalidOptions)
		}
		options.RawDataSet = &RawDataSetOptions{TransferSyntax: syntax}
	}
	seen := make(map[string]struct{}, len(options.Selectors))
	for i := range options.Selectors {
		selector := &options.Selectors[i]
		selector.ID = strings.TrimSpace(selector.ID)
		if !selectorIDPattern.MatchString(selector.ID) || selector.Occurrence > OccurrenceAll {
			return Options{}, fmt.Errorf("%w: invalid selector", ErrInvalidOptions)
		}
		if _, ok := seen[selector.ID]; ok {
			return Options{}, fmt.Errorf("%w: duplicate selector ID", ErrInvalidOptions)
		}
		seen[selector.ID] = struct{}{}
		if selector.MaxValues < 0 || selector.MaxValueBytes < 0 {
			return Options{}, fmt.Errorf("%w: negative selector limit", ErrInvalidOptions)
		}
		if selector.MaxValues == 0 {
			if selector.Occurrence == OccurrenceAll {
				selector.MaxValues = options.Limits.MaxSelectedValues
			} else {
				selector.MaxValues = 1
			}
		}
		if selector.MaxValueBytes == 0 {
			selector.MaxValueBytes = options.Limits.MaxSelectedValueBytes
		}
		selector.Path = append([]core.Tag(nil), selector.Path...)
	}
	return options, nil
}

func normalizeLimits(limits *Limits, defaults Limits) error {
	if limits.MaxTotalBytes < 0 || limits.MaxSelectedValueBytes < 0 || limits.MaxElements < 0 ||
		limits.MaxSequenceDepth < 0 || limits.MaxTokens < 0 || limits.MaxFragments < 0 ||
		limits.MaxSelectedValues < 0 || limits.MaxDiagnostics < 0 {
		return fmt.Errorf("%w: negative resource limit", ErrInvalidOptions)
	}
	if limits.MaxTotalBytes == 0 {
		limits.MaxTotalBytes = defaults.MaxTotalBytes
	}
	if limits.MaxSelectedValueBytes == 0 {
		limits.MaxSelectedValueBytes = defaults.MaxSelectedValueBytes
	}
	if limits.MaxElements == 0 {
		limits.MaxElements = defaults.MaxElements
	}
	if limits.MaxSequenceDepth == 0 {
		limits.MaxSequenceDepth = defaults.MaxSequenceDepth
	}
	if limits.MaxTokens == 0 {
		limits.MaxTokens = defaults.MaxTokens
	}
	if limits.MaxFragments == 0 {
		limits.MaxFragments = defaults.MaxFragments
	}
	if limits.MaxSelectedValues == 0 {
		limits.MaxSelectedValues = defaults.MaxSelectedValues
	}
	if limits.MaxDiagnostics == 0 {
		limits.MaxDiagnostics = defaults.MaxDiagnostics
	}
	return nil
}
