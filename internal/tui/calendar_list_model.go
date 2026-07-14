package tui

import (
	"fmt"
	"image/color"
	"maps"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// CalendarVisibilityToggledMsg is emitted when the user toggles a calendar's visibility.
type CalendarVisibilityToggledMsg struct {
	ID     int64
	Hidden bool
}

// CalendarVisibilityBatchToggledMsg applies one visibility state to every
// calendar in an account group.
type CalendarVisibilityBatchToggledMsg struct {
	IDs    []int64
	Hidden bool
}

// CalendarDialogRequestedMsg is emitted when the user wants to open the
// calendar dialog. ID == 0 means "create a new calendar".
type CalendarDialogRequestedMsg struct{ ID int64 }

// SyncHealth describes a calendar's last-known sync state, used to render an
// ambient health marker in the list. It is derived from persisted sync state.
type SyncHealth int

const (
	SyncHealthNone SyncHealth = iota
	SyncHealthOK
	SyncHealthError
	SyncHealthPending
)

// CalendarListItem is the display data for a single calendar row. AccountName
// enables grouped rendering; an empty AccountName retains the legacy flat list
// used by small embedded callers and tests.
type CalendarListItem struct {
	ID          int64
	Name        string
	Color       string
	Health      SyncHealth
	Order       int64
	AccountID   int64
	AccountName string
	Access      string
	Missing     bool
}

// CalendarReorderedMsg is emitted when the user moves a calendar in the list.
// IDs is the full calendar order, excluding account headers.
type CalendarReorderedMsg struct{ IDs []int64 }

type calendarListKeyMap struct {
	Up, Down, Tab, ShiftTab key.Binding
	MoveUp, MoveDown        key.Binding
	Toggle                  key.Binding
	Open                    key.Binding
}

func defaultCalendarListKeys() calendarListKeyMap {
	return calendarListKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		MoveUp:   key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/K", "move up")),
		MoveDown: key.NewBinding(key.WithKeys("shift+down", "J"), key.WithHelp("shift+↓/J", "move down")),
		Toggle:   key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle visibility")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open or collapse")),
	}
}

type calendarListRowKind uint8

const (
	calendarRow calendarListRowKind = iota
	accountHeaderRow
)

type calendarListRow struct {
	kind        calendarListRowKind
	itemIndex   int
	accountID   int64
	accountName string
}

type calendarRowIdentity struct {
	kind calendarListRowKind
	id   int64
}

// CalendarListModel renders calendar rows grouped under collapsible account
// headers and keeps a height-aware viewport around the focused row.
type CalendarListModel struct {
	items             []CalendarListItem
	rows              []calendarListRow
	hidden            map[int64]bool
	collapsed         map[int64]bool
	grouped           bool
	cursor            int
	offset            int
	focused           bool
	width             int
	height            int
	keys              calendarListKeyMap
	accentColor       color.Color
	mutedColor        color.Color
	textColor         color.Color
	selectedTextColor color.Color
	errColor          color.Color
}

func NewCalendarListModel(items []CalendarListItem, hidden map[int64]bool) CalendarListModel {
	m := CalendarListModel{
		items:     slices.Clone(items),
		hidden:    maps.Clone(hidden),
		collapsed: make(map[int64]bool),
		keys:      defaultCalendarListKeys(),
	}
	if m.hidden == nil {
		m.hidden = make(map[int64]bool)
	}
	m.grouped = hasAccountGroups(items)
	m.rebuildRows()
	return m
}

func (m CalendarListModel) SetTheme(accent, muted, text, selectedText, errColor color.Color) CalendarListModel {
	m.accentColor = accent
	m.mutedColor = muted
	m.textColor = text
	m.selectedTextColor = selectedText
	m.errColor = errColor
	return m
}

func (m CalendarListModel) Focus() CalendarListModel { m.focused = true; return m }
func (m CalendarListModel) Blur() CalendarListModel  { m.focused = false; return m }

func (m CalendarListModel) SetWidth(w int) CalendarListModel {
	m.width = w
	return m.ensureCursorVisible()
}

func (m CalendarListModel) SetSize(w, h int) CalendarListModel {
	m.width = w
	m.height = max(0, h)
	return m.ensureCursorVisible()
}

