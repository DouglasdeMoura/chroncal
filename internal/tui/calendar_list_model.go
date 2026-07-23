package tui

import (
	"cmp"
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

type calendarPromotionCandidate struct {
	id   int64
	name string
}

func defaultPromotionCandidates(calendars map[int64]CalendarInfo, excludeID int64) []calendarPromotionCandidate {
	ids := sortedCalendarIDs(calendars)
	out := make([]calendarPromotionCandidate, 0, len(ids))
	for _, id := range ids {
		if id != excludeID {
			out = append(out, calendarPromotionCandidate{id: id, name: calendars[id].Name})
		}
	}
	return out
}

func compareCalendarOrder(aOrder int64, aName string, bOrder int64, bName string) int {
	return cmp.Or(cmp.Compare(aOrder, bOrder), strings.Compare(aName, bName))
}

func sortedCalendarIDs(calendars map[int64]CalendarInfo) []int64 {
	items := sortedCalendarListItems(calendars)
	ids := make([]int64, len(items))
	for i, item := range items {
		ids[i] = item.ID
	}
	return ids
}

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
	ID           int64
	Name         string
	Color        string
	Health       SyncHealth
	Order        int64
	AccountID    int64
	AccountName  string
	AccountOrder int64
	Access       string
	Missing      bool
}

// CalendarReorderedMsg is emitted when the user moves a calendar in the list.
// IDs is the full calendar order, excluding account headers.
type CalendarReorderedMsg struct{ IDs []int64 }

// AccountReorderedMsg is emitted when the user moves a complete remote-account
// section. IDs contains every remote account in final sidebar order.
type AccountReorderedMsg struct{ IDs []int64 }

type calendarListKeyMap struct {
	Up, Down, Left, Right, Tab, ShiftTab key.Binding
	MoveUp, MoveDown                     key.Binding
	Toggle                               key.Binding
	Open                                 key.Binding
}

func defaultCalendarListKeys() calendarListKeyMap {
	return calendarListKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "collapse")),
		Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "expand")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		MoveUp:   key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/K", "move up")),
		MoveDown: key.NewBinding(key.WithKeys("shift+down", "J"), key.WithHelp("shift+↓/J", "move down")),
		Toggle:   key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

type calendarListRowKind uint8

