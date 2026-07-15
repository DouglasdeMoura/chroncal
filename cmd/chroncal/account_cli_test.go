package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
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

func TestAccountRemovePreservesDownloadedCalendarsAsLocal(t *testing.T) {
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
		"account", "discover", "Test account", "--all", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account discover: %v", err)
	}
	if _, _, err := runChroncalCommand(t,
		"account", "remove", "Test account", "--yes", "--allow-plaintext",
	); err != nil {
		t.Fatalf("account remove: %v", err)
	}

	a, err := app.New(dbPath)
	if err != nil {
		t.Fatalf("app.New: %v", err)
	}
	defer a.Close()
	accounts, err := a.Accounts.List(context.Background())
	if err != nil {
		t.Fatalf("list accounts: %v", err)
	}
	if len(accounts) != 0 {
		t.Fatalf("accounts after remove = %+v", accounts)
	}
	calendars, err := a.Calendars.List(context.Background())
	if err != nil {
		t.Fatalf("list calendars: %v", err)
	}
	if len(calendars) != 3 {
		t.Fatalf("calendar count after removal = %d, want original local calendar plus 2 downloads", len(calendars))
	}
	for _, calendar := range calendars {
		if calendar.AccountID != 0 || calendar.RemoteURL != "" {
			t.Fatalf("calendar still linked after account removal: %+v", calendar)
		}
	}
}

func newAccountDiscoveryServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

// resolveDiscoveredSelections turns user-facing names/paths into collection
// paths. Ambiguous names, unknown references, and unsupported component types
// are rejected up front so Import never receives an invalid selection.
func TestResolveDiscoveredSelectionsRejectsAmbiguousAndUnknown(t *testing.T) {
	t.Parallel()
	discovery := account.Discovery{Calendars: []account.DiscoveredCalendar{
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/a/", Name: "Shared"}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/b/", Name: "Shared"}, Importable: true},
		{RemoteCalendar: caldav.RemoteCalendar{Path: "/cal/c/", Name: "Availability", SupportedComponentSet: []string{"VFREEBUSY"}}, Importable: false},
	}}

	if _, err := resolveDiscoveredSelections(discovery, []string{"Shared"}); err == nil {
		t.Fatal("ambiguous calendar name should be rejected")
	}
	if _, err := resolveDiscoveredSelections(discovery, []string{"Missing"}); err == nil {
		t.Fatal("unknown calendar reference should be rejected")
	}
	if _, err := resolveDiscoveredSelections(discovery, []string{"Availability"}); err == nil {
		t.Fatal("unsupported component calendar should be rejected")
	}

	paths, err := resolveDiscoveredSelections(discovery, []string{"/cal/a/"})
	if err != nil {
		t.Fatalf("select by remote path: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/cal/a/" {
		t.Fatalf("resolved paths = %v, want [/cal/a/]", paths)
	}
}

// TestResolveAccountRejectsAmbiguousName proves that two accounts whose
// case-insensitive names collide are never silently resolved to the first
// match. The caller must disambiguate with a numeric ID.
func TestResolveAccountRejectsAmbiguousName(t *testing.T) {
	db, q, err := storage.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer db.Close()

	ctx := context.Background()
	svc := account.NewService(db, q)

	// "Work" and "WORK" are distinct under SQLite's BINARY collation so both
	// inserts succeed, but EqualFold treats them as the same name.
	accA, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "Work", ServerUrl: "https://a.test/", AuthType: "basic", Username: "alice",
	})
	if err != nil {
		t.Fatalf("create account A: %v", err)
	}
	_, err = q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "WORK", ServerUrl: "https://b.test/", AuthType: "basic", Username: "bob",
	})
	if err != nil {
		t.Fatalf("create account B: %v", err)
	}

	if _, err := resolveAccount(ctx, svc, "Work"); err == nil {
		t.Fatal("ambiguous account name should be rejected, not silently resolved to the first match")
	}
	if _, err := resolveAccount(ctx, svc, "work"); err == nil {
		t.Fatal("case-insensitive ambiguous name should be rejected")
	}

	// Numeric ID disambiguates.
	got, err := resolveAccount(ctx, svc, fmt.Sprintf("%d", accA.ID))
	if err != nil {
		t.Fatalf("resolveAccount by numeric ID: %v", err)
	}
	if got.ID != accA.ID {
		t.Fatalf("resolveAccount by ID = %d, want %d", got.ID, accA.ID)
	}
}
