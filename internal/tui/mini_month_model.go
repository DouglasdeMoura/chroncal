package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// MiniMonthDateSelectedMsg is emitted when the user presses Enter on a day.
// The parent (app.go) decides what to do — typically move the active main view.
type MiniMonthDateSelectedMsg struct{ Date time.Time }

type miniMonthKeyMap struct {
	Up, Down, Left, Right key.Binding
	PrevMonth, NextMonth  key.Binding
	Today, Select         key.Binding
}

func defaultMiniMonthKeys() miniMonthKeyMap {
	return miniMonthKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
		PrevMonth: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "prev month")),
		NextMonth: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next month")),
		Today:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "jump main view")),
	}
}

// MiniMonthModel is a compact month picker that lives in the sidebar.
// Its cursor is independent of the main calendar view; navigation only
// affects the main view when the user presses Enter.
type MiniMonthModel struct {
	cursor       time.Time // selected day
	displayMonth time.Time // first-of-month for the rendered grid
	keys         miniMonthKeyMap
	focused      bool
	accentColor  color.Color
	todayColor   color.Color
	textColor    color.Color
}

func NewMiniMonthModel(initial time.Time) MiniMonthModel {
	d := initial
	return MiniMonthModel{
		cursor:       d,
		displayMonth: time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, d.Location()),
		keys:         defaultMiniMonthKeys(),
	}
}

func (m MiniMonthModel) SetTheme(accent, today, text color.Color) MiniMonthModel {
	m.accentColor = accent
	m.todayColor = today
	m.textColor = text
	return m
}

func (m MiniMonthModel) Focus() MiniMonthModel { m.focused = true; return m }
func (m MiniMonthModel) Blur() MiniMonthModel  { m.focused = false; return m }
func (m MiniMonthModel) Focused() bool         { return m.focused }

func (m MiniMonthModel) Cursor() time.Time { return m.cursor }

func (m MiniMonthModel) moveCursor(dx, dy int) MiniMonthModel {
	next := m.cursor.AddDate(0, 0, dy*7+dx)
	m.cursor = next
	if next.Year() != m.displayMonth.Year() || next.Month() != m.displayMonth.Month() {
		m.displayMonth = time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, next.Location())
	}
	return m
}

func (m MiniMonthModel) shiftMonth(delta int) MiniMonthModel {
	m.displayMonth = m.displayMonth.AddDate(0, delta, 0)
	return m
}

func (m MiniMonthModel) snapToday() MiniMonthModel {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	m.cursor = today
	m.displayMonth = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	return m
}

func (m MiniMonthModel) Update(msg tea.Msg) (MiniMonthModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Matches(kp, m.keys.Up):
		return m.moveCursor(0, -1), nil
	case key.Matches(kp, m.keys.Down):
		return m.moveCursor(0, 1), nil
	case key.Matches(kp, m.keys.Left):
		return m.moveCursor(-1, 0), nil
	case key.Matches(kp, m.keys.Right):
		return m.moveCursor(1, 0), nil
	case key.Matches(kp, m.keys.PrevMonth):
		return m.shiftMonth(-1), nil
	case key.Matches(kp, m.keys.NextMonth):
		return m.shiftMonth(1), nil
	case key.Matches(kp, m.keys.Today):
		return m.snapToday(), nil
	case key.Matches(kp, m.keys.Select):
		sel := m.cursor
		return m, func() tea.Msg { return MiniMonthDateSelectedMsg{Date: sel} }
	}
	return m, nil
}

// View renders a 7-column day grid with a header row showing the month.
// Cursor is highlighted; today is bolded.
func (m MiniMonthModel) View() string {
	var b strings.Builder
	header := lipgloss.NewStyle().Bold(true).Render(m.displayMonth.Format("January 2006"))
	b.WriteString(header)
	b.WriteString("\n")
	b.WriteString("Su Mo Tu We Th Fr Sa\n")

	first := m.displayMonth
	// Pad to align first-of-month under its weekday column.
	leading := int(first.Weekday())
	for i := 0; i < leading; i++ {
		b.WriteString("   ")
	}

	today := time.Now()
	cursorDay := m.cursor.Format("2006-01-02")
	todayKey := today.Format("2006-01-02")

	cur := first
	col := leading
	for cur.Month() == first.Month() {
		cell := fmt.Sprintf("%2d", cur.Day())
		isCursor := cur.Format("2006-01-02") == cursorDay
		isToday := cur.Format("2006-01-02") == todayKey
		switch {
		case isCursor && m.focused:
			// Filled highlight when the widget has focus.
			cell = lipgloss.NewStyle().Background(m.accentColor).Foreground(m.textColor).Bold(true).Render(cell)
		case isCursor:
			// Unfocused cursor: underline + bold so selection is still visible.
			cell = lipgloss.NewStyle().Foreground(m.textColor).Bold(true).Underline(true).Render(cell)
		case isToday:
			cell = lipgloss.NewStyle().Foreground(m.todayColor).Bold(true).Render(cell)
		}
		b.WriteString(cell)
		col++
		if col == 7 {
			b.WriteString("\n")
			col = 0
		} else {
			b.WriteString(" ")
		}
		cur = cur.AddDate(0, 0, 1)
	}
	if col != 0 {
		b.WriteString("\n")
	}
	return b.String()
}
