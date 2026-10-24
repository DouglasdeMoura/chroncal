package icaltransfer_test

import (
	"bytes"
	"context"
	"os"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// parseCrossFixture parses data, or the fixture file when data is nil.
func parseCrossFixture(t *testing.T, data []byte) ical.ImportResult {
	t.Helper()
	if data == nil {
		var err error
		if data, err = os.ReadFile("testdata/cross_calendar.ics"); err != nil {
			t.Fatalf("read fixture: %v", err)
		}
	}
	result, err := ical.ImportFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	return result
}

// importCrossFixture imports the fixture file into calendarID.
func importCrossFixture(t *testing.T, a *app.App, calendarID int64) {
	t.Helper()
	result := parseCrossFixture(t, nil)
	if s := icaltransfer.Import(context.Background(), a, calendarID, &result); s.Failed != 0 {
		t.Fatalf("import Failed = %d, warnings %v", s.Failed, s.Warnings)
	}
}

// TestImport_RoundTripIntoAnotherLocalCalendar follows issue #882. The
// export of one local calendar goes into a second local calendar. The
// event gets its own copy there (issue #842). The todo and the journal
// cannot have a copy, because their key spans calendars. The import skips
// them with a warning that names both calendars, and the first calendar
// keeps them. Nothing fails, so a directory import stays at exit 0.
func TestImport_RoundTripIntoAnotherLocalCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	imported, err := a.Calendars.Create(ctx, "Imported", "", "")
	if err != nil {
		t.Fatalf("create Imported: %v", err)
	}
	roundTrip, err := a.Calendars.Create(ctx, "RoundTrip", "", "")
	if err != nil {
		t.Fatalf("create RoundTrip: %v", err)
	}
	importCrossFixture(t, a, imported.ID)

	exported, err := icaltransfer.ExportCalendar(ctx, a, imported.ID, imported.Name)
	if err != nil {
		t.Fatalf("ExportCalendar: %v", err)
	}
	result := parseCrossFixture(t, exported.Data)
	s := icaltransfer.Import(ctx, a, roundTrip.ID, &result)
	if s.NewEvents != 3 || s.Failed != 0 || s.SkippedTodos != 1 || s.SkippedJournals != 1 || len(s.Todos)+len(s.Journals) != 0 {
		t.Fatalf("summary = new events %d, failed %d, skipped %d/%d, todos %d, journals %d; warnings %v",
			s.NewEvents, s.Failed, s.SkippedTodos, s.SkippedJournals, len(s.Todos), len(s.Journals), s.Warnings)
	}
	for _, want := range []string{
		`skipped: imp-todo@test lives in calendar "Imported", not in calendar "RoundTrip"`,
		`skipped: imp-journal@test lives in calendar "Imported", not in calendar "RoundTrip"`,
	} {
		if !containsSubstring(s.Warnings, want) {
			t.Errorf("warnings %v lack %q", s.Warnings, want)
		}
	}
	td, err := a.Todos.GetByUID(ctx, "imp-todo@test")
	if err != nil || td.CalendarID != imported.ID || td.Sequence != 0 {
		t.Errorf("todo = %+v, %v; want it unchanged in Imported", td, err)
	}
	jr, err := a.Journals.GetByUID(ctx, "imp-journal@test")
	if err != nil || jr.CalendarID != imported.ID || jr.Sequence != 0 {
		t.Errorf("journal = %+v, %v; want it unchanged in Imported", jr, err)
	}
	if _, err := a.Events.GetByCalendarUID(ctx, imported.ID, "imp-allday@test"); err != nil {
		t.Errorf("event left Imported: %v", err)
	}
	if _, err := a.Events.GetByCalendarUID(ctx, roundTrip.ID, "imp-allday@test"); err != nil {
		t.Errorf("event has no copy in RoundTrip: %v", err)
	}
}

