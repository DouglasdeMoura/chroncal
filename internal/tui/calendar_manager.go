package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// CalendarManagerClosedMsg is emitted when the user closes the manager
// (Esc/q) and asks the host to tear the overlay down.
type CalendarManagerClosedMsg struct{}

// CalendarManagerScreen identifies which screen the unified calendar manager
// shows. That is the grouped calendar root, a pushed calendar detail, or a
// pushed account detail stacked on top of a calendar detail.
type CalendarManagerScreen int

const (
	// CalendarManagerScreenList is the grouped calendar hierarchy with an
	// inspector pane for the selected calendar or account.
	CalendarManagerScreenList CalendarManagerScreen = iota
	// CalendarManagerScreenCalendar is the pushed calendar detail
	// (CalendarDialogModel), reached by opening a row.
	CalendarManagerScreenCalendar
	// CalendarManagerScreenAccount is the pushed account detail
	// (AccountSettingsDialogModel), reached from a remote calendar's
	// Account opener. The originating calendar detail stays underneath.
	CalendarManagerScreenAccount
	CalendarManagerScreenAccountCalendars
	CalendarManagerScreenTransfer
)

type calendarManagerKeyMap struct {
	Open  key.Binding
	Close key.Binding
	Add   key.Binding
	// Next/Prev cycle the Apple-style root focus ring (Tab/Shift-Tab) before
	// any list child sees the key. Activate fires Enter/Space on the focused
	// source or inspector action.
	Next     key.Binding
	Prev     key.Binding
	Activate key.Binding
}

func defaultCalendarManagerKeys() calendarManagerKeyMap {
	return calendarManagerKeyMap{
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Close:    key.NewBinding(key.WithKeys("esc", "q", "C", "shift+c"), key.WithHelp("esc", "close")),
		Add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		Next:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		Prev:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Activate: key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter/space", "activate")),
	}
}

// CalendarManagerModel is the unified, calendar-first calendar manager root.
// It renders the shared grouped calendar hierarchy beside a contextual
// inspector and routes every action by immutable calendar or account ID.
type CalendarManagerTarget int

const (
	CalendarManagerTargetRoot CalendarManagerTarget = iota
	CalendarManagerTargetCalendar
	CalendarManagerTargetAccount
	CalendarManagerTargetLocalCreate
	CalendarManagerTargetAccountConnect
	// CalendarManagerTargetImport launches the iCal file import flow. The host
	// routes it to the transfer screen (OpenImport) inside the manager.
	CalendarManagerTargetImport
)

type CalendarManagerRequestedMsg struct {
	Target     CalendarManagerTarget
	CalendarID int64
	AccountID  int64
}

// calendarManagerRootFocus is the Apple-style keyboard focus target at the
// manager root. It is independent from the list's selection cursor. A cycle
// of it moves the active control (and the visual focus ring) without a move
// of the selected calendar. The list owns arrow/space/enter input only while
// it holds root focus. The + Add and inspector actions activate on Enter/Space.
type calendarManagerRootFocus int

const (
	// rootFocusList is the grouped calendar hierarchy. The zero value so a
	// freshly built manager starts with the list active.
	rootFocusList calendarManagerRootFocus = iota
	// rootFocusAdd is the compact + Add source-list action.
	rootFocusAdd
	// rootFocusInspector is the selection inspector's bottom action, which
	// exists only in wide two-pane roots whose selection has an action.
	rootFocusInspector
)

type CalendarManagerModel struct {
	screen CalendarManagerScreen
	// rootFocus is the active root keyboard focus target. It is synced to the
	// list's focused flag via applyRootFocus so the list renders and receives
	// arrow/space/enter input only while it holds root focus.
	rootFocus calendarManagerRootFocus

	calendars map[int64]CalendarInfo
	// list is the shared grouped calendar hierarchy used by the sidebar. It
	// is the single owner of the calendar visibility (hidden) set within the
	// manager. Account headers, collapse state, visibility dots, and stable
	// identity selection all read through it. There is then no second
	// mirrored hidden map to drift out of sync.
	list CalendarListModel

	pendingSelectionID int64

	keys calendarManagerKeyMap
	// help renders the footer hint line with the shared themed styles
	// (key/desc colors and " · " separators) used by every other dialog.
	help help.Model

	// addMenuOpen/addMenuCursor hold the transient anchored Add-menu state.
	// The menu is manager-local: it captures input while open and emits a
	// typed CalendarManagerRequestedMsg on selection.
	addMenuOpen   bool
	addMenuCursor int

	width, height int

	// theme builds the pushed detail dialogs so they match the host's
	// active theme; captured at construction from the package theme.
	theme Theme

	// calendarForm is the pushed calendar detail, non-nil while the screen
	// is Calendar (or underneath an Account detail). accountSettings is the
	// pushed account detail, non-nil only while the screen is Account.
	// Pushing the account detail never reconstructs the calendar form, so
	// an unsaved calendar draft survives a drill-down and Back.
	calendarForm    *CalendarDialogModel
	accountSettings *AccountSettingsDialogModel
	accountPicker   *AccountCalendarPickerModel
	transfer        *CalendarTransferDialogModel

	// discardConfirm is the centered "discard unsaved changes?" prompt shown
	// when Esc/Cancel would drop a dirty calendar draft. Non-nil only while
	// the prompt is open; it owns all input until answered.
	discardConfirm *ConfirmDialogModel
	// inspector memoizes the rendered calendar-selection preview. The
	// edit-form preview is then not rebuilt on every root render (held-arrow
	// idle, clock ticks, mouse motion, focus cycle) until one of its
	// inputs changes. It is a pointer so a value-receiver render writes
	// through to the model the runtime keeps. The manager is passed by
	// value between Update and View. A value field would then be discarded
	// with each copy. A shared pointer survives.
	inspector *inspectorPreviewCache
	// themeFP is a fingerprint of theme (see fingerprintTheme), recomputed
	// only when the theme is assigned. It enters the preview cache key so a
	// theme or style change invalidates the memo without re-scanning every
	// color on each render.
	themeFP string
}

