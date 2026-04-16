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
)

// CalendarListDialogClosedMsg is emitted when the dialog requests to close.
type CalendarListDialogClosedMsg struct{}

// CalendarListDialogRequestedMsg opens the manage-calendars dialog.
type CalendarListDialogRequestedMsg struct{}

type calendarListDialogKeyMap struct {
	Up       key.Binding
	Down     key.Binding
	Close    key.Binding
	Edit     key.Binding
	Delete   key.Binding
	New      key.Binding
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
}

func (k calendarListDialogKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Tab, k.Enter, k.New, k.Close}
}

func defaultCalendarListDialogKeys() calendarListDialogKeyMap {
	return calendarListDialogKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Close:    key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
		Edit:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:   key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "delete")),
		New:      key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "sections")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev section")),
		Enter:    key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "select")),
	}
}

const (
	clZoneList = iota
	clZoneActions
)

// CalendarListDialogModel renders the list of calendars with a details pane
// and action buttons for creating, editing, and deleting calendars. It is the
// calendar-focused analogue of EventDialogModel.
type CalendarListDialogModel struct {
	calendars     map[int64]CalendarInfo
	order         []int64 // calendar IDs sorted by name
	hidden        map[int64]bool
	selected      int
	scroll        int
	focusedAction int
	focusZone     int
	keys          calendarListDialogKeyMap
	help          help.Model
	selectedColor color.Color
	mutedColor    color.Color
	width         int
	height        int
}

// NewCalendarListDialogModel builds a dialog populated from the given calendar
// map and hidden set. Calendars are sorted by name for a stable list order.
func NewCalendarListDialogModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, h help.Model) CalendarListDialogModel {
	order := sortedCalendarIDs(calendars)
	return CalendarListDialogModel{
		calendars: calendars,
		order:     order,
		hidden:    hidden,
		keys:      defaultCalendarListDialogKeys(),
		help:      h,
	}
}

func sortedCalendarIDs(calendars map[int64]CalendarInfo) []int64 {
	ids := make([]int64, 0, len(calendars))
	for id := range calendars {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		return strings.Compare(calendars[a].Name, calendars[b].Name)
	})
	return ids
}

func (m CalendarListDialogModel) SetSize(w, h int) CalendarListDialogModel {
	m.width = w
	m.height = h
	return m
}

func (m CalendarListDialogModel) SetSelectedColor(c color.Color) CalendarListDialogModel {
	m.selectedColor = c
	return m
}

func (m CalendarListDialogModel) SetMutedColor(c color.Color) CalendarListDialogModel {
	m.mutedColor = c
	return m
}

// SetCalendars replaces the calendar map and hidden set, preserving the
// selected ID when possible so edits don't jump the cursor.
func (m CalendarListDialogModel) SetCalendars(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarListDialogModel {
	var prevID int64
	if m.selected >= 0 && m.selected < len(m.order) {
		prevID = m.order[m.selected]
	}
	m.calendars = calendars
	m.hidden = hidden
	m.order = sortedCalendarIDs(calendars)
	m.selected = 0
	for i, id := range m.order {
		if id == prevID {
			m.selected = i
			break
		}
	}
	if m.selected >= len(m.order) {
		m.selected = max(len(m.order)-1, 0)
	}
	return m
}

// BoxSize returns the dialog's outer dimensions.
func (m CalendarListDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return m.boxSize()
}

func (m CalendarListDialogModel) boxSize() (int, int) {
	if m.isNarrow() {
		boxW := max(m.width-4, 20)
		boxH := max(m.height-4, 14)
		return boxW, boxH
	}
	boxW := min(max(m.width*2/3, 50), m.width-2)
	boxH := min(max(m.height*2/3, 14), m.height-2)
	return boxW, boxH
}

func (m CalendarListDialogModel) isNarrow() bool {
	return m.width < narrowThreshold
}

func (m CalendarListDialogModel) selectedID() (int64, bool) {
	if m.selected < 0 || m.selected >= len(m.order) {
		return 0, false
	}
	return m.order[m.selected], true
}

type calendarAction struct {
	label          string
	underlineIndex int
	msg            func() tea.Msg
	danger         bool
	primary        bool
}

// visibleActions returns the action buttons in render order. Edit/Delete are
// only present when a calendar is selected.
func (m CalendarListDialogModel) visibleActions() []calendarAction {
	actions := []calendarAction{
		{label: "New", underlineIndex: 0, primary: true, msg: func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} }},
	}
	id, ok := m.selectedID()
	if !ok {
		return actions
	}
	info := m.calendars[id]
	actions = append(actions,
		calendarAction{label: "Edit", underlineIndex: 0, msg: func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }},
		calendarAction{label: "Delete", underlineIndex: 0, danger: true, msg: func() tea.Msg {
			return CalendarDeleteRequestedMsg{ID: id, Name: info.Name}
		}},
	)
	return actions
}

func (m *CalendarListDialogModel) clampFocus() {
	n := len(m.visibleActions())
	if m.focusedAction >= n {
		m.focusedAction = max(n-1, 0)
	}
}

