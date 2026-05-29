package dicomweb

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/index"
	"github.com/ThalesMMS/dicom-go/transfer"
)

const statusClientClosedRequest = 499

var errSearchLimitReached = errors.New("dicomweb: search response limit reached")

// Server is an embeddable http.Handler and an optional owner of an http.Server.
type Server struct {
	options ServerOptions
	limits  ServerLimits
	handler http.Handler
	sem     chan struct{}

	mu         sync.Mutex
	httpServer *http.Server
}

// NewServer constructs a bounded, deny-by-default DICOMweb handler.
func NewServer(options ServerOptions) (*Server, error) {
	if options.Backend == nil {
		return nil, fmt.Errorf("%w: backend is required", ErrInvalidServerOptions)
	}
	limits, err := normalizeServerLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	policy, err := normalizeStorePolicy(options.StorePolicy)
	if err != nil {
		return nil, err
	}
	options.StorePolicy = policy
	if options.SpoolRetentionAge < 0 || options.SpoolAggregateQuotaBytes < 0 {
		return nil, fmt.Errorf("%w: invalid spool retention policy", ErrInvalidServerOptions)
	}
	if options.SpoolRetentionAge == 0 {
		options.SpoolRetentionAge = defaultSpoolRetentionAge
	}
	if options.SpoolAggregateQuotaBytes == 0 {
		options.SpoolAggregateQuotaBytes = defaultSpoolAggregateQuotaBytes
	}
	if err := validateServiceRoot(options.ServiceRoot); err != nil {
		return nil, err
	}
	if err := scavengeDICOMwebSpool(options, limits, time.Now()); err != nil {
		return nil, err
	}
	server := &Server{
		options: options,
		limits:  limits,
		sem:     make(chan struct{}, limits.MaxConcurrentRequests),
	}
	var handler http.Handler = http.HandlerFunc(server.serveCore)
	if options.Middleware != nil {
		handler = options.Middleware(handler)
		if handler == nil {
			return nil, fmt.Errorf("%w: middleware returned nil handler", ErrInvalidServerOptions)
		}
	}
	server.handler = handler
	return server, nil
}

type dicomwebSpoolEntry struct {
	name    string
	size    int64
	modTime time.Time
	removed bool
}

func scavengeDICOMwebSpool(options ServerOptions, limits ServerLimits, now time.Time) error {
	return scavengeDICOMwebSpoolWithRemove(options, limits, now, os.Remove)
}

func scavengeDICOMwebSpoolWithRemove(options ServerOptions, limits ServerLimits, now time.Time, removeFile func(string) error) error {
	directory := options.SpoolDirectory
	if directory == "" {
		directory = os.TempDir()
	}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: spool directory is unavailable", ErrInvalidServerOptions)
	}
	owned := make([]dicomwebSpoolEntry, 0)
	var aggregate int64
	for _, entry := range entries {
		if !dicomwebOwnsSpoolName(entry.Name()) || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() < 0 || aggregate > int64(^uint64(0)>>1)-info.Size() {
			continue
		}
		aggregate += info.Size()
		owned = append(owned, dicomwebSpoolEntry{name: entry.Name(), size: info.Size(), modTime: info.ModTime()})
	}
	activeCutoff := now.Add(-limits.MaxDuration)
	retentionCutoff := now.Add(-options.SpoolRetentionAge)
	removeEntry := func(entry *dicomwebSpoolEntry) error {
		path := filepath.Join(directory, entry.name)
		info, statErr := os.Lstat(path)
		if errors.Is(statErr, os.ErrNotExist) {
			entry.removed = true
			return nil
		}
		if statErr != nil || !info.Mode().IsRegular() {
			// Racing or irregular entries are skipped so scavenging can continue.
			return nil
		}
		if err := removeFile(path); err != nil {
			// Individual removal failures are non-fatal and can be retried later.
			return nil
		}
		entry.removed = true
		aggregate -= entry.size
		return nil
	}
	for index := range owned {
		entry := &owned[index]
		if entry.modTime.Before(activeCutoff) && entry.modTime.Before(retentionCutoff) {
			if err := removeEntry(entry); err != nil {
				return err
			}
		}
	}
	if aggregate <= options.SpoolAggregateQuotaBytes {
		return nil
	}
	sort.SliceStable(owned, func(left, right int) bool { return owned[left].modTime.Before(owned[right].modTime) })
	for index := range owned {
		entry := &owned[index]
		if aggregate <= options.SpoolAggregateQuotaBytes {
			break
		}
		if entry.removed || !entry.modTime.Before(activeCutoff) {
			continue
		}
		if err := removeEntry(entry); err != nil {
			return err
		}
	}
	return nil
}

func dicomwebOwnsSpoolName(name string) bool {
	for _, prefix := range []string{".dicomweb-json-", ".dicomweb-stow-"} {
		if strings.HasPrefix(name, prefix) && len(name) > len(prefix) {
			return true
		}
	}
	return false
}

func validateServiceRoot(root string) error {
	if root == "" {
		return nil
	}
	if !strings.HasPrefix(root, "/") || root == "/" || strings.HasSuffix(root, "/") || strings.Contains(root, "//") || strings.ContainsAny(root, "?#%") {
		return fmt.Errorf("%w: invalid service root", ErrInvalidServerOptions)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." || url.PathEscape(segment) != segment {
			return fmt.Errorf("%w: invalid service root", ErrInvalidServerOptions)
		}
	}
	return nil
}

func (s *Server) servicePath(resource string) string {
	return s.options.ServiceRoot + resource
}

func (s *Server) serviceURI(r *http.Request) string {
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	host := "dicomweb"
	if r != nil && r.Host != "" {
		host = r.Host
	}
	return scheme + "://" + host + s.options.ServiceRoot
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.handler == nil {
		http.Error(w, "DICOMweb service unavailable", http.StatusServiceUnavailable)
		return
	}
	s.handler.ServeHTTP(w, r)
}

// Serve serves the handler on listener. Only one owned server may run at once.
func (s *Server) Serve(listener net.Listener) error {
	if s == nil || listener == nil {
		return fmt.Errorf("%w: listener is required", ErrInvalidServerOptions)
	}
	httpOptions := s.options.HTTP
	if httpOptions.ReadHeaderTimeout <= 0 {
		httpOptions.ReadHeaderTimeout = 10 * time.Second
	}
	if httpOptions.ReadTimeout <= 0 {
		httpOptions.ReadTimeout = s.limits.MaxDuration
	}
	if httpOptions.WriteTimeout <= 0 {
		httpOptions.WriteTimeout = s.limits.MaxDuration
	}
	if httpOptions.IdleTimeout <= 0 {
		httpOptions.IdleTimeout = 60 * time.Second
	}
	if httpOptions.MaxHeaderBytes <= 0 {
		httpOptions.MaxHeaderBytes = s.limits.MaxHeaderBytes
	}
	owned := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: httpOptions.ReadHeaderTimeout,
		ReadTimeout:       httpOptions.ReadTimeout,
		WriteTimeout:      httpOptions.WriteTimeout,
		IdleTimeout:       httpOptions.IdleTimeout,
		MaxHeaderBytes:    httpOptions.MaxHeaderBytes,
	}
	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: server is already running", ErrInvalidServerOptions)
	}
	s.httpServer = owned
	s.mu.Unlock()
	err := owned.Serve(listener)
	s.mu.Lock()
	if s.httpServer == owned {
		s.httpServer = nil
	}
	s.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops an owned server. It is idempotent when Serve has
// not been called or has already returned.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	owned := s.httpServer
	s.mu.Unlock()
	if owned == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	return owned.Shutdown(ctx)
}

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

func allowedMethods(path string) []string {
	parts := resourceParts(path)
	if len(parts) == 1 && parts[0] == "studies" {
		return []string{http.MethodGet, http.MethodPost}
	}
	if len(parts) == 2 && parts[0] == "studies" {
		return []string{http.MethodGet, http.MethodPost}
	}
	probe := &http.Request{Method: http.MethodGet, URL: &url.URL{Path: path}}
	if routeOperation(probe) != OperationUnknown {
		return []string{http.MethodGet}
	}
	return nil
}