// NewCalendarManagerModel builds a grouped calendar manager populated from
// the given calendar map and hidden set, in canonical sidebar order.
func NewCalendarManagerModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, h help.Model) CalendarManagerModel {
	m := CalendarManagerModel{
		screen:    CalendarManagerScreenList,
		calendars: calendars,
		theme:     activeTheme,
		themeFP:   fingerprintTheme(activeTheme),
		inspector: &inspectorPreviewCache{},
		keys:      defaultCalendarManagerKeys(),
		help:      h,
	}
	m.list = NewCalendarListModel(sortedCalendarListItems(calendars), hidden).
		WithoutDisclosure().
		WithInactiveSelection(m.theme.ButtonBg, oklch.ContrastingFg(m.theme.ButtonBg)).
		SetTheme(m.theme.Selected, m.theme.Muted, m.theme.Text, m.theme.SelectedText, m.theme.Error).
		Focus()
	m = m.rebuild()
	if len(m.list.items) > 0 {
		m = m.selectCalendar(m.list.items[0].ID)
	}
	return m
}

// CalendarForm returns the pushed calendar detail while it is the active
// screen.
func (m CalendarManagerModel) CalendarForm() (*CalendarDialogModel, bool) {
	if m.screen == CalendarManagerScreenCalendar && m.calendarForm != nil {
		return m.calendarForm, true
	}
	return nil, false
}

func (m CalendarManagerModel) AccountSettings() (*AccountSettingsDialogModel, bool) {
	if m.screen == CalendarManagerScreenAccount && m.accountSettings != nil {
		return m.accountSettings, true
	}
	return nil, false
}

func (m CalendarManagerModel) ActiveAccountID() int64 {
	if m.accountSettings == nil {
		return 0
	}
	return m.accountSettings.params.AccountID
}

func (m CalendarManagerModel) LocalDraft() *CalendarDialogParams {
	if m.calendarForm != nil {
		return m.calendarForm.localDraft
	}
	return nil
}

func (m CalendarManagerModel) ManagingAccountCalendars() bool {
	return m.accountPicker != nil && m.accountPicker.manage
}

func (m CalendarManagerModel) DiscoveryPicker() *AccountCalendarPickerModel {
	if m.accountPicker != nil {
		return m.accountPicker
	}
	if m.calendarForm != nil {
		return m.calendarForm.discoveryPicker
	}
	return nil
}

func (m CalendarManagerModel) Transfer() (*CalendarTransferDialogModel, bool) {
	return m.transfer, m.screen == CalendarManagerScreenTransfer && m.transfer != nil
}

func (m CalendarManagerModel) OpenImport(generation ...uint64) CalendarManagerModel {
	transfer := NewCalendarImportDialogModel(m.theme, generation...)
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) OpenExport(calendarID int64, name string, generation ...uint64) CalendarManagerModel {
	transfer := NewCalendarExportDialogModel(calendarID, name, m.theme, generation...)
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) SetTransfer(transfer CalendarTransferDialogModel) CalendarManagerModel {
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) CompleteTransfer(calendarID int64) CalendarManagerModel {
	m.transfer = nil
	m.screen = CalendarManagerScreenList
	m.pendingSelectionID = calendarID
	return m.selectCalendar(calendarID)
}

func (m CalendarManagerModel) CloseTransfer() CalendarManagerModel {
	m.transfer = nil
	if m.calendarForm != nil {
		m.screen = CalendarManagerScreenCalendar
	} else {
		m.screen = CalendarManagerScreenList
	}
	return m
}

func (m CalendarManagerModel) WithTestStatus(status lipgloss.Style, text string) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := *m.calendarForm
		cp.testStatus = status.Render(text)
		m.calendarForm = &cp
	}
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) ShowDiscovery(d account.Discovery) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := m.calendarForm.ShowDiscovery(d)
		m.calendarForm = &cp
	}
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) HideDiscovery() CalendarManagerModel {
	if m.accountPicker != nil {
		m.accountPicker = nil
		if m.accountSettings != nil {
			m.screen = CalendarManagerScreenAccount
		} else {
			m.screen = CalendarManagerScreenList
		}
	}
	if m.calendarForm != nil {
		cp := m.calendarForm.HideDiscovery()
		m.calendarForm = &cp
	}
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) SetAccountName(name string) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := m.calendarForm.SetAccountName(name)
		m.calendarForm = &cp
	}
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) FormSetError(field int, err string) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := *m.calendarForm
		cp.form.SetError(field, err)
		m.calendarForm = &cp
	}
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) CloseAccount() CalendarManagerModel {
	m.accountSettings = nil
	m.accountPicker = nil
	if m.calendarForm != nil {
		m.screen = CalendarManagerScreenCalendar
	} else {
		m.screen = CalendarManagerScreenList
	}
	return m
}

func (m CalendarManagerModel) CloseDetail() CalendarManagerModel {
	m.screen = CalendarManagerScreenList
	m.calendarForm = nil
	m.accountSettings = nil
	m.accountPicker = nil
	m.transfer = nil
	m.addMenuOpen = false
	m.discardConfirm = nil
	return m
}

func (m CalendarManagerModel) Screen() CalendarManagerScreen { return m.screen }

// SetTheme updates manager-owned chrome and ensures subsequently opened child
// screens use the current terminal theme.
func (m CalendarManagerModel) SetTheme(theme Theme) CalendarManagerModel {
	m.theme = theme
	m.themeFP = fingerprintTheme(theme)
	m.help = newThemedHelp(theme)
	m.list = m.list.SetTheme(theme.Selected, theme.Muted, theme.Text, theme.SelectedText, theme.Error).
		WithInactiveSelection(theme.ButtonBg, oklch.ContrastingFg(theme.ButtonBg))
	return m.rebuild()
}

