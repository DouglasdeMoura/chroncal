package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// stubBrowser replaces openBrowserFn for the test's duration so no real
// browser process is spawned. Returns a pointer to the captured URL.
func stubBrowser(t *testing.T, openErr error) *string {
	t.Helper()
	var captured string
	prev := openBrowserFn
	openBrowserFn = func(_ context.Context, u string) error {
		captured = u
		return openErr
	}
	t.Cleanup(func() { openBrowserFn = prev })
	return &captured
}

// stubTokenExchange swaps googleHTTPClient for a transport that returns a
// canned token response.
func stubTokenExchange(t *testing.T) {
	t.Helper()
	prev := googleHTTPClient
	googleHTTPClient = &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"access_token":"tok","refresh_token":"ref","expires_in":3600}`)),
				Header:     make(http.Header),
			}, nil
		}),
	}
	t.Cleanup(func() { googleHTTPClient = prev })
}

// TestFlowBanner pins the CLI wrapper's printed output byte-for-byte. The
// TUI depends on GoogleOAuthFlow being the only printing variant; the CLI
// depends on these exact strings not drifting.
func TestFlowBanner(t *testing.T) {
	if got, want := flowBanner("https://x", true), "Browser opened for Google authorization. Waiting for redirect...\n"; got != want {
		t.Errorf("opened banner = %q, want %q", got, want)
	}
	if got, want := flowBanner("https://accounts.example/auth", false), "Open this URL in your browser to authorize:\n\n  https://accounts.example/auth\n\n"; got != want {
		t.Errorf("fallback banner = %q, want %q", got, want)
	}
}

func TestStartGoogleOAuthFlow_WaitExchangesCode(t *testing.T) {
	stubBrowser(t, errors.New("no browser"))
	stubTokenExchange(t)

	flow, err := StartGoogleOAuthFlow(context.Background(), "cid", "secret")
	if err != nil {
		t.Fatalf("StartGoogleOAuthFlow: %v", err)
	}
	defer flow.Close()

	if flow.BrowserOpened {
		t.Error("BrowserOpened = true with failing opener")
	}
	u, err := url.Parse(flow.AuthURL)
	if err != nil {
		t.Fatalf("AuthURL unparsable: %v", err)
	}
	state := u.Query().Get("state")
	if state == "" {
		t.Fatal("AuthURL missing state parameter")
	}
	redirect := u.Query().Get("redirect_uri")
	if !strings.HasPrefix(redirect, "http://127.0.0.1:") {
		t.Fatalf("redirect_uri = %q, want loopback", redirect)
	}

	// Simulate the browser redirect hitting the loopback handler.
	go func() {
		req, rerr := http.NewRequestWithContext(context.Background(), http.MethodGet,
			fmt.Sprintf("%s/?state=%s&code=authcode", redirect, state), nil)
		if rerr != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result, err := flow.Wait(ctx)
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if result.AccessToken != "tok" || result.RefreshToken != "ref" {
		t.Errorf("result = %+v, want tok/ref", result)
	}
}

func TestStartGoogleOAuthFlow_StateMismatchFailsWait(t *testing.T) {
	stubBrowser(t, errors.New("no browser"))

	flow, err := StartGoogleOAuthFlow(context.Background(), "cid", "secret")
	if err != nil {
		t.Fatalf("StartGoogleOAuthFlow: %v", err)
	}
	defer flow.Close()

	u, _ := url.Parse(flow.AuthURL)
	redirect := u.Query().Get("redirect_uri")
	go func() {
		req, rerr := http.NewRequestWithContext(context.Background(), http.MethodGet,
			redirect+"/?state=wrong&code=authcode", nil)
		if rerr != nil {
			return
		}
		resp, err := http.DefaultClient.Do(req)
		if err == nil {
			resp.Body.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := flow.Wait(ctx); err == nil || !strings.Contains(err.Error(), "state mismatch") {
		t.Fatalf("Wait err = %v, want state mismatch", err)
	}
}

func TestPendingOAuthFlow_CancelUnblocksWait(t *testing.T) {
	stubBrowser(t, errors.New("no browser"))

	flow, err := StartGoogleOAuthFlow(context.Background(), "cid", "secret")
	if err != nil {
		t.Fatalf("StartGoogleOAuthFlow: %v", err)
	}
	defer flow.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, werr := flow.Wait(ctx)
		done <- werr
	}()
	cancel()

	select {
	case werr := <-done:
		if !errors.Is(werr, context.Canceled) {
			t.Fatalf("Wait err = %v, want context.Canceled", werr)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Wait did not unblock on cancel")
	}
}

func TestPendingOAuthFlow_CloseIdempotent(t *testing.T) {
	stubBrowser(t, nil)

	flow, err := StartGoogleOAuthFlow(context.Background(), "cid", "secret")
	if err != nil {
		t.Fatalf("StartGoogleOAuthFlow: %v", err)
	}
	if !flow.BrowserOpened {
		t.Error("BrowserOpened = false with succeeding opener")
	}
	flow.Close()
	flow.Close() // must not panic

	// After Close, the listener is released: Wait should not hang forever
	// on a dead flow (the serve goroutine error lands in errCh).
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, err := flow.Wait(ctx); err == nil {
		t.Fatal("Wait on a closed flow should error")
	}
}
