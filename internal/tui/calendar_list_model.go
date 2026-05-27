package tui

import (
	"image/color"
	"maps"
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

// SyncHealth describes a calendar's last-known sync state, used to render an
// ambient health marker in the list. It is derived from the persisted
// last_sync_error / last_sync_at fields, not computed live, so it reflects the
// most recent sync attempt — including background `chroncal tick` runs the user
// never triggered.
type SyncHealth int

const (
	// SyncHealthNone is a calendar not linked to a CalDAV account; it has no
	// sync state and renders no marker.
	SyncHealthNone SyncHealth = iota
	// SyncHealthOK is a linked calendar whose last sync completed cleanly.
	SyncHealthOK
	// SyncHealthError is a linked calendar whose last sync attempt recorded an
	// error. This is the only state that renders a (loud) marker.
	SyncHealthError
	// SyncHealthPending is a linked calendar that has never completed a clean
	// sync but has no recorded error yet.
	SyncHealthPending
)

// CalendarListItem is the display data for a single row.
type CalendarListItem struct {
	ID     int64
	Name   string
	Color  string // hex like "#a6e3a1"
	Health SyncHealth
}

type calendarListKeyMap struct {
	Up, Down, Tab, ShiftTab key.Binding
	Toggle                  key.Binding
	Open                    key.Binding
}

func defaultCalendarListKeys() calendarListKeyMap {
	return calendarListKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Toggle:   key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "toggle visibility")),
		Open:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "open")),
	}
}

// CalendarListModel renders a list of calendars (color swatch, name, visibility
// indicator).
type CalendarListModel struct {
	items             []CalendarListItem
	hidden            map[int64]bool
	cursor            int
	focused           bool
	width             int
	keys              calendarListKeyMap
	accentColor       color.Color
	mutedColor        color.Color
	textColor         color.Color
	selectedTextColor color.Color
	errColor          color.Color
}

func NewCalendarListModel(items []CalendarListItem, hidden map[int64]bool) CalendarListModel {
	h := make(map[int64]bool, len(hidden))
	maps.Copy(h, hidden)
	return CalendarListModel{
		items:  items,
		hidden: h,
		keys:   defaultCalendarListKeys(),
	}
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

// SetWidth sets the available render width so long calendar names truncate
// with an ellipsis instead of wrapping onto the next line.
func (m CalendarListModel) SetWidth(w int) CalendarListModel { m.width = w; return m }
func (m CalendarListModel) Focused() bool                    { return m.focused }
func (m CalendarListModel) Cursor() int                      { return m.cursor }
func (m CalendarListModel) ItemCount() int                   { return len(m.items) }

// RowCount returns the number of selectable rows in the list.
func (m CalendarListModel) RowCount() int { return len(m.items) }

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
	maps.Copy(out, m.hidden)
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

// toggleCurrent flips the hidden state of the item under the cursor and
// returns the new model plus a command that emits
// CalendarVisibilityToggledMsg.
func (m CalendarListModel) toggleCurrent() (CalendarListModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return m, nil
	}
	id := m.items[m.cursor].ID
	m.hidden[id] = !m.hidden[id]
	hidden := m.hidden[id]
	return m, func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: hidden} }
}

// HandleClick hit-tests a click at (x, y) in the widget's local coordinates
// (top-left of the first item row is (0, 0)). A click on an item row moves
// the cursor there and toggles its visibility. y values outside the rendered
// rows are no-ops. x is currently ignored — any click within the sidebar's
// x range that lands on a row activates it.
func (m CalendarListModel) HandleClick(_ int, y int) (CalendarListModel, tea.Cmd) {
	if y < 0 || y >= len(m.items) {
		return m, nil
	}
	m.cursor = y
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
	case key.Matches(kp, m.keys.Up), key.Matches(kp, m.keys.ShiftTab):
		return m.moveCursor(-1), nil
	case key.Matches(kp, m.keys.Down), key.Matches(kp, m.keys.Tab):
		return m.moveCursor(1), nil
	case key.Matches(kp, m.keys.Toggle):
		return m.toggleCurrent()
	case key.Matches(kp, m.keys.Open):
		if m.cursor < 0 || m.cursor >= len(m.items) {
			return m, nil
		}
		id := m.items[m.cursor].ID
		return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }
	}
	return m, nil
}

func (m CalendarListModel) View() string {
	var b strings.Builder
	for i, it := range m.items {
		selected := m.focused && i == m.cursor
		// Filled dot = visible, hollow dot = hidden. A single glyph carries
		// both the calendar's color identity and its on/off state, avoiding
		// the previous ● + ✓ doubling.
		glyph := "●"
		if m.hidden[it.ID] {
			glyph = "○"
		}
		// Swatch keeps its calendar-color foreground in every state — its
		// job is to identify the calendar, not signal selection.
		swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(it.Color)).Render(glyph)

		// Health marker: a trailing ⚠ on calendars whose last sync failed.
		// Only SyncHealthError is loud; every other state renders nothing so
		// the row stays calm. The marker lives in its own reserved trailing
		// cell *outside* the (possibly Reverse'd) name chip, so the inverted
		// selection block never paints over it. markerCells accounts for the
		// glyph plus its one leading separator space.
		marker := ""
		markerCells := 0
		if it.Health == SyncHealthError {
			marker = lipgloss.NewStyle().Foreground(m.errColor).Render("⚠")
			markerCells = lipgloss.Width(marker) + 1
		}

		// Mirror the manage-calendars dialog's selection treatment:
		// inverted (Reverse) + bold for the focused row, faint for hidden.
		// Reverse swaps terminal fg/bg so the chip pops regardless of theme,
		// avoiding the muddy-tint problem of explicit Background()+Width().
		nameStyle := lipgloss.NewStyle()
		if m.hidden[it.ID] && !selected {
			nameStyle = nameStyle.Foreground(m.mutedColor)
		}
		if selected {
			nameStyle = nameStyle.Reverse(true).Bold(true)
			// Reserve the swatch (1 cell) + separator space (1 cell) + the
			// health marker cells, and let the chip fill the rest so trailing
			// pad cells pick up the tint without overrunning the marker.
			if remaining := m.width - 2 - markerCells; remaining > 0 {
				nameStyle = nameStyle.Width(remaining)
			}
		}
		// Pad the chip with surrounding spaces so the inverted block has
		// breathing room on both sides of the label, matching the dialog.
		nameText := it.Name
		if avail := m.width - 4 - markerCells; m.width > 4 && avail > 0 {
			nameText = truncateTo(nameText, avail)
		}
		name := nameStyle.Render(" " + nameText + " ")

		b.WriteString(swatch + " " + name)
		if marker != "" {
			b.WriteString(" " + marker)
		}
		if i < len(m.items)-1 {
			b.WriteString("\n")
		}
	}
	return b.String()
}
