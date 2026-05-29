package dicomweb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultSpoolRetentionAge        = 24 * time.Hour
	defaultSpoolAggregateQuotaBytes = int64(4 << 30)
)

var (
	// ErrInvalidServerOptions reports an invalid or incomplete server configuration.
	ErrInvalidServerOptions = errors.New("dicomweb: invalid server options")
	// ErrInvalidRequest reports a malformed DICOMweb request.
	ErrInvalidRequest = errors.New("dicomweb: invalid request")
	// ErrUnauthorized reports that authentication is required or failed.
	ErrUnauthorized = errors.New("dicomweb: unauthorized")
	// ErrForbidden reports that the authenticated caller is not authorized.
	ErrForbidden = errors.New("dicomweb: forbidden")
	// ErrNotFound reports that a requested DICOM resource does not exist.
	ErrNotFound = errors.New("dicomweb: resource not found")
	// ErrConflict reports a store or resource identity conflict.
	ErrConflict = errors.New("dicomweb: conflict")
	// ErrUnsupported reports an unsupported resource, media type, or operation.
	ErrUnsupported = errors.New("dicomweb: unsupported operation")
	// ErrResourceLimit reports a finite server resource limit.
	ErrResourceLimit = errors.New("dicomweb: resource limit exceeded")
	// ErrBackend reports a backend failure whose details must not cross the HTTP boundary.
	ErrBackend = errors.New("dicomweb: backend failure")
)

// Operation is a PHI-free authorization and audit operation identifier.
type Operation string

const (
	OperationSearchStudies    Operation = "search_studies"
	OperationSearchSeries     Operation = "search_series"
	OperationSearchInstances  Operation = "search_instances"
	OperationRetrieveStudy    Operation = "retrieve_study"
	OperationRetrieveSeries   Operation = "retrieve_series"
	OperationRetrieveInstance Operation = "retrieve_instance"
	OperationRetrieveMetadata Operation = "retrieve_metadata"
	OperationRetrieveFrames   Operation = "retrieve_frames"
	OperationRetrieveBulkData Operation = "retrieve_bulk_data"
	OperationStoreInstances   Operation = "store_instances"
	OperationUnknown          Operation = "unknown"
)

// AuditEvent contains only closed-schema operational metadata. It deliberately
// omits UIDs, paths, query values, DICOM attributes, remote error text, and
// authorization details.
type AuditEvent struct {
	Operation  Operation     `json:"operation"`
	RequestID  string        `json:"request_id,omitempty"`
	StatusCode int           `json:"status_code"`
	ItemCount  int           `json:"item_count,omitempty"`
	Duration   time.Duration `json:"duration"`
	ErrorCode  string        `json:"error_code,omitempty"`
}

// Authorizer authorizes one request after routing but before backend work.
// Implementations may inspect the request, including route UIDs and query
// parameters, but should not retain them in logs. Return ErrUnauthorized or
// ErrForbidden to select the corresponding HTTP status.
type Authorizer func(context.Context, *http.Request, Operation) error

// ServerLimits bounds parsing, buffering, concurrency, and response work.
// Zero fields receive finite defaults from DefaultServerLimits.
type ServerLimits struct {
	MaxRequestBytes       int64
	MaxPartBytes          int64
	MaxDecodedPartBytes   int64
	MaxResponseBytes      int64
	MaxRequestURIBytes    int
	MaxHeaderBytes        int
	MaxPartHeaderBytes    int
	MaxQueryValueBytes    int
	MaxUIDBytes           int
	MaxJSONValueBytes     int
	MaxJSONValues         int
	MaxJSONDepth          int
	MaxParts              int
	MaxQueryFields        int
	MaxResults            int
	MaxOffset             int
	MaxFrames             int
	MaxConcurrentRequests int
	MaxDuration           time.Duration
}

