package event

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// insertGuardedCalendar inserts a calendar advertising the given remote
// access/component metadata and returns its id. The default test calendar
// (id 1) keeps empty metadata so it stays writable; these dedicated rows carry
// the read-only / component restrictions under test.
func insertGuardedCalendar(t *testing.T, db *sql.DB, name, access, components string) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		`INSERT INTO calendars (name, remote_access, remote_components) VALUES (?, ?, ?)`,
		name, access, components,
	)
	if err != nil {
		t.Fatalf("insert calendar %q: %v", name, err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}
	return id
}

func countEvents(t *testing.T, db *sql.DB) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("count events: %v", err)
	}
	return n
}

func countOverrides(t *testing.T, db *sql.DB, uid string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(
		context.Background(),
		`SELECT COUNT(*) FROM events WHERE uid = ? AND recurrence_id <> '' AND deleted_at IS NULL`,
		uid,
	).Scan(&n); err != nil {
		t.Fatalf("count overrides: %v", err)
	}
	return n
}

// TestEventAccessGuard_CreateRejected proves Create on a read-only or
// VEVENT-less collection is rejected with the wrapped policy error and persists
// no event row. UpsertByUID (the sync/import path) stays unguarded, so masters
// for the recurrence tests below are seeded through it.
func TestEventAccessGuard_CreateRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")
	tasksOnly := insertGuardedCalendar(t, db, "Tasks-Only Cal", "owner", "VTODO")

	base := CreateParams{
		Title:     "Guarded",
		StartTime: time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}

	t.Run("read_only", func(t *testing.T) {
		before := countEvents(t, db)
		p := base
		p.CalendarID = readOnly
		_, err := svc.Create(ctx, p)
		if !errors.Is(err, calendaraccess.ErrReadOnly) {
			t.Fatalf("Create on read-only calendar: error = %v, want ErrReadOnly", err)
		}
		if got := countEvents(t, db); got != before {
			t.Fatalf("event rows after rejected Create = %d, want %d (no persisted mutation)", got, before)
		}
	})

	t.Run("unsupported_component", func(t *testing.T) {
		before := countEvents(t, db)
		p := base
		p.CalendarID = tasksOnly
		_, err := svc.Create(ctx, p)
		if !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
			t.Fatalf("Create on VTODO-only calendar: error = %v, want ErrUnsupportedComponent", err)
		}
		if got := countEvents(t, db); got != before {
			t.Fatalf("event rows after rejected Create = %d, want %d (no persisted mutation)", got, before)
		}
	})
}

// TestEventAccessGuard_UpdateRejected proves an Update on a read-only calendar
// fails with ErrReadOnly and leaves the row unchanged, including the
// destination-calendar guard for a cross-calendar move.
func TestEventAccessGuard_UpdateRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")

	// Seed via the unguarded sync/import path.
	seeded, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "ro-update-uid",
		CalendarID: readOnly,
		Title:      "Original",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}

	if _, err := svc.Update(ctx, seeded.ID, UpdateParams{
		Title:      "Changed",
		CalendarID: readOnly,
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	}); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Update on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("re-get event: %v", err)
	}
	if got.Title != "Original" {
		t.Errorf("title after rejected Update = %q, want %q (no persisted mutation)", got.Title, "Original")
	}

	// Moving a writable event into the read-only calendar must trip the
	// destination guard before the row is rewritten.
	movable, err := svc.Create(ctx, CreateParams{
		CalendarID: 1,
		Title:      "Movable",
		StartTime:  time.Date(2026, 4, 2, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 2, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("create movable event: %v", err)
	}
	if _, err := svc.Update(ctx, movable.ID, UpdateParams{
		Title:      "Moved",
		CalendarID: readOnly,
		StartTime:  movable.StartTime,
		EndTime:    movable.EndTime,
	}); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Update moving into read-only calendar: error = %v, want ErrReadOnly", err)
	}
	moved, err := svc.Get(ctx, movable.ID)
	if err != nil {
		t.Fatalf("re-get movable event: %v", err)
	}
	if moved.CalendarID != 1 {
		t.Errorf("calendar after rejected move = %d, want 1 (no persisted mutation)", moved.CalendarID)
	}
	if moved.Title != "Movable" {
		t.Errorf("title after rejected move = %q, want %q", moved.Title, "Movable")
	}
}

