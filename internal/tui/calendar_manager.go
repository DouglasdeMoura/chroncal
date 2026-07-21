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

// CalendarManagerAddRequestedMsg is emitted by the persistent Add action
// (the title-line button or the 'a' key) and asks the host to start the
// add-calendar flow.
type CalendarManagerAddRequestedMsg struct{}

// CalendarManagerScreen identifies which screen the unified calendar manager
// is showing: the flat list root, a pushed calendar detail, or a pushed
// account detail stacked on top of a calendar detail.
type CalendarManagerScreen int

const (
	// CalendarManagerScreenList is the calendar-first root: one flat column,
	// one physical row per calendar, no account headings or detail pane.
	CalendarManagerScreenList CalendarManagerScreen = iota
	// CalendarManagerScreenCalendar is the pushed calendar detail
	// (CalendarDialogModel), reached by opening a row.
	CalendarManagerScreenCalendar
	// CalendarManagerScreenAccount is the pushed account detail
	// (AccountSettingsDialogModel), reached from a remote calendar's
	// Account opener. The originating calendar detail stays underneath.
	CalendarManagerScreenAccount
	CalendarManagerScreenTransfer
)

type calendarManagerKeyMap struct {
	Up, Down         key.Binding
	Open             key.Binding
	Toggle           key.Binding
	MoveUp, MoveDown key.Binding
	Close            key.Binding
	Add              key.Binding
}

func defaultCalendarManagerKeys() calendarManagerKeyMap {
	return calendarManagerKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
		Toggle:   key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "hide/show")),
		MoveUp:   key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/K", "move up")),
		MoveDown: key.NewBinding(key.WithKeys("shift+down", "J"), key.WithHelp("shift+↓/J", "move down")),
		Close:    key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
		Add:      key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
	}
}

// CalendarManagerModel is the unified, calendar-first calendar manager root.
// It renders a single flat column with one row per calendar and routes every
// action at the selected calendar's immutable ID.
type CalendarManagerTarget int

const (
	CalendarManagerTargetRoot CalendarManagerTarget = iota
	CalendarManagerTargetCalendar
	CalendarManagerTargetAccount
	CalendarManagerTargetLocalCreate
	CalendarManagerTargetAccountConnect
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
	// order is the canonical top-to-bottom calendar ID order, produced by the
	// shared sortedCalendarIDs helper so this list matches the sidebar and
	// the legacy dialog exactly (Local first, then account order, then
	// in-account DisplayOrder with name as tiebreak).
	order []int64
	// rows holds the unstyled per-row labels, parallel to order, rebuilt on
	// any data/order change. The View renderer applies color and selection
	// styling on top, so cursor moves never need to rebuild these.
	rows []string

	cursor             int
	scroll             int
	pendingSelectionID int64

	keys        calendarManagerKeyMap
	titleAction *ListDialogAction // persistent Add button in the title row
	help        help.Model

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
	transfer        *CalendarTransferDialogModel
}

// NewCalendarManagerModel builds a flat calendar manager populated from the
// given calendar map and hidden set, in canonical sidebar order.
func NewCalendarManagerModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, h help.Model) CalendarManagerModel {
	m := CalendarManagerModel{
		screen:    CalendarManagerScreenList,
		calendars: calendars,
		hidden:    hidden,
		theme:     activeTheme,
		keys:      defaultCalendarManagerKeys(),
		help:      h,
		titleAction: &ListDialogAction{
			Label:   "+ Add",
			Primary: true,
			Msg:     func() tea.Msg { return CalendarManagerAddRequestedMsg{} },
		},
	}
	m.order = sortedCalendarIDs(calendars)
	return m.rebuild().ensureVisible()
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

func (m CalendarManagerModel) DiscoveryPicker() *AccountCalendarPickerModel {
	if m.calendarForm != nil {
		return m.calendarForm.discoveryPicker
	}
	return nil
}

func (m CalendarManagerModel) Transfer() (*CalendarTransferDialogModel, bool) {
	return m.transfer, m.screen == CalendarManagerScreenTransfer && m.transfer != nil
}

func (m CalendarManagerModel) OpenImport(generation ...uint64) CalendarManagerModel {
	transfer := NewCalendarImportDialogModel(m.theme, generation...).SetSize(m.width, m.height)
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m
}

func (m CalendarManagerModel) OpenExport(calendarID int64, name string, generation ...uint64) CalendarManagerModel {
	transfer := NewCalendarExportDialogModel(calendarID, name, m.theme, generation...).SetSize(m.width, m.height)
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m
}

func (m CalendarManagerModel) SetTransfer(transfer CalendarTransferDialogModel) CalendarManagerModel {
	m.transfer = &transfer
	m.screen = CalendarManagerScreenTransfer
	return m
}

func (m CalendarManagerModel) CompleteTransfer(calendarID int64) CalendarManagerModel {
	m.transfer = nil
	m.screen = CalendarManagerScreenList
	m.pendingSelectionID = calendarID
	return m.selectCalendar(calendarID).ensureVisible()
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
	return m
}

func (m CalendarManagerModel) ShowDiscovery(d account.Discovery) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := m.calendarForm.ShowDiscovery(d)
		m.calendarForm = &cp
	}
	return m
}