func (m CalendarListModel) Focused() bool  { return m.focused }
func (m CalendarListModel) Cursor() int    { return m.cursor }
func (m CalendarListModel) ItemCount() int { return len(m.items) }
func (m CalendarListModel) RowCount() int  { return len(m.rows) }

// SetItems replaces the items, prunes stale hidden IDs, and clamps the cursor.
func (m CalendarListModel) SetItems(items []CalendarListItem) CalendarListModel {
	m.items = slices.Clone(items)
	m.grouped = hasAccountGroups(items)
	valid := make(map[int64]bool, len(items))
	for _, item := range items {
		valid[item.ID] = true
	}
	m.hidden = maps.Clone(m.hidden)
	for id := range m.hidden {
		if !valid[id] {
			delete(m.hidden, id)
		}
	}
	m.rebuildRows()
	return m.ensureCursorVisible()
}

// SetItemsPreservingCursor keeps the cursor on the same calendar or account
// header when a reload changes ordering.
func (m CalendarListModel) SetItemsPreservingCursor(items []CalendarListItem) CalendarListModel {
	identity, ok := m.currentIdentity()
	m = m.SetItems(items)
	if ok {
		m.selectIdentity(identity)
	}
	return m.ensureCursorVisible()
}

func (m CalendarListModel) HiddenSet() map[int64]bool { return maps.Clone(m.hidden) }

func (m CalendarListModel) moveCursor(delta int) CalendarListModel {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= m.RowCount() {
		m.cursor = m.RowCount() - 1
	}
	return m.ensureCursorVisible()
}

// moveCurrent reorders only adjacent calendars within the same account group.
func (m CalendarListModel) moveCurrent(delta int) (CalendarListModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return m, nil
	}
	current := m.rows[m.cursor]
	targetRow := m.cursor + delta
	if current.kind != calendarRow || targetRow < 0 || targetRow >= len(m.rows) {
		return m, nil
	}
	target := m.rows[targetRow]
	if target.kind != calendarRow || target.accountID != current.accountID {
		return m, nil
	}

	movedID := m.items[current.itemIndex].ID
	items := slices.Clone(m.items)
	items[current.itemIndex], items[target.itemIndex] = items[target.itemIndex], items[current.itemIndex]
	m.items = items
	m.rebuildRows()
	m.selectIdentity(calendarRowIdentity{kind: calendarRow, id: movedID})
	m = m.ensureCursorVisible()

	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return m, func() tea.Msg { return CalendarReorderedMsg{IDs: ids} }
}

func (m CalendarListModel) toggleCurrent() (CalendarListModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.cursor]
	m.hidden = maps.Clone(m.hidden)
	if row.kind == accountHeaderRow {
		ids := m.accountCalendarIDs(row.accountID)
		if len(ids) == 0 {
			return m, nil
		}
		allHidden := true
		for _, id := range ids {
			allHidden = allHidden && m.hidden[id]
		}
		hide := !allHidden
		for _, id := range ids {
			if hide {
				m.hidden[id] = true
			} else {
				delete(m.hidden, id)
			}
		}
		return m, func() tea.Msg { return CalendarVisibilityBatchToggledMsg{IDs: ids, Hidden: hide} }
	}

	id := m.items[row.itemIndex].ID
	m.hidden[id] = !m.hidden[id]
	hidden := m.hidden[id]
	return m, func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: hidden} }
}

func (m CalendarListModel) toggleCollapsed() CalendarListModel {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].kind != accountHeaderRow {
		return m
	}
	accountID := m.rows[m.cursor].accountID
	m.collapsed = maps.Clone(m.collapsed)
	m.collapsed[accountID] = !m.collapsed[accountID]
	m.rebuildRows()
	m.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: accountID})
	return m.ensureCursorVisible()
}

// HandleClick hit-tests a viewport-relative row. Account headers collapse;
// calendar rows toggle visibility.
func (m CalendarListModel) HandleClick(_ int, y int) (CalendarListModel, tea.Cmd) {
	if y < 0 || (m.height > 0 && y >= m.height) {
		return m, nil
	}
	row := m.offset + y
	if row < 0 || row >= len(m.rows) {
		return m, nil
	}
	m.cursor = row
	if m.rows[row].kind == accountHeaderRow {
		return m.toggleCollapsed(), nil
	}
	return m.toggleCurrent()
}