// TestEventAccessGuard_DeleteRejected proves Delete leaves the row live on a
// read-only calendar.
func TestEventAccessGuard_DeleteRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")

	seeded, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "ro-delete-uid",
		CalendarID: readOnly,
		Title:      "Keep",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}

	if err := svc.Delete(ctx, seeded.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Delete on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.Get(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("re-get event: %v", err)
	}
	if got.DeletedAt != nil {
		t.Errorf("event soft-deleted after rejected Delete at %v (no persisted mutation)", got.DeletedAt)
	}
}

// TestEventAccessGuard_RecurrenceRejected covers the UID-keyed series mutation
// paths (DeleteInstance, UpdateInstance, DeleteSeries) on a read-only calendar,
// asserting the master is untouched and no override row is created.
func TestEventAccessGuard_RecurrenceRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "ro-recur-uid",
		CalendarID:     readOnly,
		Title:          "Weekly",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("seed master: %v", err)
	}
	firstOccurrence := time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC)

	if err := svc.DeleteInstance(ctx, master.UID, firstOccurrence); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteInstance on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	if after, _ := svc.GetByUID(ctx, master.UID); after.ExDates != "" {
		t.Errorf("master EXDATE = %q after rejected DeleteInstance, want empty (no persisted mutation)", after.ExDates)
	}
	if n := countOverrides(t, db, master.UID); n != 0 {
		t.Fatalf("override rows after rejected DeleteInstance = %d, want 0", n)
	}

	if _, err := svc.UpdateInstance(ctx, master.UID, firstOccurrence, UpdateParams{
		Title:      "Moved Instance",
		CalendarID: readOnly,
		StartTime:  time.Date(2026, 4, 1, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 15, 0, 0, 0, time.UTC),
	}); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("UpdateInstance on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	if n := countOverrides(t, db, master.UID); n != 0 {
		t.Fatalf("override rows after rejected UpdateInstance = %d, want 0 (no persisted mutation)", n)
	}

	if err := svc.DeleteSeries(ctx, master.UID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteSeries on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("re-get master after DeleteSeries: %v", err)
	}
	if got.DeletedAt != nil {
		t.Errorf("master soft-deleted after rejected DeleteSeries at %v (no persisted mutation)", got.DeletedAt)
	}
}

// TestEventAccessGuard_RestoreRejected proves the restore path (un-hide +
// re-push) is blocked on a read-only calendar and the row stays deleted. The
// row is soft-deleted via SQL because the guarded Delete would itself reject.
func TestEventAccessGuard_RestoreRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")

	seeded, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "ro-restore-uid",
		CalendarID: readOnly,
		Title:      "Deleted",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if _, err := db.ExecContext(ctx,
		`UPDATE events SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), seeded.ID,
	); err != nil {
		t.Fatalf("soft-delete event: %v", err)
	}

	if err := svc.RestoreByID(ctx, seeded.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByID on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.GetIncludingDeleted(ctx, seeded.ID)
	if err != nil {
		t.Fatalf("re-get deleted event: %v", err)
	}
	if got.DeletedAt == nil {
		t.Fatal("event restored after rejected RestoreByID (no persisted mutation)")
	}
}

func countAttendees(t *testing.T, db *sql.DB, eventID int64) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM event_attendees WHERE event_id = ?`, eventID).Scan(&n); err != nil {
		t.Fatalf("count attendees: %v", err)
	}
	return n
}

