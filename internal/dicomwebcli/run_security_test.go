package dicomwebcli

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ThalesMMS/dicom-go/net/dicomweb"
)

func TestRunWithContextHelpAndParsing(t *testing.T) {
	for _, test := range []struct {
		name       string
		args       []string
		wantCode   int
		wantOutput string
	}{
		{name: "root help", args: []string{"help"}, wantCode: ExitOK, wantOutput: "verify|query|retrieve|frames|store"},
		{name: "go run separator", args: []string{"--", "help"}, wantCode: ExitOK, wantOutput: "verify|query|retrieve|frames|store"},
		{name: "short help", args: []string{"-h"}, wantCode: ExitOK, wantOutput: "Usage: dicomweb"},
		{name: "verify help", args: []string{"verify", "-h"}, wantCode: ExitOK, wantOutput: "dicomweb verify"},
		{name: "query help", args: []string{"query", "-h"}, wantCode: ExitOK, wantOutput: "dicomweb query"},
		{name: "retrieve help", args: []string{"retrieve", "-h"}, wantCode: ExitOK, wantOutput: "dicomweb retrieve"},
		{name: "frames help", args: []string{"frames", "-h"}, wantCode: ExitOK, wantOutput: "dicomweb frames"},
		{name: "store help", args: []string{"store", "-h"}, wantCode: ExitOK, wantOutput: "dicomweb store"},
		{name: "missing command", wantCode: ExitInput, wantOutput: "Usage: dicomweb"},
		{name: "unknown command", args: []string{"unknown"}, wantCode: ExitInput, wantOutput: "unknown subcommand"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := RunWithContext(context.Background(), test.args, &stdout, &stderr)
			if code != test.wantCode {
				t.Fatalf("code=%d, want %d; stdout=%q stderr=%q", code, test.wantCode, stdout.String(), stderr.String())
			}
			if combined := stdout.String() + stderr.String(); !strings.Contains(combined, test.wantOutput) {
				t.Fatalf("output=%q, want substring %q", combined, test.wantOutput)
			}
		})
	}
}

func TestCommonFlagsValidatePathsLimitsAndMetadata(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "missing base URL", args: []string{"verify"}},
		{name: "userinfo", args: []string{"verify", "-base-url", "https://user:secret@example.invalid"}},
		{name: "base URL query", args: []string{"verify", "-base-url", "https://example.invalid?token=secret"}},
		{name: "empty base URL query", args: []string{"verify", "-base-url", "https://example.invalid/path?"}},
		{name: "base URL fragment", args: []string{"verify", "-base-url", "https://example.invalid#fragment"}},
		{name: "timeout", args: []string{"verify", "-base-url", "https://example.invalid", "-timeout", "0s"}},
		{name: "body limit", args: []string{"verify", "-base-url", "https://example.invalid", "-max-body-bytes", "0"}},
		{name: "metadata", args: []string{"verify", "-base-url", "https://example.invalid", "-metadata", "yaml"}},
		{name: "basic pair", args: []string{"verify", "-base-url", "https://example.invalid", "-basic-user", "operator"}},
		{name: "auth conflict", args: []string{"verify", "-base-url", "https://example.invalid", "-basic-user", "operator", "-basic-password-env", "TEST_BASIC", "-bearer-token-env", "TEST_BEARER"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("TEST_BASIC", "basic-secret")
			t.Setenv("TEST_BEARER", "bearer-secret")
			var stdout, stderr bytes.Buffer
			if code := RunWithContext(context.Background(), test.args, &stdout, &stderr); code != ExitInput {
				t.Fatalf("code=%d, want %d; stderr=%q", code, ExitInput, stderr.String())
			}
			for _, secret := range []string{"basic-secret", "bearer-secret", "user:secret"} {
				if strings.Contains(stdout.String()+stderr.String(), secret) {
					t.Fatalf("diagnostic exposed %q: stdout=%q stderr=%q", secret, stdout.String(), stderr.String())
				}
			}
		})
	}
}

