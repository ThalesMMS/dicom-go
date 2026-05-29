package jpip

import (
	"context"
	"encoding/binary"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ThalesMMS/dicom-go/core"
	"github.com/ThalesMMS/dicom-go/dictionary/std"
	"github.com/ThalesMMS/dicom-go/dictionary/tags"
	"github.com/ThalesMMS/dicom-go/net/dicomweb"
	"github.com/ThalesMMS/dicom-go/object"
	"github.com/ThalesMMS/dicom-go/pixeldata"
	"github.com/ThalesMMS/dicom-go/transfer"
)

var jp2Fixture = []byte{
	0x00, 0x00, 0x00, 0x0c, 'j', 'P', ' ', ' ', '\r', '\n', 0x87, '\n',
	0x00, 0x00, 0x00, 0x08, 'f', 't', 'y', 'p',
}

type testBearerSource struct {
	mu          sync.Mutex
	tokens      []string
	tokenErr    error
	invalidated []string
}

func (s *testBearerSource) Token(context.Context) (dicomweb.AccessToken, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.tokenErr != nil {
		return dicomweb.AccessToken{}, s.tokenErr
	}
	if len(s.tokens) == 0 {
		return dicomweb.AccessToken{}, dicomweb.ErrBearerTokenUnavailable
	}
	return dicomweb.NewAccessToken(s.tokens[0], time.Time{})
}

func (s *testBearerSource) Invalidate(token dicomweb.AccessToken) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.invalidated = append(s.invalidated, token.Value())
	if len(s.tokens) > 1 && token.Value() == s.tokens[0] {
		s.tokens = s.tokens[1:]
	}
}

func TestRuleForHostRequiresNumericPort(t *testing.T) {
	for _, port := range []string{"http", "0", "65536", "-1"} {
		if _, err := RuleForHost("https", "jpip.example.test", port); err == nil {
			t.Fatalf("RuleForHost port %q succeeded, want error", port)
		}
	}
	if _, err := RuleForHost("https", "jpip.example.test", "00443"); err != nil {
		t.Fatal(err)
	}
}

func TestRuleFromURLUsesEffectiveDefaultPort(t *testing.T) {
	tests := []struct {
		raw      string
		wantPort string
	}{
		{raw: "http://jpip.example.test/path", wantPort: "80"},
		{raw: "https://jpip.example.test/path", wantPort: "443"},
		{raw: "https://jpip.example.test:8443/path", wantPort: "8443"},
	}
	for _, tt := range tests {
		rule, err := RuleFromURL(tt.raw)
		if err != nil {
			t.Fatal(err)
		}
		if rule.Port != tt.wantPort {
			t.Fatalf("RuleFromURL(%q).Port = %q, want %q", tt.raw, rule.Port, tt.wantPort)
		}
	}
}

func TestJPIPURLValidationErrorsRedactSensitiveInput(t *testing.T) {
	const sensitiveURL = "https://jpip-sensitive.example/%zz?token=provider-secret"
	tests := []struct {
		name string
		run  func() error
	}{
		{
			name: "rule",
			run: func() error {
				_, err := RuleFromURL(sensitiveURL)
				return err
			},
		},
		{
			name: "credential origin",
			run: func() error {
				_, err := NewClient(Config{Policy: Policy{
					Credentials: []BasicCredential{{Origin: sensitiveURL}},
				}})
				return err
			},
		},
		{
			name: "retrieve",
			run: func() error {
				client, err := NewClient(Config{})
				if err != nil {
					return err
				}
				_, err = client.Retrieve(context.Background(), Request{
					Reference: pixeldata.JPIPReference{PixelDataProviderURL: sensitiveURL},
				})
				return err
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run()
			if !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v, want ErrInvalidRequest", err)
			}
			for _, sensitive := range []string{sensitiveURL, "jpip-sensitive.example", "provider-secret"} {
				if strings.Contains(err.Error(), sensitive) {
					t.Fatalf("error = %q, leaked %q", err, sensitive)
				}
			}
		})
	}
}

