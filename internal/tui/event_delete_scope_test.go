package tui

import (
	"context"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestRunEventDeleteScope_LegacyOverrideNamesMasterCalendar: a scope delete
// of a legacy override writes the calendar of the master. The message names
// that calendar, so the push sends the changed series.
func TestRunEventDeleteScope_LegacyOverrideNamesMasterCalendar(t *testing.T) {
	_, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, cals)
	masterCal := cals[0].ID
	res, err := a.DB.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	require.NoError(t, err)
	legacyCal, err := res.LastInsertId()
	require.NoError(t, err)

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-legacy", CalendarID: masterCal, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)
	legacy, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: legacyCal, Title: "Legacy",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	require.NoError(t, err)

	msg := runEventDeleteScope(ctx, a, legacy, 0)
	require.NoError(t, msg.err)
	require.Equal(t, masterCal, msg.calendarID, "the message names the calendar of the master")
}

// TestRunEventDelete_LegacyOverrideNamesMasterCalendar: a plain delete of
// a legacy override writes the EXDATE on its master. The message names the
// calendar of the master.
func TestRunEventDelete_LegacyOverrideNamesMasterCalendar(t *testing.T) {
	_, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	masterCal := cals[0].ID
	res, err := a.DB.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Legacy')`)
	require.NoError(t, err)
	legacyCal, err := res.LastInsertId()
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-plain-legacy", CalendarID: masterCal, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)
	legacy, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: legacyCal, Title: "Legacy",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	require.NoError(t, err)

	msg := runEventDelete(ctx, a, legacy)
	require.NoError(t, msg.err)
	require.Equal(t, masterCal, msg.calendarID, "the message names the calendar of the master")
}

// TestUndo_AlreadyRestoredEntryLeavesTheStack: the top undo entry names a
// delete that another path restored already. The first press of u drops
// that entry and shows "already restored". The second press restores the
// entry below it.
func TestUndo_AlreadyRestoredEntryLeavesTheStack(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	metas := make([]event.UndoMeta, 0, 2)
	for _, title := range []string{"First", "Second"} {
		ev, err := a.Events.Create(ctx, event.CreateParams{CalendarID: cals[0].ID, Title: title, StartTime: start, EndTime: start.Add(time.Hour)})
		require.NoError(t, err)
		meta, err := a.Events.DeleteWithUndo(ctx, ev.ID)
		require.NoError(t, err)
		m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
		metas = append(metas, meta)
	}
	// Another path restores the second delete.
	require.NoError(t, a.Events.RestoreUndo(ctx, metas[1]))

	m.focus = focusCalendar
	undo := tea.KeyPressMsg{Code: 'u', Text: "u"}
	press := func() eventRestoredMsg {
		t.Helper()
		next, cmd, handled := m.interceptGlobalKeys(undo)
		require.True(t, handled)
		require.NotNil(t, cmd)
		msg, ok := cmd().(eventRestoredMsg)
		require.True(t, ok)
		updated, _ := next.Update(msg)
		m = updated.(Model)
		return msg
	}
	first := press()
	require.ErrorIs(t, first.err, event.ErrAlreadyRestored)
	require.Equal(t, 1, m.undoStack.Len(), "the restored entry leaves the stack")
	require.Equal(t, ToastFailed, m.toast.State())
	require.Equal(t, "already restored", m.toast.reason)

	second := press()
	require.NoError(t, second.err)
	require.Equal(t, 0, m.undoStack.Len())
	got, err := a.Events.GetIncludingDeleted(ctx, metas[0].MasterID)
	require.NoError(t, err)
	require.Nil(t, got.DeletedAt, "the second press restores the first delete")
}

// TestUndo_TruncationWithMasterInTrashKeepsEntry: the undo of a truncation
// whose master is in the trash keeps the entry and asks for the restore of
// the series first.
func TestUndo_TruncationWithMasterInTrashKeepsEntry(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	ev, err := a.Events.Create(ctx, event.CreateParams{
		CalendarID: cals[0].ID, Title: "Series", StartTime: start, EndTime: start.Add(time.Hour),
		RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	require.NoError(t, err)
	meta, err := a.Events.DeleteFromInstanceWithUndo(ctx, cals[0].ID, ev.UID, start.Add(48*time.Hour))
	require.NoError(t, err)
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	require.NoError(t, a.Events.DeleteSeries(ctx, cals[0].ID, ev.UID))

	m.focus = focusCalendar
	next, cmd, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.True(t, handled)
	msg, ok := cmd().(eventRestoredMsg)
	require.True(t, ok)
	require.ErrorIs(t, msg.err, event.ErrMasterInTrash)
	updated, _ := next.Update(msg)
	m = updated.(Model)
	require.Equal(t, 1, m.undoStack.Len(), "the entry stays for a later undo")
	require.Equal(t, "restore the series first", m.toast.reason)
}

// TestUndo_PurgedRowsLeaveTheStack: the undo of a delete whose row a purge
// took has nothing to restore. The entry leaves the stack with the message
// "nothing to restore".
func TestUndo_PurgedRowsLeaveTheStack(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	ev, err := a.Events.Create(ctx, event.CreateParams{CalendarID: cals[0].ID, Title: "Gone", StartTime: start, EndTime: start.Add(time.Hour)})
	require.NoError(t, err)
	meta, err := a.Events.DeleteWithUndo(ctx, ev.ID)
	require.NoError(t, err)
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	require.NoError(t, a.Events.PurgeByID(ctx, ev.ID))

	m.focus = focusCalendar
	next, cmd, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.True(t, handled)
	msg, ok := cmd().(eventRestoredMsg)
	require.True(t, ok)
	require.ErrorIs(t, msg.err, event.ErrNotDeleted)
	require.NotErrorIs(t, msg.err, event.ErrAlreadyRestored)
	updated, _ := next.Update(msg)
	m = updated.(Model)
	require.Equal(t, 0, m.undoStack.Len())
	require.Equal(t, "nothing to restore", m.toast.reason)
}
