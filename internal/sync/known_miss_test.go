package sync

import (
	"context"
	"database/sql"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

const knownMissHref = "/calendar/stuck.ics"

// seedKnownMissCalendar links the first calendar to an account and stores
// one synced event at knownMissHref. The stored etag differs from the etag
// that syncCollectionXML lists, so each pull fetches the href again.
func seedKnownMissCalendar(t *testing.T, db *sql.DB, q *storage.Queries) int64 {
	t.Helper()
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "known-miss",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        calendarID,
		AccountID: &account.ID,
		RemoteUrl: storage.StringToNullable("/calendar/"),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	insertTestEvent(t, db, calendarID, "stuck")
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "stuck", OwnerType: "event",
		RemoteUrl: knownMissHref, Etag: "old", Dirty: 0, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	return calendarID
}

// TestEngineKnownHrefStopsBlockingAfterBudget is the regression test for
// issue #816. The server keeps a bad answer for an href that has a local
// row: an unparseable body or a 404. Before the fix, each sync withheld the
// sync-token and the calendar kept a sync error forever. After the retry
// budget, the token must advance, the calendar must be healthy, the local
// row must stay, and sync status must name the href.
func TestEngineKnownHrefStopsBlockingAfterBudget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		multiget   func(int, string) string
		wantReason string
	}{
		{
			name: "unparseable body",
			multiget: func(int, string) string {
				return multigetXML(map[string]string{knownMissHref: "this is not an ical body"}, nil)
			},
			wantReason: knownMissReasonUnparseable,
		},
		{
			name:       "404",
			multiget:   func(int, string) string { return multigetXML(nil, []string{knownMissHref}) },
			wantReason: knownMissReasonMissing,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			engine, db, q := newTestEngine(t)
			advance := fakeEngineClock(engine)
			ctx := context.Background()
			calendarID := seedKnownMissCalendar(t, db, q)
			svc := &Service{engine: engine, db: db, q: q}

			const token = "https://example.com/sync/t1"
			client := newScriptedClient(t,
				func(string) string { return syncCollectionXML(token, knownMissHref) },
				tc.multiget,
			)

			pullOnce := func(run int) *pullResult {
				t.Helper()
				result, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt)
				if err != nil {
					t.Fatalf("pull %d: %v", run, err)
				}
				sr := &SyncResult{CalendarID: calendarID}
				sr.Errors = append(sr.Errors, result.errors...)
				if err := engine.updateSyncHealth(ctx, calendarID, time.Now().UTC().Format(time.RFC3339), sr, nil); err != nil {
					t.Fatalf("updateSyncHealth %d: %v", run, err)
				}
				return result
			}
			lastError := func() string {
				t.Helper()
				cal, err := q.GetCalendar(ctx, calendarID)
				if err != nil {
					t.Fatalf("GetCalendar: %v", err)
				}
				return storage.NullableToString(cal.LastSyncError)
			}

			for run := 1; run < knownMissLimit; run++ {
				result := pullOnce(run)
				if len(result.errors) == 0 {
					t.Fatalf("run %d: no incomplete-pull error inside the budget", run)
				}
				if tok := calendarToken(t, q, calendarID); tok != "" {
					t.Fatalf("run %d: sync_token = %q, want it withheld inside the budget", run, tok)
				}
				if lastError() == "" {
					t.Fatalf("run %d: LastSyncError empty inside the budget", run)
				}
			}

			// The give-up needs the time span from the first miss too.
			advance(knownMissMinSpan)
			result := pullOnce(knownMissLimit)
			if len(result.errors) != 0 {
				t.Fatalf("run %d: errors = %v, want none after the budget", knownMissLimit, result.errors)
			}
			if !hasWarningWith(result.warnings, knownMissHref, "gave up") {
				t.Fatalf("run %d: warnings = %v, want a gave-up warning that names %s", knownMissLimit, result.warnings, knownMissHref)
			}
			if tok := calendarToken(t, q, calendarID); tok != token {
				t.Fatalf("run %d: sync_token = %q, want %q", knownMissLimit, tok, token)
			}

			// The server keeps the bad answer. Later runs must stay healthy.
			for run := knownMissLimit + 1; run <= knownMissLimit+2; run++ {
				pullOnce(run)
				if tok := calendarToken(t, q, calendarID); tok != token {
					t.Fatalf("run %d: sync_token = %q, want %q after the budget", run, tok, token)
				}
				if got := lastError(); got != "" {
					t.Fatalf("run %d: LastSyncError = %q, want empty after the budget", run, got)
				}
			}

			if _, err := q.GetEventByCalendarUID(ctx, storage.GetEventByCalendarUIDParams{CalendarID: 1, Uid: "stuck"}); err != nil {
				t.Fatalf("local row was deleted: %v", err)
			}
			rows := listKnownMisses(t, q, calendarID)
			if len(rows) != 1 || rows[0].Reason != tc.wantReason || rows[0].GaveUpAt == nil {
				t.Fatalf("known misses = %+v, want one given-up row with reason %s", rows, tc.wantReason)
			}

			statuses, err := svc.Status(ctx)
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if len(statuses) != 1 {
				t.Fatalf("statuses = %+v, want 1", statuses)
			}
			stuck := statuses[0].StuckHrefs
			if len(stuck) != 1 || stuck[0].Href != knownMissHref || stuck[0].UID != "stuck" {
				t.Fatalf("StuckHrefs = %+v, want one entry for %s", stuck, knownMissHref)
			}
			if stuck[0].Reason != tc.wantReason {
				t.Fatalf("StuckHrefs[0].Reason = %q, want %s", stuck[0].Reason, tc.wantReason)
			}
		})
	}
}