func TestPolicyMatchesImplicitHTTPAndHTTPSDefaultPorts(t *testing.T) {
	tests := []struct {
		name   string
		rule   Rule
		target string
		want   bool
	}{
		{
			name:   "implicit HTTPS matches explicit 443",
			rule:   Rule{Scheme: "https", Host: "jpip.example.test", Port: "443"},
			target: "https://jpip.example.test/image",
			want:   true,
		},
		{
			name:   "implicit HTTP matches explicit 80",
			rule:   Rule{Scheme: "http", Host: "jpip.example.test", Port: "80"},
			target: "http://jpip.example.test/image",
			want:   true,
		},
		{
			name:   "implicit HTTPS does not match non-default port",
			rule:   Rule{Scheme: "https", Host: "jpip.example.test", Port: "8443"},
			target: "https://jpip.example.test/image",
		},
		{
			name:   "host rule without port still permits explicit service port",
			rule:   Rule{Scheme: "https", Host: "jpip.example.test"},
			target: "https://jpip.example.test:8443/image",
			want:   true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, err := compilePolicy(Policy{Rules: []Rule{tt.rule}})
			if err != nil {
				t.Fatal(err)
			}
			target, err := parseEndpointURL(tt.target)
			if err != nil {
				t.Fatal(err)
			}
			_, err = policy.authorize(target)
			if tt.want && err != nil {
				t.Fatalf("authorize() error = %v, want allowed", err)
			}
			if !tt.want && !errors.Is(err, ErrPolicyDenied) {
				t.Fatalf("authorize() error = %v, want ErrPolicyDenied", err)
			}
		})
	}
}

func TestPolicyRejectsBasicAndBearerCredentialsForSameOrigin(t *testing.T) {
	_, err := compilePolicy(Policy{
		Credentials: []BasicCredential{{
			Origin: "https://jpip.example.test",
		}},
		BearerCredentials: []BearerCredential{{
			Origin: "https://jpip.example.test:443/",
			Source: &testBearerSource{tokens: []string{"token"}},
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "both Basic and bearer") {
		t.Fatalf("compilePolicy() error = %v, want mixed-credential rejection", err)
	}
}

func TestClientEnforcesPolicyAndBuildsProgressiveQuery(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if got := r.URL.Query().Get("stream"); got != "3" {
			t.Errorf("stream = %q, want 3", got)
		}
		if got := r.URL.Query().Get("fsiz"); got != "640,480,closest" {
			t.Errorf("fsiz = %q", got)
		}
		if got := r.URL.Query().Get("roff"); got != "12,34" {
			t.Errorf("roff = %q", got)
		}
		if got := r.URL.Query().Get("rsiz"); got != "120,80" {
			t.Errorf("rsiz = %q", got)
		}
		w.Header().Set("Content-Type", "image/jp2")
		_, _ = w.Write(jp2Fixture)
	}))
	defer server.Close()

	rule, err := RuleFromURL(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{
		HTTPClient: server.Client(),
		Policy:     Policy{Rules: []Rule{rule}},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL + "/jpip?target=fixture"},
		Frame:     2,
		Size:      Size{Width: 640, Height: 480},
		Region:    Region{X: 12, Y: 34, Width: 120, Height: 80},
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.CacheHit || response.ContentType != "image/jp2" || len(response.Data) != len(jp2Fixture) {
		t.Fatalf("response = %#v", response)
	}

	denied, err := NewClient(Config{Policy: Policy{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = denied.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
	})
	if !errors.Is(err, ErrPolicyDenied) {
		t.Fatalf("denied error = %v, want ErrPolicyDenied", err)
	}
	if requests.Load() != 1 {
		t.Fatalf("server requests = %d, want 1", requests.Load())
	}
}

func TestClientRevalidatesRedirectAndDoesNotForwardCredentials(t *testing.T) {
	var redirectedAuthorizationMu sync.Mutex
	var redirectedAuthorization string
	var sourceRequests atomic.Int32
	redirectTarget := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorizationMu.Lock()
		redirectedAuthorization = r.Header.Get("Authorization")
		redirectedAuthorizationMu.Unlock()
		w.Header().Set("Content-Type", "image/jp2")
		_, _ = w.Write(jp2Fixture)
	}))
	defer redirectTarget.Close()

	redirectSource := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sourceRequests.Add(1) == 1 {
			user, password, ok := r.BasicAuth()
			if !ok || user != "alice" || password != "secret" {
				t.Errorf("source BasicAuth = (%q, %q, %t)", user, password, ok)
			}
		}
		http.Redirect(w, r, redirectTarget.URL+"/jpip", http.StatusFound)
	}))
	defer redirectSource.Close()

	sourceRule, _ := RuleFromURL(redirectSource.URL)
	targetRule, _ := RuleFromURL(redirectTarget.URL)
	client, err := NewClient(Config{
		HTTPClient: redirectSource.Client(),
		Policy: Policy{
			Rules: []Rule{sourceRule, targetRule},
			Credentials: []BasicCredential{{
				Origin: redirectSource.URL, Username: "alice", Password: "secret",
			}},
		},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: redirectSource.URL + "/jpip"},
	}); err != nil {
		t.Fatal(err)
	}
	redirectedAuthorizationMu.Lock()
	gotRedirectedAuthorization := redirectedAuthorization
	redirectedAuthorizationMu.Unlock()
	if gotRedirectedAuthorization != "" {
		t.Fatalf("Authorization forwarded to a different origin: %q", gotRedirectedAuthorization)
	}

	deniedClient, err := NewClient(Config{
		HTTPClient: redirectSource.Client(),
		Policy: Policy{
			Rules: []Rule{sourceRule},
		},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = deniedClient.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: redirectSource.URL + "/jpip"},
	})
	if !errors.Is(err, ErrRedirectDenied) {
		t.Fatalf("redirect error = %v, want ErrRedirectDenied", err)
	}
}

