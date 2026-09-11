package dicomweb

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

type renderTestRenderer830 func(context.Context, RenderRequest, func(RenderedPart) error) error

func (f renderTestRenderer830) Render(ctx context.Context, request RenderRequest, yield func(RenderedPart) error) error {
	return f(ctx, request, yield)
}

func newRenderedServer830(t *testing.T, renderer Renderer, limits ServerLimits) *Server {
	t.Helper()
	server, err := NewServer(ServerOptions{
		Backend:              completeServerTestBackend(),
		Renderer:             renderer,
		AllowUnauthenticated: true,
		Limits:               limits,
		ServiceRoot:          "/dicomweb",
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func TestServerRendered830RoutesAllRenderedAndThumbnailResources(t *testing.T) {
	tests := []struct {
		path      string
		operation Operation
		level     RenderLevel
		frames    int
	}{
		{"/studies/1.2/rendered", OperationRetrieveRendered, RenderLevelStudy, 0},
		{"/studies/1.2/thumbnail", OperationRetrieveThumbnail, RenderLevelStudy, 0},
		{"/studies/1.2/series/1.3/rendered", OperationRetrieveRendered, RenderLevelSeries, 0},
		{"/studies/1.2/series/1.3/thumbnail", OperationRetrieveThumbnail, RenderLevelSeries, 0},
		{"/studies/1.2/series/1.3/instances/1.4/rendered", OperationRetrieveRendered, RenderLevelInstance, 0},
		{"/studies/1.2/series/1.3/instances/1.4/thumbnail", OperationRetrieveThumbnail, RenderLevelInstance, 0},
		{"/studies/1.2/series/1.3/instances/1.4/frames/1,3/rendered", OperationRetrieveRendered, RenderLevelFrames, 2},
		{"/studies/1.2/series/1.3/instances/1.4/frames/1,3/thumbnail", OperationRetrieveThumbnail, RenderLevelFrames, 2},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if got := routeOperation(request); got != test.operation {
				t.Fatalf("operation=%q want %q", got, test.operation)
			}
			renderer := renderTestRenderer830(func(_ context.Context, got RenderRequest, yield func(RenderedPart) error) error {
				if got.Level != test.level || got.Thumbnail != (test.operation == OperationRetrieveThumbnail) || len(got.Frames) != test.frames {
					t.Fatalf("render request=%+v", got)
				}
				part := RenderedPart{ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("jpeg")}
				if got.Level == RenderLevelStudy {
					part.Ref = InstanceRef{StudyInstanceUID: got.Ref.StudyInstanceUID, SeriesInstanceUID: "1.3", SOPInstanceUID: "1.4"}
				} else if got.Level == RenderLevelSeries {
					part.Ref = InstanceRef{StudyInstanceUID: got.Ref.StudyInstanceUID, SeriesInstanceUID: got.Ref.SeriesInstanceUID, SOPInstanceUID: "1.4"}
				}
				if got.Level == RenderLevelFrames && !got.Thumbnail {
					for _, frame := range got.Frames {
						part.FrameNumber = frame
						if err := yield(part); err != nil {
							return err
						}
					}
					return nil
				}
				return yield(part)
			})
			server := newRenderedServer830(t, renderer, ServerLimits{})
			recorder := httptest.NewRecorder()
			if !strings.HasSuffix(test.path, "/thumbnail") && (test.level == RenderLevelStudy || test.level == RenderLevelSeries || test.level == RenderLevelFrames) {
				request.Header.Set("Accept", `multipart/related; type="image/jpeg"`)
			} else {
				request.Header.Set("Accept", "image/jpeg")
			}
			server.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestServerRendered830ParsesFrameAndRenderingParameters(t *testing.T) {
	var captured RenderRequest
	renderer := renderTestRenderer830(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		captured = request
		return yield(RenderedPart{FrameNumber: 2, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("jpeg")})
	})
	server := newRenderedServer830(t, renderer, ServerLimits{})
	request := httptest.NewRequest(http.MethodGet,
		"/studies/1.2/series/1.3/instances/1.4/frames/2/rendered?viewport=640,480,,,256,-128&window=42.5,100.25,linear-exact&quality=91&annotation=patient,technique&iccprofile=srgb", nil)
	request.Header.Set("Accept", "image/jpeg")
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "jpeg" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if captured.Level != RenderLevelFrames || len(captured.Frames) != 1 || captured.Frames[0] != 2 || captured.Quality != 91 || !captured.QualitySet || captured.ICCProfile != "srgb" {
		t.Fatalf("request=%+v", captured)
	}
	if captured.Viewport == nil || captured.Viewport.Width != 640 || captured.Viewport.Height != 480 || captured.Viewport.SourceX != nil || captured.Viewport.SourceY != nil || captured.Viewport.SourceWidth == nil || *captured.Viewport.SourceWidth != 256 || captured.Viewport.SourceHeight == nil || *captured.Viewport.SourceHeight != -128 {
		t.Fatalf("viewport=%+v", captured.Viewport)
	}
	if captured.Window == nil || captured.Window.Center != 42.5 || captured.Window.Width != 100.25 || captured.Window.Function != "linear-exact" || strings.Join(captured.Annotations, ",") != "patient,technique" {
		t.Fatalf("rendering parameters=%+v annotations=%v", captured.Window, captured.Annotations)
	}
	if got := recorder.Header().Get("Content-Location"); got != "/dicomweb/studies/1.2/series/1.3/instances/1.4/frames/2/rendered" {
		t.Fatalf("Content-Location=%q", got)
	}
}

func TestServerRendered830StreamsMultipartWithValidatedLocations(t *testing.T) {
	renderer := renderTestRenderer830(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		for index, sop := range []string{"1.4.1", "1.4.2"} {
			if err := yield(RenderedPart{
				Ref:         InstanceRef{StudyInstanceUID: request.Ref.StudyInstanceUID, SeriesInstanceUID: "1.3", SOPInstanceUID: sop},
				FrameNumber: index + 1,
				ContentType: "image/png",
				Width:       1,
				Height:      1,
				Reader:      strings.NewReader(sop),
			}); err != nil {
				return err
			}
		}
		return nil
	})
	server := newRenderedServer830(t, renderer, ServerLimits{})
	request := httptest.NewRequest(http.MethodGet, "/studies/1.2/rendered", nil)
	request.Header.Set("Accept", `multipart/related; type="image/png"`)
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	mediaType, params, err := mime.ParseMediaType(recorder.Header().Get("Content-Type"))
	if err != nil || mediaType != "multipart/related" || params["type"] != "image/png" {
		t.Fatalf("Content-Type=%q err=%v", recorder.Header().Get("Content-Type"), err)
	}
	reader := multipart.NewReader(strings.NewReader(recorder.Body.String()), params["boundary"])
	for index, sop := range []string{"1.4.1", "1.4.2"} {
		part, err := reader.NextPart()
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(part)
		if string(data) != sop || part.Header.Get("Content-Type") != "image/png" || !strings.HasSuffix(part.Header.Get("Content-Location"), "/instances/"+sop+"/frames/"+string(rune('1'+index))+"/rendered") {
			t.Fatalf("part=%d headers=%v body=%q", index, part.Header, data)
		}
	}
}

func TestServerRendered830RejectsMalformedNegotiationQueriesAndLimits(t *testing.T) {
	var called atomic.Int32
	renderer := renderTestRenderer830(func(_ context.Context, _ RenderRequest, yield func(RenderedPart) error) error {
		called.Add(1)
		return yield(RenderedPart{FrameNumber: 1, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("jpeg")})
	})
	tests := []struct {
		name   string
		path   string
		accept string
		status int
		limits ServerLimits
	}{
		{"missing Accept header", "/studies/1.2/thumbnail?accept=image/jpeg", "", http.StatusNotAcceptable, ServerLimits{}},
		{"DICOM media", "/studies/1.2/series/1.3/instances/1.4/rendered", "application/dicom", http.StatusNotAcceptable, ServerLimits{}},
		{"query wildcard", "/studies/1.2/series/1.3/instances/1.4/rendered?accept=image/*", "image/*", http.StatusBadRequest, ServerLimits{}},
		{"query incompatible", "/studies/1.2/series/1.3/instances/1.4/rendered?accept=image/png", "image/jpeg", http.StatusNotAcceptable, ServerLimits{}},
		{"mixed DICOM query", "/studies/1.2/series/1.3/instances/1.4/rendered?accept=application/dicom,image/jpeg", "*/*", http.StatusBadRequest, ServerLimits{}},
		{"duplicate quality", "/studies/1.2/series/1.3/instances/1.4/rendered?quality=80&quality=90", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"quality zero", "/studies/1.2/series/1.3/instances/1.4/rendered?quality=0", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"quality with PNG", "/studies/1.2/series/1.3/instances/1.4/rendered?quality=80", "image/png", http.StatusBadRequest, ServerLimits{}},
		{"NaN window", "/studies/1.2/series/1.3/instances/1.4/rendered?window=NaN,20,linear", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"nonpositive window", "/studies/1.2/series/1.3/instances/1.4/rendered?window=10,0,linear", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"thumbnail crop", "/studies/1.2/thumbnail?viewport=64,64,0,0,32,32", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"viewport limit", "/studies/1.2/thumbnail?viewport=11,10", "image/jpeg", http.StatusBadRequest, ServerLimits{MaxRenderedPixels: 100}},
		{"frame zero", "/studies/1.2/series/1.3/instances/1.4/frames/0/rendered", "image/jpeg", http.StatusBadRequest, ServerLimits{}},
		{"multiple frames single part", "/studies/1.2/series/1.3/instances/1.4/frames/1,2/rendered", "image/jpeg", http.StatusNotAcceptable, ServerLimits{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			called.Store(0)
			server := newRenderedServer830(t, renderer, test.limits)
			request := httptest.NewRequest(http.MethodGet, test.path, nil)
			if test.accept != "" {
				request.Header.Set("Accept", test.accept)
			}
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status || called.Load() != 0 {
				t.Fatalf("status=%d want=%d renderer calls=%d body=%q", recorder.Code, test.status, called.Load(), recorder.Body.String())
			}
		})
	}
}

func TestServerRendered830ClosesReadersAndRejectsBackendContractViolations(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		size        int64
		err         error
		status      int
	}{
		{"not found", "image/jpeg", 0, ErrNotFound, http.StatusNotFound},
		{"MIME mismatch", "image/png", 0, nil, http.StatusNotAcceptable},
		{"oversize declaration", "image/jpeg", 5, nil, http.StatusRequestEntityTooLarge},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var closed atomic.Int32
			renderer := renderTestRenderer830(func(_ context.Context, _ RenderRequest, yield func(RenderedPart) error) error {
				if test.err != nil {
					return test.err
				}
				return yield(RenderedPart{ContentType: test.contentType, Width: 1, Height: 1, Size: test.size, Reader: &trackingReadCloser{Reader: strings.NewReader("jpeg"), closed: &closed}})
			})
			server := newRenderedServer830(t, renderer, ServerLimits{MaxPartBytes: 4, MaxRequestBytes: 4})
			request := httptest.NewRequest(http.MethodGet, "/studies/1.2/series/1.3/instances/1.4/rendered", nil)
			request.Header.Set("Accept", "image/jpeg")
			recorder := httptest.NewRecorder()
			server.ServeHTTP(recorder, request)
			if recorder.Code != test.status {
				t.Fatalf("status=%d want=%d body=%q", recorder.Code, test.status, recorder.Body.String())
			}
			if test.err == nil && closed.Load() != 1 {
				t.Fatalf("reader closed=%d", closed.Load())
			}
			if recorder.Header().Get("Cache-Control") != "private, no-store" || recorder.Header().Get("Vary") != "Accept" {
				t.Fatalf("cache headers=%v", recorder.Header())
			}
		})
	}
}
