package tui

import (
	"fmt"

	"strings"
	"testing"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// TestCalendarManagerRootMouseRejectsClicksOutsideListWidth verifies a click
// on the right row's Y but past the list's right edge does not select. The
// hit-test bounds the list column on both sides, not just the left.
func TestCalendarManagerRootMouseRejectsClicksOutsideListWidth(t *testing.T) {
	m := newFlatManager()
	lx, listY, lw, _ := m.listRegion()
	before, _ := m.selectedID()
	clicked, cmd := m.Update(tea.MouseClickMsg{X: lx + lw + 5, Y: listY + 2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("out-of-width click should not emit a command: %T", cmd())
	}
	after, _ := clicked.selectedID()
	if after != before {
		t.Fatalf("out-of-width click changed selection: before=%d after=%d", before, after)
	}
}

// TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible is the regression
// for the clampedScroll max-clamp bug. When the list overflows, the indicator
// reserves one row (contentH = h-1). The max-scroll clamp used the full
// viewport height h. That off-by-one let the indicator overwrite the selected
// last row. It hid the row and made it unclickable. The clamp must use contentH.
// The last data row then stays rendered and reachable above the indicator.
func TestCalendarManagerRootOverflowKeepsLastSelectedRowVisible(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 12; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal%02d", i), DisplayOrder: i}
	}
	// SetSize(60,16) -> narrow box, bodyH = 7, so 12 calendars overflow and
	// reserve the last visible line for the scroll indicator.
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(60, 16)
	m = m.selectCalendar(12)
	if id, _ := m.selectedID(); id != 12 {
		t.Fatalf("setup: selected %d, want 12", id)
	}

	// The selected last row must still be rendered, not overwritten by the
	// overflow indicator.
	view := stripANSI(m.View())
	if !strings.Contains(view, "Cal12") {
		t.Errorf("selected last row Cal12 missing from view (overwritten by indicator?)\n%s", view)
	}

	// ... and it must remain clickable in the grouped list viewport.
	listX, listY, _, listH := m.listRegion()
	row := calendarListRowForCalendarID(t, m.list, 12) - m.list.offset
	if row < 0 || row >= listH {
		t.Fatalf("selected last row viewport position = %d, height %d", row, listH)
	}
	opened, _ := m.Update(tea.MouseClickMsg{X: listX + 8, Y: listY + row, Button: tea.MouseLeft})
	if opened.calendarForm == nil || opened.calendarForm.Draft().ID != 12 {
		t.Error("selected last row Cal12 is not mouse-clickable")
	}
}

// TestCalendarManagerDetailBackRestoresRootSelection verifies a push of a
// calendar detail and a press of Back (Esc) returns to the root list. The
// same calendar stays selected by immutable ID.
func TestCalendarManagerDetailBackRestoresRootSelection(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	closing, cmd := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on pop")
	}
	if id, ok := popped.selectedID(); !ok || id != 3 {
		t.Fatalf("root selection after Back = %d ok=%v, want 3", id, ok)
	}
}

// TestCalendarManagerDetailBackRestoresRootScroll verifies the scroll offset
// survives a push/pop cycle by ID. Open and close of a detail then does not
// jump the list back to the top.
func TestCalendarManagerDetailBackRestoresRootScroll(t *testing.T) {
	cals := map[int64]CalendarInfo{}
	for i := int64(1); i <= 30; i++ {
		cals[i] = CalendarInfo{Name: fmt.Sprintf("Cal %d", i), Color: "#a6e3a1", DisplayOrder: i}
	}
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(50, 16)
	// Move the cursor well past the first viewport so the list scrolls.
	for range 18 {
		m.list = m.list.moveCursor(1)
	}
	start := m.list.offset
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	closing, cmd := pushed.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List", popped.Screen())
	}
	gotStart := popped.list.offset
	if gotStart != start {
		t.Fatalf("scroll top = %d after Back, want %d", gotStart, start)
	}
}

// TestCalendarManagerDetailLocalHasLocationOnly verifies a local calendar's
// detail renders a labeled Location row valued Local. It has no Account
// opener. Local calendars have no owning account to drill into.
func TestCalendarManagerDetailLocalHasLocationOnly(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Location") || !strings.Contains(view, "Local") {
		t.Errorf("local detail missing labeled Location row:\n%s", view)
	}
	if calendarDetailFieldIndex(pushed, "opener") >= 0 {
		t.Errorf("local detail should not expose an Account opener:\n%s", view)
	}
}