func (s *Server) dispatch(w *serverResponseWriter, r *http.Request, operation Operation) (int, error) {
	if strings.TrimSpace(requestAcceptHeader(r)) == "" {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	switch operation {
	case OperationSearchStudies, OperationSearchSeries, OperationSearchInstances:
		return s.serveSearch(w, r, operation)
	case OperationRetrieveMetadata:
		return s.serveMetadata(w, r)
	case OperationRetrieveStudy, OperationRetrieveSeries, OperationRetrieveInstance:
		return s.serveRetrieve(w, r, operation)
	case OperationRetrieveFrames:
		return s.serveFrames(w, r)
	case OperationRetrieveBulkData:
		return s.serveBulkData(w, r)
	case OperationStoreInstances:
		return s.serveStore(w, r)
	default:
		return 0, ErrNotFound
	}
}

func routeOperation(r *http.Request) Operation {
	if r == nil || r.URL == nil {
		return OperationUnknown
	}
	parts := resourceParts(r.URL.Path)
	if r.Method == http.MethodPost && len(parts) >= 1 && parts[0] == "studies" && len(parts) <= 2 {
		return OperationStoreInstances
	}
	if r.Method != http.MethodGet {
		return OperationUnknown
	}
	if len(parts) >= 2 && parts[0] == "bulkdata" {
		return OperationRetrieveBulkData
	}
	if len(parts) == 1 {
		switch parts[0] {
		case "studies":
			return OperationSearchStudies
		case "series":
			return OperationSearchSeries
		case "instances":
			return OperationSearchInstances
		}
	}
	if len(parts) == 2 && parts[0] == "studies" {
		return OperationRetrieveStudy
	}
	if len(parts) == 3 && parts[0] == "studies" {
		switch parts[2] {
		case "series":
			return OperationSearchSeries
		case "instances":
			return OperationSearchInstances
		case "metadata":
			return OperationRetrieveMetadata
		}
	}
	if len(parts) == 4 && parts[0] == "studies" && parts[2] == "series" {
		return OperationRetrieveSeries
	}
	if len(parts) == 5 && parts[0] == "studies" && parts[2] == "series" {
		switch parts[4] {
		case "instances":
			return OperationSearchInstances
		case "metadata":
			return OperationRetrieveMetadata
		}
	}
	if len(parts) == 6 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" {
		return OperationRetrieveInstance
	}
	if len(parts) == 7 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" && parts[6] == "metadata" {
		return OperationRetrieveMetadata
	}
	if len(parts) == 8 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" && parts[6] == "frames" {
		return OperationRetrieveFrames
	}
	return OperationUnknown
}

func resourceParts(path string) []string {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func requestHeaderBytes(header http.Header) int {
	total := 0
	for key, values := range header {
		total += len(key)
		for _, value := range values {
			total += len(value)
		}
	}
	return total
}

func (s *Server) serveSearch(w *serverResponseWriter, r *http.Request, operation Operation) (int, error) {
	backend, ok := s.options.Backend.(SearchBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	if !acceptsDICOMJSON(requestAcceptHeader(r)) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	request, err := s.parseSearchRequest(r, operation)
	if err != nil {
		return 0, err
	}
	request.MaximumResults = s.limits.MaxResults
	if request.LimitSet && request.Limit < request.MaximumResults {
		request.MaximumResults = request.Limit
	}
	spool, err := newJSONArraySpool(s.options.SpoolDirectory, s.limits.MaxResponseBytes)
	if err != nil {
		return 0, ErrBackend
	}
	spoolClosed := false
	defer func() {
		if !spoolClosed {
			_ = spool.Close()
		}
	}()
	truncated := false
	result, err := safeSearch(backend, r.Context(), request, func(dataset Dataset) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if spool.count >= request.MaximumResults {
			truncated = true
			return errSearchLimitReached
		}
		data, err := s.encodeResponseDataset(r.Context(), dataset)
		if err != nil {
			return ErrBackend
		}
		return spool.Append(data)
	})
	if errors.Is(err, errSearchLimitReached) {
		err = nil
	}
	if err != nil {
		return 0, classifyBackendError(err)
	}
	if result.Remaining != nil && *result.Remaining < 0 {
		return 0, ErrBackend
	}
	if spool.count == request.MaximumResults && result.Remaining == nil {
		return 0, ErrBackend
	}
	if truncated && (result.Remaining == nil || *result.Remaining <= 0) {
		return 0, ErrBackend
	}
	if request.FuzzyMatching && !result.FuzzyMatchingApplied {
		w.Header().Add("Warning", fmt.Sprintf("299 %s: The fuzzymatching parameter is not supported. Only literal matching has been performed.", s.serviceURI(r)))
	}
	if request.EmptyValueMatching && !result.EmptyValueMatchingApplied {
		w.Header().Add("Warning", fmt.Sprintf("299 %s: The emptyvaluematching parameter is not supported. Empty Value Matching has not been performed.", s.serviceURI(r)))
	}
	if request.MultipleValueMatching && !result.MultipleValueMatchingApplied {
		w.Header().Add("Warning", fmt.Sprintf("299 %s: The multiplevaluematching parameter is not supported. Multiple Value Matching has not been performed.", s.serviceURI(r)))
	}
	if result.Remaining != nil && *result.Remaining > 0 {
		w.Header().Add("Warning", fmt.Sprintf("299 %s: There are %d additional results that can be requested", s.serviceURI(r), *result.Remaining))
	}
	w.Header().Set("Content-Type", "application/dicom+json")
	if err := spool.Finalize(); err != nil {
		return 0, err
	}
	_, err = copyBounded(r.Context(), w, spool.file, s.limits.MaxResponseBytes)
	closeErr := spool.Close()
	spoolClosed = true
	if err != nil {
		return spool.count, err
	}
	return spool.count, closeErr
}

func (s *Server) parseSearchRequest(r *http.Request, operation Operation) (SearchRequest, error) {
	parts := resourceParts(r.URL.Path)
	request := SearchRequest{}
	switch operation {
	case OperationSearchStudies:
		request.Level = SearchLevelStudy
	case OperationSearchSeries:
		request.Level = SearchLevelSeries
	case OperationSearchInstances:
		request.Level = SearchLevelInstance
	}
	if len(parts) >= 2 && parts[0] == "studies" {
		request.StudyInstanceUID = parts[1]
		if err := s.validateUID(request.StudyInstanceUID); err != nil {
			return SearchRequest{}, err
		}
	}
	if len(parts) >= 4 && parts[2] == "series" {
		request.SeriesInstanceUID = parts[3]
		if err := s.validateUID(request.SeriesInstanceUID); err != nil {
			return SearchRequest{}, err
		}
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return SearchRequest{}, ErrInvalidRequest
	}
	queryFields := 0
	for _, values := range query {
		queryFields += len(values)
	}
	if queryFields > s.limits.MaxQueryFields {
		return SearchRequest{}, ErrResourceLimit
	}
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values := query[key]
		for _, value := range values {
			if len(value) > s.limits.MaxQueryValueBytes {
				return SearchRequest{}, ErrResourceLimit
			}
		}
		switch key {
		case "limit":
			value, err := singleNonNegativeInteger(values, int(^uint(0)>>1))
			if err != nil {
				return SearchRequest{}, ErrInvalidRequest
			}
			request.Limit = value
			request.LimitSet = true
		case "offset":
			value, err := singleNonNegativeInteger(values, int(^uint(0)>>1))
			if err != nil {
				return SearchRequest{}, ErrInvalidRequest
			}
			if value > s.limits.MaxOffset {
				return SearchRequest{}, ErrResourceLimit
			}
			request.Offset = value
		case "fuzzymatching":
			value, err := singleBoolean(values)
			if err != nil {
				return SearchRequest{}, ErrInvalidRequest
			}
			request.FuzzyMatching = value
		case "emptyvaluematching":
			value, err := singleBoolean(values)
			if err != nil {
				return SearchRequest{}, ErrInvalidRequest
			}
			request.EmptyValueMatching = value
		case "multiplevaluematching":
			value, err := singleBoolean(values)
			if err != nil {
				return SearchRequest{}, ErrInvalidRequest
			}
			request.MultipleValueMatching = value
		case "includefield":
			for _, value := range values {
				for _, field := range strings.Split(value, ",") {
					if field == "" || (field != "all" && !validAttributeIdentifier(field)) {
						return SearchRequest{}, ErrInvalidRequest
					}
					request.IncludeFields = append(request.IncludeFields, field)
				}
			}
		default:
			if !validAttributeIdentifier(key) || len(values) != 1 {
				return SearchRequest{}, ErrInvalidRequest
			}
			request.Filters = append(request.Filters, SearchFilter{Key: key, Values: append([]string(nil), values...)})
		}
	}
	all := false
	for _, field := range request.IncludeFields {
		if field == "all" {
			if all || len(request.IncludeFields) != 1 {
				return SearchRequest{}, ErrInvalidRequest
			}
			all = true
		}
	}
	return request, nil
}

