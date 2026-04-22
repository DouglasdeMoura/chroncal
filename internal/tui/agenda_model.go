package tui

import (
	"fmt"
	"image/color"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// AgendaWindowDays is the number of days rendered forward from the cursor.
const AgendaWindowDays = 30

// agendaWheelStep is the number of rows advanced per mouse-wheel tick.
const agendaWheelStep = 3

// Fixed column widths for the agenda row layout. Kept as constants so the
// renderer and any layout tweaks stay in sync.
const (
	agendaDayColWidth  = 8  // "Wed  22 " or "Wed  22 " with today badge
	agendaDotColWidth  = 3  // " ● "
	agendaTimeColWidth = 13 // "09:00–10:30  " / "All day      "
	agendaTitleCalGap  = 2  // spaces between title and right-aligned calendar
	agendaMaxCalendar  = 18 // soft cap on the right-aligned calendar column
	agendaLeftPad      = 0  // leading space in front of the day column
)

// AgendaCursorChangedMsg is emitted when the cursor moves to a new day, so
// the host model can reload events for the new agenda window.
type AgendaCursorChangedMsg struct{ Day time.Time }

type agendaKeyMap struct {
	Up       key.Binding
	Down     key.Binding
	PrevDay  key.Binding
	NextDay  key.Binding
	PrevWeek key.Binding
	NextWeek key.Binding
	Today    key.Binding
	Select   key.Binding
	Create   key.Binding
}

func defaultAgendaKeys() agendaKeyMap {
	return agendaKeyMap{
		Up:       key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "previous")),
		Down:     key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next")),
		PrevDay:  key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev day")),
		NextDay:  key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next day")),
		PrevWeek: key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "prev week")),
		NextWeek: key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next week")),
		Today:    key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:   key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "view")),
		Create:   key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
	}
}

// agendaRow is one rendered line in the agenda. Event rows are selectable
// and show the dot/time/title/calendar layout; separator rows are blank
// spacers drawn between day groups; monthHeader rows repeat the top-of-
// view title at each month boundary reached while scrolling forward.
type agendaRow struct {
	day         time.Time
	event       event.Event
	dayIndex    int // 1-based position within a multi-day span
	totalDays   int
	firstOfDay  bool
	separator   bool
	monthHeader bool
}

type AgendaModel struct {
	cursor    time.Time
	today     time.Time
	events    []event.Event
	calendars map[int64]CalendarInfo
	rows      []agendaRow
	selected  int // index into rows; -1 when empty
	scroll    int
	keys      agendaKeyMap
	theme     Theme
	width     int
	height    int
	// selectedColor highlights the focused event row. Set to theme.Selected.
	selectedColor color.Color
}

func NewAgendaModel(today time.Time) AgendaModel {
	t := today.Local()
	return AgendaModel{
		cursor:   t,
		today:    t,
		selected: -1,
		keys:     defaultAgendaKeys(),
	}
}

func (m AgendaModel) Cursor() time.Time { return m.cursor }

// WindowStart returns the first day included in the current agenda window.
func (m AgendaModel) WindowStart() time.Time {
	return time.Date(m.cursor.Year(), m.cursor.Month(), m.cursor.Day(), 0, 0, 0, 0, m.cursor.Location())
}

// WindowEnd returns the exclusive end of the current agenda window.
func (m AgendaModel) WindowEnd() time.Time {
	return m.WindowStart().AddDate(0, 0, AgendaWindowDays)
}

func (m AgendaModel) SetSize(w, h int) AgendaModel {
	m.width = w
	m.height = h
	m.clampScroll()
	return m
}

func (m AgendaModel) SetTheme(t Theme) AgendaModel {
	m.theme = t
	m.selectedColor = t.Selected
	return m
}

func (m AgendaModel) SetSelectedColor(c color.Color) AgendaModel {
	m.selectedColor = c
	return m
}

// SetEvents updates the cached event slice, the calendar info used for color
// and name lookups, and rebuilds the rendered rows. Selection is kept on the
// first event on or after the cursor day so view switches / reloads don't
// drop the user mid-list.
func (m AgendaModel) SetEvents(events []event.Event, calendars map[int64]CalendarInfo) AgendaModel {
	m.events = events
	m.calendars = calendars
	m.rows = buildAgendaRows(events, m.WindowStart(), AgendaWindowDays)
	m.selected = firstSelectableOnOrAfter(m.rows, m.cursor)
	m.clampScroll()
	return m
}

// SelectedDay returns the day associated with the current selection, falling
// back to the cursor when no row is selected.
func (m AgendaModel) SelectedDay() time.Time {
	if m.selected >= 0 && m.selected < len(m.rows) {
		return m.rows[m.selected].day
	}
	return m.cursor
}

