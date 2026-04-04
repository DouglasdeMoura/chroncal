package caldav

import (
	"context"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestNewClientFromCredential_RefreshesExpiredOAuthToken(t *testing.T) {
	prevRefresh := refreshGoogleTokenFn
	refreshCalls := 0
	refreshGoogleTokenFn = func(ctx context.Context, clientID, refreshToken string) (*auth.GoogleOAuthResult, error) {
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
	if _, err := client.httpClient.Do(req); err != nil {
		t.Fatalf("Do first request: %v", err)
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.com/resource", nil)
	if err != nil {
		t.Fatalf("NewRequestWithContext second: %v", err)
	}
	if _, err := client.httpClient.Do(req); err != nil {
		t.Fatalf("Do second request: %v", err)
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

func TestNewClientFromCredential_UsesBoundedHTTPClient(t *testing.T) {
	client, err := NewClientFromCredential("https://example.com", auth.Credential{
		AccessToken:   "token",
		OAuthClientID: "client-id",
	}, nil)
	if err != nil {
		t.Fatalf("NewClientFromCredential: %v", err)
	}

	oauthClient, ok := client.httpClient.(*oauth2HTTPClient)
	if !ok {
		t.Fatalf("httpClient type = %T, want *oauth2HTTPClient", client.httpClient)
	}
	if oauthClient.inner.Timeout != defaultHTTPTimeout {
		t.Fatalf("inner timeout = %s, want %s", oauthClient.inner.Timeout, defaultHTTPTimeout)
	}
}