// TestCalendarManagerDetailRemoteHasAccountOpener verifies a remote calendar's
// detail renders an actionable Account row. That is the account name plus
// drill-in chevron in the shared label column. When activated, it pushes the
// owning account's settings onto the stack.
func TestCalendarManagerDetailRemoteHasAccountOpener(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google, account 7
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	view := stripANSI(pushed.calendarForm.View())
	if !strings.Contains(view, "Account") || !strings.Contains(view, "Google ›") {
		t.Errorf("remote detail missing actionable Account row:\n%s", view)
	}
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	// The opener does not push the account detail itself: it passes the
	// canonical AccountSettingsRequestedMsg to the host, which owns the
	// account record and later calls OpenAccount with full params. The
	// calendar detail stays put underneath.
	opened, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Account opener emitted no request")
	}
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("Account opener = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	if opened.Screen() != CalendarManagerScreenCalendar || opened.accountSettings != nil {
		t.Fatalf("opener should not push account detail: screen=%v account=%v", opened.Screen(), opened.accountSettings)
	}
}

func TestCalendarManagerDetailAccountBackPreservesDraft(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	pushed.calendarForm.form.Field(cdIdxName).(*TextField).SetValue("Edited Primary")

	// The opener passes AccountSettingsRequestedMsg to the host; the host
	// (Task 4) resolves the canonical account record and calls OpenAccount
	// with full params — provider/server/username/attention preserved.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	requested, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	req, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || req.AccountID != 7 {
		t.Fatalf("opener request = %#v, want AccountSettingsRequestedMsg{AccountID:7}", cmd())
	}
	opened := requested.OpenAccount(AccountSettingsParams{
		AccountID:      7,
		DisplayName:    "Personal Google",
		Provider:       "Google Account",
		ServerURL:      "https://apidata.googleusercontent.com/caldav/v2/",
		Username:       "douglas@example.com",
		CalendarCount:  2,
		AttentionCount: 1,
		AuthType:       "oauth2",
	})
	if opened.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", opened.Screen())
	}
	if p := opened.accountSettings.Params(); p.Provider != "Google Account" ||
		p.ServerURL != "https://apidata.googleusercontent.com/caldav/v2/" ||
		p.Username != "douglas@example.com" || p.AttentionCount != 1 {
		t.Fatalf("canonical account params not preserved: %+v", p)
	}
	// Back returns to the originating calendar detail with its unsaved draft
	// intact — the form is never reconstructed.
	closing, cmd := opened.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("Esc emitted no account close command")
	}
	popped, _ := closing.Update(cmd())
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Back", popped.Screen())
	}
	if got := popped.calendarForm.Draft().Name; got != "Edited Primary" {
		t.Fatalf("calendar draft after Account Back = %q, want %q", got, "Edited Primary")
	}
}

// TestCalendarManagerDetailVisibilityToggleEmitsDesiredState verifies the
// detail's Display Calendar toggle emits CalendarVisibilityToggledMsg with the
// desired Hidden state immediately. It mirrors the change into the list-owned
// hidden set. The dot then stays consistent on Back.
func TestCalendarManagerDetailVisibilityToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, visible
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.list.IsHidden(1) {
		t.Fatal("calendar 1 should start visible")
	}
	focused, ok := focusCalendarDetailField(pushed, "checkbox")
	if !ok {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	hidden, cmd := focused.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	hidden, _ = hidden.Update(msg)
	if !hidden.list.IsHidden(1) {
		t.Error("list-owned hidden set not mirrored to true")
	}
	// Toggling back emits the opposite desired state.
	visible, cmd := hidden.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	msg, ok = cmd().(CalendarVisibilityToggledMsg)
	if !ok || msg.ID != 1 || msg.Hidden {
		t.Fatalf("toggle back msg = %+v, want {ID:1 Hidden:false}", msg)
	}
	visible, _ = visible.Update(msg)
	if visible.list.IsHidden(1) {
		t.Error("list-owned hidden set not mirrored back to false")
	}
}

// TestCalendarManagerDetailLeftPopsToRoot verifies the Left arrow pops a
// pushed calendar detail back to the root list (a Back gesture). That happens
// when the focus is not on a text-edit field. Root Left is unchanged (a no-op).
func TestCalendarManagerDetailLeftPopsToRoot(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar", pushed.Screen())
	}
	// Focus the Account opener (a non-editing field) so Left is free to pop.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	popped, cmd := focused.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("screen = %v, want List after Left", popped.Screen())
	}
	if popped.calendarForm != nil {
		t.Fatal("calendar form was not cleared on Left pop")
	}
	// Root Left is a no-op (no root binding consumes it).
	rootAgain, cmd := popped.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil || rootAgain.Screen() != CalendarManagerScreenList {
		t.Fatalf("root Left should be a no-op: cmd=%v screen=%v", cmd, rootAgain.Screen())
	}
}