const (
	calendarRow calendarListRowKind = iota
	accountHeaderRow
	accountSpacerRow
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

	// inactiveSelection opts the list into painting its blurred cursor row with
	// a neutral background plus contrasting foreground, so a host that keeps
	// the list mounted while keyboard focus lives elsewhere (the unified
	// Calendars manager) still shows which row is selected. The default
	// sidebar never opts in and keeps its current "drop the selection when
	// blurred" appearance. The inactive colors track Theme.ButtonBg, so a
	// host must re-apply this when the theme changes.
	inactiveSelection bool
	inactiveBg        color.Color
	inactiveFg        color.Color

	// hideDisclosure drops the ▾/▸ chevron in front of account headings and
	// the header's dedicated disclosure click zone. The unified Calendars
	// manager opts in so headings read as plain section titles (collapse
	// stays reachable via ←/→/space); the sidebar keeps its chevrons.
	hideDisclosure bool
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

// WithInactiveSelection opts the list into painting its blurred cursor row
// with a neutral inactive style — quieter than the active accent but still
// visible — so a host that keeps the list mounted while keyboard focus is
// elsewhere (the unified Calendars manager) preserves a visible selection.
// The default sidebar list never opts in and keeps its current behavior of
// dropping the selection entirely when blurred. The inactive colors track
// Theme.ButtonBg, so callers must re-apply this whenever the theme changes.
func (m CalendarListModel) WithInactiveSelection(bg, fg color.Color) CalendarListModel {
	m.inactiveSelection = true
	m.inactiveBg = bg
	m.inactiveFg = fg
	return m
}

// WithoutDisclosure hides the account disclosure chevron and its header click
// zone; see the hideDisclosure field for the rationale.
func (m CalendarListModel) WithoutDisclosure() CalendarListModel {
	m.hideDisclosure = true
	return m
}

// visibilityIndicatorWidth returns the cell width of the leading visibility
// circle so mouse hit-testing matches the rendered presentation.
func (m CalendarListModel) visibilityIndicatorWidth() int {
	return lipgloss.Width("●")
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

// SetHidden applies an explicit visibility state without emitting another
// toggle message. The app uses it to keep the sidebar projection aligned when
// visibility changes originate in Manage Calendars.
func (m CalendarListModel) SetHidden(id int64, hidden bool) CalendarListModel {
	m.hidden = maps.Clone(m.hidden)
	if m.hidden == nil {
		m.hidden = make(map[int64]bool)
	}
	if hidden {
		m.hidden[id] = true
	} else {
		delete(m.hidden, id)
	}
	return m
}

func (m CalendarListModel) moveCursor(delta int) CalendarListModel {
	if len(m.rows) == 0 || delta == 0 {
		return m.ensureCursorVisible()
	}
	for next := m.cursor + delta; next >= 0 && next < len(m.rows); next += delta {
		if m.rows[next].kind != accountSpacerRow {
			m.cursor = next
			return m.ensureCursorVisible()
		}
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
func (m CalendarListModel) moveAccount(delta int) (CalendarListModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.rows) || delta == 0 {
		return m, nil
	}
	row := m.rows[m.cursor]
	if row.kind != accountHeaderRow || row.accountID == 0 {
		return m, nil
	}
	accountIDs := make([]int64, 0)
	for _, item := range m.items {
		if item.AccountID == 0 ||
			(len(accountIDs) > 0 && accountIDs[len(accountIDs)-1] == item.AccountID) {
			continue
		}
		accountIDs = append(accountIDs, item.AccountID)
	}
	current := slices.Index(accountIDs, row.accountID)
	target := current + delta
	if current < 0 || target < 0 || target >= len(accountIDs) {
		return m, nil
	}
	accountIDs[current], accountIDs[target] = accountIDs[target], accountIDs[current]

	items := make([]CalendarListItem, 0, len(m.items))
	for _, item := range m.items {
		if item.AccountID == 0 {
			items = append(items, item)
		}
	}
	for order, accountID := range accountIDs {
		for _, item := range m.items {
			if item.AccountID == accountID {
				item.AccountOrder = int64(order)
				items = append(items, item)
			}
		}
	}
	m.items = items
	m.rebuildRows()
	m.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: row.accountID})
	m = m.ensureCursorVisible()
	return m, func() tea.Msg { return AccountReorderedMsg{IDs: slices.Clone(accountIDs)} }
}

func (m CalendarListModel) moveSelected(delta int) (CalendarListModel, tea.Cmd) {
	if m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].kind == accountHeaderRow {
		return m.moveAccount(delta)
	}
	return m.moveCurrent(delta)
}

func (m CalendarListModel) toggleCurrent() (CalendarListModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.rows) {
		return m, nil
	}
	row := m.rows[m.cursor]
	if row.kind != calendarRow {
		return m, nil
	}
	m.hidden = maps.Clone(m.hidden)

	id := m.items[row.itemIndex].ID
	m.hidden[id] = !m.hidden[id]
	hidden := m.hidden[id]
	return m, func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: hidden} }
}

func (m CalendarListModel) setCollapsed(collapsed bool) CalendarListModel {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].kind != accountHeaderRow {
		return m
	}
	accountID := m.rows[m.cursor].accountID
	if m.collapsed[accountID] == collapsed {
		return m
	}
	m.collapsed = maps.Clone(m.collapsed)
	m.collapsed[accountID] = collapsed
	m.rebuildRows()
	m.selectIdentity(calendarRowIdentity{kind: accountHeaderRow, id: accountID})
	return m.ensureCursorVisible()
}

func (m CalendarListModel) toggleCollapsed() CalendarListModel {
	if m.cursor < 0 || m.cursor >= len(m.rows) || m.rows[m.cursor].kind != accountHeaderRow {
		return m
	}
	return m.setCollapsed(!m.collapsed[m.rows[m.cursor].accountID])
}

// accountActionsCmd requests the account-scoped settings panel. The account ID
// is sufficient; no representative calendar is selected or allocated.
func (m CalendarListModel) accountActionsCmd(row calendarListRow) tea.Cmd {
	if row.kind != accountHeaderRow || row.accountID == 0 {
		return nil
	}
	msg := CalendarManagerRequestedMsg{Target: CalendarManagerTargetAccount, AccountID: row.accountID}
	return func() tea.Msg { return msg }
}

