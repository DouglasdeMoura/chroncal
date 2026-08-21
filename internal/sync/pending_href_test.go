package sync

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func firstCalendarID(t *testing.T, q *storage.Queries) int64 {
	t.Helper()
	cals, err := q.ListCalendars(context.Background())
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	return cals[0].ID
}

func davXML(r *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusMultiStatus,
		Status:     "207 Multi-Status",
		Header:     http.Header{"Content-Type": []string{"application/xml"}},
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    r,
	}
}

func syncCollectionXML(token string, hrefs ...string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">`)
	for _, href := range hrefs {
		b.WriteString(`<d:response><d:href>`)
		b.WriteString(href)
		b.WriteString(`</d:href><d:propstat><d:prop><d:getetag>&quot;etag&quot;</d:getetag></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	b.WriteString(`<d:sync-token>`)
	b.WriteString(token)
	b.WriteString(`</d:sync-token></d:multistatus>`)
	return b.String()
}

func multigetXML(bodies map[string]string, missing []string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?><d:multistatus xmlns:d="DAV:" xmlns:cal="urn:ietf:params:xml:ns:caldav">`)
	for href, ics := range bodies {
		b.WriteString(`<d:response><d:href>`)
		b.WriteString(href)
		b.WriteString(`</d:href><d:propstat><d:prop><d:getetag>&quot;etag&quot;</d:getetag><cal:calendar-data>`)
		b.WriteString(ics)
		b.WriteString(`</cal:calendar-data></d:prop><d:status>HTTP/1.1 200 OK</d:status></d:propstat></d:response>`)
	}
	for _, href := range missing {
		b.WriteString(`<d:response><d:href>`)
		b.WriteString(href)
		b.WriteString(`</d:href><d:status>HTTP/1.1 404 Not Found</d:status></d:response>`)
	}
	b.WriteString(`</d:multistatus>`)
	return b.String()
}

func testEventICS(uid, summary string) string {
	return "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//chroncal//tests//EN\nBEGIN:VEVENT\nUID:" + uid + "\nDTSTAMP:20260403T120000Z\nDTSTART:20260403T120000Z\nDTEND:20260403T130000Z\nSUMMARY:" + summary + "\nEND:VEVENT\nEND:VCALENDAR\n"
}

func newScriptedClient(t *testing.T, onSync func(req string) string, onMultiget func(n int, req string) string) *caldav.Client {
	t.Helper()
	var n int
	return newTestCalDAVClient(t, func(r *http.Request) (*http.Response, error) {
		if r.Method != "REPORT" {
			t.Fatalf("unexpected %s %s", r.Method, r.URL.Path)
			return nil, nil
		}
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read req body: %v", err)
		}
		body := string(raw)
		if strings.Contains(body, "calendar-multiget") {
			n++
			return davXML(r, onMultiget(n, body)), nil
		}
		return davXML(r, onSync(body)), nil
	})
}

func listPendingHrefs(t *testing.T, q *storage.Queries, calendarID int64) []storage.SyncPendingHref {
	t.Helper()
	rows, err := q.ListSyncPendingHrefsByCalendar(context.Background(), calendarID)
	if err != nil {
		t.Fatalf("ListSyncPendingHrefsByCalendar: %v", err)
	}
	return rows
}

func calendarToken(t *testing.T, q *storage.Queries, calendarID int64) string {
	t.Helper()
	cal, err := q.GetCalendar(context.Background(), calendarID)
	if err != nil {
		t.Fatalf("GetCalendar: %v", err)
	}
	return storage.NullableToString(cal.SyncToken)
}

