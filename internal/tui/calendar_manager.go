package tui

import (
	"fmt"
	"slices"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// CalendarManagerClosedMsg is emitted when the user closes the manager
// (Esc/q) and asks the host to tear the overlay down.
type CalendarManagerClosedMsg struct{}

// CalendarManagerAddRequestedMsg is retained temporarily: app.go still routes
// it through the generic choice dialog, but the manager no longer emits it.
// The anchored Add menu emits typed CalendarManagerRequestedMsg targets
// instead. Task 2 removes this type and its app routing.
type CalendarManagerAddRequestedMsg struct{}

// CalendarManagerScreen identifies which screen the unified calendar manager
// is showing: the grouped calendar root, a pushed calendar detail, or a
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
}

func defaultCalendarManagerKeys() calendarManagerKeyMap {
	return calendarManagerKeyMap{
		Open:  key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Close: key.NewBinding(key.WithKeys("esc", "q", "C", "shift+c"), key.WithHelp("esc", "close")),
		Add:   key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
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
	// CalendarManagerTargetImport launches the iCal file import flow. Task 2
	// wires host routing; the manager only emits the typed target.
	CalendarManagerTargetImport
)

type CalendarManagerRequestedMsg struct {
	Target     CalendarManagerTarget
	CalendarID int64
	AccountID  int64
}

type CalendarManagerModel struct {
	screen CalendarManagerScreen

	calendars map[int64]CalendarInfo
	hidden    map[int64]bool
	// list is the shared grouped calendar hierarchy used by the sidebar. It
	// keeps account headers, collapse state, visibility controls, and stable
	// identity selection consistent across both surfaces.
	list CalendarListModel

	pendingSelectionID int64

	keys calendarManagerKeyMap

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
}

// NewCalendarManagerModel builds a grouped calendar manager populated from
// the given calendar map and hidden set, in canonical sidebar order.
func NewCalendarManagerModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, _ help.Model) CalendarManagerModel {
	m := CalendarManagerModel{
		screen:    CalendarManagerScreenList,
		calendars: calendars,
		hidden:    hidden,
		theme:     activeTheme,
		keys:      defaultCalendarManagerKeys(),
	}
	m.list = NewCalendarListModel(sortedCalendarListItems(calendars), hidden).
		WithCheckboxVisibility().
		SetTheme(m.theme.Selected, m.theme.Muted, m.theme.Text, m.theme.SelectedText, m.theme.Error).
		Focus()
	m = m.rebuild().sizeList()
	if len(m.list.items) > 0 {
		m = m.selectCalendar(m.list.items[0].ID)
	}
	return m
}

// Screen returns the currently active manager screen.
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
	return m
}

func (m CalendarManagerModel) Screen() CalendarManagerScreen { return m.screen }

// SetSize records the host terminal dimensions so the manager can size its
// box and viewport and keep the cursor in view.
// SetTheme updates manager-owned chrome and ensures subsequently opened child
// screens use the current terminal theme.
func (m CalendarManagerModel) SetTheme(theme Theme) CalendarManagerModel {
	m.theme = theme
	m.list = m.list.SetTheme(theme.Selected, theme.Muted, theme.Text, theme.SelectedText, theme.Error)
	return m.rebuild()
}

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
	m = m.sizeList()
	return m.sizeActiveInspector()
}

// BoxSize returns the manager shell's arithmetic outer dimensions. Child
// inspectors never introduce another modal or render as part of sizing.
func (m CalendarManagerModel) BoxSize() (int, int) { return m.boxSize() }