// SelectedEvent returns the event under the cursor, when the selected row
// is an event row (not a separator or month header).
func (m AgendaModel) SelectedEvent() (event.Event, bool) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return event.Event{}, false
	}
	r := m.rows[m.selected]
	if !isEventRow(r) {
		return event.Event{}, false
	}
	return r.event, true
}

// isEventRow reports whether r is selectable (i.e., carries an event).
func isEventRow(r agendaRow) bool { return !r.separator && !r.monthHeader }

func (m AgendaModel) Update(msg tea.Msg) (AgendaModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}

	switch {
	case key.Matches(kp, m.keys.Up):
		m.selected = prevSelectable(m.rows, m.selected)
		m.ensureVisible()
		return m, nil
	case key.Matches(kp, m.keys.Down):
		m.selected = nextSelectable(m.rows, m.selected)
		m.ensureVisible()
		return m, nil
	case key.Matches(kp, m.keys.PrevDay):
		return m.moveCursor(-1)
	case key.Matches(kp, m.keys.NextDay):
		return m.moveCursor(1)
	case key.Matches(kp, m.keys.PrevWeek):
		return m.moveCursor(-7)
	case key.Matches(kp, m.keys.NextWeek):
		return m.moveCursor(7)
	case key.Matches(kp, m.keys.Today):
		if sameDay(m.cursor, m.today) {
			return m, nil
		}
		m.cursor = m.today
		cursor := m.cursor
		return m, func() tea.Msg { return AgendaCursorChangedMsg{Day: cursor} }
	case key.Matches(kp, m.keys.Select):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
		}
		return m, nil
	case key.Matches(kp, m.keys.Create):
		day := m.SelectedDay()
		return m, func() tea.Msg { return EventCreateMsg{Day: day} }
	}
	return m, nil
}

func (m AgendaModel) moveCursor(deltaDays int) (AgendaModel, tea.Cmd) {
	m.cursor = m.cursor.AddDate(0, 0, deltaDays)
	cursor := m.cursor
	return m, func() tea.Msg { return AgendaCursorChangedMsg{Day: cursor} }
}

func (m AgendaModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	headerLines := 2
	viewportH := max(m.height-headerLines, 1)

	var out strings.Builder

	if len(m.rows) == 0 {
		headerDay := m.cursor
		out.WriteString(m.renderMonthHeader(headerDay))
		out.WriteString("\n\n")
		out.WriteString(lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("  No events in the next 30 days."))
		return out.String()
	}

	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.rows))

	// Drive the fixed top header from the first visible row that carries a
	// day, so scrolling past a month break updates the sticky title.
	headerDay := m.cursor
	for i := start; i < end; i++ {
		if !m.rows[i].day.IsZero() {
			headerDay = m.rows[i].day
			break
		}
	}
	out.WriteString(m.renderMonthHeader(headerDay))
	out.WriteString("\n\n")

	// Skip the leading run of separators / matching-month headers at the
	// top of the viewport: the sticky header already names that month, so
	// re-rendering them would just duplicate it. Extend end by the
	// skipped count to keep the viewport filled.
	activeMonth := monthKey(headerDay)
	renderStart := start
	for renderStart < end {
		r := m.rows[renderStart]
		if r.separator {
			renderStart++
			continue
		}
		if r.monthHeader && monthKey(r.day) == activeMonth {
			renderStart++
			continue
		}
		break
	}
	end = min(end+(renderStart-start), len(m.rows))

	for i := renderStart; i < end; i++ {
		if i > renderStart {
			out.WriteByte('\n')
		}
		if m.rows[i].separator {
			out.WriteString(lipgloss.NewStyle().Width(m.width).Render(""))
			continue
		}
		if m.rows[i].monthHeader {
			out.WriteString(m.renderMonthHeader(m.rows[i].day))
			continue
		}
		out.WriteString(m.renderEventRow(m.rows[i], i == m.selected))
	}
	return out.String()
}

// renderMonthHeader formats an in-list month title using the same style
// as the top-of-view header so month transitions read clearly.
func (m AgendaModel) renderMonthHeader(d time.Time) string {
	style := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Text)
	return lipgloss.NewStyle().Width(m.width).Render(style.Render(d.Format("January 2006")))
}

