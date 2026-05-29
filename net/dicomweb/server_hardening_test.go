package dicomweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/dicomjson"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

func TestServerQIDOAllSixResourcesEmptyAndWarnings(t *testing.T) {
	backend := completeServerTestBackend()
	var requests []SearchRequest
	remaining := 7
	backend.search = func(_ context.Context, req SearchRequest, _ func(Dataset) error) (SearchResult, error) {
		requests = append(requests, req)
		return SearchResult{Remaining: &remaining}, nil
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	tests := []struct {
		path  string
		level SearchLevel
	}{
		{"/studies", SearchLevelStudy},
		{"/series", SearchLevelSeries},
		{"/instances", SearchLevelInstance},
		{"/studies/1.2.3/series", SearchLevelSeries},
		{"/studies/1.2.3/instances", SearchLevelInstance},
		{"/studies/1.2.3/series/1.2.4/instances", SearchLevelInstance},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path, nil).WithContext(context.Background())
		request.Header.Set("Accept", "application/dicom+json")
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK || strings.TrimSpace(recorder.Body.String()) != "[]" {
			t.Fatalf("%s: status=%d body=%q", test.path, recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Warning"); got != "299 http://example.com: There are 7 additional results that can be requested" {
			t.Fatalf("%s: Warning = %q", test.path, recorder.Header().Get("Warning"))
		}
		if got := requests[len(requests)-1].Level; got != test.level {
			t.Fatalf("%s: level=%s want=%s", test.path, got, test.level)
		}
	}
}

func TestNewServerScavengesOnlyInactiveOwnedSpoolFiles(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	writeSpool := func(name string, contents []byte, age time.Duration) string {
		t.Helper()
		path := filepath.Join(directory, name)
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			t.Fatal(err)
		}
		modified := now.Add(-age)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
		return path
	}
	stale := writeSpool(".dicomweb-json-stale", []byte("stale"), 2*time.Hour)
	quotaCandidate := writeSpool(".dicomweb-stow-quota", []byte("12345678"), 30*time.Minute)
	active := writeSpool(".dicomweb-stow-active", []byte("12345678"), time.Minute)
	unrelated := writeSpool("unrelated", []byte("keep"), 2*time.Hour)
	symlinkTarget := writeSpool("symlink-target", []byte("keep"), 2*time.Hour)
	symlink := filepath.Join(directory, ".dicomweb-json-link")
	if err := os.Symlink(symlinkTarget, symlink); err != nil {
		t.Fatal(err)
	}

	if _, err := NewServer(ServerOptions{
		Backend: completeServerTestBackend(), AllowUnauthenticated: true, SpoolDirectory: directory,
		SpoolRetentionAge: time.Hour, SpoolAggregateQuotaBytes: 10,
		Limits: ServerLimits{MaxDuration: 10 * time.Minute},
	}); err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	for _, removed := range []string{stale, quotaCandidate} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned inactive spool entry %q remains: %v", filepath.Base(removed), err)
		}
	}
	for _, preserved := range []string{active, unrelated, symlink, symlinkTarget} {
		if _, err := os.Lstat(preserved); err != nil {
			t.Fatalf("preserved spool entry %q: %v", filepath.Base(preserved), err)
		}
	}
}

func TestScavengeDICOMwebSpoolContinuesAfterRemovalFailure(t *testing.T) {
	directory := t.TempDir()
	now := time.Now()
	failed := filepath.Join(directory, ".dicomweb-json-failed")
	removed := filepath.Join(directory, ".dicomweb-json-removed")
	for _, path := range []string{failed, removed} {
		if err := os.WriteFile(path, []byte("stale"), 0o600); err != nil {
			t.Fatal(err)
		}
		modified := now.Add(-2 * time.Hour)
		if err := os.Chtimes(path, modified, modified); err != nil {
			t.Fatal(err)
		}
	}
	removeFile := func(path string) error {
		if path == failed {
			return os.ErrPermission
		}
		return os.Remove(path)
	}
	err := scavengeDICOMwebSpoolWithRemove(ServerOptions{
		SpoolDirectory: directory, SpoolRetentionAge: time.Hour, SpoolAggregateQuotaBytes: 1,
	}, ServerLimits{MaxDuration: 10 * time.Minute}, now, removeFile)
	if err != nil {
		t.Fatalf("scavengeDICOMwebSpoolWithRemove() error = %v", err)
	}
	if _, err := os.Stat(failed); err != nil {
		t.Fatalf("entry with removal failure was not preserved: %v", err)
	}
	if _, err := os.Stat(removed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("scavenging did not continue to the next entry: %v", err)
	}
}

