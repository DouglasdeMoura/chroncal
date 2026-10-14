package sync

import (
	"context"
	"database/sql"
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

// TestEngineKnownUnparseableHrefStopsBlockingAfterBudget is the regression
// test for issue #816. The server keeps an unparseable body for an href
// that has a local row. Before the fix, each sync withheld the sync-token
// and the calendar stayed failing forever. After the retry budget, the
// token must advance, the calendar must be healthy, the local row must
// stay, and sync status must name the href.
func TestEngineKnownUnparseableHrefStopsBlockingAfterBudget(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)
	svc := &Service{engine: engine, db: db, q: q}

	const token = "https://example.com/sync/t1"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML(token, knownMissHref) },
		func(int, string) string {
			return multigetXML(map[string]string{knownMissHref: "this is not an ical body"}, nil)
		},
	)

	pullOnce := func(run int) *pullResult {
		t.Helper()
		result, err := engine.pull(ctx, client, calendarID, "/calendar/")
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
		pullOnce(run)
		if tok := calendarToken(t, q, calendarID); tok != "" {
			t.Fatalf("run %d: sync_token = %q, want it withheld inside the budget", run, tok)
		}
		if lastError() == "" {
			t.Fatalf("run %d: LastSyncError empty inside the budget", run)
		}
	}

	result := pullOnce(knownMissLimit)
	if !hasWarningWith(result.warnings, knownMissHref, "gave up") {
		t.Fatalf("run %d: warnings = %v, want a gave-up warning that names %s", knownMissLimit, result.warnings, knownMissHref)
	}

	// The server keeps the bad body. Later runs must stay healthy.
	for run := knownMissLimit + 1; run <= knownMissLimit+2; run++ {
		pullOnce(run)
		if tok := calendarToken(t, q, calendarID); tok != token {
			t.Fatalf("run %d: sync_token = %q, want %q after the budget", run, tok, token)
		}
		if got := lastError(); got != "" {
			t.Fatalf("run %d: LastSyncError = %q, want empty after the budget", run, got)
		}
	}

	if _, err := q.GetEventByUID(ctx, "stuck"); err != nil {
		t.Fatalf("local row was deleted: %v", err)
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
	if !strings.Contains(stuck[0].Reason, "unparseable") {
		t.Fatalf("StuckHrefs[0].Reason = %q, want unparseable", stuck[0].Reason)
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
		result, err := engine.pull(ctx, client, calendarID, "/calendar/")
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

// TestEngineKnown404HrefStopsBlockingAfterBudget covers the 404 form of
// issue #816. The multiget returns 404 for a known href on each run.
func TestEngineKnown404HrefStopsBlockingAfterBudget(t *testing.T) {
	t.Parallel()

	engine, db, q := newTestEngine(t)
	ctx := context.Background()
	calendarID := seedKnownMissCalendar(t, db, q)

	const token = "https://example.com/sync/t1"
	client := newScriptedClient(t,
		func(string) string { return syncCollectionXML(token, knownMissHref) },
		func(int, string) string { return multigetXML(nil, []string{knownMissHref}) },
	)

	var result *pullResult
	for run := 1; run <= knownMissLimit; run++ {
		var err error
		result, err = engine.pull(ctx, client, calendarID, "/calendar/")
		if err != nil {
			t.Fatalf("pull %d: %v", run, err)
		}
		if run < knownMissLimit && len(result.errors) == 0 {
			t.Fatalf("run %d: no incomplete-pull error inside the budget", run)
		}
	}
	if len(result.errors) != 0 {
		t.Fatalf("run %d: errors = %v, want none after the budget", knownMissLimit, result.errors)
	}
	if !hasWarningWith(result.warnings, knownMissHref, "gave up") {
		t.Fatalf("warnings = %v, want a gave-up warning that names %s", result.warnings, knownMissHref)
	}
	if tok := calendarToken(t, q, calendarID); tok != token {
		t.Fatalf("sync_token = %q, want %q", tok, token)
	}
	if _, err := q.GetEventByUID(ctx, "stuck"); err != nil {
		t.Fatalf("local row was deleted: %v", err)
	}
	rows := listKnownMisses(t, q, calendarID)
	if len(rows) != 1 || rows[0].Reason != knownMissReasonMissing || rows[0].GaveUpAt == nil {
		t.Fatalf("known misses = %+v, want one given-up row with reason missing", rows)
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