// TestCalendarManagerDetailLeftKeepsDirtyDraft verifies the Back gesture
// never discards an unsaved draft. With edited metadata and focus on a
// non-edit field, Left leaves the editor mounted with the edit intact.
// Esc (Cancel) remains the explicit discard.
func TestCalendarManagerDetailLeftKeepsDirtyDraft(t *testing.T) {
	m := newFlatManager().selectCalendar(2) // Primary, Google
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	// Type into the focused Name field to dirty the draft.
	pushed, _ = pushed.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if pushed.calendarForm == nil || !pushed.calendarForm.dirtyMetadata() {
		t.Fatal("typing into Name did not dirty the draft")
	}
	dirtyName := pushed.calendarForm.Draft().Name
	// Move focus to the Account opener so Left is not a cursor move.
	focused, ok := focusCalendarDetailField(pushed, "opener")
	if !ok {
		t.Fatal("remote detail has no focusable Account opener")
	}
	kept, _ := focused.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if kept.Screen() != CalendarManagerScreenCalendar || kept.calendarForm == nil {
		t.Fatalf("Left discarded a dirty draft: screen=%v", kept.Screen())
	}
	if got := kept.calendarForm.Draft().Name; got != dirtyName {
		t.Fatalf("draft name = %q, want %q preserved", got, dirtyName)
	}
}

// TestCalendarManagerDetailButtonDisposition pins the Apple-sheet action
// layout in a wide editor. Set as Default and Export share the utility tier
// on one row. Delete sits flush-left on the same line as the
// right-aligned Save and Cancel commit controls.
func TestCalendarManagerDetailButtonDisposition(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // local: all three actions
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	rows := strings.Split(stripANSI(m.calendarForm.form.ButtonRowView()), "\n")
	if len(rows) < 3 {
		t.Fatalf("button block = %d rows, want utility tier, blank, commit:\n%s", len(rows), strings.Join(rows, "\n"))
	}
	utility := strings.Join(rows[:len(rows)-2], "\n")
	for _, label := range []string{"Set as Default", "Export Calendar…", "Move to Account…"} {
		if !strings.Contains(utility, label) {
			t.Fatalf("utility tier missing %q:\n%s", label, utility)
		}
	}
	if strings.TrimSpace(rows[len(rows)-2]) != "" {
		t.Fatalf("row between tiers = %q, want blank", rows[len(rows)-2])
	}
	commit := rows[len(rows)-1]
	if !strings.Contains(commit, "Delete Calendar…") || !strings.Contains(commit, "Save") || !strings.Contains(commit, "Cancel") {
		t.Fatalf("commit row = %q, want Delete flush-left beside Save/Cancel", commit)
	}
	if strings.Index(commit, "Delete Calendar…") > strings.Index(commit, "Save") {
		t.Fatalf("Delete must render left of Save: %q", commit)
	}
}

// TestCalendarManagerTabTraversalRoundTripsThroughEditor verifies Tab
// traversal is continuous across the whole dialog. From the source list,
// repeated Tab enters the previewed editor. It walks its fields and buttons.
// It exits past the last control back to the focused source list. Shift-Tab
// from the editor's first field exits back to + Add.
func TestCalendarManagerTabTraversalRoundTripsThroughEditor(t *testing.T) {
	tab := managerTabKey(false)
	m := newFlatManager().selectCalendar(1)

	// list → + Add → editor (first field focused).
	m, _ = m.Update(tab)
	m, _ = m.Update(tab)
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil {
		t.Fatalf("tab did not enter the editor: screen=%v", m.Screen())
	}
	if got, want := m.calendarForm.form.Focused(), m.calendarForm.form.FirstFocusable(); got != want {
		t.Fatalf("editor entry focus = %d, want first focusable %d", got, want)
	}

	// Walk the whole form: Tab from every slot until the last focusable.
	for guard := 0; m.calendarForm.form.Focused() != m.calendarForm.form.LastFocusable(); guard++ {
		if guard > 32 {
			t.Fatal("tab never reached the form's last focusable slot")
		}
		m, _ = m.Update(tab)
		if m.calendarForm == nil {
			t.Fatal("tab exited the editor before its last control")
		}
	}

	// Tab past the last control exits to the focused source list.
	m, _ = m.Update(tab)
	if m.Screen() != CalendarManagerScreenList || m.calendarForm != nil {
		t.Fatalf("tab past the last control did not return to the list: screen=%v", m.Screen())
	}
	if m.rootFocus != rootFocusList || !m.list.Focused() {
		t.Fatalf("root focus = %v after exiting the editor, want focused list", m.rootFocus)
	}

	// Shift-Tab from the editor's first field exits back to + Add.
	m, _ = m.Update(tab)
	m, _ = m.Update(tab) // re-enter the editor
	if m.calendarForm == nil {
		t.Fatal("tab did not re-enter the editor")
	}
	m, _ = m.Update(managerTabKey(true))
	if m.Screen() != CalendarManagerScreenList || m.rootFocus != rootFocusAdd {
		t.Fatalf("shift-tab from first field: screen=%v focus=%v, want list screen with + Add focus", m.Screen(), m.rootFocus)
	}
}