// SetSize records the host terminal dimensions so the manager can size its
// box and viewport and keep the cursor in view.
func (m CalendarManagerModel) SetSize(w, h int) CalendarManagerModel {
	m.width, m.height = w, h
	if m.calendarForm != nil {
		next := m.calendarForm.SetSize(w, h)
		m.calendarForm = &next
	}
	if m.accountSettings != nil {
		next := m.accountSettings.SetSize(w, h)
		m.accountSettings = &next
	}
	if m.accountPicker != nil {
		next := m.accountPicker.SetInspectorSize(w, h)
		m.accountPicker = &next
	}
	if m.transfer != nil {
		next := m.transfer.SetSize(w, h)
		m.transfer = &next
	}
	if m.discardConfirm != nil {
		next := m.discardConfirm.SetSize(m.confirmOverlayWidth(), h)
		m.discardConfirm = &next
	}
	m = m.sizeList()
	return m.sizeActiveInspector().normalizeRootFocus()
}

// BoxSize returns the manager shell's arithmetic outer dimensions. Child
// inspectors never introduce another modal or render as part of sizing.
func (m CalendarManagerModel) BoxSize() (int, int) { return m.boxSize() }

// SetData replaces the calendar map and hidden set. It keeps the selected
// calendar and the scroll anchor by immutable ID. Edits and reloads then
// do not jump the cursor or scroll.
func (m CalendarManagerModel) SetData(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarManagerModel {
	identity, hadIdentity := m.list.currentIdentity()
	oldIndex := 0
	if hadIdentity && identity.kind == calendarRow {
		oldIndex = slices.IndexFunc(m.list.items, func(item CalendarListItem) bool { return item.ID == identity.id })
		oldIndex = max(oldIndex, 0)
	}

	m.calendars = calendars
	// Replace items and visibility together so the list clones the host map and
	// prunes stale IDs in one pass without a transient second projection.
	m.list = m.list.SetItemsAndHiddenPreservingCursor(sortedCalendarListItems(m.calendars), hidden)
	m = m.sizeList()

	switch {
	case !hadIdentity && len(m.list.items) > 0:
		m = m.selectCalendar(m.list.items[0].ID)
	case hadIdentity && identity.kind == calendarRow && !m.hasCalendar(identity.id) && len(m.list.items) > 0:
		fallback := min(oldIndex, len(m.list.items)-1)
		m = m.selectCalendar(m.list.items[fallback].ID)
	}
	if m.pendingSelectionID != 0 && m.hasCalendar(m.pendingSelectionID) {
		m = m.selectCalendar(m.pendingSelectionID)
		m.pendingSelectionID = 0
	}
	// rebuild already re-sized the list; selection moves don't change pane
	// geometry, so only the focus ring needs normalizing here.
	return m.normalizeRootFocus()
}

func (m CalendarManagerModel) hasCalendar(id int64) bool {
	_, ok := m.calendars[id]
	return ok
}

// selectedID returns the immutable calendar ID at the cursor.
func (m CalendarManagerModel) selectedID() (int64, bool) {
	identity, ok := m.list.currentIdentity()
	if !ok || identity.kind != calendarRow {
		return 0, false
	}
	return identity.id, true
}

// selectCalendar moves the grouped hierarchy onto the given calendar ID.
func (m CalendarManagerModel) selectCalendar(id int64) CalendarManagerModel {
	m.list.selectIdentity(calendarRowIdentity{kind: calendarRow, id: id})
	m.list = m.list.ensureCursorVisible()
	return m
}

func (m CalendarManagerModel) SelectCalendar(id int64) CalendarManagerModel {
	return m.selectCalendar(id)
}

func (m CalendarManagerModel) rebuild() CalendarManagerModel {
	m.list = m.list.SetItemsPreservingCursor(sortedCalendarListItems(m.calendars))
	return m.sizeList()
}

func (m CalendarManagerModel) sizeList() CalendarManagerModel {
	w, h := m.rootPaneSize()
	// Reserve two source-pane rows (blank spacer + + Add action) below the
	// list viewport; the list renders into the remaining height.
	m.list = m.list.SetSize(w, max(h-2, 1))
	return m.applyRootFocus()
}

// applyRootFocus mirrors rootFocus onto the shared list's focused flag. The
// list then renders its selection and receives arrow/space/enter input only
// while it holds root focus. The selection cursor itself is untouched. A
// cycle of focus never moves the selected calendar.
func (m CalendarManagerModel) applyRootFocus() CalendarManagerModel {
	if m.rootFocus == rootFocusList {
		m.list = m.list.Focus()
	} else {
		m.list = m.list.Blur()
	}
	return m
}

// setRootFocus applies a single focus target (used by mouse route) and keeps
// the list's focused flag in sync.
func (m CalendarManagerModel) setRootFocus(f calendarManagerRootFocus) CalendarManagerModel {
	m.rootFocus = f
	return m.applyRootFocus()
}

// rootFocusTargets is the ordered ring of focusable root controls for the
// current state. It always has the list and the + Add action. It also has
// the inspector pane in a wide two-pane root whose selection has an action
// pill or an edit-form preview to enter.
func (m CalendarManagerModel) rootFocusTargets() []calendarManagerRootFocus {
	targets := []calendarManagerRootFocus{rootFocusList}
	if m.sourceAddActionRendered() {
		targets = append(targets, rootFocusAdd)
	}
	if m.inspectorFocusAvailable() {
		targets = append(targets, rootFocusInspector)
	}
	return targets
}

// inspectorFocusAvailable reports whether the inspector pane is a root focus
// target. That is a wide two-pane root whose selection is a calendar that
// already exists (Tab enters its previewed edit form). Or a remote account
// with a pinned action.
func (m CalendarManagerModel) inspectorFocusAvailable() bool {
	if m.screen != CalendarManagerScreenList || m.onePaneLayout() || m.width <= 0 || m.height <= 0 {
		return false
	}
	if _, ok := m.selectionInspectorAction(); ok {
		return true
	}
	_, _, ok := m.selectedCalendar()
	return ok
}

// selectedCalendar resolves the root selection to a calendar that already
// exists: its immutable ID and info. ok is false for account, spacer, or
// gone selections.
func (m CalendarManagerModel) selectedCalendar() (int64, CalendarInfo, bool) {
	identity, ok := m.list.currentIdentity()
	if !ok || identity.kind != calendarRow {
		return 0, CalendarInfo{}, false
	}
	info, exists := m.calendars[identity.id]
	return identity.id, info, exists
}

// openSelectedCalendar pushes the edit form for a calendar row that already
// exists. It wires its current visibility into the params.
func (m CalendarManagerModel) openSelectedCalendar(id int64, info CalendarInfo) CalendarManagerModel {
	return m.OpenCalendar(calendarDialogParamsFor(id, info, m.list.IsHidden(id)))
}

// cycleRootFocus moves root focus one step around the available ring. Forward
// (Tab) visits list → + Add → inspector → list; Shift-Tab reverses it.
func (m CalendarManagerModel) cycleRootFocus(forward bool) CalendarManagerModel {
	targets := m.rootFocusTargets()
	if len(targets) == 0 {
		m.rootFocus = rootFocusList
		return m.applyRootFocus()
	}
	idx := 0
	for i, t := range targets {
		if t == m.rootFocus {
			idx = i
			break
		}
	}
	if forward {
		idx = (idx + 1) % len(targets)
	} else {
		idx = (idx - 1 + len(targets)) % len(targets)
	}
	m.rootFocus = targets[idx]
	return m.applyRootFocus()
}

// advanceRootFocus cycles the root ring one step. A land on the inspector
// while a calendar is selected opens its edit form directly. The previewed
// pane IS the form, so Tab flows into it like any other control. List
// focus is restored first so Back returns to a focused source list. Account
// selections keep the pill focus state (Enter/Space then activates it).
func (m CalendarManagerModel) advanceRootFocus(forward bool) (CalendarManagerModel, tea.Cmd) {
	m = m.cycleRootFocus(forward)
	if m.rootFocus != rootFocusInspector {
		return m, nil
	}
	id, info, ok := m.selectedCalendar()
	if !ok {
		return m, nil
	}
	return m.setRootFocus(rootFocusList).openSelectedCalendar(id, info), nil
}

// normalizeRootFocus drops root focus back to the list when its target is no
// longer available (the selection lost its inspector action). An
// unavailable control can then never hold or enter the focus ring.
func (m CalendarManagerModel) normalizeRootFocus() CalendarManagerModel {
	switch m.rootFocus {
	case rootFocusList:
		// The list is always available at the manager root.
	case rootFocusInspector:
		if !m.inspectorFocusAvailable() {
			m.rootFocus = rootFocusList
		}
	case rootFocusAdd:
		if !m.sourceAddActionRendered() {
			m.rootFocus = rootFocusList
		}
	}
	return m.applyRootFocus()
}

func (m CalendarManagerModel) managerBodySize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	return innerW, max(innerH-4, 3)
}