func TestServerRejectsInvalidQIDOAndUnsupportedMedia(t *testing.T) {
	backend := completeServerTestBackend()
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxQueryFields: 2, MaxQueryValueBytes: 4, MaxResults: 10}})
	tests := []struct {
		path   string
		accept string
		status int
	}{
		{"/studies?limit=-1", "application/dicom+json", http.StatusBadRequest},
		{"/studies?Limit=1", "application/dicom+json", http.StatusBadRequest},
		{"/studies?PatientID=a&PatientID=b", "application/dicom+json", http.StatusBadRequest},
		{"/studies?fuzzymatching=x", "application/dicom+json", http.StatusBadRequest},
		{"/studies?fuzzymatching=TRUE", "application/dicom+json", http.StatusBadRequest},
		{"/studies?includefield=ALL", "application/dicom+json", http.StatusBadRequest},
		{"/studies?patientname=x", "application/dicom+json", http.StatusBadRequest},
		{"/studies?00111010=x", "application/dicom+json", http.StatusBadRequest},
		{"/studies?00020010=x", "application/dicom+json", http.StatusBadRequest},
		{"/studies?limit=%201", "application/dicom+json", http.StatusBadRequest},
		{"/studies?UnknownAttribute=x", "application/dicom+json", http.StatusBadRequest},
		{"/studies?PatientID=12345", "application/dicom+json", http.StatusRequestEntityTooLarge},
		{"/studies?PatientID=1&StudyDate=2&SOPClassUID=3", "application/dicom+json", http.StatusRequestEntityTooLarge},
		{"/studies", "", http.StatusNotAcceptable},
		{"/studies", "application/dicom+xml", http.StatusNotAcceptable},
		{"/studies", "application/json", http.StatusNotAcceptable},
		{"/studies", "application/dicom+json;q=NaN", http.StatusNotAcceptable},
		{"/studies", "application/dicom+json;q=.5", http.StatusNotAcceptable},
		{"/studies", "application/dicom+json;q=01", http.StatusNotAcceptable},
		{"/studies", "application/dicom+json;q=0.1234", http.StatusNotAcceptable},
		{"/studies", "*/*;q=1, application/dicom+json;q=0", http.StatusNotAcceptable},
		{"/studies/1.02.3", `multipart/related; type="application/dicom"`, http.StatusBadRequest},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path, nil).WithContext(context.Background())
		request.Header.Set("Accept", test.accept)
		server.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s: status=%d want=%d body=%q", test.path, recorder.Code, test.status, recorder.Body.String())
		}
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(context.Background())
	request.Header.Add("Accept", "application/dicom+xml")
	request.Header.Add("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("multiple Accept fields status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerQIDOOffsetIsIndependentFromResultLimit(t *testing.T) {
	backend := completeServerTestBackend()
	backend.search = func(_ context.Context, req SearchRequest, _ func(Dataset) error) (SearchResult, error) {
		if req.Offset != 100001 || req.Limit != 1 {
			t.Fatalf("request = %+v", req)
		}
		return SearchResult{}, nil
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxResults: 10}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies?offset=100001&limit=1", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies?includefield=all&includefield=StudyDescription", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("includefield status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies?PatientID=1", nil).WithContext(context.Background())
	request.URL.RawQuery = "PatientID=1&PatientName=%zz"
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed query status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	limited, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxOffset: 3}})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies?offset=4", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	limited.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("offset limit status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerQIDODistinguishesAbsentAndZeroLimit(t *testing.T) {
	backend := completeServerTestBackend()
	var requests []SearchRequest
	backend.search = func(_ context.Context, req SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		requests = append(requests, req)
		err := yield(Dataset{"0020000D": {VR: "UI", Value: []any{"1.2.3"}}})
		if req.LimitSet && req.Limit == 0 {
			remaining := 1
			return SearchResult{Remaining: &remaining}, err
		}
		return SearchResult{}, err
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	for _, path := range []string{"/studies", "/studies?limit=0"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.Background())
		request.Header.Set("Accept", "application/dicom+json")
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
		if path == "/studies?limit=0" && recorder.Body.String() != "[]" {
			t.Fatalf("limit=0 body=%q", recorder.Body.String())
		}
	}
	if len(requests) != 2 || requests[0].LimitSet || !requests[1].LimitSet || requests[1].Limit != 0 {
		t.Fatalf("requests=%+v", requests)
	}
}

func TestServerQIDOSystemResultCapReturnsPartialSuccess(t *testing.T) {
	backend := completeServerTestBackend()
	backend.search = func(_ context.Context, req SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		if req.MaximumResults != 1 || !req.LimitSet || req.Limit != 100 {
			t.Fatalf("request=%+v", req)
		}
		for _, uid := range []string{"1.2.3", "1.2.4"} {
			if err := yield(Dataset{"0020000D": {VR: "UI", Value: []any{uid}}}); err != nil {
				remaining := 1
				return SearchResult{Remaining: &remaining}, err
			}
		}
		return SearchResult{}, nil
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxResults: 1}})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies?limit=100", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Warning"), "There are 1 additional results") {
		t.Fatalf("status=%d Warning=%q body=%q", recorder.Code, recorder.Header().Get("Warning"), recorder.Body.String())
	}
	var datasets []Dataset
	if err := json.Unmarshal(recorder.Body.Bytes(), &datasets); err != nil || len(datasets) != 1 {
		t.Fatalf("datasets=%v err=%v", datasets, err)
	}
}

