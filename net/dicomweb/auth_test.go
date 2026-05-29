package dicomweb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func mustAccessToken(t testing.TB, value string, expiry time.Time) AccessToken {
	t.Helper()
	token, err := NewAccessToken(value, expiry)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

func TestAccessTokenRedactsFormattingAndJSON(t *testing.T) {
	const secret = "secret-bearer-token"
	token := mustAccessToken(t, secret, time.Now().Add(time.Hour))
	for _, rendered := range []string{
		token.String(),
		fmt.Sprintf("%v", token),
		fmt.Sprintf("%+v", token),
		fmt.Sprintf("%#v", token),
	} {
		if strings.Contains(rendered, secret) || !strings.Contains(strings.ToLower(rendered), "redact") {
			t.Errorf("formatted token was not redacted: %q", rendered)
		}
	}
	data, err := json.Marshal(token)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "expiry") {
		t.Fatalf("token JSON = %s", data)
	}
}

func TestTokenManagerSerializesConcurrentRefresh(t *testing.T) {
	var refreshes atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	fresh := mustAccessToken(t, "fresh-token", time.Now().Add(time.Hour))
	manager := NewTokenManager(AccessToken{}, func(ctx context.Context) (AccessToken, error) {
		if refreshes.Add(1) == 1 {
			close(entered)
		}
		select {
		case <-ctx.Done():
			return AccessToken{}, ctx.Err()
		case <-release:
			return fresh, nil
		}
	})

	const callers = 24
	results := make(chan AccessToken, callers)
	errorsSeen := make(chan error, callers)
	var start sync.WaitGroup
	start.Add(callers)
	for range callers {
		go func() {
			start.Done()
			start.Wait()
			token, err := manager.Token(context.Background())
			results <- token
			errorsSeen <- err
		}()
	}
	<-entered
	close(release)

	for range callers {
		if err := <-errorsSeen; err != nil {
			t.Fatal(err)
		}
		if got := (<-results).Value(); got != "fresh-token" {
			t.Errorf("token = %q, want fresh-token", got)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refresh count = %d, want 1", got)
	}
}

func TestTokenManagerWaiterCancellationPropagates(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fresh := mustAccessToken(t, "fresh-token", time.Now().Add(time.Hour))
	manager := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		close(entered)
		<-release
		return fresh, nil
	})
	ownerDone := make(chan error, 1)
	go func() {
		_, err := manager.Token(context.Background())
		ownerDone <- err
	}()
	<-entered

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("waiter error = %v, want context.Canceled", err)
	}
	close(release)
	if err := <-ownerDone; err != nil {
		t.Fatal(err)
	}
}

func TestTokenManagerOwnerCancellationDoesNotAbortSharedRefresh(t *testing.T) {
	type contextKey struct{}
	const contextValue = "refresh-value"

	entered := make(chan struct{})
	release := make(chan struct{})
	fresh := mustAccessToken(t, "fresh-token", time.Now().Add(time.Hour))
	manager := NewTokenManager(AccessToken{}, func(ctx context.Context) (AccessToken, error) {
		if got := ctx.Value(contextKey{}); got != contextValue {
			t.Errorf("refresh context value = %v, want %q", got, contextValue)
		}
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > DefaultTimeout {
			t.Errorf("refresh deadline = %v, ok=%t, want within %s", deadline, ok, DefaultTimeout)
		}
		close(entered)
		select {
		case <-ctx.Done():
			return AccessToken{}, ctx.Err()
		case <-release:
			return fresh, nil
		}
	})

	ownerCtx, cancelOwner := context.WithCancel(
		context.WithValue(context.Background(), contextKey{}, contextValue),
	)
	ownerDone := make(chan error, 1)
	go func() {
		_, err := manager.Token(ownerCtx)
		ownerDone <- err
	}()
	<-entered
	cancelOwner()
	if err := <-ownerDone; !errors.Is(err, context.Canceled) {
		t.Fatalf("owner error = %v, want context.Canceled", err)
	}

	waiterDone := make(chan error, 1)
	go func() {
		token, err := manager.Token(context.Background())
		if err == nil && token.Value() != "fresh-token" {
			err = fmt.Errorf("waiter token = %q, want fresh-token", token.Value())
		}
		waiterDone <- err
	}()
	close(release)
	if err := <-waiterDone; err != nil {
		t.Fatal(err)
	}
}

func TestTokenManagerRefreshErrorsAreRedacted(t *testing.T) {
	const secret = "refresh-secret"
	manager := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		return AccessToken{}, errors.New("provider rejected " + secret)
	})
	_, err := manager.Token(context.Background())
	if !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("Token() error = %v, want ErrBearerTokenUnavailable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("refresh error leaked secret: %v", err)
	}
}

