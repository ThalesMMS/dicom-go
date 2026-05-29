// Package jpip retrieves and decodes pixel streams referenced by DICOM JPIP
// transfer syntaxes.
package jpip

import (
	"errors"
	"fmt"
)

var (
	ErrInvalidRequest         = errors.New("dicom: invalid JPIP request")
	ErrBearerTokenUnavailable = errors.New("dicom: JPIP bearer token unavailable")
	ErrPolicyDenied           = errors.New("dicom: JPIP endpoint denied by policy")
	ErrRedirectDenied         = errors.New("dicom: JPIP redirect denied by policy")
	ErrOffline                = errors.New("dicom: JPIP endpoint unavailable")
	ErrHTTPStatus             = errors.New("dicom: JPIP endpoint returned an unsuccessful status")
	ErrUnsupportedContentType = errors.New("dicom: unsupported JPIP response content type")
	ErrResponseTooLarge       = errors.New("dicom: JPIP response exceeds configured limit")
	ErrPartialResponse        = errors.New("dicom: incomplete JPIP response")
	ErrCorruptResponse        = errors.New("dicom: corrupt JPIP response")
	ErrIncrementalStream      = errors.New("dicom: JPIP incremental data-bin stream is not directly decodable")
	ErrDecode                 = errors.New("dicom: JPIP response decode failed")
)

// ErrorKind is a stable category callers can map to a user-facing diagnostic.
type ErrorKind string

const (
	ErrorKindInvalidRequest         ErrorKind = "invalid-request"
	ErrorKindAuthentication         ErrorKind = "authentication"
	ErrorKindPolicyDenied           ErrorKind = "policy-denied"
	ErrorKindRedirectDenied         ErrorKind = "redirect-denied"
	ErrorKindOffline                ErrorKind = "offline"
	ErrorKindHTTPStatus             ErrorKind = "http-status"
	ErrorKindUnsupportedContentType ErrorKind = "unsupported-content-type"
	ErrorKindResponseTooLarge       ErrorKind = "response-too-large"
	ErrorKindPartialResponse        ErrorKind = "partial-response"
	ErrorKindCorruptResponse        ErrorKind = "corrupt-response"
	ErrorKindIncrementalStream      ErrorKind = "incremental-stream"
	ErrorKindDecode                 ErrorKind = "decode"
)

// Error describes a JPIP failure without retaining the provider URL or its
// query parameters. Provider URLs can contain short-lived target identifiers
// and must not become durable diagnostics.
type Error struct {
	Kind        ErrorKind
	Operation   string
	StatusCode  int
	ContentType string
	Limit       int64
	Size        int64
	Err         error
}

func (e *Error) Error() string {
	if e == nil {
		return "dicom: JPIP operation failed"
	}
	message := "dicom: JPIP operation failed"
	if e.Err != nil {
		message = e.Err.Error()
	}
	if e.Operation != "" {
		message += " during " + e.Operation
	}
	if e.StatusCode != 0 {
		message += fmt.Sprintf(" (HTTP %d)", e.StatusCode)
	}
	if e.ContentType != "" {
		message += fmt.Sprintf(" (content type %q)", e.ContentType)
	}
	if e.Limit > 0 {
		message += fmt.Sprintf(" (limit %d bytes", e.Limit)
		if e.Size > 0 {
			message += fmt.Sprintf(", received %d", e.Size)
		}
		message += ")"
	}
	return message
}

func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}