func TestClientBearerCredentialRefreshesOnceAfterUnauthorized(t *testing.T) {
	source := &testBearerSource{tokens: []string{"expired-token", "fresh-token"}}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer expired-token":
			w.WriteHeader(http.StatusUnauthorized)
		case "Bearer fresh-token":
			w.Header().Set("Content-Type", "image/jp2")
			_, _ = w.Write(jp2Fixture)
		default:
			t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
		}
	}))
	defer server.Close()

	rule, _ := RuleFromURL(server.URL)
	client, err := NewClient(Config{
		HTTPClient: server.Client(),
		Policy: Policy{
			Rules: []Rule{rule},
			BearerCredentials: []BearerCredential{{
				Origin: server.URL,
				Source: source,
			}},
		},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
	}); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want one challenge and one replay", requests.Load())
	}
	source.mu.Lock()
	invalidated := append([]string(nil), source.invalidated...)
	source.mu.Unlock()
	if len(invalidated) != 1 || invalidated[0] != "expired-token" {
		t.Fatalf("invalidated tokens = %v, want expired token once", invalidated)
	}
}

func TestClientBearerCredentialDoesNotLoopOnRepeatedUnauthorized(t *testing.T) {
	source := &testBearerSource{tokens: []string{"first-token", "second-token"}}
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()

	rule, _ := RuleFromURL(server.URL)
	client, err := NewClient(Config{
		HTTPClient: server.Client(),
		Policy: Policy{
			Rules: []Rule{rule},
			BearerCredentials: []BearerCredential{{
				Origin: server.URL,
				Source: source,
			}},
		},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
	})
	if !errors.Is(err, ErrHTTPStatus) {
		t.Fatalf("error = %v, want ErrHTTPStatus", err)
	}
	if requests.Load() != 2 {
		t.Fatalf("requests = %d, want exactly two", requests.Load())
	}
}

