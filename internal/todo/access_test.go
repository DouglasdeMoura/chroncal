package todo

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// setTodoCalendarAccess mutates calendar 1's remote capability metadata so the
// EnsureWritable guard observes a read-only or component-restricted collection.
func setTodoCalendarAccess(t *testing.T, db *sql.DB, access, components string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		"UPDATE calendars SET remote_access = ?, remote_components = ? WHERE id = 1",
		access, components,
	); err != nil {
		t.Fatalf("set calendar access: %v", err)
	}
}

// addTodoCalendar inserts an extra calendar with the given capability metadata
// and returns its id. Used to model a move destination with different access.
func addTodoCalendar(t *testing.T, db *sql.DB, access, components string) int64 {
	t.Helper()
	res, err := db.ExecContext(context.Background(),
		"INSERT INTO calendars (name, remote_access, remote_components) VALUES (?, ?, ?)",
		"Extra", access, components,
	)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}
	return id
}

// A read-only collection rejects new todo creation and persists nothing.
func TestTodoCreate_ReadOnlyRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	setTodoCalendarAccess(t, db, "read", "")

	_, err := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "blocked"})
	if !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Create on read-only calendar: error = %v, want ErrReadOnly", err)
	}
	todos, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("expected no persisted todos, got %d", len(todos))
	}
}

// A VEVENT-only collection rejects VTODO creation (component not advertised).
func TestTodoCreate_UnsupportedComponentRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	setTodoCalendarAccess(t, db, "owner", "VEVENT")

	_, err := svc.Create(ctx, CreateParams{CalendarID: 1, Summary: "blocked"})
	if !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("Create on VEVENT-only calendar: error = %v, want ErrUnsupportedComponent", err)
	}
	todos, err := svc.List(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(todos) != 0 {
		t.Fatalf("expected no persisted todos, got %d", len(todos))
	}
}

// Every user-facing mutation rejects on a read-only collection and leaves the
// row untouched: Update, Complete, Delete, and DeleteSeries.
func TestTodoMutations_ReadOnlyRejectedNoMutation(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	// Seed while the calendar is writable.
	td := createTodo(t, svc)
	series := createTodo(t, svc) // standalone UID drives DeleteSeries

	setTodoCalendarAccess(t, db, "read", "")

	orig, err := svc.Get(ctx, td.ID)
	if err != nil {
		t.Fatalf("get seed: %v", err)
	}

	// Update — in place, no move.
	if _, err := svc.Update(ctx, td.ID, UpdateParams{
		Summary:    "changed",
		CalendarID: td.CalendarID,
	}); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Update: error = %v, want ErrReadOnly", err)
	}
	if got, _ := svc.Get(ctx, td.ID); got.Summary != orig.Summary {
		t.Errorf("Update mutated summary: got %q, want %q", got.Summary, orig.Summary)
	}

	// Complete.
	if _, err := svc.Complete(ctx, td.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Complete: error = %v, want ErrReadOnly", err)
	}
	if got, _ := svc.Get(ctx, td.ID); got.IsCompleted() {
		t.Errorf("Complete mutated status to COMPLETED")
	}

	// Delete (single instance / standalone).
	if err := svc.Delete(ctx, td.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("Delete: error = %v, want ErrReadOnly", err)
	}
	if got, err := svc.Get(ctx, td.ID); err != nil {
		t.Errorf("Delete dropped todo: %v", err)
	} else if got.DeletedAt != nil {
		t.Errorf("Delete soft-deleted todo")
	}

	// DeleteSeries.
	if err := svc.DeleteSeries(ctx, series.UID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteSeries: error = %v, want ErrReadOnly", err)
	}
	if _, err := svc.Get(ctx, series.ID); err != nil {
		t.Errorf("DeleteSeries dropped todo: %v", err)
	}
}

