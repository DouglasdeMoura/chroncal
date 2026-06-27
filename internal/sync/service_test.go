package sync

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// mockCredStore implements auth.CredentialStore for testing.
type mockCredStore struct {
	creds map[int64]auth.Credential
}

func (m *mockCredStore) Get(accountID int64) (auth.Credential, error) {
	c, ok := m.creds[accountID]
	if !ok {
		return auth.Credential{}, nil
	}
	return c, nil
}

func (m *mockCredStore) Set(cred auth.Credential) error { return nil }
func (m *mockCredStore) Delete(accountID int64) error   { return nil }

func newTestService(t *testing.T) (*Service, *storage.Queries) {
	t.Helper()
	svc, _, q := newTestServiceWithDB(t)
	return svc, q
}

func newTestServiceWithDB(t *testing.T) (*Service, *sql.DB, *storage.Queries) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	credStore := &mockCredStore{creds: make(map[int64]auth.Credential)}
	calendars := calendar.NewService(db, q)
	events := event.NewService(db, q)
	todos := todo.NewService(db, q)
	journals := journal.NewService(db, q)
	svc := NewService(db, q, credStore, calendars, events, todos, journals, nil)
	return svc, db, q
}

func TestService_StatusEmpty(t *testing.T) {
	svc, _ := newTestService(t)
	statuses, err := svc.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 0 {
		t.Errorf("expected 0 statuses, got %d", len(statuses))
	}
}

func TestService_StatusIncludesSyncHealthFields(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()

	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID

	if err := q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID:        calendarID,
		AccountID: &account.ID,
		RemoteUrl: func() *string {
			s := "https://example.com/cal"
			return &s
		}(),
	}); err != nil {
		t.Fatalf("LinkCalendarToAccount: %v", err)
	}

	lastSync := "2026-04-03T08:30:00Z"
	lastAttempt := "2026-04-03T08:35:00Z"
	lastError := "partial push failure"
	if _, err := db.ExecContext(ctx,
		"UPDATE calendars SET last_sync_at = ?, last_sync_attempted_at = ?, last_sync_error = ? WHERE id = ?",
		lastSync,
		lastAttempt,
		lastError,
		calendarID,
	); err != nil {
		t.Fatalf("seed sync health: %v", err)
	}

	statuses, err := svc.Status(ctx)
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if len(statuses) != 1 {
		t.Fatalf("statuses = %d, want 1", len(statuses))
	}
	if statuses[0].LastSyncAt != lastSync {
		t.Fatalf("LastSyncAt = %q, want %q", statuses[0].LastSyncAt, lastSync)
	}

	statusValue := reflect.ValueOf(statuses[0])
	attemptedField := statusValue.FieldByName("LastSyncAttemptedAt")
	if !attemptedField.IsValid() {
		t.Fatal("SyncStatus is missing LastSyncAttemptedAt")
	}
	if got := attemptedField.String(); got != lastAttempt {
		t.Fatalf("LastSyncAttemptedAt = %q, want %q", got, lastAttempt)
	}

	errorField := statusValue.FieldByName("LastSyncError")
	if !errorField.IsValid() {
		t.Fatal("SyncStatus is missing LastSyncError")
	}
	if got := errorField.String(); got != lastError {
		t.Fatalf("LastSyncError = %q, want %q", got, lastError)
	}
}

func TestService_ListConflictsEmpty(t *testing.T) {
	svc, _ := newTestService(t)
	conflicts, err := svc.ListConflicts(context.Background())
	if err != nil {
		t.Fatalf("ListConflicts: %v", err)
	}
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestService_ResolveConflict_InvalidPick(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	// Create an account and a linked calendar so we can create a conflict
	account, err := q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "user",
	})
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	// Use the seeded calendar
	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID
	_ = account

	// Create a conflict
	err = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "test-uid",
		LocalIcal:  "BEGIN:VCALENDAR\nEND:VCALENDAR",
		ServerIcal: "BEGIN:VCALENDAR\nEND:VCALENDAR",
		ServerEtag: "etag-123",
	})
	if err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	if len(conflicts) == 0 {
		t.Fatal("expected at least 1 conflict")
	}

	// Resolve with invalid pick
	err = svc.ResolveConflict(ctx, conflicts[0].ID, "invalid")
	if err == nil {
		t.Error("expected error for invalid pick value")
	}
}