// sourceColumnWidth is the source-list column width in wide two-pane layout.
// It is roughly one third of the manager interior, floored at 24 so grouped
// rows and the visibility checkbox always fit. Size, render, and mouse
// hit-test all read this single value so the three stay in lockstep.
func (m CalendarManagerModel) sourceColumnWidth() int {
	innerW, _ := m.managerBodySize()
	return max(innerW/3, 24)
}

func (m CalendarManagerModel) onePaneLayout() bool {
	innerW, _ := m.managerBodySize()
	if innerW == 0 || m.width < narrowThreshold {
		return true
	}
	listW := m.sourceColumnWidth()
	return innerW-listW-3 < 24
}

func (m CalendarManagerModel) rootPaneSize() (int, int) {
	innerW, bodyH := m.managerBodySize()
	if m.onePaneLayout() {
		return innerW, bodyH
	}
	return m.sourceColumnWidth(), bodyH
}

func (m CalendarManagerModel) inspectorPaneSize() (int, int) {
	innerW, bodyH := m.managerBodySize()
	if m.onePaneLayout() {
		return innerW, bodyH
	}
	listW := m.sourceColumnWidth()
	return max(innerW-listW-3, 24), bodyH
}

func (m CalendarManagerModel) sizeActiveInspector() CalendarManagerModel {
	w, h := m.inspectorPaneSize()
	if m.calendarForm != nil {
		next := m.calendarForm.SetInspectorSize(w, h)
		m.calendarForm = &next
	}
	if m.accountPicker != nil {
		next := m.accountPicker.SetInspectorSize(w, h)
		m.accountPicker = &next
	}
	if m.transfer != nil {
		next := m.transfer.SetInspectorSize(w, h)
		m.transfer = &next
	}
	return m
}

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

// OpenCalendar pushes the calendar detail for the given params onto the
// stack. It is the entry point for both the root's Enter key and later app
// route. Root selection and scroll are left untouched so Back restores
// them by ID.
func (m CalendarManagerModel) OpenCalendar(params CalendarDialogParams) CalendarManagerModel {
	params.ManagerEmbedded = true
	form := NewCalendarDialogModel(params, m.theme)
	m.calendarForm = &form
	m.screen = CalendarManagerScreenCalendar
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) OpenAccountConnection() CalendarManagerModel {
	form := NewAccountDialogModel(m.theme)
	m.calendarForm = &form
	m.accountSettings = nil
	m.screen = CalendarManagerScreenCalendar
	return m.sizeActiveInspector()
}

// OpenAccount pushes the account detail for the given params on top of the
// calendar detail. It keeps the in-progress calendar draft untouched. It
// is the entry point for the calendar detail's Account opener and later app
// route.
func (m CalendarManagerModel) OpenAccount(params AccountSettingsParams) CalendarManagerModel {
	settings := NewAccountSettingsDialogModel(params, m.theme)
	m.accountSettings = &settings
	m.accountPicker = nil
	m.screen = CalendarManagerScreenAccount
	return m.sizeActiveInspector()
}

