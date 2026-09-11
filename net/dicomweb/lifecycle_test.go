package dicomweb

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"testing"
	"time"
)

func TestServerShutdownTimeoutCleansActiveHandlers(t *testing.T) {
	backend := completeServerTestBackend()
	handlerStarted := make(chan struct{})
	handlerExited := make(chan struct{})
	backend.search = func(ctx context.Context, _ SearchRequest, _ func(Dataset) error) (SearchResult, error) {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerExited)
		return SearchResult{}, ctx.Err()
	}
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listener.Addr().String()+"/studies", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("Accept", "application/dicom+json")
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want context deadline", err)
	}
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		cancelRequest()
		<-handlerExited
		t.Fatal("timed-out Shutdown left the active handler running")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("timed-out Shutdown left the client request running")
	}
	select {
	case err := <-serveDone:
		if err != nil {
			t.Fatalf("Serve() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed-out Shutdown left Serve running")
	}
}

func TestServerServeErrorCleansActiveHandlers(t *testing.T) {
	backend := completeServerTestBackend()
	handlerStarted := make(chan struct{})
	handlerExited := make(chan struct{})
	backend.search = func(ctx context.Context, _ SearchRequest, _ func(Dataset) error) (SearchResult, error) {
		close(handlerStarted)
		<-ctx.Done()
		close(handlerExited)
		return SearchResult{}, ctx.Err()
	}
	server, err := NewServer(ServerOptions{Backend: backend, AllowUnauthenticated: true})
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	listener, err := new(net.ListenConfig).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	serveDone := make(chan error, 1)
	go func() { serveDone <- server.Serve(listener) }()

	requestCtx, cancelRequest := context.WithCancel(context.Background())
	defer cancelRequest()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listener.Addr().String()+"/studies", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext() error = %v", err)
	}
	request.Header.Set("Accept", "application/dicom+json")
	requestDone := make(chan error, 1)
	go func() {
		response, requestErr := http.DefaultClient.Do(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		requestDone <- requestErr
	}()
	select {
	case <-handlerStarted:
	case <-time.After(time.Second):
		t.Fatal("request handler did not start")
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("listener.Close() error = %v", err)
	}
	select {
	case err := <-serveDone:
		if err == nil {
			t.Fatal("Serve() error = nil after external listener failure")
		}
	case <-time.After(time.Second):
		t.Fatal("external listener failure left Serve running")
	}
	select {
	case <-handlerExited:
	case <-time.After(time.Second):
		cancelRequest()
		<-handlerExited
		t.Fatal("Serve error left the active handler running")
	}
	select {
	case <-requestDone:
	case <-time.After(time.Second):
		t.Fatal("Serve error left the client request running")
	}
}