func TestPullViewKnownMissBlocksBothGates(t *testing.T) {
	t.Parallel()

	empty := pullView{}
	if !empty.inventoryObserved() || !empty.localRowsSafe() || empty.incomplete() {
		t.Fatalf("zero view must be complete: %+v", empty)
	}

	unknownRecordFail := pullView{pendingRecordFails: 1}
	if !unknownRecordFail.inventoryObserved() {
		t.Fatal("a pending-record failure has no local row to lose")
	}
	if unknownRecordFail.localRowsSafe() {
		t.Fatal("a pending-record failure must withhold the token")
	}
	if !unknownRecordFail.incomplete() {
		t.Fatal("a pending-record failure must surface as incomplete")
	}

	known := pullView{knownMisses: 1}
	if known.inventoryObserved() || known.localRowsSafe() {
		t.Fatal("a known miss must block absence deletion and the token")
	}
}

func TestPendingHrefsNoteMissDropsAfterBudget(t *testing.T) {
	t.Parallel()

	_, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)
	pending, err := loadPendingHrefs(ctx, q, discardLogger(), calendarID)
	if err != nil {
		t.Fatalf("loadPendingHrefs: %v", err)
	}
	const href = "/calendar/phantom-invite.ics"
	for i := 0; i < pendingHrefMissLimit; i++ {
		if err := pending.noteMiss(ctx, href); err != nil {
			t.Fatalf("noteMiss %d: %v", i, err)
		}
	}
	if rows := listPendingHrefs(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("pending hrefs = %d, want 0 after the miss budget", len(rows))
	}
}

func TestPendingHrefsAppendUnseenAndForget(t *testing.T) {
	t.Parallel()

	_, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)
	pending, err := loadPendingHrefs(ctx, q, discardLogger(), calendarID)
	if err != nil {
		t.Fatalf("loadPendingHrefs: %v", err)
	}
	const href = "/calendar/phantom-invite.ics"
	if err := pending.noteMiss(ctx, href); err != nil {
		t.Fatalf("noteMiss: %v", err)
	}
	got := pending.appendUnseen(nil)
	if len(got) != 1 || got[0] != href {
		t.Fatalf("appendUnseen(nil) = %v, want [%s]", got, href)
	}
	got = pending.appendUnseen([]string{href})
	if len(got) != 1 {
		t.Fatalf("appendUnseen already-seen = %v, want one href", got)
	}
	pending.forget(ctx, href)
	if got := pending.appendUnseen(nil); len(got) != 0 {
		t.Fatalf("appendUnseen after forget = %v, want none", got)
	}
	if rows := listPendingHrefs(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("pending hrefs after forget = %d, want 0", len(rows))
	}
}

func TestPendingHrefsForgetSkipsHrefNotInTable(t *testing.T) {
	t.Parallel()

	_, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)
	pending, err := loadPendingHrefs(ctx, q, discardLogger(), calendarID)
	if err != nil {
		t.Fatalf("loadPendingHrefs: %v", err)
	}
	pending.forgetSet(ctx, map[string]bool{"/calendar/never-pending.ics": true})
	if rows := listPendingHrefs(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("pending hrefs = %d, want 0", len(rows))
	}
}

// TestEnginePullUnknownMultigetMissAdvancesToken reproduces issue #576.
// Google can list stale invitation hrefs on the initial snapshot. Those
// hrefs 404 on every multiget and have no local row. A count of that miss
// as incomplete withholds the token forever. Fetchable resources must
// persist and the token must advance. The pending table must record the
// unknown href. A miss for a local row still withholds the token (see
// TestEnginePullToleratesMultigetMissingPath).
func TestEnginePullUnknownMultigetMissAdvancesToken(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	const alive = "/calendar/alive.ics"
	const phantom = "/calendar/phantom-invite.ics"
	client := newScriptedClient(t,
		func(string) string {
			return syncCollectionXML("https://example.com/sync/after-phantoms", alive, phantom)
		},
		func(int, string) string {
			return multigetXML(map[string]string{alive: testEventICS("alive-uid", "Alive")}, []string{phantom})
		},
	)

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.pulled != 1 {
		t.Fatalf("pulled = %d, want 1 (alive event)", result.pulled)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0", result.deleted)
	}
	if len(result.errors) != 0 {
		t.Fatalf("errors = %v, want none (unknown miss must not mark the pull incomplete)", result.errors)
	}
	if _, err := q.GetEventByUID(ctx, "alive-uid"); err != nil {
		t.Fatalf("alive event missing: %v", err)
	}
	if tok := calendarToken(t, q, calendarID); tok != "https://example.com/sync/after-phantoms" {
		t.Fatalf("sync_token = %q, want the snapshot token (unknown miss must not withhold it)", tok)
	}
	rows := listPendingHrefs(t, q, calendarID)
	if len(rows) != 1 || rows[0].Href != phantom || rows[0].MissCount != 1 {
		t.Fatalf("pending hrefs = %+v, want one row for %s with miss_count 1", rows, phantom)
	}
}

