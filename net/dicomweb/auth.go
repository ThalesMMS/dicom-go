package dicomweb

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	defaultTokenRefreshSkew           = 30 * time.Second
	defaultTokenRefreshFailureBackoff = time.Second
)

var (
	// ErrBearerTokenUnavailable reports that a dynamic bearer source could not
	// provide a usable access token. Provider errors are deliberately redacted.
	ErrBearerTokenUnavailable = errors.New("DICOMweb bearer token unavailable")
)

// AccessToken keeps the bearer value private while exposing only non-secret
// lifecycle metadata.
type AccessToken struct {
	value  string
	Expiry time.Time `json:"expiry,omitempty"`
}

// NewAccessToken creates an in-memory bearer token. The token value is never
// serialized or included in String/GoString output.
func NewAccessToken(value string, expiry time.Time) (AccessToken, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return AccessToken{}, ErrBearerTokenUnavailable
	}
	if !expiry.IsZero() {
		expiry = expiry.UTC()
	}
	return AccessToken{value: value, Expiry: expiry}, nil
}

// Value returns the bearer value for an Authorization header.
func (t AccessToken) Value() string {
	return t.value
}

// String redacts the bearer value from ordinary formatting and logs.
func (t AccessToken) String() string {
	if t.value == "" {
		return "DICOMweb access token (empty)"
	}
	if t.Expiry.IsZero() {
		return "DICOMweb access token (redacted, no expiry)"
	}
	return "DICOMweb access token (redacted, expires " + t.Expiry.Format(time.RFC3339) + ")"
}

// GoString redacts the bearer value from %#v formatting and crash diagnostics.
func (t AccessToken) GoString() string {
	return t.String()
}

// MarshalJSON emits lifecycle metadata but never the bearer value.
func (t AccessToken) MarshalJSON() ([]byte, error) {
	if t.Expiry.IsZero() {
		return []byte("{}"), nil
	}
	return json.Marshal(struct {
		Expiry time.Time `json:"expiry,omitempty"`
	}{Expiry: t.Expiry})
}

func (t AccessToken) validAt(now time.Time, refreshSkew time.Duration) bool {
	if t.value == "" {
		return false
	}
	return t.Expiry.IsZero() || now.Add(refreshSkew).Before(t.Expiry)
}

// BearerTokenSource supplies an access token for one DICOMweb request.
type BearerTokenSource interface {
	Token(context.Context) (AccessToken, error)
}

type bearerTokenInvalidator interface {
	Invalidate(AccessToken)
}

// TokenRefreshFunc obtains a new access token. Refresh-token persistence and
// OIDC protocol exchange remain outside the manager so callers can use
// OS-protected credential storage.
type TokenRefreshFunc func(context.Context) (AccessToken, error)

// TokenStatus exposes non-secret lifecycle state for product UI.
type TokenStatus struct {
	Authenticated bool
	Expiry        time.Time
	Refreshing    bool
}

// TokenManager caches access tokens in memory and serializes refresh calls.
type TokenManager struct {
	mu                sync.Mutex
	token             AccessToken
	refresh           TokenRefreshFunc
	inFlight          *tokenRefreshCall
	refreshFailure    error
	refreshRetryAfter time.Time
	generation        uint64
}

// String reports non-secret lifecycle state for diagnostics.
func (m *TokenManager) String() string {
	status := m.Status()
	expiry := "none"
	if !status.Expiry.IsZero() {
		expiry = status.Expiry.Format(time.RFC3339)
	}
	return fmt.Sprintf(
		"DICOMweb token manager (authenticated=%t, refreshing=%t, expiry=%s)",
		status.Authenticated,
		status.Refreshing,
		expiry,
	)
}

// GoString redacts token state from %#v formatting and crash diagnostics.
func (m *TokenManager) GoString() string {
	return m.String()
}

type tokenRefreshCall struct {
	done  chan struct{}
	token AccessToken
	err   error
}

// NewTokenManager constructs a concurrency-safe, in-memory access-token
// manager. An empty initial token is allowed when refresh is configured.
func NewTokenManager(initial AccessToken, refresh TokenRefreshFunc) *TokenManager {
	return &TokenManager{token: initial, refresh: refresh}
}