func (m CalendarManagerModel) HideDiscovery() CalendarManagerModel {
	if m.calendarForm != nil {
		cp := m.calendarForm.HideDiscovery()
		m.calendarForm = &cp
	}
	return m
}

func (m CalendarManagerModel) SetAccountName(name string) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := m.calendarForm.SetAccountName(name)
		m.calendarForm = &cp
	}
	return m
}

func (m CalendarManagerModel) FormSetError(field int, err string) CalendarManagerModel {
	if m.calendarForm != nil {
		cp := *m.calendarForm
		cp.form.SetError(field, err)
		m.calendarForm = &cp
	}
	return m
}

func (m CalendarManagerModel) CloseAccount() CalendarManagerModel {
	m.accountSettings = nil
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
	m.transfer = nil
	return m
}

func (m CalendarManagerModel) Screen() CalendarManagerScreen { return m.screen }

// SetSize records the host terminal dimensions so the manager can size its
// box and viewport and keep the cursor in view.
// SetTheme updates manager-owned chrome and ensures subsequently opened child
// screens use the current terminal theme.
func (m CalendarManagerModel) SetTheme(theme Theme) CalendarManagerModel {
	m.theme = theme
	m.help = newThemedHelp(theme)
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
	if m.transfer != nil {
		next := m.transfer.SetSize(w, h)
		m.transfer = &next
	}
	return m.ensureVisible()
}

// BoxSize returns the rendered dialog's outer dimensions, for host-side
// overlay placement.
func (m CalendarManagerModel) BoxSize() (int, int) {
	switch m.screen {
	case CalendarManagerScreenList:
		return m.boxSize()
	case CalendarManagerScreenCalendar:
		if m.calendarForm != nil {
			return m.calendarForm.BoxSize()
		}
	case CalendarManagerScreenAccount:
		if m.accountSettings != nil {
			return m.accountSettings.BoxSize()
		}
	case CalendarManagerScreenTransfer:
		if m.transfer != nil {
			return m.transfer.BoxSize()
		}
	}
	return m.boxSize()
}