// TestService_ResolveConflict_Server is the regression test for issue #67:
// resolving a conflict to "server" must import the recorded server iCal into
// the local row (so the local view reflects the server), then clear the dirty
// flag with the server ETag. Before the fix the local row kept its divergent
// local copy while the ETag claimed it matched the server.
func TestService_ResolveConflict_Server(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	const uid = "resolve-server-uid"
	events := event.NewService(db, q)

	// Seed the local row with the divergent LOCAL version.
	if _, err := events.UpsertByUID(ctx, event.UpsertParams{
		UID:        uid,
		CalendarID: calID,
		Title:      "Local Title",
		StartTime:  time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed local event: %v", err)
	}

	err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/resolve-server-uid.ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// The server version differs from the local row: it carries "Server Title".
	serverIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTART:20260403T120000Z\r\n" +
		"DTEND:20260403T130000Z\r\n" +
		"SUMMARY:Server Title\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	err = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        uid,
		LocalIcal:  "local",
		ServerIcal: serverIcal,
		ServerEtag: "etag-456",
	})
	if err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	err = svc.ResolveConflict(ctx, conflicts[0].ID, "server")
	if err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}

	// Conflict should be deleted
	remaining, _ := q.ListSyncConflicts(ctx)
	if len(remaining) != 0 {
		t.Errorf("expected 0 conflicts after resolve, got %d", len(remaining))
	}

	// The local row must now reflect the SERVER version.
	evt, err := events.GetByUID(ctx, uid)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if evt.Title != "Server Title" {
		t.Fatalf("local Title = %q, want %q (server data not imported)", evt.Title, "Server Title")
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calID,
		Uid:        uid,
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 0 {
		t.Fatalf("Dirty = %d, want 0", res.Dirty)
	}
	if res.Etag != "etag-456" {
		t.Fatalf("Etag = %q, want %q", res.Etag, "etag-456")
	}
}

