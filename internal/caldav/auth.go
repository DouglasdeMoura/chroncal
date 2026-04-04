package caldav

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/emersion/go-webdav"

	"github.com/douglasdemoura/chroncal/internal/auth"
)

var refreshGoogleTokenFn = auth.RefreshGoogleToken

// NewClientFromCredential creates a CalDAV client using the provided credential.
func NewClientFromCredential(endpoint string, cred auth.Credential, persist func(auth.Credential) error) (*Client, error) {
	switch {
	case cred.AccessToken != "":
		httpClient := &oauth2HTTPClient{
			inner:       defaultHTTPClient,
			accessToken: cred.AccessToken,
		}
		if cred.TokenExpiry != "" {
			if expiry, err := time.Parse(time.RFC3339, cred.TokenExpiry); err == nil {
				httpClient.expiry = expiry
			}
		}
		if cred.RefreshToken != "" && cred.OAuthClientID != "" {
			httpClient.refreshFn = func(ctx context.Context) (string, time.Time, error) {
				refreshed, err := refreshGoogleTokenFn(ctx, cred.OAuthClientID, cred.RefreshToken)
				if err != nil {
					return "", time.Time{}, err
				}
				cred.AccessToken = refreshed.AccessToken
				cred.RefreshToken = refreshed.RefreshToken
				cred.TokenExpiry = refreshed.Expiry.Format(time.RFC3339)
				if persist != nil {
					if err := persist(cred); err != nil {
						return "", time.Time{}, err
					}
				}
				return refreshed.AccessToken, refreshed.Expiry, nil
			}
		}
		return NewClient(httpClient, endpoint)
	case cred.Password != "":
		httpClient := webdav.HTTPClientWithBasicAuth(defaultHTTPClient, cred.Username, cred.Password)
		return NewClient(httpClient, endpoint)
	default:
		return nil, fmt.Errorf("credential has no password or access token")
	}
}

// oauth2HTTPClient adds OAuth 2.0 Bearer token to requests.
// It supports token refresh when a refresh function is provided.
type oauth2HTTPClient struct {
	inner       *http.Client
	accessToken string
	expiry      time.Time
	refreshFn   func(ctx context.Context) (string, time.Time, error)
	mu          sync.Mutex
}

func (c *oauth2HTTPClient) Do(req *http.Request) (*http.Response, error) {
	c.mu.Lock()
	if c.refreshFn != nil && !c.expiry.IsZero() && time.Now().After(c.expiry.Add(-30*time.Second)) {
		newToken, newExpiry, err := c.refreshFn(req.Context())
		if err == nil {
			c.accessToken = newToken
			c.expiry = newExpiry
		}
	}
	token := c.accessToken
	c.mu.Unlock()

	req.Header.Set("Authorization", "Bearer "+token)
	return c.inner.Do(req)
}