// SetData replaces the calendar map and hidden set, preserving the selected
// calendar and the scroll anchor by immutable ID so edits and reloads don't
// jump the cursor or scroll.
func (m CalendarManagerModel) SetData(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarManagerModel {
	selID, hadSel := m.selectedID()
	_, topIdx, haveAnchor := m.scrollAnchor()

	m.calendars = calendars
	m.hidden = hidden
	m.order = sortedCalendarIDs(calendars)
	m = m.rebuild()

	if hadSel {
		m = m.selectCalendar(selID)
	}
	if m.pendingSelectionID != 0 && slices.Contains(m.order, m.pendingSelectionID) {
		m = m.selectCalendar(m.pendingSelectionID)
		m.pendingSelectionID = 0
	}
	// If the selected calendar (often the tail) disappeared, selectCalendar
	// leaves the cursor pointing at a now-out-of-range index; clamp onto a
	// surviving row so selectedID stays valid.
	m = m.clampCursor()
	if haveAnchor {
		// Try to land the same calendar at the top of the viewport; the
		// ensureVisible pass below keeps the (possibly restored) cursor in
		// view without fighting the anchor.
		if idx := slices.Index(m.order, topIdx); idx >= 0 {
			m.scroll = idx
		}
	}
	return m.ensureVisible()
}

// selectedID returns the immutable calendar ID at the cursor.
func (m CalendarManagerModel) selectedID() (int64, bool) {
	if m.cursor < 0 || m.cursor >= len(m.order) {
		return 0, false
	}
	return m.order[m.cursor], true
}

// selectCalendar moves the cursor onto the given calendar ID, if present.
func (m CalendarManagerModel) selectCalendar(id int64) CalendarManagerModel {
	if idx := slices.Index(m.order, id); idx >= 0 {
		m.cursor = idx
	}
	return m
}

func (m CalendarManagerModel) SelectCalendar(id int64) CalendarManagerModel {
	return m.selectCalendar(id)
}

// clampCursor keeps the cursor within the order slice. Used after a SetData
// refresh that drops the selected calendar (often the tail) so selectedID
// never reports an out-of-range index.
func (m CalendarManagerModel) clampCursor() CalendarManagerModel {
	switch {
	case len(m.order) == 0:
		m.cursor = 0
	case m.cursor < 0:
		m.cursor = 0
	case m.cursor >= len(m.order):
		m.cursor = len(m.order) - 1
	}
	return m
}

// setHidden returns a copy of hidden with id set to val. Copying keeps the
// optimistic toggle off the host's (possibly shared) hidden map and is safe
// when hidden is nil.
func setHidden(hidden map[int64]bool, id int64, val bool) map[int64]bool {
	out := make(map[int64]bool, len(hidden)+1)
	for k, v := range hidden {
		out[k] = v
	}
	out[id] = val
	return out
}

// scrollAnchor returns the top-visible calendar ID so SetData can preserve the
// scroll position by identity rather than by row index.
func (m CalendarManagerModel) scrollAnchor() (haveSpace bool, id int64, ok bool) {
	start, end := m.visibleRange()
	if end <= start {
		return false, 0, false
	}
	if start < 0 || start >= len(m.order) {
		return false, 0, false
	}
	return true, m.order[start], true
}

// rebuild re-renders the unstyled per-row labels from the current data and
// order. Selection styling is applied at View time, so this only needs to run
// when the data, order, or hidden set changes.
func (m CalendarManagerModel) rebuild() CalendarManagerModel {
	rows := make([]string, len(m.order))
	for i, id := range m.order {
		info := m.calendars[id]
		rows[i] = flatCalendarRowLabel(info, m.hidden[id])
	}
	m.rows = rows
	return m
}

// ensureVisible clamps the scroll offset so the cursor stays in the viewport,
// mirroring ListDialogModel.adjustScroll (which reserves the last visible row
// for the scroll indicator when the list overflows).
func (m CalendarManagerModel) ensureVisible() CalendarManagerModel {
	h := m.viewportHeight()
	if h <= 0 || len(m.order) == 0 {
		m.scroll = 0
		return m
	}
	m.scroll = m.clampedScroll(h)
	return m
}

// clampedScroll is the non-mutating scroll computation used by both
// ensureVisible (to persist) and View (to render against the current size).
func (m CalendarManagerModel) clampedScroll(h int) int {
	if len(m.order) == 0 || h <= 0 {
		return 0
	}
	contentH := h
	if len(m.order) > h && contentH > 1 {
		contentH = h - 1
	}
	s := m.scroll
	if m.cursor < s {
		s = m.cursor
	}
	if m.cursor >= s+contentH {
		s = m.cursor - contentH + 1
	}
	if s < 0 {
		s = 0
	}
	// Use contentH (not h) for the max clamp: when the list overflows the
	// last visible line holds the scroll indicator, so only contentH rows are
	// data. Clamping against h let the indicator overwrite the selected last
	// row; clamping against contentH keeps it rendered and clickable.
	if maxScroll := len(m.order) - contentH; s > maxScroll {
		s = maxScroll
	}
	if s < 0 {
		s = 0
	}
	return s
}

// viewportHeight is the number of list rows the current box can show.
func (m CalendarManagerModel) viewportHeight() int {
	_, _, _, h := m.listRegion()
	return h
}

// visibleRange returns the [start, end) calendar indices currently in view.
func (m CalendarManagerModel) visibleRange() (int, int) {
	h := m.viewportHeight()
	if h <= 0 || len(m.order) == 0 {
		return 0, 0
	}
	start := m.clampedScroll(h)
	return start, min(start+h, len(m.order))
}

func (m CalendarManagerModel) Update(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	// A pushed detail owns input until it closes. Each child handles its own
	// field editing; the manager intercepts only navigation: Esc/close pops
	// via the child's close message, and Left pops one child before
	// delegation (a Back gesture) — except while a text-editing field holds
	// focus in the calendar detail, where Left still moves the cursor.
	if m.screen != CalendarManagerScreenList {
		if popped, ok := m.popOnLeft(msg); ok {
			return popped, nil
		}
	}
	switch m.screen {
	case CalendarManagerScreenList:
		// handled by the root list below
	case CalendarManagerScreenAccount:
		return m.updateAccount(msg)
	case CalendarManagerScreenCalendar:
		return m.updateCalendar(msg)
	case CalendarManagerScreenTransfer:
		return m.updateTransfer(msg)
	}
	// Root list.
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
// and is suppressed while a text-editing field holds focus in the calendar
// detail, where Left must keep moving the cursor. The bool reports whether
// the message was consumed as a Back gesture.
func (m CalendarManagerModel) popOnLeft(msg tea.Msg) (CalendarManagerModel, bool) {
	press, ok := msg.(tea.KeyPressMsg)
	if !ok || press.Code != tea.KeyLeft {
		return m, false
	}
	switch m.screen {
	case CalendarManagerScreenList, CalendarManagerScreenTransfer:
		return m, false
	case CalendarManagerScreenAccount:
		// Account details have no text fields, so Left always pops back to
		// the originating calendar detail or the root account list.
		return m.CloseAccount(), true
	case CalendarManagerScreenCalendar:
		if m.calendarForm == nil || m.calendarForm.leftMovesCursor() {
			return m, false
		}
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, true
	}
	return m, false
}

func (m CalendarManagerModel) handleKey(msg tea.KeyPressMsg) (CalendarManagerModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return CalendarManagerClosedMsg{} }
	case key.Matches(msg, m.keys.Add):
		return m, func() tea.Msg { return CalendarManagerAddRequestedMsg{} }
	case key.Matches(msg, m.keys.Up):
		return m.moveCursor(-1), nil
	case key.Matches(msg, m.keys.Down):
		return m.moveCursor(1), nil
	case key.Matches(msg, m.keys.Open):
		if id, ok := m.selectedID(); ok {
			return m.OpenCalendar(calendarDialogParamsFor(id, m.calendars[id], m.hidden[id])), nil
		}
		return m, nil
	case key.Matches(msg, m.keys.Toggle):
		if id, ok := m.selectedID(); ok {
			// Emit the DESIRED state and apply it locally + to the rows so the
			// dot flips immediately and the host has the target state to
			// persist; relying on the host to round-trip would leave the row
			// stale until reload.
			desired := !m.hidden[id]
			m.hidden = setHidden(m.hidden, id, desired)
			m = m.rebuild()
			return m, func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: desired} }
		}
		return m, nil
	case key.Matches(msg, m.keys.MoveUp):
		return m.moveSelected(-1)
	case key.Matches(msg, m.keys.MoveDown):
		return m.moveSelected(1)
	}
	return m, nil
}

