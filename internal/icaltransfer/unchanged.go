package icaltransfer

import (
	"bytes"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

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
// content:
//   - the row ID, the calendar ID, and the SEQUENCE
//   - DTSTAMP, CREATED, and LAST-MODIFIED
//   - the stored UID of an alarm that has no UID in the file, because the
//     store gives a UID to an alarm without one
//   - both UIDs of an alarm whose file UID another alarm row holds, because
//     the store cannot keep that UID (issue #915)
//   - the order of the alarms, because the store reads them in the order of
//     their row IDs
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

// sortedCategories returns the categories of list in sorted order.
func sortedCategories(list string) string {
	cats := timeutil.ParseCategoryList(list)
	slices.Sort(cats)
	return timeutil.JoinCategoryList(cats)
}

// sortedAttendees returns a copy of attendees in one fixed order.
func sortedAttendees(attendees []model.Attendee) []model.Attendee {
	return sortedByPrintedForm(attendees, func(a model.Attendee) model.Attendee {
		a.ID, a.EventID = 0, 0
		return a
	})
}

// sortedByPrintedForm returns the normalized copies of xs, sorted by their
// printed form. normalize clears the fields that the comparison ignores.
func sortedByPrintedForm[T any](xs []T, normalize func(T) T) []T {
	type keyed struct {
		key string
		x   T
	}
	ks := make([]keyed, len(xs))
	for i, x := range xs {
		x = normalize(x)
		ks[i] = keyed{fmt.Sprintf("%+v", x), x}
	}
	slices.SortStableFunc(ks, func(a, b keyed) int { return strings.Compare(a.key, b.key) })
	out := make([]T, len(ks))
	for i, k := range ks {
		out[i] = k.x
	}
	return out
}

// comparableAlarms returns copies of the stored and the parsed alarms in a
// form that the comparison can use. It clears each parsed UID that the
// store refuses, with the rule of the alarm write
// (model.WithoutRefusedAlarmUIDs, issue #915). It then pairs each parsed
// alarm with a stored alarm, first by an equal UID and then by equal
// content. A parsed alarm with no UID clears the UID of its stored alarm,
// because the store gave that UID. A file UID that the store can keep
// stays, so a change of it counts.
//
// The stored alarms come back in the order of their row IDs, not in the
// order of the file, so both lists go in one fixed order.
func comparableAlarms(stored, parsed []model.Alarm, heldElsewhere func(uid string) (bool, error)) (outStored, outParsed []model.Alarm, err error) {
	outParsed, err = model.WithoutRefusedAlarmUIDs(parsed, heldElsewhere)
	if err != nil {
		return nil, nil, err
	}
	outStored = slices.Clone(stored)
	used := make([]bool, len(outStored))
	paired := make([]bool, len(outParsed))
	join := func(same func(p, s model.Alarm) bool) {
		for i, p := range outParsed {
			if paired[i] {
				continue
			}
			for j, s := range outStored {
				if !used[j] && same(p, s) {
					paired[i], used[j] = true, true
					if p.UID == "" {
						outStored[j].UID = ""
					}
					break
				}
			}
		}
	}
	join(func(p, s model.Alarm) bool { return p.UID != "" && p.UID == s.UID })
	join(model.Alarm.ContentEqual)
	return sortedAlarms(outStored), sortedAlarms(outParsed), nil
}

// sortedAlarms returns alarms in one fixed order. The order of the VALARM
// components is no change.
func sortedAlarms(alarms []model.Alarm) []model.Alarm {
	return sortedByPrintedForm(alarms, func(a model.Alarm) model.Alarm {
		a.ID, a.EventID = 0, 0
		a.Attendees = slices.Clone(a.Attendees)
		for i := range a.Attendees {
			a.Attendees[i].ID = 0
		}
		a.XProperties = slices.Clone(a.XProperties)
		for i := range a.XProperties {
			a.XProperties[i].ID, a.XProperties[i].OwnerID, a.XProperties[i].OwnerType = 0, 0, ""
		}
		return a
	})
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
	old.Alarms, e.Alarms, err = comparableAlarms(old.Alarms, e.Alarms, event.AlarmUIDHeldElsewhere(ctx, a.Queries, old.ID))
	if err != nil {
		return false
	}
	form := func(e event.Event) func() ([]byte, error) {
		e.ID, e.CalendarID, e.Sequence, e.DtStamp = 0, 0, 0, ""
		e.CreatedAt, e.UpdatedAt, e.DeletedAt = time.Time{}, time.Time{}, nil
		e.Categories = sortedCategories(e.Categories)
		e.Attendees = sortedAttendees(e.Attendees)
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
	old.Alarms, t.Alarms, err = comparableAlarms(old.Alarms, t.Alarms, todo.AlarmUIDHeldElsewhere(ctx, a.Queries, old.ID))
	if err != nil {
		return false
	}
	form := func(t todo.Todo) func() ([]byte, error) {
		t.ID, t.CalendarID, t.Sequence, t.DtStamp = 0, 0, 0, ""
		t.CreatedAt, t.UpdatedAt, t.DeletedAt = time.Time{}, time.Time{}, nil
		t.Categories = sortedCategories(t.Categories)
		t.Attendees = sortedAttendees(t.Attendees)
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
		j.ID, j.CalendarID, j.Sequence, j.DtStamp = 0, 0, 0, ""
		j.CreatedAt, j.UpdatedAt, j.DeletedAt = time.Time{}, time.Time{}, nil
		j.Categories = sortedCategories(j.Categories)
		j.Attendees = sortedAttendees(j.Attendees)
		// The store keeps the joined Description only, so the split of
		// the parsed DESCRIPTION properties is no change.
		j.Descriptions = nil
		return func() ([]byte, error) { return ical.ExportJournals([]journal.Journal{j}, "") }
	}
	return sameExport(form(old), form(j))
}