// TestEngineKnownMissCounterResetsAfterGoodFetch verifies that a good fetch
// clears the miss count. A later bad body then gets a full new budget.
func TestEngineKnownMissCounterResetsAfterGoodFetch(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)

	good := false
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML("https://example.com/sync/t1", knownMissHref) },
		func(int, string) string {
			body := "this is not an ical body"
			if good {
				body = testEventICS("stuck", "fixed")
			}
			return multigetXML(map[string]string{knownMissHref: body}, nil)
		},
	)
	pullOnce := func(label string) *pullResult {
		t.Helper()
		result, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt)
		if err != nil {
			t.Fatalf("pull %s: %v", label, err)
		}
		return result
	}

	for run := 1; run < knownMissLimit; run++ {
		pullOnce("bad")
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 1 || rows[0].MissCount != knownMissLimit-1 {
		t.Fatalf("known misses = %+v, want one row with miss_count %d", rows, knownMissLimit-1)
	}

	good = true
	if result := pullOnce("good"); result.pulled != 1 {
		t.Fatalf("good pull: pulled = %d, want 1", result.pulled)
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("known misses after a good fetch = %+v, want none", rows)
	}

	// The stored etag now matches the listed etag. Change the stored etag
	// so the next pull fetches the href again.
	if _, err := db.ExecContext(ctx, `UPDATE sync_resources SET etag = 'old' WHERE uid = 'stuck'`); err != nil {
		t.Fatalf("reset etag: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE calendars SET sync_token = NULL WHERE id = ?`, calendarID); err != nil {
		t.Fatalf("clear token: %v", err)
	}

	good = false
	for run := 1; run < knownMissLimit; run++ {
		result := pullOnce("bad again")
		if len(result.errors) == 0 {
			t.Fatalf("bad run %d after reset: no incomplete-pull error, want a fresh budget", run)
		}
		if tok := calendarToken(t, q, calendarID); tok != "" {
			t.Fatalf("bad run %d after reset: sync_token = %q, want it withheld", run, tok)
		}
	}
}

func listKnownMisses(t *testing.T, q *storage.Queries, calendarID int64) []storage.SyncKnownMiss {
	t.Helper()
	rows, err := q.ListSyncKnownMissesByCalendar(context.Background(), calendarID)
	if err != nil {
		t.Fatalf("ListSyncKnownMissesByCalendar: %v", err)
	}
	return rows
}

// TestEngineKnownMissChangedAfterGiveUpGetsOneAttempt gives up on an
// href, and then the server lists it again with a new ETag. A broken object
// can get a new ETag on each request, so the new version gets one attempt,
// not a new budget. When that attempt fails, the row stays given up: the
// sync-token advances and no new warning repeats. A version that fetches
// clears the row.
func TestEngineKnownMissChangedAfterGiveUpGetsOneAttempt(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	advance := fakeEngineClock(engine)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)

	const firstToken = "https://example.com/sync/t1"
	token, etag, good := firstToken, "etag", false
	client := newScriptedClient(t,
		func(string) string {
			return strings.ReplaceAll(syncCollectionXML(token, knownMissHref), "&quot;etag&quot;", "&quot;"+etag+"&quot;")
		},
		func(int, string) string {
			if good {
				return multigetXML(map[string]string{knownMissHref: testEventICS("stuck", "fixed")}, nil)
			}
			return multigetXML(map[string]string{knownMissHref: "this is not an ical body"}, nil)
		},
	)
	pullOnce := func(label string) *pullResult {
		t.Helper()
		result, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt)
		if err != nil {
			t.Fatalf("pull %s: %v", label, err)
		}
		return result
	}

	for run := 1; run <= knownMissLimit; run++ {
		if run == knownMissLimit {
			advance(knownMissMinSpan)
		}
		pullOnce("give up")
	}
	if tok := calendarToken(t, q, calendarID); tok != firstToken {
		t.Fatalf("sync_token after the give-up = %q, want %q", tok, firstToken)
	}

	// The broken object churns its ETag.
	token, etag = "https://example.com/sync/t2", "etag2"
	result := pullOnce("churn")
	if len(result.errors) != 0 {
		t.Fatalf("churn after the give-up gave errors %v, want none", result.errors)
	}
	if len(result.warnings) != 0 {
		t.Fatalf("churn after the give-up gave warnings %v, want none", result.warnings)
	}
	if tok := calendarToken(t, q, calendarID); tok != token {
		t.Fatalf("sync_token after the churn = %q, want it advanced to %q", tok, token)
	}
	rows := listKnownMisses(t, q, calendarID)
	if len(rows) != 1 || rows[0].GaveUpAt == nil || rows[0].Etag != "etag2" {
		t.Fatalf("known misses = %+v, want one given-up row with the new ETag", rows)
	}

	// A real new version fetches fine.
	token, etag, good = "https://example.com/sync/t3", "etag3", true
	if result := pullOnce("good"); result.pulled != 1 {
		t.Fatalf("good pull: pulled = %d, want 1", result.pulled)
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("known misses after a good fetch = %+v, want none", rows)
	}
}

