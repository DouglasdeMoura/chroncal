package icaltransfer

import (
	"bytes"
	"context"
	"slices"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// The import skips the write of an item that changes nothing (issue #883).
// The upsert adds 1 to the SEQUENCE of each row that it writes, so a
// directory import from a timer would add 1 on every run. A skipped item
// keeps its SEQUENCE, its child rows, and its sync state, and the summary
// counts it as unchanged.
//
// The rule compares the iCalendar export of the stored item with the export
// of the parsed item. The export holds almost every field that the import
// stores, so a change of a row field, a category, or a child row makes the
// two exports differ. The export can drop the timezone column and the
// duration column, so the rule also compares those columns directly.
//
// The comparison ignores the fields that an import does not count as
// content. The content form of internal/ical (ical.ContentEvent,
// ical.ContentTodo, ical.ContentJournal) holds the shared rules:
//   - the row ID, the calendar ID, and the SEQUENCE
//   - DTSTAMP, CREATED, and LAST-MODIFIED
//   - the stored UID of an alarm that has no UID in the file, because the
//     store gives a UID to an alarm without one
//   - both UIDs of an alarm whose file UID an earlier alarm of the item
//     has, because the store cannot keep that UID (issue #915)
//   - the order of the alarms, because the store reads them in the order of
//     their row IDs and pairs them by content or by UID
//   - the order of the categories and of the attendees, because the store
//     reads them in a sorted order
//
// A parsed SEQUENCE above the stored SEQUENCE is a new revision, so the
// item is not unchanged. A failed read or a failed export also counts as a
// change: the import then writes the item, as before.

// skippable reports whether the import can skip the write of a row with
// the stored SEQUENCE storedSeq in calendarID. A parsed SEQUENCE above it
// needs the write. A synced calendar with a tombstone of uid needs the write
// too: the import clears the pending delete of a master
// (storage.ApplyImport).
func skippable(ctx context.Context, a *app.App, mode *storage.Import, calendarID int64, uid string, storedSeq, parsedSeq int64) bool {
	if parsedSeq > storedSeq {
		return false
	}
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

// comparableAlarms returns copies of the stored and the parsed alarms in a
// form that the comparison can use. It applies the rules of the alarm
// write: it clears each parsed UID that the store refuses
// (model.WithoutRefusedAlarmUIDs), and it pairs the alarms with
// model.PairAlarms. The parsed alarms then take the order of their stored
// alarms, so the order of the VALARMs is no change. A parsed alarm with no
// UID clears the UID of its stored alarm, because the store gave that UID.
// A file UID that differs from the UID of its stored alarm stays, so the
// change counts. A pair of the third pass is such a change: the write
// gives the stored alarm the UID of the file.
//
// ok is false when an alarm has no pair or a lookup fails: the write then
// creates or deletes an alarm, so the item changes.
func comparableAlarms(stored, parsed []model.Alarm, heldElsewhere func(uids []string) (map[string]bool, error)) (outStored, outParsed []model.Alarm, ok bool) {
	parsed = model.WithoutRefusedAlarmUIDs(parsed)
	if len(parsed) != len(stored) {
		return nil, nil, false
	}
	held, err := heldElsewhere(model.AlarmUIDs(parsed))
	if err != nil {
		return nil, nil, false
	}
	outStored = slices.Clone(stored)
	outParsed = make([]model.Alarm, len(stored))
	for i, p := range model.PairAlarms(stored, parsed, held) {
		if p.Stored < 0 {
			return nil, nil, false
		}
		if parsed[i].UID == "" {
			outStored[p.Stored].UID = ""
		}
		outParsed[p.Stored] = parsed[i]
	}
	return outStored, outParsed, true
}

// unchangedEvent reports whether the import of e changes nothing on the
// stored event old. old is a live row in the calendar of the import.
func unchangedEvent(ctx context.Context, a *app.App, mode *storage.Import, old, e event.Event) bool {
	e = e.WithStoreDefaults()
	if !skippable(ctx, a, mode, old.CalendarID, old.UID, old.Sequence, e.Sequence) ||
		old.Timezone != e.Timezone || old.DurationValue != e.DurationValue {
		return false
	}
	// The read of the row drops a failed category read, so read the
	// categories again here.
	cats, err := a.Events.ListCategories(ctx, old.ID)
	if err != nil || a.Events.Hydrate(ctx, &old) != nil {
		return false
	}
	old.Categories = timeutil.JoinCategoryList(cats)
	var ok bool
	old.Alarms, e.Alarms, ok = comparableAlarms(old.Alarms, e.Alarms, func(uids []string) (map[string]bool, error) {
		return event.AlarmUIDsHeldElsewhere(ctx, a.Queries, old.ID, uids)
	})
	if !ok {
		return false
	}
	form := func(e event.Event) func() ([]byte, error) {
		e = ical.ContentEvent(e)
		return func() ([]byte, error) { return ical.ExportEvents([]event.Event{e}, "") }
	}
	return sameExport(form(old), form(e))
}

// unchangedTodo does the same work as unchangedEvent for todos.
func unchangedTodo(ctx context.Context, a *app.App, mode *storage.Import, old, t todo.Todo) bool {
	t = t.WithStoreDefaults(old.CompletedAt)
	if !skippable(ctx, a, mode, old.CalendarID, old.UID, old.Sequence, t.Sequence) ||
		old.Timezone != t.Timezone || old.Duration != t.Duration {
		return false
	}
	cats, err := a.Todos.ListCategories(ctx, old.ID)
	if err != nil || a.Todos.Hydrate(ctx, &old) != nil {
		return false
	}
	old.Categories = timeutil.JoinCategoryList(cats)
	var ok bool
	old.Alarms, t.Alarms, ok = comparableAlarms(old.Alarms, t.Alarms, func(uids []string) (map[string]bool, error) {
		return todo.AlarmUIDsHeldElsewhere(ctx, a.Queries, old.ID, uids)
	})
	if !ok {
		return false
	}
	form := func(t todo.Todo) func() ([]byte, error) {
		t = ical.ContentTodo(t)
		return func() ([]byte, error) { return ical.ExportTodos([]todo.Todo{t}, "") }
	}
	return sameExport(form(old), form(t))
}

// unchangedJournal does the same work as unchangedEvent for journals. A
// journal has no alarms.
func unchangedJournal(ctx context.Context, a *app.App, mode *storage.Import, old, j journal.Journal) bool {
	j = j.WithStoreDefaults()
	if !skippable(ctx, a, mode, old.CalendarID, old.UID, old.Sequence, j.Sequence) || old.Timezone != j.Timezone {
		return false
	}
	cats, err := a.Journals.ListCategories(ctx, old.ID)
	if err != nil || a.Journals.Hydrate(ctx, &old) != nil {
		return false
	}
	old.Categories = timeutil.JoinCategoryList(cats)
	form := func(j journal.Journal) func() ([]byte, error) {
		j = ical.ContentJournal(j)
		return func() ([]byte, error) { return ical.ExportJournals([]journal.Journal{j}, "") }
	}
	return sameExport(form(old), form(j))
}
