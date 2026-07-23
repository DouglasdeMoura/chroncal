// Package icaltransfer implements the parse / validate / import pipeline
// behind the `chroncal ical import` CLI. The CLI used to inline this
// behavior; centralizing it here lets the TUI and other internal callers
// drive the same flow without re-implementing the UID-upsert, capability,
// and warning semantics.
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
// messages and capability checks; callers should reuse them when reporting
// problems instead of string literals.
const (
	FamilyEvent   = "VEVENT"
	FamilyTodo    = "VTODO"
	FamilyJournal = "VJOURNAL"
)

// Preview is the parsed view of an .ics file before any row is written. It
// wraps the underlying ical.ImportResult with convenient per-family counts
// and a copy of the warnings emitted at parse time. The warnings are copied
// so the preview stands on its own even after Import mutates the result.
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
// enough of the imported rows for the CLI to render them in JSON or text
// form without re-reading the database.
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
// Open and parse errors are wrapped so callers can surface them uniformly
// (matching the CLI's prior "open file" / "import" wrapping).
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

// ValidateDestination validates every present component family and every
// cross-calendar UID source row before the first write. This prevents both
// partial mixed imports and UID upserts that would move data out of a
// read-only remote collection. Each present family must be writable at the
// destination, and any pre-existing row matched by UID must already live in
// a calendar the caller can write to.
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
	for _, imported := range result.Events {
		existing, err := a.Queries.GetEventByUIDAndRecurrenceID(ctx, storage.GetEventByUIDAndRecurrenceIDParams{
			Uid: imported.UID, RecurrenceID: imported.RecurrenceID,
		})
		if err == nil {
			if err := checkSource(existing.CalendarID, FamilyEvent, imported.UID); err != nil {
				return err
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check existing %s UID %q: %w", FamilyEvent, imported.UID, err)
		}
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
// calendarID. A failure on any single component is recorded in result's
// warnings (and Summary.Failed) and the loop moves on, so one bad item no
// longer aborts the run and discards the components that follow it. Child
// collections (alarms, attendees, ...) that fail to attach are likewise
// surfaced as warnings rather than silently dropped, so the import never
// reports a clean success while quietly losing data. The passed result is
// mutated to accumulate warnings, mirroring the legacy CLI behavior.
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
		_, lookupErr := a.Events.GetByUID(ctx, e.UID)
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
		_, lookupErr := a.Todos.GetByUID(ctx, t.UID)
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
		_, lookupErr := a.Journals.GetByUID(ctx, j.UID)
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
// attendees, ...) to a freshly imported event. Each failure is returned as a
// warning rather than only logged, so callers can surface partially-dropped
// child data in the import summary instead of silently reporting success.
func importEventFields(ctx context.Context, svc *event.Service, id int64, e event.Event) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import event %d: replace %s: %v", id, field, err))
		}
	}
	if len(e.Alarms) > 0 {
		add("alarms", svc.ReplaceAlarms(ctx, id, e.Alarms))
	}
	if len(e.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, e.Attendees))
	}
	if len(e.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, e.Attachments))
	}
	if len(e.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, e.Comments))
	}
	if len(e.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, e.Contacts))
	}
	if len(e.Resources) > 0 {
		add("resources", svc.ReplaceResources(ctx, id, e.Resources))
	}
	if len(e.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, e.Relations))
	}
	if len(e.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, e.XProperties))
	}
	return warns
}

// importTodoFields mirrors importEventFields for todos.
func importTodoFields(ctx context.Context, svc *todo.Service, id int64, t todo.Todo) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import todo %d: replace %s: %v", id, field, err))
		}
	}
	if len(t.Alarms) > 0 {
		add("alarms", svc.ReplaceAlarms(ctx, id, t.Alarms))
	}
	if len(t.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, t.Attendees))
	}
	if len(t.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, t.Attachments))
	}
	if len(t.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, t.Comments))
	}
	if len(t.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, t.Contacts))
	}
	if len(t.Resources) > 0 {
		add("resources", svc.ReplaceResources(ctx, id, t.Resources))
	}
	if len(t.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, t.Relations))
	}
	if len(t.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, t.XProperties))
	}
	return warns
}