func (m CalendarManagerModel) moveCursor(delta int) CalendarManagerModel {
	if len(m.order) == 0 {
		m.cursor = 0
		return m
	}
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.order) {
		m.cursor = len(m.order) - 1
	}
	return m.ensureVisible()
}

// moveSelected swaps the selected calendar with its neighbour delta rows away
// (±1), keeps the selection on the moved calendar, and emits CalendarReorderedMsg
// with the full new top-to-bottom ID order. It only swaps within the same
// AccountID; crossing an owner boundary is a no-op so a calendar can never
// escape its account group.
func (m CalendarManagerModel) moveSelected(delta int) (CalendarManagerModel, tea.Cmd) {
	if len(m.order) == 0 {
		return m, nil
	}
	i := m.cursor
	j := i + delta
	if i < 0 || j < 0 || j >= len(m.order) {
		return m, nil
	}
	if m.calendars[m.order[i]].AccountID != m.calendars[m.order[j]].AccountID {
		return m, nil
	}
	order := slices.Clone(m.order)
	order[i], order[j] = order[j], order[i]
	m.order = order
	m.cursor = j
	m = m.rebuild().ensureVisible()
	return m, func() tea.Msg { return CalendarReorderedMsg{IDs: slices.Clone(order)} }
}

func (m CalendarManagerModel) handleMouse(msg tea.MouseClickMsg) (CalendarManagerModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if cmd, ok := m.titleActionAtPosition(msg.X, msg.Y); ok {
		return m, cmd
	}
	if idx, ok := m.rowAtPosition(msg.X, msg.Y); ok {
		m.cursor = idx
		return m.ensureVisible(), nil
	}
	return m, nil
}

