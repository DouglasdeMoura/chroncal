// Package icaltransfer implements the parse, validate, and import pipeline
// behind the `chroncal ical import` CLI.
//
// The CLI used to keep this behavior inline. This package holds it in one place.
// The TUI and other internal callers can then use the same flow. They do not
// re-implement the UID-upsert, capability, and warning semantics.
package icaltransfer

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/textsafe"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Stable iCal component-family names. These are the labels used in error
// messages and capability checks. Callers should reuse them when they
// report problems. Do not use string literals.
const (
	FamilyEvent   = "VEVENT"
	FamilyTodo    = "VTODO"
	FamilyJournal = "VJOURNAL"
)

// Preview is the parsed view of an .ics file before any row is written. It
// wraps the ical.ImportResult with per-family counts and a copy of the
// warnings from parse time. The warnings are copied so the preview stands
// on its own even after Import mutates the result.
type Preview struct {
	Result ical.ImportResult

	Events   int
	Todos    int
	Journals int
	FreeBusy int

	// Warnings is a snapshot of Result.Warnings captured at parse time.
	Warnings []string
}

// Summary records what an Import landed and what it dropped. It carries
// enough of the imported rows for the CLI to show them in JSON or text
// form without a second database read.
type Summary struct {
	Events   []event.Event
	Todos    []todo.Todo
	Journals []journal.Journal

	NewEvents, UpdatedEvents     int
	NewTodos, UpdatedTodos       int
	NewJournals, UpdatedJournals int

	// Failed counts components whose own upsert failed (and were therefore
	// skipped entirely). Child-field failures are recorded as Warnings
	// instead, since the parent component itself did land.
	Failed int

	// Warnings is a snapshot of the result's warnings after Import ran,
	// i.e. both parse-time and import-time warnings in order.
	Warnings []string
}

// ParseFile opens path, parses it with ical.ImportFile, and closes the file.
// Open and parse errors are wrapped so callers can show them in one form
// (same as the prior CLI "open file" / "import" wraps).
func ParseFile(path string) (Preview, error) {
	var preview Preview

	f, err := os.Open(path)
	if err != nil {
		return preview, fmt.Errorf("open file: %w", err)
	}
	defer f.Close()

	result, err := ical.ImportFile(f)
	if err != nil {
		return preview, fmt.Errorf("import: %w", err)
	}

	preview.Result = result
	preview.Events = len(result.Events)
	preview.Todos = len(result.Todos)
	preview.Journals = len(result.Journals)
	preview.FreeBusy = len(result.FreeBusy)
	preview.Warnings = append([]string(nil), result.Warnings...)
	return preview, nil
}

// ValidateDestination validates every present component family before the
// first write. Each present family must be writable at the destination.
// Event UIDs are unique per calendar (issue #756), so an event that already
// lives on another calendar does not move. Todos and journals still use a
// global UID. A todo or journal UID that already lives in another calendar
// must live in a calendar the caller can write to.
func ValidateDestination(ctx context.Context, a *app.App, calendarID int64, preview Preview) error {
	result := preview.Result

	checkDestination := func(present bool, component string) error {
		if !present {
			return nil
		}
		if err := calendaraccess.EnsureWritable(ctx, a.Queries, calendarID, component); err != nil {
			return fmt.Errorf("import %s: %w", component, err)
		}
		return nil
	}
	for _, check := range []struct {
		present   bool
		component string
	}{
		{present: len(result.Events) > 0, component: FamilyEvent},
		{present: len(result.Todos) > 0, component: FamilyTodo},
		{present: len(result.Journals) > 0, component: FamilyJournal},
	} {
		if err := checkDestination(check.present, check.component); err != nil {
			return err
		}
	}

	checkSource := func(sourceID int64, component, uid string) error {
		if sourceID == calendarID {
			return nil
		}
		if err := calendaraccess.EnsureWritable(ctx, a.Queries, sourceID, component); err != nil {
			return fmt.Errorf("import %s UID %q from calendar %d: %w", component, uid, sourceID, err)
		}
		return nil
	}
	for _, imported := range result.Todos {
		existing, err := a.Queries.GetTodoByUIDAndRecurrenceID(ctx, storage.GetTodoByUIDAndRecurrenceIDParams{
			Uid: imported.UID, RecurrenceID: imported.RecurrenceID,
		})
		if err == nil {
			if err := checkSource(existing.CalendarID, FamilyTodo, imported.UID); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing %s UID %q: %w", FamilyTodo, imported.UID, err)
		}
	}
	for _, imported := range result.Journals {
		existing, err := a.Queries.GetJournalByUIDAndRecurrenceID(ctx, storage.GetJournalByUIDAndRecurrenceIDParams{
			Uid: imported.UID, RecurrenceID: imported.RecurrenceID,
		})
		if err == nil {
			if err := checkSource(existing.CalendarID, FamilyJournal, imported.UID); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing %s UID %q: %w", FamilyJournal, imported.UID, err)
		}
	}
	return nil
}