func validAttributeIdentifier(value string) bool {
	if len(value) == 8 {
		if tag, err := core.ParseTag(value); err == nil && tag.Group != 0x0002 && tag.Group%2 == 0 {
			return true
		}
	}
	entry, ok := std.Dictionary.ByKeyword(value)
	return ok && entry.Keyword == value
}

func singleNonNegativeInteger(values []string, maximum int) (int, error) {
	if len(values) != 1 {
		return 0, ErrInvalidRequest
	}
	parsed, err := strconv.ParseInt(values[0], 10, 63)
	if err != nil || parsed < 0 || parsed > int64(maximum) {
		return 0, ErrInvalidRequest
	}
	return int(parsed), nil
}

func singleBoolean(values []string) (bool, error) {
	if len(values) != 1 {
		return false, ErrInvalidRequest
	}
	switch values[0] {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, ErrInvalidRequest
	}
}

func (s *Server) serveMetadata(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(MetadataBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	if !acceptsDICOMJSON(requestAcceptHeader(r)) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	request, err := s.metadataRequest(r.URL.Path)
	if err != nil {
		return 0, err
	}
	spool, err := newJSONArraySpool(s.options.SpoolDirectory, s.limits.MaxResponseBytes)
	if err != nil {
		return 0, ErrBackend
	}
	spoolClosed := false
	defer func() {
		if !spoolClosed {
			_ = spool.Close()
		}
	}()
	err = safeMetadata(backend, r.Context(), request, func(dataset Dataset) error {
		if err := r.Context().Err(); err != nil {
			return err
		}
		if spool.count >= s.limits.MaxResults {
			return ErrResourceLimit
		}
		data, err := s.encodeResponseDataset(r.Context(), dataset)
		if err != nil {
			return ErrBackend
		}
		return spool.Append(data)
	})
	if err != nil {
		return 0, classifyBackendError(err)
	}
	w.Header().Set("Content-Type", "application/dicom+json")
	if err := spool.Finalize(); err != nil {
		return 0, err
	}
	_, err = copyBounded(r.Context(), w, spool.file, s.limits.MaxResponseBytes)
	closeErr := spool.Close()
	spoolClosed = true
	if err != nil {
		return spool.count, err
	}
	return spool.count, closeErr
}

func (s *Server) metadataRequest(path string) (MetadataRequest, error) {
	parts := resourceParts(path)
	request := MetadataRequest{Level: MetadataLevelStudy}
	if len(parts) >= 2 {
		request.StudyInstanceUID = parts[1]
	}
	if len(parts) >= 4 {
		request.Level = MetadataLevelSeries
		request.SeriesInstanceUID = parts[3]
	}
	if len(parts) >= 6 {
		request.Level = MetadataLevelInstance
		request.SOPInstanceUID = parts[5]
	}
	for _, uid := range []string{request.StudyInstanceUID, request.SeriesInstanceUID, request.SOPInstanceUID} {
		if uid != "" {
			if err := s.validateUID(uid); err != nil {
				return MetadataRequest{}, err
			}
		}
	}
	return request, nil
}

func (s *Server) serveRetrieve(w *serverResponseWriter, r *http.Request, operation Operation) (int, error) {
	backend, ok := s.options.Backend.(RetrieveBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	request, err := s.retrieveRequest(r, operation)
	if err != nil {
		return 0, err
	}
	var writer *multipart.Writer
	responseMultipart := request.Level != RetrieveLevelInstance
	count := 0
	err = safeRetrieve(backend, r.Context(), request, func(part RetrievePart) error {
		if part.Reader == nil {
			return ErrBackend
		}
		return withReaderClosed(part.Reader, func() error {
			if err := r.Context().Err(); err != nil {
				return err
			}
			if count >= s.limits.MaxResults || part.Size < -1 || part.Size > s.limits.MaxPartBytes {
				return ErrResourceLimit
			}
			if request.Level == RetrieveLevelInstance && count > 0 {
				return ErrBackend
			}
			if part.TransferSyntaxUID != "" {
				if err := s.validateUID(part.TransferSyntaxUID); err != nil {
					return ErrBackend
				}
				if !webServiceTransferSyntaxAllowed(part.TransferSyntaxUID) {
					return ErrBackend
				}
			}
			contentType, err := normalizedDICOMPartContentType(part.ContentType, part.TransferSyntaxUID)
			if err != nil {
				return ErrUnsupported
			}
			preference, ok := matchingMediaPreference(request.Accept, contentType, part.TransferSyntaxUID, responseMultipart)
			if !ok {
				return ErrUnsupported
			}
			if request.Level == RetrieveLevelInstance && count == 0 {
				responseMultipart = preference.Multipart
			}
			ref := part.Ref
			if request.Level == RetrieveLevelInstance && ref == (InstanceRef{}) {
				ref = InstanceRef{StudyInstanceUID: request.StudyInstanceUID, SeriesInstanceUID: request.SeriesInstanceUID, SOPInstanceUID: request.SOPInstanceUID}
			}
			if !retrieveRefMatches(request, ref) {
				return ErrBackend
			}
			location, err := s.instanceLocation(ref)
			if err != nil {
				return ErrBackend
			}
			var destination io.Writer = w
			if responseMultipart {
				if writer == nil {
					writer = multipart.NewWriter(w)
					w.Header().Set("Content-Type", `multipart/related; type="application/dicom"; boundary=`+writer.Boundary())
				}
				header := textproto.MIMEHeader{}
				header.Set("Content-Type", contentType)
				header.Set("Content-Location", location)
				destination, err = writer.CreatePart(header)
				if err != nil {
					return err
				}
			} else {
				if count != 0 {
					return ErrBackend
				}
				w.Header().Set("Content-Type", contentType)
				w.Header().Set("Content-Location", location)
			}
			if _, err := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); err != nil {
				return err
			}
			count++
			return nil
		})
	})
	if err != nil {
		if count == 0 {
			return 0, classifyBackendError(err)
		}
		return count, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}
	if writer != nil {
		if err := writer.Close(); err != nil {
			return count, err
		}
	}
	return count, nil
}

func (s *Server) retrieveRequest(r *http.Request, operation Operation) (RetrieveRequest, error) {
	parts := resourceParts(r.URL.Path)
	request := RetrieveRequest{}
	switch operation {
	case OperationRetrieveStudy:
		request.Level = RetrieveLevelStudy
	case OperationRetrieveSeries:
		request.Level = RetrieveLevelSeries
	case OperationRetrieveInstance:
		request.Level = RetrieveLevelInstance
	}
	acceptHeader := requestAcceptHeader(r)
	request.Accept = parseMediaPreferences(acceptHeader, "application/dicom")
	if strings.TrimSpace(acceptHeader) == "" {
		request.Accept[0].Multipart = true
	}
	if len(request.Accept) == 0 || (request.Level != RetrieveLevelInstance && !hasMultipartDICOMPreference(request.Accept)) {
		return RetrieveRequest{}, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	if len(parts) >= 2 {
		request.StudyInstanceUID = parts[1]
	}
	if len(parts) >= 4 {
		request.SeriesInstanceUID = parts[3]
	}
	if len(parts) >= 6 {
		request.SOPInstanceUID = parts[5]
	}
	for _, uid := range []string{request.StudyInstanceUID, request.SeriesInstanceUID, request.SOPInstanceUID} {
		if uid != "" {
			if err := s.validateUID(uid); err != nil {
				return RetrieveRequest{}, err
			}
		}
	}
	return request, nil
}

func retrieveRefMatches(request RetrieveRequest, ref InstanceRef) bool {
	if ref.StudyInstanceUID != request.StudyInstanceUID {
		return false
	}
	if request.Level == RetrieveLevelStudy {
		return ref.SeriesInstanceUID != "" && ref.SOPInstanceUID != ""
	}
	if ref.SeriesInstanceUID != request.SeriesInstanceUID {
		return false
	}
	return request.Level != RetrieveLevelInstance || ref.SOPInstanceUID == request.SOPInstanceUID
}

func hasMultipartDICOMPreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality > 0 && ((preference.Multipart && preference.MediaType == "application/dicom") || preference.MediaType == "*/*") {
			return true
		}
	}
	return false
}