// OpenCalendar pushes the calendar detail for the given params onto the
// stack. It is the entry point for both the root's Enter key and later app
// routing; root selection and scroll are left untouched so Back restores
// them by ID.
func (m CalendarManagerModel) OpenCalendar(params CalendarDialogParams) CalendarManagerModel {
	params.ManagerEmbedded = true
	form := NewCalendarDialogModel(params, m.theme).SetSize(m.width, m.height)
	m.calendarForm = &form
	m.screen = CalendarManagerScreenCalendar
	return m
}

func (m CalendarManagerModel) OpenAccountConnection() CalendarManagerModel {
	form := NewAccountDialogModel(m.theme).SetSize(m.width, m.height)
	m.calendarForm = &form
	m.accountSettings = nil
	m.screen = CalendarManagerScreenCalendar
	return m
}

// OpenAccount pushes the account detail for the given params on top of the
// calendar detail, preserving the in-progress calendar draft untouched. It
// is the entry point for the calendar detail's Account opener and later app
// routing.
func (m CalendarManagerModel) OpenAccount(params AccountSettingsParams) CalendarManagerModel {
	settings := NewAccountSettingsDialogModel(params, m.theme).SetSize(m.width, m.height)
	m.accountSettings = &settings
	m.screen = CalendarManagerScreenAccount
	return m
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

	if m.calendarForm == nil {
		m.screen = CalendarManagerScreenList
		return m, nil
	}
	next, cmd := m.calendarForm.Update(msg)
	m.calendarForm = &next
	if cmd == nil {
		return m, nil
	}
	out := cmd()
	switch typed := out.(type) {
	case CalendarDialogClosedMsg:
		m.calendarForm = nil
		m.screen = CalendarManagerScreenList
		return m, nil
	case CalendarVisibilityToggledMsg:
		// Mirror the detail's optimistic toggle into the root so the row's
		// dot is already correct when the user pops back, then forward the
		// message so the host persists it.
		m.hidden = setHidden(m.hidden, typed.ID, typed.Hidden)
		m = m.rebuild()
		return m, func() tea.Msg { return typed }
	}
	return m, func() tea.Msg { return out }
}

// updateAccount delegates input to the pushed account detail. Account close
// (Esc or Done) pops back to the originating calendar detail with its fields
// unchanged; Manage/Rename/Sign In Again/Remove requests pass through to the
// host, which owns those canonical actions.
func (m CalendarManagerModel) updateAccount(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if m.accountSettings == nil {
		return m.CloseAccount(), nil
	}
	next, cmd := m.accountSettings.Update(msg)
	m.accountSettings = &next
	if cmd == nil {
		return m, nil
	}
	out := cmd()
	if _, ok := out.(AccountSettingsClosedMsg); ok {
		return m.CloseAccount(), nil
	}
	return m, func() tea.Msg { return out }
}