// Import upserts the parsed timezones, events, todos, and journals into
// calendarID. A failure on any single component is recorded in result
// warnings (and Summary.Failed). The loop then continues. One bad item
// does not abort the run or discard later components.
//
// Child collections (alarms, attendees, and others) that fail to attach
// become warnings. They are not dropped in silence. The import then never
// reports a clean success while it loses data. The passed result is mutated
// to collect warnings. This matches the legacy CLI behavior.
func Import(ctx context.Context, a *app.App, calendarID int64, result *ical.ImportResult) Summary {
	var summary Summary

	// Store imported VTIMEZONE components.
	for _, tz := range result.Timezones {
		if _, err := a.Queries.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
			Tzid:          tz.TZID,
			VtimezoneData: tz.Data,
		}); err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("store VTIMEZONE %s: %v", tz.TZID, err))
		}
	}

	// Import events.
	for _, e := range result.Events {
		_, lookupErr := lookupEvent(ctx, a.Events, calendarID, e.UID, e.RecurrenceID)
		saved, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
			UID: e.UID, CalendarID: calendarID,
			Title: e.Title, Description: e.Description, Location: e.Location,
			StartTime: e.StartTime, EndTime: e.EndTime, AllDay: e.AllDay,
			RecurrenceRule: e.RecurrenceRule, Timezone: e.Timezone,
			Status: e.Status, Transp: e.Transp, Sequence: e.Sequence,
			Priority: e.Priority, Class: e.Class, URL: e.URL,
			ConferenceURI: e.ConferenceURI,
			Categories:    e.Categories, ExDates: e.ExDates, RDates: e.RDates,
			RecurrenceID: e.RecurrenceID, Geo: e.Geo,
			DurationValue: e.DurationValue, DtStamp: e.DtStamp,
		})
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("import event %q: %v", textsafe.Display(e.Title), err))
			summary.Failed++
			continue
		}
		result.Warnings = append(result.Warnings, importEventFields(ctx, a.Events, saved.ID, e)...)
		summary.Events = append(summary.Events, saved)
		if lookupErr != nil {
			summary.NewEvents++
		} else {
			summary.UpdatedEvents++
		}
	}

	// Import todos.
	for _, t := range result.Todos {
		_, lookupErr := lookupTodo(ctx, a.Todos, t.UID, t.RecurrenceID)
		saved, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{
			UID: t.UID, CalendarID: calendarID,
			Summary: t.Summary, Description: t.Description, Location: t.Location,
			DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
			CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
			Status: t.Status, Priority: t.Priority, Class: t.Class,
			URL: t.URL, Categories: t.Categories,
			RecurrenceRule: t.RecurrenceRule, Timezone: t.Timezone,
			Sequence: t.Sequence, ExDates: t.ExDates, RDates: t.RDates,
			RecurrenceID: t.RecurrenceID, Geo: t.Geo,
			DtStamp: t.DtStamp,
		})
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("import todo %q: %v", textsafe.Display(t.Summary), err))
			summary.Failed++
			continue
		}
		result.Warnings = append(result.Warnings, importTodoFields(ctx, a.Todos, saved.ID, t)...)
		summary.Todos = append(summary.Todos, saved)
		if lookupErr != nil {
			summary.NewTodos++
		} else {
			summary.UpdatedTodos++
		}
	}

	// Import journals.
	for _, j := range result.Journals {
		_, lookupErr := lookupJournal(ctx, a.Journals, j.UID, j.RecurrenceID)
		saved, err := a.Journals.UpsertByUID(ctx, journal.UpsertParams{
			UID: j.UID, CalendarID: calendarID,
			Summary: j.Summary, Description: j.Description,
			StartDate: j.StartDate, Status: j.Status, Class: j.Class,
			URL: j.URL, Categories: j.Categories,
			RecurrenceRule: j.RecurrenceRule, Timezone: j.Timezone,
			Sequence: j.Sequence, ExDates: j.ExDates, RDates: j.RDates,
			RecurrenceID: j.RecurrenceID,
			DtStamp:      j.DtStamp,
		})
		if err != nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("import journal %q: %v", textsafe.Display(j.Summary), err))
			summary.Failed++
			continue
		}
		result.Warnings = append(result.Warnings, importJournalFields(ctx, a.Journals, saved.ID, j)...)
		summary.Journals = append(summary.Journals, saved)
		if lookupErr != nil {
			summary.NewJournals++
		} else {
			summary.UpdatedJournals++
		}
	}

	summary.Warnings = append([]string(nil), result.Warnings...)
	return summary
}