func TestServerQIDOUnsupportedMatchingWarningsAreNormative(t *testing.T) {
	backend := completeServerTestBackend()
	backend.search = func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, nil
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies?fuzzymatching=true&emptyvaluematching=true&multiplevaluematching=true", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	warnings := strings.Join(recorder.Header().Values("Warning"), "\n")
	for _, sentence := range []string{
		"The fuzzymatching parameter is not supported. Only literal matching has been performed.",
		"The emptyvaluematching parameter is not supported. Empty Value Matching has not been performed.",
		"The multiplevaluematching parameter is not supported. Multiple Value Matching has not been performed.",
	} {
		if !strings.Contains(warnings, sentence) {
			t.Fatalf("Warning=%q missing %q", warnings, sentence)
		}
	}
}

func TestServerReturnsMethodNotAllowedForKnownResources(t *testing.T) {
	server, _ := NewServer(ServerOptions{Backend: completeServerTestBackend(), AllowUnauthenticated: true})
	for _, test := range []struct {
		method, path, allow string
	}{
		{http.MethodPut, "/studies", "GET, POST"},
		{http.MethodPost, "/instances", "GET"},
		{http.MethodPost, "/studies/1/series/2/instances/3", "GET"},
	} {
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, httptest.NewRequest(test.method, test.path, nil).WithContext(context.Background()))
		if recorder.Code != http.StatusMethodNotAllowed || recorder.Header().Get("Allow") != test.allow {
			t.Fatalf("%s %s status=%d Allow=%q", test.method, test.path, recorder.Code, recorder.Header().Get("Allow"))
		}
	}
}

func TestServerServiceRootKeepsGeneratedURLsInsideMount(t *testing.T) {
	backend := completeServerTestBackend()
	remaining := 1
	backend.search = func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error) {
		return SearchResult{Remaining: &remaining}, nil
	}
	backend.retrieve = func(_ context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
		return yield(RetrievePart{Ref: InstanceRef{StudyInstanceUID: req.StudyInstanceUID, SeriesInstanceUID: "1.2.3.1", SOPInstanceUID: "1.2.3.1.1"}, ContentType: "application/dicom", Reader: io.NopCloser(strings.NewReader("object"))})
	}
	backend.metadata = func(_ context.Context, _ MetadataRequest, yield func(Dataset) error) error {
		return yield(Dataset{"7FE00010": {VR: "OB", BulkDataURI: "/dicomweb/bulkdata/token"}})
	}
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, ServiceRoot: "/dicomweb"})
	if err != nil {
		t.Fatal(err)
	}
	httpServer := httptest.NewServer(http.StripPrefix("/dicomweb", server))
	defer httpServer.Close()

	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, httpServer.URL+"/dicomweb/studies", nil)
	request.Header.Set("Accept", "application/dicom+json")
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !strings.Contains(response.Header.Get("Warning"), httpServer.URL+"/dicomweb") {
		t.Fatalf("Warning=%q", response.Header.Get("Warning"))
	}
	request, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, httpServer.URL+"/dicomweb/studies/1.2.3/metadata", nil)
	request.Header.Set("Accept", "application/dicom+json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("metadata status=%d", response.StatusCode)
	}

	request, _ = http.NewRequestWithContext(context.Background(), http.MethodGet, httpServer.URL+"/dicomweb/studies/1.2.3", nil)
	request.Header.Set("Accept", `multipart/related; type="application/dicom"`)
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	mediaType, params, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if mediaType != "multipart/related" {
		t.Fatalf("Content-Type=%q", response.Header.Get("Content-Type"))
	}
	part, err := multipart.NewReader(response.Body, params["boundary"]).NextPart()
	if err != nil {
		t.Fatal(err)
	}
	if got := part.Header.Get("Content-Location"); !strings.HasPrefix(got, "/dicomweb/studies/") {
		t.Fatalf("Content-Location=%q", got)
	}
	_ = part.Close()
	_ = response.Body.Close()

	body, contentType := makeSTOWByteBody(t, stowTestFile())
	request, _ = http.NewRequestWithContext(context.Background(), http.MethodPost, httpServer.URL+"/dicomweb/studies/"+dicomtest.TestStudyInstanceUID, body)
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	response, err = http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if got := response.Header.Get("Location"); got != "/dicomweb/studies/"+dicomtest.TestStudyInstanceUID {
		t.Fatalf("Location=%q", got)
	}
	var storeResponse Dataset
	if err := json.NewDecoder(response.Body).Decode(&storeResponse); err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	urls := storeResponse["00081190"].Value
	if len(urls) != 1 || urls[0] != "/dicomweb/studies/"+dicomtest.TestStudyInstanceUID {
		t.Fatalf("RetrieveURL=%v", storeResponse["00081190"].Value)
	}
}