func (m CalendarListModel) Update(msg tea.Msg) (CalendarListModel, tea.Cmd) {
	if !m.focused {
		return m, nil
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Matches(kp, m.keys.MoveUp):
		return m.moveCurrent(-1)
	case key.Matches(kp, m.keys.MoveDown):
		return m.moveCurrent(1)
	case key.Matches(kp, m.keys.Up), key.Matches(kp, m.keys.ShiftTab):
		return m.moveCursor(-1), nil
	case key.Matches(kp, m.keys.Down), key.Matches(kp, m.keys.Tab):
		return m.moveCursor(1), nil
	case key.Matches(kp, m.keys.Toggle):
		return m.toggleCurrent()
	case key.Matches(kp, m.keys.Open):
		if m.cursor < 0 || m.cursor >= len(m.rows) {
			return m, nil
		}
		row := m.rows[m.cursor]
		if row.kind == accountHeaderRow {
			return m.toggleCollapsed(), nil
		}
		id := m.items[row.itemIndex].ID
		return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }
	}
	return m, nil
}

func (m CalendarListModel) View() string {
	start, end := m.viewportBounds()
	var b strings.Builder
	for i := start; i < end; i++ {
		row := m.rows[i]
		selected := m.focused && i == m.cursor
		if row.kind == accountHeaderRow {
			b.WriteString(m.renderAccountHeader(row, selected))
		} else {
			b.WriteString(m.renderCalendarRow(row, selected))
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m CalendarListModel) renderAccountHeader(row calendarListRow, selected bool) string {
	ids := m.accountCalendarIDs(row.accountID)
	hiddenCount := 0
	hasError := false
	for _, item := range m.items {
		if item.AccountID != row.accountID {
			continue
		}
		if m.hidden[item.ID] {
			hiddenCount++
		}
		hasError = hasError || item.Health == SyncHealthError || item.Missing
	}
	glyph := "●"
	if hiddenCount == len(ids) && len(ids) > 0 {
		glyph = "○"
	} else if hiddenCount > 0 {
		glyph = "◐"
	}
	swatch := lipgloss.NewStyle().Foreground(m.mutedColor).Render(glyph)
	arrow := "▾"
	if m.collapsed[row.accountID] {
		arrow = "▸"
	}
	marker := ""
	markerCells := 0
	if hasError {
		marker = lipgloss.NewStyle().Foreground(m.errColor).Render("⚠")
		markerCells = lipgloss.Width(marker) + 1
	}
	label := fmt.Sprintf("%s %s %d", arrow, row.accountName, len(ids))
	if avail := m.width - 4 - markerCells; m.width > 4 && avail > 0 {
		label = truncateTo(label, avail)
	}
	style := lipgloss.NewStyle().Bold(true)
	if selected {
		style = style.Reverse(true)
		if remaining := m.width - 2 - markerCells; remaining > 0 {
			style = style.Width(remaining)
		}
	}
	out := swatch + " " + style.Render(" "+label+" ")
	if marker != "" {
		out += " " + marker
	}
	return out
}

func (m CalendarListModel) renderCalendarRow(row calendarListRow, selected bool) string {
	item := m.items[row.itemIndex]
	glyph := "●"
	if m.hidden[item.ID] {
		glyph = "○"
	}
	swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(item.Color)).Render(glyph)
	indent := ""
	if m.grouped {
		indent = "  "
	}

	marker := ""
	switch {
	case item.Missing:
		marker = "[missing]"
	case item.Access == "read":
		marker = "[read-only]"
	case item.Health == SyncHealthError:
		marker = "⚠"
	}
	if marker != "" && (item.Missing || item.Health == SyncHealthError) {
		marker = lipgloss.NewStyle().Foreground(m.errColor).Render(marker)
	} else if marker != "" {
		marker = lipgloss.NewStyle().Foreground(m.mutedColor).Render(marker)
	}
	markerCells := 0
	if marker != "" {
		markerCells = lipgloss.Width(marker) + 1
	}

	nameStyle := lipgloss.NewStyle()
	if m.hidden[item.ID] && !selected {
		nameStyle = nameStyle.Foreground(m.mutedColor)
	}
	prefixCells := lipgloss.Width(indent) + 2
	if selected {
		nameStyle = nameStyle.Reverse(true).Bold(true)
		if remaining := m.width - prefixCells - markerCells; remaining > 0 {
			nameStyle = nameStyle.Width(remaining)
		}
	}
	nameText := item.Name
	if avail := m.width - prefixCells - 2 - markerCells; m.width > prefixCells+2 && avail > 0 {
		nameText = truncateTo(nameText, avail)
	}
	out := indent + swatch + " " + nameStyle.Render(" "+nameText+" ")
	if marker != "" {
		out += " " + marker
	}
	return out
}

func (m *CalendarListModel) rebuildRows() {
	rows := make([]calendarListRow, 0, len(m.items)*2)
	if !m.grouped {
		for i, item := range m.items {
			rows = append(rows, calendarListRow{kind: calendarRow, itemIndex: i, accountID: item.AccountID})
		}
		m.rows = rows
		m.clampCursor()
		return
	}

	var previousID int64
	haveGroup := false
	for i, item := range m.items {
		if !haveGroup || item.AccountID != previousID {
			name := item.AccountName
			if name == "" {
				if item.AccountID == 0 {
					name = "Local"
				} else {
					name = "Remote"
				}
			}
			rows = append(rows, calendarListRow{kind: accountHeaderRow, accountID: item.AccountID, accountName: name})
			previousID = item.AccountID
			haveGroup = true
		}
		if !m.collapsed[item.AccountID] {
			rows = append(rows, calendarListRow{kind: calendarRow, itemIndex: i, accountID: item.AccountID})
		}
	}
	m.rows = rows
	m.clampCursor()
}

func (m *CalendarListModel) clampCursor() {
	if len(m.rows) == 0 {
		m.cursor = -1
		m.offset = 0
		return
	}
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= len(m.rows) {
		m.cursor = len(m.rows) - 1
	}
}

func (m CalendarListModel) ensureCursorVisible() CalendarListModel {
	if m.cursor < 0 || m.height <= 0 {
		m.offset = 0
		return m
	}
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+m.height {
		m.offset = m.cursor - m.height + 1
	}
	maxOffset := max(0, len(m.rows)-m.height)
	if m.offset > maxOffset {
		m.offset = maxOffset
	}
	return m
}

func (m CalendarListModel) viewportBounds() (int, int) {
	if len(m.rows) == 0 {
		return 0, 0
	}
	if m.height <= 0 || m.height >= len(m.rows) {
		return 0, len(m.rows)
	}
	start := min(m.offset, len(m.rows)-1)
	return start, min(len(m.rows), start+m.height)
}

func (m CalendarListModel) currentIdentity() (calendarRowIdentity, bool) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return calendarRowIdentity{}, false
	}
	row := m.rows[m.cursor]
	if row.kind == accountHeaderRow {
		return calendarRowIdentity{kind: accountHeaderRow, id: row.accountID}, true
	}
	return calendarRowIdentity{kind: calendarRow, id: m.items[row.itemIndex].ID}, true
}

func (m *CalendarListModel) selectIdentity(identity calendarRowIdentity) {
	for i, row := range m.rows {
		if identity.kind == accountHeaderRow && row.kind == accountHeaderRow && row.accountID == identity.id {
			m.cursor = i
			return
		}
		if identity.kind == calendarRow && row.kind == calendarRow && m.items[row.itemIndex].ID == identity.id {
			m.cursor = i
			return
		}
	}
}

func (m CalendarListModel) accountCalendarIDs(accountID int64) []int64 {
	ids := make([]int64, 0)
	for _, item := range m.items {
		if item.AccountID == accountID {
			ids = append(ids, item.ID)
		}
	}
	return ids
}

func hasAccountGroups(items []CalendarListItem) bool {
	for _, item := range items {
		if item.AccountName != "" {
			return true
		}
	}
	return false
}