// renderEventRow composes a single agenda line.
func (m AgendaModel) renderEventRow(r agendaRow, selected bool) string {
	ev := r.event

	dayCol := m.renderDayColumn(r)

	cal := m.calendars[ev.CalendarID]
	dotColor := m.theme.Muted
	if cal.Color != "" {
		dotColor = lipgloss.Color(cal.Color)
	}
	dot := lipgloss.NewStyle().Foreground(dotColor).Render(Glyphs["dot"])

	timeText := agendaTimeText(ev, r.dayIndex, r.totalDays)
	timeStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim).Width(agendaTimeColWidth)
	if ev.AllDay {
		timeStyle = timeStyle.Italic(true)
	}
	timeCol := timeStyle.Render(timeText)

	title := ev.Title
	if r.totalDays > 1 {
		title += fmt.Sprintf(" (day %d/%d)", r.dayIndex, r.totalDays)
	}

	calLabel := cal.Name

	fixedLeft := agendaLeftPad + agendaDayColWidth + 1 + agendaDotColWidth + agendaTimeColWidth
	available := max(m.width-fixedLeft, 1)

	// Drop the calendar label on very narrow widths so the title keeps its
	// full run before truncation takes over.
	calW := 0
	if calLabel != "" && available >= 20 {
		calW = min(lipgloss.Width(calLabel), agendaMaxCalendar)
	}

	titleW := available
	if calW > 0 {
		titleW = max(available-agendaTitleCalGap-calW, 1)
	}
	titleCol := lipgloss.NewStyle().
		Foreground(m.theme.Text).
		Width(titleW).
		Render(truncateTo(title, titleW))

	rightCol := ""
	if calW > 0 {
		rightCol = strings.Repeat(" ", agendaTitleCalGap) +
			lipgloss.NewStyle().Foreground(m.theme.TextDim).Render(truncateTo(calLabel, calW))
	}

	line := strings.Repeat(" ", agendaLeftPad) +
		dayCol +
		" " +
		lipgloss.NewStyle().Width(agendaDotColWidth).Render(" "+dot+" ") +
		timeCol +
		titleCol +
		rightCol

	rowStyle := lipgloss.NewStyle().Width(m.width)
	if selected {
		rowStyle = rowStyle.Background(m.selectedColor).Foreground(m.theme.Text).Bold(true)
	}
	return rowStyle.Render(line)
}

// renderDayColumn returns the 8-column-wide day label shown at the start of
// the first event row of a calendar day. Continuation rows get a blank
// column. Today's day number is rendered in a filled pill using the theme
// "today" color.
func (m AgendaModel) renderDayColumn(r agendaRow) string {
	if !r.firstOfDay {
		return strings.Repeat(" ", agendaDayColWidth)
	}
	d := r.day
	weekday := d.Format("Mon")
	dayNum := fmt.Sprintf("%d", d.Day())

	isToday := sameDay(d, m.today)
	isCursor := sameDay(d, m.cursor) && !isToday

	var weekdayStyle, numStyle lipgloss.Style
	switch {
	case isToday:
		weekdayStyle = lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true)
		numStyle = lipgloss.NewStyle().
			Background(m.theme.Primary).
			Foreground(m.theme.Surface).
			Bold(true).
			PaddingRight(1)
	case isCursor:
		weekdayStyle = lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true)
		numStyle = lipgloss.NewStyle().Foreground(m.theme.Primary).Bold(true)
	default:
		weekdayStyle = lipgloss.NewStyle().Foreground(m.theme.TextDim)
		numStyle = lipgloss.NewStyle().Foreground(m.theme.Text).Bold(true)
	}

	body := numStyle.Render(dayNum) + " " + weekdayStyle.Render(weekday)
	return lipgloss.NewStyle().Width(agendaDayColWidth).Render(body)
}

// agendaTimeText produces the compact time-column text for an event on a
// given day of its span.
func agendaTimeText(ev event.Event, dayIndex, totalDays int) string {
	if ev.AllDay {
		return "All day"
	}
	if totalDays <= 1 {
		start := ev.StartTime.Local().Format("15:04")
		if ev.EndTime.IsZero() {
			return start
		}
		return start + "–" + ev.EndTime.Local().Format("15:04")
	}
	switch dayIndex {
	case 1:
		return ev.StartTime.Local().Format("15:04") + " " + Glyphs["time.arrow"]
	case totalDays:
		return Glyphs["time.arrow"] + " " + ev.EndTime.Local().Format("15:04")
	default:
		return "cont. " + Glyphs["time.arrow"]
	}
}

// ensureVisible scrolls the viewport so the selected row is in view. When
// the selected row is the first event of a day and a separator precedes
// it, the separator is kept in view too.
func (m *AgendaModel) ensureVisible() {
	headerLines := 2
	viewportH := max(m.height-headerLines, 1)
	if m.selected < 0 || len(m.rows) == 0 {
		m.scroll = 0
		return
	}
	target := m.selected
	for target > 0 && !isEventRow(m.rows[target-1]) {
		target--
	}
	if target < m.scroll {
		m.scroll = target
	}
	bottom := m.scroll + viewportH - 1
	if m.selected > bottom {
		m.scroll = m.selected - viewportH + 1
	}
	m.clampScroll()
}