func TestServerAuthorizationPrecedesBodyReadAndBackendPanicIsRedacted(t *testing.T) {
	backend := completeServerTestBackend()
	var reads atomic.Int32
	body := &readSpy{reader: strings.NewReader("PATIENT^SECRET"), reads: &reads}
	server, _ := NewServer(ServerOptions{Backend: backend})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Content-Type", `multipart/related; type="application/dicom"; boundary=x`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized || reads.Load() != 0 {
		t.Fatalf("status=%d reads=%d", recorder.Code, reads.Load())
	}

	backend.search = func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error) {
		panic("PATIENT^SECRET")
	}
	server, _ = NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "SECRET") {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerSTOWStagesWholeMultipartBeforeBackendAndCleansSpool(t *testing.T) {
	spool := t.TempDir()
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		calls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, SpoolDirectory: spool})
	body, contentType := makeSTOWByteBody(t, stowTestFile())
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Content-Type", contentType)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotAcceptable || calls.Load() != 0 {
		t.Fatalf("missing Accept status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
	}
	malformed := "--broken\r\nContent-Type: application/dicom\r\n\r\npayload"
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/studies", strings.NewReader(malformed)).WithContext(context.Background())
	request.Header.Set("Content-Type", `multipart/related; type="application/dicom"; boundary=broken`)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || calls.Load() != 0 {
		t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool entries=%v err=%v", entries, err)
	}
}

func TestServerSTOWProvidesStagedDigestAndClientScopedInterop(t *testing.T) {
	backend := completeServerTestBackend()
	backend.store = func(_ context.Context, req StoreRequest) StoreOutcome {
		data, err := io.ReadAll(req.Reader)
		if err != nil || req.Size != int64(len(data)) || req.SHA256 != sha256.Sum256(data) || req.RouteStudyUID != dicomtest.TestStudyInstanceUID || req.Ref.StudyInstanceUID != dicomtest.TestStudyInstanceUID || req.SOPClassUID != dicomtest.TestSOPClassUID {
			return StoreOutcome{Status: StoreStatusFailed, Err: ErrBackend}
		}
		return StoreOutcome{Status: StoreStatusStored}
	}
	_, httpServer := newOpenTestServer(t, backend, nil)
	client := Client{Endpoint: Endpoint{BaseURL: httpServer.URL}}
	result, err := client.StoreInstancesToStudy(context.Background(), dicomtest.TestStudyInstanceUID, []StoreInstance{{Data: stowTestFile()}})
	if err != nil {
		t.Fatalf("StoreInstancesToStudy: %v", err)
	}
	wantURL := "/studies/" + dicomtest.TestStudyInstanceUID + "/series/" + dicomtest.TestSeriesInstanceUID + "/instances/" + dicomtest.TestSOPInstanceUID
	if result.StatusCode != http.StatusOK || len(result.Stored) != 1 || result.Stored[0].RetrieveURL != wantURL {
		t.Fatalf("result = %+v", result)
	}
}

func TestServerSTOWPartLimitIsExactAndNeverCallsBackendOnOverflow(t *testing.T) {
	part10 := stowTestFile()
	for _, test := range []struct {
		name    string
		payload []byte
		status  int
		calls   int32
	}{
		{"exact", part10, http.StatusOK, 1},
		{"over", append(append([]byte(nil), part10...), 0), http.StatusRequestEntityTooLarge, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			backend := completeServerTestBackend()
			var calls atomic.Int32
			backend.store = func(context.Context, StoreRequest) StoreOutcome {
				calls.Add(1)
				return StoreOutcome{Status: StoreStatusStored}
			}
			server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxPartBytes: int64(len(part10)), MaxRequestBytes: 1 << 20}})
			body, contentType := makeSTOWByteBody(t, test.payload)
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Accept", "application/dicom+json")
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status || calls.Load() != test.calls {
				t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
			}
		})
	}
}

