package tui

import (
	"fmt"
	"image/color"
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

// CalendarDialogRequestedMsg is emitted when the user wants to open the
// calendar dialog. ID == 0 means "create a new calendar".
type CalendarDialogRequestedMsg struct{ ID int64 }

// CalendarListItem is the display data for a single row.
type CalendarListItem struct {
	ID    int64
	Name  string
	Color string // hex like "#a6e3a1"
}

type calendarListKeyMap struct {
	Up, Down, Tab, ShiftTab key.Binding
	Toggle, Open            key.Binding
}

func defaultCalendarListKeys() calendarListKeyMap {
	return calendarListKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Toggle:   key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "toggle visibility")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open dialog")),
	}
}

// CalendarListModel renders a list of calendars (color swatch, name, visibility
// indicator) followed by a "+ Add calendar" row. The cursor index spans both;
// cursor == len(items) means the Add row is selected.
type CalendarListModel struct {
	items       []CalendarListItem
	hidden      map[int64]bool
	cursor      int
	focused     bool
	keys        calendarListKeyMap
	accentColor color.Color
	mutedColor  color.Color
	textColor   color.Color
}

func NewCalendarListModel(items []CalendarListItem, hidden map[int64]bool) CalendarListModel {
	h := make(map[int64]bool, len(hidden))
	for k, v := range hidden {
		h[k] = v
	}
	return CalendarListModel{
		items:  items,
		hidden: h,
		keys:   defaultCalendarListKeys(),
	}
}

func (m CalendarListModel) SetTheme(accent, muted, text color.Color) CalendarListModel {
	m.accentColor = accent
	m.mutedColor = muted
	m.textColor = text
	return m
}

func (m CalendarListModel) Focus() CalendarListModel { m.focused = true; return m }
func (m CalendarListModel) Blur() CalendarListModel  { m.focused = false; return m }
func (m CalendarListModel) Focused() bool            { return m.focused }
func (m CalendarListModel) Cursor() int              { return m.cursor }
func (m CalendarListModel) ItemCount() int           { return len(m.items) }

// RowCount includes the trailing "+ Add" row.
func (m CalendarListModel) RowCount() int { return len(m.items) + 1 }

// SetItems replaces the items. Clamps cursor to the new range and prunes the
// hidden set of any IDs no longer present.
func (m CalendarListModel) SetItems(items []CalendarListItem) CalendarListModel {
	m.items = items
	valid := make(map[int64]bool, len(items))
	for _, it := range items {
		valid[it.ID] = true
	}
	for id := range m.hidden {
		if !valid[id] {
			delete(m.hidden, id)
		}
	}
	if m.cursor >= m.RowCount() {
		m.cursor = m.RowCount() - 1
	}
	return m
}

// HiddenSet returns a copy of the current hidden set.
func (m CalendarListModel) HiddenSet() map[int64]bool {
	out := make(map[int64]bool, len(m.hidden))
	for k, v := range m.hidden {
		out[k] = v
	}
	return out
}

// moveCursor shifts the cursor by delta rows, clamped to [0, RowCount()-1].
func (m CalendarListModel) moveCursor(delta int) CalendarListModel {
	m.cursor += delta
	if m.cursor < 0 {
		m.cursor = 0
	}
	if m.cursor >= m.RowCount() {
		m.cursor = m.RowCount() - 1
	}
	return m
}

// toggleCurrent flips the hidden state of the item under the cursor (no-op on
// the Add row) and returns the new model plus a command that emits
// CalendarVisibilityToggledMsg.
func (m CalendarListModel) toggleCurrent() (CalendarListModel, tea.Cmd) {
	if m.cursor >= len(m.items) {
		return m, nil
	}
	id := m.items[m.cursor].ID
	m.hidden[id] = !m.hidden[id]
	hidden := m.hidden[id]
	return m, func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: hidden} }
}

// activateCurrent returns a command to open the dialog for the cursor row.
// The Add row returns ID == 0.
func (m CalendarListModel) activateCurrent() tea.Cmd {
	var id int64
	if m.cursor < len(m.items) {
		id = m.items[m.cursor].ID
	}
	return func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }
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
	case key.Matches(kp, m.keys.Up), key.Matches(kp, m.keys.ShiftTab):
		return m.moveCursor(-1), nil
	case key.Matches(kp, m.keys.Down), key.Matches(kp, m.keys.Tab):
		return m.moveCursor(1), nil
	case key.Matches(kp, m.keys.Toggle):
		return m.toggleCurrent()
	case key.Matches(kp, m.keys.Open):
		return m, m.activateCurrent()
	}
	return m, nil
}

func (m CalendarListModel) View() string {
	var b strings.Builder
	for i, it := range m.items {
		swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(it.Color)).Render("●")
		marker := "✓"
		if m.hidden[it.ID] {
			marker = "✗"
		}
		line := fmt.Sprintf("%s %s %s", swatch, marker, it.Name)
		if m.focused && i == m.cursor {
			line = lipgloss.NewStyle().Background(m.accentColor).Foreground(m.textColor).Bold(true).Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	add := "+ Add calendar"
	if m.focused && m.cursor == len(m.items) {
		add = lipgloss.NewStyle().Background(m.accentColor).Foreground(m.textColor).Bold(true).Render(add)
	} else {
		add = lipgloss.NewStyle().Foreground(m.mutedColor).Render(add)
	}
	b.WriteString(add)
	return b.String()
}
