package dicomweb

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestSafeURLRemovesSensitiveComponentsAndFailsClosed(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  string
	}{
		{
			name:  "absolute HTTPS",
			value: "https://alice:basic-secret@pacs.example.test:8443/dicom-web/studies?PatientName=PHI%5ESECRET&access_token=bearer-secret#fragment-secret",
			want:  "https://pacs.example.test:8443/dicom-web/studies",
		},
		{
			name:  "escaped path retained",
			value: "http://pacs.example.test/a%2Fb?patient=secret",
			want:  "http://pacs.example.test/a%2Fb",
		},
		{name: "empty", value: "", want: "endpoint"},
		{name: "relative", value: "/studies?patient=secret", want: "endpoint"},
		{name: "unsupported scheme", value: "ftp://pacs.example.test/secret", want: "endpoint"},
		{name: "malformed", value: "https://pacs.example.test/%zz?patient=PHI-SECRET", want: "endpoint"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := SafeURL(test.value); got != test.want {
				t.Fatalf("SafeURL() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestEndpointRejectsBaseURLQueryAndFragment(t *testing.T) {
	const secret = "PHI-SECRET"
	for _, test := range []struct {
		name string
		base string
		want string
	}{
		{name: "query", base: "https://pacs.example.test/dicom-web?PatientName=" + secret, want: "base URL must not include a query"},
		{name: "empty query", base: "https://pacs.example.test/dicom-web?", want: "base URL must not include a query"},
		{name: "fragment", base: "https://pacs.example.test/dicom-web#" + secret, want: "base URL must not include a fragment"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := (Endpoint{BaseURL: test.base}).StudySearchURL(nil)
			var typed *Error
			if !errors.As(err, &typed) || typed.Kind != ErrorKindInvalidEndpoint {
				t.Fatalf("StudySearchURL() error = %#v", err)
			}
			if typed.Err == nil || typed.Err.Error() != test.want {
				t.Fatalf("underlying error = %v, want %q", typed.Err, test.want)
			}
			assertNoSensitiveText(t, fmt.Sprintf("%v", err), secret)
		})
	}
}

func TestDICOMwebOperationalErrorsKeepOnlySafeURLAndStatus(t *testing.T) {
	const (
		patientSecret = "PHI^SECRET"
		basicSecret   = "basic-secret"
		bearerSecret  = "bearer-secret"
	)

	t.Run("TLS", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeEmptyDICOMJSON(w)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, &http.Client{})
		client.Options.BasicPassword = basicSecret
		_, err := client.SearchStudies(context.Background(), url.Values{"PatientName": {patientSecret}})
		assertSafeDICOMwebError(t, err, server.URL, patientSecret, basicSecret)
	})

	t.Run("timeout", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			<-r.Context().Done()
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, server.Client())
		client.Options.BasicPassword = basicSecret
		client.Options.Timeout = 10 * time.Millisecond
		_, err := client.SearchStudies(context.Background(), url.Values{"PatientName": {patientSecret}})
		assertSafeDICOMwebError(t, err, server.URL, patientSecret, basicSecret)
		var typed *Error
		if !errors.As(err, &typed) || typed.Kind != ErrorKindTimeout {
			t.Fatalf("timeout error = %#v", err)
		}
	})

	t.Run("server controlled status", func(t *testing.T) {
		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusTeapot,
				Status:     "418 " + patientSecret + " " + bearerSecret,
				Header:     make(http.Header),
				Body:       http.NoBody,
				Request:    req,
			}, nil
		})}
		client := Client{
			Endpoint: Endpoint{BaseURL: "https://pacs.example.test/dicom-web", QIDOPath: "qido"},
			Options:  Options{HTTPClient: httpClient, BearerToken: bearerSecret},
		}
		_, err := client.SearchStudies(context.Background(), url.Values{"PatientName": {patientSecret}})
		assertSafeDICOMwebError(t, err, "https://pacs.example.test", patientSecret, bearerSecret)
		var typed *Error
		if !errors.As(err, &typed) || typed.Status != "418 I'm a teapot" {
			t.Fatalf("sanitized status = %#v", typed)
		}
	})

	t.Run("redirect", func(t *testing.T) {
		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "https://different.example.test/stolen?PatientName="+url.QueryEscape(patientSecret)+"&access_token="+bearerSecret, http.StatusFound)
		}))
		defer origin.Close()

		client := basicSecurityClient(origin.URL, origin.Client())
		client.Options.BasicPassword = basicSecret
		_, err := client.SearchStudies(context.Background(), url.Values{"PatientName": {patientSecret}})
		assertSafeDICOMwebError(t, err, origin.URL, patientSecret, basicSecret, bearerSecret)
		var urlErr *url.Error
		if !errors.As(err, &urlErr) || urlErr.URL != "https://different.example.test" {
			t.Fatalf("sanitized redirect cause = %#v", urlErr)
		}
	})
}

