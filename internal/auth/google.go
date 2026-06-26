package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/douglasdemoura/chroncal/internal/retry"
)

const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/v2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
	googleScope    = "https://www.googleapis.com/auth/calendar"
)

var googleHTTPClient = &http.Client{Timeout: 30 * time.Second}

var tokenRetryOptions = retry.RetryOptions{
	MaxAttempts: 4,
	BaseDelay:   500 * time.Millisecond,
	MaxDelay:    8 * time.Second,
}

// GoogleOAuthResult holds tokens from a successful OAuth flow.
type GoogleOAuthResult struct {
	AccessToken  string
	RefreshToken string
	Expiry       time.Time
}

// PendingOAuthFlow is a started, not-yet-authorized Google OAuth flow: the
// loopback listener is bound, the redirect handler is serving, and the
// browser may already be open. Call Wait to block for the redirect and
// exchange the code, or Close to abandon the flow and release the listener.
//
// The split exists so UIs (the TUI's pending modal) can render AuthURL and a
// waiting state between the two phases without the flow printing anything;
// GoogleOAuthFlow composes Start+Wait and keeps the CLI's printed output.
type PendingOAuthFlow struct {
	// AuthURL is the Google consent URL. UIs show it when the browser
	// could not be opened so the user can open it manually.
	AuthURL string
	// BrowserOpened reports whether the local browser launch succeeded.
	BrowserOpened bool

	clientID     string
	clientSecret string
	redirectURI  string
	state        string
	codeVerifier string

	server   *http.Server
	listener net.Listener
	codeCh   chan string
	errCh    chan error

	closeOnce sync.Once
}

// openBrowserFn is swappable in tests so StartGoogleOAuthFlow doesn't spawn
// a real browser process.
var openBrowserFn = openBrowser

// StartGoogleOAuthFlow binds the loopback listener, builds the consent URL,
// starts the redirect handler, and tries to open the user's browser. It
// never prints; callers own all user-facing output.
//
// Pass an empty clientSecret to omit it from the token request. Google's
// Desktop OAuth clients require a non-empty client secret even with PKCE;
// the caller is responsible for sourcing it and rejecting empty values when
// targeting Google.
func StartGoogleOAuthFlow(ctx context.Context, clientID, clientSecret string) (*PendingOAuthFlow, error) {
	// Start a temporary listener on a random port
	var lc net.ListenConfig
	listener, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("start listener: %w", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	redirectURI := fmt.Sprintf("http://127.0.0.1:%d", port)

	// Generate CSRF state token
	stateBytes := make([]byte, 16)
	if _, err := rand.Read(stateBytes); err != nil {
		listener.Close()
		return nil, fmt.Errorf("generate state token: %w", err)
	}
	state := hex.EncodeToString(stateBytes)
	codeVerifier, err := generateCodeVerifier()
	if err != nil {
		listener.Close()
		return nil, fmt.Errorf("generate code verifier: %w", err)
	}
	codeChallenge := codeChallengeS256(codeVerifier)

	// Build authorization URL
	authURL := fmt.Sprintf("%s?client_id=%s&redirect_uri=%s&response_type=code&scope=%s&access_type=offline&prompt=consent&state=%s&code_challenge=%s&code_challenge_method=S256",
		googleAuthURL,
		url.QueryEscape(clientID),
		url.QueryEscape(redirectURI),
		url.QueryEscape(googleScope),
		url.QueryEscape(state),
		url.QueryEscape(codeChallenge),
	)

	p := &PendingOAuthFlow{
		AuthURL:      authURL,
		clientID:     clientID,
		clientSecret: clientSecret,
		redirectURI:  redirectURI,
		state:        state,
		codeVerifier: codeVerifier,
		listener:     listener,
		codeCh:       make(chan string, 1),
		errCh:        make(chan error, 1),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()

		// A bad or missing state does NOT abort the flow: any unrelated
		// localhost request (browser prefetch, favicon probe, a port
		// scanner) would otherwise kill an in-progress re-auth without
		// knowing the CSRF token. Reject just this request and keep
		// waiting for the legitimate redirect.
		if q.Get("state") != p.state {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<html><body><h1>Ignored</h1><p>Unexpected request. You can close this window.</p></body></html>")
			return
		}

		// Past the state check this is the real redirect. An explicit
		// error param (user denied consent) or a missing code is a genuine
		// flow failure and aborts.
		code := q.Get("code")
		if code == "" {
			errMsg := q.Get("error")
			if errMsg == "" {
				errMsg = "no authorization code received"
			}
			p.sendErr(fmt.Errorf("oauth error: %s", errMsg))
			fmt.Fprintf(w, "<html><body><h1>Authorization failed</h1><p>%s</p></body></html>", html.EscapeString(errMsg))
			return
		}
		select {
		case p.codeCh <- code:
		default:
		}
		fmt.Fprint(w, "<html><body><h1>Authorization successful</h1><p>You can close this window.</p></body></html>")
	})

	// ReadHeaderTimeout bounds a slow/stuck local client so it can't tie up
	// the handler goroutine; Close uses a bounded Shutdown so Esc/timeout
	// can't hang the TUI on such a client.
	p.server = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		if err := p.server.Serve(listener); err != http.ErrServerClosed {
			p.sendErr(err)
		}
	}()

	// The handler is serving before the browser opens, so a fast redirect
	// can't race a not-yet-listening server.
	p.BrowserOpened = openBrowserFn(ctx, authURL) == nil
	return p, nil
}

// sendErr delivers an error without blocking; only the first error matters
// and Wait may have already returned.
func (p *PendingOAuthFlow) sendErr(err error) {
	select {
	case p.errCh <- err:
	default:
	}
}

