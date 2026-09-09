package caldav

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/retry"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestNewClientFromCredential_RefreshesExpiredOAuthToken(t *testing.T) {
	prevRefresh := refreshGoogleTokenFn
	refreshCalls := 0
	refreshGoogleTokenFn = func(ctx context.Context, clientID, clientSecret, refreshToken string) (*auth.GoogleOAuthResult, error) {
		refreshCalls++
		return &auth.GoogleOAuthResult{
			AccessToken:  "fresh-token",
			RefreshToken: refreshToken,
			Expiry:       time.Now().Add(time.Hour),
		}, nil
	}
	t.Cleanup(func() {
		refreshGoogleTokenFn = prevRefresh
	})

	prevDefaultClient := defaultHTTPClient
	var tokens []string
	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			tokens = append(tokens, r.Header.Get("Authorization"))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}),
	}
	t.Cleanup(func() {
		defaultHTTPClient = prevDefaultClient
	})

	var persisted auth.Credential
	client, err := NewClientFromCredential("https://example.com", auth.Credential{
		AccountID:     7,
		AccessToken:   "stale-token",
		RefreshToken:  "refresh-token",
		TokenExpiry:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		OAuthClientID: "client-id",
	}, func(updated auth.Credential) error {
		persisted = updated
		return nil
	})
	if err != nil {
		t.Fatalf("NewClientFromCredential: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	if resp, err := client.httpClient.Do(req); err != nil {
		t.Fatalf("Do first request: %v", err)
	} else {
		resp.Body.Close()
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext second: %v", err)
	}
	if resp, err := client.httpClient.Do(req); err != nil {
		t.Fatalf("Do second request: %v", err)
	} else {
		resp.Body.Close()
	}

	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if len(tokens) != 2 {
		t.Fatalf("saw %d requests, want 2", len(tokens))
	}
	if tokens[0] != "Bearer fresh-token" || tokens[1] != "Bearer fresh-token" {
		t.Fatalf("Authorization headers = %#v, want fresh token on both requests", tokens)
	}
	if persisted.AccessToken != "fresh-token" {
		t.Fatalf("persisted access token = %q, want fresh-token", persisted.AccessToken)
	}
	if persisted.TokenExpiry == "" {
		t.Fatal("persisted token expiry should be updated")
	}
}

func TestOAuth2HTTPClient_FailsFastOnNonTransientRefreshError(t *testing.T) {
	prevRefresh := refreshGoogleTokenFn
	refreshGoogleTokenFn = func(ctx context.Context, clientID, clientSecret, refreshToken string) (*auth.GoogleOAuthResult, error) {
		return nil, errors.New("token refresh failed (400): invalid_grant")
	}
	t.Cleanup(func() { refreshGoogleTokenFn = prevRefresh })

	prevDefaultClient := defaultHTTPClient
	transportCalled := false
	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			transportCalled = true
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}),
	}
	t.Cleanup(func() { defaultHTTPClient = prevDefaultClient })

	client, err := NewClientFromCredential("https://example.com", auth.Credential{
		AccountID:     1,
		AccessToken:   "stale-token",
		RefreshToken:  "revoked-refresh",
		TokenExpiry:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		OAuthClientID: "client-id",
	}, nil)
	if err != nil {
		t.Fatalf("NewClientFromCredential: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.httpClient.Do(req)
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected error from non-transient refresh failure, got nil")
	}
	if transportCalled {
		t.Fatal("transport should not have been called after non-transient refresh failure")
	}
}

