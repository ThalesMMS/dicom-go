package dicomweb

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
)

func FuzzServerSTOWMultipart(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("--fuzz-boundary--\r\n"))
	f.Add([]byte("--fuzz-boundary\r\nContent-Type: application/dicom\r\n\r\nnot-dicom\r\n--fuzz-boundary--\r\n"))

	server, err := NewServer(ServerOptions{
		Backend:              completeServerTestBackend(),
		AllowUnauthenticated: true,
		SpoolDirectory:       f.TempDir(),
		Limits: ServerLimits{
			MaxRequestBytes: 64 << 10,
			MaxPartBytes:    32 << 10,
			MaxParts:        4,
		},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		request := httptest.NewRequest(http.MethodPost, "/studies", bytes.NewReader(body))
		request.Header.Set("Content-Type", `multipart/related; type="application/dicom"; boundary=fuzz-boundary`)
		request.Header.Set("Accept", "application/dicom+json")
		recorder := httptest.NewRecorder()
		server.ServeHTTP(recorder, request)
		if recorder.Code < http.StatusOK || recorder.Code >= http.StatusInternalServerError {
			t.Fatalf("unexpected status %d for multipart body of %d bytes", recorder.Code, len(body))
		}
	})
}

func TestServerMiddlewareAndCoreOrderCharacterization(t *testing.T) {
	var calls []string
	backend := completeServerTestBackend()
	backend.search = func(context.Context, SearchRequest, func(Dataset) error) (SearchResult, error) {
		calls = append(calls, "backend")
		return SearchResult{}, nil
	}
	server, err := NewServer(ServerOptions{
		Backend: backend,
		Authorize: func(_ context.Context, _ *http.Request, operation Operation) error {
			calls = append(calls, "authorize:"+string(operation))
			return nil
		},
		Audit: func(context.Context, AuditEvent) {
			calls = append(calls, "audit")
		},
		Middleware: func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls = append(calls, "middleware:before")
				w.Header().Set("X-Characterization-Middleware", "applied")
				next.ServeHTTP(w, r)
				calls = append(calls, "middleware:after")
			})
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/studies", nil)
	request.Header.Set("Accept", "application/dicom+json")
	server.ServeHTTP(recorder, request)

	if recorder.Code != http.StatusOK || recorder.Header().Get("Content-Type") != "application/dicom+json" || recorder.Body.String() != "[]" {
		t.Fatalf("response = status %d content-type %q body %q", recorder.Code, recorder.Header().Get("Content-Type"), recorder.Body.String())
	}
	if recorder.Header().Get("X-Characterization-Middleware") != "applied" {
		t.Fatal("outer middleware header was not preserved")
	}
	wantCalls := []string{
		"middleware:before",
		"authorize:search_studies",
		"backend",
		"audit",
		"middleware:after",
	}
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("call order = %#v, want %#v", calls, wantCalls)
	}
}

func TestServerRouteResponseCharacterization(t *testing.T) {
	server, err := NewServer(ServerOptions{Backend: completeServerTestBackend(), AllowUnauthenticated: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name        string
		method      string
		path        string
		status      int
		allow       string
		contentType string
		body        string
	}{
		{name: "QIDO studies", method: http.MethodGet, path: "/studies", status: http.StatusOK, contentType: "application/dicom+json", body: "[]"},
		{name: "unknown route", method: http.MethodGet, path: "/patients", status: http.StatusNotFound, contentType: "text/plain; charset=utf-8", body: "DICOMweb request failed\n"},
		{name: "method not allowed", method: http.MethodDelete, path: "/studies", status: http.StatusMethodNotAllowed, allow: "GET, POST", contentType: "text/plain; charset=utf-8", body: "DICOMweb request failed\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(test.method, test.path, nil)
			request.Header.Set("Accept", "application/dicom+json")
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status || recorder.Header().Get("Allow") != test.allow || recorder.Header().Get("Content-Type") != test.contentType || recorder.Body.String() != test.body {
				t.Fatalf("response = status %d allow %q content-type %q body %q", recorder.Code, recorder.Header().Get("Allow"), recorder.Header().Get("Content-Type"), recorder.Body.String())
			}
		})
	}
}