// seedOrphanOverride inserts an override row with no master, modeling the state
// left behind when a recurring master is purged independently while an override
// (or series tail) survives — the trash view supports these orphans.
func seedOrphanOverride(t *testing.T, svc *Service, calendarID int64, uid string) Event {
	t.Helper()
	override, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:          uid,
		CalendarID:   calendarID,
		Title:        "Orphan Override",
		StartTime:    time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 8, 11, 0, 0, 0, time.UTC),
		RecurrenceID: "2026-04-08T10:00:00Z",
	})
	if err != nil {
		t.Fatalf("seed orphan override: %v", err)
	}
	return override
}

// TestEventAccessGuard_DeleteSeriesOrphanTailRejected proves DeleteSeries is
// blocked on a read-only calendar even when only an orphaned override (master
// purged) remains, so the orphan is not silently soft-deleted.
func TestEventAccessGuard_DeleteSeriesOrphanTailRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")
	override := seedOrphanOverride(t, svc, readOnly, "orphan-delete-series-uid")

	if err := svc.DeleteSeries(ctx, override.UID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteSeries orphan tail: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.Get(ctx, override.ID)
	if err != nil {
		t.Fatalf("re-get orphan override: %v", err)
	}
	if got.DeletedAt != nil {
		t.Error("orphan override soft-deleted after rejected DeleteSeries (no persisted mutation)")
	}
}

// TestEventAccessGuard_RestoreByUIDOrphanTailRejected proves RestoreByUID is
// blocked on a read-only calendar even when only an orphaned, soft-deleted
// override remains, so the orphan is not silently resurrected.
func TestEventAccessGuard_RestoreByUIDOrphanTailRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")
	override := seedOrphanOverride(t, svc, readOnly, "orphan-restore-uid-uid")
	if _, err := db.ExecContext(ctx,
		`UPDATE events SET deleted_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), override.ID,
	); err != nil {
		t.Fatalf("soft-delete orphan override: %v", err)
	}

	if err := svc.RestoreByUID(ctx, override.UID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByUID orphan tail: error = %v, want ErrReadOnly", err)
	}
	got, err := svc.GetIncludingDeleted(ctx, override.ID)
	if err != nil {
		t.Fatalf("re-get orphan override: %v", err)
	}
	if got.DeletedAt == nil {
		t.Error("orphan override restored after rejected RestoreByUID (no persisted mutation)")
	}
}

// TestEventAccessGuard_ReplaceAttendeesRSVPRejected proves the TUI RSVP path
// (ReplaceAttendees called without a preceding Update) is blocked on a
// read-only calendar and persists no attendee row, while the sync-only entry
// point still mirrors server-originated attendees through.
func TestEventAccessGuard_ReplaceAttendeesRSVPRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	readOnly := insertGuardedCalendar(t, db, "Read-Only Cal", "read", "VEVENT")
	seeded, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:        "rsvp-uid",
		CalendarID: readOnly,
		Title:      "RSVP Target",
		StartTime:  time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("seed event: %v", err)
	}
	attendee := model.Attendee{Email: "alice@example.com", RSVPStatus: "ACCEPTED", Role: "REQ-PARTICIPANT"}

	before := countAttendees(t, db, seeded.ID)
	if err := svc.ReplaceAttendees(ctx, seeded.ID, []model.Attendee{attendee}); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("ReplaceAttendees on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	if got := countAttendees(t, db, seeded.ID); got != before {
		t.Fatalf("attendee rows after rejected ReplaceAttendees = %d, want %d (no persisted mutation)", got, before)
	}

	// The CalDAV sync engine must still mirror server-originated attendees.
	if err := svc.ReplaceAttendeesForSync(ctx, seeded.ID, []model.Attendee{attendee}); err != nil {
		t.Fatalf("ReplaceAttendeesForSync on read-only calendar: %v (sync path must stay unguarded)", err)
	}
	if got := countAttendees(t, db, seeded.ID); got != 1 {
		t.Fatalf("attendee rows after ReplaceAttendeesForSync = %d, want 1", got)
	}
}