// DefaultServerLimits returns conservative finite limits suitable for an
// embedded service. Applications should tune these to their deployment.
func DefaultServerLimits() ServerLimits {
	return ServerLimits{
		MaxRequestBytes:       2 << 30,
		MaxPartBytes:          1 << 30,
		MaxDecodedPartBytes:   2 << 30,
		MaxResponseBytes:      64 << 20,
		MaxRequestURIBytes:    16 << 10,
		MaxHeaderBytes:        1 << 20,
		MaxPartHeaderBytes:    64 << 10,
		MaxQueryValueBytes:    8 << 10,
		MaxUIDBytes:           64,
		MaxJSONValueBytes:     64 << 20,
		MaxJSONValues:         1_000_000,
		MaxJSONDepth:          64,
		MaxParts:              10_000,
		MaxQueryFields:        256,
		MaxResults:            100_000,
		MaxOffset:             10_000_000,
		MaxFrames:             10_000,
		MaxConcurrentRequests: 64,
		MaxDuration:           5 * time.Minute,
	}
}

// HTTPServerOptions configures the optional net/http server owned by Server.
type HTTPServerOptions struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
}

// ServerOptions configures an embeddable DICOMweb handler.
type ServerOptions struct {
	Backend any
	Limits  ServerLimits
	// ServiceRoot is the externally visible mount path (for example,
	// "/dicomweb"). It is used for same-origin URLs and Warning headers. Mount
	// the handler with http.StripPrefix when it is non-empty.
	ServiceRoot string

	// Authorize is called before any backend work. When it is nil, requests are
	// rejected unless AllowUnauthenticated is explicitly true.
	Authorize            Authorizer
	AllowUnauthenticated bool

	// Middleware may add deployment-specific authentication, CORS, tracing, or
	// telemetry around the bounded core handler. No CORS policy is enabled by
	// default.
	Middleware func(http.Handler) http.Handler
	RequestID  func(*http.Request) string
	Audit      func(context.Context, AuditEvent)

	StorePolicy StorePolicy
	// SpoolDirectory selects temporary storage for bounded STOW request staging
	// and QIDO/metadata response spooling. Empty uses the operating system
	// temporary directory. These 0600 files may contain PHI and the deployment
	// must place them on appropriately protected storage.
	SpoolDirectory string
	// SpoolRetentionAge removes inactive server-owned spool files older than
	// this age when NewServer starts. Zero uses a finite default.
	SpoolRetentionAge time.Duration
	// SpoolAggregateQuotaBytes removes the oldest inactive server-owned spool
	// files until aggregate usage is at or below this value. Zero uses a finite
	// default. Active and unrelated entries are never removed.
	SpoolAggregateQuotaBytes int64
	HTTP                     HTTPServerOptions
}

// SearchLevel selects a PS3.18 QIDO-RS resource.
type SearchLevel string

const (
	SearchLevelStudy    SearchLevel = "study"
	SearchLevelSeries   SearchLevel = "series"
	SearchLevelInstance SearchLevel = "instance"
)

// SearchFilter is one DICOM tag or keyword match parameter. Values preserve
// request order and multiplicity for backend-specific matching semantics.
type SearchFilter struct {
	Key    string
	Values []string
}

// SearchRequest is a validated, bounded QIDO-RS query.
type SearchRequest struct {
	Level             SearchLevel
	StudyInstanceUID  string
	SeriesInstanceUID string
	Filters           []SearchFilter
	IncludeFields     []string
	Limit             int
	LimitSet          bool
	// MaximumResults is the finite effective response cap after combining the
	// request limit with ServerLimits.MaxResults.
	MaximumResults        int
	Offset                int
	FuzzyMatching         bool
	EmptyValueMatching    bool
	MultipleValueMatching bool
}

// SearchResult describes server-side pagination and optional matching support.
// Remaining is nil when no pagination warning is needed. A backend that reaches
// MaximumResults must return the exact remaining count; the handler emits the
// PS3.18 Warning 299 response header when it is non-zero.
type SearchResult struct {
	Remaining                    *int
	FuzzyMatchingApplied         bool
	EmptyValueMatchingApplied    bool
	MultipleValueMatchingApplied bool
}

// SearchBackend streams DICOM JSON result datasets synchronously through yield.
type SearchBackend interface {
	Search(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error)
}

// MetadataLevel selects a WADO-RS metadata resource.
type MetadataLevel string

const (
	MetadataLevelStudy    MetadataLevel = "study"
	MetadataLevelSeries   MetadataLevel = "series"
	MetadataLevelInstance MetadataLevel = "instance"
)

// MetadataRequest identifies a WADO-RS metadata resource.
type MetadataRequest struct {
	Level             MetadataLevel
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPInstanceUID    string
}