func TestDICOMwebErrorRemovesUIDPathSegments(t *testing.T) {
	const uid = "1.2.840.113619.2.55.3.604688123.781.1599722341.467"
	err := newDICOMwebError(ErrorKindDecodeResponse,
		"https://pacs.example.test/dicom-web/studies/"+uid+"/series/2.3", http.StatusBadGateway, errors.New("decode failed"))
	if err.URL != "https://pacs.example.test" {
		t.Fatalf("Error.URL = %q, want origin only", err.URL)
	}
	assertNoSensitiveText(t, err.Error(), uid)
}

func TestMalformedEndpointAndMultipartErrorsDoNotExposeURLs(t *testing.T) {
	const secret = "PHI-SECRET"
	client := Client{Endpoint: Endpoint{BaseURL: "https://pacs.example.test/%zz?PatientName=" + secret}}
	_, err := client.SearchStudies(context.Background(), nil)
	if err == nil {
		t.Fatal("malformed endpoint error = nil")
	}
	assertNoSensitiveText(t, fmt.Sprintf("%v", err), secret, "%zz")
	assertNoSensitiveText(t, fmt.Sprintf("%#v", err), secret, "%zz")
	assertNoSensitiveText(t, fmt.Sprint(errors.Unwrap(err)), secret, "%zz")

	response := Response{
		URL:    "https://alice:basic-secret@pacs.example.test/studies?PatientName=" + secret + "#bearer-secret",
		Header: http.Header{"Content-Type": {"multipart/related"}},
	}
	if _, err := objectPartsFromResponse(response); err == nil {
		t.Fatal("objectPartsFromResponse() error = nil")
	} else {
		assertNoSensitiveText(t, err.Error(), secret, "alice", "basic-secret", "bearer-secret")
		if !strings.Contains(err.Error(), "https://pacs.example.test/studies") {
			t.Fatalf("multipart error lost safe endpoint: %q", err)
		}
	}
	if err := (Client{}).streamObjectParts(response, strings.NewReader(""), dicomObjectResponsePolicy, func(LocatedObjectPartStream) error { return nil }); err == nil {
		t.Fatal("streamObjectParts() error = nil")
	} else {
		assertNoSensitiveText(t, err.Error(), secret, "alice", "basic-secret", "bearer-secret")
	}
}

func TestResponseFromHTTPNormalizesDiagnosticFields(t *testing.T) {
	response := responseFromHTTP(
		"https://alice:basic-secret@pacs.example.test/dicom-web/studies?PatientName=PHI-SECRET#bearer-secret",
		&http.Response{
			StatusCode: http.StatusTeapot,
			Status:     "418 PHI-SECRET bearer-secret",
			Header:     make(http.Header),
		},
	)
	if response.URL != "https://pacs.example.test/dicom-web/studies" {
		t.Fatalf("Response.URL = %q", response.URL)
	}
	if response.Status != "418 I'm a teapot" {
		t.Fatalf("Response.Status = %q", response.Status)
	}
}

func TestErrorFormattingIsDefensiveAndUnwrapIsPreserved(t *testing.T) {
	const secret = "PHI-SECRET"
	cause := errors.New("stable cause")
	err := &Error{
		Kind:       ErrorKindRequestFailure,
		URL:        "https://alice:basic-secret@pacs.example.test/studies?PatientName=" + secret + "#bearer-secret",
		StatusCode: http.StatusBadGateway,
		Status:     "502 " + secret,
		Err:        cause,
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		rendered := fmt.Sprintf(format, err)
		assertNoSensitiveText(t, rendered, secret, "alice", "basic-secret", "bearer-secret")
		if !strings.Contains(rendered, "https://pacs.example.test/studies") {
			t.Fatalf("format %s lost safe URL: %q", format, rendered)
		}
	}
	if !errors.Is(err, cause) || errors.Unwrap(err) != cause {
		t.Fatal("Error.Unwrap no longer preserves the cause")
	}
}

func assertSafeDICOMwebError(t *testing.T, err error, wantURL string, secrets ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("operation error = nil")
	}
	var typed *Error
	if !errors.As(err, &typed) {
		t.Fatalf("operation error type = %T, want *Error", err)
	}
	if typed.URL != wantURL {
		t.Fatalf("Error.URL = %q, want %q", typed.URL, wantURL)
	}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		assertNoSensitiveText(t, fmt.Sprintf(format, err), secrets...)
	}
	assertNoSensitiveText(t, typed.URL, secrets...)
}

func assertNoSensitiveText(t *testing.T, value string, secrets ...string) {
	t.Helper()
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			t.Fatalf("sensitive value %q appeared in %q", secret, value)
		}
	}
}
