package dicomweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/internal/dicomtest"
	"github.com/ThalesMMS/dicom-go/transfer"
)

type serverTestBackend struct {
	search   func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error)
	metadata func(context.Context, MetadataRequest, func(Dataset) error) error
	retrieve func(context.Context, RetrieveRequest, func(RetrievePart) error) error
	frames   func(context.Context, FrameRequest, func(FramePartStream) error) error
	bulk     func(context.Context, BulkDataRequest) (BulkDataPart, error)
	store    func(context.Context, StoreRequest) StoreOutcome
	exists   func(context.Context, InstanceRef) (Existence, error)
}

func (b *serverTestBackend) Search(ctx context.Context, req SearchRequest, yield func(Dataset) error) (SearchResult, error) {
	return b.search(ctx, req, yield)
}
func (b *serverTestBackend) Metadata(ctx context.Context, req MetadataRequest, yield func(Dataset) error) error {
	return b.metadata(ctx, req, yield)
}
func (b *serverTestBackend) Retrieve(ctx context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
	return b.retrieve(ctx, req, yield)
}
func (b *serverTestBackend) RetrieveFrames(ctx context.Context, req FrameRequest, yield func(FramePartStream) error) error {
	return b.frames(ctx, req, yield)
}
func (b *serverTestBackend) RetrieveBulkData(ctx context.Context, req BulkDataRequest) (BulkDataPart, error) {
	return b.bulk(ctx, req)
}
func (b *serverTestBackend) Store(ctx context.Context, req StoreRequest) StoreOutcome {
	return b.store(ctx, req)
}
func (b *serverTestBackend) Exists(ctx context.Context, ref InstanceRef) (Existence, error) {
	return b.exists(ctx, ref)
}

func completeServerTestBackend() *serverTestBackend {
	return &serverTestBackend{
		search: func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error) {
			return SearchResult{}, nil
		},
		metadata: func(context.Context, MetadataRequest, func(Dataset) error) error { return nil },
		retrieve: func(context.Context, RetrieveRequest, func(RetrievePart) error) error { return nil },
		frames:   func(context.Context, FrameRequest, func(FramePartStream) error) error { return nil },
		bulk:     func(context.Context, BulkDataRequest) (BulkDataPart, error) { return BulkDataPart{}, ErrNotFound },
		store:    func(context.Context, StoreRequest) StoreOutcome { return StoreOutcome{Status: StoreStatusStored} },
		exists:   func(context.Context, InstanceRef) (Existence, error) { return ExistenceAbsent, nil },
	}
}

func newOpenTestServer(t *testing.T, backend any, mutate func(*ServerOptions)) (*Server, *httptest.Server) {
	t.Helper()
	opts := ServerOptions{Backend: backend, AllowUnauthenticated: true}
	if mutate != nil {
		mutate(&opts)
	}
	server, err := NewServer(opts)
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	httpServer := httptest.NewServer(server)
	t.Cleanup(httpServer.Close)
	return server, httpServer
}

func TestServerDefaultsFailClosedAndHooksArePHIFree(t *testing.T) {
	backend := completeServerTestBackend()
	server, err := NewServer(ServerOptions{Backend: backend})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/studies?PatientName=SECRET", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "SECRET") {
		t.Fatalf("response leaked query value: %q", recorder.Body.String())
	}

	var audit AuditEvent
	server, _ = NewServer(ServerOptions{
		Backend:   backend,
		Authorize: func(context.Context, *http.Request, Operation) error { return ErrForbidden },
		RequestID: func(*http.Request) string { return "request-1\r\nSECRET" },
		Audit:     func(_ context.Context, event AuditEvent) { audit = event },
	})
	recorder = httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/studies?PatientName=SECRET", nil))
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", recorder.Code)
	}
	if strings.Contains(recorder.Header().Get("X-Request-ID"), "SECRET") || strings.Contains(audit.RequestID, "SECRET") {
		t.Fatalf("unsafe request ID escaped sanitization: header=%q audit=%+v", recorder.Header().Get("X-Request-ID"), audit)
	}
	encoded, _ := json.Marshal(audit)
	if bytes.Contains(encoded, []byte("SECRET")) {
		t.Fatalf("audit leaked PHI: %s", encoded)
	}
}

