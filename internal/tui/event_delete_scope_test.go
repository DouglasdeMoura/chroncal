package tui

import (
	"context"
	"fmt"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
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

// TestUndo_TruncationWithMasterInTrashLeavesTheStack: the undo of a
// truncation whose master is in the trash drops the entry, so it never
// blocks the older entries. The message asks for the restore of the series
// first and names the trash.
func TestUndo_TruncationWithMasterInTrashLeavesTheStack(t *testing.T) {
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
	require.Equal(t, 0, m.undoStack.Len(), "the entry leaves the stack")
	require.Equal(t, "restore the series first, then restore this from the trash", m.toast.reason)
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

// TestUndo_PurgedMasterRefusalLeavesTheStack: the service refuses the undo
// of a series delete whose master a purge took, beside a live master of the
// UID in another calendar. The entry leaves the stack.
func TestUndo_PurgedMasterRefusalLeavesTheStack(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-purged", CalendarID: cals[0].ID, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)
	second := start.Add(24 * time.Hour)
	_, err = a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: cals[0].ID, Title: "Late",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	})
	require.NoError(t, err)
	meta, err := a.Events.DeleteSeriesWithUndo(ctx, cals[0].ID, master.UID)
	require.NoError(t, err)
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	_, err = a.DB.ExecContext(ctx, `DELETE FROM events WHERE id = ?`, master.ID)
	require.NoError(t, err)
	res, err := a.DB.ExecContext(ctx, `INSERT INTO calendars (name) VALUES ('Other')`)
	require.NoError(t, err)
	other, err := res.LastInsertId()
	require.NoError(t, err)
	_, err = a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: master.UID, CalendarID: other, Title: "Other series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	require.NoError(t, err)

	m.focus = focusCalendar
	next, cmd, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.True(t, handled)
	msg, ok := cmd().(eventRestoredMsg)
	require.True(t, ok)
	require.ErrorIs(t, msg.err, event.ErrMasterPurged)
	updated, _ := next.Update(msg)
	m = updated.(Model)
	require.Equal(t, 0, m.undoStack.Len())
}

// TestDeletedMsgOf_PushesEveryCalendar: the message of a series delete that
// wrote two calendars names the first, and the other one as an extra push.
func TestDeletedMsgOf_PushesEveryCalendar(t *testing.T) {
	msg := deletedMsgOf(event.Event{CalendarID: 9}, event.UndoMeta{CalendarID: 1, PushCalendars: []int64{1, 4}}, nil)
	require.Equal(t, int64(1), msg.calendarID)
	require.Equal(t, []int64{4}, msg.extraCalendarIDs)
}

// TestUndo_ReadOnlyRefusalLeavesTheStack: the undo of a delete in a calendar
// that became read-only is a permanent refusal (event.ErrUndoNotPossible).
// The entry leaves the stack with the message of the refusal, so the older
// entry below it is the next undo.
func TestUndo_ReadOnlyRefusalLeavesTheStack(t *testing.T) {
	m, a := newDBBackedModel(t)
	ctx := context.Background()
	cals, err := a.Calendars.List(ctx)
	require.NoError(t, err)
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	ev, err := a.Events.UpsertByUID(ctx, event.UpsertParams{
		UID: "tui-read-only", CalendarID: cals[0].ID, Title: "Meeting",
		StartTime: start, EndTime: start.Add(time.Hour),
	})
	require.NoError(t, err)
	meta, err := a.Events.DeleteWithUndo(ctx, ev.ID)
	require.NoError(t, err)
	older := event.UndoMeta{Kind: event.UndoKindSingle, UID: "older", CalendarID: cals[0].ID}
	m.undoStack.Push(UndoEntry{Meta: older, DeletedAt: time.Now()})
	m.undoStack.Push(UndoEntry{Meta: meta, DeletedAt: time.Now()})
	_, err = a.DB.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = ?`, cals[0].ID)
	require.NoError(t, err)

	m.focus = focusCalendar
	next, cmd, handled := m.interceptGlobalKeys(tea.KeyPressMsg{Code: 'u', Text: "u"})
	require.True(t, handled)
	msg, ok := cmd().(eventRestoredMsg)
	require.True(t, ok)
	require.ErrorIs(t, msg.err, event.ErrUndoNotPossible)
	require.ErrorIs(t, msg.err, calendaraccess.ErrReadOnly)
	updated, _ := next.Update(msg)
	m = updated.(Model)
	require.Equal(t, 1, m.undoStack.Len())
	top, ok := m.undoStack.Peek()
	require.True(t, ok)
	require.Equal(t, "older", top.Meta.UID)
	require.Equal(t, msg.err.Error(), m.toast.reason)
}

// TestUndo_TransientErrorKeepsTheEntry: a transient error, for example a
// canceled context, is no refusal. The entry stays on the stack for a retry.
func TestUndo_TransientErrorKeepsTheEntry(t *testing.T) {
	m := Model{undoStack: NewUndoStack()}
	e := entry("A")
	m.undoStack.Push(e)
	updated, _ := m.Update(eventRestoredMsg{meta: e.Meta, err: fmt.Errorf("restore: %w", context.Canceled)})
	m = updated.(Model)
	require.Equal(t, 1, m.undoStack.Len())
}