func hasMultipartFramePreference(preferences []MediaPreference) bool {
	for _, preference := range preferences {
		if preference.Quality <= 0 {
			continue
		}
		if preference.MediaType == "*/*" || (preference.Multipart && (isFrameMediaType(preference.MediaType) || preference.MediaType == "image/*" || preference.MediaType == "video/*")) {
			return true
		}
	}
	return false
}

func (s *Server) serveFrames(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(FrameBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	ref := InstanceRef{StudyInstanceUID: parts[1], SeriesInstanceUID: parts[3], SOPInstanceUID: parts[5]}
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID} {
		if err := s.validateUID(uid); err != nil {
			return 0, err
		}
	}
	frames, err := parseFrames(parts[7], s.limits.MaxFrames)
	if err != nil {
		return 0, err
	}
	acceptHeader := requestAcceptHeader(r)
	preferences := parseMediaPreferences(acceptHeader, "application/octet-stream")
	if strings.TrimSpace(acceptHeader) == "" {
		preferences[0].Multipart = true
	}
	if len(preferences) == 0 || !hasMultipartFramePreference(preferences) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	request := FrameRequest{Ref: ref, Frames: frames, Accept: preferences}
	writer := multipart.NewWriter(w)
	w.Header().Set("Content-Type", `multipart/related; type="application/octet-stream"; boundary=`+writer.Boundary())
	count := 0
	err = safeFrames(backend, r.Context(), request, func(part FramePartStream) error {
		if part.Reader == nil {
			return ErrBackend
		}
		return withReaderClosed(part.Reader, func() error {
			if part.Size < -1 || part.Size > s.limits.MaxPartBytes {
				return ErrResourceLimit
			}
			if count >= len(frames) || part.FrameNumber != frames[count] {
				return ErrBackend
			}
			if part.TransferSyntaxUID != "" {
				if err := s.validateUID(part.TransferSyntaxUID); err != nil {
					return ErrBackend
				}
			}
			contentType, err := normalizedFrameContentType(part.ContentType)
			if err != nil {
				return ErrBackend
			}
			if !frameMediaTypeMatchesTransferSyntax(contentType, part.TransferSyntaxUID) {
				return ErrBackend
			}
			if _, ok := matchingMediaPreference(preferences, contentType, part.TransferSyntaxUID, true); !ok {
				return ErrUnsupported
			}
			partMediaType := mediaTypeWithTransferSyntax(contentType, part.TransferSyntaxUID)
			if count == 0 {
				w.Header().Set("Content-Type", `multipart/related; type="`+contentType+`"; boundary=`+writer.Boundary())
			}
			header := textproto.MIMEHeader{}
			header.Set("Content-Type", partMediaType)
			header.Set("Content-Location", s.frameLocation(ref, part.FrameNumber))
			destination, err := writer.CreatePart(header)
			if err != nil {
				return err
			}
			if _, err := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); err != nil {
				return err
			}
			count++
			return nil
		})
	})
	if err != nil {
		if count == 0 {
			return 0, classifyBackendError(err)
		}
		return count, err
	}
	if count == 0 {
		return 0, ErrNotFound
	}
	if count != len(frames) {
		return count, ErrBackend
	}
	if err := writer.Close(); err != nil {
		return count, err
	}
	return count, nil
}

