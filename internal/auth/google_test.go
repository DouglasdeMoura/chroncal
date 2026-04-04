package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func TestExchangeGoogleCode_UsesPKCEWithoutClientSecret(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := string(body)
			if strings.Contains(payload, "client_secret=") {
				t.Fatalf("token exchange payload should not include client_secret: %s", payload)
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

	result, err := exchangeGoogleCode(context.Background(), "client-id", "code-123", "http://127.0.0.1/callback", "verifier-123")
	if err != nil {
		t.Fatalf("exchangeGoogleCode: %v", err)
	}
	if result.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", result.AccessToken)
	}
}

func TestRefreshGoogleToken_DoesNotSendClientSecret(t *testing.T) {
	prevClient := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			payload := string(body)
			if strings.Contains(payload, "client_secret=") {
				t.Fatalf("refresh payload should not include client_secret: %s", payload)
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

	result, err := RefreshGoogleToken(context.Background(), "client-id", "refresh-token")
	if err != nil {
		t.Fatalf("RefreshGoogleToken: %v", err)
	}
	if result.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", result.AccessToken)
	}
}