// importEventFields attaches the transient child collections (alarms,
// attendees, and others) to a freshly imported event. Each failure is
// returned as a warning rather than only logged. Callers can then show
// partial child-data loss in the import summary. The function does not
// report success in silence.
//
// Replace every child collection unconditionally. A re-import of a UID
// must remove a child that the new file omits. This mirrors the CalDAV
// pull path in internal/sync (engine_persist.go).
func importEventFields(ctx context.Context, svc *event.Service, id int64, e event.Event) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import event %d: replace %s: %v", id, field, err))
		}
	}
	add("alarms", svc.ReplaceAlarms(ctx, id, e.Alarms))
	add("attendees", svc.ReplaceAttendees(ctx, id, e.Attendees))
	add("attachments", svc.ReplaceAttachments(ctx, id, e.Attachments))
	add("comments", svc.ReplaceComments(ctx, id, e.Comments))
	add("contacts", svc.ReplaceContacts(ctx, id, e.Contacts))
	add("resources", svc.ReplaceResources(ctx, id, e.Resources))
	add("relations", svc.ReplaceRelations(ctx, id, e.Relations))
	add("x-properties", svc.ReplaceXProperties(ctx, id, e.XProperties))
	return warns
}

// importTodoFields does the same work as importEventFields for todos.
func importTodoFields(ctx context.Context, svc *todo.Service, id int64, t todo.Todo) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import todo %d: replace %s: %v", id, field, err))
		}
	}
	add("alarms", svc.ReplaceAlarms(ctx, id, t.Alarms))
	add("attendees", svc.ReplaceAttendees(ctx, id, t.Attendees))
	add("attachments", svc.ReplaceAttachments(ctx, id, t.Attachments))
	add("comments", svc.ReplaceComments(ctx, id, t.Comments))
	add("contacts", svc.ReplaceContacts(ctx, id, t.Contacts))
	add("resources", svc.ReplaceResources(ctx, id, t.Resources))
	add("relations", svc.ReplaceRelations(ctx, id, t.Relations))
	add("x-properties", svc.ReplaceXProperties(ctx, id, t.XProperties))
	return warns
}

// importJournalFields does the same work as importEventFields for journals.
func importJournalFields(ctx context.Context, svc *journal.Service, id int64, j journal.Journal) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import journal %d: replace %s: %v", id, field, err))
		}
	}
	add("attendees", svc.ReplaceAttendees(ctx, id, j.Attendees))
	add("attachments", svc.ReplaceAttachments(ctx, id, j.Attachments))
	add("comments", svc.ReplaceComments(ctx, id, j.Comments))
	add("contacts", svc.ReplaceContacts(ctx, id, j.Contacts))
	add("relations", svc.ReplaceRelations(ctx, id, j.Relations))
	add("x-properties", svc.ReplaceXProperties(ctx, id, j.XProperties))
	return warns
}