// calendarDialogParamsFor builds the calendar detail params for the given
// calendar from its sidebar info and current visibility. RemoteLinked is
// derived from AccountID so the detail mirrors the linked-sync context.
// ManagerEmbedded marks the detail as hosted by the manager so its
// manager-only affordances (Export) surface; legacy app-wired dialogs build
// params without it and expose no no-op actions.
func (m CalendarManagerModel) updateTransfer(msg tea.Msg) (CalendarManagerModel, tea.Cmd) {
	if m.transfer == nil {
		m.screen = CalendarManagerScreenList
		return m, nil
	}
	child, cmd := m.transfer.Update(msg)
	m.transfer = &child
	if cmd == nil {
		return m, nil
	}
	out := cmd()
	if _, ok := out.(CalendarTransferClosedMsg); ok {
		m = m.CloseTransfer()
		return m, func() tea.Msg { return out }
	}
	return m, func() tea.Msg { return out }
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

func (m CalendarManagerModel) View() string {
	switch m.screen {
	case CalendarManagerScreenList:
		return m.rootView()
	case CalendarManagerScreenAccount:
		if m.accountSettings != nil {
			return m.accountSettings.View()
		}
	case CalendarManagerScreenCalendar:
		if m.calendarForm != nil {
			return m.calendarForm.View()
		}
	case CalendarManagerScreenTransfer:
		if m.transfer != nil {
			return m.transfer.View()
		}
	}
	return m.rootView()
}

// rootView renders the flat calendar list. It is the manager's only screen
// when no detail is pushed.
func (m CalendarManagerModel) rootView() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)

	title := m.renderTitleRow(innerW)
	body := m.renderList(innerW, bodyH)
	help := m.renderHelp(innerW)
	blank := strings.Repeat(" ", innerW)

	contentLines := make([]string, 0, innerH)
	contentLines = append(contentLines, title, blank)
	contentLines = append(contentLines, strings.Split(body, "\n")...)
	contentLines = append(contentLines, blank, help)
	return framedDialog(boxW, contentLines)
}

func (m CalendarManagerModel) renderTitleRow(w int) string {
	var button string
	if m.titleAction != nil {
		button = renderTitleActionButton(*m.titleAction, false)
	}
	titleW := max(w-lipgloss.Width(button), 0)
	title := lipgloss.NewStyle().Bold(true).Width(titleW).Render("Calendars")
	return lipgloss.JoinHorizontal(lipgloss.Top, title, button)
}

func (m CalendarManagerModel) renderHelp(w int) string {
	parts := []string{"↑↓ move", "enter open", "space hide/show", "shift+↑↓ reorder", "a add", "esc close"}
	s := truncateTo(strings.Join(parts, "  "), w)
	return lipgloss.NewStyle().Faint(true).Width(w).Align(lipgloss.Center).Render(s)
}

func (m CalendarManagerModel) renderList(w, h int) string {
	if len(m.rows) == 0 {
		msg := lipgloss.NewStyle().Faint(true).Render("No calendars yet.")
		return padLines([]string{msg}, w, h)
	}
	scroll := m.clampedScroll(h)
	total := len(m.rows)
	end := min(scroll+h, total)

	lines := make([]string, 0, h)
	for i := scroll; i < end; i++ {
		lines = append(lines, m.renderRow(m.order[i], i == m.cursor, w))
	}

	if total > h {
		var arrows string
		if scroll > 0 {
			arrows += "▲"
		}
		if end < total {
			if arrows != "" {
				arrows += " "
			}
			arrows += "▼"
		}
		indicator := fmt.Sprintf("%d/%d", m.cursor+1, total)
		if arrows != "" {
			indicator += " " + arrows
		}
		indicator = truncateTo(indicator, w)
		faint := lipgloss.NewStyle().Faint(true).Render(indicator)
		if len(lines) >= h {
			lines[h-1] = faint
		} else {
			lines = append(lines, faint)
		}
	}

	return padLines(lines, w, h)
}

// renderRow paints one calendar row: a colored dot, the bold calendar name,
// a dim owning context (Local / account name), and compact applicable state
// tags. The selected row takes reverse video across its full width so the
// highlight reads as a single bar, matching the legacy list convention.
func (m CalendarManagerModel) renderRow(id int64, selected bool, w int) string {
	info := m.calendars[id]
	hidden := m.hidden[id]

	glyph := "●"
	if hidden {
		glyph = "○"
	}
	dotFg := lipgloss.Color(info.Color)
	dotStyle := lipgloss.NewStyle().Foreground(dotFg)
	nameStyle := lipgloss.NewStyle().Bold(true)
	ctxStyle := lipgloss.NewStyle().Foreground(activeTheme.Muted)
	tagStyle := lipgloss.NewStyle().Foreground(activeTheme.Muted)
	if hidden {
		nameStyle = nameStyle.Faint(true)
	}
	if selected {
		dotStyle = lipgloss.NewStyle().Foreground(dotFg).Reverse(true)
		nameStyle = lipgloss.NewStyle().Reverse(true).Bold(true)
		ctxStyle = lipgloss.NewStyle().Reverse(true)
		tagStyle = lipgloss.NewStyle().Reverse(true)
	}

	out := dotStyle.Render(glyph) + " " +
		nameStyle.Render(info.Name) + " " +
		ctxStyle.Render(flatAccountContext(info))
	if tags := flatStateTags(info); len(tags) > 0 {
		out += " " + tagStyle.Render(strings.Join(tags, " "))
	}
	if selected {
		if rem := w - lipgloss.Width(out); rem > 0 {
			out += lipgloss.NewStyle().Reverse(true).Render(strings.Repeat(" ", rem))
		}
	}
	return out
}