// Wait blocks until the redirect arrives, the context is cancelled, or the
// flow times out, then exchanges the authorization code for tokens. The
// loopback server is shut down on every exit path.
func (p *PendingOAuthFlow) Wait(ctx context.Context) (*GoogleOAuthResult, error) {
	defer p.Close()

	var code string
	select {
	case code = <-p.codeCh:
	case err := <-p.errCh:
		return nil, err
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(5 * time.Minute):
		return nil, fmt.Errorf("authorization timed out after 5 minutes")
	}

	// Exchange code for tokens
	return exchangeGoogleCode(ctx, p.clientID, p.clientSecret, code, p.redirectURI, p.codeVerifier)
}

// Close abandons the flow and releases the listener. Idempotent; safe to
// call after Wait (which closes via defer) and on a flow that never reached
// Wait — the Esc-between-Start-and-Wait window in the TUI.
func (p *PendingOAuthFlow) Close() {
	p.closeOnce.Do(func() {
		if p.server != nil {
			// Bounded: a slow-loris local client must not make Shutdown
			// (and therefore Esc/cancel in the TUI) block indefinitely.
			// The listener Close below force-drops anything still hanging.
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			_ = p.server.Shutdown(shutdownCtx)
			cancel()
		}
		if p.listener != nil {
			_ = p.listener.Close()
		}
	})
}

// flowBanner returns the exact lines GoogleOAuthFlow has always printed
// after starting the flow. Kept as a pure function so the CLI output stays
// byte-for-byte stable under test.
func flowBanner(authURL string, browserOpened bool) string {
	if browserOpened {
		return "Browser opened for Google authorization. Waiting for redirect...\n"
	}
	return fmt.Sprintf("Open this URL in your browser to authorize:\n\n  %s\n\n", authURL)
}

// GoogleOAuthFlow performs the installed-app loopback redirect flow for Google OAuth 2.0.
// It starts a temporary HTTP server, opens the user's browser, and waits for the redirect.
//
// This is the printing CLI wrapper around StartGoogleOAuthFlow + Wait; UIs
// that own their rendering call those two directly.
func GoogleOAuthFlow(ctx context.Context, clientID, clientSecret string) (*GoogleOAuthResult, error) {
	flow, err := StartGoogleOAuthFlow(ctx, clientID, clientSecret)
	if err != nil {
		return nil, err
	}
	fmt.Print(flowBanner(flow.AuthURL, flow.BrowserOpened))
	return flow.Wait(ctx)
}

func exchangeGoogleCode(ctx context.Context, clientID, clientSecret, code, redirectURI, codeVerifier string) (*GoogleOAuthResult, error) {
	return retry.Retry(ctx, tokenRetryOptions, func(ctx context.Context) (*GoogleOAuthResult, error) {
		data := url.Values{
			"code":          {code},
			"client_id":     {clientID},
			"redirect_uri":  {redirectURI},
			"grant_type":    {"authorization_code"},
			"code_verifier": {codeVerifier},
		}
		if clientSecret != "" {
			data.Set("client_secret", clientSecret)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := googleHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("token exchange: %w", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("token exchange failed (%d): %s", resp.StatusCode, body)
		}

		var result struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse token response: %w", err)
		}

		return &GoogleOAuthResult{
			AccessToken:  result.AccessToken,
			RefreshToken: result.RefreshToken,
			Expiry:       time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
		}, nil
	})
}

// RefreshGoogleToken refreshes an expired access token.
//
// Pass an empty clientSecret to omit it from the refresh request. Google's
// Desktop OAuth clients require a non-empty client secret; the caller is
// responsible for surfacing a clear error when the secret is missing.
func RefreshGoogleToken(ctx context.Context, clientID, clientSecret, refreshToken string) (*GoogleOAuthResult, error) {
	return retry.Retry(ctx, tokenRetryOptions, func(ctx context.Context) (*GoogleOAuthResult, error) {
		data := url.Values{
			"client_id":     {clientID},
			"refresh_token": {refreshToken},
			"grant_type":    {"refresh_token"},
		}
		if clientSecret != "" {
			data.Set("client_secret", clientSecret)
		}

		req, err := http.NewRequestWithContext(ctx, "POST", googleTokenURL, strings.NewReader(data.Encode()))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

		resp, err := googleHTTPClient.Do(req)
		if err != nil {
			return nil, fmt.Errorf("token refresh: %w", err)
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("token refresh failed (%d): %s", resp.StatusCode, body)
		}

		var result struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
			ExpiresIn    int    `json:"expires_in"`
		}
		if err := json.Unmarshal(body, &result); err != nil {
			return nil, fmt.Errorf("parse refresh response: %w", err)
		}

		// RFC 6749 §6: the server MAY rotate the refresh token. Persist the
		// new one when present; otherwise keep reusing the existing token.
		newRefreshToken := result.RefreshToken
		if newRefreshToken == "" {
			newRefreshToken = refreshToken
		}

		return &GoogleOAuthResult{
			AccessToken:  result.AccessToken,
			RefreshToken: newRefreshToken,
			Expiry:       time.Now().Add(time.Duration(result.ExpiresIn) * time.Second),
		}, nil
	})
}

func generateCodeVerifier() (string, error) {
	data := make([]byte, 32)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func codeChallengeS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func openBrowser(ctx context.Context, url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.CommandContext(ctx, "xdg-open", url).Start()
	case "darwin":
		return exec.CommandContext(ctx, "open", url).Start()
	case "windows":
		return exec.CommandContext(ctx, "rundll32", "url.dll,FileProtocolHandler", url).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}
