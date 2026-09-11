package dicomweb

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Server is an embeddable http.Handler and an optional owner of an http.Server.
type Server struct {
	options ServerOptions
	limits  ServerLimits
	handler http.Handler
	sem     chan struct{}

	mu         sync.Mutex
	httpServer *http.Server
}

// NewServer constructs a bounded, deny-by-default DICOMweb handler.
func NewServer(options ServerOptions) (*Server, error) {
	if options.Backend == nil {
		return nil, fmt.Errorf("%w: backend is required", ErrInvalidServerOptions)
	}
	limits, err := normalizeServerLimits(options.Limits)
	if err != nil {
		return nil, err
	}
	policy, err := normalizeStorePolicy(options.StorePolicy)
	if err != nil {
		return nil, err
	}
	options.StorePolicy = policy
	if options.SpoolRetentionAge < 0 || options.SpoolAggregateQuotaBytes < 0 {
		return nil, fmt.Errorf("%w: invalid spool retention policy", ErrInvalidServerOptions)
	}
	if options.SpoolRetentionAge == 0 {
		options.SpoolRetentionAge = defaultSpoolRetentionAge
	}
	if options.SpoolAggregateQuotaBytes == 0 {
		options.SpoolAggregateQuotaBytes = defaultSpoolAggregateQuotaBytes
	}
	if err := validateServiceRoot(options.ServiceRoot); err != nil {
		return nil, err
	}
	if err := scavengeDICOMwebSpool(options, limits, time.Now()); err != nil {
		return nil, err
	}
	server := &Server{
		options: options,
		limits:  limits,
		sem:     make(chan struct{}, limits.MaxConcurrentRequests),
	}
	var handler http.Handler = http.HandlerFunc(server.serveCore)
	if options.Middleware != nil {
		handler = options.Middleware(handler)
		if handler == nil {
			return nil, fmt.Errorf("%w: middleware returned nil handler", ErrInvalidServerOptions)
		}
	}
	server.handler = handler
	return server, nil
}

func validateServiceRoot(root string) error {
	if root == "" {
		return nil
	}
	if !strings.HasPrefix(root, "/") || root == "/" || strings.HasSuffix(root, "/") || strings.Contains(root, "//") || strings.ContainsAny(root, "?#%") {
		return fmt.Errorf("%w: invalid service root", ErrInvalidServerOptions)
	}
	for _, segment := range strings.Split(strings.TrimPrefix(root, "/"), "/") {
		if segment == "" || segment == "." || segment == ".." || url.PathEscape(segment) != segment {
			return fmt.Errorf("%w: invalid service root", ErrInvalidServerOptions)
		}
	}
	return nil
}

func (s *Server) servicePath(resource string) string {
	return s.options.ServiceRoot + resource
}

func (s *Server) serviceURI(r *http.Request) string {
	scheme := "http"
	if r != nil && r.TLS != nil {
		scheme = "https"
	}
	host := "dicomweb"
	if r != nil && r.Host != "" {
		host = r.Host
	}
	return scheme + "://" + host + s.options.ServiceRoot
}

// ServeHTTP implements http.Handler.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.handler == nil {
		http.Error(w, "DICOMweb service unavailable", http.StatusServiceUnavailable)
		return
	}
	s.handler.ServeHTTP(w, r)
}

// Serve serves the handler on listener. Only one owned server may run at once.
func (s *Server) Serve(listener net.Listener) error {
	if s == nil || listener == nil {
		return fmt.Errorf("%w: listener is required", ErrInvalidServerOptions)
	}
	httpOptions := s.options.HTTP
	if httpOptions.ReadHeaderTimeout <= 0 {
		httpOptions.ReadHeaderTimeout = 10 * time.Second
	}
	if httpOptions.ReadTimeout <= 0 {
		httpOptions.ReadTimeout = s.limits.MaxDuration
	}
	if httpOptions.WriteTimeout <= 0 {
		httpOptions.WriteTimeout = s.limits.MaxDuration
	}
	if httpOptions.IdleTimeout <= 0 {
		httpOptions.IdleTimeout = 60 * time.Second
	}
	if httpOptions.MaxHeaderBytes <= 0 {
		httpOptions.MaxHeaderBytes = s.limits.MaxHeaderBytes
	}
	owned := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: httpOptions.ReadHeaderTimeout,
		ReadTimeout:       httpOptions.ReadTimeout,
		WriteTimeout:      httpOptions.WriteTimeout,
		IdleTimeout:       httpOptions.IdleTimeout,
		MaxHeaderBytes:    httpOptions.MaxHeaderBytes,
	}
	s.mu.Lock()
	if s.httpServer != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: server is already running", ErrInvalidServerOptions)
	}
	s.httpServer = owned
	s.mu.Unlock()
	err := owned.Serve(listener)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		// An external listener failure bypasses http.Server.Shutdown. Close the
		// remaining connections before dropping the owned server reference so
		// active request contexts and backend handlers cannot outlive Serve.
		if closeErr := owned.Close(); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	s.mu.Lock()
	if s.httpServer == owned {
		s.httpServer = nil
	}
	s.mu.Unlock()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// Shutdown gracefully stops an owned server. It is idempotent when Serve has
// not been called or has already returned.
func (s *Server) Shutdown(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	owned := s.httpServer
	s.mu.Unlock()
	if owned == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := owned.Shutdown(ctx); err != nil {
		// Shutdown stops accepting new connections before waiting for active
		// handlers. If its context expires, force-close the owned server while it
		// is still reachable so request contexts and their backend work cannot be
		// left running after Serve returns.
		return errors.Join(err, owned.Close())
	}
	return nil
}
