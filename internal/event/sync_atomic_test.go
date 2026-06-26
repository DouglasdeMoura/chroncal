package event

import (
	"context"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// makeSyncedCalendar links calendar 1 to an account so the sync-tracking
// writes (MarkResourceDirty / CreateTombstoneIfSynced) actually fire.
func makeSyncedCalendar(t *testing.T, s *Service) {
	t.Helper()
	ctx := context.Background()
	acct, err := s.q.CreateAccount(ctx, storage.CreateAccountParams{
		Name:      "test",
		ServerUrl: "https://example.com",
		AuthType:  "basic",
		Username:  "u",
	})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	remote := "https://example.com/cal/"
	if err := s.q.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		AccountID: &acct.ID,
		RemoteUrl: &remote,
		ID:        1,
	}); err != nil {
		t.Fatalf("link calendar: %v", err)
	}
}

// TestCreate_SyncMarkIsAtomic exercises issue #107: the sync-tracking write
// must participate in the mutation's transaction and its error must not be
// discarded. We force the sync_resources INSERT to fail (by dropping the
// table) and assert that Create reports the failure and rolls the event back
// rather than silently committing a row that will never be pushed.
func TestCreate_SyncMarkIsAtomic(t *testing.T) {
	svc := newTestService(t)
	makeSyncedCalendar(t, svc)
	ctx := context.Background()

	// Force the dirty-mark INSERT to fail.
	if _, err := svc.db.ExecContext(ctx, `DROP TABLE sync_resources`); err != nil {
		t.Fatalf("drop sync_resources: %v", err)
	}

	_, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Synced Event",
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	})
	if err == nil {
		t.Fatal("Create succeeded but the sync-tracking write failed; the error was discarded")
	}

	// The mutation must have rolled back: no event should be persisted.
	var n int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	if n != 0 {
		t.Fatalf("event was committed (%d rows) despite the sync-tracking write failing; "+
			"the dirty-mark is not atomic with the mutation", n)
	}
}

// TestDelete_TombstoneIsAtomic exercises the most dangerous half of issue #107:
// for a synced standalone event the tombstone is written together with the
// soft-delete. If the tombstone write fails, the soft-delete must roll back so
// the next sync cannot DELETE a still-live event from the server.
func TestDelete_TombstoneIsAtomic(t *testing.T) {
	svc := newTestService(t)
	makeSyncedCalendar(t, svc)
	ctx := context.Background()

	evt := createEvent(t, svc)

	// Mark the resource as already synced (non-empty remote_url) so Delete
	// takes the tombstone-creating path.
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   evt.CalendarID,
		Uid:          evt.UID,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/evt.ics",
		Etag:         "etag-1",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
	}

	// Force the tombstone INSERT to fail.
	if _, err := svc.db.ExecContext(ctx, `DROP TABLE tombstones`); err != nil {
		t.Fatalf("drop tombstones: %v", err)
	}

	if err := svc.Delete(ctx, evt.ID); err == nil {
		t.Fatal("Delete succeeded but the tombstone write failed; the error was discarded")
	}

	// The soft-delete must have rolled back: the event is still live.
	var deletedAt *string
	if err := svc.db.QueryRowContext(ctx,
		`SELECT deleted_at FROM events WHERE id = ?`, evt.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read event: %v", err)
	}
	if deletedAt != nil && *deletedAt != "" {
		t.Fatalf("event was soft-deleted (deleted_at=%q) despite the tombstone write failing; "+
			"the tombstone is not atomic with the soft-delete", *deletedAt)
	}
}

// TestDeleteSeries_TombstoneIsAtomic is the DeleteSeries analogue of
// TestDelete_TombstoneIsAtomic: a recurring master's series-delete must write
// its tombstone inside the soft-delete transaction so a failed tombstone write
// can't leave a tombstone for a still-live series (which the next sync would
// DELETE from the server). Regression test for the issue #107 gap that the
// original fix left in DeleteSeries.
func TestDeleteSeries_TombstoneIsAtomic(t *testing.T) {
	svc := newTestService(t)
	makeSyncedCalendar(t, svc)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "series-atomic",
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 15, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=DAILY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Mark the resource as already synced so DeleteSeries takes the
	// tombstone-creating path.
	if err := svc.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID:   master.CalendarID,
		Uid:          master.UID,
		OwnerType:    "event",
		RemoteUrl:    "https://example.com/cal/series.ics",
		Etag:         "etag-1",
		Dirty:        0,
		SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("upsert sync resource: %v", err)
	}

	if _, err := svc.db.ExecContext(ctx, `DROP TABLE tombstones`); err != nil {
		t.Fatalf("drop tombstones: %v", err)
	}

	if err := svc.DeleteSeries(ctx, master.UID); err == nil {
		t.Fatal("DeleteSeries succeeded but the tombstone write failed; the error was discarded")
	}

	// The series must still be live: the soft-delete rolled back.
	var deletedAt *string
	if err := svc.db.QueryRowContext(ctx,
		`SELECT deleted_at FROM events WHERE id = ?`, master.ID).Scan(&deletedAt); err != nil {
		t.Fatalf("read master: %v", err)
	}
	if deletedAt != nil && *deletedAt != "" {
		t.Fatalf("series was soft-deleted (deleted_at=%q) despite the tombstone write failing; "+
			"DeleteSeries tombstone is not atomic with the soft-delete", *deletedAt)
	}
}
