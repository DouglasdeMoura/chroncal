package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type DayChangedMsg struct{ Day time.Time }

type dayKeyMap struct {
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PrevDay    key.Binding
	NextDay    key.Binding
	PrevWeek   key.Binding
	NextWeek   key.Binding
	Today      key.Binding
	Select     key.Binding
}

func defaultDayKeys() dayKeyMap {
	return dayKeyMap{
		ScrollUp:   key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
		PrevDay:    key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev day")),
		NextDay:    key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next day")),
		PrevWeek:   key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "prev week")),
		NextWeek:   key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next week")),
		Today:      key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
	}
}

type DayModel struct {
	cursor        time.Time
	today         time.Time
	events        []CalendarEvent
	keys          dayKeyMap
	width         int
	height        int
	selectedColor color.Color
	scrollOffset  int
	linesPerHour  int
}

func NewDayModel(today time.Time) DayModel {
	t := today.Local()
	now := time.Now().Local()
	initialScroll := now.Hour()*defaultLinesPerHour - 4
	if initialScroll < 0 {
		initialScroll = 0
	}
	return DayModel{
		cursor:       t,
		today:        t,
		keys:         defaultDayKeys(),
		linesPerHour: defaultLinesPerHour,
		scrollOffset: initialScroll,
	}
}

func (m DayModel) Cursor() time.Time { return m.cursor }

func (m DayModel) SetSize(w, h int) DayModel {
	m.width = w
	m.height = h
	return m
}

func (m DayModel) SetEvents(events []CalendarEvent) DayModel {
	m.events = events
	return m
}

func (m DayModel) SetSelectedColor(c color.Color) DayModel {
	m.selectedColor = c
	return m
}

func (m DayModel) allDayCount() int {
	dayKey := m.cursor.Format("2006-01-02")
	count := 0
	for _, ev := range m.events {
		if ev.AllDay && ev.Day.Format("2006-01-02") == dayKey {
			count++
		}
	}
	return count
}

func (m DayModel) viewportHeight() int {
	allDayRows := m.allDayCount()
	if allDayRows < 1 {
		allDayRows = 1
	}
	fixedLines := 2 + 1 + allDayRows + 1
	vh := m.height - fixedLines
	if vh < 1 {
		vh = 1
	}
	return vh
}

func (m DayModel) maxScroll() int {
	totalRows := totalHours * m.linesPerHour
	ms := totalRows - m.viewportHeight()
	if ms < 0 {
		ms = 0
	}
	return ms
}

func (m DayModel) selectDay(day time.Time) (DayModel, tea.Cmd) {
	prevDay := m.cursor.Format("2006-01-02")
	m.cursor = day

	var cmds []tea.Cmd
	if m.cursor.Format("2006-01-02") != prevDay {
		cursor := m.cursor
		cmds = append(cmds, func() tea.Msg { return DayChangedMsg{Day: cursor} })
	}
	cursor := m.cursor
	cmds = append(cmds, func() tea.Msg { return CalendarDaySelectedMsg{Day: cursor} })
	return m, tea.Batch(cmds...)
}

func (m DayModel) DayAtPosition(x, y int) (time.Time, bool) {
	if m.width <= 0 || m.height <= 0 {
		return time.Time{}, false
	}
	if y < 2 {
		return time.Time{}, false
	}
	if x < timeLabelWidth+1 {
		return time.Time{}, false
	}
	return m.cursor, true
}

func (m DayModel) EventAtPosition(x, y int) int64 {
	if x < timeLabelWidth+1 {
		return 0
	}

	allDayRows := m.allDayCount()
	if allDayRows < 1 {
		allDayRows = 1
	}
	fixedLines := 2 + 1 + allDayRows + 1
	if y < fixedLines {
		return 0
	}

	scrollOffset := m.scrollOffset
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	if ms := m.maxScroll(); scrollOffset > ms {
		scrollOffset = ms
	}
	row := scrollOffset + (y - fixedLines)

	lph := m.linesPerHour
	if lph < 1 {
		lph = defaultLinesPerHour
	}

	colWidth := m.width - timeLabelWidth - 2
	if colWidth < 1 {
		colWidth = 1
	}

	placed := placeDayEvents(m.events, m.cursor, lph)
	resolveOverlaps(placed)
	matches := findPlacedEvents(placed, row, 0)
	if len(matches) == 0 {
		return 0
	}
	xInCol := x - timeLabelWidth - 1
	return hitSubCol(matches, xInCol, colWidth)
}

func (m DayModel) Update(msg tea.Msg) (DayModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	prevDay := m.cursor.Format("2006-01-02")
	switch {
	case key.Matches(keyMsg, m.keys.ScrollUp):
		m.scrollOffset -= m.linesPerHour
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
	case key.Matches(keyMsg, m.keys.ScrollDown):
		m.scrollOffset += m.linesPerHour
		if ms := m.maxScroll(); m.scrollOffset > ms {
			m.scrollOffset = ms
		}
	case key.Matches(keyMsg, m.keys.PrevDay):
		m.cursor = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.NextDay):
		m.cursor = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.PrevWeek):
		m.cursor = m.cursor.AddDate(0, 0, -7)
	case key.Matches(keyMsg, m.keys.NextWeek):
		m.cursor = m.cursor.AddDate(0, 0, 7)
	case key.Matches(keyMsg, m.keys.Today):
		m.cursor = m.today
		now := time.Now().Local()
		targetRow := now.Hour()*m.linesPerHour + now.Minute()*m.linesPerHour/60
		m.scrollOffset = targetRow - m.viewportHeight()/2
		if m.scrollOffset < 0 {
			m.scrollOffset = 0
		}
		if ms := m.maxScroll(); m.scrollOffset > ms {
			m.scrollOffset = ms
		}
	case key.Matches(keyMsg, m.keys.Select):
		return m, func() tea.Msg { return CalendarDaySelectedMsg{Day: m.cursor} }
	default:
		return m, nil
	}

	if m.cursor.Format("2006-01-02") != prevDay {
		cursor := m.cursor
		return m, func() tea.Msg { return DayChangedMsg{Day: cursor} }
	}
	return m, nil
}

func (m DayModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	scrollOffset := m.scrollOffset
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	if ms := m.maxScroll(); scrollOffset > ms {
		scrollOffset = ms
	}
	return DayGrid(DayOptions{
		Day:          m.cursor,
		Events:       m.events,
		Today:        m.today,
		Width:        m.width,
		Height:       m.height,
		ShowHeader:   true,
		ScrollOffset: scrollOffset,
		LinesPerHour: m.linesPerHour,
	})
}