// TestImport_SkipsEveryRowOfASeriesOfALocalCalendar imports items whose
// UID has rows in another local calendar but no row of their own: the
// master of a series with an override there, a new override of a series,
// and a new override beside an orphan override with no master. The import
// skips each one, so no series splits across two calendars and no row
// moves.
func TestImport_SkipsEveryRowOfASeriesOfALocalCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	if err != nil {
		t.Fatalf("create Home: %v", err)
	}
	other, err := a.Calendars.Create(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("create Other: %v", err)
	}
	const rid, rid2 = "2026-11-02T00:00:00Z", "2026-11-09T00:00:00Z"
	// An orphan override of "orphan-series": no master anywhere.
	orphan, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "orphan-series", CalendarID: home.ID, Summary: "Orphan", DueDate: "2026-11-02", RecurrenceID: rid})
	if err != nil {
		t.Fatalf("seed orphan override: %v", err)
	}
	// Only an override of "todo-series" lives in Home, so the master of
	// the file has no row of its own.
	override, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "todo-series", CalendarID: home.ID, Summary: "One", DueDate: "2026-11-02", RecurrenceID: rid})
	if err != nil {
		t.Fatalf("seed todo override: %v", err)
	}
	journalMaster, err := a.Journals.UpsertByUID(ctx, journal.UpsertParams{UID: "journal-series", CalendarID: home.ID, Summary: "Series", StartDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"})
	if err != nil {
		t.Fatalf("seed journal series: %v", err)
	}
	s := icaltransfer.Import(ctx, a, other.ID, &ical.ImportResult{
		Todos: []todo.Todo{
			{UID: "todo-series", Summary: "Series", DueDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"},
			{UID: "orphan-series", Summary: "Other", DueDate: "2026-11-09", RecurrenceID: rid2},
		},
		Journals: []journal.Journal{{UID: "journal-series", Summary: "Moved", RecurrenceID: rid}},
	})
	if s.Failed != 0 || s.SkippedTodos != 2 || s.SkippedJournals != 1 {
		t.Fatalf("summary = failed %d, skipped %d/%d, warnings %v", s.Failed, s.SkippedTodos, s.SkippedJournals, s.Warnings)
	}
	if !containsSubstring(s.Warnings, `skipped: the series of todo-series lives in calendar "Home", not in calendar "Other"`) {
		t.Errorf("warnings %v lack the series skip", s.Warnings)
	}
	for _, id := range []int64{orphan.ID, override.ID} {
		if got, err := a.Todos.Get(ctx, id); err != nil || got.CalendarID != home.ID {
			t.Errorf("todo %d = %+v, %v; want it in Home", id, got, err)
		}
	}
	if got, err := a.Journals.Get(ctx, journalMaster.ID); err != nil || got.CalendarID != home.ID {
		t.Errorf("journal master = %+v, %v; want it in Home", got, err)
	}
	for _, uid := range []string{"todo-series", "orphan-series"} {
		if n, err := a.Todos.ListOverridesByUID(ctx, uid); err != nil || len(n) != 1 {
			t.Errorf("overrides of %s = %d, %v; want the one row in Home", uid, len(n), err)
		}
	}
	if _, err := a.Journals.GetByUIDAndRecurrenceID(ctx, "journal-series", rid); err == nil {
		t.Error("the journal override landed")
	}
}

// TestImport_RefusesAnOverrideOfASeriesInASyncedCalendar imports the
// override of a todo series whose master lives in another synced calendar.
// No override row exists yet. A new row would split the series, and the
// import holds no lock of that calendar, so it refuses the override.
func TestImport_RefusesAnOverrideOfASeriesInASyncedCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	calA, calB := syncedPair(t, a)
	const rid = "2026-11-02T00:00:00Z"
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "todo-series", CalendarID: calB.ID, Summary: "Series", DueDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"}); err != nil {
		t.Fatalf("seed todo series: %v", err)
	}
	s := icaltransfer.Import(ctx, a, calA.ID, &ical.ImportResult{
		Todos: []todo.Todo{{UID: "todo-series", Summary: "Moved", RecurrenceID: rid}},
	})
	if s.Failed != 1 || !containsSubstring(s.Warnings, `the series of todo-series lives in calendar "b"; import the item into that calendar`) {
		t.Fatalf("summary = failed %d, warnings %v; want the series refusal", s.Failed, s.Warnings)
	}
	if _, err := a.Todos.GetByUIDAndRecurrenceID(ctx, "todo-series", rid); err == nil {
		t.Error("the todo override landed")
	}
}