func (m CalendarManagerModel) OpenAccountCalendars(discovery account.Discovery) CalendarManagerModel {
	picker := NewAccountCalendarManagerModel(discovery, m.theme)
	m.accountPicker = &picker
	m.screen = CalendarManagerScreenAccountCalendars
	return m.sizeActiveInspector()
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

func calendarDialogParamsFor(id int64, info CalendarInfo, hidden bool) CalendarDialogParams {
	return CalendarDialogParams{
		ID:              id,
		AccountID:       info.AccountID,
		AccountName:     info.AccountName,
		Name:            info.Name,
		Color:           info.Color,
		Description:     info.Description,
		OwnerEmail:      info.OwnerEmail,
		RemoteLinked:    info.AccountID != 0,
		IsDefault:       info.IsDefault,
		LastSyncAt:      info.LastSyncAt,
		LastSyncError:   info.LastSyncError,
		Hidden:          hidden,
		ManagerEmbedded: true,
	}
}

func (m CalendarManagerModel) View() string { return m.rootView() }

// rootView renders the persistent grouped hierarchy and active inspector.
func (m CalendarManagerModel) rootView() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, _ := m.boxSize()
	innerW, bodyH := m.managerBodySize()

	title := m.renderTitleRow(innerW)
	body := m.renderManagerBody(innerW, bodyH)
	help := m.renderHelp(innerW)
	blank := strings.Repeat(" ", innerW)

	contentLines := make([]string, 0, bodyH+4)
	contentLines = append(contentLines, title, blank)
	contentLines = append(contentLines, strings.Split(body, "\n")...)
	contentLines = append(contentLines, blank, help)
	base := mouseSweep(framedDialog(boxW, contentLines))
	if m.addMenuOpen {
		base = m.composeAddMenu(base)
	}
	if m.discardConfirm != nil {
		base = composeCenteredOverlay(base, m.discardConfirm.View(), boxW)
	}
	return base
}

func (m CalendarManagerModel) renderManagerBody(w, h int) string {
	if m.onePaneLayout() {
		if m.screen == CalendarManagerScreenList {
			return strings.Join(m.renderSourceColumn(w, h), "\n")
		}
		return padLines(m.activeInspectorLines(w, h), w, h)
	}
	listW, _ := m.rootPaneSize()
	dividerW := 3
	detailW := max(w-listW-dividerW, 1)
	leftLines := m.renderSourceColumn(listW, h)
	right := padLines(m.activeInspectorLines(detailW, h), detailW, h)
	divider := lipgloss.NewStyle().Foreground(m.theme.Muted).Render(" │ ")
	rightRows := strings.Split(right, "\n")
	lines := make([]string, h)
	for i := range h {
		lines[i] = leftLines[i] + divider + rightRows[i]
	}
	return strings.Join(lines, "\n")
}

// footerBinding builds a display-only footer key binding whose hint label
// doubles as the key. That matches the themed help model's "key · desc"
// format shared by every dialog footer.
func footerBinding(k, desc string) key.Binding {
	return key.NewBinding(key.WithKeys(k), key.WithHelp(k, desc))
}

// managerChild is the pushed-screen contract. The screen that currently owns
// the manager's input renders its own inspector view and advertises its own
// footer help bindings. Screen→child resolve stays in activeChild. The
// footer and the inspector then cannot drift independently when a new
// screen is added. Both read the one mapping.
type managerChild interface {
	HelpBindings() []key.Binding
	InspectorView(w, h int) string
}

// activeChild returns the pushed screen that currently owns input and the
// inspector pane, or nil at the root list. It is the single source of truth
// for the screen→child mapping shared by the footer (helpBindings) and the
// inspector (activeInspectorLines). The two cannot then drift apart when a
// screen is added.
func (m CalendarManagerModel) activeChild() managerChild {
	switch m.screen {
	case CalendarManagerScreenList:
		return nil
	case CalendarManagerScreenCalendar:
		if m.calendarForm != nil {
			return m.calendarForm
		}
	case CalendarManagerScreenAccount:
		if m.accountSettings != nil {
			return m.accountSettings
		}
	case CalendarManagerScreenAccountCalendars:
		if m.accountPicker != nil {
			return m.accountPicker
		}
	case CalendarManagerScreenTransfer:
		if m.transfer != nil {
			return m.transfer
		}
	}
	return nil
}

func (m CalendarManagerModel) activeInspectorLines(w, h int) []string {
	if child := m.activeChild(); child != nil {
		return strings.Split(child.InspectorView(w, h), "\n")
	}
	return m.selectionInspectorLines(w, h)
}

// inspectorPreviewCache memoizes the calendar-selection inspector so the
// edit-form preview is not rebuilt on every root render. Held-arrow idle,
// clock ticks, mouse motion, and a focus cycle all re-render the identical
// preview until one of its inputs changes. The memo skips that rebuild. It
// lives behind a pointer on CalendarManagerModel. A value-receiver render
// (the manager is copied between Update and View) still writes through to
// the model the runtime keeps.
type inspectorPreviewCache struct {
	key   inspectorPreviewKey
	lines []string
}

// inspectorPreviewKey captures every input to the calendar-selection
// preview. CalendarDialogParams folds in the selected calendar's immutable
// ID, its display metadata, its default flag, and its sidebar visibility.
// It is a comparable value type, so equality is exact. Any field added
// to calendarDialogParamsFor then participates in invalidation.
// themeFP covers the theme/style. w and h are the inspector pane size.
// Everything else the preview depends on is read live when the key is
// built. A changed selection, resize, data reload, or visibility toggle
// then produces a different key and forces a rebuild. No separate
// invalidation hooks are needed.
type inspectorPreviewKey struct {
	params  CalendarDialogParams
	themeFP string
	w, h    int
}