// HandleClick hit-tests a viewport-relative row. The account disclosure
// control occupies the first three cells; the heading opens Account actions.
// Calendar rows toggle visibility.
func (m CalendarListModel) HandleClick(x, y int) (CalendarListModel, tea.Cmd) {
	if y < 0 || (m.height > 0 && y >= m.height) {
		return m, nil
	}
	rowIndex := m.offset + y
	if rowIndex < 0 || rowIndex >= len(m.rows) {
		return m, nil
	}
	row := m.rows[rowIndex]
	if row.kind == accountSpacerRow {
		return m, nil
	}
	if row.kind == accountHeaderRow {
		// Both hit targets select the header. The disclosure control also
		// toggles the section; clicking a remote account name opens its
		// inspector without changing collapse or visibility state. Without a
		// rendered chevron there is no disclosure zone: the whole header is
		// one target and Local headers just select.
		m.cursor = rowIndex
		if row.accountID != 0 && (m.hideDisclosure || x > 2) {
			return m, m.accountActionsCmd(row)
		}
		if m.hideDisclosure {
			return m, nil
		}
		return m.toggleCollapsed(), nil
	}
	m.cursor = rowIndex
	indentCells := 0
	if m.grouped {
		indentCells = 1
	}
	if x >= indentCells && x < indentCells+m.visibilityIndicatorWidth() {
		return m.toggleCurrent()
	}
	id := m.items[row.itemIndex].ID
	return m, func() tea.Msg {
		return CalendarManagerRequestedMsg{Target: CalendarManagerTargetCalendar, CalendarID: id}
	}
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
		return m.moveSelected(-1)
	case key.Matches(kp, m.keys.MoveDown):
		return m.moveSelected(1)
	case key.Matches(kp, m.keys.Up), key.Matches(kp, m.keys.ShiftTab):
		return m.moveCursor(-1), nil
	case key.Matches(kp, m.keys.Down), key.Matches(kp, m.keys.Tab):
		return m.moveCursor(1), nil
	case key.Matches(kp, m.keys.Left):
		return m.setCollapsed(true), nil
	case key.Matches(kp, m.keys.Right):
		return m.setCollapsed(false), nil
	case key.Matches(kp, m.keys.Toggle):
		if m.cursor >= 0 && m.cursor < len(m.rows) && m.rows[m.cursor].kind == accountHeaderRow {
			return m.toggleCollapsed(), nil
		}
		return m.toggleCurrent()
	case key.Matches(kp, m.keys.Open):
		if m.cursor < 0 || m.cursor >= len(m.rows) {
			return m, nil
		}
		row := m.rows[m.cursor]
		if row.kind == accountSpacerRow {
			return m, nil
		}
		if row.kind == accountHeaderRow {
			if row.accountID == 0 {
				return m.toggleCollapsed(), nil
			}
			return m, m.accountActionsCmd(row)
		}
		id := m.items[row.itemIndex].ID
		return m, func() tea.Msg {
			return CalendarManagerRequestedMsg{Target: CalendarManagerTargetCalendar, CalendarID: id}
		}
	}
	return m, nil
}