// TestImport_EventOfAnotherCalendarStillCopies keeps the rule of issue
// #842: an event key is per calendar, so the import makes a copy.
func TestImport_EventOfAnotherCalendarStillCopies(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	first, err := a.Calendars.Create(ctx, "First", "", "")
	if err != nil {
		t.Fatalf("create First: %v", err)
	}
	second, err := a.Calendars.Create(ctx, "Second", "", "")
	if err != nil {
		t.Fatalf("create Second: %v", err)
	}
	importCrossFixture(t, a, first.ID)
	result := parseCrossFixture(t, nil)
	s := icaltransfer.Import(ctx, a, second.ID, &ical.ImportResult{Events: result.Events})
	if s.Failed != 0 || s.NewEvents != len(result.Events) {
		t.Fatalf("summary = failed %d, new %d, warnings %v", s.Failed, s.NewEvents, s.Warnings)
	}
}

// TestImport_TodoOfAReadOnlyCalendarFailsAlone imports a file whose todo
// lives in a read-only calendar. The import never writes that calendar, so
// the destination check passes. The todo fails with its own warning, and
// the events of the file still land.
func TestImport_TodoOfAReadOnlyCalendarFailsAlone(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	calA, calB := syncedPair(t, a)
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "imp-todo@test", CalendarID: calB.ID, Summary: "In B"}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if _, err := a.DB.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read' WHERE id = ?`, calB.ID); err != nil {
		t.Fatalf("make B read-only: %v", err)
	}
	result := parseCrossFixture(t, nil)
	if err := icaltransfer.ValidateDestination(ctx, a, calA.ID, icaltransfer.Preview{Result: result}); err != nil {
		t.Fatalf("ValidateDestination: %v", err)
	}
	s := icaltransfer.Import(ctx, a, calA.ID, &result)
	if s.Failed != 1 || s.NewEvents != 3 || !containsSubstring(s.Warnings, `imp-todo@test lives in the read-only calendar "b"`) {
		t.Fatalf("summary = failed %d, new events %d, warnings %v", s.Failed, s.NewEvents, s.Warnings)
	}
}

// TestImport_SkipsAnOverrideOfASeriesInTheTrashOfALocalCalendar keeps a
// series in the trash where it is. The import skips the override, so a
// restore in that calendar still finds every row.
func TestImport_SkipsAnOverrideOfASeriesInTheTrashOfALocalCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	if err != nil {
		t.Fatalf("create Home: %v", err)
	}
	other, err := a.Calendars.Create(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("create Other: %v", err)
	}
	master, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "todo-series", CalendarID: home.ID, Summary: "Series", DueDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"})
	if err != nil {
		t.Fatalf("seed todo series: %v", err)
	}
	if err := a.Todos.DeleteSeries(ctx, "todo-series"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	s := icaltransfer.Import(ctx, a, other.ID, &ical.ImportResult{
		Todos: []todo.Todo{{UID: "todo-series", Summary: "Moved", RecurrenceID: "2026-11-02T00:00:00Z"}},
	})
	if s.Failed != 0 || s.SkippedTodos != 1 || !containsSubstring(s.Warnings, `skipped: the series of todo-series is in the trash of calendar "Home"`) {
		t.Fatalf("summary = failed %d, skipped %d, warnings %v", s.Failed, s.SkippedTodos, s.Warnings)
	}
	if got, err := a.Todos.GetIncludingDeleted(ctx, master.ID); err != nil || got.CalendarID != home.ID || got.DeletedAt == nil {
		t.Errorf("master = %+v, %v; want it in the trash of Home", got, err)
	}
}