func TestOAuth2HTTPClient_ProceedsWithStaleTokenOnTransientRefreshError(t *testing.T) {
	prevRefresh := refreshGoogleTokenFn
	refreshGoogleTokenFn = func(ctx context.Context, clientID, clientSecret, refreshToken string) (*auth.GoogleOAuthResult, error) {
		// Match the production contract: google.go returns a typed status
		// so IsTransient classifies without scraping the message.
		return nil, retry.NewHTTPError(503, errors.New("token refresh failed (503): Service Unavailable"))
	}
	t.Cleanup(func() { refreshGoogleTokenFn = prevRefresh })

	prevDefaultClient := defaultHTTPClient
	var gotAuth string
	defaultHTTPClient = &http.Client{
		Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			gotAuth = r.Header.Get("Authorization")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(http.NoBody),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}),
	}
	t.Cleanup(func() { defaultHTTPClient = prevDefaultClient })

	client, err := NewClientFromCredential("https://example.com", auth.Credential{
		AccountID:     2,
		AccessToken:   "stale-token",
		RefreshToken:  "refresh-token",
		TokenExpiry:   time.Now().Add(-time.Hour).Format(time.RFC3339),
		OAuthClientID: "client-id",
	}, nil)
	if err != nil {
		t.Fatalf("NewClientFromCredential: %v", err)
	}

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := client.httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v (should proceed with stale token on transient refresh failure)", err)
	}
	resp.Body.Close()
	if gotAuth != "Bearer stale-token" {
		t.Fatalf("Authorization = %q, want Bearer stale-token", gotAuth)
	}
}

func TestNewClientFromCredential_UsesBoundedHTTPClient(t *testing.T) {
	client, err := NewClientFromCredential("https://example.com", auth.Credential{
		AccessToken:   "token",
		OAuthClientID: "client-id",
	}, nil)
	if err != nil {
		t.Fatalf("NewClientFromCredential: %v", err)
	}

	boundedClient, ok := client.httpClient.(boundedHTTPClient)
	if !ok {
		t.Fatalf("httpClient type = %T, want boundedHTTPClient", client.httpClient)
	}
	oauthClient, ok := boundedClient.inner.(*oauth2HTTPClient)
	if !ok {
		t.Fatalf("inner client type = %T, want *oauth2HTTPClient", boundedClient.inner)
	}
	if oauthClient.inner.Timeout != defaultHTTPTimeout {
		t.Fatalf("inner timeout = %s, want %s", oauthClient.inner.Timeout, defaultHTTPTimeout)
	}
}

// A basic-auth credential with a password command sends the first stdout
// line of the command as the password.
func TestHTTPClientFromCredential_RunsThePasswordCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test uses a POSIX shell")
	}
	httpClient, err := httpClientFromCredential(auth.Credential{
		Username:        "alice",
		PasswordCommand: `printf 'from-command\nlogin: alice\n'`,
	}, nil)
	if err != nil {
		t.Fatalf("httpClientFromCredential: %v", err)
	}

	var gotUser, gotPassword string
	var ok bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser, gotPassword, ok = r.BasicAuth()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext: %v", err)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	resp.Body.Close()

	if !ok {
		t.Fatal("the request carried no basic-auth header")
	}
	if gotUser != "alice" || gotPassword != "from-command" {
		t.Fatalf("basic auth = %q/%q, want alice/from-command", gotUser, gotPassword)
	}
}

func TestHTTPClientFromCredential_RejectsBothPasswordSources(t *testing.T) {
	_, err := httpClientFromCredential(auth.Credential{
		Username:        "alice",
		Password:        "secret123",
		PasswordCommand: "true",
	}, nil)
	if !errors.Is(err, auth.ErrPasswordSourceConflict) {
		t.Fatalf("httpClientFromCredential error = %v, want ErrPasswordSourceConflict", err)
	}
}

// An access-token credential stays on the OAuth path. A stray password
// command must not divert it to basic auth.
func TestHTTPClientFromCredential_AccessTokenIgnoresThePasswordCommand(t *testing.T) {
	httpClient, err := httpClientFromCredential(auth.Credential{
		AccessToken:     "token",
		PasswordCommand: "exit 1",
	}, nil)
	if err != nil {
		t.Fatalf("httpClientFromCredential: %v", err)
	}
	if _, ok := httpClient.(*oauth2HTTPClient); !ok {
		t.Fatalf("client type = %T, want *oauth2HTTPClient", httpClient)
	}
}
