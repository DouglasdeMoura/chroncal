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

// NewClientFromCredential creates a CalDAV client using the provided credential.
func NewClientFromCredential(endpoint string, cred auth.Credential) (*Client, error) {
	switch {
	case cred.AccessToken != "":
		httpClient := &oauth2HTTPClient{
			inner:       http.DefaultClient,
			accessToken: cred.AccessToken,
		}
		return NewClient(httpClient, endpoint)
	case cred.Password != "":
		httpClient := webdav.HTTPClientWithBasicAuth(http.DefaultClient, cred.Username, cred.Password)
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
