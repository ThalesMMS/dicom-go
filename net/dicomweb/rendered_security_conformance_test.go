package dicomweb

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const renderedSecurityInstancePath = "/studies/1.2/series/1.3/instances/1.4/rendered"

type renderedSecurityRenderer func(context.Context, RenderRequest, func(RenderedPart) error) error

func (render renderedSecurityRenderer) Render(ctx context.Context, request RenderRequest, yield func(RenderedPart) error) error {
	return render(ctx, request, yield)
}

func TestRenderedSecurityRejectsMalformedParametersBeforeRendering(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		accept string
		status int
	}{
		{name: "quality missing", path: renderedSecurityInstancePath + "?quality=", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "quality zero", path: renderedSecurityInstancePath + "?quality=0", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "quality above one hundred", path: renderedSecurityInstancePath + "?quality=101", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "quality is fractional", path: renderedSecurityInstancePath + "?quality=1.5", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "quality duplicated", path: renderedSecurityInstancePath + "?quality=80&quality=90", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "quality with lossless representation", path: renderedSecurityInstancePath + "?quality=80", accept: "image/png", status: http.StatusBadRequest},
		{name: "viewport has one field", path: renderedSecurityInstancePath + "?viewport=64", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "viewport zero width", path: renderedSecurityInstancePath + "?viewport=0,64", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "viewport negative height", path: renderedSecurityInstancePath + "?viewport=64,-1", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "viewport non finite crop", path: renderedSecurityInstancePath + "?viewport=8,8,NaN,0,8,8", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "viewport zero crop width", path: renderedSecurityInstancePath + "?viewport=8,8,0,0,0,8", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "unsupported viewport dimensions", path: renderedSecurityInstancePath + "?viewport=9,8", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "thumbnail crop forbidden", path: "/studies/1.2/series/1.3/instances/1.4/thumbnail?viewport=8,8,0,0,8,8", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "window missing function", path: renderedSecurityInstancePath + "?window=40,400", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "window zero width", path: renderedSecurityInstancePath + "?window=40,0,linear", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "window non finite center", path: renderedSecurityInstancePath + "?window=Inf,400,linear", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "window unsupported function", path: renderedSecurityInstancePath + "?window=40,400,power", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "window duplicated", path: renderedSecurityInstancePath + "?window=40,400,linear&window=50,500,sigmoid", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "frame zero", path: "/studies/1.2/series/1.3/instances/1.4/frames/0/rendered", accept: "image/jpeg", status: http.StatusBadRequest},
		{name: "duplicate frame", path: "/studies/1.2/series/1.3/instances/1.4/frames/1,1/rendered", accept: `multipart/related; type="image/jpeg"`, status: http.StatusBadRequest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(context.Context, RenderRequest, func(RenderedPart) error) error {
				calls.Add(1)
				return nil
			}), func(limits *ServerLimits) {
				limits.MaxRenderedPixels = 64
			})
			recorder := renderedSecurityRequest(server, test.path, test.accept, context.Background())
			if recorder.Code != test.status || calls.Load() != 0 {
				t.Fatalf("status=%d calls=%d body=%q, want status=%d and no renderer call", recorder.Code, calls.Load(), recorder.Body.String(), test.status)
			}
			if strings.Contains(recorder.Body.String(), test.path) {
				t.Fatalf("error payload leaked request target: %q", recorder.Body.String())
			}
		})
	}
}

func TestRenderedSecurityEnforcesDICOMwebAcceptRules(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		accept string
		status int
	}{
		{name: "query accept does not replace header", path: renderedSecurityInstancePath + "?accept=image/jpeg", status: http.StatusNotAcceptable},
		{name: "dicom media type", path: renderedSecurityInstancePath, accept: "application/dicom", status: http.StatusNotAcceptable},
		{name: "mixed DICOM and rendered media", path: renderedSecurityInstancePath, accept: "application/dicom, image/jpeg", status: http.StatusBadRequest},
		{name: "transfer syntax forbidden", path: renderedSecurityInstancePath, accept: "image/jpeg; transfer-syntax=1.2.840.10008.1.2.1", status: http.StatusNotAcceptable},
		{name: "unsupported media", path: renderedSecurityInstancePath, accept: "image/tiff", status: http.StatusNotAcceptable},
		{name: "explicit rejection", path: renderedSecurityInstancePath, accept: "image/jpeg;q=0", status: http.StatusNotAcceptable},
		{name: "query wildcard forbidden", path: renderedSecurityInstancePath + "?accept=image/*", accept: "image/*", status: http.StatusBadRequest},
		{name: "query and header incompatible", path: renderedSecurityInstancePath + "?accept=image/jpeg", accept: "image/png", status: http.StatusNotAcceptable},
		{name: "query mixes DICOM and rendered", path: renderedSecurityInstancePath + "?accept=application/dicom,image/jpeg", accept: "*/*", status: http.StatusBadRequest},
		{name: "multiple frames require multipart", path: "/studies/1.2/series/1.3/instances/1.4/frames/1,2/rendered", accept: "image/jpeg", status: http.StatusNotAcceptable},
		{name: "thumbnail rejects multipart", path: "/studies/1.2/thumbnail", accept: `multipart/related; type="image/jpeg"`, status: http.StatusNotAcceptable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var calls atomic.Int32
			server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(context.Context, RenderRequest, func(RenderedPart) error) error {
				calls.Add(1)
				return nil
			}), nil)
			recorder := renderedSecurityRequest(server, test.path, test.accept, context.Background())
			if recorder.Code != test.status || calls.Load() != 0 {
				t.Fatalf("status=%d calls=%d body=%q, want status=%d and no renderer call", recorder.Code, calls.Load(), recorder.Body.String(), test.status)
			}
		})
	}
}

