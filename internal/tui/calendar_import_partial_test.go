package tui

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestCalendarImportIntoNewCalendarKeepsItsItemsOnARefusal imports a file
// into a new calendar. The todo of the file lives in a synced calendar, so
// the import refuses it (issue #882). The new calendar and its event stay,
// and the status line counts the failed item and shows its warning.
func TestCalendarImportIntoNewCalendarKeepsItsItemsOnARefusal(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()

	account, err := a.Queries.CreateAccount(ctx, storage.CreateAccountParams{
		Name: "acct", ServerUrl: "https://example.com", AuthType: "basic", Username: "u",
	})
	require.NoError(t, err)
	synced, err := a.Queries.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Synced"})
	require.NoError(t, err)
	require.NoError(t, a.Queries.LinkCalendarToAccount(ctx, storage.LinkCalendarToAccountParams{
		ID: synced.ID, AccountID: &account.ID, RemoteUrl: storage.StringToNullable("/cal/synced/"),
	}))
	_, err = a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "shared-todo", CalendarID: synced.ID, Summary: "In Synced"})
	require.NoError(t, err)

	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	preview := icaltransfer.Preview{
		Events: 1, Todos: 1,
		Result: ical.ImportResult{
			Events: []event.Event{{UID: "new-event", Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour)}},
			Todos:  []todo.Todo{{UID: "shared-todo", Summary: "Imported todo"}},
		},
	}
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(harnessWidth, harnessHeight).
		OpenImport(4)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 4

	m, msg := step(t, m, CalendarImportRequestedMsg{Generation: 4, Path: "/tmp/round.ics", Preview: preview, NewName: "RoundTrip", NewColor: "#336699"})
	finished, ok := msg.(calendarImportFinishedMsg)
	require.True(t, ok, "message = %T", msg)
	require.NoError(t, finished.Err)
	require.NotZero(t, finished.CalendarID)

	got, err := a.Events.GetByCalendarUID(ctx, finished.CalendarID, "new-event")
	require.NoError(t, err)
	require.Equal(t, "Imported", got.Title)
	td, err := a.Todos.GetByUID(ctx, "shared-todo")
	require.NoError(t, err)
	require.Equal(t, synced.ID, td.CalendarID)

	updated, _ := m.Update(finished)
	m = updated.(Model)
	require.Contains(t, m.syncStatus, "1 failed")
	require.Contains(t, m.syncStatus, `shared-todo lives in calendar "Synced"`)
}

// TestCalendarImportIntoNewCalendarSkipsATodoOfALocalCalendar imports a
// file into a new calendar while its todo lives in a local calendar. The
// import skips the todo (issue #882), nothing fails, and the status line
// shows the skip.
func TestCalendarImportIntoNewCalendarSkipsATodoOfALocalCalendar(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	require.NoError(t, err)
	_, err = a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "shared-todo", CalendarID: home.ID, Summary: "In Home"})
	require.NoError(t, err)

	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	preview := icaltransfer.Preview{
		Events: 1, Todos: 1,
		Result: ical.ImportResult{
			Events: []event.Event{{UID: "new-event", Title: "Imported", StartTime: start, EndTime: start.Add(time.Hour)}},
			Todos:  []todo.Todo{{UID: "shared-todo", Summary: "Imported todo"}},
		},
	}
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(harnessWidth, harnessHeight).
		OpenImport(5)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 5

	m, msg := step(t, m, CalendarImportRequestedMsg{Generation: 5, Path: "/tmp/round.ics", Preview: preview, NewName: "RoundTrip", NewColor: "#336699"})
	finished, ok := msg.(calendarImportFinishedMsg)
	require.True(t, ok, "message = %T", msg)
	require.NoError(t, finished.Err)
	require.Zero(t, finished.Summary.Failed)
	require.Equal(t, 1, finished.Summary.SkippedTodos)
	_, err = a.Events.GetByCalendarUID(ctx, finished.CalendarID, "new-event")
	require.NoError(t, err)
	td, err := a.Todos.GetByUID(ctx, "shared-todo")
	require.NoError(t, err)
	require.Equal(t, home.ID, td.CalendarID)

	updated, _ := m.Update(finished)
	m = updated.(Model)
	require.Contains(t, m.syncStatus, `skipped: shared-todo lives in calendar "Home"`)
}

// TestCalendarImportIntoNewCalendarThatLandsNothingRemovesIt imports a file
// whose only todo lives in a local calendar. The import skips it, nothing
// lands, so the new empty calendar goes, and the error names the skip.
func TestCalendarImportIntoNewCalendarThatLandsNothingRemovesIt(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	home, err := a.Calendars.Create(ctx, "Home", "", "")
	require.NoError(t, err)
	_, err = a.Todos.UpsertByUID(ctx, todo.UpsertParams{UID: "shared-todo", CalendarID: home.ID, Summary: "In Home"})
	require.NoError(t, err)
	before, err := a.Calendars.List(ctx)
	require.NoError(t, err)

	preview := icaltransfer.Preview{Todos: 1, Result: ical.ImportResult{Todos: []todo.Todo{{UID: "shared-todo", Summary: "Imported todo"}}}}
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(harnessWidth, harnessHeight).
		OpenImport(6)
	m.calendarManagerOpen = true
	m.calendarTransferGeneration = 6

	_, msg := step(t, m, CalendarImportRequestedMsg{Generation: 6, Path: "/tmp/only-todo.ics", Preview: preview, NewName: "Empty", NewColor: "#336699"})
	finished, ok := msg.(calendarImportFinishedMsg)
	require.True(t, ok, "message = %T", msg)
	require.Error(t, finished.Err)
	require.Contains(t, finished.Err.Error(), `shared-todo lives in calendar "Home"`)
	after, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	require.Len(t, after, len(before))
}
