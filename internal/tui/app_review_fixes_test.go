package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSaveWhileSyncingSurfacesFormError verifies a Save issued during a sync
// is refused with a visible form error instead of being silently dropped
// (code-review finding: app.go CalendarSavedMsg guard).
func TestSaveWhileSyncingSurfacesFormError(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendars = map[int64]CalendarInfo{1: {Name: "Personal"}}
	m.calendarManager = NewCalendarManagerModel(m.calendars, nil, newThemedHelp(m.theme)).
		SetSize(m.width, m.height).
		OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true})
	m.calendarManagerOpen = true
	m.syncing = true

	updated, cmd := m.Update(CalendarSavedMsg{ID: 1, Name: "Renamed"})
	m = updated.(Model)
	if cmd == nil {
		t.Fatal("blocked save emitted no feedback command")
	}
	done, ok := cmd().(calendarMutationDoneMsg)
	if !ok || done.err == nil {
		t.Fatalf("blocked save = %T/%+v, want calendarMutationDoneMsg with error", cmd(), done)
	}
	updated, _ = m.Update(done)
	m = updated.(Model)
	form, mounted := m.calendarManager.CalendarForm()
	if !mounted {
		t.Fatal("blocked save closed the editor")
	}
	if !form.form.HasError() {
		t.Fatal("blocked save did not surface a form error")
	}
}

// TestSetDefaultKeepsDirtyEditorMounted verifies the Set as Default success
// message no longer pops the edit form (code-review finding: unconditional
// pop on calendarMutationDoneMsg discarded dirty drafts).
func TestSetDefaultKeepsDirtyEditorMounted(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // dirty the draft
	dirtyName := m.calendarForm.Draft().Name

	kept, _ := m.Update(calendarMutationDoneMsg{err: nil, keepEditor: true})
	if kept.calendarForm == nil || kept.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("keepEditor mutation popped the editor: screen=%v", kept.Screen())
	}
	if got := kept.calendarForm.Draft().Name; got != dirtyName {
		t.Fatalf("draft name = %q, want %q preserved", got, dirtyName)
	}

	// A plain success (Save) still pops.
	popped, _ := m.Update(calendarMutationDoneMsg{err: nil})
	if popped.calendarForm != nil || popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("save success did not pop the editor: screen=%v", popped.Screen())
	}
}

// TestVisibilityToggleReachesOpenManager verifies the app forwards
// CalendarVisibilityToggledMsg into the open manager so its root state never
// goes stale (code-review finding: message returned before the forward).
func TestVisibilityToggleReachesOpenManager(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendars = map[int64]CalendarInfo{1: {Name: "Personal"}}
	m.calendarManager = NewCalendarManagerModel(m.calendars, nil, newThemedHelp(m.theme)).
		SetSize(m.width, m.height).
		OpenCalendar(CalendarDialogParams{ID: 1, Name: "Personal", ManagerEmbedded: true})
	m.calendarManagerOpen = true

	updated, _ := m.Update(CalendarVisibilityToggledMsg{ID: 1, Hidden: true})
	m = updated.(Model)
	if !m.calendarManager.hidden[1] {
		t.Fatal("visibility toggle did not reach the open manager's hidden map")
	}
}

// TestEmbeddedPickerRowClickToggles verifies mouse clicks work in the
// manager-embedded discovery picker (code-review finding: translated
// MouseEvents made the pane inert).
func TestEmbeddedPickerRowClickToggles(t *testing.T) {
	m := newFlatManager().OpenAccountCalendars(pickerDiscovery())
	if m.accountPicker.selected["/primary/"] {
		t.Fatal("precondition: /primary/ starts unselected in manage mode")
	}
	row := pickerRowForPath(t, *m.accountPicker, "/primary/")
	px, py, pw, ph := m.inspectorPaneRect()
	clicked, _ := m.Update(tea.MouseClickMsg{X: px + 4, Y: py + 3 + row, Button: tea.MouseLeft})
	if !clicked.accountPicker.selected["/primary/"] {
		t.Fatalf("pane click on row %d (rect %d,%d %dx%d) did not toggle /primary/", row, px, py, pw, ph)
	}
}

// TestAccountSettingsSpaceActivates verifies Space activates the focused
// account-settings action (code-review finding: bound as " " which bubbletea
// v2 never emits).
func TestAccountSettingsSpaceActivates(t *testing.T) {
	d := NewAccountSettingsDialogModel(AccountSettingsParams{AccountID: 7, DisplayName: "Google"}, activeTheme)
	_, cmd := d.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("space did not activate the focused account-settings action")
	}
}

// TestKeepLocalRequestOpensConfirm verifies the restored keep-local path:
// the remote detail's request opens a neutral confirm and stages the ID.
func TestKeepLocalRequestOpensConfirm(t *testing.T) {
	m := NewModel(nil, "")
	m.width, m.height = 120, 40
	m.calendarManagerOpen = true

	updated, _ := m.Update(CalendarKeepLocalRequestedMsg{ID: 42, Name: "Work"})
	m = updated.(Model)
	if !m.confirmOpen || m.pendingCalendarKeepLocal != 42 {
		t.Fatalf("keep-local request: confirmOpen=%v pending=%d, want open with 42", m.confirmOpen, m.pendingCalendarKeepLocal)
	}
	if view := stripANSI(m.confirmDialog.View()); !strings.Contains(view, "Keep") || !strings.Contains(view, "local calendar") {
		t.Fatalf("confirm copy missing keep-local message:\n%s", view)
	}

	// Cancelling clears the staged ID.
	updated, _ = m.Update(ConfirmDialogResultMsg{Confirmed: false})
	m = updated.(Model)
	if m.pendingCalendarKeepLocal != 0 {
		t.Fatal("cancel did not clear the staged keep-local ID")
	}
}