func (s *Server) serveBulkData(w *serverResponseWriter, r *http.Request) (int, error) {
	backend, ok := s.options.Backend.(BulkDataBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	if len(parts) < 2 {
		return 0, ErrInvalidRequest
	}
	token, err := bulkDataToken(r.URL.Path, r.URL.RawPath, "/bulkdata/")
	if err != nil {
		return 0, err
	}
	acceptHeader := requestAcceptHeader(r)
	request := BulkDataRequest{Token: token, Accept: parseMediaPreferences(acceptHeader, "application/octet-stream")}
	if strings.TrimSpace(acceptHeader) == "" {
		request.Accept[0].Multipart = true
	}
	part, err := safeBulkData(backend, r.Context(), request)
	if err != nil {
		return 0, classifyBackendError(err)
	}
	if part.Reader == nil {
		return 0, ErrBackend
	}
	err = withReaderClosed(part.Reader, func() error {
		if part.Size < -1 || part.Size > s.limits.MaxPartBytes {
			return ErrResourceLimit
		}
		contentType, contentErr := normalizedBulkContentType(part.ContentType)
		if contentErr != nil {
			return ErrBackend
		}
		preference, ok := matchingMediaPreference(request.Accept, contentType, "", false)
		if !ok {
			return newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
		}
		multipartResponse := preference.Multipart || preference.MediaType == "*/*"
		if !multipartResponse {
			w.Header().Set("Content-Type", contentType)
			_, copyErr := copyBounded(r.Context(), w, part.Reader, s.limits.MaxPartBytes)
			return copyErr
		}
		writer := multipart.NewWriter(w)
		w.Header().Set("Content-Type", `multipart/related; type="application/octet-stream"; boundary=`+writer.Boundary())
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", contentType)
		header.Set("Content-Location", s.servicePath(r.URL.EscapedPath()))
		destination, createErr := writer.CreatePart(header)
		if createErr != nil {
			return createErr
		}
		if _, copyErr := copyBounded(r.Context(), destination, part.Reader, s.limits.MaxPartBytes); copyErr != nil {
			return copyErr
		}
		return writer.Close()
	})
	if err != nil {
		return 0, err
	}
	return 1, nil
}

type stagedStorePart struct {
	path              string
	contentType       string
	transferSyntaxUID string
	size              int64
	digest            [32]byte
	ref               InstanceRef
	sopClassUID       string
	info              os.FileInfo
	preflightFailure  uint16
}

func (s *Server) serveStore(w *serverResponseWriter, r *http.Request) (count int, returnedErr error) {
	backend, ok := s.options.Backend.(StoreBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	if !acceptsDICOMJSON(requestAcceptHeader(r)) {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	parts := resourceParts(r.URL.Path)
	routeStudyUID := ""
	if len(parts) == 2 {
		routeStudyUID = parts[1]
		if err := s.validateUID(routeStudyUID); err != nil {
			return 0, err
		}
	}
	staged, err := s.stageStoreRequest(w, r, routeStudyUID)
	if err != nil {
		return 0, err
	}
	cleanupPending := true
	defer func() {
		if cleanupPending {
			if cleanupErr := cleanupStagedParts(staged); cleanupErr != nil && returnedErr == nil {
				count = 0
				returnedErr = cleanupErr
			}
		}
	}()
	outcomes := make([]StoreOutcome, 0, len(staged))
	stored := 0
	failed := 0
	warnings := 0
	createdStudies := make(map[string]struct{})
	for index, stagedPart := range staged {
		if err := r.Context().Err(); err != nil {
			return index, err
		}
		if stagedPart.preflightFailure != 0 {
			failed++
			outcomes = append(outcomes, StoreOutcome{
				Status: StoreStatusFailed, FailureReason: stagedPart.preflightFailure,
				Ref: stagedPart.ref, SOPClassUID: stagedPart.sopClassUID,
			})
			continue
		}
		reader, err := openStagedStorePart(r.Context(), stagedPart)
		if err != nil {
			return index, ErrBackend
		}
		outcome := safeStore(backend, r.Context(), StoreRequest{
			Index: index, RouteStudyUID: routeStudyUID, Ref: stagedPart.ref, SOPClassUID: stagedPart.sopClassUID, ContentType: stagedPart.contentType,
			TransferSyntaxUID: stagedPart.transferSyntaxUID, Policy: s.options.StorePolicy,
			Reader: reader, Size: stagedPart.size, SHA256: stagedPart.digest,
		})
		closeErr := reader.Close()
		if err := r.Context().Err(); err != nil {
			return index, err
		}
		if closeErr != nil && outcome.Err == nil {
			outcome.Err = closeErr
			outcome.Status = StoreStatusFailed
		}
		if outcome.Ref == (InstanceRef{}) {
			outcome.Ref = stagedPart.ref
		}
		if strings.TrimSpace(outcome.SOPClassUID) == "" {
			outcome.SOPClassUID = stagedPart.sopClassUID
		}
		if outcome.Ref != stagedPart.ref || outcome.SOPClassUID != stagedPart.sopClassUID {
			outcome.Status = StoreStatusFailed
			outcome.Err = ErrBackend
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		}
		if outcome.Err != nil {
			return index, classifyBackendError(outcome.Err)
		}
		if outcome.Status == StoreStatusStored || outcome.Status == StoreStatusWarning || outcome.Status == StoreStatusDuplicate {
			if s.validateUID(outcome.SOPClassUID) != nil || s.validateUID(outcome.Ref.StudyInstanceUID) != nil || s.validateUID(outcome.Ref.SeriesInstanceUID) != nil || s.validateUID(outcome.Ref.SOPInstanceUID) != nil {
				outcome.Status = StoreStatusFailed
				outcome.FailureReason = 0x0110
			}
		}
		switch outcome.Status {
		case StoreStatusStored:
			stored++
			createdStudies[outcome.Ref.StudyInstanceUID] = struct{}{}
		case StoreStatusWarning:
			stored++
			warnings++
			if outcome.WarningReason == 0 {
				outcome.Status = StoreStatusFailed
				outcome.FailureReason = 0x0110
				stored--
				warnings--
				failed++
			} else {
				createdStudies[outcome.Ref.StudyInstanceUID] = struct{}{}
			}
		case StoreStatusDuplicate:
			if s.options.StorePolicy == StorePolicyIdempotent {
				stored++
			} else {
				failed++
				outcome.Status = StoreStatusConflict
				if outcome.FailureReason == 0 {
					outcome.FailureReason = 0x0111
				}
			}
		case StoreStatusConflict:
			failed++
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		default:
			failed++
			outcome.Status = StoreStatusFailed
			if outcome.FailureReason == 0 {
				outcome.FailureReason = 0x0110
			}
		}
		outcomes = append(outcomes, outcome)
	}
	if err := cleanupStagedParts(staged); err != nil {
		return 0, err
	}
	cleanupPending = false
	status := http.StatusOK
	if stored > 0 && (failed > 0 || warnings > 0) {
		status = http.StatusAccepted
	}
	if stored == 0 {
		status = http.StatusConflict
	}
	dataset := storeResponseDataset(outcomes, routeStudyUID, s)
	data, err := json.Marshal(dataset)
	if err != nil {
		return 0, ErrBackend
	}
	if int64(len(data)) > s.limits.MaxResponseBytes {
		return 0, ErrResourceLimit
	}
	w.Header().Set("Content-Type", "application/dicom+json")
	if len(createdStudies) == 1 {
		for studyUID := range createdStudies {
			location := s.servicePath("/studies/" + url.PathEscape(studyUID))
			w.Header().Set("Location", location)
			w.Header().Set("Content-Location", location)
		}
	}
	w.WriteHeader(status)
	_, err = w.Write(data)
	return len(outcomes), err
}

func (s *Server) stageStoreRequest(w http.ResponseWriter, r *http.Request, routeStudyUID string) ([]stagedStorePart, error) {
	mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(mediaType, "multipart/related") || !strings.EqualFold(strings.Trim(params["type"], `"`), "application/dicom") || params["boundary"] == "" {
		return nil, newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported)
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.limits.MaxRequestBytes)
	reader := multipart.NewReader(r.Body, params["boundary"])
	var staged []stagedStorePart
	for {
		if err := r.Context().Err(); err != nil {
			return nil, cleanupStoreStaging(staged, nil, "", err)
		}
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, cleanupStoreStaging(staged, nil, "", classifyMultipartError(err))
		}
		if len(staged) >= s.limits.MaxParts {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", ErrResourceLimit)
		}
		partType, partParams, err := mime.ParseMediaType(part.Header.Get("Content-Type"))
		if err != nil || !strings.EqualFold(partType, "application/dicom") {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", newServerStatusError(http.StatusUnsupportedMediaType, "unsupported_media_type", ErrUnsupported))
		}
		if requestHeaderBytes(http.Header(part.Header)) > s.limits.MaxPartHeaderBytes {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", ErrResourceLimit)
		}
		temporary, err := os.CreateTemp(s.options.SpoolDirectory, ".dicomweb-stow-*")
		if err != nil {
			_ = part.Close()
			return nil, cleanupStoreStaging(staged, nil, "", newServerStatusError(http.StatusServiceUnavailable, "spool_failure", ErrBackend))
		}
		temporaryPath := temporary.Name()
		hash := sha256.New()
		size, copyErr := copyBounded(r.Context(), io.MultiWriter(temporary, hash), part, s.limits.MaxPartBytes)
		partCloseErr := part.Close()
		if copyErr != nil || partCloseErr != nil {
			if copyErr != nil {
				return nil, cleanupStoreStaging(staged, temporary, temporaryPath, classifyMultipartError(copyErr))
			}
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		if _, err := temporary.Seek(0, io.SeekStart); err != nil {
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		info, err := temporary.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() != size {
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		var digest [32]byte
		copy(digest[:], hash.Sum(nil))
		candidate := stagedStorePart{path: temporaryPath, contentType: "application/dicom", transferSyntaxUID: strings.TrimSpace(partParams["transfer-syntax"]), size: size, digest: digest, info: info}
		inspectErr := s.inspectStagedStorePart(r.Context(), &candidate, routeStudyUID, temporary)
		fileCloseErr := temporary.Close()
		if inspectErr != nil || fileCloseErr != nil {
			if inspectErr != nil {
				return nil, cleanupStoreStaging(staged, temporary, temporaryPath, inspectErr)
			}
			return nil, cleanupStoreStaging(staged, temporary, temporaryPath, ErrBackend)
		}
		staged = append(staged, candidate)
	}
	if len(staged) == 0 {
		return nil, ErrInvalidRequest
	}
	return staged, nil
}

func (s *Server) inspectStagedStorePart(ctx context.Context, staged *stagedStorePart, routeStudyUID string, reader io.ReadSeeker) error {
	options := index.DefaultOptions()
	options.Limits.MaxTotalBytes = s.limits.MaxDecodedPartBytes
	result, err := index.ReadSeeker(ctx, "staged-part", reader, staged.size, options)
	if err != nil {
		if errors.Is(err, index.ErrResourceLimit) {
			return ErrResourceLimit
		}
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return err
		}
		if errors.Is(err, transfer.ErrUnknownTransferSyntax) || errors.Is(err, transfer.ErrUnsupportedTransferSyntax) {
			staged.preflightFailure = 0xC122
			return nil
		}
		return ErrInvalidRequest
	}
	record := result.Record
	ref := InstanceRef{StudyInstanceUID: record.Study.InstanceUID, SeriesInstanceUID: record.Series.InstanceUID, SOPInstanceUID: record.Instance.SOPInstanceUID}
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID, record.Instance.SOPClassUID, record.FileMeta.TransferSyntaxUID} {
		if err := s.validateUID(uid); err != nil {
			return ErrInvalidRequest
		}
	}
	if record.FileMeta.MediaStorageSOPClassUID != record.Instance.SOPClassUID || record.FileMeta.MediaStorageSOPInstanceUID != ref.SOPInstanceUID {
		return ErrInvalidRequest
	}
	if staged.transferSyntaxUID != "" && staged.transferSyntaxUID != record.FileMeta.TransferSyntaxUID {
		return newServerStatusError(http.StatusUnsupportedMediaType, "transfer_syntax_mismatch", ErrUnsupported)
	}
	if routeStudyUID != "" && ref.StudyInstanceUID != routeStudyUID {
		return ErrConflict
	}
	staged.ref = ref
	staged.sopClassUID = record.Instance.SOPClassUID
	staged.transferSyntaxUID = record.FileMeta.TransferSyntaxUID
	if !webServiceTransferSyntaxAllowed(staged.transferSyntaxUID) {
		staged.preflightFailure = 0xC122
	}
	return nil
}

func openStagedStorePart(ctx context.Context, staged stagedStorePart) (*os.File, error) {
	file, err := os.Open(staged.path)
	if err != nil {
		return nil, ErrBackend
	}
	closeOnError := func() (*os.File, error) {
		_ = file.Close()
		return nil, ErrBackend
	}
	info, err := file.Stat()
	if err != nil || staged.info == nil || !info.Mode().IsRegular() || !os.SameFile(info, staged.info) || info.Size() != staged.size {
		return closeOnError()
	}
	hash := sha256.New()
	written, err := copyBounded(ctx, hash, file, staged.size)
	if err != nil || written != staged.size {
		return closeOnError()
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	if digest != staged.digest {
		return closeOnError()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeOnError()
	}
	return file, nil
}

func cleanupStagedParts(parts []stagedStorePart) error {
	failed := false
	for _, part := range parts {
		if err := os.Remove(part.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = true
		}
	}
	if failed {
		return ErrBackend
	}
	return nil
}

func cleanupStoreStaging(parts []stagedStorePart, temporary *os.File, temporaryPath string, preferred error) error {
	failed := false
	if temporary != nil {
		if err := temporary.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			failed = true
		}
	}
	if temporaryPath != "" {
		if err := os.Remove(temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = true
		}
	}
	if err := cleanupStagedParts(parts); err != nil {
		failed = true
	}
	if failed {
		return ErrBackend
	}
	return preferred
}

func storeResponseDataset(outcomes []StoreOutcome, routeStudyUID string, s *Server) Dataset {
	storedItems := make([]any, 0)
	failedItems := make([]any, 0)
	otherFailureItems := make([]any, 0)
	for _, outcome := range outcomes {
		item := Dataset{
			"00081150": {VR: "UI", Value: stringValues(outcome.SOPClassUID)},
			"00081155": {VR: "UI", Value: stringValues(outcome.Ref.SOPInstanceUID)},
		}
		if location, err := s.instanceLocation(outcome.Ref); err == nil {
			item["00081190"] = Element{VR: "UR", Value: []any{location}}
		}
		switch outcome.Status {
		case StoreStatusStored, StoreStatusDuplicate:
			storedItems = append(storedItems, item)
		case StoreStatusWarning:
			item["00081196"] = Element{VR: "US", Value: []any{outcome.WarningReason}}
			storedItems = append(storedItems, item)
		default:
			item["00081197"] = Element{VR: "US", Value: []any{outcome.FailureReason}}
			if strings.TrimSpace(outcome.SOPClassUID) == "" || strings.TrimSpace(outcome.Ref.SOPInstanceUID) == "" {
				otherFailureItems = append(otherFailureItems, Dataset{"00081197": item["00081197"]})
			} else {
				failedItems = append(failedItems, item)
			}
		}
	}
	response := Dataset{
		"00081190": {VR: "UR"},
	}
	if routeStudyUID != "" {
		response["00081190"] = Element{VR: "UR", Value: []any{s.servicePath("/studies/" + url.PathEscape(routeStudyUID))}}
	}
	if len(storedItems) > 0 {
		response["00081199"] = Element{VR: "SQ", Value: storedItems}
	}
	if len(failedItems) > 0 {
		response["00081198"] = Element{VR: "SQ", Value: failedItems}
	}
	if len(otherFailureItems) > 0 {
		response["0008119A"] = Element{VR: "SQ", Value: otherFailureItems}
	}
	return response
}

func stringValues(value string) []any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return []any{strings.TrimSpace(value)}
}

func (s *Server) validateUID(uid string) error {
	if len(uid) == 0 || len(uid) > s.limits.MaxUIDBytes || len(uid) > 64 || uid[0] == '.' || uid[len(uid)-1] == '.' {
		return ErrInvalidRequest
	}
	for _, component := range strings.Split(uid, ".") {
		if component == "" || (len(component) > 1 && component[0] == '0') {
			return ErrInvalidRequest
		}
		for _, char := range component {
			if char < '0' || char > '9' {
				return ErrInvalidRequest
			}
		}
	}
	return nil
}

func (s *Server) instanceLocation(ref InstanceRef) (string, error) {
	for _, uid := range []string{ref.StudyInstanceUID, ref.SeriesInstanceUID, ref.SOPInstanceUID} {
		if err := s.validateUID(uid); err != nil {
			return "", err
		}
	}
	return s.servicePath("/studies/" + url.PathEscape(ref.StudyInstanceUID) + "/series/" + url.PathEscape(ref.SeriesInstanceUID) + "/instances/" + url.PathEscape(ref.SOPInstanceUID)), nil
}

func (s *Server) frameLocation(ref InstanceRef, frame int) string {
	location, _ := s.instanceLocation(ref)
	return location + "/frames/" + strconv.Itoa(frame)
}

func parseFrames(value string, maximum int) ([]int, error) {
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > maximum {
		return nil, ErrResourceLimit
	}
	frames := make([]int, len(parts))
	seen := make(map[int]bool, len(parts))
	for index, raw := range parts {
		frame, err := strconv.ParseInt(raw, 10, 31)
		if err != nil || frame <= 0 || seen[int(frame)] {
			return nil, ErrInvalidRequest
		}
		seen[int(frame)] = true
		frames[index] = int(frame)
	}
	return frames, nil
}

func acceptsDICOMJSON(header string) bool {
	if strings.TrimSpace(header) == "" {
		return true
	}
	preference, ok := bestMatchingMediaPreference(parseAcceptHeader(header), "application/dicom+json", "", false)
	return ok && preference.Quality > 0
}

func parseMediaPreferences(header, defaultMediaType string) []MediaPreference {
	if strings.TrimSpace(header) == "" {
		return []MediaPreference{{MediaType: defaultMediaType, Quality: 1}}
	}
	return parseAcceptHeader(header)
}

func parseAcceptHeader(header string) []MediaPreference {
	var result []MediaPreference
	for _, raw := range splitAcceptValues(header) {
		mediaType, params, err := mime.ParseMediaType(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		quality := 1.0
		if rawQuality := params["q"]; rawQuality != "" {
			parsed, ok := parseAcceptQValue(rawQuality)
			if !ok {
				continue
			}
			quality = parsed
		}
		multipartRelated := strings.EqualFold(mediaType, "multipart/related")
		if multipartRelated && params["type"] != "" {
			mediaType = strings.Trim(params["type"], `"`)
		}
		result = append(result, MediaPreference{MediaType: strings.ToLower(mediaType), TransferSyntaxUID: strings.TrimSpace(params["transfer-syntax"]), Quality: quality, Multipart: multipartRelated})
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Quality > result[j].Quality })
	return result
}

func requestAcceptHeader(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.Join(r.Header.Values("Accept"), ",")
}

func parseAcceptQValue(raw string) (float64, bool) {
	if raw == "0" || raw == "1" {
		parsed, err := strconv.ParseFloat(raw, 64)
		return parsed, err == nil
	}
	if len(raw) < 2 || len(raw) > 5 || raw[1] != '.' || (raw[0] != '0' && raw[0] != '1') {
		return 0, false
	}
	for index := 2; index < len(raw); index++ {
		if raw[index] < '0' || raw[index] > '9' || (raw[0] == '1' && raw[index] != '0') {
			return 0, false
		}
	}
	parsed, err := strconv.ParseFloat(raw, 64)
	return parsed, err == nil
}

func splitAcceptValues(header string) []string {
	var values []string
	start := 0
	quoted := false
	for index, char := range header {
		if char == '"' {
			quoted = !quoted
		}
		if char == ',' && !quoted {
			values = append(values, header[start:index])
			start = index + 1
		}
	}
	values = append(values, header[start:])
	return values
}

func mediaPreferenceAccepts(preferences []MediaPreference, contentType, transferSyntaxUID string) bool {
	_, ok := matchingMediaPreference(preferences, contentType, transferSyntaxUID, false)
	return ok
}

func matchingMediaPreference(preferences []MediaPreference, contentType, transferSyntaxUID string, requireMultipart bool) (MediaPreference, bool) {
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil {
		return MediaPreference{}, false
	}
	if transferSyntaxUID == "" {
		transferSyntaxUID = params["transfer-syntax"]
	}
	mediaType = strings.ToLower(strings.TrimSpace(mediaType))
	if requireMultipart {
		preference, ok := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, true)
		return preference, ok && preference.Quality > 0
	}
	direct, directOK := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, false)
	multipartPreference, multipartOK := bestMatchingMediaPreference(preferences, mediaType, transferSyntaxUID, true)
	if !directOK || direct.Quality <= 0 {
		if multipartOK && multipartPreference.Quality > 0 {
			return multipartPreference, true
		}
		return MediaPreference{}, false
	}
	if multipartOK && multipartPreference.Quality > direct.Quality {
		return multipartPreference, true
	}
	return direct, true
}