func TestServerQIDOParsesQueriesAndInteroperatesWithClient(t *testing.T) {
	backend := completeServerTestBackend()
	backend.search = func(ctx context.Context, req SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		if req.Level != SearchLevelStudy || req.Limit != 2 || req.Offset != 3 || !req.FuzzyMatching {
			t.Fatalf("request = %+v", req)
		}
		if len(req.IncludeFields) != 2 || len(req.Filters) != 2 {
			t.Fatalf("query projection = %+v", req)
		}
		err := yield(Dataset{
			"0020000D": {VR: "UI", Value: []any{"1.2.3"}},
			"00081030": {VR: "LO", Value: []any{"description"}},
		})
		return SearchResult{FuzzyMatchingApplied: true}, err
	}
	_, httpServer := newOpenTestServer(t, backend, nil)
	client := Client{Endpoint: Endpoint{BaseURL: httpServer.URL}}
	params := url.Values{
		"limit":         {"2"},
		"offset":        {"3"},
		"fuzzymatching": {"true"},
		"includefield":  {"StudyDescription,00080020"},
		"PatientName":   {"DOE^JANE"},
		"00100020":      {"P123"},
	}
	got, err := client.SearchStudies(context.Background(), params)
	if err != nil {
		t.Fatalf("SearchStudies: %v", err)
	}
	if len(got) != 1 || got[0]["0020000D"].Value[0] != "1.2.3" {
		t.Fatalf("datasets = %#v", got)
	}
}