func TestCommonMetadataNegotiation(t *testing.T) {
	for _, test := range []struct {
		name     string
		metadata string
		wantJSON bool
		wantXML  bool
	}{
		{name: "json", metadata: "json", wantJSON: true},
		{name: "xml", metadata: "xml", wantXML: true},
		{name: "both", metadata: "both", wantJSON: true, wantXML: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				accept := r.Header.Get("Accept")
				if got := strings.Contains(accept, "application/dicom+json"); got != test.wantJSON {
					t.Errorf("Accept=%q, JSON present=%v want %v", accept, got, test.wantJSON)
				}
				if got := strings.Contains(accept, "application/dicom+xml"); got != test.wantXML {
					t.Errorf("Accept=%q, XML present=%v want %v", accept, got, test.wantXML)
				}
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			var stdout, stderr bytes.Buffer
			code := RunWithContext(context.Background(), []string{"verify", "-base-url", server.URL, "-metadata", test.metadata}, &stdout, &stderr)
			if code != ExitOK {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestCommonAuthenticationUsesEnvironmentAndRequiresHTTPOptIn(t *testing.T) {
	t.Setenv("DICOMWEBCLI_TEST_BASIC", "basic-secret-value")
	t.Setenv("DICOMWEBCLI_TEST_BEARER", "bearer-secret-value")

	t.Run("insecure auth rejected", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := RunWithContext(context.Background(), []string{
			"verify", "-base-url", "http://example.invalid", "-basic-user", "operator", "-basic-password-env", "DICOMWEBCLI_TEST_BASIC",
		}, &stdout, &stderr)
		if code != ExitInput {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if strings.Contains(stderr.String(), "basic-secret-value") {
			t.Fatalf("stderr exposed password: %q", stderr.String())
		}
	})

	t.Run("Basic cannot opt in to insecure HTTP", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			requests.Add(1)
		}))
		defer server.Close()
		var stdout, stderr bytes.Buffer
		code := RunWithContext(context.Background(), []string{
			"verify", "-base-url", server.URL, "-allow-insecure-auth",
			"-basic-user", "operator", "-basic-password-env", "DICOMWEBCLI_TEST_BASIC",
		}, &stdout, &stderr)
		if code != ExitInput {
			t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
		if requests.Load() != 0 {
			t.Fatalf("HTTP requests=%d, want 0", requests.Load())
		}
	})

	for _, test := range []struct {
		name       string
		flags      []string
		wantHeader string
	}{
		{name: "bearer with HTTP opt-in", flags: []string{"-bearer-token-env", "DICOMWEBCLI_TEST_BEARER"}, wantHeader: "Bearer bearer-secret-value"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var gotAuthorization string
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotAuthorization = r.Header.Get("Authorization")
				w.Header().Set("Content-Type", "application/dicom+json")
				_, _ = w.Write([]byte("[]"))
			})
			server := httptest.NewServer(handler)
			defer server.Close()

			args := []string{"verify", "-base-url", server.URL, "-allow-insecure-auth"}
			args = append(args, test.flags...)
			var stdout, stderr bytes.Buffer
			if code := RunWithContext(context.Background(), args, &stdout, &stderr); code != ExitOK {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if gotAuthorization != test.wantHeader {
				t.Fatalf("Authorization=%q, want %q", gotAuthorization, test.wantHeader)
			}
			for _, secret := range []string{"basic-secret-value", "bearer-secret-value"} {
				if strings.Contains(stdout.String()+stderr.String(), secret) {
					t.Fatalf("command output exposed %q", secret)
				}
			}
		})
	}
}

func TestClassifyExitMapsEveryDICOMwebErrorKind(t *testing.T) {
	for _, test := range []struct {
		kind dicomweb.ErrorKind
		want int
	}{
		{dicomweb.ErrorKindInvalidEndpoint, ExitInput},
		{dicomweb.ErrorKindAuthStatus, ExitAuth},
		{dicomweb.ErrorKindAuthToken, ExitAuth},
		{dicomweb.ErrorKindHTTPStatus, ExitHTTP},
		{dicomweb.ErrorKindRequestFailure, ExitFailure},
		{dicomweb.ErrorKindTimeout, ExitFailure},
		{dicomweb.ErrorKindDecodeResponse, ExitDecode},
	} {
		t.Run(string(test.kind), func(t *testing.T) {
			err := &dicomweb.Error{Kind: test.kind, URL: "https://user:secret@example.invalid/studies?PatientID=SECRET", StatusCode: http.StatusUnauthorized, Err: errors.New("bearer-secret")}
			if got := classifyExit(context.Background(), err); got != test.want {
				t.Fatalf("classifyExit(%s)=%d, want %d", test.kind, got, test.want)
			}
			var diagnostic bytes.Buffer
			writeDiagnostic(&diagnostic, "query", err, test.want)
			for _, forbidden := range []string{"user", "secret", "PatientID", "bearer"} {
				if strings.Contains(diagnostic.String(), forbidden) {
					t.Fatalf("diagnostic %q exposes %q", diagnostic.String(), forbidden)
				}
			}
			if !strings.Contains(diagnostic.String(), "endpoint=https://example.invalid/studies") {
				t.Fatalf("diagnostic lost safe host/path: %q", diagnostic.String())
			}
		})
	}
}

func TestWriteDiagnosticFailsClosedForMalformedSensitiveURL(t *testing.T) {
	const secret = "PHI-SECRET"
	err := &dicomweb.Error{
		Kind: dicomweb.ErrorKindRequestFailure,
		URL:  "https://alice:basic-secret@pacs.example.test/%zz?PatientName=" + secret + "#bearer-secret",
		Err:  errors.New("token=" + secret),
	}
	var diagnostic bytes.Buffer
	writeDiagnostic(&diagnostic, "retrieve", err, ExitFailure)
	if got := diagnostic.String(); got != "retrieve: kind=request_failure endpoint=endpoint\n" {
		t.Fatalf("diagnostic = %q", got)
	}
}

func TestRunWithContextCancellationIsRedacted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr bytes.Buffer
	code := RunWithContext(ctx, []string{"verify", "-base-url", "https://example.invalid"}, &stdout, &stderr)
	if code != ExitCanceled || !strings.Contains(stderr.String(), "canceled") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}
