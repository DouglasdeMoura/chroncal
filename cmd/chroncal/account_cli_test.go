package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
)

func TestAccountAddAndList(t *testing.T) {
	setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")

	stdout, _, err := runChroncalCommand(t,
		"account", "add", "Personal Google",
		"--server", "https://calendar.example.test/caldav/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account add: %v", err)
	}
	if !strings.Contains(stdout, `Account "Personal Google" added`) {
		t.Fatalf("account add output = %q", stdout)
	}

	stdout, _, err = runChroncalCommand(t, "account", "list", "--output", "json")
	if err != nil {
		t.Fatalf("account list: %v", err)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("decode account list: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0]["name"] != "Personal Google" || rows[0]["username"] != "me@example.test" {
		t.Fatalf("account list = %#v", rows)
	}
}

func TestAccountDiscoverImportsAllUsableCollectionsIdempotently(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)

	if _, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account add: %v", err)
	}

	stdout, _, err := runChroncalCommand(t,
		"account", "discover", "Test account", "--all", "--allow-plaintext",
	)
	if err != nil {
		t.Fatalf("account discover --all: %v", err)
	}
	if !strings.Contains(stdout, "Imported 2 calendars") || !strings.Contains(stdout, "Availability") {
		t.Fatalf("discover output = %q", stdout)
	}

	// Repeating the same complete discovery must reuse both linked rows.
	if _, _, err := runChroncalCommand(t,
		"account", "discover", "Test account", "--all", "--allow-plaintext",
	); err != nil {
		t.Fatalf("repeat account discover --all: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	var imported int
	for _, calendar := range calendars {
		if calendar.AccountID != 0 {
			imported++
		}
	}
	if imported != 2 {
		t.Fatalf("linked calendar count = %d, want 2 after repeat discovery", imported)
	}
}

func TestAccountDiscoverSelectsCalendarByName(t *testing.T) {
	dbPath := setupCalendarCLITestEnv(t)
	t.Setenv("CHRONCAL_BEARER_TOKEN", "test-token")
	srv := newAccountDiscoveryServer(t)

	if _, _, err := runChroncalCommand(t,
		"account", "add", "Test account",
		"--server", srv.URL+"/",
		"--username", "me@example.test",
		"--auth", "bearer",
		"--allow-insecure",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account add: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"account", "discover", "Test account",
		"--select", "Holidays in Brazil",
		"--allow-plaintext",
	); err != nil {
		t.Fatalf("account discover --select: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	for _, calendar := range calendars {
		if calendar.AccountID != 0 {
			if calendar.Name != "Holidays in Brazil" {
				t.Fatalf("selected calendar = %q", calendar.Name)
			}
			return
		}
	}
	t.Fatal("selected remote calendar was not imported")
}

func newAccountDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-token" {
			t.Errorf("Authorization = %q, want bearer token", got)
		}
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.WriteHeader(http.StatusMultiStatus)
		var body string
		switch r.URL.Path {
		case "/":
			body = accountMultistatus(`/`, `<d:current-user-principal><d:href>/principals/me/</d:href></d:current-user-principal>`)
		case "/principals/me/":
			body = accountMultistatus(`/principals/me/`, `<c:calendar-home-set><d:href>/calendars/me/</d:href></c:calendar-home-set>`)
		case "/calendars/me/":
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>/calendars/me/personal/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Personal</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/calendars/me/holidays/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Holidays in Brazil</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VEVENT"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
  <d:response><d:href>/calendars/me/freebusy/</d:href><d:propstat><d:prop>
    <d:resourcetype><d:collection/><c:calendar/></d:resourcetype><d:displayname>Availability</d:displayname>
    <c:supported-calendar-component-set><c:comp name="VFREEBUSY"/></c:supported-calendar-component-set>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`
		case "/calendars/me/personal/":
			body = accountCalendarMetadata("Personal", "#112233", "write")
		case "/calendars/me/holidays/":
			body = accountCalendarMetadata("Holidays in Brazil", "#445566", "read")
		case "/calendars/me/freebusy/":
			body = accountCalendarMetadata("Availability", "#778899", "read")
		default:
			t.Errorf("unexpected discovery request path %q", r.URL.Path)
			body = accountMultistatus(r.URL.Path, "")
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func accountMultistatus(href, prop string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav">
  <d:response><d:href>%s</d:href><d:propstat><d:prop>%s</d:prop>
  <d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, href, prop)
}

func accountCalendarMetadata(name, color, privilege string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:c="urn:ietf:params:xml:ns:caldav" xmlns:ic="http://apple.com/ns/ical/">
  <d:response><d:href>/</d:href><d:propstat><d:prop>
    <d:displayname>%s</d:displayname><d:resourcetype><d:collection/><c:calendar/></d:resourcetype>
    <d:current-user-privilege-set><d:privilege><d:%s/></d:privilege></d:current-user-privilege-set>
    <ic:calendar-color>%s</ic:calendar-color>
  </d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>
</d:multistatus>`, name, privilege, color)
}
