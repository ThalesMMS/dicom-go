package dicomweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestClientBearerSourceRetriesOneUnauthorizedGET(t *testing.T) {
	initial := mustAccessToken(t, "old-token", time.Now().Add(time.Hour))
	var refreshes atomic.Int32
	manager := NewTokenManager(initial, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return mustAccessToken(t, "new-token", time.Now().Add(time.Hour)), nil
	})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.Header.Get("Authorization") {
		case "Bearer old-token":
			http.Error(w, "expired", http.StatusUnauthorized)
		case "Bearer new-token":
			w.Header().Set("Content-Type", "application/dicom+json")
			_, _ = w.Write([]byte("[]"))
		default:
			http.Error(w, "missing bearer", http.StatusForbidden)
		}
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options:  Options{HTTPClient: server.Client(), BearerTokenSource: manager},
	}
	if _, err := client.SearchStudies(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if requests.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("requests=%d refreshes=%d, want 2/1", requests.Load(), refreshes.Load())
	}
}

func TestClientBearerSourceDoesNotLoopOnRepeatedUnauthorized(t *testing.T) {
	initial := mustAccessToken(t, "old-token", time.Now().Add(time.Hour))
	fresh := mustAccessToken(t, "new-token", time.Now().Add(time.Hour))
	var refreshes atomic.Int32
	manager := NewTokenManager(initial, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return fresh, nil
	})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		http.Error(w, "still unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options:  Options{HTTPClient: server.Client(), BearerTokenSource: manager},
	}
	_, err := client.SearchStudies(context.Background(), nil)
	var webErr *Error
	if !errors.As(err, &webErr) || webErr.Kind != ErrorKindAuthStatus {
		t.Fatalf("SearchStudies() error = %#v, want auth-status Error", err)
	}
	if requests.Load() != 2 || refreshes.Load() != 1 {
		t.Fatalf("requests=%d refreshes=%d, want 2/1", requests.Load(), refreshes.Load())
	}
}

func TestClientBearerSourceRetryPreservesReplayMetadata(t *testing.T) {
	initial := mustAccessToken(t, "old-token", time.Now().Add(time.Hour))
	var refreshes atomic.Int32
	manager := NewTokenManager(initial, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return mustAccessToken(t, "new-token", time.Now().Add(time.Hour)), nil
	})
	const payload = "synthetic-replayable-dicom"
	contentLengths := make(chan int64, 3)
	bodies := make(chan string, 3)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		contentLengths <- r.ContentLength
		body, _ := io.ReadAll(r.Body)
		bodies <- string(body)
		switch requests.Add(1) {
		case 1:
			http.Error(w, "expired", http.StatusUnauthorized)
		case 2:
			w.Header().Set("Location", "/final")
			w.WriteHeader(http.StatusTemporaryRedirect)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	endpoint, err := url.Parse(server.URL + "/stow")
	if err != nil {
		t.Fatal(err)
	}
	client := Client{
		Options: Options{HTTPClient: server.Client(), BearerTokenSource: manager},
	}
	resp, cancel, err := client.doHTTP(
		context.Background(),
		http.MethodPost,
		endpoint,
		strings.NewReader(payload),
		"application/dicom",
		acceptDICOMJSON,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancel != nil {
		defer cancel()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNoContent)
	}
	if requests.Load() != 3 || refreshes.Load() != 1 {
		t.Fatalf("requests=%d refreshes=%d, want 3/1", requests.Load(), refreshes.Load())
	}
	for range 3 {
		if got := <-contentLengths; got != int64(len(payload)) {
			t.Fatalf("Content-Length = %d, want %d", got, len(payload))
		}
		if got := <-bodies; got != payload {
			t.Fatalf("body = %q, want %q", got, payload)
		}
	}
}

