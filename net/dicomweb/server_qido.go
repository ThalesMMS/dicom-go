package dicomweb

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
)

var errSearchLimitReached = errors.New("dicomweb: search response limit reached")

func (s *Server) serveSearch(w *serverResponseWriter, r *http.Request, operation Operation) (int, error) {
	backend, ok := s.options.Backend.(SearchBackend)
	if !ok {
		return 0, newServerStatusError(http.StatusNotImplemented, "not_implemented", ErrUnsupported)
	}
	representation, ok := selectMetadataRepresentation(requestAcceptHeader(r))
	if !ok {
		return 0, newServerStatusError(http.StatusNotAcceptable, "not_acceptable", ErrUnsupported)
	}
	w.Header().Set("Vary", "Accept")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	request, err := s.parseSearchRequest(r, operation)
	if err != nil {
		return 0, err
	}
	request.MaximumResults = s.limits.MaxResults
	if request.LimitSet && request.Limit < request.MaximumResults {
		request.MaximumResults = request.Limit
	}
	spool, err := newDatasetResponseSpool(s.options.SpoolDirectory, s.limits.MaxResponseBytes, representation)
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
		return spool.Append(r.Context(), s, dataset, "")
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
	if err := spool.Finalize(r.Context(), s, true); err != nil {
		return 0, err
	}
	w.Header().Set("Content-Type", spool.contentType)
	_, err = copyBounded(r.Context(), w, spool.Reader(), s.limits.MaxResponseBytes)
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
