package tui

import (
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

func (m CalendarManagerModel) Update(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	// The discard-changes prompt owns all input while open.
	if m.discardConfirm != nil {
		return m.updateDiscardConfirm(msg)
	}
	// A pushed detail owns input until it closes. Each child handles its own
	// field edits. The manager intercepts only navigation. Esc/close pops
	// via the child's close message. Left pops one child before
	// delegation (a Back gesture). Exception: while a text-edit field holds
	// focus in the calendar detail, Left still moves the cursor.
	if m.screen != CalendarManagerScreenList {
		if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
			// The discovery picker renders no mouse marks, so translated
			// MouseEvents cannot reach it; route its pane clicks through the
			// geometry-based inspector handler instead.
			if m.screen == CalendarManagerScreenAccountCalendars && m.accountPicker != nil {
				px, py, pw, ph := m.inspectorPaneRect()
				if click.X >= px && click.X < px+pw && click.Y >= py && click.Y < py+ph {
					next, cmd := m.accountPicker.HandleInspectorClick(click.X-px, click.Y-py, pw, ph)
					m.accountPicker = &next
					return m.sizeActiveInspector(), cmd
				}
				return m, nil
			}
			ox, oy := m.dialogOrigin()
			msg = MouseEvent{IsClick: true, Target: mouseResolve(click.X-ox, click.Y-oy)}
		}
		if popped, cmd, ok := m.popOnLeft(msg); ok {
			return popped, cmd
		}
	}
	switch m.screen {
	case CalendarManagerScreenList:
		// handled by the root list below
	case CalendarManagerScreenAccount:
		return m.updateAccount(msg)
	case CalendarManagerScreenAccountCalendars:
		return m.updateAccountCalendars(msg)
	case CalendarManagerScreenCalendar:
		return m.updateCalendar(msg)
	case CalendarManagerScreenTransfer:
		return m.updateTransfer(msg)
	}
	// Root list.
	if m.addMenuOpen {
		return m.updateAddMenu(msg)
	}
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

// popOnLeft implements the Back gesture for a pushed detail. The Left arrow
// pops one child before the message is delegated to it. The user can then
// drill out with the arrow key. It never fires at the root (root Left is
// unchanged). It is suppressed while a field owns Left for edit, every
// direct account-connection field included. The bool reports whether the
// message was consumed as a Back gesture.
func (m CalendarManagerModel) popOnLeft(msg tea.Msg) (CalendarManagerModel, tea.Cmd, bool) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok || press.Code != tea.KeyLeft {
		return m, nil, false
	}
	switch m.screen {
	case CalendarManagerScreenList, CalendarManagerScreenTransfer:
		return m, nil, false
	case CalendarManagerScreenAccountCalendars:
		// Staged-but-unapplied subscription changes must not be discarded by
		// a navigation gesture; Esc/Cancel stays the explicit discard.
		if m.accountPicker != nil && m.accountPicker.dirtySelection() {
			return m, nil, false
		}
		return m.HideDiscovery(), nil, true
	case CalendarManagerScreenAccount:
		// Account settings opened from a calendar pop back to that unchanged
		// edit form. Directly opened account settings pop back to the root
		// calendar list (CloseAccount routes both, same as Esc).
		return m.CloseAccount(), nil, true
	case CalendarManagerScreenCalendar:
		// The child owns the Left key while a text field is in edit, in the
		// account-connection layout, or with unsaved edits. A navigation
		// gesture must never discard a draft. Esc/Cancel stays the
		// explicit discard.
		if m.calendarForm == nil || m.calendarForm.absorbsBack() {
			return m, nil, false
		}
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, nil, true
	}
	return m, nil, false
}

func (m CalendarManagerModel) handleKey(msg tea.KeyPressMsg) (CalendarManagerModel, tea.Cmd) {
	// Tab/Shift-Tab cycle root focus before any list child sees the key, so the
	// ring never leaks navigation into list cursor movement.
	if key.Matches(msg, m.keys.Next) {
		return m.advanceRootFocus(true)
	}
	if key.Matches(msg, m.keys.Prev) {
		return m.advanceRootFocus(false)
	}
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return CalendarManagerClosedMsg{} }
	case key.Matches(msg, m.keys.Add):
		return m.openAddMenu(), nil
	}
	// Enter/Space activate the focused source or inspector action.
	if key.Matches(msg, m.keys.Activate) {
		switch m.rootFocus {
		case rootFocusList:
			// Continue to the list-specific Enter/Space handling below.
		case rootFocusAdd:
			return m.openAddMenu(), nil
		case rootFocusInspector:
			action, _ := m.selectionInspectorAction()
			return m.applyInspectorAction(action)
		}
	}
	// While the list holds root focus, Enter opens the selected calendar
	// internally (unchanged routing); non-calendar rows fall through to the
	// list's own Open handling.
	if m.rootFocus == rootFocusList && key.Matches(msg, m.keys.Open) {
		if id, info, ok := m.selectedCalendar(); ok {
			return m.openSelectedCalendar(id, info), nil
		}
	}
	// Everything else (arrows, space, collapse/expand) belongs to the list,
	// which only acts while focused. Keep the focus ring consistent in case the
	// selection changed and dropped an inspector action out of the ring.
	m = m.applyRootFocus()
	next, cmd := m.list.Update(msg)
	m.list = next
	return m.normalizeRootFocus(), cmd
}