// TestCalendarManagerTabStaysInsideDirtyEditor verifies Tab keeps a wrap
// inside the form once the draft is dirty. Traversal can then never discard
// typed edits.
func TestCalendarManagerTabStaysInsideDirtyEditor(t *testing.T) {
	tab := managerTabKey(false)
	m := newFlatManager().selectCalendar(1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // dirty the Name field

	// Tab far past the form's slot count: the editor must stay mounted.
	for range 40 {
		m, _ = m.Update(tab)
		if m.calendarForm == nil {
			t.Fatal("tab exited a dirty editor")
		}
	}
	if m.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar while draft is dirty", m.Screen())
	}
}

// TestCalendarManagerEscOnDirtyDraftAsksBeforeDiscarding verifies the
// Apple-style save-changes flow. Esc on a dirty calendar draft opens a
// destructive Discard prompt instead of a close. A decline keeps the draft
// intact. A confirm pops to the root list.
func TestCalendarManagerEscOnDirtyDraftAsksBeforeDiscarding(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"}) // dirty the Name field
	dirtyName := m.calendarForm.Draft().Name

	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc emitted no close command")
	}
	m, _ = m.Update(cmd())
	if m.discardConfirm == nil {
		t.Fatal("esc on a dirty draft did not open the discard prompt")
	}
	if m.Screen() != CalendarManagerScreenCalendar || m.calendarForm == nil {
		t.Fatalf("prompt must keep the editor mounted: screen=%v", m.Screen())
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Discard unsaved changes?") || !strings.Contains(view, "Discard") {
		t.Fatalf("discard prompt not rendered:\n%s", view)
	}

	// Keep Editing: the prompt closes, the draft survives.
	kept, _ := m.Update(ConfirmDialogResultMsg{Confirmed: false})
	if kept.discardConfirm != nil || kept.calendarForm == nil {
		t.Fatal("declining the prompt must return to the editor")
	}
	if got := kept.calendarForm.Draft().Name; got != dirtyName {
		t.Fatalf("draft name = %q, want %q preserved", got, dirtyName)
	}

	// Discard: the prompt closes and the editor pops to the root list.
	discarded, _ := m.Update(ConfirmDialogResultMsg{Confirmed: true})
	if discarded.discardConfirm != nil || discarded.calendarForm != nil || discarded.Screen() != CalendarManagerScreenList {
		t.Fatalf("confirming discard did not pop to the list: screen=%v", discarded.Screen())
	}
}

// TestCalendarManagerEscOnCleanDraftClosesWithoutPrompt verifies an unedited
// form still closes on Esc with no prompt in between.
func TestCalendarManagerEscOnCleanDraftClosesWithoutPrompt(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("esc emitted no close command")
	}
	m, _ = m.Update(cmd())
	if m.discardConfirm != nil {
		t.Fatal("clean draft must not prompt on close")
	}
	if m.Screen() != CalendarManagerScreenList || m.calendarForm != nil {
		t.Fatalf("clean esc did not close the editor: screen=%v", m.Screen())
	}
}

// TestCalendarManagerPickerLeftKeepsStagedSelection verifies Left pops an
// untouched account-calendars picker. It never pops one that holds staged,
// unapplied subscription changes.
func TestCalendarManagerPickerLeftKeepsStagedSelection(t *testing.T) {
	// Untouched picker: Left pops back to the root list.
	m := newFlatManager().OpenAccountCalendars(pickerDiscovery())
	popped, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if popped.Screen() != CalendarManagerScreenList {
		t.Fatalf("Left did not pop a clean picker: screen=%v", popped.Screen())
	}

	// Staged change: Left must keep the picker and its selection mounted.
	m = newFlatManager().OpenAccountCalendars(pickerDiscovery())
	m.accountPicker.selected["/primary/"] = true
	if !m.accountPicker.dirtySelection() {
		t.Fatal("staged toggle did not mark the picker dirty")
	}
	kept, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if kept.Screen() != CalendarManagerScreenAccountCalendars || kept.accountPicker == nil {
		t.Fatalf("Left discarded staged picker changes: screen=%v", kept.Screen())
	}
	if !kept.accountPicker.selected["/primary/"] {
		t.Fatal("staged selection lost after Left")
	}
}

