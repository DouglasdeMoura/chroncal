package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type WeekChangedMsg struct{ Week time.Time }

type weekKeyMap struct {
	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	PrevWeek key.Binding
	NextWeek key.Binding
	Today    key.Binding
	Select   key.Binding
}

func defaultWeekKeys() weekKeyMap {
	return weekKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "prev week")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next week")),
		Left:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev day")),
		Right:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next day")),
		PrevWeek: key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "prev week")),
		NextWeek: key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next week")),
		Today:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:   key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
	}
}

type WeekModel struct {
	cursor        time.Time
	today         time.Time
	events        []CalendarEvent
	keys          weekKeyMap
	width         int
	height        int
	weekStart     time.Weekday
	selectedColor color.Color
}

func NewWeekModel(today time.Time) WeekModel {
	t := today.Local()
	return WeekModel{
		cursor:    t,
		today:     t,
		keys:      defaultWeekKeys(),
		weekStart: time.Sunday,
	}
}

func (m WeekModel) WeekStartDate() time.Time {
	offset := (int(m.cursor.Weekday()) - int(m.weekStart) + 7) % 7
	return time.Date(m.cursor.Year(), m.cursor.Month(), m.cursor.Day()-offset, 0, 0, 0, 0, m.cursor.Location())
}

func (m WeekModel) Cursor() time.Time { return m.cursor }

func (m WeekModel) SetSize(w, h int) WeekModel {
	m.width = w
	m.height = h
	return m
}

func (m WeekModel) SetEvents(events []CalendarEvent) WeekModel {
	m.events = events
	return m
}

func (m WeekModel) SetSelectedColor(c color.Color) WeekModel {
	m.selectedColor = c
	return m
}

func (m WeekModel) selectDay(day time.Time) (WeekModel, tea.Cmd) {
	prevWeek := m.WeekStartDate()
	m.cursor = day

	var cmds []tea.Cmd
	if m.WeekStartDate() != prevWeek {
		week := m.WeekStartDate()
		cmds = append(cmds, func() tea.Msg { return WeekChangedMsg{Week: week} })
	}
	cursor := m.cursor
	cmds = append(cmds, func() tea.Msg { return CalendarDaySelectedMsg{Day: cursor} })
	return m, tea.Batch(cmds...)
}

func (m WeekModel) DayAtPosition(x, y int) (time.Time, bool) {
	if m.width <= 0 || m.height <= 0 {
		return time.Time{}, false
	}

	preambleLines := 3
	if y < preambleLines {
		return time.Time{}, false
	}

	anchor := m.WeekStartDate()

	availW := m.width - 8
	baseW := availW / 7
	if baseW < 6 {
		baseW = 6
	}
	remW := availW - baseW*7
	if remW < 0 {
		remW = 0
	}
	cellWs := make([]int, 7)
	for i := range 7 {
		cellWs[i] = baseW
		if i < remW {
			cellWs[i]++
		}
	}

	col := -1
	posX := 1
	for j := range 7 {
		if x >= posX && x < posX+cellWs[j] {
			col = j
			break
		}
		posX += cellWs[j] + 1
	}
	if col < 0 {
		return time.Time{}, false
	}

	return anchor.AddDate(0, 0, col), true
}

func (m WeekModel) Update(msg tea.Msg) (WeekModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	prevWeek := m.WeekStartDate()
	switch {
	case key.Matches(keyMsg, m.keys.Up):
		m.cursor = m.cursor.AddDate(0, 0, -7)
	case key.Matches(keyMsg, m.keys.Down):
		m.cursor = m.cursor.AddDate(0, 0, 7)
	case key.Matches(keyMsg, m.keys.Left):
		m.cursor = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.Right):
		m.cursor = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.PrevWeek):
		m.cursor = m.cursor.AddDate(0, 0, -7)
	case key.Matches(keyMsg, m.keys.NextWeek):
		m.cursor = m.cursor.AddDate(0, 0, 7)
	case key.Matches(keyMsg, m.keys.Today):
		m.cursor = m.today
	case key.Matches(keyMsg, m.keys.Select):
		return m, func() tea.Msg { return CalendarDaySelectedMsg{Day: m.cursor} }
	default:
		return m, nil
	}

	if m.WeekStartDate() != prevWeek {
		week := m.WeekStartDate()
		return m, func() tea.Msg { return WeekChangedMsg{Week: week} }
	}
	return m, nil
}

func (m WeekModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	return WeekGrid(WeekOptions{
		WeekStart:     m.WeekStartDate(),
		Events:        m.events,
		Today:         m.today,
		Selected:      m.cursor,
		Width:         m.width,
		Height:        m.height,
		ShowHeader:    true,
		SelectedColor: m.selectedColor,
	})
}