// A VEVENT-only collection rejects representative mutations and persists nothing.
func TestTodoMutations_UnsupportedComponentRejected(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	td := createTodo(t, svc)
	setTodoCalendarAccess(t, db, "owner", "VEVENT")

	if _, err := svc.Update(ctx, td.ID, UpdateParams{
		Summary:    "x",
		CalendarID: td.CalendarID,
	}); !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("Update: error = %v, want ErrUnsupportedComponent", err)
	}
	if _, err := svc.Complete(ctx, td.ID); !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("Complete: error = %v, want ErrUnsupportedComponent", err)
	}
	if err := svc.Delete(ctx, td.ID); !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("Delete: error = %v, want ErrUnsupportedComponent", err)
	}
	if err := svc.DeleteSeries(ctx, td.UID); !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("DeleteSeries: error = %v, want ErrUnsupportedComponent", err)
	}

	// No mutation: the todo is still live and unchanged.
	got, err := svc.Get(ctx, td.ID)
	if err != nil {
		t.Fatalf("todo dropped: %v", err)
	}
	if got.Summary != td.Summary {
		t.Errorf("summary mutated: got %q, want %q", got.Summary, td.Summary)
	}
	if got.IsCompleted() {
		t.Errorf("status mutated to COMPLETED")
	}
	if got.DeletedAt != nil {
		t.Errorf("todo soft-deleted by rejected write")
	}
}

// A read-only collection rejects restore paths, leaving rows soft-deleted.
func TestTodoRestore_ReadOnlyRejectedNoMutation(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()

	// Seed and soft-delete while writable.
	td := createTodo(t, svc)
	master := createTodo(t, svc)
	if err := svc.Delete(ctx, td.ID); err != nil {
		t.Fatalf("seed delete: %v", err)
	}
	if err := svc.DeleteSeries(ctx, master.UID); err != nil {
		t.Fatalf("seed delete series: %v", err)
	}

	setTodoCalendarAccess(t, db, "read", "")

	// RestoreByID rejected; todo stays soft-deleted (hidden from live reads).
	if err := svc.RestoreByID(ctx, td.ID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByID: error = %v, want ErrReadOnly", err)
	}
	if _, err := svc.Get(ctx, td.ID); err == nil {
		t.Errorf("RestoreByID un-deleted todo")
	}

	// RestoreByUID rejected; series stays soft-deleted.
	if err := svc.RestoreByUID(ctx, master.UID); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByUID: error = %v, want ErrReadOnly", err)
	}
	if _, err := svc.Get(ctx, master.ID); err == nil {
		t.Errorf("RestoreByUID un-deleted master")
	}
}

// A move validates both endpoints: a read-only destination rejects the move
// even when the source is writable, and a read-only source rejects it too.
func TestTodoUpdate_MoveGuardsSourceAndDestination(t *testing.T) {
	t.Run("read-only destination", func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		svc := NewService(db, q)
		ctx := context.Background()
		roDest := addTodoCalendar(t, db, "read", "")
		td := createTodo(t, svc) // calendar 1, writable

		if _, err := svc.Update(ctx, td.ID, UpdateParams{
			Summary:    "moved",
			CalendarID: roDest,
		}); !errors.Is(err, calendaraccess.ErrReadOnly) {
			t.Fatalf("move to read-only: error = %v, want ErrReadOnly", err)
		}
		if got, _ := svc.Get(ctx, td.ID); got.CalendarID != td.CalendarID {
			t.Errorf("move changed calendar: got %d, want %d", got.CalendarID, td.CalendarID)
		}
	})

	t.Run("read-only source", func(t *testing.T) {
		db, q := testutil.NewTestDB(t)
		svc := NewService(db, q)
		ctx := context.Background()
		writable := addTodoCalendar(t, db, "owner", "")
		td := createTodo(t, svc) // calendar 1

		// Flip the source calendar read-only after seeding.
		setTodoCalendarAccess(t, db, "read", "")

		if _, err := svc.Update(ctx, td.ID, UpdateParams{
			Summary:    "moved",
			CalendarID: writable,
		}); !errors.Is(err, calendaraccess.ErrReadOnly) {
			t.Fatalf("move from read-only: error = %v, want ErrReadOnly", err)
		}
		if got, _ := svc.Get(ctx, td.ID); got.CalendarID != td.CalendarID {
			t.Errorf("move changed calendar: got %d, want %d", got.CalendarID, td.CalendarID)
		}
	})
}