// TestEnginePullUnknownMultigetMissAllowsAbsenceDeletion pins the
// completeness rule for issue #576. An unknown phantom does not make the
// inventory incomplete. A local row that the snapshot does not list is
// then a real absence and must be deleted.
func TestEnginePullUnknownMultigetMissAllowsAbsenceDeletion(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	insertTestEvent(t, db, calendarID, "gone")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "gone", OwnerType: "event",
		RemoteUrl: "/calendar/gone.ics", Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource gone: %v", err)
	}

	const phantom = "/calendar/phantom-invite.ics"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML("https://example.com/sync/t1", phantom) },
		func(int, string) string { return multigetXML(nil, []string{phantom}) },
	)

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.deleted != 1 {
		t.Fatalf("deleted = %d, want 1 (unknown miss must not withhold absence deletion)", result.deleted)
	}
	if _, err := q.GetEventByUID(ctx, "gone"); err == nil {
		t.Fatal("gone row survived a complete snapshot that did not list it")
	}
	if tok := calendarToken(t, q, calendarID); tok != "https://example.com/sync/t1" {
		t.Fatalf("sync_token = %q, want t1", tok)
	}
}

// TestEnginePullRetriesUnknownMultigetMissAfterTokenAdvance covers the
// other half of issue #576. After the token advances past a phantom href,
// a later pull must still fetch that href. Otherwise a new event that
// 404s once is lost until the server touches it again.
func TestEnginePullRetriesUnknownMultigetMissAfterTokenAdvance(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	const phantom = "/calendar/phantom-invite.ics"
	client := newScriptedClient(t,
		func(req string) string {
			if strings.Contains(req, "https://example.com/sync/t1") {
				return syncCollectionXML("https://example.com/sync/t2")
			}
			return syncCollectionXML("https://example.com/sync/t1", phantom)
		},
		func(n int, req string) string {
			if !strings.Contains(req, phantom) {
				t.Fatalf("multiget %d omitted the pending href", n)
			}
			if n == 1 {
				return multigetXML(nil, []string{phantom})
			}
			return multigetXML(map[string]string{phantom: testEventICS("late-invite", "Late invite")}, nil)
		},
	)

	if _, err := engine.pull(ctx, client, calendarID, "/calendar/"); err != nil {
		t.Fatalf("first pull: %v", err)
	}
	if _, err := q.GetEventByUID(ctx, "late-invite"); err == nil {
		t.Fatal("late-invite imported on the first pull; the 404 was ignored")
	}
	if tok := calendarToken(t, q, calendarID); tok != "https://example.com/sync/t1" {
		t.Fatalf("sync_token after first pull = %q, want t1", tok)
	}

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("second pull: %v", err)
	}
	if result.pulled != 1 {
		t.Fatalf("second pull pulled = %d, want 1", result.pulled)
	}
	if _, err := q.GetEventByUID(ctx, "late-invite"); err != nil {
		t.Fatalf("late-invite missing after retry: %v", err)
	}
	if rows := listPendingHrefs(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("pending hrefs = %d, want 0 after a successful retry", len(rows))
	}
}

