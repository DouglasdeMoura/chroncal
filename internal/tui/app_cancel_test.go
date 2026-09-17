package tui

import (
	"context"
	"testing"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
)

// esc stops a running sync. The spinner gates the calendar list and the
// account manager, so without the key the user waits for the whole budget
// against a server that never answers.
func TestEscCancelsARunningSync(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m, ctx := m.beginCancellableOp()

	next, cmd, handled := m.interceptGlobalKeys(keyPressMsg("esc"))
	if !handled {
		t.Fatal("esc was not handled while a sync ran")
	}
	if cmd != nil {
		t.Errorf("esc returned a command %v, want none", cmd)
	}
	if err := ctx.Err(); err != context.Canceled {
		t.Errorf("context error = %v, want context.Canceled", err)
	}
	if !next.opCancelled {
		t.Error("opCancelled = false, want true")
	}
	if next.syncStatus != "Cancelling…" {
		t.Errorf("syncStatus = %q, want %q", next.syncStatus, "Cancelling…")
	}
}

// With no cancellable operation in flight, esc keeps its old meaning. The
// overlays then still close on it.
func TestEscIsNotCaughtWithNoRunningOperation(t *testing.T) {
	m := Model{syncSpinner: spinner.New()}
	if _, _, handled := m.interceptGlobalKeys(keyPressMsg("esc")); handled {
		t.Error("esc was caught with no operation in flight")
	}

	// A non-cancellable operation (an account rename) also leaves esc alone.
	m.syncing = true
	if _, _, handled := m.interceptGlobalKeys(keyPressMsg("esc")); handled {
		t.Error("esc was caught while a non-cancellable operation ran")
	}
}

// The cancelled run reports "Sync cancelled". The cancelled context names
// nothing the user must act on, so the error text stays off the footer.
func TestFinishSyncReportsACancelledRun(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, _ := m.finishSync(syncFinishedMsg{err: context.Canceled})
	if next.syncStatus != "Sync cancelled" {
		t.Errorf("syncStatus = %q, want %q", next.syncStatus, "Sync cancelled")
	}
	if next.syncing {
		t.Error("syncing = true after a cancelled run, want false")
	}
	if next.opCancel != nil || next.opCancelled {
		t.Error("the cancel state survived the finished run")
	}
}

// A cancel drops the queued calendar too. Starting it would put the spinner
// back on the screen the user just escaped from.
func TestCancelDropsTheQueuedCalendar(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m.pendingSyncCalendar = syncTarget{ID: 7, Name: "Work"}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, cmd := m.finishSync(syncFinishedMsg{err: context.Canceled})
	if next.pendingSyncCalendar.ID != 0 {
		t.Errorf("pendingSyncCalendar = %+v, want empty", next.pendingSyncCalendar)
	}
	if cmd == nil {
		t.Fatal("finishSync returned no command")
	}
	if batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(SyncCalendarRequestedMsg)
		return ok
	}) {
		t.Error("the cancelled run still asked for the queued calendar")
	}
}

// A sync of every calendar stops between two calendars as well as inside
// one. The finished calendars keep their results.
func TestCancelStopsTheSyncAllChain(t *testing.T) {
	m := Model{syncing: true, syncSpinner: spinner.New()}
	m.syncTargets = []syncTarget{{ID: 1, Name: "One"}, {ID: 2, Name: "Two"}}
	m, _ = m.beginCancellableOp()
	m, _ = m.cancelRunningOp()

	next, cmd := m.handleSyncCalendarFinished(syncCalendarFinishedMsg{index: 0, total: 2, name: "One"})
	if cmd == nil {
		t.Fatal("handleSyncCalendarFinished returned no command")
	}
	if !batchEmits(cmd, func(msg tea.Msg) bool {
		_, ok := msg.(syncFinishedMsg)
		return ok
	}) {
		t.Error("the cancelled chain started the next calendar")
	}
	model, ok := next.(Model)
	if !ok {
		t.Fatalf("handleSyncCalendarFinished returned %T, want Model", next)
	}
	if model.syncTargets != nil {
		t.Errorf("syncTargets = %+v, want nil", model.syncTargets)
	}
}