// SetData replaces the calendar map and hidden set, preserving the selected
// calendar and the scroll anchor by immutable ID so edits and reloads don't
// jump the cursor or scroll.
func (m CalendarManagerModel) SetData(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarManagerModel {
	identity, hadIdentity := m.list.currentIdentity()
	oldIndex := 0
	if hadIdentity && identity.kind == calendarRow {
		oldIndex = slices.IndexFunc(m.list.items, func(item CalendarListItem) bool { return item.ID == identity.id })
		oldIndex = max(oldIndex, 0)
	}

	m.calendars = calendars
	m.hidden = hidden
	m = m.rebuild()

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
	return m.sizeList()
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

// setHidden returns a copy of hidden with id set to val. Copying keeps the
// optimistic toggle off the host's (possibly shared) hidden map and is safe
// when hidden is nil.
func setHidden(hidden map[int64]bool, id int64, val bool) map[int64]bool {
	out := make(map[int64]bool, len(hidden)+1)
	for k, v := range hidden {
		if k != id {
			out[k] = v
		}
	}
	if val {
		out[id] = true
	}
	return out
}

func (m CalendarManagerModel) rebuild() CalendarManagerModel {
	m.list = m.list.SetItemsPreservingCursor(sortedCalendarListItems(m.calendars))
	for id := range m.calendars {
		m.list = m.list.SetHidden(id, m.hidden[id])
	}
	return m.sizeList()
}

func (m CalendarManagerModel) sizeList() CalendarManagerModel {
	w, h := m.rootPaneSize()
	// Reserve two source-pane rows (blank spacer + + Add action) below the
	// list viewport; the list renders into the remaining height.
	m.list = m.list.SetSize(w, max(h-2, 1)).Focus()
	return m
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

func (m CalendarManagerModel) onePaneLayout() bool {
	innerW, _ := m.managerBodySize()
	if innerW == 0 || m.width < narrowThreshold {
		return true
	}
	listW := max(innerW*2/5, 24)
	return innerW-listW < 24
}

func (m CalendarManagerModel) rootPaneSize() (int, int) {
	innerW, bodyH := m.managerBodySize()
	if m.onePaneLayout() {
		return innerW, bodyH
	}
	return max(innerW*2/5, 24), bodyH
}

func (m CalendarManagerModel) inspectorPaneSize() (int, int) {
	innerW, bodyH := m.managerBodySize()
	if m.onePaneLayout() {
		return innerW, bodyH
	}
	listW, _ := m.rootPaneSize()
	return max(innerW-listW-3, 1), bodyH
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
	// A pushed detail owns input until it closes. Each child handles its own
	// field editing; the manager intercepts only navigation: Esc/close pops
	// via the child's close message, and Left pops one child before
	// delegation (a Back gesture) — except while a text-editing field holds
	// focus in the calendar detail, where Left still moves the cursor.
	if m.screen != CalendarManagerScreenList {
		if click, ok := msg.(tea.MouseClickMsg); ok && click.Button == tea.MouseLeft {
			boxW, boxH := m.boxSize()
			ox := (m.width - boxW) / 2
			oy := (m.height - boxH) / 2
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

// popOnLeft implements the Back gesture for a pushed detail: the Left arrow
// pops one child before the message is delegated to it, so the user can drill
// out with the arrow key. It never fires at the root (root Left is unchanged)
// and is suppressed while a field owns Left for editing, including every
// direct account-connection field. The bool reports whether the message was
// consumed as a Back gesture.
func (m CalendarManagerModel) popOnLeft(msg tea.Msg) (CalendarManagerModel, tea.Cmd, bool) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok || press.Code != tea.KeyLeft {
		return m, nil, false
	}
	switch m.screen {
	case CalendarManagerScreenList, CalendarManagerScreenTransfer:
		return m, nil, false
	case CalendarManagerScreenAccountCalendars:
		return m.HideDiscovery(), nil, true
	case CalendarManagerScreenAccount:
		// Account settings opened from a calendar pop back to that unchanged
		// edit form. Direct account settings close the manager itself; exposing
		// the root calendar list here would be an unrelated navigation step.
		return m.CloseAccount(), nil, true
	case CalendarManagerScreenCalendar:
		if m.calendarForm == nil || m.calendarForm.leftMovesCursor() || m.calendarForm.accountConnection {
			return m, nil, false
		}
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, nil, true
	}
	return m, nil, false
}

func (m CalendarManagerModel) handleKey(msg tea.KeyPressMsg) (CalendarManagerModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return CalendarManagerClosedMsg{} }
	case key.Matches(msg, m.keys.Add):
		return m.openAddMenu(), nil
	case key.Matches(msg, m.keys.Open):
		identity, ok := m.list.currentIdentity()
		if ok && identity.kind == calendarRow {
			info := m.calendars[identity.id]
			return m.OpenCalendar(calendarDialogParamsFor(identity.id, info, m.hidden[identity.id])), nil
		}
	}
	next, cmd := m.list.Update(msg)
	m.list = next
	m = m.syncListProjection()
	return m, cmd
}

func (m CalendarManagerModel) syncListProjection() CalendarManagerModel {
	m.hidden = m.list.HiddenSet()
	return m
}

func (m CalendarManagerModel) handleMouse(msg tea.MouseClickMsg) (CalendarManagerModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if m.sourceAddActionActive() {
		if ax, ay, aw, ok := m.sourceAddActionRect(); ok && msg.Y == ay && msg.X >= ax && msg.X < ax+aw {
			return m.openAddMenu(), nil
		}
	}
	lx, ly, lw, lh := m.listRegion()
	if msg.X < lx || msg.X >= lx+lw || msg.Y < ly || msg.Y >= ly+lh {
		return m, nil
	}
	relX := msg.X - lx
	next, cmd := m.list.HandleClick(relX, msg.Y-ly)
	m.list = next
	m = m.syncListProjection()
	identity, selected := m.list.currentIdentity()
	indicatorEnd := m.list.visibilityIndicatorWidth()
	if m.list.grouped {
		indicatorEnd++
	}
	if selected && identity.kind == calendarRow && relX >= indicatorEnd {
		info := m.calendars[identity.id]
		return m.OpenCalendar(calendarDialogParamsFor(identity.id, info, m.hidden[identity.id])), nil
	}
	return m, cmd
}

// OpenCalendar pushes the calendar detail for the given params onto the
// stack. It is the entry point for both the root's Enter key and later app
// routing; root selection and scroll are left untouched so Back restores
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
// calendar detail, preserving the in-progress calendar draft untouched. It
// is the entry point for the calendar detail's Account opener and later app
// routing.
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
// list; CalendarVisibilityToggledMsg is mirrored into the root hidden map so
// the dot stays consistent on Back. The Account opener's
// AccountSettingsRequestedMsg is NOT intercepted here — it passes through to
// the host, which owns the canonical account record and later calls
// OpenAccount with full params. Every other domain/action message (Save, Set
// Default, Export, Delete, …) likewise passes through unchanged.
func (m CalendarManagerModel) updateCalendar(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if done, ok := msg.(calendarMutationDoneMsg); ok {
		if done.err == nil {
			m.calendarForm = nil
			m.screen = CalendarManagerScreenList
			return m, nil
		}
	}

	switch typed := msg.(type) {
	case CalendarDialogClosedMsg:
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, nil
	case CalendarVisibilityToggledMsg:
		// Mirror the detail's optimistic toggle into the root so the row's
		// checkbox is already correct when the user pops back. The host also
		// persists this message when it receives the child command.
		m.hidden = setHidden(m.hidden, typed.ID, typed.Hidden)
		m = m.rebuild()
		return m, nil
	}

	if m.calendarForm == nil {
		m.screen = CalendarManagerScreenList
		return m, nil
	}
	next, cmd := m.calendarForm.Update(msg)
	m.calendarForm = &next
	m = m.sizeActiveInspector()
	// Commands may contain timers (for example the text cursor blink). Bubble
	// Tea must execute them asynchronously; invoking them here stalls Update.
	return m, cmd
}

// updateAccount delegates input to the pushed account detail. Account close
// (Esc or Done) returns to the originating calendar detail when one exists;
// a directly opened account asks the host to close the manager. Other account
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

// calendarDialogParamsFor builds the calendar detail params for the given
// calendar from its sidebar info and current visibility. RemoteLinked is
// derived from AccountID so the detail mirrors the linked-sync context.
// ManagerEmbedded marks the detail as hosted by the manager so its
// manager-only affordances (Export) surface; legacy app-wired dialogs build
// params without it and expose no no-op actions.
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
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	title := m.renderTitleRow(innerW)
	body := m.renderManagerBody(innerW, bodyH)
	help := m.renderHelp(innerW)
	blank := strings.Repeat(" ", innerW)

	contentLines := make([]string, 0, innerH)
	contentLines = append(contentLines, title, blank)
	contentLines = append(contentLines, strings.Split(body, "\n")...)
	contentLines = append(contentLines, blank, help)
	base := mouseSweep(framedDialog(boxW, contentLines))
	if m.addMenuOpen {
		base = m.composeAddMenu(base)
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

func (m CalendarManagerModel) activeInspectorLines(w, h int) []string {
	switch m.screen {
	case CalendarManagerScreenList:
		// The root selection inspector is rendered below.
	case CalendarManagerScreenCalendar:
		if m.calendarForm != nil {
			return strings.Split(m.calendarForm.InspectorView(w, h), "\n")
		}
	case CalendarManagerScreenAccount:
		if m.accountSettings != nil {
			return strings.Split(m.accountSettings.InspectorView(w, h), "\n")
		}
	case CalendarManagerScreenAccountCalendars:
		if m.accountPicker != nil {
			return strings.Split(m.accountPicker.InspectorView(w, h), "\n")
		}
	case CalendarManagerScreenTransfer:
		if m.transfer != nil {
			return strings.Split(m.transfer.InspectorView(w, h), "\n")
		}
	}
	return m.selectionInspectorLines(w)
}

func (m CalendarManagerModel) selectionInspectorLines(w int) []string {
	identity, ok := m.list.currentIdentity()
	if !ok {
		return []string{lipgloss.NewStyle().Faint(true).Render("Select a calendar or account.")}
	}
	faint := lipgloss.NewStyle().Foreground(m.theme.Muted)
	labelWidth := min(10, max(7, w/4))
	if identity.kind == accountHeaderRow {
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
				"",
				lipgloss.NewStyle().Underline(true).Render("a  Add Calendar"),
			)
			return lines
		}
		status := "Up to date"
		if errors > 0 {
			status = lipgloss.NewStyle().Foreground(m.theme.Error).Render(fmt.Sprintf("%d need attention", errors))
		}
		return append(lines,
			detailLine(faint, "Calendars", fmt.Sprintf("%d", count), labelWidth, w),
			detailLine(faint, "Status", status, labelWidth, w),
			"",
			lipgloss.NewStyle().Underline(true).Render("Enter  Account Settings"),
		)
	}

	info, exists := m.calendars[identity.id]
	if !exists {
		return []string{lipgloss.NewStyle().Faint(true).Render("Calendar unavailable.")}
	}
	dot := lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color)).Render("●")
	visibility := "Shown"
	if m.hidden[identity.id] {
		visibility = "Hidden"
	}
	location := flatAccountContext(info)
	lines := []string{
		lipgloss.NewStyle().Bold(true).Render(truncateTo(dot+" "+info.Name, w)),
		"",
		detailLine(faint, "Display", visibility, labelWidth, w),
		detailLine(faint, "Location", location, labelWidth, w),
	}
	if info.IsDefault {
		lines = append(lines, detailLine(faint, "Default", "Yes", labelWidth, w))
	}
	if access := strings.TrimSpace(info.RemoteAccess); access != "" {
		lines = append(lines, detailLine(faint, "Access", access, labelWidth, w))
	}
	if info.Synced {
		lines = append(lines, detailLine(faint, "Last sync", formatSyncTime(info.LastSyncAt), labelWidth, w))
	}
	if info.LastSyncError != "" {
		errText := lipgloss.NewStyle().Foreground(m.theme.Error).Render(info.LastSyncError)
		lines = append(lines, detailLine(faint, "Error", errText, labelWidth, w))
	}
	if strings.TrimSpace(info.Description) != "" {
		lines = append(lines, "")
		lines = append(lines, wrapLine(info.Description, w)...)
	}
	lines = append(lines, "", lipgloss.NewStyle().Underline(true).Render("Enter  Edit Calendar"))
	return lines
}