// TestEnginePullDropsUnknownMultigetMissAfterBudget pins the miss budget
// for issue #576. A stale Google href that 404s on every pull must not
// retry forever. After pendingHrefMissLimit misses the engine drops it.
func TestEnginePullDropsUnknownMultigetMissAfterBudget(t *testing.T) {
	t.Parallel()

	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	const phantom = "/calendar/phantom-invite.ics"
	var phantomGets int
	client := newScriptedClient(t,
		func(req string) string {
			if strings.Contains(req, "https://example.com/sync/") {
				return syncCollectionXML("https://example.com/sync/t2")
			}
			return syncCollectionXML("https://example.com/sync/t1", phantom)
		},
		func(_ int, req string) string {
			if strings.Contains(req, phantom) {
				phantomGets++
			}
			return multigetXML(nil, []string{phantom})
		},
	)

	for i := 0; i < pendingHrefMissLimit+2; i++ {
		if _, err := engine.pull(ctx, client, calendarID, "/calendar/"); err != nil {
			t.Fatalf("pull %d: %v", i+1, err)
		}
	}
	if phantomGets != pendingHrefMissLimit {
		t.Fatalf("phantom multigets = %d, want %d", phantomGets, pendingHrefMissLimit)
	}
	if rows := listPendingHrefs(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("pending hrefs = %d, want 0 after the miss budget", len(rows))
	}
}

// TestEnginePullUncanonicalMultigetMissAdvancesToken covers issue #625.
// A server can report a 404 under an href that CanonicalObjectRef rejects:
// a query string, another origin, or a collection path. No local row can
// map to such an href, so the miss carries no data-loss signal. The engine
// must skip it: no known-miss count, no pending row, and the token
// advances. The same table pins the old rules for canonical 404s: an
// unknown miss takes the budget path, and a miss for a local row still
// withholds the token.
func TestEnginePullUncanonicalMultigetMissAdvancesToken(t *testing.T) {
	t.Parallel()

	const (
		token1  = "https://example.com/sync/t1"
		alive   = "/calendar/alive.ics"
		phantom = "/calendar/phantom-invite.ics"
		racey   = "/calendar/racey.ics"
	)

	cases := []struct {
		name        string
		seedLocal   string   // non-empty seeds a local row for this UID at racey
		missing     []string // hrefs the multiget response reports as 404
		wantToken   string
		wantErrors  bool
		wantPulled  int
		wantDeleted int
		wantPending int
	}{
		{
			name:        "query string",
			missing:     []string{phantom + "?rev=2"},
			wantToken:   token1,
			wantPulled:  1,
			wantPending: 0,
		},
		{
			name:        "other origin",
			missing:     []string{"https://evil.example.com/calendar/phantom-invite.ics"},
			wantToken:   token1,
			wantPulled:  1,
			wantPending: 0,
		},
		{
			name:        "root collection path",
			missing:     []string{"/"},
			wantToken:   token1,
			wantPulled:  1,
			wantPending: 0,
		},
		{
			name:        "canonical unknown miss still records and advances",
			missing:     []string{phantom},
			wantToken:   token1,
			wantPulled:  1,
			wantPending: 1,
		},
		{
			name:        "canonical known miss still withholds the token",
			seedLocal:   "racey",
			missing:     []string{racey},
			wantToken:   "",
			wantErrors:  true,
			wantPulled:  1,
			wantDeleted: 0,
			wantPending: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine, db, q := newTestEngine(t)
			ctx := context.Background()
			calendarID := firstCalendarID(t, q)

			listed := []string{alive, phantom}
			if tc.seedLocal != "" {
				insertTestEvent(t, db, calendarID, tc.seedLocal)
				if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
					CalendarID: calendarID, Uid: tc.seedLocal, OwnerType: "event",
					RemoteUrl: racey, Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
				}); err != nil {
					t.Fatalf("UpsertSyncResource %s: %v", tc.seedLocal, err)
				}
				listed = append(listed, racey)
			}

			client := newScriptedClient(t,
				func(string) string { return syncCollectionXML(token1, listed...) },
				func(int, string) string {
					return multigetXML(map[string]string{alive: testEventICS("alive-uid", "Alive")}, tc.missing)
				},
			)

			result, err := engine.pull(ctx, client, calendarID, "/calendar/")
			if err != nil {
				t.Fatalf("pull: %v", err)
			}
			if result.pulled != tc.wantPulled {
				t.Fatalf("pulled = %d, want %d", result.pulled, tc.wantPulled)
			}
			if result.deleted != tc.wantDeleted {
				t.Fatalf("deleted = %d, want %d", result.deleted, tc.wantDeleted)
			}
			if (len(result.errors) > 0) != tc.wantErrors {
				t.Fatalf("errors = %v, wantErrors = %v", result.errors, tc.wantErrors)
			}
			if tok := calendarToken(t, q, calendarID); tok != tc.wantToken {
				t.Fatalf("sync_token = %q, want %q", tok, tc.wantToken)
			}
			rows := listPendingHrefs(t, q, calendarID)
			if len(rows) != tc.wantPending {
				t.Fatalf("pending hrefs = %+v, want %d row(s)", rows, tc.wantPending)
			}
			if tc.wantPending == 1 {
				if rows[0].Href != phantom || rows[0].MissCount != 1 {
					t.Fatalf("pending hrefs = %+v, want one row for %s with miss_count 1", rows, phantom)
				}
			}
			if _, err := q.GetEventByUID(ctx, "alive-uid"); err != nil {
				t.Fatalf("alive event missing: %v", err)
			}
			if tc.seedLocal != "" {
				if _, err := q.GetEventByUID(ctx, tc.seedLocal); err != nil {
					t.Fatalf("%s row (known miss) was wrongly deleted: %v", tc.seedLocal, err)
				}
			}
		})
	}
}