// TestService_ResolveConflict_ServerPreservesConcurrentEdit is the regression
// test for issue #466 (a reopening of #417 on the manual path): a local edit
// landing in the window between the accept-server import and the dirty clear
// must not be silently dropped. The resolveConflictAfterRevCapture hook
// simulates that edit; the rev-guarded clear must leave the resource dirty so
// the next push sends it. With the previous unconditional ClearSyncResourceDirty
// this test fails because the edit's dirty flag is wiped. Serial (no t.Parallel)
// because it mutates the package-level hook.
func TestService_ResolveConflict_ServerPreservesConcurrentEdit(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()

	// Link the calendar to an account so MarkResourceDirty (which no-ops on
	// local-only calendars) actually writes — the hook below relies on it to
	// bump rev and simulate the concurrent edit.
	testutil.LinkCalendarToAccount(t, db)

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	const uid = "resolve-server-race"
	events := event.NewService(db, q)

	if _, err := events.UpsertByUID(ctx, event.UpsertParams{
		UID:        uid,
		CalendarID: calID,
		Title:      "Local Title",
		StartTime:  time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("seed local event: %v", err)
	}

	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/resolve-server-race.ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	serverIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTART:20260403T120000Z\r\n" +
		"DTEND:20260403T130000Z\r\n" +
		"SUMMARY:Server Title\r\n" +
		"END:VEVENT\r\n" +
		"END:VCALENDAR\r\n"

	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        uid,
		LocalIcal:  "local",
		ServerIcal: serverIcal,
		ServerEtag: "etag-456",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	// Simulate a concurrent local edit landing after the import recorded the
	// server version but before the dirty flag is cleared: it bumps rev and
	// re-marks the resource dirty, exactly as a real service-layer mutation would.
	var fired int
	resolveConflictAfterRevCapture = func() {
		fired++
		if err := storage.MarkResourceDirty(ctx, db, calID, uid, "event"); err != nil {
			t.Errorf("simulate concurrent edit: %v", err)
		}
	}
	t.Cleanup(func() { resolveConflictAfterRevCapture = nil })

	conflicts, _ := q.ListSyncConflicts(ctx)
	if err := svc.ResolveConflict(ctx, conflicts[0].ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if fired != 1 {
		t.Fatalf("resolveConflictAfterRevCapture fired %d times, want 1", fired)
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calID, Uid: uid})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1 (concurrent edit must not be dropped, #466)", res.Dirty)
	}
	// The ETag still advances to the server's version so the next push's If-Match
	// matches the server, mirroring FinalizePushedResource on the push path.
	if res.Etag != "etag-456" {
		t.Fatalf("Etag = %q, want %q", res.Etag, "etag-456")
	}
}

// TestService_ResolveConflict_ServerDoesNotResurrectDeleted is the regression
// test for issue #89 (gap #2): accepting the server version of a conflict must
// NOT un-delete a row the user has locally soft-deleted and tombstoned for
// propagation. UpsertByUID clears deleted_at, so without tombstone-aware import
// the resolve resurrects the local row and contradicts the pending delete. The
// tombstone-safe pull path skips tombstoned UIDs; the resolve path must match.
func TestService_ResolveConflict_ServerDoesNotResurrectDeleted(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	const uid = "resolve-server-deleted-uid"
	events := event.NewService(db, q)

	created, err := events.UpsertByUID(ctx, event.UpsertParams{
		UID:        uid,
		CalendarID: calID,
		Title:      "Local Title",
		StartTime:  time.Date(2026, 4, 3, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 3, 11, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed local event: %v", err)
	}

	// Mark the resource synced (non-empty remote_url) so deleting it queues a
	// tombstone via CreateTombstoneIfSynced.
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/" + uid + ".ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	// User deletes the event locally: soft-deletes the row and queues a tombstone.
	if err := events.Delete(ctx, created.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := events.GetByUID(ctx, uid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("precondition: event should be soft-deleted, GetByUID err = %v", err)
	}

	// A pre-existing conflict for the same UID carries the server's version.
	serverIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:" + uid + "\r\n" +
		"DTSTART:20260403T120000Z\r\nDTEND:20260403T130000Z\r\n" +
		"SUMMARY:Server Title\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID, OwnerType: "event", OwnerID: created.ID, Uid: uid,
		LocalIcal: "local", ServerIcal: serverIcal, ServerEtag: "etag-456",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	if err := svc.ResolveConflict(ctx, conflicts[0].ID, "server"); err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}

	// The row must STAY deleted — the pending local delete wins, matching the
	// tombstone-safe pull path. Resurrection here is the bug.
	if _, err := events.GetByUID(ctx, uid); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("event was resurrected by resolve 'server'; GetByUID err = %v, want sql.ErrNoRows", err)
	}

	// The tombstone must survive so the delete still propagates to the server.
	tombstones, _ := q.ListTombstonesByCalendar(ctx, calID)
	if len(tombstones) != 1 {
		t.Fatalf("tombstones = %d, want 1 (delete intent must survive)", len(tombstones))
	}

	// The conflict must be cleared so we stop looping.
	remaining, _ := q.ListSyncConflicts(ctx)
	if len(remaining) != 0 {
		t.Fatalf("conflicts after resolve = %d, want 0", len(remaining))
	}
}

func TestService_ResolveConflict_Local(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// The stored etag is the stale value that already failed If-Match.
	err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "resolve-local-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/resolve-local-uid.ics",
		Etag:         "stale-etag",
		Dirty:        0,
		SyncStrategy: "sync-token",
	})
	if err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	err = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "resolve-local-uid",
		LocalIcal:  "local",
		ServerIcal: "server",
		ServerEtag: "etag-456",
	})
	if err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	err = svc.ResolveConflict(ctx, conflicts[0].ID, "local")
	if err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}

	// Conflict should be deleted
	remaining, _ := q.ListSyncConflicts(ctx)
	if len(remaining) != 0 {
		t.Errorf("expected 0 conflicts after resolve, got %d", len(remaining))
	}

	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calID,
		Uid:        "resolve-local-uid",
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	// Resource must be dirty so the next sync pushes the local version.
	if res.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1", res.Dirty)
	}
	// The stale stored etag must be replaced with the conflict's ServerEtag
	// (the value the server had at conflict-detection time). This breaks the
	// 412 loop while keeping the concurrency check: the next push sends
	// If-Match: <ServerEtag>, succeeding if the server is unchanged and
	// surfacing a fresh conflict if it changed again.
	if res.Etag != "etag-456" {
		t.Fatalf("Etag = %q, want %q (the conflict ServerEtag)", res.Etag, "etag-456")
	}
}

