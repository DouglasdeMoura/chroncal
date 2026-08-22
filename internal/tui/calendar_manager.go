package tui

import (
	"slices"

	"charm.land/bubbles/v2/help"
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