func TestRenderedSecurityPassesValidatedBoundedRequest(t *testing.T) {
	var captured RenderRequest
	renderer := renderedSecurityRenderer(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		captured = request
		return yield(RenderedPart{
			Ref: request.Ref, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("jpeg"), Size: 4,
		})
	})
	server := newRenderedSecurityServer(t, renderer, func(limits *ServerLimits) { limits.MaxRenderedPixels = 1 << 20 })
	path := renderedSecurityInstancePath + "?viewport=32,16,,,8,-4&window=40,400,linear-exact&quality=75&annotation=patient,vendor-safe&iccprofile=srgb"
	recorder := renderedSecurityRequest(server, path, "image/jpeg", context.Background())
	if recorder.Code != http.StatusOK || recorder.Body.String() != "jpeg" {
		t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if captured.Viewport == nil || captured.Viewport.Width != 32 || captured.Viewport.Height != 16 || captured.Viewport.SourceX != nil || captured.Viewport.SourceY != nil ||
		captured.Viewport.SourceWidth == nil || *captured.Viewport.SourceWidth != 8 || captured.Viewport.SourceHeight == nil || *captured.Viewport.SourceHeight != -4 {
		t.Fatalf("viewport = %+v", captured.Viewport)
	}
	if captured.Window == nil || captured.Window.Center != 40 || captured.Window.Width != 400 || captured.Window.Function != "linear-exact" || !captured.QualitySet || captured.Quality != 75 {
		t.Fatalf("window/quality = %+v/%d set=%t", captured.Window, captured.Quality, captured.QualitySet)
	}
	if len(captured.Annotations) != 1 || captured.Annotations[0] != "patient" || captured.ICCProfile != "srgb" {
		t.Fatalf("annotations/ICC = %v/%q", captured.Annotations, captured.ICCProfile)
	}
	if captured.MaxPixels != 1<<20 || captured.MaxOutputBytes != DefaultServerLimits().MaxResponseBytes {
		t.Fatalf("renderer budgets = pixels:%d bytes:%d", captured.MaxPixels, captured.MaxOutputBytes)
	}
	if !strings.Contains(recorder.Header().Get("Warning"), "299") {
		t.Fatalf("Warning = %q", recorder.Header().Get("Warning"))
	}
	renderedSecurityAssertPrivateResponse(t, recorder)
}

func TestRenderedSecurityEnforcesFrameAndAggregatePixelBudgets(t *testing.T) {
	t.Run("frame list", func(t *testing.T) {
		var calls atomic.Int32
		server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(context.Context, RenderRequest, func(RenderedPart) error) error {
			calls.Add(1)
			return nil
		}), func(limits *ServerLimits) { limits.MaxFrames = 1 })
		recorder := renderedSecurityRequest(server, "/studies/1.2/series/1.3/instances/1.4/frames/1,2/rendered", `multipart/related; type="image/jpeg"`, context.Background())
		if recorder.Code != http.StatusRequestEntityTooLarge || calls.Load() != 0 {
			t.Fatalf("status=%d calls=%d body=%q", recorder.Code, calls.Load(), recorder.Body.String())
		}
	})

	t.Run("aggregate rendered pixels", func(t *testing.T) {
		server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
			for index := 0; index < 2; index++ {
				part := RenderedPart{Ref: InstanceRef{StudyInstanceUID: "1.2", SeriesInstanceUID: "1.3", SOPInstanceUID: "1.4"}, ContentType: "image/jpeg", Width: 8, Height: 8, Reader: strings.NewReader("jpeg"), Size: 4}
				if err := yield(part); err != nil {
					return err
				}
			}
			return nil
		}), func(limits *ServerLimits) { limits.MaxRenderedPixels = 64 })
		recorder := renderedSecurityRequest(server, "/studies/1.2/rendered", `multipart/related; type="image/jpeg"`, context.Background())
		if recorder.Code != http.StatusRequestEntityTooLarge || bytes.Contains(recorder.Body.Bytes(), []byte("jpeg")) {
			t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
		}
		renderedSecurityAssertPrivateResponse(t, recorder)
	})
}