func TestServerWADOStreamsStudyMetadataFramesAndBulk(t *testing.T) {
	backend := completeServerTestBackend()
	var closed atomic.Int32
	backend.retrieve = func(ctx context.Context, req RetrieveRequest, yield func(RetrievePart) error) error {
		if req.Level != RetrieveLevelStudy || req.StudyInstanceUID != "1.2.3" {
			t.Fatalf("retrieve request = %+v", req)
		}
		for _, payload := range []string{"one", "two"} {
			reader := &trackingReadCloser{Reader: strings.NewReader(payload), closed: &closed}
			if err := yield(RetrievePart{Ref: InstanceRef{StudyInstanceUID: "1.2.3", SeriesInstanceUID: "1.2.3.1", SOPInstanceUID: "1.2.3.1.1"}, ContentType: "application/dicom", TransferSyntaxUID: "1.2.840.10008.1.2.1", Reader: reader}); err != nil {
				return err
			}
		}
		return nil
	}
	backend.metadata = func(ctx context.Context, req MetadataRequest, yield func(Dataset) error) error {
		return yield(Dataset{"00080018": {VR: "UI", Value: []any{"1.2.3.4"}}})
	}
	backend.frames = func(ctx context.Context, req FrameRequest, yield func(FramePartStream) error) error {
		for _, frame := range req.Frames {
			if err := yield(FramePartStream{FrameNumber: frame, ContentType: "application/octet-stream", Reader: strings.NewReader(string(rune('0' + frame)))}); err != nil {
				return err
			}
		}
		return nil
	}
	backend.bulk = func(ctx context.Context, req BulkDataRequest) (BulkDataPart, error) {
		if req.Token != "opaque/token" {
			t.Fatalf("bulk request = %+v", req)
		}
		return BulkDataPart{ContentType: "application/octet-stream", Reader: io.NopCloser(strings.NewReader("bulk"))}, nil
	}
	_, httpServer := newOpenTestServer(t, backend, nil)

	req, _ := http.NewRequest(http.MethodGet, httpServer.URL+"/studies/1.2.3", nil)
	req.Header.Set("Accept", `multipart/related; type="application/dicom"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("retrieve: %v", err)
	}
	parts := readMultipartStrings(t, resp)
	_ = resp.Body.Close()
	if strings.Join(parts, ",") != "one,two" || closed.Load() != 2 {
		t.Fatalf("parts=%v closed=%d", parts, closed.Load())
	}

	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/studies/1.2.3/metadata", nil)
	req.Header.Set("Accept", "application/dicom+json")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("metadata: %v", err)
	}
	var metadata []Dataset
	if err := json.NewDecoder(resp.Body).Decode(&metadata); err != nil {
		t.Fatalf("metadata JSON: %v", err)
	}
	_ = resp.Body.Close()
	if len(metadata) != 1 {
		t.Fatalf("metadata = %#v", metadata)
	}

	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/studies/1/series/2/instances/3/frames/1,2", nil)
	req.Header.Set("Accept", `multipart/related; type="application/octet-stream"`)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("frames: %v", err)
	}
	if got := strings.Join(readMultipartStrings(t, resp), ","); got != "1,2" {
		t.Fatalf("frames = %q", got)
	}
	_ = resp.Body.Close()

	req, _ = http.NewRequest(http.MethodGet, httpServer.URL+"/bulkdata/opaque/token", nil)
	req.Header.Set("Accept", `multipart/related; type="application/octet-stream"`)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("bulk: %v", err)
	}
	bulkParts := readMultipartStrings(t, resp)
	_ = resp.Body.Close()
	if len(bulkParts) != 1 || bulkParts[0] != "bulk" {
		t.Fatalf("bulk = %q", bulkParts)
	}
}

func TestServerSTOWStreamsMultiplePartsAndReportsPartialFailures(t *testing.T) {
	backend := completeServerTestBackend()
	var bodies [][]byte
	backend.store = func(ctx context.Context, req StoreRequest) StoreOutcome {
		data, err := io.ReadAll(req.Reader)
		if err != nil {
			return StoreOutcome{Status: StoreStatusFailed, Err: err}
		}
		bodies = append(bodies, data)
		if req.Index == 0 {
			return StoreOutcome{Status: StoreStatusStored}
		}
		return StoreOutcome{Status: StoreStatusConflict, FailureReason: 0x0111}
	}
	_, httpServer := newOpenTestServer(t, backend, nil)
	part10 := stowTestFile()
	body, contentType := makeSTOWByteBody(t, part10, part10)
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/studies/"+dicomtest.TestStudyInstanceUID, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/dicom+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("STOW: %v", err)
	}
	data, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", resp.StatusCode, data)
	}
	parsed, err := StoreResultFromDICOMJSON(data)
	if err != nil {
		t.Fatalf("StoreResultFromDICOMJSON: %v", err)
	}
	if len(parsed.Stored) != 1 || len(parsed.Failed) != 1 || len(bodies) != 2 || !bytes.Equal(bodies[0], part10) || !bytes.Equal(bodies[1], part10) {
		t.Fatalf("stored=%+v failed=%+v bodies=%v", parsed.Stored, parsed.Failed, bodies)
	}
}

func TestServerLimitsCancellationAndGracefulShutdown(t *testing.T) {
	backend := completeServerTestBackend()
	backend.store = func(ctx context.Context, req StoreRequest) StoreOutcome {
		_, err := io.Copy(io.Discard, req.Reader)
		return StoreOutcome{Status: StoreStatusStored, Err: err}
	}
	server, httpServer := newOpenTestServer(t, backend, func(opts *ServerOptions) {
		opts.Limits = ServerLimits{MaxRequestBytes: 64, MaxPartBytes: 4, MaxParts: 1, MaxDuration: time.Second}
	})
	body, contentType := makeSTOWBody(t, "oversized")
	req, _ := http.NewRequest(http.MethodPost, httpServer.URL+"/studies", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Accept", "application/dicom+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("oversized STOW: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", resp.StatusCode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/studies", nil).WithContext(ctx))
	if recorder.Code != 499 {
		t.Fatalf("canceled status = %d, want 499", recorder.Code)
	}
	if err := server.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown without Serve: %v", err)
	}
}

func TestServerConcurrencyBackpressure(t *testing.T) {
	backend := completeServerTestBackend()
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	backend.search = func(ctx context.Context, req SearchRequest, yield func(Dataset) error) (SearchResult, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return SearchResult{}, nil
		case <-ctx.Done():
			return SearchResult{}, ctx.Err()
		}
	}
	server, _ := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true, Limits: ServerLimits{MaxConcurrentRequests: 1}})
	first := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		request := httptest.NewRequest(http.MethodGet, "/studies", nil)
		request.Header.Set("Accept", "application/dicom+json")
		server.ServeHTTP(first, request)
		close(done)
	}()
	<-entered
	second := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies", nil)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(second, request)
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", second.Code)
	}
	close(release)
	<-done
}

type trackingReadCloser struct {
	io.Reader
	closed *atomic.Int32
}

func (r *trackingReadCloser) Close() error {
	r.closed.Add(1)
	return nil
}

func readMultipartStrings(t *testing.T, resp *http.Response) []string {
	t.Helper()
	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/related" || params["boundary"] == "" {
		t.Fatalf("Content-Type = %q: %v", resp.Header.Get("Content-Type"), err)
	}
	reader := multipart.NewReader(resp.Body, params["boundary"])
	var result []string
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil {
			t.Fatalf("NextPart: %v", err)
		}
		data, err := io.ReadAll(part)
		if err != nil {
			t.Fatalf("ReadAll: %v", err)
		}
		result = append(result, string(data))
	}
}

func makeSTOWBody(t *testing.T, payloads ...string) (io.Reader, string) {
	bytePayloads := make([][]byte, len(payloads))
	for index, payload := range payloads {
		bytePayloads[index] = []byte(payload)
	}
	return makeSTOWByteBody(t, bytePayloads...)
}

func stowTestFile() []byte {
	return stowTestFileWithRef(InstanceRef{
		StudyInstanceUID: dicomtest.TestStudyInstanceUID, SeriesInstanceUID: dicomtest.TestSeriesInstanceUID, SOPInstanceUID: dicomtest.TestSOPInstanceUID,
	})
}

func stowTestFileWithRef(ref InstanceRef) []byte {
	return stowTestFileWithRefAndSyntax(ref, transfer.ExplicitVRLittleEndian)
}

func stowTestFileWithRefAndSyntax(ref InstanceRef, syntax transfer.Syntax) []byte {
	elements := dicomtest.DatasetWithPixelData()
	replacements := map[core.Tag]string{
		core.NewTag(0x0008, 0x0018): ref.SOPInstanceUID,
		core.NewTag(0x0020, 0x000D): ref.StudyInstanceUID,
		core.NewTag(0x0020, 0x000E): ref.SeriesInstanceUID,
	}
	for index := range elements {
		if value, ok := replacements[elements[index].Tag()]; ok {
			elements[index] = dicomtest.StringElement(elements[index].Tag(), core.VRUI, value)
		}
	}
	data, err := dicomtest.Part10File(syntax, elements...)
	if err != nil {
		panic(err)
	}
	return data
}

func stowTestFileWithUnsupportedTransferSyntax(uid string) []byte {
	var data bytes.Buffer
	data.Write(make([]byte, 128))
	data.WriteString("DICM")
	data.Write(dicomtest.NewFileMetaBuilder().WithTransferSyntax(uid).Encode())
	data.Write(dicomtest.EncodeElements(transfer.ExplicitVRLittleEndian, dicomtest.DatasetWithPixelData()...))
	return data.Bytes()
}

func makeSTOWByteBody(t *testing.T, payloads ...[]byte) (io.Reader, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for _, payload := range payloads {
		header := textproto.MIMEHeader{}
		header.Set("Content-Type", "application/dicom")
		part, err := writer.CreatePart(header)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write(payload)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return bytes.NewReader(body.Bytes()), `multipart/related; type="application/dicom"; boundary=` + writer.Boundary()
}
