package dicomweb

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const safeEndpointLabel = "endpoint"

// SafeURL returns the non-sensitive part of an absolute HTTP(S) URL for use in
// errors and diagnostics. Credentials, query parameters, and fragments are
// always removed. Invalid, relative, and unsupported URLs fail closed to the
// generic word "endpoint".
func SafeURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return safeEndpointLabel
	}
	return safeParsedURL(parsed)
}

func safeParsedURL(value *url.URL) string {
	if value == nil || value.Host == "" ||
		(!strings.EqualFold(value.Scheme, "http") && !strings.EqualFold(value.Scheme, "https")) {
		return safeEndpointLabel
	}
	clean := *value
	clean.Scheme = strings.ToLower(clean.Scheme)
	clean.User = nil
	clean.RawQuery = ""
	clean.ForceQuery = false
	clean.Fragment = ""
	clean.RawFragment = ""
	clean.Opaque = ""
	return clean.String()
}

func safeDiagnosticURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return safeEndpointLabel
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return safeParsedURL(parsed)
}

func safeHTTPStatus(code int) string {
	if code <= 0 {
		return ""
	}
	if text := http.StatusText(code); text != "" {
		return fmt.Sprintf("%d %s", code, text)
	}
	return fmt.Sprintf("HTTP %d", code)
}

func safeOperationalError(err error) error {
	if err == nil {
		return nil
	}
	urlErr, ok := err.(*url.Error)
	if !ok {
		return err
	}
	clone := *urlErr
	clone.URL = safeDiagnosticURL(urlErr.URL)
	clone.Err = safeOperationalError(urlErr.Err)
	return &clone
}

func newDICOMwebError(kind ErrorKind, rawURL string, statusCode int, err error) *Error {
	return &Error{
		Kind:       kind,
		URL:        safeDiagnosticURL(rawURL),
		StatusCode: statusCode,
		Status:     safeHTTPStatus(statusCode),
		Err:        safeOperationalError(err),
	}
}