// fakeEngineClock gives engine a fake clock. The returned func moves the
// clock forward by d.
func fakeEngineClock(engine *Engine) (advance func(d time.Duration)) {
	now := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)
	engine.now = func() time.Time { return now }
	return func(d time.Duration) { now = now.Add(d) }
}

// TestEngineKnownMissNeedsTimeSpanToGiveUp is the regression test for a
// frequent background sync. knownMissLimit misses in a few minutes must not
// spend the budget: a transient 404 can last that long. The give-up needs
// knownMissMinSpan from the first miss too.
func TestEngineKnownMissNeedsTimeSpanToGiveUp(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	advance := fakeEngineClock(engine)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)

	const token = "https://example.com/sync/t1"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML(token, knownMissHref) },
		func(int, string) string { return multigetXML(nil, []string{knownMissHref}) },
	)
	pullOnce := func(run int) *pullResult {
		t.Helper()
		result, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt)
		if err != nil {
			t.Fatalf("pull %d: %v", run, err)
		}
		return result
	}

	for run := 1; run <= knownMissLimit+1; run++ {
		result := pullOnce(run)
		if len(result.errors) == 0 {
			t.Fatalf("run %d: no incomplete-pull error inside the time span", run)
		}
		if tok := calendarToken(t, q, calendarID); tok != "" {
			t.Fatalf("run %d: sync_token = %q, want it withheld inside the time span", run, tok)
		}
		advance(time.Minute)
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 1 || rows[0].GaveUpAt != nil {
		t.Fatalf("known misses = %+v, want one row and no give-up", rows)
	}

	advance(knownMissMinSpan)
	result := pullOnce(knownMissLimit + 2)
	if len(result.errors) != 0 {
		t.Fatalf("errors after the time span = %v, want none", result.errors)
	}
	if tok := calendarToken(t, q, calendarID); tok != token {
		t.Fatalf("sync_token after the time span = %q, want %q", tok, token)
	}
}