// TestCalendarManagerDetailLeftDoesNotStealCursor verifies Left keeps a move
// of the cursor while a text field is focused. The Back gesture then never
// interrupts the edit.
func TestCalendarManagerDetailLeftDoesNotStealCursor(t *testing.T) {
	m := newFlatManager().selectCalendar(1) // On device, local
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil {
		t.Fatal("Enter did not push a calendar form")
	}
	// Name (a TextField) is focused by default after the detail opens.
	popped, _ := pushed.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("Left popped the detail while editing: screen=%v", popped.Screen())
	}
	if popped.calendarForm == nil {
		t.Fatal("Left discarded the calendar detail while editing")
	}
}

// TestCalendarManagerDetailLeftPopsAccountToCalendar verifies Left pops the
// pushed account detail back to the calendar detail it came from.
func TestCalendarManagerDetailLeftPopsAccountToCalendar(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = m.OpenAccount(AccountSettingsParams{
		AccountID:   7,
		DisplayName: "Personal Google",
		Provider:    "Google Account",
		AuthType:    "oauth2",
	})
	if m.Screen() != CalendarManagerScreenAccount {
		t.Fatalf("screen = %v, want Account", m.Screen())
	}
	popped, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
	if cmd != nil {
		t.Fatalf("Left pop should emit no command, got %T", cmd())
	}
	if popped.Screen() != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want Calendar after Left", popped.Screen())
	}
	if popped.accountSettings != nil {
		t.Fatal("account settings was not cleared on Left pop")
	}
}

// TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState verifies a
// mouse click on the Display Calendar checkbox emits the same
// CalendarVisibilityToggledMsg as the keyboard path. Regression: the mouse
// branch used to return before the pre/post visibility comparison.
func TestCalendarManagerDetailVisibilityMouseToggleEmitsDesiredState(t *testing.T) {
	m := newFlatManager().selectCalendar(1).SetSize(120, 40)
	pushed, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if pushed.calendarForm == nil || pushed.list.IsHidden(1) {
		t.Fatal("precondition: calendar 1 detail open and visible")
	}
	cbIdx := calendarDetailFieldIndex(pushed, "checkbox")
	if cbIdx < 0 {
		t.Fatal("detail has no Display Calendar checkbox")
	}
	click, ok := calendarDetailCheckboxClick(pushed, cbIdx)
	if !ok {
		t.Fatal("could not resolve checkbox click point")
	}
	hidden, cmd := pushed.Update(click)
	if cmd == nil {
		t.Fatal("mouse visibility toggle emitted no command")
	}
	msg, ok := cmd().(CalendarVisibilityToggledMsg)
	if !ok {
		t.Fatalf("expected CalendarVisibilityToggledMsg, got %T", cmd())
	}
	if msg.ID != 1 || !msg.Hidden {
		t.Fatalf("mouse toggle msg = %+v, want {ID:1 Hidden:true}", msg)
	}
	hidden, _ = hidden.Update(msg)
	if !hidden.list.IsHidden(1) {
		t.Error("list-owned hidden set not mirrored to true after mouse toggle")
	}
}

func TestCalendarManagerAccountConnectionLeftNeverOpensRoot(t *testing.T) {
	for _, focus := range []int{calDAVIdxAuth, -1} {
		m := newFlatManager().OpenAccountConnection()
		if focus < 0 {
			focus = m.calendarForm.form.cancelIndex()
		}
		m.calendarForm.form.focused = focus
		updated, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		if updated.Screen() != CalendarManagerScreenCalendar || updated.calendarForm == nil {
			t.Fatalf("Left at focus %d exposed manager root: screen=%v", focus, updated.Screen())
		}
	}
}