func (m CalendarManagerModel) handleMouse(msg tea.MouseClickMsg) (CalendarManagerModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	// Selection inspector bottom action (wide root only): restore its focus, then
	// route before the source-list click so the pill never activates an
	// underlying row.
	if ax, ay, aw, ok := m.inspectorActionRect(); ok && msg.Y == ay && msg.X >= ax && msg.X < ax+aw {
		m = m.setRootFocus(rootFocusInspector)
		action, _ := m.selectionInspectorAction()
		return m.applyInspectorAction(action)
	}
	if m.sourceAddActionActive() {
		if ax, ay, aw, ok := m.sourceAddActionRect(); ok && msg.Y == ay && msg.X >= ax && msg.X < ax+aw {
			return m.setRootFocus(rootFocusAdd).openAddMenu(), nil
		}
	}
	// A click anywhere in the root inspector pane focuses the previewed edit
	// form (calendar selections in wide roots; account selections use the
	// pinned action above).
	if px, py, pw, ph, ok := m.previewPaneRect(); ok &&
		msg.X >= px && msg.X < px+pw && msg.Y >= py && msg.Y < py+ph {
		if id, info, selOK := m.selectedCalendar(); selOK {
			return m.setRootFocus(rootFocusList).openSelectedCalendar(id, info), nil
		}
	}
	lx, ly, lw, lh := m.listRegion()
	if msg.X < lx || msg.X >= lx+lw || msg.Y < ly || msg.Y >= ly+lh {
		return m, nil
	}
	// A source-list click always restores list focus before routing. The list
	// owns the visibility set, so a click that toggles a dot updates the single
	// source directly — no projection to mirror back.
	m = m.setRootFocus(rootFocusList)
	relX := msg.X - lx
	next, cmd := m.list.HandleClick(relX, msg.Y-ly)
	m.list = next
	m = m.normalizeRootFocus()
	identity, selected := m.list.currentIdentity()
	indicatorEnd := m.list.visibilityIndicatorWidth()
	if m.list.grouped {
		indicatorEnd++
	}
	if selected && identity.kind == calendarRow && relX >= indicatorEnd {
		if id, info, ok := m.selectedCalendar(); ok {
			return m.openSelectedCalendar(id, info), nil
		}
	}
	return m, cmd
}

func (m CalendarManagerModel) updateAccountCalendars(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if _, ok := msg.(AccountCalendarPickerClosedMsg); ok {
		return m.HideDiscovery(), nil
	}
	if m.accountPicker == nil {
		return m.HideDiscovery(), nil
	}
	next, cmd := m.accountPicker.Update(msg)
	m.accountPicker = &next
	return m.sizeActiveInspector(), cmd
}