func (m CalendarListDialogModel) availableZones() []int {
	zones := []int{clZoneList}
	if len(m.visibleActions()) > 0 {
		zones = append(zones, clZoneActions)
	}
	return zones
}

func (m CalendarListDialogModel) nextZone() int {
	zones := m.availableZones()
	for i, z := range zones {
		if z == m.focusZone {
			return zones[(i+1)%len(zones)]
		}
	}
	return clZoneList
}

func (m CalendarListDialogModel) prevZone() int {
	zones := m.availableZones()
	for i, z := range zones {
		if z == m.focusZone {
			return zones[(i-1+len(zones))%len(zones)]
		}
	}
	return clZoneList
}

func (m CalendarListDialogModel) Update(msg tea.Msg) (CalendarListDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m CalendarListDialogModel) handleKey(msg tea.KeyPressMsg) (CalendarListDialogModel, tea.Cmd) {
	actions := m.visibleActions()

	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return CalendarListDialogClosedMsg{} }

	case key.Matches(msg, m.keys.Tab):
		m.focusZone = m.nextZone()

	case key.Matches(msg, m.keys.ShiftTab):
		m.focusZone = m.prevZone()

	case key.Matches(msg, m.keys.Up):
		if m.focusZone == clZoneList && m.selected > 0 {
			m.selected--
			m.clampFocus()
		}

	case key.Matches(msg, m.keys.Down):
		if m.focusZone == clZoneList && m.selected < len(m.order)-1 {
			m.selected++
			m.clampFocus()
		}

	case key.Matches(msg, m.keys.Enter):
		switch m.focusZone {
		case clZoneList:
			if _, ok := m.selectedID(); ok && len(actions) > 1 {
				return m, actions[1].msg // Edit on Enter over list
			}
			if len(m.order) == 0 {
				return m, actions[0].msg // New when list is empty
			}
		case clZoneActions:
			if m.focusedAction >= 0 && m.focusedAction < len(actions) {
				return m, actions[m.focusedAction].msg
			}
		}

	case key.Matches(msg, m.keys.New):
		if len(actions) > 0 {
			return m, actions[0].msg
		}
	case key.Matches(msg, m.keys.Edit):
		if _, ok := m.selectedID(); ok && len(actions) > 1 {
			return m, actions[1].msg
		}
	case key.Matches(msg, m.keys.Delete):
		if _, ok := m.selectedID(); ok && len(actions) > 2 {
			return m, actions[2].msg
		}
	}
	return m, nil
}

func (m CalendarListDialogModel) handleMouse(msg tea.MouseClickMsg) (CalendarListDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	if idx, ok := m.rowAtPosition(msg.X, msg.Y); ok {
		m.selected = idx
		m.focusZone = clZoneList
		m.clampFocus()
		return m, nil
	}

	actions := m.visibleActions()
	ox, oy := m.actionBarOrigin()
	if msg.Y == oy {
		x := ox
		for i, a := range actions {
			w := len(a.label) + 2
			if msg.X >= x && msg.X < x+w {
				m.focusZone = clZoneActions
				m.focusedAction = i
				return m, a.msg
			}
			x += w + 1
		}
	}

	return m, nil
}

func (m CalendarListDialogModel) rowAtPosition(x, y int) (int, bool) {
	if len(m.order) == 0 || m.width <= 0 || m.height <= 0 {
		return 0, false
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 10)
	innerH := max(boxH-4, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2
	listX := dialogX + 3
	listY := dialogY + 4
	listW := innerW
	listH := bodyH

	if m.isNarrow() {
		listH = min(max(len(m.order)+1, 3), max(bodyH/3, 3))
	} else {
		listW = listColumnWidth(innerW)
	}

	if x < listX || x >= listX+listW || y < listY || y >= listY+listH {
		return 0, false
	}

	row := y - listY
	if len(m.order) > listH && row == listH-1 {
		return 0, false
	}

	idx := m.scroll + row
	if idx < 0 || idx >= len(m.order) {
		return 0, false
	}
	return idx, true
}

func (m CalendarListDialogModel) actionBarOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 10)
	innerH := max(boxH-4, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	contentX := dialogX + 3
	actionsY := dialogY + bodyH + 3

	if m.isNarrow() {
		return contentX, actionsY
	}

	return contentX + listColumnWidth(innerW) + dialogDividerWidth, actionsY
}