func TestServerRejectsUnsafeBulkDataURIAndMislabeledWADO(t *testing.T) {
	backend := completeServerTestBackend()
	backend.metadata = func(context.Context, MetadataRequest, func(Dataset) error) error {
		return nil
	}
	backend.metadata = func(_ context.Context, _ MetadataRequest, yield func(Dataset) error) error {
		return yield(Dataset{"7FE00010": {VR: "OB", BulkDataURI: "https://evil.example/secret"}})
	}
	var closed atomic.Int32
	backend.retrieve = func(_ context.Context, _ RetrieveRequest, yield func(RetrievePart) error) error {
		return yield(RetrievePart{ContentType: "image/jpeg", Reader: &trackingReadCloser{Reader: strings.NewReader("jpeg"), closed: &closed}})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	for _, path := range []string{"/studies/1.2.3/metadata", "/studies/1.2.3"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.Background())
		if strings.HasSuffix(path, "metadata") {
			request.Header.Set("Accept", "application/dicom+json")
		} else {
			request.Header.Set("Accept", `multipart/related; type="application/dicom"`)
		}
		server.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusInternalServerError && recorder.Code != http.StatusNotAcceptable {
			t.Fatalf("%s: status=%d body=%q", path, recorder.Code, recorder.Body.String())
		}
	}
	if closed.Load() != 1 {
		t.Fatalf("WADO reader closed=%d", closed.Load())
	}
}

func TestServerClientStreamsStudySeriesAndInstanceMetadata(t *testing.T) {
	backend := completeServerTestBackend()
	backend.retrieve = func(_ context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
		ref := InstanceRef{StudyInstanceUID: req.StudyInstanceUID, SeriesInstanceUID: req.SeriesInstanceUID, SOPInstanceUID: "1.2.3.4.5"}
		if ref.SeriesInstanceUID == "" {
			ref.SeriesInstanceUID = "1.2.3.4"
		}
		return yield(RetrievePart{Ref: ref, ContentType: "application/dicom", Reader: io.NopCloser(strings.NewReader(string(req.Level)))})
	}
	backend.metadata = func(_ context.Context, req MetadataRequest, yield func(Dataset) error) error {
		return yield(Dataset{"00080018": {VR: "UI", Value: []any{req.SOPInstanceUID}}})
	}
	_, httpServer := newOpenTestServer(t, backend, nil)
	client := Client{Endpoint: Endpoint{BaseURL: httpServer.URL}}
	var payloads []string
	handle := func(located LocatedObjectPartStream) error {
		if located.ContentLocation == "" {
			t.Fatal("missing Content-Location")
		}
		data, err := io.ReadAll(located.Part.Reader)
		payloads = append(payloads, string(data))
		return err
	}
	if err := client.RetrieveStudyStreamWithOptions(context.Background(), "1.2.3", RetrieveOptions{}, handle); err != nil {
		t.Fatalf("RetrieveStudy: %v", err)
	}
	if err := client.RetrieveSeriesStreamWithOptions(context.Background(), "1.2.3", "1.2.3.4", RetrieveOptions{}, handle); err != nil {
		t.Fatalf("RetrieveSeries: %v", err)
	}
	metadata, err := client.InstanceMetadata(context.Background(), InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.4", SOPInstanceUID: "1.2.3.4.5"})
	if err != nil || len(metadata) != 1 || strings.Join(payloads, ",") != "study,series" {
		t.Fatalf("metadata=%v payloads=%v err=%v", metadata, payloads, err)
	}
}

func TestServerGracefulServeAndShutdown(t *testing.T) {
	backend := completeServerTestBackend()
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	deadline := time.Now().Add(2 * time.Second)
	for {
		response, requestErr := http.Get("http://" + listener.Addr().String() + "/studies")
		if requestErr == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("server did not start: %v", requestErr)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Serve: %v", err)
	}
}

func TestServerCurlInterop(t *testing.T) {
	if os.Getenv("DICOMGO_DICOMWEB_CURL") != "1" {
		t.Skip("set DICOMGO_DICOMWEB_CURL=1 to run the independent curl gate")
	}
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Skip("curl is unavailable")
	}
	backend := completeServerTestBackend()
	_, httpServer := newOpenTestServer(t, backend, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, curl, "--fail", "--silent", "--show-error", "--header", "Accept: application/dicom+json", httpServer.URL+"/studies")
	output, err := command.Output()
	if err != nil {
		t.Fatalf("curl QIDO: %v", err)
	}
	if strings.TrimSpace(string(output)) != "[]" {
		t.Fatalf("curl response = %q", output)
	}
}

func TestServerIdempotentStoreDecisionRemainsBackendAtomic(t *testing.T) {
	backend := completeServerTestBackend()
	var mu sync.Mutex
	seen := map[[32]byte]bool{}
	backend.store = func(_ context.Context, req StoreRequest) StoreOutcome {
		mu.Lock()
		duplicate := seen[req.SHA256]
		seen[req.SHA256] = true
		mu.Unlock()
		status := StoreStatusStored
		if duplicate {
			status = StoreStatusDuplicate
		}
		return StoreOutcome{Status: status}
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, StorePolicy: StorePolicyIdempotent})
	statuses := make(chan int, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			body, contentType := multipartBytesBody(stowTestFile())
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
			request.Header.Set("Content-Type", contentType)
			request.Header.Set("Accept", "application/dicom+json")
			server.ServeHTTP(recorder, request)
			statuses <- recorder.Code
		}()
	}
	group.Wait()
	close(statuses)
	for status := range statuses {
		if status != http.StatusOK {
			t.Fatalf("status=%d", status)
		}
	}
}