func TestClientBearerSourceDoesNotReplayStreamingBody(t *testing.T) {
	initial := mustAccessToken(t, "stow-token", time.Now().Add(time.Hour))
	var refreshes atomic.Int32
	manager := NewTokenManager(initial, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return mustAccessToken(t, "unexpected-refresh", time.Now().Add(time.Hour)), nil
	})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer server.Close()

	endpoint, err := url.Parse(server.URL + "/stow")
	if err != nil {
		t.Fatal(err)
	}
	body := &oneShotReader{Reader: strings.NewReader("synthetic-non-phi-dicom")}
	client := Client{
		Options: Options{HTTPClient: server.Client(), BearerTokenSource: manager},
	}
	resp, cancel, err := client.doHTTP(
		context.Background(),
		http.MethodPost,
		endpoint,
		body,
		"application/dicom",
		acceptDICOMJSON,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cancel != nil {
		defer cancel()
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
	if requests.Load() != 1 || refreshes.Load() != 0 {
		t.Fatalf("requests=%d refreshes=%d, want 1/0", requests.Load(), refreshes.Load())
	}
	if status := manager.Status(); status.Authenticated {
		t.Fatalf("rejected streaming token remains cached: %#v", status)
	}
}

func TestClientBearerSourceFailureIsRedactedAndDoesNotDowngrade(t *testing.T) {
	const secret = "provider-secret"
	source := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		return AccessToken{}, errors.New("refresh failed with " + secret)
	})
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL},
		Options: Options{
			HTTPClient:        server.Client(),
			BasicUsername:     "fallback",
			BasicPassword:     "must-not-be-used",
			BearerToken:       "static-must-not-be-used",
			BearerTokenSource: source,
		},
	}
	_, err := client.SearchStudies(context.Background(), nil)
	var webErr *Error
	if !errors.As(err, &webErr) || webErr.Kind != ErrorKindAuthToken {
		t.Fatalf("SearchStudies() error = %#v, want auth-token Error", err)
	}
	if !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("SearchStudies() error = %v, want ErrBearerTokenUnavailable", err)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "must-not-be-used") {
		t.Fatalf("auth error leaked a secret: %v", err)
	}
	if requests.Load() != 0 {
		t.Fatalf("HTTP requests = %d, want 0", requests.Load())
	}
}

// oneShotReader intentionally hides the concrete reader type from
// http.NewRequest so GetBody remains nil and the request is not replayable.
type oneShotReader struct {
	io.Reader
}

func TestClientBearerSourceAppliesToStreamingSTOW(t *testing.T) {
	manager := NewTokenManager(mustAccessToken(t, "stow-token", time.Time{}), nil)
	var sawBearer atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawBearer.Store(r.Header.Get("Authorization") == "Bearer stow-token")
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "application/dicom+json")
		_, _ = w.Write([]byte(`{
			"00081199": {"vr": "SQ", "Value": [
				{"00081155": {"vr": "UI", "Value": ["1.2.3"]}}
			]}
		}`))
	}))
	defer server.Close()

	client := Client{
		Endpoint: Endpoint{BaseURL: server.URL, STOWPath: "stow"},
		Options:  Options{HTTPClient: server.Client(), BearerTokenSource: manager},
	}
	if _, err := client.StoreInstances(context.Background(), []StoreInstance{{
		SOPInstanceUID: "1.2.3",
		Data:           []byte("synthetic-non-phi-dicom"),
	}}); err != nil {
		t.Fatal(err)
	}
	if !sawBearer.Load() {
		t.Fatal("STOW-RS request did not use the dynamic bearer source")
	}
}

func TestOptionsJSONRedactsCredentialsAndTokenSource(t *testing.T) {
	manager := NewTokenManager(mustAccessToken(t, "dynamic-secret", time.Time{}), nil)
	options := Options{
		HTTPClient:        &http.Client{},
		BasicUsername:     "basic-user@example.test",
		BasicPassword:     "basic-secret",
		BearerToken:       "static-secret",
		BearerTokenSource: manager,
	}
	data, err := json.Marshal(options)
	if err != nil {
		t.Fatal(err)
	}
	secrets := []string{"basic-user@example.test", "basic-secret", "static-secret", "dynamic-secret"}
	for _, secret := range secrets {
		if strings.Contains(string(data), secret) {
			t.Fatalf("Options JSON leaked %q: %s", secret, data)
		}
	}
	for _, rendered := range []string{
		options.String(),
		fmt.Sprintf("%v", options),
		fmt.Sprintf("%+v", options),
		fmt.Sprintf("%#v", options),
		fmt.Sprintf("%v", Client{Options: options}),
		fmt.Sprintf("%+v", Client{Options: options}),
		fmt.Sprintf("%#v", Client{Options: options}),
	} {
		for _, secret := range secrets {
			if strings.Contains(rendered, secret) {
				t.Fatalf("Options formatting leaked %q: %s", secret, rendered)
			}
		}
	}
}
