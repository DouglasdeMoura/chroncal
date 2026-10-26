package caldav

import (
	"net/http"
	"testing"
)

// TestDefaultHTTPClientHasItsOwnTransport covers issue #936. A call of
// CloseIdleConnections on http.DefaultTransport (httptest.Server.Close
// makes one) must not reach the connections of the CalDAV client.
func TestDefaultHTTPClientHasItsOwnTransport(t *testing.T) {
	tr, ok := defaultHTTPClient.Transport.(*http.Transport)
	if !ok || tr == nil {
		t.Fatalf("transport = %T, want an *http.Transport of the client", defaultHTTPClient.Transport)
	}
	if tr == http.DefaultTransport {
		t.Fatal("the CalDAV client shares http.DefaultTransport")
	}
	if tr.Proxy == nil {
		t.Error("the transport lost the proxy setting of http.DefaultTransport")
	}
}
