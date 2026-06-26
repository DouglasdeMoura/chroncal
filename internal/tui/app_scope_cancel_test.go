package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/stretchr/testify/require"
)

// Regression: cancelling the recurring-edit scope dialog must clear
// m.viewReturnEvent. Without the fix, viewReturnEvent stays non-zero and the
// next unrelated eventUpdatedMsg / eventCreatedMsg (guarded on
// viewReturnEvent.ID != 0) dispatch an EventViewRequestedMsg, spuriously
// reopening the old event's view.
func TestChoiceDialogCancel_ClearsViewReturnEvent(t *testing.T) {
	ev := event.Event{
		ID:        42,
		Title:     "Standup",
		StartTime: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
	}

	m := Model{
		viewReturnEvent:  ev,
		pendingScopeKind: pendingScopeEdit,
		pendingEditSave:  EventFormSaveMsg{Title: "Standup"},
	}

	updated, _ := m.Update(ChoiceDialogResultMsg{Choice: -1})
	m = updated.(Model)

	require.Equal(t, int64(0), m.viewReturnEvent.ID,
		"cancelling scope dialog must clear viewReturnEvent")
}

// Regression: after a scope-dialog cancel, a subsequent eventUpdatedMsg must
// not dispatch EventViewRequestedMsg inside its tea.Batch. Before the fix,
// the stale viewReturnEvent caused the next event update to reopen the old
// event's view. Uses batchEmits (defined in oauth_wiring_test.go) to recurse
// into the returned tea.Batch.
func TestChoiceDialogCancel_NoSpuriousReopenOnNextUpdate(t *testing.T) {
	ev := event.Event{
		ID:        42,
		Title:     "Standup",
		StartTime: time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
	}

	m := Model{
		viewReturnEvent:  ev,
		pendingScopeKind: pendingScopeEdit,
		pendingEditSave:  EventFormSaveMsg{Title: "Standup"},
	}

	// Cancel the scope dialog.
	updated, _ := m.Update(ChoiceDialogResultMsg{Choice: -1})
	m = updated.(Model)

	// An unrelated eventUpdatedMsg arrives (e.g. from another calendar sync).
	_, cmd := m.Update(eventUpdatedMsg{calendarID: 1})

	// The batch must not contain an EventViewRequestedMsg.
	reopened := batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(EventViewRequestedMsg)
		return ok
	})
	require.False(t, reopened,
		"spurious EventViewRequestedMsg must not be dispatched after scope-dialog cancel")
}

// Regression (#407): the defensive out-of-bounds branch of the
// calendar-promote choice handler must clear all four promote fields, just
// like the cancel path. Otherwise a stale pendingCalendarPromoteName leaks
// into the next calendar-delete confirm text.
func TestChoiceDialogPromoteOOB_ClearsAllPendingFields(t *testing.T) {
	m := Model{
		pendingScopeKind:            pendingScopeCalendarPromote,
		pendingCalendarDelete:       7,
		pendingCalendarDeleteName:   "Work",
		pendingCalendarPromote:      9,
		pendingCalendarPromoteName:  "Home",
		pendingCalendarPromoteCands: nil, // empty: any non-negative Choice is OOB
	}

	// Choice 0 with no candidates triggers the defensive OOB branch.
	updated, _ := m.Update(ChoiceDialogResultMsg{Choice: 0})
	m = updated.(Model)

	require.Equal(t, int64(0), m.pendingCalendarDelete)
	require.Equal(t, "", m.pendingCalendarDeleteName)
	require.Equal(t, int64(0), m.pendingCalendarPromote,
		"OOB branch must clear pendingCalendarPromote")
	require.Equal(t, "", m.pendingCalendarPromoteName,
		"OOB branch must clear pendingCalendarPromoteName")
	require.Nil(t, m.pendingCalendarPromoteCands)
}