// MetadataBackend streams DICOM JSON metadata datasets synchronously.
type MetadataBackend interface {
	Metadata(context.Context, MetadataRequest, func(Dataset) error) error
}

// RetrieveLevel selects a WADO-RS object resource.
type RetrieveLevel string

const (
	RetrieveLevelStudy    RetrieveLevel = "study"
	RetrieveLevelSeries   RetrieveLevel = "series"
	RetrieveLevelInstance RetrieveLevel = "instance"
)

// MediaPreference is one normalized Accept alternative passed to a backend.
type MediaPreference struct {
	MediaType         string
	TransferSyntaxUID string
	Quality           float64
	Multipart         bool
}

// RetrieveRequest identifies objects and the request's ordered media preferences.
type RetrieveRequest struct {
	Level             RetrieveLevel
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPInstanceUID    string
	Accept            []MediaPreference
}

// RetrievePart is one synchronous WADO-RS object stream. The server always
// closes Reader after yield returns. Reader must remain valid until then.
type RetrievePart struct {
	Ref               InstanceRef
	ContentType       string
	TransferSyntaxUID string
	Reader            io.ReadCloser
	Size              int64
}

// RetrieveBackend streams Part 10 objects synchronously without whole-study buffering.
type RetrieveBackend interface {
	Retrieve(context.Context, RetrieveRequest, func(RetrievePart) error) error
}

// FrameRequest identifies one-based frames and ordered media preferences.
type FrameRequest struct {
	Ref    InstanceRef
	Frames []int
	Accept []MediaPreference
}

// FramePartStream is one synchronous frame payload. If Reader implements
// io.Closer, the server closes it after yield returns.
type FramePartStream struct {
	FrameNumber       int
	ContentType       string
	TransferSyntaxUID string
	Reader            io.Reader
	Size              int64
}

// FrameBackend streams requested frame payloads in request order.
type FrameBackend interface {
	RetrieveFrames(context.Context, FrameRequest, func(FramePartStream) error) error
}

// BulkDataRequest contains an opaque, backend-defined token from an authorized
// same-origin BulkDataURI. The core server never creates public BulkDataURIs.
type BulkDataRequest struct {
	Token  string
	Accept []MediaPreference
}

// BulkDataPart is one bulk data stream owned and closed by the server.
type BulkDataPart struct {
	ContentType string
	Reader      io.ReadCloser
	Size        int64
}

// BulkDataBackend resolves authorized same-origin bulk data tokens.
type BulkDataBackend interface {
	RetrieveBulkData(context.Context, BulkDataRequest) (BulkDataPart, error)
}

// StorePolicy controls duplicate SOP Instance handling. Atomic identity,
// duplicate, and conflict resolution belongs inside StoreBackend.Store.
type StorePolicy string

const (
	StorePolicyRejectDuplicate StorePolicy = "reject_duplicate"
	StorePolicyIdempotent      StorePolicy = "idempotent"
)

// StoreStatus is one backend-selected atomic storage disposition.
type StoreStatus string

const (
	StoreStatusStored    StoreStatus = "stored"
	StoreStatusWarning   StoreStatus = "warning"
	StoreStatusDuplicate StoreStatus = "duplicate"
	StoreStatusConflict  StoreStatus = "conflict"
	StoreStatusFailed    StoreStatus = "failed"
)

// StoreRequest is one bounded multipart part. Store must consume Reader before
// returning and atomically decide duplicate/conflict policy before commit.
type StoreRequest struct {
	Index             int
	RouteStudyUID     string
	Ref               InstanceRef
	SOPClassUID       string
	ContentType       string
	TransferSyntaxUID string
	Policy            StorePolicy
	Reader            io.Reader
	Size              int64
	SHA256            [32]byte
}

// StoreOutcome is one STOW-RS instance disposition. Err is never exposed to
// HTTP clients or audit hooks.
type StoreOutcome struct {
	Status        StoreStatus
	Ref           InstanceRef
	SOPClassUID   string
	WarningReason uint16
	FailureReason uint16
	Err           error
}

// StoreBackend stores one multipart part synchronously and atomically.
type StoreBackend interface {
	Store(context.Context, StoreRequest) StoreOutcome
}

// Existence describes whether a SOP Instance identity is present. Content
// equality and conflicts are decided atomically by StoreBackend.Store, which
// also receives the staged SHA-256 digest.
type Existence uint8

