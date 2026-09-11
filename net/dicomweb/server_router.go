package dicomweb

import (
	"net/http"
	"net/url"
	"strings"
)

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
	case OperationRetrieveRendered, OperationRetrieveThumbnail:
		return s.serveRendered(w, r, operation == OperationRetrieveThumbnail)
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
		case "rendered":
			return OperationRetrieveRendered
		case "thumbnail":
			return OperationRetrieveThumbnail
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
		case "rendered":
			return OperationRetrieveRendered
		case "thumbnail":
			return OperationRetrieveThumbnail
		}
	}
	if len(parts) == 6 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" {
		return OperationRetrieveInstance
	}
	if len(parts) == 7 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" && parts[6] == "metadata" {
		return OperationRetrieveMetadata
	}
	if len(parts) == 7 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" {
		switch parts[6] {
		case "rendered":
			return OperationRetrieveRendered
		case "thumbnail":
			return OperationRetrieveThumbnail
		}
	}
	if len(parts) == 8 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" && parts[6] == "frames" {
		return OperationRetrieveFrames
	}
	if len(parts) == 9 && parts[0] == "studies" && parts[2] == "series" && parts[4] == "instances" && parts[6] == "frames" {
		switch parts[8] {
		case "rendered":
			return OperationRetrieveRendered
		case "thumbnail":
			return OperationRetrieveThumbnail
		}
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