// TestBumpSyncKnownMissKeepsKnownETag bumps one known miss twice. The
// second miss has no listed ETag (an href that the pull fetches again
// without a change-list entry). The row must keep the ETag of the first
// miss. An empty ETag tells nothing, and resetIfChanged needs the old one.
func TestBumpSyncKnownMissKeepsKnownETag(t *testing.T) {
	t.Parallel()
	_, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)

	bump := func(etag string) storage.SyncKnownMiss {
		t.Helper()
		row, err := q.BumpSyncKnownMiss(ctx, storage.BumpSyncKnownMissParams{
			CalendarID: calendarID, Href: knownMissHref, Uid: "stuck", Reason: knownMissReasonMissing,
			Etag: etag, FirstMissAt: "2026-01-01T00:00:00Z",
		})
		if err != nil {
			t.Fatalf("BumpSyncKnownMiss(%q): %v", etag, err)
		}
		return row
	}
	bump(`"e1"`)
	if row := bump(""); row.Etag != `"e1"` {
		t.Fatalf("etag after a miss with no ETag = %q, want %q", row.Etag, `"e1"`)
	}
	if row := bump(`"e2"`); row.Etag != `"e2"` {
		t.Fatalf("etag after a miss with a new ETag = %q, want %q", row.Etag, `"e2"`)
	}
}

// TestKnownMissETagChurnInsideBudgetSpendsIt lists a known-miss href with
// a new ETag while its row is inside the retry budget. A server can give a
// broken object a new ETag on each request. A reset on each new ETag would
// never spend the budget, so the row must keep its count and its ETag. A
// given-up row stays given up and records the new ETag (one attempt per
// change, see recordNewVersion).
func TestKnownMissETagChurnInsideBudgetSpendsIt(t *testing.T) {
	t.Parallel()
	_, _, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := firstCalendarID(t, q)
	const gaveUpHref = "/calendar/gave-up.ics"
	for _, href := range []string{knownMissHref, gaveUpHref} {
		if _, err := q.BumpSyncKnownMiss(ctx, storage.BumpSyncKnownMissParams{
			CalendarID: calendarID, Href: href, Uid: "stuck", Reason: knownMissReasonMissing,
			Etag: `"v1"`, FirstMissAt: "2026-01-01T00:00:00Z",
		}); err != nil {
			t.Fatalf("BumpSyncKnownMiss %s: %v", href, err)
		}
	}
	gaveUpAt := "2026-01-02T00:00:00Z"
	if err := q.MarkSyncKnownMissGaveUp(ctx, storage.MarkSyncKnownMissGaveUpParams{
		GaveUpAt: &gaveUpAt, CalendarID: calendarID, Href: gaveUpHref,
	}); err != nil {
		t.Fatalf("MarkSyncKnownMissGaveUp: %v", err)
	}
	local := map[string]storage.SyncResource{knownMissHref: {}, gaveUpHref: {}}
	k, err := loadKnownMisses(ctx, q, slog.New(slog.DiscardHandler), calendarID, local, time.Now())
	if err != nil {
		t.Fatalf("loadKnownMisses: %v", err)
	}

	k.recordNewVersion(ctx, knownMissHref, `"v2"`)
	k.recordNewVersion(ctx, gaveUpHref, `"v2"`)
	rows, err := q.ListSyncKnownMissesByCalendar(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListSyncKnownMissesByCalendar: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows after a new ETag = %+v, want both kept", rows)
	}
	for _, r := range rows {
		switch r.Href {
		case knownMissHref:
			if r.Etag != `"v1"` || r.GaveUpAt != nil {
				t.Fatalf("in-budget row = %+v, want its count and ETag kept", r)
			}
		case gaveUpHref:
			if r.Etag != `"v2"` || r.GaveUpAt == nil {
				t.Fatalf("given-up row = %+v, want it given up with the new ETag", r)
			}
		}
	}
}

