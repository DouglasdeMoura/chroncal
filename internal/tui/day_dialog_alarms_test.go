package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// TestDayDialogShowsAlarmsOfSelectedEvent guards issue #912. The rows that
// the TUI loads carry their alarms, so the detail pane of the day dialog
// lists them.
func TestDayDialogShowsAlarmsOfSelectedEvent(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	// A fixed day away from a DST change. The month view loads its range.
	day := time.Date(2026, 5, 13, 0, 0, 0, 0, time.Local)
	m.viewMode = viewMonth
	m.calendar.cursor = day
	m.calendar.month = time.Date(2026, 5, 1, 0, 0, 0, 0, time.Local)
	ev, err := a.Events.Create(ctx, event.CreateParams{CalendarID: cals[0].ID, Title: "Standup",
		StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)})
	require.NoError(t, err)
	require.NoError(t, a.Events.ReplaceAlarms(ctx, ev.ID, []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT2M", Related: "START"}}))
	m, _ = step(t, m, m.loadEvents()())

	m, _ = step(t, m, CalendarDaySelectedMsg{Day: day})
	require.True(t, m.dialogOpen)
	plain := ansi.Strip(m.dialog.shell.View())
	require.True(t, strings.Contains(plain, "2 min"), "the detail pane does not list the alarm:\n%s", plain)

	// A duplicate from the day dialog copies no alarms, as before.
	sel, ok := m.dialog.selectedEvent()
	require.True(t, ok)
	require.NotEmpty(t, sel.Alarms)
	m, _ = step(t, m, EventDuplicateMsg{Event: sel})
	require.True(t, m.formOpen)
	require.Empty(t, m.form.alarms, "the duplicate copied the alarm summary")
}
