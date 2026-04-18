package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type DayChangedMsg struct{ Day time.Time }

type dayKeyMap struct {
	ScrollUp    key.Binding
	ScrollDown  key.Binding
	PrevDay     key.Binding
	NextDay     key.Binding
	PrevBracket key.Binding
	NextBracket key.Binding
	Today       key.Binding
	Select      key.Binding
}

func (k dayKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollUp, k.ScrollDown, k.PrevDay, k.NextDay, k.PrevBracket, k.NextBracket, k.Today, k.Select}
}

func (k dayKeyMap) FullHelp() [][]key.Binding {
	prev, next := k.PrevDay, k.NextDay
	prev.SetHelp("←/h", "previous day")
	next.SetHelp("→/l", "next day")
	prevB, nextB := k.PrevBracket, k.NextBracket
	prevB.SetHelp("[", "previous day")
	nextB.SetHelp("]", "next day")
	return [][]key.Binding{
		{k.ScrollUp, k.ScrollDown, prev, next},
		{prevB, nextB, k.Today, k.Select},
	}
}

func defaultDayKeys() dayKeyMap {
	return dayKeyMap{
		ScrollUp:    key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
		ScrollDown:  key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
		PrevDay:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		NextDay:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		PrevBracket: key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "previous day")),
		NextBracket: key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next day")),
		Today:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:      key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
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
	initialScroll := max(now.Hour()*defaultLinesPerHour-4, 0)
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
	allDayRows := max(m.allDayCount(), 1)
	fixedLines := 2 + 1 + allDayRows + 1
	vh := max(m.height-fixedLines, 1)
	return vh
}

func (m DayModel) maxScroll() int {
	totalRows := totalHours * m.linesPerHour
	ms := max(totalRows-m.viewportHeight(), 0)
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

	allDayRows := max(m.allDayCount(), 1)
	fixedLines := 2 + 1 + allDayRows + 1
	if y < fixedLines {
		return 0
	}

	scrollOffset := min(max(m.scrollOffset, 0), m.maxScroll())
	row := scrollOffset + (y - fixedLines)

	lph := m.linesPerHour
	if lph < 1 {
		lph = defaultLinesPerHour
	}

	colWidth := max(m.width-timeLabelWidth-2, 1)

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
		m.scrollOffset = max(m.scrollOffset-m.linesPerHour, 0)
	case key.Matches(keyMsg, m.keys.ScrollDown):
		m.scrollOffset = min(m.scrollOffset+m.linesPerHour, m.maxScroll())
	case key.Matches(keyMsg, m.keys.PrevDay):
		m.cursor = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.NextDay):
		m.cursor = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.PrevBracket):
		m.cursor = m.cursor.AddDate(0, 0, -1)
	case key.Matches(keyMsg, m.keys.NextBracket):
		m.cursor = m.cursor.AddDate(0, 0, 1)
	case key.Matches(keyMsg, m.keys.Today):
		m.cursor = m.today
		now := time.Now().Local()
		targetRow := now.Hour()*m.linesPerHour + now.Minute()*m.linesPerHour/60
		m.scrollOffset = min(max(targetRow-m.viewportHeight()/2, 0), m.maxScroll())
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
	scrollOffset := min(max(m.scrollOffset, 0), m.maxScroll())
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