const (
	ExistenceAbsent Existence = iota
	ExistencePresent
)

// ExistenceBackend supports planning and diagnostics. StoreBackend.Store must
// still perform its own atomic duplicate/conflict decision.
type ExistenceBackend interface {
	Exists(context.Context, InstanceRef) (Existence, error)
}

func normalizeServerLimits(limits ServerLimits) (ServerLimits, error) {
	defaults := DefaultServerLimits()
	if limits.MaxRequestBytes < 0 || limits.MaxPartBytes < 0 || limits.MaxDecodedPartBytes < 0 || limits.MaxResponseBytes < 0 ||
		limits.MaxRequestURIBytes < 0 || limits.MaxHeaderBytes < 0 || limits.MaxPartHeaderBytes < 0 || limits.MaxQueryValueBytes < 0 || limits.MaxUIDBytes < 0 || limits.MaxJSONValueBytes < 0 || limits.MaxJSONValues < 0 || limits.MaxJSONDepth < 0 || limits.MaxParts < 0 || limits.MaxQueryFields < 0 ||
		limits.MaxResults < 0 || limits.MaxOffset < 0 || limits.MaxFrames < 0 || limits.MaxConcurrentRequests < 0 || limits.MaxDuration < 0 {
		return ServerLimits{}, ErrInvalidServerOptions
	}
	if limits.MaxRequestBytes == 0 {
		limits.MaxRequestBytes = defaults.MaxRequestBytes
	}
	if limits.MaxPartBytes == 0 {
		limits.MaxPartBytes = defaults.MaxPartBytes
	}
	if limits.MaxDecodedPartBytes == 0 {
		limits.MaxDecodedPartBytes = defaults.MaxDecodedPartBytes
	}
	if limits.MaxResponseBytes == 0 {
		limits.MaxResponseBytes = defaults.MaxResponseBytes
	}
	if limits.MaxRequestURIBytes == 0 {
		limits.MaxRequestURIBytes = defaults.MaxRequestURIBytes
	}
	if limits.MaxHeaderBytes == 0 {
		limits.MaxHeaderBytes = defaults.MaxHeaderBytes
	}
	if limits.MaxPartHeaderBytes == 0 {
		limits.MaxPartHeaderBytes = defaults.MaxPartHeaderBytes
	}
	if limits.MaxQueryValueBytes == 0 {
		limits.MaxQueryValueBytes = defaults.MaxQueryValueBytes
	}
	if limits.MaxUIDBytes == 0 {
		limits.MaxUIDBytes = defaults.MaxUIDBytes
	}
	if limits.MaxJSONValueBytes == 0 {
		limits.MaxJSONValueBytes = defaults.MaxJSONValueBytes
	}
	if limits.MaxJSONValues == 0 {
		limits.MaxJSONValues = defaults.MaxJSONValues
	}
	if limits.MaxJSONDepth == 0 {
		limits.MaxJSONDepth = defaults.MaxJSONDepth
	}
	if limits.MaxParts == 0 {
		limits.MaxParts = defaults.MaxParts
	}
	if limits.MaxQueryFields == 0 {
		limits.MaxQueryFields = defaults.MaxQueryFields
	}
	if limits.MaxResults == 0 {
		limits.MaxResults = defaults.MaxResults
	}
	if limits.MaxOffset == 0 {
		limits.MaxOffset = defaults.MaxOffset
	}
	if limits.MaxFrames == 0 {
		limits.MaxFrames = defaults.MaxFrames
	}
	if limits.MaxConcurrentRequests == 0 {
		limits.MaxConcurrentRequests = defaults.MaxConcurrentRequests
	}
	if limits.MaxDuration == 0 {
		limits.MaxDuration = defaults.MaxDuration
	}
	if limits.MaxPartBytes > limits.MaxRequestBytes {
		limits.MaxPartBytes = limits.MaxRequestBytes
	}
	return limits, nil
}

func normalizeStorePolicy(policy StorePolicy) (StorePolicy, error) {
	if strings.TrimSpace(string(policy)) == "" {
		return StorePolicyRejectDuplicate, nil
	}
	switch policy {
	case StorePolicyRejectDuplicate, StorePolicyIdempotent:
		return policy, nil
	default:
		return "", ErrInvalidServerOptions
	}
}
