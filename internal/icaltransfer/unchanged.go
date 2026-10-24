package icaltransfer

import (
	"bytes"
	"context"
	"time"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The import skips the write of an item that changes nothing (issue #883).
// The upsert adds 1 to the SEQUENCE of each row that it writes, so a
// directory import from a timer would add 1 on every run. A skipped item
// keeps its SEQUENCE, its child rows, and its sync state, and the summary
// counts it as unchanged.
//
// The rule compares the iCalendar export of the stored item with the export
// of the parsed item. The export holds every field that the import stores,
// so a change of a row field, a category, or a child row makes the two
// exports differ. The comparison ignores the fields that an import does not
// count as content:
//   - the row ID, the calendar ID, and the SEQUENCE
//   - DTSTAMP, CREATED, and LAST-MODIFIED
//   - the UID of an alarm, because the store gives a UID to an alarm
//     without one
//
// A parsed SEQUENCE above the stored SEQUENCE is a new revision, so the
// item is not unchanged. A failed read or a failed export also counts as a
// change: the import then writes the item, as before.

// skippable reports whether the import can skip a write in calendarID. A
// synced calendar with a tombstone of uid needs the write: the import
// clears the pending delete of a master (storage.ApplyImport).
func skippable(ctx context.Context, a *app.App, mode *storage.Import, calendarID int64, uid string) bool {
	if !mode.Synced {
		return true
	}
	_, err := a.Queries.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	found, err := storage.Found(err)
	return err == nil && !found
}

// sameExport reports whether two exports succeed and hold the same bytes.
func sameExport(stored, imported func() ([]byte, error)) bool {
	a, err := stored()
	if err != nil {
		return false
	}
	b, err := imported()
	return err == nil && bytes.Equal(a, b)
}

// withoutAlarmUIDs returns a copy of alarms with no UID.
func withoutAlarmUIDs(alarms []model.Alarm) []model.Alarm {
	if alarms == nil {
		return nil
	}
	out := make([]model.Alarm, len(alarms))
	copy(out, alarms)
	for i := range out {
		out[i].UID = ""
	}
	return out
}

// unchangedEvent reports whether the import of e changes nothing on the
// stored event old. old is a live row in the calendar of the import.
func unchangedEvent(ctx context.Context, a *app.App, mode *storage.Import, old, e event.Event) bool {
	if e.Sequence > old.Sequence || !skippable(ctx, a, mode, old.CalendarID, old.UID) {
		return false
	}
	if err := a.Events.Hydrate(ctx, &old); err != nil {
		return false
	}
	form := func(e event.Event) func() ([]byte, error) {
		e.ID, e.CalendarID, e.Sequence, e.DtStamp = 0, 0, 0, ""
		e.CreatedAt, e.UpdatedAt, e.DeletedAt = time.Time{}, time.Time{}, nil
		e.Alarms = withoutAlarmUIDs(e.Alarms)
		return func() ([]byte, error) { return ical.ExportEvents([]event.Event{e}, "") }
	}
	return sameExport(form(old), form(e.WithStoreDefaults()))
}

// unchangedTodo does the same work as unchangedEvent for todos.
func unchangedTodo(ctx context.Context, a *app.App, mode *storage.Import, old, t todo.Todo) bool {
	if t.Sequence > old.Sequence || !skippable(ctx, a, mode, old.CalendarID, old.UID) {
		return false
	}
	if err := a.Todos.Hydrate(ctx, &old); err != nil {
		return false
	}
	form := func(t todo.Todo) func() ([]byte, error) {
		t.ID, t.CalendarID, t.Sequence, t.DtStamp = 0, 0, 0, ""
		t.CreatedAt, t.UpdatedAt, t.DeletedAt = time.Time{}, time.Time{}, nil
		t.Alarms = withoutAlarmUIDs(t.Alarms)
		return func() ([]byte, error) { return ical.ExportTodos([]todo.Todo{t}, "") }
	}
	return sameExport(form(old), form(t.WithStoreDefaults(old.CompletedAt)))
}

// unchangedJournal does the same work as unchangedEvent for journals.
func unchangedJournal(ctx context.Context, a *app.App, mode *storage.Import, old, j journal.Journal) bool {
	if j.Sequence > old.Sequence || !skippable(ctx, a, mode, old.CalendarID, old.UID) {
		return false
	}
	if err := a.Journals.Hydrate(ctx, &old); err != nil {
		return false
	}
	form := func(j journal.Journal) func() ([]byte, error) {
		j.ID, j.CalendarID, j.Sequence, j.DtStamp = 0, 0, 0, ""
		j.CreatedAt, j.UpdatedAt, j.DeletedAt = time.Time{}, time.Time{}, nil
		return func() ([]byte, error) { return ical.ExportJournals([]journal.Journal{j}, "") }
	}
	return sameExport(form(old), form(j.WithStoreDefaults()))
}