func TestRenderedSecurityThumbnailNeverRequestsPatientAnnotation(t *testing.T) {
	var captured RenderRequest
	server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		captured = request
		return yield(RenderedPart{ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("thumb"), Size: 5})
	}), nil)
	recorder := renderedSecurityRequest(server, "/studies/1.2/thumbnail?annotation=patient&viewport=64,64", "image/jpeg", context.Background())
	if recorder.Code != http.StatusOK || !captured.Thumbnail || len(captured.Annotations) != 0 {
		t.Fatalf("status=%d thumbnail=%t annotations=%v body=%q", recorder.Code, captured.Thumbnail, captured.Annotations, recorder.Body.String())
	}
	renderedSecurityAssertPrivateResponse(t, recorder)
}

func TestRenderedSecurityRejectsOversizedOrInvalidRendererOutput(t *testing.T) {
	tests := []struct {
		name     string
		renderer renderedSecurityRenderer
		status   int
	}{
		{
			name: "declared output exceeds limit",
			renderer: func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
				return yield(RenderedPart{Ref: request.Ref, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("12345"), Size: 5})
			},
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name: "actual output exceeds limit",
			renderer: func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
				return yield(RenderedPart{Ref: request.Ref, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: strings.NewReader("12345"), Size: -1})
			},
			status: http.StatusRequestEntityTooLarge,
		},
		{
			name: "media differs from negotiation",
			renderer: func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
				return yield(RenderedPart{Ref: request.Ref, ContentType: "image/png", Width: 1, Height: 1, Reader: strings.NewReader("png"), Size: 3})
			},
			status: http.StatusNotAcceptable,
		},
		{
			name: "content type parameters forbidden",
			renderer: func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
				return yield(RenderedPart{Ref: request.Ref, ContentType: "image/jpeg; transfer-syntax=1.2", Width: 1, Height: 1, Reader: strings.NewReader("jpeg"), Size: 4})
			},
			status: http.StatusInternalServerError,
		},
		{
			name: "nil output reader",
			renderer: func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
				return yield(RenderedPart{Ref: request.Ref, ContentType: "image/jpeg", Width: 1, Height: 1, Size: 0})
			},
			status: http.StatusInternalServerError,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRenderedSecurityServer(t, test.renderer, func(limits *ServerLimits) {
				limits.MaxPartBytes = 4
				limits.MaxResponseBytes = 64
			})
			recorder := renderedSecurityRequest(server, renderedSecurityInstancePath, "image/jpeg", context.Background())
			if recorder.Code != test.status {
				t.Fatalf("status=%d body=%q, want %d", recorder.Code, recorder.Body.String(), test.status)
			}
			if test.name == "actual output exceeds limit" && bytes.Contains(recorder.Body.Bytes(), []byte("1234")) {
				t.Fatalf("oversized response published a successful payload prefix: %q", recorder.Body.String())
			}
			renderedSecurityAssertPrivateResponse(t, recorder)
		})
	}
}

func TestRenderedSecurityContainsRendererFailureAndPanic(t *testing.T) {
	tests := []struct {
		name     string
		renderer renderedSecurityRenderer
	}{
		{name: "error", renderer: func(context.Context, RenderRequest, func(RenderedPart) error) error {
			return errors.New("SECRET backend path")
		}},
		{name: "panic", renderer: func(context.Context, RenderRequest, func(RenderedPart) error) error { panic("SECRET panic") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := newRenderedSecurityServer(t, test.renderer, nil)
			recorder := renderedSecurityRequest(server, renderedSecurityInstancePath, "image/jpeg", context.Background())
			if recorder.Code != http.StatusInternalServerError || strings.Contains(recorder.Body.String(), "SECRET") {
				t.Fatalf("status=%d body=%q", recorder.Code, recorder.Body.String())
			}
		})
	}
}