// updateCalendar delegates input to the pushed calendar detail and intercepts
// only navigation messages. CalendarDialogClosedMsg pops back to the root
// list. CalendarVisibilityToggledMsg is mirrored into the list-owned hidden
// set so the dot stays consistent on Back. The Account opener's
// AccountSettingsRequestedMsg is NOT intercepted here. It passes through to
// the host, which owns the canonical account record and later calls
// OpenAccount with full params. Every other domain/action message (Save, Set
// Default, Export, Delete, …) likewise passes through unchanged.
func (m CalendarManagerModel) updateCalendar(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if done, ok := msg.(calendarMutationDoneMsg); ok {
		// A successful Save pops back to the root list; keepEditor mutations
		// (Set as Default) leave the form — and any unsaved draft — mounted.
		if done.err == nil {
			if done.keepEditor {
				return m, nil
			}
			m.calendarForm = nil
			m.screen = CalendarManagerScreenList
			return m, nil
		}
	}

	switch typed := msg.(type) {
	case CalendarDialogClosedMsg:
		// Esc/Cancel on a dirty draft asks before discarding (Apple's
		// save-changes prompt); a clean form closes immediately.
		if m.calendarForm != nil && m.calendarForm.dirtyMetadata() {
			confirm := NewConfirmDialogModel("Discard unsaved changes?", "Discard", m.theme).
				Destructive().SetSize(m.confirmOverlayWidth(), m.height)
			m.discardConfirm = &confirm
			return m, nil
		}
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, nil
	case CalendarVisibilityToggledMsg:
		// Mirror the detail's optimistic toggle into the list-owned set so
		// the row's dot is already correct when the user pops back. The list
		// is the single source of truth within the manager; the host also
		// persists this message when it receives the child command.
		m.list = m.list.SetHidden(typed.ID, typed.Hidden)
		return m, nil
	}

	if m.calendarForm == nil {
		m.screen = CalendarManagerScreenList
		return m, nil
	}
	// Tab traversal is continuous across the whole dialog. On a clean form,
	// Tab past the last control returns to the source list. Shift-Tab
	// from the first field returns to + Add. That completes the root ring
	// (list → + Add → form → list). A dirty form keeps wrapping internally.
	// Traversal can then never discard typed edits.
	if popped, ok := m.tabOutOfCalendarForm(msg); ok {
		return popped, nil
	}
	next, cmd := m.calendarForm.Update(msg)
	m.calendarForm = &next
	m = m.sizeActiveInspector()
	// Commands may contain timers (for example the text cursor blink). Bubble
	// Tea must execute them asynchronously; invoking them here stalls Update.
	return m, cmd
}

// tabOutOfCalendarForm implements the Tab boundary hand-off for the pushed
// calendar editor. The bool reports whether the key was consumed as a
// traversal exit. It never fires for the account-connection layout, while an
// embedded discovery picker is open, or while the draft is dirty.
func (m CalendarManagerModel) tabOutOfCalendarForm(msg tea.Msg) (CalendarManagerModel, bool) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok || m.calendarForm == nil {
		return m, false
	}
	if !key.Matches(press, m.keys.Next) && !key.Matches(press, m.keys.Prev) {
		return m, false
	}
	if m.calendarForm.absorbsTab() {
		return m, false
	}
	form := m.calendarForm.form
	switch {
	case key.Matches(press, m.keys.Next) && form.Focused() == form.LastFocusable():
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m.setRootFocus(rootFocusList), true
	case key.Matches(press, m.keys.Prev) && form.Focused() == form.FirstFocusable():
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m.setRootFocus(rootFocusAdd).normalizeRootFocus(), true
	}
	return m, false
}

// confirmOverlayWidth is the width budget for the discard prompt: it must fit
// inside the manager box interior on every layout.
func (m CalendarManagerModel) confirmOverlayWidth() int {
	boxW, _ := m.boxSize()
	return max(boxW-4, 20)
}

// updateDiscardConfirm owns input while the discard-changes prompt is open.
// Confirmed drops the dirty draft and pops to the root list. Any other
// input keeps the edit. Mouse clicks are swallowed. The overlay is
// keyboard-driven. A click must never reach the covered form's controls.
func (m CalendarManagerModel) updateDiscardConfirm(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	switch typed := msg.(type) {
	case ConfirmDialogResultMsg:
		m.discardConfirm = nil
		if typed.Confirmed {
			m.calendarForm = nil
			m.screen = CalendarManagerScreenList
		}
		return m, nil
	case tea.MouseClickMsg:
		return m, nil
	}
	if m.discardConfirm == nil {
		return m, nil
	}
	next, cmd := m.discardConfirm.Update(msg)
	m.discardConfirm = &next
	return m, cmd
}

// updateAccount delegates input to the pushed account detail. Account close
// (Esc or Done) returns to the calendar detail that opened it when one exists.
// A directly opened account asks the host to close the manager. Other account
// requests pass through to the host, which owns those canonical actions.
func (m CalendarManagerModel) updateAccount(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if _, ok := msg.(AccountSettingsClosedMsg); ok {
		return m.CloseAccount(), nil
	}
	if m.accountSettings == nil {
		return m.CloseAccount(), nil
	}
	next, cmd := m.accountSettings.Update(msg)
	m.accountSettings = &next
	return m.sizeActiveInspector(), cmd
}

func (m CalendarManagerModel) updateTransfer(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if _, ok := msg.(CalendarTransferClosedMsg); ok {
		return m.CloseTransfer(), nil
	}
	if m.transfer == nil {
		m.screen = CalendarManagerScreenList
		return m, nil
	}
	child, cmd := m.transfer.Update(msg)
	m.transfer = &child
	return m.sizeActiveInspector(), cmd
}
