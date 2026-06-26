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

func (k calendarKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Up, k.Down, k.Left, k.Right, k.PrevMonth, k.NextMonth, k.Today, k.Select}
}

func (k calendarKeyMap) FullHelp() [][]key.Binding {
	left, right, up, down := k.Left, k.Right, k.Up, k.Down
	left.SetHelp("←/h", "previous day")
	right.SetHelp("→/l", "next day")
	up.SetHelp("↑/k", "previous week")
	down.SetHelp("↓/j", "next week")
	prev, next := k.PrevMonth, k.NextMonth
	prev.SetHelp("[", "previous month")
	next.SetHelp("]", "next month")
	return [][]key.Binding{
		{up, down, left, right},
		{prev, next, k.Today, k.Select},
	}
}

func defaultCalendarKeys() calendarKeyMap {
	return calendarKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "previous")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next")),
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		PrevMonth: key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "previous")),
		NextMonth: key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next")),
		Today:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
	}
}

type CalendarModel struct {
	month           time.Time
	cursor          time.Time
	today           time.Time
	events          []CalendarEvent
	keys            calendarKeyMap
	width           int
	height          int
	weekStart       time.Weekday
	selectedColor   color.Color
	showWeekNumbers bool
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

func (m CalendarModel) SetShowWeekNumbers(show bool) CalendarModel {
	m.showWeekNumbers = show
	return m
}

func (m CalendarModel) Cursor() time.Time       { return m.cursor }
func (m CalendarModel) Month() time.Time        { return m.month }
func (m CalendarModel) WeekStart() time.Weekday { return m.weekStart }

type cellHit struct {
	Day   time.Time
	Line  int
	CellH int
}

func (m CalendarModel) hitCell(x, y int) (cellHit, bool) {
	if m.width <= 0 || m.height <= 0 {
		return cellHit{}, false
	}

	anchor := calendarGridAnchor(m.month, m.weekStart)
	cellWs, cellHs, preambleLines := calendarGridSizes(m.width, m.height, true)

	tableY := y - preambleLines
	if tableY < 0 {
		return cellHit{}, false
	}

	week, cellTop := -1, -1
	posY := 1
	for i := range 6 {
		if tableY >= posY && tableY < posY+cellHs[i] {
			week = i
			cellTop = posY
			break
		}
		posY += cellHs[i] + 1
	}
	if week < 0 {
		return cellHit{}, false
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
		return cellHit{}, false
	}

	return cellHit{
		Day:   anchor.AddDate(0, 0, week*7+col),
		Line:  tableY - cellTop,
		CellH: cellHs[week],
	}, true
}

// DayAtPosition maps a position relative to the calendar widget's top-left
// corner to the corresponding day in the grid.
func (m CalendarModel) DayAtPosition(x, y int) (time.Time, bool) {
	hit, ok := m.hitCell(x, y)
	return hit.Day, ok
}

// EventAtPosition returns the ID of the visible month-view event pill at the
// given position, or 0 when the click doesn't land on a rendered event.
func (m CalendarModel) EventAtPosition(x, y int) int64 {
	hit, ok := m.hitCell(x, y)
	if !ok || hit.Line < 1 {
		return 0
	}
	maxEventLines := hit.CellH - 1
	if maxEventLines < 1 {
		return 0
	}

	dy, dm, dd := hit.Day.Date()
	var events []CalendarEvent
	for _, ev := range m.events {
		ey, em, ed := ev.Day.Date()
		if ey == dy && em == dm && ed == dd {
			events = append(events, ev)
		}
	}

	visibleEvents := min(len(events), maxEventLines)
	if len(events) > maxEventLines {
		visibleEvents--
	}
	if visibleEvents <= 0 || hit.Line > visibleEvents {
		return 0
	}

	return events[hit.Line-1].ID
}

// moveCursor moves the cursor to day, syncs m.month to it, and returns a
// CalendarMonthChangedMsg command when the month boundary was crossed.
func (m CalendarModel) moveCursor(day time.Time) (CalendarModel, tea.Cmd) {
	prevMonth := m.month
	m.cursor = day
	if m.cursor.Year() == prevMonth.Year() && m.cursor.Month() == prevMonth.Month() {
		return m, nil
	}
	m.month = time.Date(m.cursor.Year(), m.cursor.Month(), 1, 0, 0, 0, 0, m.cursor.Location())
	month := m.month
	return m, func() tea.Msg { return CalendarMonthChangedMsg{Month: month} }
}

func (m CalendarModel) selectDay(day time.Time) (CalendarModel, tea.Cmd) {
	m, monthCmd := m.moveCursor(day)
	cursor := m.cursor
	selectCmd := func() tea.Msg { return CalendarDaySelectedMsg{Day: cursor} }
	if monthCmd == nil {
		return m, selectCmd
	}
	return m, tea.Batch(monthCmd, selectCmd)
}

func (m CalendarModel) Update(msg tea.Msg) (CalendarModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	var target time.Time
	switch {
	case key.Matches(keyMsg, m.keys.Up):
		target = m.cursor.AddDate(0, 0, -7)
	case key.Matches(keyMsg, m.keys.Down):
		target = m.cursor.AddDate(0, 0, 7)
	case key.Matches(keyMsg, m.keys.Left):
		target = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.Right):
		target = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.PrevMonth):
		target = addMonthClamped(m.cursor, -1)
	case key.Matches(keyMsg, m.keys.NextMonth):
		target = addMonthClamped(m.cursor, 1)
	case key.Matches(keyMsg, m.keys.Today):
		target = m.today
	case key.Matches(keyMsg, m.keys.Select):
		return m, func() tea.Msg { return CalendarDaySelectedMsg{Day: m.cursor} }
	default:
		return m, nil
	}

	return m.moveCursor(target)
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
		ShowWeekNumbers:  m.showWeekNumbers,
		SelectedColor:    m.selectedColor,
	})
}
