package dicomweb

import (
	"context"
	"errors"
	"net/http"
)

const statusClientClosedRequest = 499

type serverStatusError struct {
	status int
	code   string
	err    error
}

func (e *serverStatusError) Error() string { return "DICOMweb request failed" }
func (e *serverStatusError) Unwrap() error { return e.err }
func newServerStatusError(status int, code string, err error) error {
	return &serverStatusError{status: status, code: code, err: err}
}

func writeServerError(w *serverResponseWriter, err error) {
	status := statusForServerError(err)
	message := "DICOMweb request failed"
	if status == statusClientClosedRequest {
		message = "request canceled"
	}
	http.Error(w, message, status)
}

func statusForServerError(err error) int {
	var statusErr *serverStatusError
	if errors.As(err, &statusErr) {
		return statusErr.status
	}
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return http.StatusRequestEntityTooLarge
	}
	switch {
	case errors.Is(err, context.Canceled):
		return statusClientClosedRequest
	case errors.Is(err, context.DeadlineExceeded):
		return http.StatusGatewayTimeout
	case errors.Is(err, ErrUnauthorized):
		return http.StatusUnauthorized
	case errors.Is(err, ErrForbidden):
		return http.StatusForbidden
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound
	case errors.Is(err, ErrConflict):
		return http.StatusConflict
	case errors.Is(err, ErrResourceLimit):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest
	case errors.Is(err, ErrUnsupported):
		return http.StatusNotAcceptable
	default:
		return http.StatusInternalServerError
	}
}

func errorCodeForServer(err error) string {
	var statusErr *serverStatusError
	if errors.As(err, &statusErr) && statusErr.code != "" {
		return statusErr.code
	}
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline"
	case errors.Is(err, ErrResourceLimit):
		return "resource_limit"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid_request"
	case errors.Is(err, ErrUnsupported):
		return "unsupported"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	default:
		return "backend_failure"
	}
}

func classifyBackendError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrNotFound) || errors.Is(err, ErrConflict) || errors.Is(err, ErrUnsupported) || errors.Is(err, ErrResourceLimit) || errors.Is(err, ErrInvalidRequest) {
		return err
	}
	return ErrBackend
}

func classifyMultipartError(err error) error {
	var maxBytes *http.MaxBytesError
	if errors.As(err, &maxBytes) {
		return newServerStatusError(http.StatusRequestEntityTooLarge, "request_too_large", ErrResourceLimit)
	}
	if errors.Is(err, ErrResourceLimit) {
		return newServerStatusError(http.StatusRequestEntityTooLarge, "request_too_large", err)
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrInvalidRequest
}

func contextErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "deadline"
	}
	return "canceled"
}
func authorizationErrorCode(err error) string {
	if errors.Is(err, ErrUnauthorized) {
		return "unauthorized"
	}
	return "forbidden"
}