// TestEnginePullMixedKnownAndUnknownMultigetMissWithholdsToken pins the
// split completeness rule. A known miss still withholds the token and
// absence deletion. An unknown phantom in the same REPORT does not
// override that guard.
func TestEnginePullMixedKnownAndUnknownMultigetMissWithholdsToken(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	insertTestEvent(t, db, calendarID, "gone")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "gone", OwnerType: "event",
		RemoteUrl: "/calendar/gone.ics", Etag: "e1", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource gone: %v", err)
	}
	insertTestEvent(t, db, calendarID, "racey")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "racey", OwnerType: "event",
		RemoteUrl: "/calendar/racey.ics", Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource racey: %v", err)
	}

	const racey = "/calendar/racey.ics"
	const phantom = "/calendar/phantom-invite.ics"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML("https://example.com/sync/t1", racey, phantom) },
		func(int, string) string { return multigetXML(nil, []string{racey, phantom}) },
	)

	result, err := engine.pull(ctx, client, calendarID, "/calendar/")
	if err != nil {
		t.Fatalf("pull: %v", err)
	}
	if result.deleted != 0 {
		t.Fatalf("deleted = %d, want 0 (known miss must withhold absence deletion)", result.deleted)
	}
	if len(result.errors) == 0 {
		t.Fatal("known miss must surface an incomplete pull")
	}
	if _, err := q.GetEventByUID(ctx, "gone"); err != nil {
		t.Fatalf("gone row was wrongly deleted: %v", err)
	}
	if _, err := q.GetEventByUID(ctx, "racey"); err != nil {
		t.Fatalf("racey row (known miss) was wrongly deleted: %v", err)
	}
	if tok := calendarToken(t, q, calendarID); tok != "" {
		t.Fatalf("sync_token = %q, want empty", tok)
	}
	rows := listPendingHrefs(t, q, calendarID)
	if len(rows) != 1 || rows[0].Href != phantom {
		t.Fatalf("pending hrefs = %+v, want one row for %s", rows, phantom)
	}
}