func (m *AgendaModel) clampScroll() {
	headerLines := 2
	viewportH := max(m.height-headerLines, 1)
	ms := m.maxScroll(viewportH)
	if m.scroll > ms {
		m.scroll = ms
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m AgendaModel) maxScroll(viewportH int) int {
	return max(len(m.rows)-viewportH, 0)
}

// ScrollBy advances the viewport by delta rows (positive scrolls down,
// negative scrolls up) without moving the selection. Used by the mouse
// wheel so scrolling feels decoupled from keyboard-driven selection.
func (m *AgendaModel) ScrollBy(delta int) {
	m.scroll += delta
	m.clampScroll()
}

// HandleClick routes a mouse click at (x, y) — in agenda-local
// coordinates — to the event row under the cursor. When the click lands
// on an event row, selection moves to that row and an
// EventViewRequestedMsg is returned so the host opens the view dialog,
// mirroring the Enter key binding.
func (m AgendaModel) HandleClick(_, y int) (AgendaModel, tea.Cmd) {
	headerLines := 2
	if y < headerLines {
		return m, nil
	}
	viewportH := max(m.height-headerLines, 1)
	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	idx := start + (y - headerLines)
	if idx < 0 || idx >= len(m.rows) || !isEventRow(m.rows[idx]) {
		return m, nil
	}
	m.selected = idx
	ev := m.rows[idx].event
	return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
}

// buildAgendaRows expands events into per-day rows covering the window
// [start, start+days). Events that span multiple days produce one row per
// day they touch. The first event of each day is tagged firstOfDay so the
// renderer can show the day-column label above it.
func buildAgendaRows(events []event.Event, start time.Time, days int) []agendaRow {
	end := start.AddDate(0, 0, days)

	byDay := make(map[string][]eventListDayEntry)
	for _, ev := range events {
		span := spanDays(ev)
		total := len(span)
		for i, d := range span {
			if d.Before(start) || !d.Before(end) {
				continue
			}
			key := d.Format("2006-01-02")
			byDay[key] = append(byDay[key], eventListDayEntry{
				ev:        ev,
				dayIndex:  i + 1,
				totalDays: total,
			})
		}
	}

	// Sort each day's entries: all-day first, then by effective start.
	for k, entries := range byDay {
		day, _ := time.ParseInLocation("2006-01-02", k, time.Local)
		sort.SliceStable(entries, func(a, b int) bool {
			ea, eb := entries[a].ev, entries[b].ev
			if ea.AllDay != eb.AllDay {
				return ea.AllDay
			}
			return effectiveStartOnDay(ea, day, entries[a].dayIndex).
				Before(effectiveStartOnDay(eb, day, entries[b].dayIndex))
		})
		byDay[k] = entries
	}

	var rows []agendaRow
	first := true
	firstMonth := monthKey(start)
	prevMonth := ""
	for i := range days {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		entries := byDay[key]
		if len(entries) == 0 {
			continue
		}
		if !first {
			rows = append(rows, agendaRow{day: d, separator: true})
		}
		first = false
		if mk := monthKey(d); mk != prevMonth && mk != firstMonth {
			rows = append(rows, agendaRow{day: d, monthHeader: true})
			rows = append(rows, agendaRow{day: d, separator: true})
		}
		prevMonth = monthKey(d)
		for j, entry := range entries {
			rows = append(rows, agendaRow{
				day:        d,
				event:      entry.ev,
				dayIndex:   entry.dayIndex,
				totalDays:  entry.totalDays,
				firstOfDay: j == 0,
			})
		}
	}
	return rows
}

func monthKey(t time.Time) string { return t.Format("2006-01") }

// nextSelectable returns the next event-row index.
func nextSelectable(rows []agendaRow, from int) int {
	for i := from + 1; i < len(rows); i++ {
		if isEventRow(rows[i]) {
			return i
		}
	}
	return from
}

// prevSelectable returns the previous event-row index.
func prevSelectable(rows []agendaRow, from int) int {
	for i := from - 1; i >= 0; i-- {
		if isEventRow(rows[i]) {
			return i
		}
	}
	return from
}

// firstSelectableOnOrAfter returns the first event-row index whose day is
// on or after the cursor. Falls back to the first selectable row, or -1
// when rows has none.
func firstSelectableOnOrAfter(rows []agendaRow, cursor time.Time) int {
	anchor := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, cursor.Location())
	first := -1
	for i, r := range rows {
		if !isEventRow(r) {
			continue
		}
		if first < 0 {
			first = i
		}
		if !r.day.Before(anchor) {
			return i
		}
	}
	return first
}
