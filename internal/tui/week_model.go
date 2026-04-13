package tui

import (
	"image/color"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

type WeekChangedMsg struct{ Week time.Time }

type weekKeyMap struct {
	ScrollUp   key.Binding
	ScrollDown key.Binding
	Left       key.Binding
	Right      key.Binding
	PrevWeek   key.Binding
	NextWeek   key.Binding
	Today      key.Binding
	Select     key.Binding
}

func (k weekKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.ScrollUp, k.ScrollDown, k.Left, k.Right, k.PrevWeek, k.NextWeek, k.Today, k.Select}
}

func (k weekKeyMap) FullHelp() [][]key.Binding {
	left, right := k.Left, k.Right
	left.SetHelp("←/h", "previous day")
	right.SetHelp("→/l", "next day")
	prev, next := k.PrevWeek, k.NextWeek
	prev.SetHelp("[", "previous week")
	next.SetHelp("]", "next week")
	return [][]key.Binding{
		{k.ScrollUp, k.ScrollDown, left, right},
		{prev, next, k.Today, k.Select},
	}
}

func defaultWeekKeys() weekKeyMap {
	return weekKeyMap{
		ScrollUp:   key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "scroll up")),
		ScrollDown: key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "scroll down")),
		Left:       key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		Right:      key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		PrevWeek:   key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "previous")),
		NextWeek:   key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next")),
		Today:      key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select day")),
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
	scrollOffset  int
	linesPerHour  int
}

func NewWeekModel(today time.Time) WeekModel {
	t := today.Local()
	now := time.Now().Local()
	initialScroll := now.Hour()*defaultLinesPerHour - 4
	if initialScroll < 0 {
		initialScroll = 0
	}
	return WeekModel{
		cursor:       t,
		today:        t,
		keys:         defaultWeekKeys(),
		weekStart:    time.Sunday,
		linesPerHour: defaultLinesPerHour,
		scrollOffset: initialScroll,
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

func (m WeekModel) allDayRowCount() int {
	anchor := m.WeekStartDate()
	maxPerCol := 0
	for col := range 7 {
		d := anchor.AddDate(0, 0, col)
		dayKey := d.Format("2006-01-02")
		count := 0
		for _, ev := range m.events {
			if ev.AllDay && ev.Day.Format("2006-01-02") == dayKey {
				count++
			}
		}
		if count > maxPerCol {
			maxPerCol = count
		}
	}
	return maxPerCol
}

func (m WeekModel) viewportHeight() int {
	allDayRows := m.allDayRowCount()
	if allDayRows < 1 {
		allDayRows = 1
	}
	fixedLines := 2 + 1 + 1 + allDayRows + 1
	vh := m.height - fixedLines
	if vh < 1 {
		vh = 1
	}
	return vh
}

func (m WeekModel) maxScroll() int {
	totalRows := totalHours * m.linesPerHour
	ms := totalRows - m.viewportHeight()
	if ms < 0 {
		ms = 0
	}
	return ms
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

	if y < 2 {
		return time.Time{}, false
	}

	anchor := m.WeekStartDate()
	gridX := x - timeLabelWidth - 1
	if gridX < 0 {
		return time.Time{}, false
	}

	colWs := calcWeekColWidths(m.width)
	col := -1
	posX := 0
	for j := range 7 {
		if gridX >= posX && gridX < posX+colWs[j] {
			col = j
			break
		}
		posX += colWs[j] + 1
	}
	if col < 0 {
		return time.Time{}, false
	}

	return anchor.AddDate(0, 0, col), true
}

func (m WeekModel) EventAtPosition(x, y int) int64 {
	anchor := m.WeekStartDate()

	gridX := x - timeLabelWidth - 1
	if gridX < 0 {
		return 0
	}
	colWs := calcWeekColWidths(m.width)
	col := -1
	colStart := 0
	for j := range 7 {
		if gridX >= colStart && gridX < colStart+colWs[j] {
			col = j
			break
		}
		colStart += colWs[j] + 1
	}
	if col < 0 {
		return 0
	}

	allDayRows := m.allDayRowCount()
	if allDayRows < 1 {
		allDayRows = 1
	}
	fixedLines := 2 + 1 + 1 + allDayRows + 1
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
	placed := placeWeekEvents(m.events, anchor, lph)
	resolveOverlaps(placed)
	matches := findPlacedEvents(placed, row, col)
	if len(matches) == 0 {
		return 0
	}
	xInCol := gridX - colStart
	return hitSubCol(matches, xInCol, colWs[col])
}

func (m WeekModel) Update(msg tea.Msg) (WeekModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	prevWeek := m.WeekStartDate()
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
	scrollOffset := m.scrollOffset
	if scrollOffset < 0 {
		scrollOffset = 0
	}
	if ms := m.maxScroll(); scrollOffset > ms {
		scrollOffset = ms
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
		ScrollOffset:  scrollOffset,
		LinesPerHour:  m.linesPerHour,
	})
}