func TestTokenManagerThrottlesRepeatedRefreshFailures(t *testing.T) {
	var refreshes atomic.Int32
	manager := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return AccessToken{}, errors.New("identity provider unavailable")
	})

	for range 2 {
		if _, err := manager.Token(context.Background()); !errors.Is(err, ErrBearerTokenUnavailable) {
			t.Fatalf("Token() error = %v, want ErrBearerTokenUnavailable", err)
		}
	}
	if got := refreshes.Load(); got != 1 {
		t.Fatalf("refresh count during backoff = %d, want 1", got)
	}

	manager.mu.Lock()
	manager.refreshRetryAfter = time.Now().Add(-time.Second)
	manager.mu.Unlock()
	if _, err := manager.Token(context.Background()); !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("Token() error after backoff = %v, want ErrBearerTokenUnavailable", err)
	}
	if got := refreshes.Load(); got != 2 {
		t.Fatalf("refresh count after backoff = %d, want 2", got)
	}
}

func TestTokenManagerRedactsFormatting(t *testing.T) {
	const secret = "manager-secret"
	manager := NewTokenManager(mustAccessToken(t, secret, time.Now().Add(time.Hour)), nil)
	for _, rendered := range []string{
		manager.String(),
		fmt.Sprintf("%v", manager),
		fmt.Sprintf("%+v", manager),
		fmt.Sprintf("%#v", manager),
	} {
		if strings.Contains(rendered, secret) {
			t.Fatalf("formatted manager leaked token: %q", rendered)
		}
	}
}

func TestTokenManagerRejectsExpiredRefreshResult(t *testing.T) {
	expired := mustAccessToken(t, "already-expired", time.Now().Add(-time.Minute))
	manager := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		return expired, nil
	})
	token, err := manager.Token(context.Background())
	if token.Value() != "" {
		t.Fatalf("Token() value = %q, want empty failed result", token.Value())
	}
	if !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("Token() error = %v, want ErrBearerTokenUnavailable", err)
	}
	if status := manager.Status(); status.Authenticated {
		t.Fatalf("expired refresh was cached: %#v", status)
	}
}

func TestTokenManagerInvalidateAndLogout(t *testing.T) {
	initial := mustAccessToken(t, "old-token", time.Now().Add(time.Hour))
	fresh := mustAccessToken(t, "new-token", time.Now().Add(time.Hour))
	var refreshes atomic.Int32
	manager := NewTokenManager(initial, func(context.Context) (AccessToken, error) {
		refreshes.Add(1)
		return fresh, nil
	})

	manager.Invalidate(mustAccessToken(t, "different-token", time.Time{}))
	token, err := manager.Token(context.Background())
	if err != nil || token.Value() != "old-token" || refreshes.Load() != 0 {
		t.Fatalf("non-matching invalidate token=%q refreshes=%d err=%v", token.Value(), refreshes.Load(), err)
	}

	manager.Invalidate(initial)
	token, err = manager.Token(context.Background())
	if err != nil || token.Value() != "new-token" || refreshes.Load() != 1 {
		t.Fatalf("matching invalidate token=%q refreshes=%d err=%v", token.Value(), refreshes.Load(), err)
	}
	manager.Logout()
	if status := manager.Status(); status.Authenticated {
		t.Fatalf("status after logout = %#v", status)
	}
	if _, err := manager.Token(context.Background()); !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("Token() after logout error = %v, want permanently disabled manager", err)
	}
	if refreshes.Load() != 1 {
		t.Fatalf("refreshes after logout = %d, want no new refresh", refreshes.Load())
	}
}

func TestTokenManagerLogoutDiscardsInFlightRefresh(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	fresh := mustAccessToken(t, "must-not-survive-logout", time.Now().Add(time.Hour))
	manager := NewTokenManager(AccessToken{}, func(context.Context) (AccessToken, error) {
		close(entered)
		<-release
		return fresh, nil
	})

	result := make(chan AccessToken, 1)
	errorsSeen := make(chan error, 1)
	go func() {
		token, err := manager.Token(context.Background())
		result <- token
		errorsSeen <- err
	}()
	<-entered
	manager.Logout()
	close(release)

	if token := <-result; token.Value() != "" {
		t.Fatalf("Token() value = %q after logout, want empty", token.Value())
	}
	if err := <-errorsSeen; !errors.Is(err, ErrBearerTokenUnavailable) {
		t.Fatalf("Token() error = %v, want ErrBearerTokenUnavailable", err)
	}
	if status := manager.Status(); status.Authenticated {
		t.Fatalf("refresh repopulated token after logout: %#v", status)
	}
}
