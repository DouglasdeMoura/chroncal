package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestExchangeGoogleCode_SendsClientSecretWithPKCE(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := string(body)
			if !strings.Contains(payload, "client_secret=secret-xyz") {
				t.Fatalf("token exchange payload should include client_secret: %s", payload)
			}
			if !strings.Contains(payload, "code_verifier=verifier-123") {
				t.Fatalf("token exchange payload should include PKCE code_verifier, got %s", payload)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh","refresh_token":"refresh","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() {
		googleHTTPClient = prevClient
	})

	result, err := exchangeGoogleCode(context.Background(), "client-id", "secret-xyz", "code-123", "http://127.0.0.1/callback", "verifier-123")
	if err != nil {
		t.Fatalf("exchangeGoogleCode: %v", err)
	}
	if result.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", result.AccessToken)
	}
}

func TestExchangeGoogleCode_OmitsEmptyClientSecret(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := string(body)
			if strings.Contains(payload, "client_secret=") {
				t.Fatalf("empty client secret should not appear in payload: %s", payload)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh","refresh_token":"refresh","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prevClient })

	if _, err := exchangeGoogleCode(context.Background(), "client-id", "", "code", "http://localhost/cb", "verifier"); err != nil {
		t.Fatalf("exchangeGoogleCode: %v", err)
	}
}

func TestRefreshGoogleToken_SendsClientSecret(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := string(body)
			if !strings.Contains(payload, "client_secret=secret-xyz") {
				t.Fatalf("refresh payload should include client_secret: %s", payload)
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() {
		googleHTTPClient = prevClient
	})

	result, err := RefreshGoogleToken(context.Background(), "client-id", "secret-xyz", "refresh-token")
	if err != nil {
		t.Fatalf("RefreshGoogleToken: %v", err)
	}
	if result.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", result.AccessToken)
	}
}

func TestRefreshGoogleToken_PersistsRotatedRefreshToken(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh","expires_in":3600,"refresh_token":"rotated-token"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() {
		googleHTTPClient = prevClient
	})

	result, err := RefreshGoogleToken(context.Background(), "client-id", "secret-xyz", "old-token")
	if err != nil {
		t.Fatalf("RefreshGoogleToken: %v", err)
	}
	if result.RefreshToken != "rotated-token" {
		t.Fatalf("RefreshToken = %q, want rotated-token (server-rotated token discarded)", result.RefreshToken)
	}
}

func TestRefreshGoogleToken_KeepsOldRefreshTokenWhenOmitted(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"fresh","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() {
		googleHTTPClient = prevClient
	})

	result, err := RefreshGoogleToken(context.Background(), "client-id", "secret-xyz", "old-token")
	if err != nil {
		t.Fatalf("RefreshGoogleToken: %v", err)
	}
	if result.RefreshToken != "old-token" {
		t.Fatalf("RefreshToken = %q, want old-token (existing token should be retained)", result.RefreshToken)
	}
}

func TestExchangeGoogleCode_RetriesTransientError(t *testing.T) {
	var calls atomic.Int32
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n < 3 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("try again")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"ok","refresh_token":"r","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prevClient })

	result, err := exchangeGoogleCode(context.Background(), "cid", "secret", "code", "http://localhost/cb", "verifier")
	if err != nil {
		t.Fatalf("exchangeGoogleCode: %v", err)
	}
	if result.AccessToken != "ok" {
		t.Fatalf("AccessToken = %q, want ok", result.AccessToken)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("transport calls = %d, want 3", got)
	}
}

func TestExchangeGoogleCode_NoRetryOnNonTransient(t *testing.T) {
	var calls atomic.Int32
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader("bad")),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prevClient })

	_, err := exchangeGoogleCode(context.Background(), "cid", "secret", "code", "http://localhost/cb", "verifier")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}

func TestRefreshGoogleToken_RetriesTransientError(t *testing.T) {
	var calls atomic.Int32
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			n := calls.Add(1)
			if n < 2 {
				return &http.Response{
					StatusCode: http.StatusServiceUnavailable,
					Body:       io.NopCloser(strings.NewReader("try again")),
					Header:     make(http.Header),
				}, nil
			}
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"refreshed","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prevClient })

	result, err := RefreshGoogleToken(context.Background(), "cid", "secret", "rt")
	if err != nil {
		t.Fatalf("RefreshGoogleToken: %v", err)
	}
	if result.AccessToken != "refreshed" {
		t.Fatalf("AccessToken = %q, want refreshed", result.AccessToken)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("transport calls = %d, want 2", got)
	}
}

func TestRefreshGoogleToken_NoRetryOn400(t *testing.T) {
	var calls atomic.Int32
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusBadRequest,
				Body:       io.NopCloser(strings.NewReader(`{"error":"invalid_grant"}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prevClient })

	_, err := RefreshGoogleToken(context.Background(), "cid", "secret", "rt")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("transport calls = %d, want 1", got)
	}
}