// fingerprintTheme reduces a Theme to a comparable string that covers every
// color token and the calendar swatch list. color.Color is an interface, so
// RGBA() is the canonical way to tell whether two themes render identically.
// It is computed only when the manager's theme is assigned (construction and
// SetTheme). The per-render cost is a single string compare against the
// cached key, never a re-scan of the palette.
func fingerprintTheme(t Theme) string {
	var b strings.Builder
	fp := func(name string, c color.Color) {
		b.WriteString(name)
		if c == nil {
			b.WriteString("|n;")
			return
		}
		r, g, bl, a := c.RGBA()
		fmt.Fprintf(&b, "|%d,%d,%d,%d;", r, g, bl, a)
	}
	fp("primary", t.Primary)
	fp("secondary", t.Secondary)
	fp("accent", t.Accent)
	fp("muted", t.Muted)
	fp("text", t.Text)
	fp("textdim", t.TextDim)
	fp("border", t.Border)
	fp("today", t.Today)
	fp("selected", t.Selected)
	fp("selectedtext", t.SelectedText)
	fp("surface", t.Surface)
	fp("error", t.Error)
	fp("badgeok", t.BadgeOK)
	fp("badgewarn", t.BadgeWarn)
	fp("badgedanger", t.BadgeDanger)
	fp("badgeinfo", t.BadgeInfo)
	fp("badgeneutral", t.BadgeNeutral)
	fp("formlabel", t.FormLabel)
	fp("formrequired", t.FormRequired)
	fp("formerror", t.FormError)
	fp("formhighlight", t.FormHighlight)
	fp("buttonbg", t.ButtonBg)
	b.WriteString("sw:")
	for _, s := range t.CalendarSwatches {
		b.WriteString(s)
		b.WriteByte(',')
	}
	return b.String()
}

// selectionInspectorLines composes the root inspector for the current
// selection to exactly h rows. A selected calendar shows a live, unfocused
// preview of its edit form. That is the same surface Enter, Tab, or a pane
// click focuses. The editable fields then appear immediately on selection
// (macOS Settings-style master–detail). Account and empty selections keep
// the summary header plus one bottom action pinned to the final row.
func (m CalendarManagerModel) selectionInspectorLines(w, h int) []string {
	if id, info, ok := m.selectedCalendar(); ok {
		params := calendarDialogParamsFor(id, info, m.list.IsHidden(id))
		key := inspectorPreviewKey{params: params, themeFP: m.themeFP, w: w, h: h}
		if m.inspector != nil && m.inspector.key == key && len(m.inspector.lines) > 0 {
			return m.inspector.lines
		}
		preview := NewCalendarDialogModel(params, m.theme).Blur().SetInspectorSize(w, h)
		lines := strings.Split(preview.InspectorView(w, h), "\n")
		if m.inspector != nil {
			*m.inspector = inspectorPreviewCache{key: key, lines: lines}
		}
		return lines
	}
	identity, ok := m.list.currentIdentity()
	faint := lipgloss.NewStyle().Foreground(m.theme.Muted)
	labelWidth := min(10, max(7, w/4))
	action, hasAction := m.selectionInspectorAction()

	// contentLimit is the last content row; the bottom action pins to the row
	// after it when present, so long content cannot push the action off-screen.
	contentLimit := h
	if hasAction {
		contentLimit = h - 1
	}
	if contentLimit < 1 {
		contentLimit = 1
	}

	lines := m.selectionInspectorHeader(identity, ok, faint, labelWidth, w)

	for len(lines) < contentLimit {
		lines = append(lines, "")
	}
	if len(lines) > contentLimit {
		lines = lines[:contentLimit]
	}
	if hasAction {
		lines = append(lines, m.renderInspectorAction(action))
	}
	for len(lines) < h {
		lines = append(lines, "")
	}
	return lines
}

// selectionInspectorHeader builds the summary header block for account and
// empty selections: the title row, a blank, and the aligned metadata rows.
// Calendar selections render the edit-form preview instead and only fall
// through here when the selected calendar no longer exists.
func (m CalendarManagerModel) selectionInspectorHeader(identity calendarRowIdentity, ok bool, faint lipgloss.Style, labelWidth, w int) []string {
	if !ok {
		return []string{lipgloss.NewStyle().Faint(true).Render("Select a calendar or account.")}
	}
	if identity.kind == accountHeaderRow {
		return m.accountInspectorHeader(identity, faint, labelWidth, w)
	}
	return []string{lipgloss.NewStyle().Faint(true).Render("Calendar unavailable.")}
}

// accountInspectorHeader builds the account/Local header: the account name
// (or "Local"), a blank, and the aligned metadata. Local shows "On this
// device" and its calendar count; remote accounts add a sync-status line.
func (m CalendarManagerModel) accountInspectorHeader(identity calendarRowIdentity, faint lipgloss.Style, labelWidth, w int) []string {
	name := "Local"
	count := 0
	errors := 0
	for _, info := range m.calendars {
		if info.AccountID != identity.id {
			continue
		}
		count++
		if identity.id != 0 && strings.TrimSpace(info.AccountName) != "" {
			name = strings.TrimSpace(info.AccountName)
		}
		if syncHealthFor(info) == SyncHealthError || info.RemoteMissing {
			errors++
		}
	}
	lines := []string{lipgloss.NewStyle().Bold(true).Render(truncateTo(name, w)), ""}
	if identity.id == 0 {
		lines = append(lines,
			faint.Render("On this device"),
			detailLine(faint, "Calendars", fmt.Sprintf("%d", count), labelWidth, w),
		)
		return lines
	}
	status := "Up to date"
	if errors > 0 {
		status = lipgloss.NewStyle().Foreground(m.theme.Error).Render(fmt.Sprintf("%d need attention", errors))
	}
	lines = append(lines,
		detailLine(faint, "Calendars", fmt.Sprintf("%d", count), labelWidth, w),
		detailLine(faint, "Status", status, labelWidth, w),
	)
	return lines
}