// TestService_ResolveConflict_ServerEmptyIcal guards against the silent
// no-op: a conflict whose ServerIcal carries no importable component (empty or
// component-less) must NOT clear dirty or stamp the server ETag, because doing
// so would leave the divergent local row in place while claiming it matches the
// server — the exact data-loss the "server" branch exists to prevent. The
// resolve should fail and leave the dirty flag and conflict intact for a retry.
func TestService_ResolveConflict_ServerEmptyIcal(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	const uid = "resolve-server-empty-uid"
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          uid,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/" + uid + ".ics",
		Etag:         "etag-before",
		Dirty:        1,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}

	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        uid,
		LocalIcal:  "local",
		ServerIcal: "", // server payload failed to encode at conflict-record time
		ServerEtag: "etag-456",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	if err := svc.ResolveConflict(ctx, conflicts[0].ID, "server"); err == nil {
		t.Fatal("ResolveConflict server with empty ServerIcal: expected error, got nil")
	}

	// The dirty flag and ETag must be untouched so the next sync can retry.
	res, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calID,
		Uid:        uid,
	})
	if err != nil {
		t.Fatalf("GetSyncResource: %v", err)
	}
	if res.Dirty != 1 {
		t.Fatalf("Dirty = %d, want 1 (must stay dirty)", res.Dirty)
	}
	if res.Etag != "etag-before" {
		t.Fatalf("Etag = %q, want %q (must not adopt server ETag)", res.Etag, "etag-before")
	}

	// The conflict must remain for a later retry, not be silently consumed.
	remaining, _ := q.ListSyncConflicts(ctx)
	if len(remaining) != 1 {
		t.Fatalf("conflicts after failed resolve = %d, want 1", len(remaining))
	}
}

func TestService_ResetCalendar(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	// Create some sync state
	_ = q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   calID,
		Uid:          "reset-test-uid",
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/event.ics",
		Etag:         "etag-789",
		Dirty:        1,
		SyncStrategy: "sync-token",
	})
	_ = q.CreateTombstone(ctx, storage.CreateTombstoneParams{
		CalendarID: calID,
		Uid:        "reset-tombstone",
		RemoteUrl:  "https://example.com/cal/old.ics",
	})
	_ = q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "reset-conflict",
		LocalIcal:  "local",
		ServerIcal: "server",
		ServerEtag: "etag",
	})

	// Reset
	err := svc.ResetCalendar(ctx, calID)
	if err != nil {
		t.Fatalf("ResetCalendar: %v", err)
	}

	// All sync state should be gone
	resources, _ := q.ListSyncResourcesByCalendar(ctx, calID)
	if len(resources) != 0 {
		t.Errorf("expected 0 sync resources, got %d", len(resources))
	}
	tombstones, _ := q.ListTombstonesByCalendar(ctx, calID)
	if len(tombstones) != 0 {
		t.Errorf("expected 0 tombstones, got %d", len(tombstones))
	}
	conflicts, _ := q.ListSyncConflictsByCalendar(ctx, calID)
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestParseTime(t *testing.T) {
	tests := []struct {
		input string
		want  time.Time
	}{
		{"2026-04-03T12:00:00Z", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)},
		{"2026-04-03 12:00:00", time.Date(2026, 4, 3, 12, 0, 0, 0, time.UTC)},
		{"", time.Time{}},
		{"invalid", time.Time{}},
	}
	for _, tt := range tests {
		got := parseTime(tt.input)
		if !got.Equal(tt.want) {
			t.Errorf("parseTime(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

// TestService_PushCalendarRejectsLocalOnly guards the contract that
// opportunistic save-time push will fail fast for calendars without a
// linked account, so CLI/TUI callers can safely treat it as a no-op.
func TestService_PushCalendarRejectsLocalOnly(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	if len(cals) == 0 {
		t.Fatal("expected at least one seeded calendar")
	}

	_, err = svc.PushCalendar(ctx, cals[0].ID, ConflictServerWins)
	if err == nil {
		t.Fatal("PushCalendar on local-only calendar: expected error, got nil")
	}
}