// importJournalFields mirrors importEventFields for journals.
func importJournalFields(ctx context.Context, svc *journal.Service, id int64, j journal.Journal) []string {
	var warns []string
	add := func(field string, err error) {
		if err != nil {
			warns = append(warns, fmt.Sprintf("import journal %d: replace %s: %v", id, field, err))
		}
	}
	if len(j.Attendees) > 0 {
		add("attendees", svc.ReplaceAttendees(ctx, id, j.Attendees))
	}
	if len(j.Attachments) > 0 {
		add("attachments", svc.ReplaceAttachments(ctx, id, j.Attachments))
	}
	if len(j.Comments) > 0 {
		add("comments", svc.ReplaceComments(ctx, id, j.Comments))
	}
	if len(j.Contacts) > 0 {
		add("contacts", svc.ReplaceContacts(ctx, id, j.Contacts))
	}
	if len(j.Relations) > 0 {
		add("relations", svc.ReplaceRelations(ctx, id, j.Relations))
	}
	if len(j.XProperties) > 0 {
		add("x-properties", svc.ReplaceXProperties(ctx, id, j.XProperties))
	}
	return warns
}

// ExportSummary contains a complete single-calendar iCal export.
type ExportSummary struct {
	Events   int
	Todos    int
	Journals int
	Data     []byte
}

// ExportCalendar serializes every supported component in one calendar,
// including its related alarms, attendees, attachments, and extension fields.
func ExportCalendar(ctx context.Context, a *app.App, calendarID int64, calendarName string) (ExportSummary, error) {
	var summary ExportSummary
	events, err := a.Events.ExportFiltered(ctx, event.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list events: %w", err)
	}
	for i := range events {
		if err := populateExportEvent(ctx, a.Events, &events[i]); err != nil {
			return summary, err
		}
	}
	todos, err := a.Todos.ExportFiltered(ctx, todo.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list todos: %w", err)
	}
	for i := range todos {
		if err := populateExportTodo(ctx, a.Todos, &todos[i]); err != nil {
			return summary, err
		}
	}
	journals, err := a.Journals.ExportFiltered(ctx, journal.ExportParams{CalendarID: calendarID})
	if err != nil {
		return summary, fmt.Errorf("list journals: %w", err)
	}
	for i := range journals {
		if err := populateExportJournal(ctx, a.Journals, &journals[i]); err != nil {
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

func populateExportEvent(ctx context.Context, svc *event.Service, item *event.Event) error {
	var err error
	if item.Alarms, err = svc.ListAlarms(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d alarms: %w", item.ID, err)
	}
	if item.Attendees, err = svc.ListAttendees(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d attendees: %w", item.ID, err)
	}
	if item.Attachments, err = svc.ListAttachments(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d attachments: %w", item.ID, err)
	}
	if item.Comments, err = svc.ListComments(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d comments: %w", item.ID, err)
	}
	if item.Contacts, err = svc.ListContacts(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d contacts: %w", item.ID, err)
	}
	if item.Resources, err = svc.ListResources(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d resources: %w", item.ID, err)
	}
	if item.Relations, err = svc.ListRelations(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d relations: %w", item.ID, err)
	}
	if item.XProperties, err = svc.ListXProperties(ctx, item.ID); err != nil {
		return fmt.Errorf("event %d x-properties: %w", item.ID, err)
	}
	return nil
}

func populateExportTodo(ctx context.Context, svc *todo.Service, item *todo.Todo) error {
	var err error
	if item.Alarms, err = svc.ListAlarms(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d alarms: %w", item.ID, err)
	}
	if item.Attendees, err = svc.ListAttendees(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d attendees: %w", item.ID, err)
	}
	if item.Attachments, err = svc.ListAttachments(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d attachments: %w", item.ID, err)
	}
	if item.Comments, err = svc.ListComments(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d comments: %w", item.ID, err)
	}
	if item.Contacts, err = svc.ListContacts(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d contacts: %w", item.ID, err)
	}
	if item.Resources, err = svc.ListResources(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d resources: %w", item.ID, err)
	}
	if item.Relations, err = svc.ListRelations(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d relations: %w", item.ID, err)
	}
	if item.XProperties, err = svc.ListXProperties(ctx, item.ID); err != nil {
		return fmt.Errorf("todo %d x-properties: %w", item.ID, err)
	}
	return nil
}

func populateExportJournal(ctx context.Context, svc *journal.Service, item *journal.Journal) error {
	var err error
	if item.Attendees, err = svc.ListAttendees(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d attendees: %w", item.ID, err)
	}
	if item.Attachments, err = svc.ListAttachments(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d attachments: %w", item.ID, err)
	}
	if item.Comments, err = svc.ListComments(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d comments: %w", item.ID, err)
	}
	if item.Contacts, err = svc.ListContacts(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d contacts: %w", item.ID, err)
	}
	if item.Relations, err = svc.ListRelations(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d relations: %w", item.ID, err)
	}
	if item.XProperties, err = svc.ListXProperties(ctx, item.ID); err != nil {
		return fmt.Errorf("journal %d x-properties: %w", item.ID, err)
	}
	return nil
}