// calendarManagerInspectorAction describes the selection inspector's single
// bottom action: Account Settings… for a remote account heading. Calendar
// selections render the edit-form preview instead of a pinned action, and
// Local and empty selections have none.
type calendarManagerInspectorAction struct {
	label   string
	account int64
}

// selectionInspectorAction resolves the bottom action for the current root
// selection: Account Settings… (remote accounts only) emits a typed account
// target. Calendar, Local, and empty selections have no pinned action.
func (m CalendarManagerModel) selectionInspectorAction() (calendarManagerInspectorAction, bool) {
	identity, ok := m.list.currentIdentity()
	if !ok || identity.kind != accountHeaderRow || identity.id == 0 {
		return calendarManagerInspectorAction{}, false
	}
	return calendarManagerInspectorAction{label: "Account Settings…", account: identity.id}, true
}

// renderInspectorAction renders the bottom action as a neutral pill button
// (ButtonStyles.Normal), the same style the Form action bar uses. It uses the
// focused variant while the action holds root focus.
func (m CalendarManagerModel) renderInspectorAction(action calendarManagerInspectorAction) string {
	return DefaultButtonStyles().Normal.Render(action.label, m.rootFocus == rootFocusInspector)
}

// applyInspectorAction routes a click on the bottom action: an account action
// asks the host to open account settings for the typed account ID.
func (m CalendarManagerModel) applyInspectorAction(action calendarManagerInspectorAction) (CalendarManagerModel, tea.Cmd) {
	if action.account != 0 {
		return m, func() tea.Msg {
			return CalendarManagerRequestedMsg{Target: CalendarManagerTargetAccount, AccountID: action.account}
		}
	}
	return m, nil
}

// inspectorActionRect returns the screen-space rectangle of the selection
// inspector's bottom action button, when one is rendered. The action exists
// only in wide two-pane root mode: narrow root shows the source list alone,
// and every pushed screen owns its own inspector affordances. Geometry uses
// the button's actual rendered width so hit-testing matches the pill exactly.
func (m CalendarManagerModel) inspectorActionRect() (int, int, int, bool) {
	if m.screen != CalendarManagerScreenList || m.onePaneLayout() || m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, false
	}
	action, ok := m.selectionInspectorAction()
	if !ok {
		return 0, 0, 0, false
	}
	dialogX, dialogY := m.dialogOrigin()
	listW := m.sourceColumnWidth()
	// Inspector pane begins after the border, left pad, source column, and the
	// three-cell divider; the action sits on the final body row.
	paneX := dialogX + addMenuContentBoxX() + listW + 3
	actionY := dialogY + 4 + m.managerBodyHeight() - 1
	return paneX, actionY, lipgloss.Width(m.renderInspectorAction(action)), true
}

// inspectorPaneRect returns the screen-space rectangle of the inspector pane:
// beside the source column in wide two-pane layouts, the full interior in
// one-pane layouts.
func (m CalendarManagerModel) inspectorPaneRect() (int, int, int, int) {
	dialogX, dialogY := m.dialogOrigin()
	paneX := dialogX + addMenuContentBoxX()
	if !m.onePaneLayout() {
		paneX += m.sourceColumnWidth() + 3
	}
	paneW, _ := m.inspectorPaneSize()
	return paneX, dialogY + 4, paneW, m.managerBodyHeight()
}

// previewPaneRect returns the screen-space rectangle of the root inspector
// pane while it shows a calendar edit-form preview, for mouse hit-test.
// It exists only in wide two-pane roots with a calendar already selected.
func (m CalendarManagerModel) previewPaneRect() (int, int, int, int, bool) {
	if m.screen != CalendarManagerScreenList || m.onePaneLayout() || m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, 0, false
	}
	if _, _, ok := m.selectedCalendar(); !ok {
		return 0, 0, 0, 0, false
	}
	px, py, pw, ph := m.inspectorPaneRect()
	return px, py, pw, ph, true
}

func (m CalendarManagerModel) renderTitleRow(w int) string {
	return lipgloss.NewStyle().Bold(true).Width(w).Render("Calendars")
}

// renderHelp renders the centered footer hint line through the shared themed
// help model. Key-binding hints then match every other dialog (key in Text,
// desc in TextDim, " · " separators).
func (m CalendarManagerModel) renderHelp(w int) string {
	m.help.SetWidth(w)
	bindings := m.helpBindings()
	view := m.help.ShortHelpView(bindings)
	// bubbles' short-help truncation keeps an overflowing item when the
	// ellipsis lands exactly on the width boundary. A too-wide line would wrap
	// and shear the dialog frame. Drop trailing hints until the line fits.
	for lipgloss.Width(view) > w && len(bindings) > 1 {
		bindings = bindings[:len(bindings)-1]
		view = m.help.ShortHelpView(bindings)
	}
	return lipgloss.NewStyle().Width(w).Align(lipgloss.Center).Render(view)
}

// helpBindings resolves the footer bindings for the current manager state.
// The discard prompt and Add menu own their own keys. Each pushed screen
// advertises its child's actual keys (HelpBindings). The root shows its
// ring keys plus whatever the focused control activates. Keys listed here
// are display-only. Input route is unchanged.
func (m CalendarManagerModel) helpBindings() []key.Binding {
	if m.discardConfirm != nil {
		return []key.Binding{footerBinding("tab", "switch"), footerBinding("enter", "select"), footerBinding("esc", "keep editing")}
	}
	if child := m.activeChild(); child != nil {
		return child.HelpBindings()
	}
	if m.addMenuOpen {
		return []key.Binding{footerBinding("↑↓", "select"), footerBinding("enter", "choose"), footerBinding("esc", "dismiss")}
	}
	switch m.rootFocus {
	case rootFocusAdd:
		return []key.Binding{footerBinding("tab", "next"), footerBinding("enter", "add"), footerBinding("esc", "close")}
	case rootFocusInspector:
		return []key.Binding{footerBinding("tab", "next"), footerBinding("enter", "activate"), footerBinding("esc", "close")}
	default:
		// "a add" is omitted. + Add is a visible tab stop in the root ring and
		// the accelerator keeps working. The compact set keeps esc visible at
		// the manager's minimum widths.
		return []key.Binding{footerBinding("↑↓", "select"), footerBinding("space", "toggle"), footerBinding("enter", "open"), footerBinding("tab", "next"), footerBinding("esc", "close")}
	}
}