// upsertTodoWithUID inserts a todo with an explicit UID (and optional
// recurrence id) via the unguarded sync upsert path, so a test can build a
// series sharing one UID — Create cannot, since it always mints a fresh UID.
func upsertTodoWithUID(t *testing.T, svc *Service, uid, recurrenceID string) Todo {
	t.Helper()
	td, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:          uid,
		CalendarID:   1,
		Summary:      "series member",
		RecurrenceID: recurrenceID,
	})
	if err != nil {
		t.Fatalf("upsert todo %q/%q: %v", uid, recurrenceID, err)
	}
	return td
}

// purgeMasterRow hard-deletes the master row (recurrence_id = ”) for uid,
// simulating an independent purge that leaves orphaned override / series-tail
// rows behind.
func purgeMasterRow(t *testing.T, db *sql.DB, uid string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(),
		`DELETE FROM todos WHERE uid = ? AND recurrence_id = ''`, uid,
	); err != nil {
		t.Fatalf("purge master row: %v", err)
	}
}

// DeleteSeries must still reject when the master row has been purged but a live
// orphaned override remains in a read-only calendar. A master-only guard would
// resolve no calendar and let the soft-delete through.
func TestTodoDeleteSeries_ReadOnlyRejectedWithPurgedMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	const uid = "orphan-series-delete"
	const recID = "2026-04-10T00:00:00Z"

	upsertTodoWithUID(t, svc, uid, "")    // master
	upsertTodoWithUID(t, svc, uid, recID) // live override
	purgeMasterRow(t, db, uid)            // master gone, override orphaned

	setTodoCalendarAccess(t, db, "read", "")

	if err := svc.DeleteSeries(ctx, uid); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("DeleteSeries with purged master: error = %v, want ErrReadOnly", err)
	}
	// The orphaned override is still live — not soft-deleted by the rejected call.
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recID); err != nil {
		t.Errorf("orphan override dropped or soft-deleted by rejected DeleteSeries: %v", err)
	}
}

// RestoreByUID must still reject when the master row has been purged but a
// soft-deleted series-tail override remains in a read-only calendar.
func TestTodoRestoreByUID_ReadOnlyRejectedWithPurgedMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	const uid = "orphan-series-restore"
	const recID = "2026-04-11T00:00:00Z"

	upsertTodoWithUID(t, svc, uid, "")    // master
	upsertTodoWithUID(t, svc, uid, recID) // override
	// Soft-delete the whole series while the calendar is writable.
	if err := svc.DeleteSeries(ctx, uid); err != nil {
		t.Fatalf("seed delete series: %v", err)
	}
	purgeMasterRow(t, db, uid) // master gone, soft-deleted override orphaned

	setTodoCalendarAccess(t, db, "read", "")

	if err := svc.RestoreByUID(ctx, uid); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreByUID with purged master: error = %v, want ErrReadOnly", err)
	}
	// The orphaned override is still soft-deleted (hidden from live reads).
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recID); err == nil {
		t.Errorf("orphan override un-deleted by rejected RestoreByUID")
	}
}

// Sanity: with the master purged, an unsupported-component calendar (VEVENT-only)
// also rejects the series delete via the same UID-spanning resolution.
func TestTodoDeleteSeries_UnsupportedComponentWithPurgedMaster(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	const uid = "orphan-series-comp"
	const recID = "2026-04-12T00:00:00Z"

	upsertTodoWithUID(t, svc, uid, "")
	upsertTodoWithUID(t, svc, uid, recID)
	purgeMasterRow(t, db, uid)

	setTodoCalendarAccess(t, db, "owner", "VEVENT")

	if err := svc.DeleteSeries(ctx, uid); !errors.Is(err, calendaraccess.ErrUnsupportedComponent) {
		t.Fatalf("DeleteSeries with purged master: error = %v, want ErrUnsupportedComponent", err)
	}
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, uid, recID); err != nil {
		t.Errorf("orphan override dropped or soft-deleted by rejected DeleteSeries: %v", err)
	}
}