// TestEngineKnownMissForgottenWhenListedETagMatchesLocal lists a known-miss
// href with the ETag that the local copy already holds, for example after
// a push wrote that version. The miss row holds the same ETag, so the
// reset rule keeps it. The pull skips the fetch, because the local copy is
// the current version. The miss is then over, so its row must go, as on
// the full-snapshot path.
func TestEngineKnownMissForgottenWhenListedETagMatchesLocal(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML("https://example.com/sync/t1", knownMissHref) },
		func(int, string) string {
			t.Error("the pull fetched an href whose ETag matches the local copy")
			return multigetXML(nil, nil)
		},
	)
	// Learn the ETag that the change list gives, and store it on the local
	// copy.
	sr, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: "stuck"})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if _, err := db.ExecContext(ctx, `UPDATE sync_resources SET etag = 'etag' WHERE id = ?`, sr.ID); err != nil {
		t.Fatalf("set local etag: %v", err)
	}
	if _, err := q.BumpSyncKnownMiss(ctx, storage.BumpSyncKnownMissParams{
		CalendarID: calendarID, Href: knownMissHref, Uid: "stuck", Reason: knownMissReasonMissing,
		Etag: "etag", FirstMissAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("BumpSyncKnownMiss: %v", err)
	}

	if _, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 0 {
		t.Fatalf("known misses = %+v, want none", rows)
	}
}

// TestEngineKnownMissKeptWhenFetchedBodyHasNoUID fetches a known-miss href
// whose body parses but does not import (a VEVENT with no UID). The body
// did not land, so the known miss must stay, as on the full-snapshot path.
func TestEngineKnownMissKeptWhenFetchedBodyHasNoUID(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)
	if _, err := q.BumpSyncKnownMiss(ctx, storage.BumpSyncKnownMissParams{
		CalendarID: calendarID, Href: knownMissHref, Uid: "stuck", Reason: knownMissReasonMissing,
		Etag: "etag", FirstMissAt: "2026-01-01T00:00:00Z",
	}); err != nil {
		t.Fatalf("BumpSyncKnownMiss: %v", err)
	}
	noUID := "BEGIN:VCALENDAR\nVERSION:2.0\nPRODID:-//chroncal//tests//EN\nBEGIN:VEVENT\nDTSTAMP:20260403T120000Z\nDTSTART:20260403T120000Z\nDTEND:20260403T130000Z\nSUMMARY:No UID\nEND:VEVENT\nEND:VCALENDAR\n"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML("https://example.com/sync/t1", knownMissHref) },
		func(int, string) string { return multigetXML(map[string]string{knownMissHref: noUID}, nil) },
	)
	if _, err := engine.pull(ctx, client, calendarID, "/calendar/", ConflictPrompt); err != nil {
		t.Fatalf("pull: %v", err)
	}
	if rows := listKnownMisses(t, q, calendarID); len(rows) != 1 {
		t.Fatalf("known misses after a body with no UID = %+v, want the row kept", rows)
	}
}