func (m CalendarListDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 10)
	innerH := max(boxH-4, 6)

	title := lipgloss.NewStyle().
		Bold(true).
		Width(innerW).
		Render("Calendars")

	bodyH := max(innerH-4, 3)

	m.help.SetWidth(innerW)
	helpText := m.help.ShortHelpView(m.keys.ShortHelp())

	var body string
	if m.isNarrow() {
		body = m.viewStacked(innerW, bodyH)
	} else {
		body = m.viewColumns(innerW, bodyH)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", helpText)

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

func (m *CalendarListDialogModel) viewColumns(innerW, bodyH int) string {
	listW := listColumnWidth(innerW)
	detailsW := detailColumnWidth(innerW)

	m.adjustScroll(bodyH)
	list := m.renderList(listW, bodyH)
	divider := m.renderDivider(dialogDividerWidth, bodyH)
	details := m.renderDetails(detailsW, bodyH)

	return lipgloss.JoinHorizontal(lipgloss.Top, list, divider, details)
}

func (m *CalendarListDialogModel) viewStacked(innerW, bodyH int) string {
	listH := min(max(len(m.order)+1, 3), max(bodyH/3, 3))
	detailsH := max(bodyH-listH-1, 3)

	m.adjustScroll(listH)
	list := m.renderList(innerW, listH)
	sep := lipgloss.NewStyle().Faint(true).Width(innerW).
		Render(strings.Repeat("─", innerW))
	details := m.renderDetails(innerW, detailsH)

	return lipgloss.JoinVertical(lipgloss.Left, list, sep, details)
}

func (m *CalendarListDialogModel) adjustScroll(visibleH int) {
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+visibleH {
		m.scroll = m.selected - visibleH + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m CalendarListDialogModel) renderList(w, h int) string {
	total := len(m.order)

	visibleStart := m.scroll
	visibleEnd := min(visibleStart+h, total)

	lines := make([]string, 0, h)
	for i := visibleStart; i < visibleEnd; i++ {
		id := m.order[i]
		info := m.calendars[id]
		glyph := "●"
		if m.hidden[id] {
			glyph = "○"
		}
		swatchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color))
		label := fmt.Sprintf("%s  %s", swatchStyle.Render(glyph), info.Name)
		lines = append(lines, renderListRow(label, w, i == m.selected, m.focusZone == clZoneList, m.selectedColor))
	}

	if total > h {
		indicator := fmt.Sprintf(" %d/%d ", m.selected+1, total)
		arrows := ""
		if m.scroll > 0 {
			arrows += "▲"
		}
		if visibleEnd < total {
			if arrows != "" {
				arrows += " "
			}
			arrows += "▼"
		}
		if arrows != "" {
			indicator += arrows + " "
		}
		indicator = truncateTo(indicator, w)

		if len(lines) >= h {
			lines[h-1] = lipgloss.NewStyle().Width(w).Faint(true).Render(indicator)
		} else {
			lines = append(lines, lipgloss.NewStyle().Width(w).Faint(true).Render(indicator))
		}
	}

	return padLines(lines, w, h)
}

func (m CalendarListDialogModel) renderDivider(w, h int) string {
	bar := lipgloss.NewStyle().Faint(true).Render("│")
	pad := strings.Repeat(" ", (w-1)/2)
	rest := strings.Repeat(" ", w-len(pad)-1)
	line := pad + bar + rest
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (m CalendarListDialogModel) renderActions(w int) string {
	actions := m.visibleActions()
	parts := make([]string, len(actions))
	for i, a := range actions {
		focused := m.focusZone == clZoneActions && i == m.focusedAction
		switch {
		case a.danger:
			parts[i] = buttonDanger(a.label, a.underlineIndex, focused)
		case a.primary:
			parts[i] = buttonStyled(a.label, a.underlineIndex, focused, true)
		default:
			parts[i] = button(a.label, a.underlineIndex, focused)
		}
	}
	return truncateTo(strings.Join(parts, " "), w)
}

func (m CalendarListDialogModel) renderEmptyDetails(w, h int) string {
	faint := lipgloss.NewStyle().Faint(true)
	msg := faint.Render("No calendars yet.")
	actionsLine := m.renderActions(w)
	lines := []string{msg}
	detailsH := max(h-2, 1)
	details := padLines(lines, w, detailsH)
	return details + "\n" + actionBar(actionsLine, w)
}

func (m CalendarListDialogModel) renderDetails(w, h int) string {
	if len(m.order) == 0 {
		return m.renderEmptyDetails(w, h)
	}
	id, ok := m.selectedID()
	if !ok {
		return padLines(nil, w, h)
	}
	info := m.calendars[id]

	actionsLine := m.renderActions(w)
	detailsH := max(h-2, 1)

	lines := calendarDetailLines(info, w, m.labelWidth())
	if len(lines) > detailsH {
		lines = lines[:detailsH]
	}
	details := padLines(lines, w, detailsH)

	return details + "\n" + actionBar(actionsLine, w)
}

func (m CalendarListDialogModel) labelWidth() int {
	if m.isNarrow() {
		return 7
	}
	return 10
}

func calendarDetailLines(info CalendarInfo, w, labelWidth int) []string {
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, strings.Split(paneTitle(info.Name, w), "\n")...)
	lines = append(lines, "")

	dot := "●"
	if info.Color != "" {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color)).Render("●")
	}
	colorVal := dot
	if info.Color != "" {
		colorVal = dot + "  " + info.Color
	}
	lines = append(lines, detailLine(faint, "Color", colorVal, labelWidth, w))

	if info.OwnerEmail != "" {
		lines = append(lines, detailLine(faint, "Owner", info.OwnerEmail, labelWidth, w))
	}

	return lines
}
