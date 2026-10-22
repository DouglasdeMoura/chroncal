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
	if _, err := engine.deleteLocalResourceByUID(ctx, calendarID, ownerTypeEvent, uid, true); err != nil {
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

// TestPruneMarker_KeepsPrunedRowOutOfSameSecondRestore: a pull prune
// writes a pruned marker. The trash lists no entry for it. A restore of the
// master with no series-delete log brings back the rows of the same second,
// but not the pruned row.
func TestPruneMarker_KeepsPrunedRowOutOfSameSecondRestore(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	const uid = "prune-marker"
	const prunedRID = "2026-07-02T17:00:00Z"
	const keptRID = "2026-08-27T17:00:00Z"
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
		pruneTestEvent(uid, calendarID, prunedRID, ""),
		pruneTestEvent(uid, calendarID, keptRID, ""),
	}}); err != nil {
		t.Fatalf("first persistImported: %v", err)
	}
	if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
		pruneTestEvent(uid, calendarID, keptRID, ""),
	}}); err != nil {
		t.Fatalf("second persistImported: %v", err)
	}
	entries, err := engine.events.ListTrash(ctx, calendarID)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == event.TrashKindInstance {
			t.Errorf("trash lists the pruned marker as an instance entry")
		}
	}
	if _, err := engine.deleteLocalResourceByUID(ctx, calendarID, ownerTypeEvent, uid, true); err != nil {
		t.Fatalf("deleteLocalResourceByUID: %v", err)
	}
	// One second for every row, as one delete would leave them.
	if _, err := engine.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-09-01T10:00:00Z' WHERE uid = ?`, uid); err != nil {
		t.Fatalf("align deleted_at: %v", err)
	}
	master, err := q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: calendarID, Uid: uid})
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if err := engine.events.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	for rid, wantLive := range map[string]bool{keptRID: true, prunedRID: false} {
		r, err := q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{CalendarID: calendarID, Uid: uid, RecurrenceID: rid})
		if err != nil {
			t.Fatalf("get row %s: %v", rid, err)
		}
		if live := r.DeletedAt == nil; live != wantLive {
			t.Errorf("row %s live = %v, want %v", rid, live, wantLive)
		}
	}
}

// TestPruneMarker_ReimportDropsIt: a pull that imports a pruned override
// again drops its pruned marker.
func TestPruneMarker_ReimportDropsIt(t *testing.T) {
	t.Parallel()
	engine, _, q := newTestEngine(t)
	ctx := context.Background()
	cals, err := q.ListCalendars(ctx)
	if err != nil {
		t.Fatalf("ListCalendars: %v", err)
	}
	calendarID := cals[0].ID
	const uid = "prune-reimport"
	const rid = "2026-07-02T17:00:00Z"
	withRow := []event.Event{
		pruneTestEvent(uid, calendarID, "", "FREQ=WEEKLY;INTERVAL=2;BYDAY=TH"),
		pruneTestEvent(uid, calendarID, rid, ""),
	}
	markers := func() int64 {
		t.Helper()
		var n int64
		if err := engine.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_exdate_deletes WHERE uid = ? AND pruned = 1`, uid).Scan(&n); err != nil {
			t.Fatalf("count markers: %v", err)
		}
		return n
	}
	for i, events := range [][]event.Event{withRow, withRow[:1], withRow} {
		if _, _, err := engine.persistImported(ctx, calendarID, icalPkg.ImportResult{Events: events}); err != nil {
			t.Fatalf("persistImported %d: %v", i, err)
		}
		if i == 1 && markers() != 1 {
			t.Fatalf("pruned markers after the prune = %d, want 1", markers())
		}
	}
	if n := markers(); n != 0 {
		t.Errorf("pruned markers after the import of the row again = %d, want 0", n)
	}
}
