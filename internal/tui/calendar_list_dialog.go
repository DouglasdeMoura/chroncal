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
	Edit   key.Binding
	Delete key.Binding
	New    key.Binding
	Sync   key.Binding
}

func defaultCalendarListDialogKeys() calendarListDialogKeyMap {
	return calendarListDialogKeyMap{
		Edit:   key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete: key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "delete")),
		New:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "new")),
		Sync:   key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "sync")),
	}
}

// CalendarListDialogModel renders the list of calendars with a details pane
// and action buttons for creating, editing, and deleting calendars. It is a
// thin wrapper around ListDialogModel that carries the calendar-specific
// state (sorted IDs, hidden set) and translates shell events into
// calendar-domain messages.
type CalendarListDialogModel struct {
	shell     ListDialogModel
	calendars map[int64]CalendarInfo
	order     []int64
	hidden    map[int64]bool
	keys      calendarListDialogKeyMap
}

// NewCalendarListDialogModel builds a dialog populated from the given calendar
// map and hidden set. Calendars are sorted by name for a stable list order.
func NewCalendarListDialogModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, h help.Model) CalendarListDialogModel {
	m := CalendarListDialogModel{
		shell:     NewListDialogModel(h).SetTitle("Calendars"),
		calendars: calendars,
		order:     sortedCalendarIDs(calendars),
		hidden:    hidden,
		keys:      defaultCalendarListDialogKeys(),
	}
	return m.refresh()
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
	m.shell = m.shell.SetSize(w, h)
	return m
}

func (m CalendarListDialogModel) SetSelectedColor(c color.Color) CalendarListDialogModel {
	m.shell = m.shell.SetSelectedColor(c)
	return m
}

func (m CalendarListDialogModel) SetMutedColor(color.Color) CalendarListDialogModel { return m }

// SetCalendars replaces the calendar map and hidden set, preserving the
// selected ID when possible so edits don't jump the cursor.
func (m CalendarListDialogModel) SetCalendars(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarListDialogModel {
	var prevID int64
	if idx := m.shell.Selected(); idx >= 0 && idx < len(m.order) {
		prevID = m.order[idx]
	}
	m.calendars = calendars
	m.hidden = hidden
	m.order = sortedCalendarIDs(calendars)

	newSel := 0
	for i, id := range m.order {
		if id == prevID {
			newSel = i
			break
		}
	}
	m.shell = m.shell.SetSelected(newSel)
	return m.refresh()
}

// BoxSize returns the dialog's outer dimensions.
func (m CalendarListDialogModel) BoxSize() (int, int) { return m.shell.BoxSize() }

func (m CalendarListDialogModel) selectedID() (int64, bool) {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.order) {
		return 0, false
	}
	return m.order[idx], true
}

// refresh rebuilds the shell's rows, detail lines, and actions from the
// current calendar list and selection.
func (m CalendarListDialogModel) refresh() CalendarListDialogModel {
	rows := make([]string, len(m.order))
	for i, id := range m.order {
		info := m.calendars[id]
		glyph := "●"
		if m.hidden[id] {
			glyph = "○"
		}
		swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color)).Render(glyph)
		rows[i] = fmt.Sprintf("%s  %s", swatch, info.Name)
	}
	m.shell = m.shell.SetRows(rows)

	if id, ok := m.selectedID(); ok {
		info := m.calendars[id]
		m.shell = m.shell.SetDetailLines(calendarDetailLines(info, m.detailWidth(), m.labelWidth()))
	} else {
		m.shell = m.shell.SetDetailLines(nil)
	}
	if len(m.order) == 0 {
		m.shell = m.shell.SetEmptyList("", []string{lipgloss.NewStyle().Faint(true).Render("No calendars yet.")})
	}

	m.shell = m.shell.SetActions(m.buildActions())
	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

func (m CalendarListDialogModel) buildActions() []ListDialogAction {
	actions := []ListDialogAction{
		{Label: "New", Primary: true, Msg: func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} }},
	}
	id, ok := m.selectedID()
	if !ok {
		return actions
	}
	info := m.calendars[id]
	return append(actions,
		ListDialogAction{Label: "Edit", Msg: func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }},
		ListDialogAction{Label: "Delete", Danger: true, Msg: func() tea.Msg {
			return CalendarDeleteRequestedMsg{ID: id, Name: info.Name}
		}},
	)
}

func (m CalendarListDialogModel) shortHelp() []key.Binding {
	sk := m.shell.Keys()
	return []key.Binding{sk.Up, sk.Down, sk.Tab, m.keys.New, m.keys.Edit, m.keys.Sync, sk.Close}
}

// detailWidth returns the width of the detail column for the current shell
// size, matching the shell's layout math.
func (m CalendarListDialogModel) detailWidth() int {
	boxW, _ := m.shell.BoxSize()
	innerW := max(boxW-5, 10)
	if boxW == 0 {
		return 40
	}
	if m.shell.isNarrow() {
		return innerW
	}
	return detailColumnWidth(innerW)
}

func (m CalendarListDialogModel) labelWidth() int {
	if m.shell.isNarrow() {
		return 7
	}
	return 10
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
	switch {
	case key.Matches(msg, m.keys.New):
		return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} }
	case key.Matches(msg, m.keys.Edit):
		if id, ok := m.selectedID(); ok {
			return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }
		}
		return m, nil
	case key.Matches(msg, m.keys.Delete):
		if id, ok := m.selectedID(); ok {
			info := m.calendars[id]
			return m, func() tea.Msg { return CalendarDeleteRequestedMsg{ID: id, Name: info.Name} }
		}
		return m, nil
	case key.Matches(msg, m.keys.Sync):
		if id, ok := m.selectedID(); ok {
			name := m.calendars[id].Name
			return m, func() tea.Msg { return SyncCalendarRequestedMsg{ID: id, Name: name} }
		}
		return m, nil
	}

	shell, cmd, _ := m.shell.HandleKey(msg, func() tea.Msg { return CalendarListDialogClosedMsg{} })
	m.shell = shell
	return m.refresh(), cmd
}

func (m CalendarListDialogModel) handleMouse(msg tea.MouseClickMsg) (CalendarListDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
		m.shell = m.shell.ClickRow(idx)
		return m.refresh(), nil
	}
	if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
		shell, cmd := m.shell.ClickAction(idx)
		m.shell = shell
		return m, cmd
	}
	return m, nil
}

func (m CalendarListDialogModel) View() string { return m.shell.View() }

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