func TestClientBearerCredentialIsRedactedAndOriginBound(t *testing.T) {
	const secret = "provider-token-secret"
	failingSource := &testBearerSource{tokenErr: errors.New("refresh failed with " + secret)}
	failingServer := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("request reached server without a bearer token")
	}))
	defer failingServer.Close()
	failingRule, _ := RuleFromURL(failingServer.URL)
	failingClient, err := NewClient(Config{
		HTTPClient: failingServer.Client(),
		Policy: Policy{
			Rules: []Rule{failingRule},
			BearerCredentials: []BearerCredential{{
				Origin: failingServer.URL,
				Source: failingSource,
			}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = failingClient.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: failingServer.URL},
	})
	if !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("error = %v, want ErrBearerTokenUnavailable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error leaked token-source detail: %q", err)
	}

	source := &testBearerSource{tokens: []string{"origin-bound-token"}}
	var redirectedAuthorizationMu sync.Mutex
	var redirectedAuthorization string
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		redirectedAuthorizationMu.Lock()
		redirectedAuthorization = r.Header.Get("Authorization")
		redirectedAuthorizationMu.Unlock()
		w.Header().Set("Content-Type", "image/jp2")
		_, _ = w.Write(jp2Fixture)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer origin-bound-token" {
			t.Errorf("source Authorization = %q", got)
		}
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirect.Close()
	redirectRule, _ := RuleFromURL(redirect.URL)
	targetRule, _ := RuleFromURL(target.URL)
	redirectClient, err := NewClient(Config{
		HTTPClient: redirect.Client(),
		Policy: Policy{
			Rules: []Rule{redirectRule, targetRule},
			BearerCredentials: []BearerCredential{{
				Origin: redirect.URL,
				Source: source,
			}},
		},
		MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := redirectClient.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: redirect.URL},
	}); err != nil {
		t.Fatal(err)
	}
	redirectedAuthorizationMu.Lock()
	gotRedirectedAuthorization := redirectedAuthorization
	redirectedAuthorizationMu.Unlock()
	if gotRedirectedAuthorization != "" {
		t.Fatalf("bearer credential forwarded to different origin: %q", gotRedirectedAuthorization)
	}
}

func TestClientRetriesCachesAndBoundsResponses(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) == 1 {
			http.Error(w, "try again", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "image/jp2; profile=jpeg2000")
		_, _ = w.Write(jp2Fixture)
	}))
	defer server.Close()
	rule, _ := RuleFromURL(server.URL)
	client, err := NewClient(Config{
		HTTPClient: server.Client(),
		Policy:     Policy{Rules: []Rule{rule}},
		MaxRetries: 1,
		RetryDelay: func(int, *http.Response) time.Duration { return 0 },
	})
	if err != nil {
		t.Fatal(err)
	}
	request := Request{Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL}}
	first, err := client.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := client.Retrieve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.CacheHit || !second.CacheHit || attempts.Load() != 2 {
		t.Fatalf("cache/retry: first=%t second=%t attempts=%d", first.CacheHit, second.CacheHit, attempts.Load())
	}
	stats := client.CacheStats()
	if stats.Entries != 1 || stats.Hits != 1 || stats.Misses != 1 {
		t.Fatalf("cache stats = %+v", stats)
	}

	oversized, err := NewClient(Config{
		HTTPClient:       server.Client(),
		Policy:           Policy{Rules: []Rule{rule}},
		MaxResponseBytes: int64(len(jp2Fixture) - 1),
		MaxRetries:       1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = oversized.Retrieve(context.Background(), request)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("oversize error = %v, want ErrResponseTooLarge", err)
	}
}

func TestClientReturnsTypedContentPartialAndCancellationErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
	}{
		{
			name: "content type",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				_, _ = w.Write([]byte("not pixels"))
			},
			want: ErrUnsupportedContentType,
		},
		{
			name: "unexpected partial",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/jp2")
				w.Header().Set("Content-Range", "bytes 0-3/20")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write(jp2Fixture[:4])
			},
			want: ErrPartialResponse,
		},
		{
			name: "corrupt image",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/jp2")
				_, _ = w.Write([]byte("not a JPEG 2000 image"))
			},
			want: ErrCorruptResponse,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()
			rule, _ := RuleFromURL(server.URL)
			client, err := NewClient(Config{
				HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}}, MaxRetries: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Retrieve(context.Background(), Request{
				Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
			})
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want %v", err, tt.want)
			}
		})
	}

	blocked := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer blocked.Close()
	rule, _ := RuleFromURL(blocked.URL)
	client, err := NewClient(Config{
		HTTPClient: blocked.Client(), Policy: Policy{Rules: []Rule{rule}}, MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err = client.Retrieve(ctx, Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: blocked.URL},
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancel error = %v, want DeadlineExceeded", err)
	}
}