func TestRenderedSecurityHonorsCancellationAndDeadline(t *testing.T) {
	t.Run("already canceled", func(t *testing.T) {
		var calls atomic.Int32
		server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(context.Context, RenderRequest, func(RenderedPart) error) error {
			calls.Add(1)
			return nil
		}), nil)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		recorder := renderedSecurityRequest(server, renderedSecurityInstancePath, "image/jpeg", ctx)
		if recorder.Code != statusClientClosedRequest || calls.Load() != 0 {
			t.Fatalf("status=%d calls=%d", recorder.Code, calls.Load())
		}
	})
	t.Run("server deadline reaches renderer", func(t *testing.T) {
		var sawCancellation atomic.Bool
		server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(ctx context.Context, _ RenderRequest, _ func(RenderedPart) error) error {
			<-ctx.Done()
			sawCancellation.Store(true)
			return ctx.Err()
		}), func(limits *ServerLimits) { limits.MaxDuration = 10 * time.Millisecond })
		recorder := renderedSecurityRequest(server, renderedSecurityInstancePath, "image/jpeg", context.Background())
		if recorder.Code != http.StatusGatewayTimeout || !sawCancellation.Load() {
			t.Fatalf("status=%d rendererCanceled=%t body=%q", recorder.Code, sawCancellation.Load(), recorder.Body.String())
		}
	})
}

func TestRenderedSecurityConcurrentRequestsAreRaceFree(t *testing.T) {
	server := newRenderedSecurityServer(t, renderedSecurityRenderer(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		return yield(RenderedPart{Ref: request.Ref, ContentType: "image/png", Width: 1, Height: 1, Reader: strings.NewReader("png"), Size: 3})
	}), nil)
	const workers = 24
	errors := make(chan string, workers)
	var group sync.WaitGroup
	for index := 0; index < workers; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			recorder := renderedSecurityRequest(server, renderedSecurityInstancePath+"?viewport=32,32", "image/png", context.Background())
			if recorder.Code != http.StatusOK || recorder.Body.String() != "png" {
				errors <- recorder.Body.String()
			}
		}()
	}
	group.Wait()
	close(errors)
	for result := range errors {
		t.Errorf("concurrent render failed: %q", result)
	}
}

func FuzzRenderedSecurityQueryParsing(f *testing.F) {
	for _, seed := range []string{"64,64", "0,1", "8,8,NaN,0,8,8", "8,8,,,8,-8", strings.Repeat("9", 128)} {
		f.Add(seed)
	}
	server := newRenderedSecurityServer(f, renderedSecurityRenderer(func(_ context.Context, request RenderRequest, yield func(RenderedPart) error) error {
		return yield(RenderedPart{Ref: request.Ref, ContentType: "image/jpeg", Width: 1, Height: 1, Reader: io.NopCloser(strings.NewReader("jpeg")), Size: 4})
	}), func(limits *ServerLimits) {
		limits.MaxQueryValueBytes = 1024
		limits.MaxRenderedPixels = 1 << 20
	})
	f.Fuzz(func(t *testing.T, value string) {
		if len(value) > 2048 {
			t.Skip()
		}
		path := renderedSecurityInstancePath + "?viewport=" + url.QueryEscape(value)
		_ = renderedSecurityRequest(server, path, "image/jpeg", context.Background())
	})
}

func newRenderedSecurityServer(t testing.TB, renderer Renderer, mutate func(*ServerLimits)) *Server {
	t.Helper()
	limits := DefaultServerLimits()
	if mutate != nil {
		mutate(&limits)
	}
	server, err := NewServer(ServerOptions{Backend: struct{}{}, Renderer: renderer, Limits: limits, AllowUnauthenticated: true})
	if err != nil {
		t.Fatalf("NewServer: %v", err)
	}
	return server
}

func renderedSecurityRequest(server *Server, path, accept string, ctx context.Context) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodGet, path, nil).WithContext(ctx)
	if accept != "" {
		request.Header.Set("Accept", accept)
	}
	recorder := httptest.NewRecorder()
	server.ServeHTTP(recorder, request)
	return recorder
}

func renderedSecurityAssertPrivateResponse(t testing.TB, recorder *httptest.ResponseRecorder) {
	t.Helper()
	cache := strings.ToLower(recorder.Header().Get("Cache-Control"))
	if !strings.Contains(cache, "private") || !strings.Contains(cache, "no-store") {
		t.Errorf("Cache-Control = %q, want private and no-store", cache)
	}
	if !strings.Contains(strings.ToLower(recorder.Header().Get("Vary")), "accept") {
		t.Errorf("Vary = %q, want Accept", recorder.Header().Get("Vary"))
	}
	if recorder.Header().Get("X-Content-Type-Options") != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", recorder.Header().Get("X-Content-Type-Options"))
	}
}