func bestMatchingMediaPreference(preferences []MediaPreference, mediaType, transferSyntaxUID string, multipart bool) (MediaPreference, bool) {
	bestSpecificity := -1
	var best MediaPreference
	found := false
	for _, preference := range preferences {
		if preference.Multipart != multipart && !(multipart && !preference.Multipart && preference.MediaType == "*/*") {
			continue
		}
		specificity, ok := mediaPreferenceSpecificity(preference, mediaType, transferSyntaxUID)
		if !ok {
			continue
		}
		if specificity > bestSpecificity || (specificity == bestSpecificity && preference.Quality > best.Quality) {
			best = preference
			best.Multipart = multipart
			bestSpecificity = specificity
			found = true
		}
	}
	return best, found
}

func mediaPreferenceSpecificity(preference MediaPreference, mediaType, transferSyntaxUID string) (int, bool) {
	specificity := 0
	switch {
	case preference.MediaType == mediaType:
		specificity = 200
	case preference.MediaType == "*/*":
		specificity = 0
	case strings.HasSuffix(preference.MediaType, "/*") && strings.HasPrefix(mediaType, strings.TrimSuffix(preference.MediaType, "*")):
		specificity = 100
	default:
		return 0, false
	}
	if preference.TransferSyntaxUID != "" {
		if preference.TransferSyntaxUID == "*" {
			specificity++
		} else {
			if preference.TransferSyntaxUID != transferSyntaxUID {
				return 0, false
			}
			specificity += 2
		}
	}
	return specificity, true
}