func TestCalendarManagerDirectAccountCloseReturnsToList(t *testing.T) {
	for _, press := range []tea.KeyPressMsg{
		{Code: tea.KeyEscape},
		{Code: tea.KeyLeft},
	} {
		m := newFlatManager().OpenAccount(AccountSettingsParams{
			AccountID: 7, DisplayName: "Personal Google", AuthType: "oauth2",
		})
		closing, cmd := m.Update(press)
		if press.Code == tea.KeyLeft {
			if cmd != nil || closing.Screen() != CalendarManagerScreenList {
				t.Fatalf("Left did not return directly to list: screen=%v cmd=%v", closing.Screen(), cmd)
			}
			continue
		}
		if cmd == nil {
			t.Fatal("Esc emitted no account close command")
		}
		closed, _ := closing.Update(cmd())
		if closed.Screen() != CalendarManagerScreenList || closed.accountSettings != nil {
			t.Fatalf("Esc did not restore list: screen=%v", closed.Screen())
		}
	}
}

func TestCalendarManagerDoesNotExecuteChildCommandsSynchronously(t *testing.T) {
	executed := false
	m := newFlatManager().OpenCalendar(CalendarDialogParams{ID: 1, Name: "On device"})
	m.calendarForm.form.items[0].Field = &managerCommandProbeField{executed: &executed}
	m.calendarForm.form.focused = 0

	updated, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if executed {
		t.Fatal("manager executed child command inside Update")
	}
	if cmd == nil {
		t.Fatal("manager dropped child command")
	}
	if updated.screen != CalendarManagerScreenCalendar {
		t.Fatalf("screen = %v, want calendar inspector", updated.screen)
	}
	_ = cmd()
	if !executed {
		t.Fatal("returned child command did not execute when Bubble Tea ran it")
	}
}

func TestCalendarManagerRootGroupsAccountsAndShowsInspector(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	view := stripANSI(m.View())
	for _, want := range []string{
		"Local", "Google", "Fastmail",
		"● Primary",
		"Edit calendar", "Name", "Save",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("manager view missing %q:\n%s", want, view)
		}
	}
	// The manager renders account headings as plain section titles: no
	// disclosure chevron in front of the account name.
	if strings.Contains(view, "▾") || strings.Contains(view, "▸") {
		t.Errorf("manager account headings must not show disclosure chevrons:\n%s", view)
	}
	if strings.Contains(view, "Edit ›") {
		t.Fatalf("calendar rows must not repeat Edit links beside the inspector:\n%s", view)
	}

	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	accountView := stripANSI(m.View())
	for _, want := range []string{"Google", "Calendars", "2", "Account Settings…"} {
		if !strings.Contains(accountView, want) {
			t.Errorf("account inspector missing %q:\n%s", want, accountView)
		}
	}
}

// TestCalendarManagerInspectorHeaderIsCalendarsNotAdd verifies the manager
// header is the plain "Calendars" title and never couples the + Add action.
func TestCalendarManagerInspectorHeaderIsCalendarsNotAdd(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	for _, line := range strings.Split(stripANSI(m.View()), "\n") {
		if strings.Contains(line, "Calendars") && strings.Contains(line, "+ Add") {
			t.Fatalf("header couples Calendars and + Add: %q", line)
		}
	}
	if _, _, pw, ph, ok := m.previewPaneRect(); !ok || pw == 0 || ph == 0 {
		t.Fatalf("calendar selection has no preview pane: ok=%v w=%d h=%d", ok, pw, ph)
	}
}