func (m CalendarListModel) View() string {
	start, end := m.viewportBounds()
	var b strings.Builder
	for i := start; i < end; i++ {
		row := m.rows[i]
		selected := m.focused && i == m.cursor
		inactive := !m.focused && m.inactiveSelection && i == m.cursor
		switch row.kind {
		case accountHeaderRow:
			b.WriteString(m.renderAccountHeader(row, selected, inactive))
		case calendarRow:
			b.WriteString(m.renderCalendarRow(row, selected, inactive))
		case accountSpacerRow:
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func (m CalendarListModel) renderAccountHeader(row calendarListRow, selected, inactive bool) string {
	hasError := false
	for _, item := range m.items {
		if item.AccountID != row.accountID {
			continue
		}
		hasError = hasError || item.Health == SyncHealthError || item.Missing
	}
	label := row.accountName
	if !m.hideDisclosure {
		arrow := "▾"
		if m.collapsed[row.accountID] {
			arrow = "▸"
		}
		label = arrow + " " + row.accountName
	}
	style := lipgloss.NewStyle().Foreground(m.mutedColor).Bold(true)
	markerStyle := lipgloss.NewStyle()
	switch {
	case selected:
		style = style.Foreground(m.textColor)
	case inactive:
		style = style.Background(m.inactiveBg).Foreground(m.inactiveFg)
		// The marker sits on the inactive background so the highlight is one
		// continuous bar; it keeps the error foreground below for legibility.
		markerStyle = markerStyle.Background(m.inactiveBg)
	}
	if hasError {
		markerStyle = markerStyle.Foreground(m.errColor)
	}
	marker := ""
	markerCells := 0
	if hasError {
		marker = markerStyle.Render("⚠")
		markerCells = lipgloss.Width(marker) + 1
	}
	if avail := m.width - 1 - markerCells; m.width > 1 && avail > 0 {
		label = truncateTo(label, avail)
	}
	out := style.Render(label + " ")
	if marker != "" {
		if inactive {
			// Keep the spacer on the inactive background so the highlight never
			// splits between the label and the warning glyph.
			out += style.Render(" ") + marker
		} else {
			out += " " + marker
		}
	}
	if inactive && m.width > 0 {
		if remaining := m.width - lipgloss.Width(out); remaining > 0 {
			out += style.Render(strings.Repeat(" ", remaining))
		}
		out = truncateTo(out, m.width)
	}
	return out
}

func (m CalendarListModel) renderCalendarRow(row calendarListRow, selected, inactive bool) string {
	item := m.items[row.itemIndex]
	hidden := m.hidden[item.ID]
	indent := ""
	if m.grouped {
		indent = " "
	}
	marker := ""
	if !m.grouped && (item.Health == SyncHealthError || item.Missing) {
		marker = "⚠"
	}
	markerCells := 0
	if marker != "" {
		markerCells = lipgloss.Width(marker) + 1
	}

	rowStyle := lipgloss.NewStyle()
	swatchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(item.Color))
	nameStyle := lipgloss.NewStyle()
	markerStyle := lipgloss.NewStyle().Foreground(m.errColor)
	if hidden && !selected && !inactive {
		nameStyle = nameStyle.Foreground(m.mutedColor)
	}
	if selected {
		rowStyle = rowStyle.Background(m.accentColor).Foreground(m.selectedTextColor)
		swatchStyle = swatchStyle.Background(m.accentColor)
		nameStyle = nameStyle.Background(m.accentColor).Foreground(m.selectedTextColor).Bold(true)
		markerStyle = markerStyle.Background(m.accentColor)
	} else if inactive {
		rowStyle = rowStyle.Background(m.inactiveBg).Foreground(m.inactiveFg)
		swatchStyle = swatchStyle.Background(m.inactiveBg)
		nameStyle = nameStyle.Background(m.inactiveBg).Foreground(m.inactiveFg)
		markerStyle = markerStyle.Background(m.inactiveBg)
	}

	// The visibility control leads the row: an item-colored filled (●) or
	// outline (○) circle shared by the sidebar and the Calendars manager.
	// The circle is the single visibility control; the calendar detail's
	// Display checkbox is the only checkbox presentation.
	prefixCells := lipgloss.Width(indent)
	circle := "●"
	if hidden {
		circle = "○"
	}
	leading := swatchStyle.Render(circle)
	prefixCells += 2

	nameText := item.Name
	if avail := m.width - prefixCells - 1 - markerCells; m.width > prefixCells+1 && avail > 0 {
		nameText = truncateTo(nameText, avail)
	}
	out := rowStyle.Render(indent) + leading + nameStyle.Render(" "+nameText+" ")
	if marker != "" {
		out += rowStyle.Render(" ") + markerStyle.Render(marker)
	}
	if m.width > 0 {
		if remaining := m.width - lipgloss.Width(out); remaining > 0 {
			out += rowStyle.Render(strings.Repeat(" ", remaining))
		}
		out = truncateTo(out, m.width)
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
			if haveGroup {
				rows = append(rows, calendarListRow{kind: accountSpacerRow})
			}
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
	if m.rows[m.cursor].kind == accountSpacerRow {
		for i := m.cursor + 1; i < len(m.rows); i++ {
			if m.rows[i].kind != accountSpacerRow {
				m.cursor = i
				return
			}
		}
		for i := m.cursor - 1; i >= 0; i-- {
			if m.rows[i].kind != accountSpacerRow {
				m.cursor = i
				return
			}
		}
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
	if row.kind == accountSpacerRow {
		return calendarRowIdentity{}, false
	}
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

func hasAccountGroups(items []CalendarListItem) bool {
	for _, item := range items {
		if item.AccountName != "" {
			return true
		}
	}
	return false
}