// boxSize mirrors ListDialogModel.boxSize so the manager shares the
// golden-rectangle sizing and narrow fallback with the rest of the dialogs.
func (m CalendarManagerModel) boxSize() (int, int) {
	w, h := m.width, m.height
	if w <= 0 || h <= 0 {
		return 0, 0
	}
	if w < narrowThreshold {
		return max(w-4, 20), max(h-4, 14)
	}
	boxH := min(max(h*2/3, 14), h-2)
	boxW := int(float64(boxH) * goldenCellRatio)
	if boxW > w-2 {
		boxW = w - 2
		boxH = min(max(int(float64(boxW)/goldenCellRatio), 14), h-2)
	}
	if boxW < 50 {
		boxW = 50
	}
	return boxW, boxH
}

// listRegion returns the screen-space rect of the list column. It matches the
// framedDialog layout (border + 1 left pad, then top border + top pad + title
// + blank before the first row). Used for mouse hit-test.
func (m CalendarManagerModel) listRegion() (int, int, int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, 0
	}
	listW, _ := m.rootPaneSize()
	dialogX, dialogY := m.dialogOrigin()
	return dialogX + addMenuContentBoxX(), dialogY + 4, listW, max(m.managerBodyHeight()-2, 1)
}

// managerBodyHeight is the body region height shared by the list viewport and
// inspector. It matches rootView's layout arithmetic.
func (m CalendarManagerModel) managerBodyHeight() int {
	_, h := m.managerBodySize()
	return h
}

// dialogOrigin returns the centered box's screen-space top-left corner — the
// shared base for every mouse hit-test rectangle.
func (m CalendarManagerModel) dialogOrigin() (x, y int) {
	boxW, boxH := m.boxSize()
	return (m.width - boxW) / 2, (m.height - boxH) / 2
}

// addMenuContentBoxX is the box-local x where source-pane content begins
// (after the border cell and the 1-space left pad).
func addMenuContentBoxX() int { return 2 }

// addMenuActionBoxY is the box-local y of the + Add action row. That is the
// last body row, directly above the blank and help rows at the end.
func addMenuActionBoxY(m CalendarManagerModel) int { return 4 + m.managerBodyHeight() - 1 }

// sourceAddActionRendered reports whether the compact + Add action is drawn.
// In wide two-pane mode the source list is always visible; in narrow one-pane
// mode the action belongs to the list screen only.
func (m CalendarManagerModel) sourceAddActionRendered() bool {
	if m.width <= 0 || m.height <= 0 {
		return false
	}
	if !m.onePaneLayout() {
		return true
	}
	return m.screen == CalendarManagerScreenList
}

// sourceAddActionActive reports whether the + Add action can be activated. It
// is active only on the root list screen. Every pushed screen (calendar
// detail, account settings, account calendars, import/export transfer) owns
// its own input. The action is then rendered muted and inert there.
func (m CalendarManagerModel) sourceAddActionActive() bool {
	return m.screen == CalendarManagerScreenList
}

// sourceAddActionRect returns the screen-space rect of the + Add label below
// the source list, for mouse hit-testing and placement assertions.
func (m CalendarManagerModel) sourceAddActionRect() (int, int, int, bool) {
	if !m.sourceAddActionRendered() {
		return 0, 0, 0, false
	}
	dialogX, dialogY := m.dialogOrigin()
	return dialogX + addMenuContentBoxX(), dialogY + addMenuActionBoxY(m), lipgloss.Width(m.renderSourceAddActionCore()), true
}

// renderSourceColumn renders the source-list column: the (possibly empty)
// list viewport, a blank spacer, and the + Add action row. It always returns
// exactly h rows so it composes cleanly into the body grid.
func (m CalendarManagerModel) renderSourceColumn(w, h int) []string {
	listH := max(h-2, 1)
	var listLines []string
	if len(m.calendars) == 0 {
		hint := lipgloss.NewStyle().Faint(true).Render("No calendars yet.")
		listLines = []string{truncateTo(hint, w)}
	} else {
		listLines = strings.Split(m.list.View(), "\n")
	}
	padded := strings.Split(padLines(listLines, w, listH), "\n")
	blank := strings.Repeat(" ", w)
	out := make([]string, 0, h)
	out = append(out, padded...)
	out = append(out, blank, m.renderSourceAddAction(w))
	for len(out) < h {
		out = append(out, blank)
	}
	return out
}

// renderSourceAddAction renders the + Add label, bold/accented when active and
// faint when muted, padded to the full column width. While it holds root focus
// the label uses the neutral button focus pill so the focus ring is visible.
func (m CalendarManagerModel) renderSourceAddAction(w int) string {
	rendered := m.renderSourceAddActionCore()
	return rendered + strings.Repeat(" ", max(w-lipgloss.Width(rendered), 0))
}

// renderSourceAddActionCore renders the bare + Add label (unpadded) so both the
// column row and the mouse hit rectangle share one rendered width.
func (m CalendarManagerModel) renderSourceAddActionCore() string {
	const label = "+ Add"
	active := m.sourceAddActionActive()
	if m.rootFocus == rootFocusAdd && active {
		return DefaultButtonStyles().Normal.Render(label, true)
	}
	if active {
		return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(label)
	}
	return lipgloss.NewStyle().Faint(true).Render(label)
}
