package sync

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// ImportFile reports what it could not represent — a malformed DTEND replaced
// by a fabricated span, an alarm dropped for an unusable trigger. The pull path
// threw those away, so a value the server still holds correctly could be
// replaced by our fabrication on the next push with nothing anywhere saying so.
// The user's only signal is the log, so the pull has to write it.
func TestEnginePullSurfacesImportWarnings(t *testing.T) {
	t.Parallel()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	db, q := testutil.NewTestDB(t)
	engine := NewEngine(db, q, &mockCredStore{creds: make(map[int64]auth.Credential)},
		calendar.NewService(db, q), event.NewService(db, q), todo.NewService(db, q),
		journal.NewService(db, q), logger)

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const reportBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop><d:getetag>&quot;etag-1&quot;</d:getetag></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:warned-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:garbage
SUMMARY:Event with an unparseable DTEND
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := reportBody
		if strings.Contains(string(raw), "calendar-multiget") {
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-1&quot;</d:getetag>
        <cal:calendar-data>` + fetchBody + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	if _, err := engine.pull(ctx, client, calendarID, "/calendar/"); err != nil {
		t.Fatalf("pull: %v", err)
	}

	out := logBuf.String()
	if !strings.Contains(out, "DTEND") {
		t.Errorf("the import warning never reached the log, so the fabricated span "+
			"is invisible before it is pushed back over the server's value; log:\n%s", out)
	}
	if !strings.Contains(out, "warned-uid") && !strings.Contains(out, "warned.ics") {
		t.Errorf("the warning does not identify the resource it came from; log:\n%s", out)
	}
}

// A multi-component payload (412 server-wins resolution, manual conflict
// resolution) can carry a warning produced by ANY of its components.
// Labeling every warning with the first component's UID sends the user to
// an event that has nothing wrong with it, so the label must be omitted
// unless the payload holds exactly one component.
func TestLogImportWarningsUIDLabeling(t *testing.T) {
	t.Parallel()

	t.Run("multi-component payload omits the uid label", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		result := icalImportResult(
			[]string{"first-uid", "second-uid"},
			"WARNING: VEVENT second-uid: malformed DTEND",
		)
		logImportWarnings(logger, collectImportWarnings("/calendar/multi.ics", result))

		out := logBuf.String()
		if strings.Contains(out, "first-uid") {
			t.Errorf("warning from a multi-component payload is blamed on the first "+
				"component's UID; the warning may belong to another component; log:\n%s", out)
		}
		if !strings.Contains(out, "/calendar/multi.ics") || !strings.Contains(out, "malformed DTEND") {
			t.Errorf("warning lost its path or text; log:\n%s", out)
		}
	})

	t.Run("single-component payload keeps the uid label", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		result := icalImportResult([]string{"only-uid"}, "WARNING: VEVENT only-uid: malformed DTEND")
		logImportWarnings(logger, collectImportWarnings("/calendar/single.ics", result))

		out := logBuf.String()
		if !strings.Contains(out, "only-uid") {
			t.Errorf("single-component payload lost its uid label; log:\n%s", out)
		}
	})

	// A recurring event's resource imports as master + overrides sharing ONE
	// UID — the common case. Whatever component the warning belongs to, the
	// UID is unambiguous, so counting components (instead of distinct UIDs)
	// dropped the label exactly where users need it most.
	t.Run("master plus override sharing one uid keep the label", func(t *testing.T) {
		t.Parallel()
		var logBuf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&logBuf, nil))

		// The message deliberately omits the UID: only the structured uid
		// attribute proves the label was attached.
		result := icalImportResult(
			[]string{"series-uid", "series-uid"},
			"WARNING: malformed DTEND on a recurrence override",
		)
		warnings := collectImportWarnings("/calendar/series.ics", result)
		if len(warnings) != 1 || warnings[0].UID != "series-uid" {
			t.Fatalf("collectImportWarnings = %+v, want one warning labeled series-uid", warnings)
		}
		logImportWarnings(logger, warnings)

		out := logBuf.String()
		if !strings.Contains(out, "series-uid") {
			t.Errorf("master+override payload sharing one UID lost its uid label; log:\n%s", out)
		}
	})
}

// icalImportResult builds an ImportResult with one event per uid and a single
// import warning.
func icalImportResult(uids []string, warning string) icalPkg.ImportResult {
	events := make([]event.Event, 0, len(uids))
	for _, uid := range uids {
		events = append(events, event.Event{UID: uid})
	}
	return icalPkg.ImportResult{Events: events, Warnings: []string{warning}}
}

// The shipped entry points that matter most — the first pull after linking,
// opportunistic save-time pushes, every TUI sync — construct the engine with
// a DISCARDED logger, so a warning that only reaches the logger reaches
// /dev/null. The warning has to travel as data on the pull result.
func TestEnginePullCollectsImportWarningsWithoutLogger(t *testing.T) {
	t.Parallel()

	db, q := testutil.NewTestDB(t)
	engine := NewEngine(db, q, &mockCredStore{creds: make(map[int64]auth.Credential)},
		calendar.NewService(db, q), event.NewService(db, q), todo.NewService(db, q),
		journal.NewService(db, q), slog.New(slog.DiscardHandler))

	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	const reportBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop><d:getetag>&quot;etag-1&quot;</d:getetag></d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
  <d:sync-token>https://example.com/sync/abc</d:sync-token>
</d:multistatus>`

	const fetchBody = `BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:warned-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:garbage
SUMMARY:Event with an unparseable DTEND
END:VEVENT
END:VCALENDAR
`

	client := newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := reportBody
		if strings.Contains(string(raw), "calendar-multiget") {
			body = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-1&quot;</d:getetag>
        <cal:calendar-data>` + fetchBody + `</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`
		}
		return &http.Response{
			StatusCode: http.StatusMultiStatus,
			Status:     "207 Multi-Status",
			Header:     http.Header{"Content-Type": []string{"application/xml"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    r,
		}, nil
	})

	pullRes, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}

	if len(pullRes.warnings) == 0 {
		t.Fatal("pull returned no import warnings; with the logger discarded the fabricated span is invisible")
	}
	w := pullRes.warnings[0]
	if !strings.Contains(w.Message, "DTEND") {
		t.Errorf("warning message = %q, want the DTEND fabrication", w.Message)
	}
	if !strings.Contains(w.Path, "warned.ics") {
		t.Errorf("warning path = %q, want the resource path", w.Path)
	}
	if w.UID != "warned-uid" {
		t.Errorf("warning uid = %q, want %q (single-component payload)", w.UID, "warned-uid")
	}
}

// SyncCalendar is the surface the CLI and TUI actually call, so the pull
// phase's warnings must survive onto the returned SyncResult.
func TestEngineSyncCalendarResultCarriesImportWarnings(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	engine := NewEngine(db, q, &mockCredStore{creds: make(map[int64]auth.Credential)},
		calendar.NewService(db, q), event.NewService(db, q), todo.NewService(db, q),
		journal.NewService(db, q), slog.New(slog.DiscardHandler))
	ctx := context.Background()

	const queryAllBody = `<?xml version="1.0" encoding="utf-8"?>
<d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">
  <d:response>
    <d:href>/calendar/warned.ics</d:href>
    <d:propstat>
      <d:prop>
        <d:getetag>&quot;etag-1&quot;</d:getetag>
        <cal:calendar-data>BEGIN:VCALENDAR
VERSION:2.0
PRODID:-//chroncal//tests//EN
BEGIN:VEVENT
UID:warned-uid
DTSTAMP:20260403T120000Z
DTSTART:20260403T120000Z
DTEND:garbage
SUMMARY:Event with an unparseable DTEND
END:VEVENT
END:VCALENDAR
</cal:calendar-data>
      </d:prop>
      <d:status>HTTP/1.1 200 OK</d:status>
    </d:propstat>
  </d:response>
</d:multistatus>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "REPORT" {
			http.Error(w, "read-only collection rejects metadata and writes", http.StatusForbidden)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "sync-collection") {
			http.Error(w, "sync-collection unsupported", http.StatusUnprocessableEntity)
			return
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, queryAllBody)
	}))
	defer server.Close()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "warned", ServerUrl: server.URL, AuthType: "basic", Username: "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	remoteURL := server.URL + "/calendar"
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: calendarID, AccountID: &account.ID, RemoteUrl: &remoteURL,
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE calendars SET remote_access = 'read' WHERE id = ?", calendarID); err != nil {
		t.Fatalf("set remote access: %v", err)
	}
	engine.credStore.(*mockCredStore).creds[account.ID] = auth.Credential{
		AccountID: account.ID, Username: "user", Password: "secret",
	}

	result, err := engine.SyncCalendar(ctx, calendarID, ConflictServerWins)
	if err != nil {
		t.Fatalf("SyncCalendar: %v", err)
	}
	if len(result.Errors) != 0 {
		t.Fatalf("sync errors = %v", result.Errors)
	}
	if len(result.Warnings) == 0 {
		t.Fatal("SyncResult.Warnings is empty; the DTEND fabrication never left the engine, " +
			"so callers built with a discarded logger cannot show it")
	}
	w := result.Warnings[0]
	if !strings.Contains(w.Message, "DTEND") || !strings.Contains(w.Path, "warned.ics") || w.UID != "warned-uid" {
		t.Errorf("warning = %+v, want DTEND message, warned.ics path, and uid warned-uid", w)
	}
}
