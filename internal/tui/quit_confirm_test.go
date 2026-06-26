package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/trash"
)

// TestCtrlCConvertsNonQuitConfirmToQuit reproduces issue #143: ctrl+c is
// documented as "truly global", but when a destructive (non-quit) confirm is
// open it used to fall through and be swallowed. ctrl+c must instead replace
// the open confirm with the quit confirm, and clear the abandoned destructive
// pending state so it can't fire later.
func TestCtrlCConvertsNonQuitConfirmToQuit(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{}
	// Simulate an open event-delete confirm: confirm dialog is up but it is
	// NOT the quit confirm.
	m.confirmOpen = true
	m.pendingQuit = false
	m.pendingDelete = event.Event{ID: 7, Title: "Standup"}

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a non-quit confirm is open (issue #143)")
	}
	if !next.confirmOpen || !next.pendingQuit {
		t.Fatalf("ctrl+c should replace the open confirm with the quit confirm: confirmOpen=%v pendingQuit=%v", next.confirmOpen, next.pendingQuit)
	}
	if next.pendingDelete.ID != 0 {
		t.Fatalf("abandoned destructive pending state not cleared: pendingDelete.ID=%d", next.pendingDelete.ID)
	}

	// A second ctrl+c now force-quits.
	_, cmd, handled := next.interceptGlobalKeys(ctrlC)
	if !handled || cmd == nil {
		t.Fatalf("second ctrl+c should force quit: handled=%v cmd=%v", handled, cmd)
	}
}

// TestCtrlCClearsPurgePendingState guards the trash-purge variant of the same
// bug: the bulk-purge confirm must also be convertible to a quit without
// leaving its pending entries armed.
func TestCtrlCClearsPurgePendingState(t *testing.T) {
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}

	m := Model{}
	m.confirmOpen = true
	m.pendingQuit = false
	m.pendingPurgeEntries = []trash.Entry{{Kind: trash.KindEvent}}
	m.pendingPurgeTitle = "1 item"

	next, _, handled := m.interceptGlobalKeys(ctrlC)
	if !handled {
		t.Fatalf("ctrl+c not handled while a purge confirm is open (issue #143)")
	}
	if !next.pendingQuit {
		t.Fatalf("ctrl+c should open the quit confirm")
	}
	if len(next.pendingPurgeEntries) != 0 || next.pendingPurgeTitle != "" {
		t.Fatalf("abandoned purge pending state not cleared: entries=%d title=%q", len(next.pendingPurgeEntries), next.pendingPurgeTitle)
	}
}