func TestServerWADONegotiatesWrappersScopesAndBackendMedia(t *testing.T) {
	backend := completeServerTestBackend()
	var closed atomic.Int32
	backend.retrieve = func(_ context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
		ref := InstanceRef{StudyInstanceUID: req.StudyInstanceUID, SeriesInstanceUID: req.SeriesInstanceUID, SOPInstanceUID: req.SOPInstanceUID}
		if ref.SeriesInstanceUID == "" {
			ref.SeriesInstanceUID = "1.2.3.4"
		}
		if ref.SOPInstanceUID == "" {
			ref.SOPInstanceUID = "1.2.3.4.5"
		}
		return yield(RetrievePart{Ref: ref, ContentType: "application/dicom", Reader: &trackingReadCloser{Reader: strings.NewReader("object"), closed: &closed}})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})

	tests := []struct {
		path, accept string
		status       int
		multipart    bool
	}{
		{"/studies/1.2.3", "application/dicom", http.StatusNotAcceptable, false},
		{"/studies/1.2.3", "*/*", http.StatusOK, true},
		{"/studies/1.2.3/series/1.2.3.4/instances/1.2.3.4.5", "application/dicom", http.StatusOK, false},
		{"/studies/1.2.3/series/1.2.3.4/instances/1.2.3.4.5", "", http.StatusNotAcceptable, false},
	}
	for _, test := range tests {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, test.path, nil).WithContext(context.Background())
		if test.accept != "" {
			request.Header.Set("Accept", test.accept)
		}
		server.ServeHTTP(recorder, request)
		if recorder.Code != test.status {
			t.Fatalf("%s Accept=%q status=%d body=%q", test.path, test.accept, recorder.Code, recorder.Body.String())
		}
		if recorder.Code == http.StatusOK {
			isMultipart := strings.HasPrefix(recorder.Header().Get("Content-Type"), "multipart/related")
			if isMultipart != test.multipart {
				t.Fatalf("%s Accept=%q Content-Type=%q", test.path, test.accept, recorder.Header().Get("Content-Type"))
			}
		}
	}

	backend.retrieve = func(_ context.Context, _ RetrieveRequest, yield func(RetrievePart) error) error {
		return yield(RetrievePart{Ref: InstanceRef{StudyInstanceUID: "9.9.9", SeriesInstanceUID: "9.9.9.1", SOPInstanceUID: "9.9.9.1.1"}, ContentType: "application/dicom", Reader: &trackingReadCloser{Reader: strings.NewReader("cross-scope"), closed: &closed}})
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies/1.2.3", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="application/dicom"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("cross-scope status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if closed.Load() != 3 {
		t.Fatalf("closed=%d", closed.Load())
	}
}