// ExportSummary contains a complete single-calendar iCal export.
type ExportSummary struct {
	Events   int
	Todos    int
	Journals int
	Data     []byte
}

// ExportCalendar serializes every supported component in one calendar.
// Related alarms, attendees, attachments, and extension fields are included.
func ExportCalendar(ctx context.Context, a *app.App, calendarID int64, calendarName string) (ExportSummary, error) {
	var summary ExportSummary
	events, err := a.Events.ExportFiltered(ctx, event.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list events: %w", err)
	}
	for i := range events {
		if err := a.Events.Hydrate(ctx, &events[i]); err != nil {
			return summary, err
		}
	}
	todos, err := a.Todos.ExportFiltered(ctx, todo.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list todos: %w", err)
	}
	for i := range todos {
		if err := a.Todos.Hydrate(ctx, &todos[i]); err != nil {
			return summary, err
		}
	}
	journals, err := a.Journals.ExportFiltered(ctx, journal.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list journals: %w", err)
	}
	for i := range journals {
		if err := a.Journals.Hydrate(ctx, &journals[i]); err != nil {
			return summary, err
		}
	}

	summary.Events, summary.Todos, summary.Journals = len(events), len(todos), len(journals)
	var parts [][]byte
	if len(events) > 0 {
		data, err := ical.ExportEvents(events, calendarName)
		if err != nil {
			return summary, fmt.Errorf("export events: %w", err)
		}
		parts = append(parts, data)
	}
	if len(todos) > 0 {
		data, err := ical.ExportTodos(todos, calendarName)
		if err != nil {
			return summary, fmt.Errorf("export todos: %w", err)
		}
		parts = append(parts, data)
	}
	if len(journals) > 0 {
		data, err := ical.ExportJournals(journals, calendarName)
		if err != nil {
			return summary, fmt.Errorf("export journals: %w", err)
		}
		parts = append(parts, data)
	}
	if len(parts) == 0 {
		return summary, nil
	}
	summary.Data = parts[0]
	for _, part := range parts[1:] {
		summary.Data = ical.MergeCalendars(summary.Data, part)
	}
	return summary, nil
}

// ExportCalendarFile writes a complete single-calendar export to path.
func ExportCalendarFile(ctx context.Context, a *app.App, calendarID int64, calendarName, path string) (ExportSummary, error) {
	summary, err := ExportCalendar(ctx, a, calendarID, calendarName)
	if err != nil {
		return summary, err
	}
	if len(summary.Data) == 0 {
		return summary, fmt.Errorf("calendar has no entries to export")
	}
	if err := os.WriteFile(path, summary.Data, 0o644); err != nil {
		return summary, fmt.Errorf("write file: %w", err)
	}
	return summary, nil
}

// lookupEvent reports whether an imported event row already exists. An
// override (a non-empty recurrence_id) must match its own row, not the
// master, so the summary counts a first-time override as new.
func lookupEvent(ctx context.Context, svc *event.Service, calendarID int64, uid, recurrenceID string) (event.Event, error) {
	if recurrenceID != "" {
		return svc.GetByCalendarUIDAndRecurrenceID(ctx, calendarID, uid, recurrenceID)
	}
	return svc.GetByCalendarUID(ctx, calendarID, uid)
}

// lookupTodo reports whether an imported todo row already exists.
func lookupTodo(ctx context.Context, svc *todo.Service, uid, recurrenceID string) (todo.Todo, error) {
	if recurrenceID != "" {
		return svc.GetByUIDAndRecurrenceID(ctx, uid, recurrenceID)
	}
	return svc.GetByUID(ctx, uid)
}

// lookupJournal reports whether an imported journal row already exists.
func lookupJournal(ctx context.Context, svc *journal.Service, uid, recurrenceID string) (journal.Journal, error) {
	if recurrenceID != "" {
		return svc.GetByUIDAndRecurrenceID(ctx, uid, recurrenceID)
	}
	return svc.GetByUID(ctx, uid)
}
