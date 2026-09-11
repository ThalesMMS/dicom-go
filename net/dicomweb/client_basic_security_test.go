package dicomweb

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestClientBasicAuthRequiresVerifiedHTTPS(t *testing.T) {
	t.Run("trusted HTTPS sends credentials", func(t *testing.T) {
		var sawBasic atomic.Bool
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			sawBasic.Store(ok && user == "alice" && password == "secret")
			writeEmptyDICOMJSON(w)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, server.Client())
		if _, err := client.SearchStudies(context.Background(), nil); err != nil {
			t.Fatalf("SearchStudies() error = %v", err)
		}
		if !sawBasic.Load() {
			t.Fatal("trusted HTTPS request did not send Basic credentials")
		}
	})

	t.Run("HTTP including loopback fails before request", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			writeEmptyDICOMJSON(w)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, server.Client())
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("HTTP destination received %d request(s), want 0", got)
		}
	})

	t.Run("non-loopback HTTP fails before transport", func(t *testing.T) {
		var requests atomic.Int32
		httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			requests.Add(1)
			return nil, errors.New("transport must not be called")
		})}
		client := basicSecurityClient("http://dicom.example.test", httpClient)
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("HTTP transport received %d request(s), want 0", got)
		}
	})

	t.Run("InsecureSkipVerify fails before request", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			writeEmptyDICOMJSON(w)
		}))
		defer server.Close()

		transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec -- deliberately insecure test input
		defer transport.CloseIdleConnections()
		client := basicSecurityClient(server.URL, &http.Client{Transport: transport})
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("skip-verify destination received %d request(s), want 0", got)
		}
	})

	t.Run("custom RoundTripper fails closed", func(t *testing.T) {
		var requests atomic.Int32
		client := basicSecurityClient("https://pacs.example.test", &http.Client{
			Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests.Add(1)
				return nil, errors.New("transport must not be called")
			}),
		})
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("custom transport received %d request(s), want 0", got)
		}
	})

	t.Run("custom TLS dialer fails closed", func(t *testing.T) {
		var dials atomic.Int32
		transport := &http.Transport{
			DialTLS: func(string, string) (net.Conn, error) {
				dials.Add(1)
				return nil, errors.New("TLS dialer must not be called")
			},
		}
		client := basicSecurityClient("https://pacs.example.test", &http.Client{Transport: transport})
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := dials.Load(); got != 0 {
			t.Fatalf("custom TLS dialer ran %d time(s), want 0", got)
		}
	})

	t.Run("invalid certificate fails before destination handler", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			writeEmptyDICOMJSON(w)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, &http.Client{})
		if _, err := client.SearchStudies(context.Background(), nil); err == nil {
			t.Fatal("SearchStudies() error = nil, want certificate verification failure")
		}
		if got := requests.Load(); got != 0 {
			t.Fatalf("untrusted destination handler received %d request(s), want 0", got)
		}
	})
}

func TestClientBasicAuthRedirectPolicy(t *testing.T) {
	t.Run("same origin HTTPS preserves credentials without mutating client", func(t *testing.T) {
		var redirectedAuth atomic.Bool
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirected" {
				_, _, ok := r.BasicAuth()
				redirectedAuth.Store(ok)
				writeEmptyDICOMJSON(w)
				return
			}
			http.Redirect(w, r, "/redirected", http.StatusFound)
		}))
		defer server.Close()

		httpClient := server.Client()
		if httpClient.CheckRedirect != nil {
			t.Fatal("test client unexpectedly has a redirect callback")
		}
		client := basicSecurityClient(server.URL, httpClient)
		if _, err := client.SearchStudies(context.Background(), nil); err != nil {
			t.Fatalf("SearchStudies() error = %v", err)
		}
		if !redirectedAuth.Load() {
			t.Fatal("same-origin HTTPS redirect did not preserve Basic credentials")
		}
		if httpClient.CheckRedirect != nil {
			t.Fatal("shared HTTP client was mutated")
		}
	})

	t.Run("cross origin HTTPS is blocked before destination", func(t *testing.T) {
		var destinationRequests atomic.Int32
		destination := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			destinationRequests.Add(1)
			writeEmptyDICOMJSON(w)
		}))
		defer destination.Close()

		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL+"/redirected", http.StatusFound)
		}))
		defer origin.Close()

		httpClient := trustedTestClient(t, origin, destination)
		client := basicSecurityClient(origin.URL, httpClient)
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := destinationRequests.Load(); got != 0 {
			t.Fatalf("cross-origin destination received %d request(s), want 0", got)
		}
	})

	t.Run("HTTPS downgrade is blocked before destination", func(t *testing.T) {
		var destinationRequests atomic.Int32
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			destinationRequests.Add(1)
			writeEmptyDICOMJSON(w)
		}))
		defer destination.Close()

		origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL+"/redirected", http.StatusFound)
		}))
		defer origin.Close()

		client := basicSecurityClient(origin.URL, origin.Client())
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := destinationRequests.Load(); got != 0 {
			t.Fatalf("downgrade destination received %d request(s), want 0", got)
		}
	})

	t.Run("redirect URL userinfo is blocked before destination", func(t *testing.T) {
		const userinfoSecret = "redirect-secret"
		var destinationRequests atomic.Int32
		var server *httptest.Server
		server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/redirected" {
				destinationRequests.Add(1)
				writeEmptyDICOMJSON(w)
				return
			}
			destination, err := url.Parse(server.URL + "/redirected")
			if err != nil {
				t.Fatal(err)
			}
			destination.User = url.UserPassword("redirect-user", userinfoSecret)
			http.Redirect(w, r, destination.String(), http.StatusFound)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, server.Client())
		_, err := client.SearchStudies(context.Background(), nil)
		if !errors.Is(err, ErrInsecureBasicAuth) {
			t.Fatalf("SearchStudies() error = %v, want ErrInsecureBasicAuth", err)
		}
		if got := destinationRequests.Load(); got != 0 {
			t.Fatalf("userinfo destination received %d request(s), want 0", got)
		}
	})

	t.Run("redirect count is bounded", func(t *testing.T) {
		var requests atomic.Int32
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			current := 0
			_, _ = fmt.Sscanf(r.URL.Query().Get("n"), "%d", &current)
			next := url.Values{"n": {fmt.Sprint(current + 1)}}
			http.Redirect(w, r, "/again?"+next.Encode(), http.StatusFound)
		}))
		defer server.Close()

		client := basicSecurityClient(server.URL, server.Client())
		if _, err := client.SearchStudies(context.Background(), url.Values{"n": {"0"}}); err == nil {
			t.Fatal("SearchStudies() error = nil, want redirect limit failure")
		}
		if got := requests.Load(); got > 10 {
			t.Fatalf("server received %d requests, want at most 10", got)
		}
	})
}

func basicSecurityClient(baseURL string, httpClient *http.Client) Client {
	return Client{
		Endpoint: Endpoint{BaseURL: baseURL + "/dicom-web", QIDOPath: "qido"},
		Options: Options{
			HTTPClient:    httpClient,
			BasicUsername: "alice",
			BasicPassword: "secret",
		},
	}
}

func trustedTestClient(t *testing.T, servers ...*httptest.Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	for _, server := range servers {
		roots.AddCert(server.Certificate())
	}
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: roots}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport}
}

func writeEmptyDICOMJSON(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/dicom+json")
	_, _ = w.Write([]byte("[]"))
}