func (m CalendarManagerModel) renderTitleRow(w int) string {
	return lipgloss.NewStyle().Bold(true).Width(w).Render("Calendars")
}

func (m CalendarManagerModel) renderHelp(w int) string {
	parts := []string{"↑↓ select", "enter details", "space show/hide", "←→ collapse", "shift+↑↓ reorder", "a add", "esc close"}
	if m.screen != CalendarManagerScreenList {
		parts = []string{"tab next", "enter activate", "esc back"}
	}
	s := truncateTo(strings.Join(parts, "  "), w)
	return lipgloss.NewStyle().Faint(true).Width(w).Align(lipgloss.Center).Render(s)
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

// listRegion returns the screen-space rect of the list column, matching the
// framedDialog layout (border + 1 left pad, then top border + top pad + title
// + blank before the first row). Used for mouse hit-testing.
func (m CalendarManagerModel) listRegion() (int, int, int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, 0
	}
	boxW, boxH := m.boxSize()
	listW, _ := m.rootPaneSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	return dialogX + addMenuContentBoxX(), dialogY + 4, listW, max(m.managerBodyHeight()-2, 1)
}

// managerBodyHeight is the body region height shared by the list viewport and
// inspector, matching rootView's layout arithmetic.
func (m CalendarManagerModel) managerBodyHeight() int {
	_, boxH := m.boxSize()
	innerH := max(boxH-3, 6)
	return max(innerH-4, 3)
}

