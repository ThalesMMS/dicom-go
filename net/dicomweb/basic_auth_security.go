package dicomweb

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const maxBasicAuthRedirects = 10

var (
	// ErrInsecureBasicAuth reports that Basic credentials would be used without
	// verified, origin-bound HTTPS. The error never includes credentials.
	ErrInsecureBasicAuth = errors.New("DICOMweb Basic authentication requires verified HTTPS")
)

func (c Client) usesBasicAuth() bool {
	return c.Options.BearerTokenSource == nil &&
		strings.TrimSpace(c.Options.BearerToken) == "" &&
		strings.TrimSpace(c.Options.BasicUsername) != ""
}

func (c Client) httpClientForRequest(target *url.URL) (*http.Client, error) {
	httpClient := c.Options.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if !c.usesBasicAuth() {
		return httpClient, nil
	}
	if err := validateBasicAuthTarget(target); err != nil {
		return nil, err
	}
	transport, err := secureBasicAuthTransport(httpClient.Transport)
	if err != nil {
		return nil, err
	}

	// Copy the client value so a reusable caller-owned client retains its
	// redirect policy, transport, cookie jar, and timeout unchanged.
	secured := *httpClient
	secured.Transport = transport
	originalRedirect := httpClient.CheckRedirect
	origin := &url.URL{Scheme: target.Scheme, Host: target.Host}
	secured.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) >= maxBasicAuthRedirects {
			return fmt.Errorf("%w: redirect limit exceeded", ErrInsecureBasicAuth)
		}
		if err := validateBasicAuthRedirect(origin, req.URL); err != nil {
			return err
		}
		if originalRedirect != nil {
			if err := originalRedirect(req, via); err != nil {
				return err
			}
			// A custom callback can mutate req.URL. Revalidate after it returns so
			// credentials cannot be redirected by that mutation.
			if err := validateBasicAuthRedirect(origin, req.URL); err != nil {
				return err
			}
		}
		return nil
	}
	return &secured, nil
}

func validateBasicAuthTarget(target *url.URL) error {
	if target == nil || !strings.EqualFold(target.Scheme, "https") || strings.TrimSpace(target.Host) == "" {
		return fmt.Errorf("%w: endpoint must use HTTPS", ErrInsecureBasicAuth)
	}
	if target.User != nil {
		return fmt.Errorf("%w: URL user information is not allowed", ErrInsecureBasicAuth)
	}
	return nil
}

func secureBasicAuthTransport(roundTripper http.RoundTripper) (*http.Transport, error) {
	if roundTripper == nil {
		roundTripper = http.DefaultTransport
	}
	transport, ok := roundTripper.(*http.Transport)
	if !ok || transport == nil {
		return nil, fmt.Errorf("%w: Basic authentication requires a verifiable HTTP transport", ErrInsecureBasicAuth)
	}
	if transport.DialTLS != nil || transport.DialTLSContext != nil {
		return nil, fmt.Errorf("%w: custom TLS dialers are not allowed", ErrInsecureBasicAuth)
	}
	secured := transport.Clone()
	if secured.TLSClientConfig != nil && secured.TLSClientConfig.InsecureSkipVerify {
		return nil, fmt.Errorf("%w: TLS certificate verification is disabled", ErrInsecureBasicAuth)
	}
	return secured, nil
}

func validateBasicAuthRedirect(origin, destination *url.URL) error {
	if err := validateBasicAuthTarget(destination); err != nil {
		return fmt.Errorf("%w: redirect must remain on HTTPS", ErrInsecureBasicAuth)
	}
	if origin == nil || !sameHTTPSOrigin(origin, destination) {
		return fmt.Errorf("%w: redirect must remain on the original origin", ErrInsecureBasicAuth)
	}
	return nil
}

func sameHTTPSOrigin(left, right *url.URL) bool {
	if left == nil || right == nil ||
		!strings.EqualFold(left.Scheme, "https") ||
		!strings.EqualFold(right.Scheme, "https") ||
		!strings.EqualFold(left.Hostname(), right.Hostname()) {
		return false
	}
	return effectiveHTTPSPort(left) == effectiveHTTPSPort(right)
}

func effectiveHTTPSPort(target *url.URL) string {
	if target == nil || target.Port() == "" {
		return "443"
	}
	return target.Port()
}
