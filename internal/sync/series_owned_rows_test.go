package sync

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// insertLegacyRow adds a calendar and a live override row of uid there at
// rid, with no master in that calendar. It returns the row ID.
func insertLegacyRow(t *testing.T, engine *Engine, uid, rid string) int64 {
	t.Helper()
	ctx := context.Background()
	res, err := engine.db.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	if err != nil {
		t.Fatalf("insert calendar: %v", err)
	}
	calL, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("calendar id: %v", err)
	}
	res, err = engine.db.ExecContext(ctx, `INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id)
		VALUES (?, ?, 'Legacy', ?, ?, ?)`, uid, calL, rid, rid, rid)
	if err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatalf("row id: %v", err)
	}
	return id
}

// trashed reports whether the event row id is in the trash.
func trashed(t *testing.T, engine *Engine, id int64) bool {
	t.Helper()
	var deletedAt *string
	if err := engine.db.QueryRowContext(context.Background(), `SELECT deleted_at FROM events WHERE id = ?`, id).Scan(&deletedAt); err != nil {
		t.Fatalf("read row %d: %v", id, err)
	}
	return deletedAt != nil
}

// TestPersistImportedKeepsLegacyRowOfOtherCalendar: a pull writes the rows
// of the pulled calendar only. A legacy override in another calendar keeps
// its row, also when the server object no longer carries its occurrence.
func TestPersistImportedKeepsLegacyRowOfOtherCalendar(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	const uid = "owned-legacy-prune"
	const legacyRID = "2026-07-02T17:00:00Z"
	const keptRID = "2026-08-27T17:00:00Z"
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
		pruneTestEvent(uid, calendarID, keptRID, ""),
	}}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}
	legacy := insertLegacyRow(t, engine, uid, legacyRID)
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
		pruneTestEvent(uid, calendarID, keptRID, ""),
	}}); err != nil {
		t.Fatalf("second persistImported: %v", err)
	}
	if trashed(t, engine, legacy) {
		t.Errorf("the legacy row of another calendar is in the trash after a pull, want it live")
	}
}

// TestDeleteLocalResource_KeepsLegacyRowOfOtherCalendar: a remote DELETE
// of the object of a series soft-deletes the rows of the pulled calendar.
// A legacy override in another calendar keeps its row.
func TestDeleteLocalResource_KeepsLegacyRowOfOtherCalendar(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	const uid = "owned-legacy-delete"
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
	}}); err != nil {
		t.Fatalf("persistImported: %v", err)
	}
	legacy := insertLegacyRow(t, engine, uid, "2026-07-02T17:00:00Z")
	if err := engine.deleteLocalResourceByUID(ctx, calendarID, ownerTypeEvent, uid); err != nil {
		t.Fatalf("deleteLocalResourceByUID: %v", err)
	}
	if trashed(t, engine, legacy) {
		t.Errorf("the legacy row of another calendar is in the trash after a remote delete, want it live")
	}
	master, err := q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: calendarID, Uid: uid})
	if err != nil || master.DeletedAt == nil {
		t.Errorf("master of the pulled calendar after the remote delete = trashed %v, %v; want it in the trash", master.DeletedAt != nil, err)
	}
}
