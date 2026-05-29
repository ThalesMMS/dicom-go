package dicomweb

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ThalesMMS/dicom-go/dicomjson"
)

const (
	// DefaultTimeout bounds a single DICOMweb request when Options.Timeout is unset.
	DefaultTimeout = 20 * time.Second
	// DefaultMaxBodyBytes bounds response bodies when Options.MaxBodyBytes is unset.
	DefaultMaxBodyBytes = int64(512 << 20)
)

// Endpoint describes a DICOMweb base URL and optional service-specific paths.
type Endpoint struct {
	BaseURL  string
	QIDOPath string
	WADOPath string
	STOWPath string
}

// Options configures HTTP transport, timeouts, response limits, and auth.
type Options struct {
	HTTPClient    *http.Client `json:"-"`
	Timeout       time.Duration
	MaxBodyBytes  int64
	BasicUsername string `json:"-"`
	BasicPassword string `json:"-"`
	BearerToken   string `json:"-"`
	// BearerTokenSource takes precedence over static bearer and basic
	// credentials. Failure to obtain a token fails closed without downgrade.
	BearerTokenSource BearerTokenSource `json:"-"`
}

// String reports configuration shape without exposing credentials.
func (o Options) String() string {
	return fmt.Sprintf(
		"DICOMweb options (custom_http_client=%t, timeout=%s, max_body_bytes=%d, basic_auth=%t, static_bearer=%t, dynamic_bearer=%t)",
		o.HTTPClient != nil,
		o.Timeout,
		o.MaxBodyBytes,
		strings.TrimSpace(o.BasicUsername) != "",
		strings.TrimSpace(o.BearerToken) != "",
		o.BearerTokenSource != nil,
	)
}

// GoString redacts credentials from %#v formatting and crash diagnostics.
func (o Options) GoString() string {
	return o.String()
}

// Client is a neutral DICOMweb client for one endpoint.
type Client struct {
	Endpoint Endpoint
	Options  Options
}

// Dataset is a raw DICOM JSON dataset keyed by uppercase tag hex.
type Dataset = dicomjson.Dataset

// Element is a raw DICOM JSON element.
type Element = dicomjson.Element

// Response captures common HTTP response details.
type Response struct {
	URL        string
	StatusCode int
	Status     string
	Header     http.Header
	Body       []byte
}

// VerifyResult describes a DICOMweb verify request.
type VerifyResult struct {
	Response
	Duration  time.Duration
	StartedAt time.Time
}

// InstanceRef identifies a DICOM instance inside a study and series.
type InstanceRef struct {
	StudyInstanceUID  string
	SeriesInstanceUID string
	SOPInstanceUID    string
}

// RetrieveOptions configures WADO-RS object retrieval.
type RetrieveOptions struct {
	// TransferSyntaxUIDs lists DICOM transfer syntax preferences for the WADO-RS
	// Accept header. Use "*" to request the server's default/preserved transfer
	// syntax as a fallback.
	TransferSyntaxUIDs []string
}

// ObjectPart is one WADO-RS DICOM object payload.
type ObjectPart struct {
	ContentType       string
	TransferSyntaxUID string
	Data              []byte
}

// ObjectPartStream is one WADO-RS DICOM object payload streamed from the HTTP response.
type ObjectPartStream struct {
	ContentType       string
	TransferSyntaxUID string
	Reader            io.Reader
}

// LocatedObjectPartStream is one streamed object plus its same-origin
// Content-Location. Study and series retrieval use this type so callers can
// correlate each representation without reparsing its Part 10 payload.
type LocatedObjectPartStream struct {
	Part            ObjectPartStream
	ContentLocation string
}

// FramePart is one WADO-RS RetrieveFrames payload. FrameNumber is populated in
// request order because DICOMweb returns one MIME part per requested frame.
type FramePart struct {
	FrameNumber       int
	ContentType       string
	TransferSyntaxUID string
	Data              []byte
}

// StoreInstance is one DICOM object to include in a STOW-RS upload.
type StoreInstance struct {
	SOPClassUID    string
	SOPInstanceUID string
	Path           string
	// Data is the in-memory DICOM Part 10 payload. Prefer Reader or Open for
	// large objects so STOW-RS can stream the multipart body.
	Data []byte
	// Reader streams the DICOM Part 10 payload for this instance. It is used
	// when Open is nil and Data is empty.
	Reader io.Reader
	// Open returns a fresh DICOM Part 10 payload reader. It is preferred over
	// Reader so callers can open files lazily during multipart streaming.
	Open func() (io.ReadCloser, error)
}

// StoreItem describes one SOP item reported in a STOW-RS response.
type StoreItem struct {
	SOPClassUID    string
	SOPInstanceUID string
	RetrieveURL    string
	WarningReason  uint16
	FailureReason  uint16
}

// StoreResult describes a STOW-RS response.
type StoreResult struct {
	Response
	Stored []StoreItem
	Failed []StoreItem
}

// ErrorKind classifies DICOMweb failures.
type ErrorKind string

const (
	ErrorKindInvalidEndpoint ErrorKind = "invalid_endpoint"
	ErrorKindRequestFailure  ErrorKind = "request_failure"
	ErrorKindTimeout         ErrorKind = "timeout"
	ErrorKindHTTPStatus      ErrorKind = "http_status"
	ErrorKindAuthStatus      ErrorKind = "auth_status"
	ErrorKindAuthToken       ErrorKind = "auth_token"
	ErrorKindDecodeResponse  ErrorKind = "decode_response"
)

// Error is a typed DICOMweb error.
type Error struct {
	Kind       ErrorKind
	URL        string
	StatusCode int
	Status     string
	Err        error
}

func (e *Error) Error() string {
	if e == nil {
		return "DICOMweb error"
	}
	target := strings.TrimSpace(e.URL)
	if target == "" {
		target = "endpoint"
	}
	switch e.Kind {
	case ErrorKindHTTPStatus, ErrorKindAuthStatus:
		if e.Status != "" {
			return fmt.Sprintf("DICOMweb %s returned %s", target, e.Status)
		}
		return fmt.Sprintf("DICOMweb %s returned HTTP %d", target, e.StatusCode)
	case ErrorKindAuthToken:
		return fmt.Sprintf("DICOMweb authentication for %s could not obtain a bearer token", target)
	case ErrorKindTimeout:
		return fmt.Sprintf("DICOMweb %s timed out", target)
	case ErrorKindInvalidEndpoint:
		if e.Err != nil {
			return fmt.Sprintf("invalid DICOMweb endpoint: %v", e.Err)
		}
		return "invalid DICOMweb endpoint"
	case ErrorKindDecodeResponse:
		if e.Err != nil {
			return fmt.Sprintf("decode DICOMweb response from %s: %v", target, e.Err)
		}
		return fmt.Sprintf("decode DICOMweb response from %s", target)
	default:
		if e.Err != nil {
			return fmt.Sprintf("DICOMweb request to %s failed: %v", target, e.Err)
		}
		return fmt.Sprintf("DICOMweb request to %s failed", target)
	}
}

// Unwrap returns the underlying error.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
