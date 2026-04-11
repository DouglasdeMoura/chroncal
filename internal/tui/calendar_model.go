package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// CalendarDaySelectedMsg is emitted when the user presses Enter on a day.
type CalendarDaySelectedMsg struct{ Day time.Time }

// CalendarMonthChangedMsg is emitted when the cursor crosses a month boundary,
// so the host model can reload events for the new month.
type CalendarMonthChangedMsg struct{ Month time.Time }

type calendarKeyMap struct {
	Up        key.Binding
	Down      key.Binding
	Left      key.Binding
	Right     key.Binding
	PrevMonth key.Binding
	NextMonth key.Binding
	Today     key.Binding
	Select    key.Binding
}

func defaultCalendarKeys() calendarKeyMap {
	return calendarKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "prev week")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next week")),
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev day")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next day")),
		PrevMonth: key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "prev month")),
		NextMonth: key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next month")),
		Today:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
	}
}

type CalendarModel struct {
	month         time.Time
	cursor        time.Time
	today         time.Time
	events        []CalendarEvent
	keys          calendarKeyMap
	width         int
	height        int
	weekStart     time.Weekday
	selectedColor color.Color
}

func NewCalendarModel(today time.Time) CalendarModel {
	t := today.Local()
	month := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return CalendarModel{
		month:     month,
		cursor:    t,
		today:     t,
		keys:      defaultCalendarKeys(),
		weekStart: time.Sunday,
	}
}

func (m CalendarModel) Init() tea.Cmd { return nil }

func (m CalendarModel) SetSize(w, h int) CalendarModel {
	m.width = w
	m.height = h
	return m
}

func (m CalendarModel) SetEvents(events []CalendarEvent) CalendarModel {
	m.events = events
	return m
}

func (m CalendarModel) SetWeekStart(w time.Weekday) CalendarModel {
	m.weekStart = w
	return m
}

func (m CalendarModel) SetSelectedColor(c color.Color) CalendarModel {
	m.selectedColor = c
	return m
}

func (m CalendarModel) Cursor() time.Time { return m.cursor }
func (m CalendarModel) Month() time.Time  { return m.month }

func (m CalendarModel) Update(msg tea.Msg) (CalendarModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	prevMonth := m.month
	switch {
	case key.Matches(keyMsg, m.keys.Up):
		m.cursor = m.cursor.AddDate(0, 0, -7)
	case key.Matches(keyMsg, m.keys.Down):
		m.cursor = m.cursor.AddDate(0, 0, 7)
	case key.Matches(keyMsg, m.keys.Left):
		m.cursor = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.Right):
		m.cursor = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.PrevMonth):
		m.cursor = m.cursor.AddDate(0, -1, 0)
	case key.Matches(keyMsg, m.keys.NextMonth):
		m.cursor = m.cursor.AddDate(0, 1, 0)
	case key.Matches(keyMsg, m.keys.Today):
		m.cursor = m.today
	case key.Matches(keyMsg, m.keys.Select):
		return m, func() tea.Msg { return CalendarDaySelectedMsg{Day: m.cursor} }
	default:
		return m, nil
	}

	if m.cursor.Year() != prevMonth.Year() || m.cursor.Month() != prevMonth.Month() {
		m.month = time.Date(m.cursor.Year(), m.cursor.Month(), 1, 0, 0, 0, 0, m.cursor.Location())
		month := m.month
		return m, func() tea.Msg { return CalendarMonthChangedMsg{Month: month} }
	}
	return m, nil
}

func (m CalendarModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	return Calendar(CalendarOptions{
		Month:            m.month,
		Events:           m.events,
		Today:            m.today,
		Selected:         m.cursor,
		WeekStartsOn:     m.weekStart,
		Width:            m.width,
		Height:           m.height,
		ShowHeader:       true,
		ShowAdjacentDays: true,
		SelectedColor:    m.selectedColor,
	})
}