// Token returns a cached token or performs one serialized refresh. Waiting
// callers may cancel independently.
func (m *TokenManager) Token(ctx context.Context) (AccessToken, error) {
	if m == nil {
		return AccessToken{}, ErrBearerTokenUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.mu.Lock()
	if m.token.validAt(time.Now(), defaultTokenRefreshSkew) {
		token := m.token
		m.mu.Unlock()
		return token, nil
	}
	if call := m.inFlight; call != nil {
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return AccessToken{}, ctx.Err()
		case <-call.done:
			return call.token, call.err
		}
	}
	if m.refresh == nil {
		m.mu.Unlock()
		return AccessToken{}, ErrBearerTokenUnavailable
	}
	if err := ctx.Err(); err != nil {
		m.mu.Unlock()
		return AccessToken{}, err
	}
	if m.refreshFailure != nil && time.Now().Before(m.refreshRetryAfter) {
		err := m.refreshFailure
		m.mu.Unlock()
		return AccessToken{}, err
	}
	m.refreshFailure = nil
	m.refreshRetryAfter = time.Time{}

	call := &tokenRefreshCall{done: make(chan struct{})}
	m.inFlight = call
	generation := m.generation
	m.mu.Unlock()

	go m.refreshToken(context.WithoutCancel(ctx), call, generation)
	select {
	case <-ctx.Done():
		return AccessToken{}, ctx.Err()
	case <-call.done:
		return call.token, call.err
	}
}

func (m *TokenManager) refreshToken(ctx context.Context, call *tokenRefreshCall, generation uint64) {
	ctx, cancel := context.WithTimeout(ctx, DefaultTimeout)
	defer cancel()

	token, err := m.refresh(ctx)
	err = sanitizedTokenError(err)
	if err == nil && !token.validAt(time.Now(), defaultTokenRefreshSkew) {
		err = ErrBearerTokenUnavailable
	}
	if err != nil {
		token = AccessToken{}
	}

	m.mu.Lock()
	if m.generation != generation {
		token = AccessToken{}
		err = ErrBearerTokenUnavailable
	} else if err == nil {
		m.token = token
		m.refreshFailure = nil
		m.refreshRetryAfter = time.Time{}
	} else {
		m.refreshFailure = err
		m.refreshRetryAfter = time.Now().Add(defaultTokenRefreshFailureBackoff)
	}
	call.token = token
	call.err = err
	m.inFlight = nil
	close(call.done)
	m.mu.Unlock()
}

// Invalidate drops the cached token only when it matches the challenged token.
func (m *TokenManager) Invalidate(challenged AccessToken) {
	if m == nil || challenged.value == "" {
		return
	}
	m.mu.Lock()
	if constantTimeTokenEqual(m.token.value, challenged.value) {
		m.token = AccessToken{}
		m.refreshFailure = nil
		m.refreshRetryAfter = time.Time{}
		m.generation++
	}
	m.mu.Unlock()
}

// Logout permanently disables the manager and removes its in-memory access
// token. Persistent refresh credentials are owned by the caller.
func (m *TokenManager) Logout() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.token = AccessToken{}
	m.refresh = nil
	m.refreshFailure = nil
	m.refreshRetryAfter = time.Time{}
	m.generation++
	m.mu.Unlock()
}

// Status returns non-secret token lifecycle information.
func (m *TokenManager) Status() TokenStatus {
	if m == nil {
		return TokenStatus{}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return TokenStatus{
		Authenticated: m.token.validAt(time.Now(), 0),
		Expiry:        m.token.Expiry,
		Refreshing:    m.inFlight != nil,
	}
}

func sanitizedTokenError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrBearerTokenUnavailable
	}
}

func constantTimeTokenEqual(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func bearerTokenError(rawURL string, err error) error {
	return &Error{
		Kind: ErrorKindAuthToken,
		URL:  rawURL,
		Err:  sanitizedTokenError(err),
	}
}

func accessTokenFromSource(
	ctx context.Context,
	rawURL string,
	source BearerTokenSource,
) (AccessToken, error) {
	token, err := source.Token(ctx)
	if err != nil {
		return AccessToken{}, bearerTokenError(rawURL, err)
	}
	if !token.validAt(time.Now(), 0) {
		return AccessToken{}, bearerTokenError(rawURL, ErrBearerTokenUnavailable)
	}
	return token, nil
}

var _ fmt.Stringer = AccessToken{}
var _ fmt.Stringer = (*TokenManager)(nil)
