package tui

import (
	"context"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestUndoWorksInDayDialog guards item 1 of issue #889. The day dialog
// shows the undo toast of its delete, so the undo key works there. The
// restored event comes back in the open dialog.
func TestUndoWorksInDayDialog(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	now := time.Now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	ev, err := a.Events.Create(ctx, event.CreateParams{CalendarID: cals[0].ID, Title: "Standup",
		StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)})
	require.NoError(t, err)
	meta, err := a.Events.DeleteWithUndo(ctx, ev.ID)
	require.NoError(t, err)
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	m.focus = focusCalendar
	m.dialog = m.newDayDialog(day, nil)
	m.dialogOpen = true

	next, cmd, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.True(t, handled, "u in the day dialog does nothing")
	require.NotNil(t, cmd)
	msg, ok := cmd().(eventRestoredMsg)
	require.True(t, ok)
	require.NoError(t, msg.err)
	// The fake clock of the bubble ends the dismiss tick of the toast.
	synctest.Test(t, func(t *testing.T) { m, _ = step(t, next, msg) })
	require.True(t, m.dialogOpen)
	require.Len(t, m.dialog.events, 1, "the restored event is not in the day dialog")

	// A confirm over the day dialog still owns the key.
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	m.confirmOpen = true
	_, _, handled = m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.False(t, handled, "u under a confirm dialog runs the undo")
}

// TestSyncWithNoTargetsReloadsRows guards item 3 of issue #889. The sync
// key reloads the rows when no calendar has a connection.
func TestSyncWithNoTargetsReloadsRows(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	now := time.Now()
	_, err = a.Events.Create(ctx, event.CreateParams{CalendarID: cals[0].ID, Title: "From the CLI",
		StartTime: now, EndTime: now.Add(time.Hour)})
	require.NoError(t, err)

	next, cmd := m.handleSyncAllPlanned(syncAllPlannedMsg{})
	m = next.(Model)
	require.Equal(t, "No connected calendars to sync", m.syncStatus)
	require.NotNil(t, cmd)
	// The fake clock of the bubble ends the expiry tick of the status.
	batch, ok := cmd().(tea.BatchMsg)
	require.True(t, ok)
	synctest.Test(t, func(t *testing.T) { m, _ = drainBatch(t, m, batch) })
	found := false
	for _, e := range m.events {
		if e.Title == "From the CLI" {
			found = true
		}
	}
	require.True(t, found, "the sync key did not reload the rows")
}

// TestCalendarSaveNormalizesColorAndSelectsNewCalendar guards items 5 and
// 6 of issue #889. A new calendar stores an uppercase color, as the CLI
// does, and the manager selects it after the save.
func TestCalendarSaveNormalizesColorAndSelectsNewCalendar(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	mid, err := a.Calendars.Create(ctx, "Mid", "#112233", "")
	require.NoError(t, err)
	m, _ = step(t, m, m.loadCalendars()())
	m.calendarManager = NewCalendarManagerModel(m.calendars, m.hiddenCalendars, newThemedHelp(m.theme)).
		SetSize(m.width, m.height).SelectCalendar(mid.ID).
		OpenCalendar(CalendarDialogParams{ManagerEmbedded: true})
	m.calendarManagerOpen = true

	updated, cmd := m.Update(CalendarSavedMsg{Name: "Beta", Color: "#a6e3a1"})
	m = updated.(Model)
	m, _ = step(t, m, cmd())

	id, ok := m.calendarManager.selectedID()
	require.True(t, ok)
	require.Equal(t, "Beta", m.calendars[id].Name, "the manager does not select the new calendar")
	require.Equal(t, "#A6E3A1", m.calendars[id].Color)

	// An edit that keeps the color in another case keeps the stored form.
	_, err = a.DB.ExecContext(ctx, `UPDATE calendars SET color = '#abcdef' WHERE id = ?`, mid.ID)
	require.NoError(t, err)
	m, _ = step(t, m, m.loadCalendars()())
	updated, cmd = m.Update(CalendarSavedMsg{ID: mid.ID, Name: "Mid", Color: "#ABCDEF"})
	m = updated.(Model)
	_, _ = step(t, m, cmd())
	got, err := a.Calendars.Get(ctx, mid.ID)
	require.NoError(t, err)
	require.Equal(t, "#abcdef", got.Color)
}

// TestBulkToastShowsCountWithoutQuotes guards item 8 of issue #889.
func TestBulkToastShowsCountWithoutQuotes(t *testing.T) {
	toast := NewToastModel(ActiveTheme())
	toast.RestoredCount("2 items", 2)
	require.Contains(t, ansi.Strip(toast.View()), "Restored 2 items")
	require.NotContains(t, toast.View(), `"`)

	toast.PurgedCount("3 items", 3)
	require.Contains(t, ansi.Strip(toast.View()), "Purged 3 items forever")

	toast.Restored("Standup")
	require.True(t, strings.Contains(ansi.Strip(toast.View()), `Restored "Standup"`))
}