func TestServerAlwaysClosesPanickingReadersAndRejectsUnsafeMedia(t *testing.T) {
	backend := completeServerTestBackend()
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})

	for _, kind := range []string{"retrieve", "frame", "bulk"} {
		var closed atomic.Int32
		panicking := &panicReadCloser{closed: &closed}
		switch kind {
		case "retrieve":
			backend.retrieve = func(_ context.Context, _ RetrieveRequest, yield func(RetrievePart) error) error {
				return yield(RetrievePart{Ref: InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.4", SOPInstanceUID: "1.2.5"}, ContentType: "application/dicom", Reader: panicking})
			}
		case "frame":
			backend.frames = func(_ context.Context, _ FrameRequest, yield func(FramePartStream) error) error {
				return yield(FramePartStream{FrameNumber: 1, ContentType: "application/octet-stream", Reader: panicking})
			}
		case "bulk":
			backend.bulk = func(context.Context, BulkDataRequest) (BulkDataPart, error) {
				return BulkDataPart{ContentType: "application/octet-stream", Reader: panicking}, nil
			}
		}
		path := map[string]string{"retrieve": "/studies/1.2.3", "frame": "/studies/1/series/2/instances/3/frames/1", "bulk": "/bulkdata/token"}[kind]
		accept := map[string]string{"retrieve": `multipart/related; type="application/dicom"`, "frame": `multipart/related; type="application/octet-stream"`, "bulk": `multipart/related; type="application/octet-stream"`}[kind]
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(context.Background())
		request.Header.Set("Accept", accept)
		server.ServeHTTP(recorder, request)
		if closed.Load() != 1 {
			t.Fatalf("%s closed=%d", kind, closed.Load())
		}
	}

	var mediaClosed atomic.Int32
	backend.frames = func(_ context.Context, _ FrameRequest, yield func(FramePartStream) error) error {
		return yield(FramePartStream{FrameNumber: 1, ContentType: "text/plain\r\nX-Evil: yes", Reader: &trackingReadCloser{Reader: strings.NewReader("x"), closed: &mediaClosed}})
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3/frames/1", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="image/*"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || mediaClosed.Load() != 1 {
		t.Fatalf("unsafe frame status=%d closed=%d", recorder.Code, mediaClosed.Load())
	}
}

func TestServerFramesAcceptsNormativeCompressedWildcards(t *testing.T) {
	backend := completeServerTestBackend()
	backend.frames = func(_ context.Context, _ FrameRequest, yield func(FramePartStream) error) error {
		return yield(FramePartStream{FrameNumber: 1, ContentType: "image/jpeg", Reader: strings.NewReader("jpeg")})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3/frames/1", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="image/*"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Header().Get("Content-Type"), `type="image/jpeg"`) {
		t.Fatalf("status=%d Content-Type=%q body=%q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3/frames/1", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="image/*"; q=1, multipart/related; type="image/jpeg"; q=0`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusNotAcceptable {
		t.Fatalf("specific q=0 status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerFramesRejectsMismatchedMediaTypeAndTransferSyntax(t *testing.T) {
	if frameMediaTypeMatchesTransferSyntax("application/octet-stream", transfer.ImplicitVRLittleEndian.UID) ||
		frameMediaTypeMatchesTransferSyntax("application/octet-stream", transfer.ExplicitVRBigEndian.UID) ||
		!frameMediaTypeMatchesTransferSyntax("application/octet-stream", transfer.ExplicitVRLittleEndian.UID) ||
		!frameMediaTypeMatchesTransferSyntax("application/octet-stream", transfer.EncapsulatedUncompressedExplicitVRLittleEndian.UID) {
		t.Fatal("uncompressed frame media type accepted a non-DICOMweb transfer syntax")
	}
	backend := completeServerTestBackend()
	var closed atomic.Int32
	backend.frames = func(_ context.Context, _ FrameRequest, yield func(FramePartStream) error) error {
		return yield(FramePartStream{
			FrameNumber:       1,
			ContentType:       "image/jpeg",
			TransferSyntaxUID: transfer.RLELossless.UID,
			Reader:            &trackingReadCloser{Reader: strings.NewReader("rle"), closed: &closed},
		})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3/frames/1", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="image/*"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || closed.Load() != 1 {
		t.Fatalf("mismatched status=%d closed=%d body=%q", recorder.Code, closed.Load(), recorder.Body.String())
	}

	var secondAccepted atomic.Bool
	backend.frames = func(_ context.Context, _ FrameRequest, yield func(FramePartStream) error) error {
		if err := yield(FramePartStream{FrameNumber: 1, ContentType: "image/jpeg", TransferSyntaxUID: transfer.JPEGBaseline.UID, Reader: strings.NewReader("jpeg")}); err != nil {
			return err
		}
		err := yield(FramePartStream{FrameNumber: 2, ContentType: "image/jls", TransferSyntaxUID: transfer.JPEGLSLossless.UID, Reader: strings.NewReader("jls")})
		secondAccepted.Store(err == nil)
		return err
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3/frames/1,2", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="image/*"`)
	server.ServeHTTP(recorder, request)
	if !secondAccepted.Load() {
		t.Fatal("independently valid multipart media types were rejected")
	}
}

func TestServerValidatesDICOMJSONRepresentationsAndCleansResponseSpool(t *testing.T) {
	spool := t.TempDir()
	backend := completeServerTestBackend()
	backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, yield(Dataset{
			"00100010": {VR: "PN", Value: []any{dicomjson.PersonNameComponents{Alphabetic: "TEST^PERSON"}}},
			"7FE00010": {VR: "OB", InlineBinary: "AQIDBA=="},
		})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, SpoolDirectory: spool})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("valid status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, yield(Dataset{"00080018": {VR: "UI", Value: []any{123}}})
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("invalid UI status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	backend.search = func(_ context.Context, _ SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		return SearchResult{}, yield(Dataset{"7FE00010": {VR: "OB", InlineBinary: "%%%"}})
	}
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("invalid base64 status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	entries, err := os.ReadDir(spool)
	if err != nil || len(entries) != 0 {
		t.Fatalf("spool entries=%v err=%v", entries, err)
	}
}

func TestServerSTOWRootAcceptsMultipleStudiesAndBackendErrorsAreServerFailures(t *testing.T) {
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		calls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	first := stowTestFileWithRef(InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.1", SOPInstanceUID: "1.2.3.1.1"})
	second := stowTestFileWithRef(InstanceRef{StudyInstanceUID: "1.2.4", SeriesInstanceUID: "1.2.4.1", SOPInstanceUID: "1.2.4.1.1"})
	body, contentType := makeSTOWByteBody(t, first, second)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || calls.Load() != 2 || recorder.Header().Get("Location") != "" {
		t.Fatalf("multi-study status=%d calls=%d Location=%q body=%q", recorder.Code, calls.Load(), recorder.Header().Get("Location"), recorder.Body.String())
	}

	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		return StoreOutcome{Status: StoreStatusFailed, Err: errors.New("backend unavailable PATIENT^SECRET")}
	}
	body, contentType = makeSTOWByteBody(t, first)
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "SECRET") {
		t.Fatalf("backend status=%d body=%q", recorder.Code, recorder.Body.String())
	}
}

func TestServerSTOWReportsUnsupportedTransferSyntaxPerInstance(t *testing.T) {
	backend := completeServerTestBackend()
	var calls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		calls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	body, contentType := makeSTOWByteBody(t, stowTestFile(), stowTestFileWithUnsupportedTransferSyntax("1.2.840.10008.999.622"))
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Content-Type", contentType)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || calls.Load() != 1 {
		t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
	}
	result, err := StoreResultFromDICOMJSON(recorder.Body.Bytes())
	if err != nil || len(result.Failed) != 1 || result.Failed[0].FailureReason != 0xC122 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestServerRejectsTransferSyntaxesForbiddenInWebServices(t *testing.T) {
	for _, uid := range []string{transfer.ImplicitVRLittleEndian.UID, transfer.ExplicitVRBigEndian.UID} {
		if webServiceTransferSyntaxAllowed(uid) {
			t.Fatalf("webServiceTransferSyntaxAllowed(%q) = true", uid)
		}
	}
	if !webServiceTransferSyntaxAllowed(transfer.ExplicitVRLittleEndian.UID) {
		t.Fatal("Explicit VR Little Endian was rejected for Web Services")
	}

	backend := completeServerTestBackend()
	var retrieveClosed atomic.Int32
	backend.retrieve = func(_ context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
		return yield(RetrievePart{
			Ref:         InstanceRef{StudyInstanceUID: req.StudyInstanceUID, SeriesInstanceUID: req.SeriesInstanceUID, SOPInstanceUID: req.SOPInstanceUID},
			ContentType: "application/dicom", TransferSyntaxUID: transfer.ImplicitVRLittleEndian.UID,
			Reader: &trackingReadCloser{Reader: strings.NewReader("implicit"), closed: &retrieveClosed},
		})
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies/1/series/2/instances/3", nil).WithContext(context.Background())
	request.Header.Set("Accept", `multipart/related; type="application/dicom"`)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusInternalServerError || retrieveClosed.Load() != 1 {
		t.Fatalf("WADO status=%d closed=%d body=%q", recorder.Code, retrieveClosed.Load(), recorder.Body.String())
	}

	var storeCalls atomic.Int32
	backend.store = func(context.Context, StoreRequest) StoreOutcome {
		storeCalls.Add(1)
		return StoreOutcome{Status: StoreStatusStored}
	}
	ref := InstanceRef{StudyInstanceUID: "1.2.5", SeriesInstanceUID: "1.2.5.1", SOPInstanceUID: "1.2.5.1.1"}
	body, contentType := makeSTOWByteBody(t, stowTestFileWithRefAndSyntax(ref, transfer.ImplicitVRLittleEndian))
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodPost, "/studies", body).WithContext(context.Background())
	request.Header.Set("Accept", "application/dicom+json")
	request.Header.Set("Content-Type", contentType)
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusConflict || storeCalls.Load() != 0 {
		t.Fatalf("STOW status=%d calls=%d body=%q", recorder.Code, storeCalls.Load(), recorder.Body.String())
	}
	result, err := StoreResultFromDICOMJSON(recorder.Body.Bytes())
	if err != nil || len(result.Failed) != 1 || result.Failed[0].SOPInstanceUID != ref.SOPInstanceUID || result.Failed[0].FailureReason != 0xC122 {
		t.Fatalf("STOW result=%+v err=%v", result, err)
	}
}

type readSpy struct {
	reader io.Reader
	reads  *atomic.Int32
}

type panicReadCloser struct{ closed *atomic.Int32 }

func (*panicReadCloser) Read([]byte) (int, error) { panic("PATIENT^SECRET") }
func (p *panicReadCloser) Close() error {
	p.closed.Add(1)
	return nil
}

func (r *readSpy) Read(data []byte) (int, error) {
	r.reads.Add(1)
	return r.reader.Read(data)
}

func multipartBody(payload string) (*bytes.Buffer, string) {
	return multipartBytesBody([]byte(payload))
}

func multipartBytesBody(payload []byte) (*bytes.Buffer, string) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Type", "application/dicom")
	part, _ := writer.CreatePart(header)
	_, _ = part.Write(payload)
	_ = writer.Close()
	return &body, `multipart/related; type="application/dicom"; boundary=` + writer.Boundary()
}