// TestImport_OverrideFollowsTheMasterOfTheImportCalendar imports a new
// override into the calendar of its master while a stray override of the
// UID lives in another calendar. The series is in the import calendar, so
// the override lands there.
func TestImport_OverrideFollowsTheMasterOfTheImportCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	if err != nil {
		t.Fatalf("create Home: %v", err)
	}
	other, err := a.Calendars.Create(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("create Other: %v", err)
	}
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "todo-series", CalendarID: home.ID, Summary: "Series", DueDate: "2026-10-26", RecurrenceRule: "FREQ=WEEKLY"}); err != nil {
		t.Fatalf("seed master: %v", err)
	}
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "todo-series", CalendarID: other.ID, Summary: "Stray", DueDate: "2026-11-02", RecurrenceID: "2026-11-02T00:00:00Z"}); err != nil {
		t.Fatalf("seed stray override: %v", err)
	}
	s := icaltransfer.Import(ctx, a, home.ID, &ical.ImportResult{
		Todos: []todo.Todo{{UID: "todo-series", Summary: "New", DueDate: "2026-11-09", RecurrenceID: "2026-11-09T00:00:00Z"}},
	})
	if s.Failed != 0 || s.SkippedTodos != 0 || s.NewTodos != 1 {
		t.Fatalf("summary = failed %d, skipped %d, new %d, warnings %v", s.Failed, s.SkippedTodos, s.NewTodos, s.Warnings)
	}
}

// TestImport_SkipsATodoInTheTrashOfALocalCalendar names the trash in the
// skip warning, so the user knows where to find the item.
func TestImport_SkipsATodoInTheTrashOfALocalCalendar(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	if err != nil {
		t.Fatalf("create Home: %v", err)
	}
	other, err := a.Calendars.Create(ctx, "Other", "", "")
	if err != nil {
		t.Fatalf("create Other: %v", err)
	}
	td, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "gone", CalendarID: home.ID, Summary: "Gone"})
	if err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := a.Todos.Delete(ctx, td.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	s := icaltransfer.Import(ctx, a, other.ID, &ical.ImportResult{Todos: []todo.Todo{{UID: "gone", Summary: "Back"}}})
	if s.Failed != 0 || s.SkippedTodos != 1 || !containsSubstring(s.Warnings, `skipped: gone is in the trash of calendar "Home", not in calendar "Other"`) {
		t.Fatalf("summary = failed %d, skipped %d, warnings %v", s.Failed, s.SkippedTodos, s.Warnings)
	}
}

// TestImport_SkipsALocalTodoWithAPendingMove skips a todo of another local
// calendar even when its UID has a pending move. A skip writes nothing, so
// the item never counts as failed.
func TestImport_SkipsALocalTodoWithAPendingMove(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	calA, calB := syncedPair(t, a)
	local, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "local"})
	if err != nil {
		t.Fatalf("CreateCalendar local: %v", err)
	}
	if _, err := a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "moving", CalendarID: local.ID, Summary: "Local"}); err != nil {
		t.Fatalf("seed todo: %v", err)
	}
	if err := a.Queries.CreateTombstone(ctx, storage.CreateTombstoneParams{CalendarID: calB.ID, Uid: "moving", RemoteUrl: "/cal/b/moving.ics", MovedToCalendarID: &local.ID}); err != nil {
		t.Fatalf("CreateTombstone: %v", err)
	}
	s := icaltransfer.Import(ctx, a, calA.ID, &ical.ImportResult{Todos: []todo.Todo{{UID: "moving", Summary: "Imported"}}})
	if s.Failed != 0 || s.SkippedTodos != 1 {
		t.Fatalf("summary = failed %d, skipped %d, warnings %v", s.Failed, s.SkippedTodos, s.Warnings)
	}
}