// boxSize mirrors ListDialogModel.boxSize so the flat manager shares the
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
	innerW := max(boxW-5, 10)
	innerH := max(boxH-3, 6)
	bodyH := max(innerH-4, 3)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	return dialogX + 2, dialogY + 4, innerW, bodyH
}

func (m CalendarManagerModel) rowAtPosition(x, y int) (int, bool) {
	lx, ly, lw, lh := m.listRegion()
	if len(m.order) == 0 || lh <= 0 || lw <= 0 {
		return 0, false
	}
	// Bound the list column on both sides — a click on the right row's Y but
	// past the list's right edge (or outside the dialog entirely) must not
	// select.
	if y < ly || y >= ly+lh || x < lx || x >= lx+lw {
		return 0, false
	}
	row := y - ly
	// When the list overflows, renderList reserves the last visible line for
	// the scroll indicator, so it is not a clickable row.
	if len(m.order) > lh && row == lh-1 {
		return 0, false
	}
	idx := m.clampedScroll(lh) + row
	if idx < 0 || idx >= len(m.order) {
		return 0, false
	}
	return idx, true
}

// titleActionRect returns the screen-space rect of the persistent Add button,
// for mouse hit-testing. Absent when there is no title action.
func (m CalendarManagerModel) titleActionRect() (int, int, int, bool) {
	if m.titleAction == nil || m.width <= 0 || m.height <= 0 {
		return 0, 0, 0, false
	}
	boxW, boxH := m.boxSize()
	innerW := max(boxW-5, 10)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	btnW := lipgloss.Width(renderTitleActionButton(*m.titleAction, false))
	btnX := dialogX + 2 + innerW - btnW
	return btnX, dialogY + 2, btnW, true
}

func (m CalendarManagerModel) titleActionAtPosition(x, y int) (tea.Cmd, bool) {
	bx, by, bw, ok := m.titleActionRect()
	if !ok || y != by || x < bx || x >= bx+bw {
		return nil, false
	}
	return m.titleAction.Msg, true
}

// flatCalendarRowLabel builds the unstyled, ANSI-free row label: dot, name,
// owning context, and applicable state tags. Selection styling is layered on
// at render time.
func flatCalendarRowLabel(info CalendarInfo, hidden bool) string {
	glyph := "●"
	if hidden {
		glyph = "○"
	}
	tags := flatStateTags(info)
	parts := make([]string, 0, 3+len(tags))
	parts = append(parts, glyph, info.Name, flatAccountContext(info))
	parts = append(parts, tags...)
	return strings.Join(parts, " ")
}

// flatAccountContext returns the dim row suffix naming the row's owner:
// "Local" for on-device calendars, the account name for linked ones, and
// "Remote" when a linked calendar lacks a recorded account name. This mirrors
// the legacy dialog's heading text but inlines it per row instead of emitting
// a structural heading.
func flatAccountContext(info CalendarInfo) string {
	if info.AccountID == 0 {
		return "Local"
	}
	if name := strings.TrimSpace(info.AccountName); name != "" {
		return name
	}
	return "Remote"
}

// flatStateTags returns the compact applicable-state tags for a calendar, in a
// stable order: default, read-only, sync error, remote missing. Hidden state
// is encoded by the hollow dot rather than a word tag, matching the sidebar
// and legacy dialog convention.
func flatStateTags(info CalendarInfo) []string {
	var tags []string
	if info.IsDefault {
		tags = append(tags, "default")
	}
	if strings.EqualFold(strings.TrimSpace(info.RemoteAccess), "read") {
		tags = append(tags, "read-only")
	}
	if syncHealthFor(info) == SyncHealthError {
		tags = append(tags, "sync error")
	}
	if info.RemoteMissing {
		tags = append(tags, "missing")
	}
	return tags
}