// addMenuContentBoxX is the box-local x where source-pane content begins
// (after the border cell and the 1-space left pad).
func addMenuContentBoxX() int { return 2 }

// addMenuActionBoxY is the box-local y of the + Add action row: the last body
// row, directly above the trailing blank and help rows.
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
// is active only on the root list screen: every pushed screen (calendar
// detail, account settings, account calendars, import/export transfer) owns
// its own input, so the action is rendered muted and inert there.
func (m CalendarManagerModel) sourceAddActionActive() bool {
	return m.screen == CalendarManagerScreenList
}

// sourceAddActionRect returns the screen-space rect of the + Add label below
// the source list, for mouse hit-testing and placement assertions.
func (m CalendarManagerModel) sourceAddActionRect() (int, int, int, bool) {
	if !m.sourceAddActionRendered() {
		return 0, 0, 0, false
	}
	boxW, boxH := m.boxSize()
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	return dialogX + addMenuContentBoxX(), dialogY + addMenuActionBoxY(m), lipgloss.Width("+ Add"), true
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
// faint when muted, padded to the full column width.
func (m CalendarManagerModel) renderSourceAddAction(w int) string {
	const label = "+ Add"
	var rendered string
	if m.sourceAddActionActive() {
		rendered = lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(label)
	} else {
		rendered = lipgloss.NewStyle().Faint(true).Render(label)
	}
	return rendered + strings.Repeat(" ", max(w-lipgloss.Width(rendered), 0))
}

// flatAccountContext returns the inspector's calendar location: "Local" for
// on-device calendars, the account name for linked ones, and "Remote" when a
// linked calendar lacks a recorded account name.
func flatAccountContext(info CalendarInfo) string {
	if info.AccountID == 0 {
		return "Local"
	}
	if name := strings.TrimSpace(info.AccountName); name != "" {
		return name
	}
	return "Remote"
}