func normalizedDICOMPartContentType(contentType, transferSyntaxUID string) (string, error) {
	mediaType, _, err := mime.ParseMediaType(strings.TrimSpace(contentType))
	if err != nil && strings.TrimSpace(contentType) != "" {
		return "", err
	}
	if mediaType == "" {
		mediaType = "application/dicom"
	}
	if !strings.EqualFold(mediaType, "application/dicom") {
		return "", ErrUnsupported
	}
	return mediaTypeWithTransferSyntax("application/dicom", transferSyntaxUID), nil
}

func normalizedFrameContentType(contentType string) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		return "application/octet-stream", nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 {
		return "", ErrUnsupported
	}
	mediaType = strings.ToLower(mediaType)
	if isFrameMediaType(mediaType) {
		return mediaType, nil
	}
	return "", ErrUnsupported
}

func isFrameMediaType(mediaType string) bool {
	switch strings.ToLower(mediaType) {
	case "application/octet-stream", "application/x-deflate", "image/jpeg", "image/jls", "image/jp2", "image/jpx", "image/jphc", "image/jxl", "image/dicom-rle", "video/mpeg", "video/mp4", "video/h265":
		return true
	default:
		return false
	}
}

func frameMediaTypeMatchesTransferSyntax(mediaType, transferSyntaxUID string) bool {
	uid := transfer.NormalizeUID(transferSyntaxUID)
	if uid == "" {
		return true
	}
	switch strings.ToLower(mediaType) {
	case "application/octet-stream":
		return uid == transfer.ExplicitVRLittleEndian.UID || uid == transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID
	case "application/x-deflate":
		return uid == transfer.DeflatedImageFrameCompression.UID
	case "image/jpeg":
		switch uid {
		case transfer.JPEGBaseline.UID, transfer.JPEGExtended.UID, transfer.JPEGLosslessNonHierarchical.UID, transfer.JPEGLosslessSV1.UID:
			return true
		}
	case "image/dicom-rle":
		return uid == transfer.RLELossless.UID
	case "image/jls":
		return transfer.IsJPEGLSTransferSyntax(uid)
	case "image/jp2":
		return uid == transfer.JPEG2000LosslessOnly.UID || uid == transfer.JPEG2000.UID
	case "image/jpx":
		return uid == transfer.JPEG2000Part2Lossless.UID || uid == transfer.JPEG2000Part2.UID
	case "image/jphc":
		return uid == transfer.HTJ2KLossless.UID || uid == transfer.HTJ2KLosslessRPCL.UID || uid == transfer.HTJ2K.UID
	case "image/jxl":
		return transfer.IsJPEGXLTransferSyntax(uid)
	case "video/mpeg":
		switch uid {
		case transfer.MPEG2MPML.UID, transfer.MPEG2MPMLF.UID, transfer.MPEG2MPHL.UID, transfer.MPEG2MPHLF.UID:
			return true
		}
	case "video/mp4":
		switch uid {
		case transfer.MPEG4HP41.UID, transfer.MPEG4HP41F.UID, transfer.MPEG4HP41BD.UID, transfer.MPEG4HP41BDF.UID,
			transfer.MPEG4HP422D.UID, transfer.MPEG4HP422DF.UID, transfer.MPEG4HP423D.UID, transfer.MPEG4HP423DF.UID,
			transfer.MPEG4HP42STEREO.UID, transfer.MPEG4HP42STEREOF.UID:
			return true
		}
	case "video/h265":
		return uid == transfer.HEVCMP51.UID || uid == transfer.HEVCM10P51.UID
	}
	return false
}

func webServiceTransferSyntaxAllowed(transferSyntaxUID string) bool {
	uid := transfer.NormalizeUID(transferSyntaxUID)
	if uid == "" || uid == transfer.ImplicitVRLittleEndian.UID || uid == transfer.ExplicitVRBigEndian.UID {
		return false
	}
	_, ok := transfer.DefaultRegistry.Get(uid)
	return ok
}

func normalizedBulkContentType(contentType string) (string, error) {
	if strings.TrimSpace(contentType) == "" {
		return "application/octet-stream", nil
	}
	mediaType, params, err := mime.ParseMediaType(contentType)
	if err != nil || len(params) != 0 || !strings.EqualFold(mediaType, "application/octet-stream") {
		return "", ErrUnsupported
	}
	return "application/octet-stream", nil
}

func mediaTypeWithTransferSyntax(mediaType, transferSyntaxUID string) string {
	if strings.TrimSpace(transferSyntaxUID) == "" {
		return mediaType
	}
	return mime.FormatMediaType(mediaType, map[string]string{"transfer-syntax": strings.TrimSpace(transferSyntaxUID)})
}

type responseDatasetValidation struct {
	values    int
	estimated int64
}

func (s *Server) validateResponseDataset(ctx context.Context, dataset Dataset) error {
	state := responseDatasetValidation{}
	return s.validateResponseDatasetAtDepth(ctx, dataset, 1, &state)
}

func (s *Server) encodeResponseDataset(ctx context.Context, dataset Dataset) ([]byte, error) {
	if err := s.validateResponseDataset(ctx, dataset); err != nil {
		return nil, err
	}
	data, err := json.Marshal(dataset)
	if err != nil {
		return nil, ErrBackend
	}
	if int64(len(data)) > s.limits.MaxResponseBytes {
		return nil, ErrResourceLimit
	}
	if _, err := dicomjson.Unmarshal(data, std.Dictionary); err != nil {
		return nil, ErrBackend
	}
	return data, nil
}

