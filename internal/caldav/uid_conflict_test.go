package caldav

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// TestPutResourceReportsNoUIDConflict covers the RFC 4791 section 5.3.2.1
// answer to a duplicate UID: 403 with the CALDAV:no-uid-conflict
// precondition. IsUIDConflict must report it, and the error must keep the
// 403 status. A plain 403, and a 403 with another precondition, must not
// count.
func TestPutResourceReportsNoUIDConflict(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want bool
	}{
		{"no-uid-conflict", `<?xml version="1.0"?><D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:no-uid-conflict><D:href>/cal/a/x.ics</D:href></C:no-uid-conflict></D:error>`, true},
		{"other precondition", `<?xml version="1.0"?><D:error xmlns:D="DAV:" xmlns:C="urn:ietf:params:xml:ns:caldav"><C:valid-calendar-data/></D:error>`, false},
		{"element in another namespace", `<?xml version="1.0"?><D:error xmlns:D="DAV:"><D:no-uid-conflict/></D:error>`, false},
		{"plain 403", "forbidden", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			client := newSyncCollectionClient(t, func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusForbidden,
					Status:     "403 Forbidden",
					Header:     http.Header{"Content-Type": []string{"application/xml"}},
					Body:       io.NopCloser(strings.NewReader(tc.body)),
					Request:    req,
				}, nil
			})
			_, err := client.PutResource(context.Background(), "/cal/b/x.ics", testCalendar(t), "")
			if err == nil {
				t.Fatal("PutResource = nil, want the 403 error")
			}
			if got := IsUIDConflict(err); got != tc.want {
				t.Fatalf("IsUIDConflict(%v) = %v, want %v", err, got, tc.want)
			}
			if IsConflict(err) {
				t.Fatalf("IsConflict(%v) = true, want false for a 403", err)
			}
		})
	}
}
