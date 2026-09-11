package dicomweb

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

type serverResponseWriter struct {
	http.ResponseWriter
	status    int
	written   bool
	remaining int64
	limit     int64
}

func (w *serverResponseWriter) WriteHeader(status int) {
	if w.written {
		return
	}
	w.status = status
	w.written = true
	w.ResponseWriter.WriteHeader(status)
}

func (w *serverResponseWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > w.remaining {
		return 0, ErrResourceLimit
	}
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(data)
	w.remaining -= int64(n)
	return n, err
}

func (w *serverResponseWriter) Flush() {
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (s *Server) serveCore(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	writer := &serverResponseWriter{ResponseWriter: w, status: http.StatusOK, remaining: s.limits.MaxResponseBytes, limit: s.limits.MaxResponseBytes}
	requestID := ""
	if s.options.RequestID != nil {
		requestID = safeRequestID(s.options.RequestID, r)
		if requestID != "" {
			writer.Header().Set("X-Request-ID", requestID)
		}
	}
	operation := routeOperation(r)
	if operation == OperationRetrieveRendered || operation == OperationRetrieveThumbnail {
		writer.Header().Set("Cache-Control", "private, no-store")
		writer.Header().Set("Vary", "Accept")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
	}
	itemCount := 0
	errorCode := ""
	defer func() {
		if recovered := recover(); recovered != nil {
			errorCode = "panic"
			if !writer.written {
				writeServerError(writer, newServerStatusError(http.StatusInternalServerError, "backend_failure", ErrBackend))
			}
		}
		if s.options.Audit != nil {
			event := AuditEvent{Operation: operation, RequestID: requestID, StatusCode: writer.status, ItemCount: itemCount, Duration: time.Since(started), ErrorCode: errorCode}
			safeAudit(s.options.Audit, r.Context(), event)
		}
	}()

	if r == nil || r.URL == nil || len(r.RequestURI) > s.limits.MaxRequestURIBytes {
		errorCode = "invalid_request"
		writeServerError(writer, newServerStatusError(http.StatusRequestURITooLong, errorCode, ErrInvalidRequest))
		return
	}
	if requestHeaderBytes(r.Header) > s.limits.MaxHeaderBytes {
		errorCode = "request_headers_too_large"
		writeServerError(writer, newServerStatusError(http.StatusRequestHeaderFieldsTooLarge, errorCode, ErrResourceLimit))
		return
	}
	select {
	case s.sem <- struct{}{}:
		defer func() { <-s.sem }()
	default:
		errorCode = "busy"
		writer.Header().Set("Retry-After", "1")
		writeServerError(writer, newServerStatusError(http.StatusServiceUnavailable, errorCode, ErrResourceLimit))
		return
	}

	ctx := r.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	if s.limits.MaxDuration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.limits.MaxDuration)
		defer cancel()
	}
	if err := ctx.Err(); err != nil {
		errorCode = contextErrorCode(err)
		writeServerError(writer, err)
		return
	}
	r = r.WithContext(ctx)
	if operation == OperationUnknown {
		if allowed := allowedMethods(r.URL.Path); len(allowed) > 0 {
			writer.Header().Set("Allow", strings.Join(allowed, ", "))
			errorCode = "method_not_allowed"
			writeServerError(writer, newServerStatusError(http.StatusMethodNotAllowed, errorCode, ErrUnsupported))
			return
		}
		errorCode = "not_found"
		writeServerError(writer, newServerStatusError(http.StatusNotFound, errorCode, ErrNotFound))
		return
	}
	if s.options.Authorize == nil {
		if !s.options.AllowUnauthenticated {
			errorCode = "unauthorized"
			writer.Header().Set("WWW-Authenticate", `Bearer realm="dicomweb"`)
			writeServerError(writer, ErrUnauthorized)
			return
		}
	} else if err := safeAuthorize(s.options.Authorize, ctx, r, operation); err != nil {
		errorCode = authorizationErrorCode(err)
		writeServerError(writer, err)
		return
	}

	count, err := s.dispatch(writer, r, operation)
	itemCount = count
	if err != nil {
		errorCode = errorCodeForServer(err)
		if !writer.written {
			writeServerError(writer, err)
		}
	}
}

func safeRequestID(callback func(*http.Request) string, r *http.Request) (result string) {
	defer func() {
		if recover() != nil {
			result = ""
		}
	}()
	value := strings.TrimSpace(callback(r))
	if len(value) > 64 {
		value = value[:64]
	}
	for index, char := range value {
		if !((char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.') {
			return value[:index]
		}
	}
	return value
}

func safeAudit(callback func(context.Context, AuditEvent), ctx context.Context, event AuditEvent) {
	defer func() { _ = recover() }()
	callback(ctx, event)
}
func safeAuthorize(callback Authorizer, ctx context.Context, r *http.Request, operation Operation) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrForbidden
		}
	}()
	err = callback(ctx, r, operation)
	if err != nil && !errors.Is(err, ErrUnauthorized) && !errors.Is(err, ErrForbidden) {
		err = ErrForbidden
	}
	return
}