// TestCalendarManagerInspectorCalendarShowsEditFormPreview verifies a select
// of a calendar renders its edit form immediately in the inspector (macOS
// Settings-style master–detail). Fields, Display checkbox, and buttons appear
// with no Edit… pill and no pinned-action rect in between.
func TestCalendarManagerInspectorCalendarShowsEditFormPreview(t *testing.T) {
	m := newFlatManager().selectCalendar(3) // Holidays, Google
	view := stripANSI(m.View())
	for _, want := range []string{"Edit calendar", "Name", "Holidays", "Display calendar", "Save"} {
		if !strings.Contains(view, want) {
			t.Errorf("calendar preview missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Edit…") {
		t.Errorf("calendar selection must render the form preview, not an Edit… pill:\n%s", view)
	}
	if _, _, _, ok := m.inspectorActionRect(); ok {
		t.Fatal("calendar selection must not expose a pinned-action rect")
	}
	if _, _, _, _, ok := m.previewPaneRect(); !ok {
		t.Fatal("calendar selection must expose the preview pane rect")
	}
}

// TestCalendarManagerInspectorAccountShowsAccountSettingsAction verifies a
// remote account inspector shows the account name, metadata, and a bottom
// Account Settings… action — and no legacy "Enter  Account Settings".
func TestCalendarManagerInspectorAccountShowsAccountSettingsAction(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	view := stripANSI(m.View())
	for _, want := range []string{"Google", "Calendars", "2", "Account Settings…"} {
		if !strings.Contains(view, want) {
			t.Errorf("account inspector missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "Enter  Account Settings") {
		t.Errorf("legacy underlined Account Settings pseudo-link still rendered:\n%s", view)
	}
	_, ay, _, ok := m.inspectorActionRect()
	if !ok {
		t.Fatal("account inspector has no action rect")
	}
	row := inspectorActionScreenRow(m, ay)
	if !strings.Contains(strings.Split(view, "\n")[row], "Account Settings…") {
		t.Fatalf("Account Settings… not on action row %d:\n%s", row, view)
	}
}

// TestCalendarManagerInspectorLocalShowsCountWithoutAction verifies the Local
// group inspector shows "On this device" with its calendar count. It has no
// bottom action (no Add, no Account Settings) and no action rect.
func TestCalendarManagerInspectorLocalShowsCountWithoutAction(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 0})
	// The Local inspector pane shows "On this device" and its calendar count;
	// the unwanted-action check is scoped to the inspector pane because the
	// source column carries its own (unrelated) + Add action.
	w, h := m.inspectorPaneSize()
	inspector := stripANSI(strings.Join(m.selectionInspectorLines(w, h), "\n"))
	for _, want := range []string{"Local", "On this device", "Calendars", "1"} {
		if !strings.Contains(inspector, want) {
			t.Errorf("Local inspector missing %q:\n%s", want, inspector)
		}
	}
	for _, unwanted := range []string{"Account Settings…", "Edit…", "Add Calendar"} {
		if strings.Contains(inspector, unwanted) {
			t.Errorf("Local inspector must not show %q:\n%s", unwanted, inspector)
		}
	}
	if _, _, _, ok := m.inspectorActionRect(); ok {
		t.Fatal("Local inspector must have no action rect")
	}
}

// TestCalendarManagerPreviewPaneClickOpensCalendar verifies a click anywhere
// inside the previewed edit form focuses it. The detail opens for the
// selected calendar's immutable ID. A click past the pane's right edge
// does nothing.
func TestCalendarManagerPreviewPaneClickOpensCalendar(t *testing.T) {
	m := newFlatManager().selectCalendar(3)
	px, py, pw, ph, ok := m.previewPaneRect()
	if !ok {
		t.Fatal("calendar selection has no preview pane rect")
	}
	clicked, cmd := m.Update(tea.MouseClickMsg{X: px + pw/2, Y: py + ph/2, Button: tea.MouseLeft})
	if cmd != nil {
		t.Fatalf("preview click emitted a command %T", cmd())
	}
	if clicked.Screen() != CalendarManagerScreenCalendar || clicked.calendarForm == nil {
		t.Fatalf("preview click did not open calendar detail: screen=%v form=%v", clicked.Screen(), clicked.calendarForm)
	}
	if got := clicked.calendarForm.Draft().ID; got != 3 {
		t.Fatalf("preview click opened calendar %d, want immutable ID 3", got)
	}
	// A click one cell past the pane's right edge must not open the detail.
	missed, _ := m.Update(tea.MouseClickMsg{X: px + pw, Y: py, Button: tea.MouseLeft})
	if missed.calendarForm != nil {
		t.Fatal("click past the preview pane's right edge opened the calendar detail")
	}
}

// TestCalendarManagerInspectorAccountActionClickEmitsTarget verifies a click
// of the account inspector's Account Settings… button emits a typed account
// target for the selected account. It leaves the root list mounted.
func TestCalendarManagerInspectorAccountActionClickEmitsTarget(t *testing.T) {
	m := newFlatManager()
	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	ax, ay, _, ok := m.inspectorActionRect()
	if !ok {
		t.Fatal("account inspector has no action rect")
	}
	clicked, cmd := m.Update(tea.MouseClickMsg{X: ax, Y: ay, Button: tea.MouseLeft})
	if cmd == nil {
		t.Fatal("Account Settings… click emitted no command")
	}
	msg, ok := cmd().(CalendarManagerRequestedMsg)
	if !ok {
		t.Fatalf("expected CalendarManagerRequestedMsg, got %T", cmd())
	}
	if msg.Target != CalendarManagerTargetAccount || msg.AccountID != 7 {
		t.Fatalf("account action msg = %+v, want {Target:Account AccountID:7}", msg)
	}
	if clicked.Screen() != CalendarManagerScreenList {
		t.Fatalf("account action should not push a screen: screen=%v", clicked.Screen())
	}
}

// TestCalendarManagerInspectorPadsExactlyHeight verifies the selection
// inspector composes exactly its height in rows. That holds for the calendar
// edit-form preview and for the account summary with its pinned action. Layout
// then never leaves a ragged bottom or pushes content off.
func TestCalendarManagerInspectorPadsExactlyHeight(t *testing.T) {
	m := newFlatManager().selectCalendar(2)
	w, h := m.inspectorPaneSize()
	lines := m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("calendar preview lines = %d, want exactly %d", len(lines), h)
	}
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "Save") {
		t.Fatal("calendar preview did not render the form's button row")
	}

	m.list.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: 7})
	lines = m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("account inspector lines = %d, want exactly %d", len(lines), h)
	}
	if !strings.Contains(stripANSI(lines[len(lines)-1]), "Account Settings…") {
		t.Fatalf("last account inspector row is not the pinned action: %q", lines[len(lines)-1])
	}
}