func TestClientValidatesRangedResponses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Range"); got != "bytes=4-11" {
			t.Errorf("Range = %q", got)
		}
		w.Header().Set("Content-Type", "image/jp2")
		w.Header().Set("Content-Range", "bytes 4-11/32")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte("midrange"))
	}))
	defer server.Close()
	rule, _ := RuleFromURL(server.URL)
	client, err := NewClient(Config{
		HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}}, MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Retrieve(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
		Range:     &ByteRange{Start: 4, End: 11},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Partial || string(response.Data) != "midrange" {
		t.Fatalf("response = %#v", response)
	}
}

func TestClientRejectsInvalidContentRanges(t *testing.T) {
	tests := []struct {
		name         string
		contentRange string
		body         string
	}{
		{name: "wrong start", contentRange: "bytes 3-10/32", body: "midrange"},
		{name: "wrong length", contentRange: "bytes 4-11/32", body: "short"},
		{name: "missing unit", contentRange: "4-11/32", body: "midrange"},
		{name: "end beyond request", contentRange: "bytes 4-12/32", body: "ninebytes!"},
		{name: "invalid total", contentRange: "bytes 4-11/11", body: "midrange"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "image/jp2")
				w.Header().Set("Content-Range", tt.contentRange)
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()
			rule, _ := RuleFromURL(server.URL)
			client, err := NewClient(Config{
				HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}}, MaxRetries: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Retrieve(context.Background(), Request{
				Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
				Range:     &ByteRange{Start: 4, End: 11},
			})
			if !errors.Is(err, ErrPartialResponse) {
				t.Fatalf("error = %v, want partial response", err)
			}
		})
	}
}

func TestDecodeFrameMapsReferencedSyntaxAndPreservesDataset(t *testing.T) {
	tests := []struct {
		name        string
		reference   pixeldata.JPIPReference
		contentType string
		wantUID     string
	}{
		{
			name: "JPEG 2000 deflated metadata",
			reference: pixeldata.JPIPReference{
				TransferSyntaxUID: transfer.JPIPReferencedDeflate.UID,
				Deflated:          true,
			},
			contentType: "image/jp2",
			wantUID:     transfer.JPEG2000.UID,
		},
		{
			name: "HTJ2K deflated metadata",
			reference: pixeldata.JPIPReference{
				TransferSyntaxUID: transfer.JPIPHTJ2KReferencedDeflate.UID,
				HTJ2K:             true,
				Deflated:          true,
			},
			contentType: "image/jph",
			wantUID:     transfer.HTJ2K.UID,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte{0xff, 0x4f, 0xff, 0xd9})
			}))
			defer server.Close()
			rule, _ := RuleFromURL(server.URL)
			registry := pixeldata.NewMemoryRegistry()
			codec := &recordingCodec{}
			if err := registry.RegisterCodec(tt.wantUID, codec); err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(Config{
				HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}},
				Registry: registry, MaxRetries: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			dataset := pixelMetadataDataset(3)
			tt.reference.PixelDataProviderURL = server.URL
			frame, err := client.DecodeFrame(context.Background(), Request{
				Reference: tt.reference,
				Frame:     1,
			}, dataset)
			if err != nil {
				t.Fatal(err)
			}
			if string(frame.Data) != "decoded" || frame.Metadata.NumberOfFrames != 1 {
				t.Fatalf("frame = %#v", frame)
			}
			if got, _ := dataset.GetString(tags.NumberOfFrames); got != "3" {
				t.Fatalf("source NumberOfFrames = %q, want 3", got)
			}
			if codec.calls.Load() != 1 {
				t.Fatalf("codec calls = %d", codec.calls.Load())
			}
		})
	}
}

