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

// agendaRow is one rendered line in the agenda list. Rows are either a day
// header (non-selectable) or an event row (selectable). Empty days surface
// as a muted "No events" row so the cursor date is always visible.
type agendaRow struct {
	header    bool
	day       time.Time
	event     event.Event
	dayIndex  int // 1-based position within a multi-day span
	totalDays int
	emptyDay  bool // true when the row is a placeholder for a day with no events
}

type AgendaModel struct {
	cursor    time.Time
	today     time.Time
	events    []event.Event
	calendars map[int64]CalendarInfo
	rows      []agendaRow
	selected  int // index into rows; always points at a selectable row when any exist
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
		cursor: t,
		today:  t,
		keys:   defaultAgendaKeys(),
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
// back to the cursor when no row is selectable.
func (m AgendaModel) SelectedDay() time.Time {
	if m.selected >= 0 && m.selected < len(m.rows) {
		return m.rows[m.selected].day
	}
	return m.cursor
}

// SelectedEvent returns the event under the cursor, when the selected row is
// an event row.
func (m AgendaModel) SelectedEvent() (event.Event, bool) {
	if m.selected < 0 || m.selected >= len(m.rows) {
		return event.Event{}, false
	}
	r := m.rows[m.selected]
	if r.header || r.emptyDay {
		return event.Event{}, false
	}
	return r.event, true
}

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
	out.WriteString(lipgloss.NewStyle().Bold(true).Width(m.width).Align(lipgloss.Left).
		Render(fmt.Sprintf("Agenda · %s", m.cursor.Format("Monday, January 2, 2006"))))
	out.WriteString("\n\n")

	if len(m.rows) == 0 {
		out.WriteString(lipgloss.NewStyle().Faint(true).Render("No events in the next 30 days."))
		return out.String()
	}

	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.rows))

	for i := start; i < end; i++ {
		if i > start {
			out.WriteByte('\n')
		}
		out.WriteString(m.renderRow(m.rows[i], i == m.selected))
	}
	return out.String()
}

func (m AgendaModel) renderRow(r agendaRow, selected bool) string {
	if r.header {
		return m.renderHeader(r.day)
	}
	if r.emptyDay {
		return lipgloss.NewStyle().
			Foreground(m.theme.TextDim).
			Faint(true).
			Width(m.width).
			Render("  — no events —")
	}
	return m.renderEventRow(r, selected)
}

func (m AgendaModel) renderHeader(d time.Time) string {
	label := d.Format("Monday, Jan 2")
	style := lipgloss.NewStyle().Bold(true)
	if sameDay(d, m.today) {
		label += " · Today"
		style = style.Foreground(m.theme.Today)
	} else if sameDay(d, m.cursor) {
		style = style.Foreground(m.theme.Primary)
	}
	return style.Width(m.width).Render(label)
}

func (m AgendaModel) renderEventRow(r agendaRow, selected bool) string {
	ev := r.event
	timeCol := formatTimeColumnMulti(ev, r.dayIndex, r.totalDays)

	cal := m.calendars[ev.CalendarID]
	swatchColor := lipgloss.Color("8")
	if cal.Color != "" {
		swatchColor = lipgloss.Color(cal.Color)
	}
	swatch := lipgloss.NewStyle().Foreground(swatchColor).Render("●")

	title := ev.Title
	if r.totalDays > 1 {
		title += fmt.Sprintf(" (day %d/%d)", r.dayIndex, r.totalDays)
	}

	line := fmt.Sprintf("  %s  %s  %s", timeCol, swatch, title)
	if cal.Name != "" {
		suffix := lipgloss.NewStyle().Foreground(m.theme.TextDim).Render(" [" + cal.Name + "]")
		line += suffix
	}

	style := lipgloss.NewStyle().Width(m.width)
	if selected {
		style = style.Background(m.selectedColor).Foreground(m.theme.Text).Bold(true)
	}
	return style.Render(line)
}

// ensureVisible scrolls the viewport so the selected row is in view.
func (m *AgendaModel) ensureVisible() {
	headerLines := 2
	viewportH := max(m.height-headerLines, 1)
	if m.selected < 0 || len(m.rows) == 0 {
		m.scroll = 0
		return
	}
	// Keep the day header above the selected event in view too, when it's
	// adjacent.
	target := m.selected
	if target > 0 && m.rows[target-1].header {
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

// buildAgendaRows expands events into selectable rows grouped by day headers,
// covering the window [start, start+days). Events that span multiple days
// produce one row per day in the window they touch.
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
	for i := range days {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		entries := byDay[key]
		if len(entries) == 0 {
			continue
		}
		rows = append(rows, agendaRow{header: true, day: d})
		for _, entry := range entries {
			rows = append(rows, agendaRow{
				day:       d,
				event:     entry.ev,
				dayIndex:  entry.dayIndex,
				totalDays: entry.totalDays,
			})
		}
	}
	return rows
}

// nextSelectable returns the next row index that isn't a day header, wrapping
// at the end.
func nextSelectable(rows []agendaRow, from int) int {
	for i := from + 1; i < len(rows); i++ {
		if !rows[i].header && !rows[i].emptyDay {
			return i
		}
	}
	return from
}

func prevSelectable(rows []agendaRow, from int) int {
	for i := from - 1; i >= 0; i-- {
		if !rows[i].header && !rows[i].emptyDay {
			return i
		}
	}
	return from
}

// firstSelectableOnOrAfter returns the first event-row index whose day is on
// or after the cursor. Falls back to the first selectable row, or -1 when
// none exist.
func firstSelectableOnOrAfter(rows []agendaRow, cursor time.Time) int {
	anchor := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, cursor.Location())
	first := -1
	for i, r := range rows {
		if r.header || r.emptyDay {
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
