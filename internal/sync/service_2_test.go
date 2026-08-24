package sync

import (
	"context"
	"database/sql"
	"errors"

	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

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
	if _, err := q.BumpSyncPendingHref(ctx, storage.BumpSyncPendingHrefParams{
		CalendarID: calID,
		Href:       "/calendar/phantom-invite.ics",
	}); err != nil {
		t.Fatalf("BumpSyncPendingHref: %v", err)
	}

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
	pending, _ := q.ListSyncPendingHrefsByCalendar(ctx, calID)
	if len(pending) != 0 {
		t.Errorf("expected 0 pending hrefs, got %d", len(pending))
	}
	conflicts, _ := q.ListSyncConflictsByCalendar(ctx, calID)
	if len(conflicts) != 0 {
		t.Errorf("expected 0 conflicts, got %d", len(conflicts))
	}
}

func TestServiceResolveConflictWaitsForCalendarLifecycle(t *testing.T) {
	svc, _, q := newTestServiceWithDB(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	// The local pick imports LocalIcal, so seed an importable body.
	localIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:locked-conflict\r\n" +
		"DTSTAMP:20260401T120000Z\r\n" +
		"DTSTART:20260403T120000Z\r\nDTEND:20260403T130000Z\r\n" +
		"SUMMARY:Locked\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: calendarID, OwnerType: "event", OwnerID: 1, Uid: "locked-conflict",
		LocalIcal: localIcal, ServerIcal: "server", ServerEtag: `"etag"`,
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}
	conflicts, err := q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil || len(conflicts) != 1 {
		t.Fatalf("ListSyncConflictsByCalendar = (%+v, %v)", conflicts, err)
	}
	release, err := svc.engine.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		t.Fatalf("lock calendar lifecycle: %v", err)
	}
	done := make(chan error, 1)
	go func() {
		_, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local")
		done <- err
	}()
	select {
	case err := <-done:
		release()
		t.Fatalf("ResolveConflict completed while lifecycle lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResolveConflict after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResolveConflict did not resume after lifecycle release")
	}
}

func TestServiceResetCalendarWaitsForLifecycle(t *testing.T) {
	svc, _, q := newTestServiceWithDB(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "reset-resource", OwnerType: "event",
		RemoteUrl: "/reset.ics", Etag: `"etag"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	release, err := svc.engine.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		t.Fatalf("lock calendar lifecycle: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- svc.ResetCalendar(ctx, calendarID) }()
	select {
	case err := <-done:
		release()
		t.Fatalf("ResetCalendar completed while lifecycle lock was held: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("ResetCalendar after release: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("ResetCalendar did not resume after lifecycle release")
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID, Uid: "reset-resource",
	}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("sync resource survived reset: %v", err)
	}
}

func TestServiceResetCalendarRollsBackPartialCleanup(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: calendarID, Uid: "rollback-resource", OwnerType: "event",
		RemoteUrl: "/rollback.ics", Etag: `"etag"`, SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := db.ExecContext(ctx, `
		CREATE TRIGGER fail_calendar_reset
		BEFORE UPDATE ON calendars
		WHEN NEW.id = 1
		BEGIN
		    SELECT RAISE(ABORT, 'forced reset failure');
		END`); err != nil {
		t.Fatalf("create failure trigger: %v", err)
	}
	if err := svc.ResetCalendar(ctx, calendarID); err == nil {
		t.Fatal("ResetCalendar succeeded despite forced calendar update failure")
	}
	if _, err := q.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID, Uid: "rollback-resource",
	}); err != nil {
		t.Fatalf("sync resource deletion was not rolled back: %v", err)
	}
}

// TestService_ResolveConflict_ServerReturnsImportWarnings: the accept-server
// path of a manual resolve runs the same importICal as the auto server-wins
// paths. A server body the importer could not represent faithfully (here a
// malformed DTEND replaced by a fabricated span) produces import warnings.
// ResolveConflict must hand them back to the caller. The CLI builds this
// service with a nil (silent) logger. The return value is then the only place
// the warning can surface before the fabricated value is pushed back over the
// server's correct one.
func TestService_ResolveConflict_ServerReturnsImportWarnings(t *testing.T) {
	svc, q := newTestService(t)
	ctx := context.Background()

	cals, _ := q.ListCalendars(ctx)
	calID := cals[0].ID

	const uid = "resolve-warned-uid"
	serverIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\n" +
		"PRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\n" +
		"UID:" + uid + "\r\n" +
		"DTSTAMP:20260403T120000Z\r\n" +
		"DTSTART:20260403T120000Z\r\n" +
		"DTEND:garbage\r\n" +
		"SUMMARY:Server event with unparseable DTEND\r\n" +
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

	conflicts, _ := q.ListSyncConflicts(ctx)
	warnings, err := svc.ResolveConflict(ctx, conflicts[0].ID, "server")
	if err != nil {
		t.Fatalf("ResolveConflict server: %v", err)
	}
	if len(warnings) != 1 {
		t.Fatalf("ResolveConflict warnings = %d, want 1 (the malformed DTEND)", len(warnings))
	}
	if !strings.Contains(warnings[0].Message, "DTEND") {
		t.Errorf("warning message = %q, want it to name the malformed DTEND", warnings[0].Message)
	}
	if warnings[0].UID != uid {
		t.Errorf("warning uid = %q, want %q (single-component server body)", warnings[0].UID, uid)
	}
}

// A local pick imports the recorded local body. A body the importer can take
// as-is must report no warnings.
func TestService_ResolveConflict_LocalReturnsNoImportWarnings(t *testing.T) {
	svc, db, q := newTestServiceWithDB(t)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)

	cals, _ := q.ListCalendars(ctx)
	localIcal := "BEGIN:VCALENDAR\r\n" +
		"VERSION:2.0\r\nPRODID:-//chroncal//test//EN\r\n" +
		"BEGIN:VEVENT\r\nUID:resolve-local-uid\r\n" +
		"DTSTAMP:20260401T120000Z\r\n" +
		"DTSTART:20260403T120000Z\r\nDTEND:20260403T130000Z\r\n" +
		"SUMMARY:Local\r\nEND:VEVENT\r\nEND:VCALENDAR\r\n"
	if err := q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
		CalendarID: cals[0].ID,
		OwnerType:  "event",
		OwnerID:    1,
		Uid:        "resolve-local-uid",
		LocalIcal:  localIcal,
		ServerIcal: "server",
		ServerEtag: "etag-1",
	}); err != nil {
		t.Fatalf("CreateSyncConflict: %v", err)
	}

	conflicts, _ := q.ListSyncConflicts(ctx)
	warnings, err := svc.ResolveConflict(ctx, conflicts[0].ID, "local")
	if err != nil {
		t.Fatalf("ResolveConflict local: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("ResolveConflict local warnings = %d, want 0", len(warnings))
	}
}