func (s *Server) validateResponseDatasetAtDepth(ctx context.Context, dataset Dataset, depth int, state *responseDatasetValidation) error {
	if depth > s.limits.MaxJSONDepth {
		return ErrResourceLimit
	}
	for tag, element := range dataset {
		if err := ctx.Err(); err != nil {
			return err
		}
		state.values++
		if state.values > s.limits.MaxJSONValues {
			return ErrResourceLimit
		}
		state.estimated += int64(len(tag) + len(element.VR) + len(element.InlineBinary) + len(element.BulkDataURI) + 32)
		if len(element.InlineBinary) > s.limits.MaxJSONValueBytes || len(element.BulkDataURI) > s.limits.MaxJSONValueBytes || state.estimated > s.limits.MaxResponseBytes {
			return ErrResourceLimit
		}
		if len(tag) != 8 || tag != strings.ToUpper(tag) {
			return ErrBackend
		}
		parsedTag, err := core.ParseTag(tag)
		if err != nil || parsedTag.Group == 0x0002 {
			return ErrBackend
		}
		vr, err := core.ParseVR(element.VR)
		if err != nil {
			return ErrBackend
		}
		representations := 0
		if len(element.Value) > 0 {
			representations++
		}
		if element.InlineBinary != "" {
			representations++
		}
		if element.BulkDataURI != "" {
			representations++
		}
		if element.InlineBinary != "" {
			decoder := base64.NewDecoder(base64.StdEncoding.Strict(), strings.NewReader(element.InlineBinary))
			if _, err := io.Copy(io.Discard, decoder); err != nil {
				return ErrBackend
			}
		}
		if representations > 1 {
			return ErrBackend
		}
		if element.BulkDataURI != "" {
			parsed, err := url.Parse(element.BulkDataURI)
			if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return ErrBackend
			}
			if _, err := bulkDataToken(parsed.Path, parsed.RawPath, s.servicePath("/bulkdata/")); err != nil {
				return ErrBackend
			}
		}
		if vr == core.VRSQ {
			if element.InlineBinary != "" || element.BulkDataURI != "" {
				return ErrBackend
			}
			for _, value := range element.Value {
				state.values++
				if state.values > s.limits.MaxJSONValues {
					return ErrResourceLimit
				}
				switch item := value.(type) {
				case Dataset:
					if err := s.validateResponseDatasetAtDepth(ctx, item, depth+1, state); err != nil {
						return err
					}
				case map[string]Element:
					if err := s.validateResponseDatasetAtDepth(ctx, Dataset(item), depth+1, state); err != nil {
						return err
					}
				case map[string]any:
					data, err := json.Marshal(item)
					if err != nil {
						return ErrBackend
					}
					var nested Dataset
					if err := json.Unmarshal(data, &nested); err != nil {
						return ErrBackend
					}
					if err := s.validateResponseDatasetAtDepth(ctx, nested, depth+1, state); err != nil {
						return err
					}
				default:
					return ErrBackend
				}
			}
			continue
		}
		for _, value := range element.Value {
			state.values++
			if state.values > s.limits.MaxJSONValues {
				return ErrResourceLimit
			}
			size, ok := responseJSONScalarSize(value)
			if !ok {
				return ErrBackend
			}
			if size > s.limits.MaxJSONValueBytes {
				return ErrResourceLimit
			}
			state.estimated += int64(size*6 + 8)
			if state.estimated > s.limits.MaxResponseBytes {
				return ErrResourceLimit
			}
		}
	}
	return nil
}

func bulkDataToken(path, rawPath, prefix string) (string, error) {
	if rawPath != "" || !strings.HasPrefix(path, prefix) {
		return "", ErrInvalidRequest
	}
	token := strings.TrimPrefix(path, prefix)
	if token == "" {
		return "", ErrInvalidRequest
	}
	for _, segment := range strings.Split(token, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return "", ErrInvalidRequest
		}
	}
	return token, nil
}

func responseJSONScalarSize(value any) (int, bool) {
	switch typed := value.(type) {
	case nil:
		return 4, true
	case string:
		return len(typed), true
	case json.Number:
		return len(typed), true
	case float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return 32, true
	case map[string]any:
		total := 0
		for key, nested := range typed {
			if key != "Alphabetic" && key != "Ideographic" && key != "Phonetic" {
				return 0, false
			}
			text, ok := nested.(string)
			if !ok {
				return 0, false
			}
			total += len(key) + len(text)
		}
		return total, true
	case dicomjson.PersonNameComponents:
		return len(typed.Alphabetic) + len(typed.Ideographic) + len(typed.Phonetic), true
	case *dicomjson.PersonNameComponents:
		if typed == nil {
			return 4, true
		}
		return len(typed.Alphabetic) + len(typed.Ideographic) + len(typed.Phonetic), true
	default:
		return 0, false
	}
}

type jsonArraySpool struct {
	file      *os.File
	path      string
	maximum   int64
	size      int64
	count     int
	finalized bool
}

func newJSONArraySpool(directory string, maximum int64) (*jsonArraySpool, error) {
	file, err := os.CreateTemp(directory, ".dicomweb-json-*")
	if err != nil {
		return nil, ErrBackend
	}
	spool := &jsonArraySpool{file: file, path: file.Name(), maximum: maximum}
	if _, err := file.Write([]byte{'['}); err != nil {
		_ = spool.Close()
		return nil, ErrBackend
	}
	spool.size = 1
	return spool, nil
}

func (s *jsonArraySpool) Append(data []byte) error {
	if s == nil || s.file == nil || s.finalized {
		return ErrBackend
	}
	extra := int64(len(data))
	if s.count > 0 {
		extra++
	}
	if extra < 0 || extra > s.maximum-1 || s.size > s.maximum-1-extra {
		return ErrResourceLimit
	}
	if s.count > 0 {
		if _, err := s.file.Write([]byte{','}); err != nil {
			return ErrBackend
		}
		s.size++
	}
	if _, err := s.file.Write(data); err != nil {
		return ErrBackend
	}
	s.size += int64(len(data))
	s.count++
	return nil
}

func (s *jsonArraySpool) Finalize() error {
	if s == nil || s.file == nil || s.finalized {
		return ErrBackend
	}
	if s.size >= s.maximum {
		return ErrResourceLimit
	}
	if _, err := s.file.Write([]byte{']'}); err != nil {
		return ErrBackend
	}
	s.size++
	s.finalized = true
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return ErrBackend
	}
	return nil
}

func (s *jsonArraySpool) Close() error {
	if s == nil {
		return nil
	}
	var closeErr, removeErr error
	if s.file != nil {
		closeErr = s.file.Close()
		s.file = nil
	}
	if s.path != "" {
		removeErr = os.Remove(s.path)
		s.path = ""
	}
	if closeErr != nil || (removeErr != nil && !errors.Is(removeErr, os.ErrNotExist)) {
		return ErrBackend
	}
	return nil
}

func copyBounded(ctx context.Context, destination io.Writer, source io.Reader, maximum int64) (int64, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	buffer := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, err
		}
		remaining := maximum - total
		if remaining < 0 {
			return total, ErrResourceLimit
		}
		readSize := len(buffer)
		if int64(readSize) > remaining+1 {
			readSize = int(remaining + 1)
		}
		n, readErr := source.Read(buffer[:readSize])
		if n > 0 {
			if total+int64(n) > maximum {
				return total, ErrResourceLimit
			}
			written, writeErr := destination.Write(buffer[:n])
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written != n {
				return total, io.ErrShortWrite
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
		if n == 0 {
			return total, io.ErrNoProgress
		}
	}
}

func closeReader(reader io.Reader) error {
	if closer, ok := reader.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func withReaderClosed(reader io.Reader, consume func() error) (err error) {
	defer func() {
		closeErr := safeCloseReader(reader)
		if recovered := recover(); recovered != nil {
			panic(recovered)
		}
		if err == nil && closeErr != nil {
			err = ErrBackend
		}
	}()
	return consume()
}

func safeCloseReader(reader io.Reader) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return closeReader(reader)
}

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
func safeSearch(backend SearchBackend, ctx context.Context, req SearchRequest, yield func(Dataset) error) (result SearchResult, err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Search(ctx, req, yield)
}
func safeMetadata(backend MetadataBackend, ctx context.Context, req MetadataRequest, yield func(Dataset) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Metadata(ctx, req, yield)
}
func safeRetrieve(backend RetrieveBackend, ctx context.Context, req RetrieveRequest, yield func(RetrievePart) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.Retrieve(ctx, req, yield)
}
func safeFrames(backend FrameBackend, ctx context.Context, req FrameRequest, yield func(FramePartStream) error) (err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.RetrieveFrames(ctx, req, yield)
}
func safeBulkData(backend BulkDataBackend, ctx context.Context, req BulkDataRequest) (part BulkDataPart, err error) {
	defer func() {
		if recover() != nil {
			err = ErrBackend
		}
	}()
	return backend.RetrieveBulkData(ctx, req)
}
func safeStore(backend StoreBackend, ctx context.Context, req StoreRequest) (outcome StoreOutcome) {
	defer func() {
		if recover() != nil {
			outcome = StoreOutcome{Status: StoreStatusFailed, Err: ErrBackend}
		}
	}()
	return backend.Store(ctx, req)
}