// TestCalendarManagerInspectorLongDescriptionKeepsPreviewHeight verifies a
// long description cannot distort the preview. The form's Description field
// truncates it. The pane still composes exactly its height. An enter of the
// editor still works.
func TestCalendarManagerInspectorLongDescriptionKeepsPreviewHeight(t *testing.T) {
	cals := flatManagerCalendars()
	base := cals[3]
	base.Description = strings.Repeat("A long calendar description word. ", 80)
	cals[3] = base
	m := NewCalendarManagerModel(cals, nil, help.New()).SetSize(120, 40).selectCalendar(3)

	w, h := m.inspectorPaneSize()
	lines := m.selectionInspectorLines(w, h)
	if len(lines) != h {
		t.Fatalf("inspector lines = %d, want exactly %d (description overflowed)", len(lines), h)
	}

	opened, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if opened.calendarForm == nil || opened.calendarForm.Draft().ID != 3 {
		t.Fatal("editor not reachable with a long description present")
	}
}

func TestCalendarManagerSelectsFirstCalendarWhenInitialDataLoads(t *testing.T) {
	m := NewCalendarManagerModel(nil, nil, help.New()).SetSize(120, 40)
	m = m.SetData(flatManagerCalendars(), nil)
	if id, ok := m.selectedID(); !ok || id != 1 {
		t.Fatalf("initial loaded selection = %d ok=%v, want first calendar 1", id, ok)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "Edit calendar") {
		t.Fatalf("initial calendar edit-form preview did not render:\n%s", view)
	}
}

func TestCalendarManagerWideCalendarEditorKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().selectCalendar(1)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide calendar editor did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Name") || !strings.Contains(view, "Save") {
		t.Fatalf("wide calendar editor missing inline form:\n%s", view)
	}
}

func TestCalendarManagerWideAccountInspectorKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().selectCalendar(2).OpenAccount(AccountSettingsParams{
		AccountID: 7, DisplayName: "Google", Provider: "google", CalendarCount: 2,
	})
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide account inspector did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Manage Calendars") || !strings.Contains(view, "Rename Account") {
		t.Fatalf("wide account inspector missing inline actions:\n%s", view)
	}
}

func TestCalendarManagerWideImportKeepsHierarchyMounted(t *testing.T) {
	m := newFlatManager().OpenImport(41)
	view := stripANSI(m.View())
	if !strings.Contains(view, "Local") || !strings.Contains(view, "Primary") {
		t.Fatalf("wide import did not keep hierarchy mounted:\n%s", view)
	}
	if !strings.Contains(view, "Import iCal file") || !strings.Contains(view, "Path") {
		t.Fatalf("wide import missing inline form:\n%s", view)
	}
}

func TestCalendarManagerShallowWideFallbackSizesWholeListPane(t *testing.T) {
	m := newFlatManager().SetSize(100, 10)
	boxW, _ := m.BoxSize()
	wantW := max(boxW-5, 10)
	listW, _ := m.rootPaneSize()
	_, _, hitW, _ := m.listRegion()
	if listW != wantW || hitW != wantW {
		t.Fatalf("one-pane list widths: size=%d hit=%d want=%d", listW, hitW, wantW)
	}
	if got := m.list.width; got != wantW {
		t.Fatalf("sized list width = %d, want %d", got, wantW)
	}
	// Width permits the menu's natural content, so the longest label must not
	// be needlessly truncated (regression: the cap once bound the box height
	// instead of its width, truncating labels even on a wide terminal).
	mm := m.openAddMenu()
	longest := 0
	for _, item := range calendarManagerAddItems {
		longest = max(longest, lipgloss.Width(item.label))
	}
	natural := longest + 1 + calendarManagerMenuTrailing
	if got := mm.addMenuContentWidth(); got != natural {
		t.Fatalf("menu content width = %d, want natural %d (label truncated at wide box)", got, natural)
	}
}