func TestDecodeFrameReportsIncrementalStreamConsistently(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/jpt-stream")
		_, _ = w.Write([]byte("data-bin"))
	}))
	defer server.Close()
	rule, _ := RuleFromURL(server.URL)
	client, err := NewClient(Config{
		HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}},
		Registry: pixeldata.NewMemoryRegistry(), MaxRetries: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.DecodeFrame(context.Background(), Request{
		Reference: pixeldata.JPIPReference{PixelDataProviderURL: server.URL},
	}, pixelMetadataDataset(1))
	if !errors.Is(err, ErrIncrementalStream) {
		t.Fatalf("error = %v, want ErrIncrementalStream", err)
	}
}

func TestDecodeFrameRejectsContentTypeForDifferentCodecFamily(t *testing.T) {
	tests := []struct {
		name        string
		reference   pixeldata.JPIPReference
		contentType string
	}{
		{
			name:        "JPEG 2000 reference with HTJ2K response",
			reference:   pixeldata.JPIPReference{},
			contentType: "image/jph",
		},
		{
			name:        "HTJ2K reference with JPEG 2000 response",
			reference:   pixeldata.JPIPReference{HTJ2K: true},
			contentType: "image/jp2",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tt.contentType)
				_, _ = w.Write([]byte{0xff, 0x4f, 0xff, 0xd9})
			}))
			defer server.Close()
			rule, _ := RuleFromURL(server.URL)
			registry := pixeldata.NewMemoryRegistry()
			codec := &recordingCodec{}
			uid := transfer.JPEG2000.UID
			if tt.reference.HTJ2K {
				uid = transfer.HTJ2K.UID
			}
			if err := registry.RegisterCodec(uid, codec); err != nil {
				t.Fatal(err)
			}
			client, err := NewClient(Config{
				HTTPClient: server.Client(), Policy: Policy{Rules: []Rule{rule}},
				Registry: registry, MaxRetries: 1,
			})
			if err != nil {
				t.Fatal(err)
			}
			tt.reference.PixelDataProviderURL = server.URL
			_, err = client.DecodeFrame(context.Background(), Request{
				Reference: tt.reference,
			}, pixelMetadataDataset(1))
			if !errors.Is(err, ErrDecode) || !errors.Is(err, ErrUnsupportedContentType) {
				t.Fatalf("DecodeFrame() error = %v, want ErrDecode and ErrUnsupportedContentType", err)
			}
			if codec.calls.Load() != 0 {
				t.Fatalf("codec calls = %d, want none for mismatched content type", codec.calls.Load())
			}
		})
	}
}

type recordingCodec struct {
	calls atomic.Int32
}

func (c *recordingCodec) Decode(pixel pixeldata.PixelData, dataset *object.Object) (pixeldata.Frames, error) {
	c.calls.Add(1)
	if !pixel.Encapsulated || len(pixel.Sequence.Fragments) != 1 {
		return pixeldata.Frames{}, errors.New("unexpected encapsulation")
	}
	if frames, _ := dataset.GetString(tags.NumberOfFrames); frames != "1" {
		return pixeldata.Frames{}, errors.New("decoder dataset is not frame scoped")
	}
	return pixeldata.Frames{Rows: 1, Columns: 2, Data: [][]byte{[]byte("decoded")}}, nil
}

func pixelMetadataDataset(frames int) *object.Object {
	u16 := func(tag core.Tag, value uint16) core.Element {
		data := make([]byte, 2)
		binary.LittleEndian.PutUint16(data, value)
		return core.NewRawElement(tag, core.VRUS, data)
	}
	return object.FromElements([]core.Element{
		u16(tags.Rows, 1),
		u16(tags.Columns, 2),
		u16(tags.SamplesPerPixel, 1),
		core.NewRawElement(tags.PhotometricInterpretation, core.VRCS, []byte("MONOCHROME2 ")),
		u16(tags.BitsAllocated, 8),
		u16(tags.BitsStored, 8),
		u16(tags.HighBit, 7),
		u16(tags.PixelRepresentation, 0),
		core.NewRawElement(tags.NumberOfFrames, core.VRIS, []byte(strconv.Itoa(frames))),
	}, std.Dictionary)
}
