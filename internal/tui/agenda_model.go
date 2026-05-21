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

// Agenda window sizing constants. The window grows as the user scrolls
// near either edge (infinite scroll); there is no hard cap — the opposite
// edge is never slid, because doing so would drop content the user is
// still looking at. Memory is bounded by the user's scrolling: each
// expansion adds AgendaExpandStep days, so a typical session stays well
// under any meaningful limit. Initial loads use AgendaWindowDays.
const (
	AgendaWindowDays  = 30
	AgendaExpandStep  = 30
	AgendaPreloadRows = 6
)

// agendaWheelStep is the number of rows advanced per mouse-wheel tick.
const agendaWheelStep = 3

// Fixed column widths for the agenda row layout. Kept as constants so the
// renderer and any layout tweaks stay in sync.
const (
	agendaDayColWidth  = 8  // "Wed  22 " or "Wed  22 " with today badge
	agendaDotColWidth  = 3  // " ● "
	agendaTimeColWidth = 13 // "09:00–10:30  " / "All day      "
	agendaLeftPad      = 0  // leading space in front of the day column
)

// AgendaCursorChangedMsg is emitted when the cursor moves to a new day, so
// the host model can reload events for the new agenda window.
type AgendaCursorChangedMsg struct{ Day time.Time }

// AgendaReloadMsg is emitted when the agenda's window bounds changed
// (e.g., the user scrolled near an edge and the window grew to preload
// more events). The host should re-query events for the current
// WindowStart()..WindowEnd() range and push them back via SetEvents.
type AgendaReloadMsg struct{}

// AgendaEmptyDaysToggledMsg is emitted when the user flips the "show
// empty days" toggle so the host can persist the new value in UIState.
type AgendaEmptyDaysToggledMsg struct{ ShowEmptyDays bool }

type agendaKeyMap struct {
	Up          key.Binding
	Down        key.Binding
	PrevDay     key.Binding
	NextDay     key.Binding
	PrevMonth   key.Binding
	NextMonth   key.Binding
	Today       key.Binding
	Select      key.Binding
	Create      key.Binding
	Edit        key.Binding
	Duplicate   key.Binding
	Delete      key.Binding
	ToggleEmpty key.Binding
}

func defaultAgendaKeys() agendaKeyMap {
	return agendaKeyMap{
		Up:          key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "previous")),
		Down:        key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "next")),
		PrevDay:     key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "prev day")),
		NextDay:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next day")),
		PrevMonth:   key.NewBinding(key.WithKeys("[", "pgup"), key.WithHelp("[", "prev month")),
		NextMonth:   key.NewBinding(key.WithKeys("]", "pgdown"), key.WithHelp("]", "next month")),
		Today:       key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:      key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "view")),
		Create:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Duplicate:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "duplicate")),
		Delete:      key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		ToggleEmpty: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "empty days")),
	}
}

// agendaRow is one rendered line in the agenda. Event rows are selectable
// and show the dot/time/title/calendar layout; separator rows are blank
// spacers drawn between day groups; monthHeader rows repeat the top-of-
// view title at each month boundary; emptyDay rows surface days with no
// events (shown only when the toggle is on), rendered with the day
// column and a faint "no events" label.
type agendaRow struct {
	day         time.Time
	event       event.Event
	dayIndex    int // 1-based position within a multi-day span
	totalDays   int
	firstOfDay  bool
	separator   bool
	monthHeader bool
	emptyDay    bool
}

type AgendaModel struct {
	cursor      time.Time
	today       time.Time
	windowStart time.Time // inclusive, day-aligned
	windowEnd   time.Time // exclusive, day-aligned
	events      []event.Event
	calendars   map[int64]CalendarInfo
	rows        []agendaRow
	selected    int // index into rows; -1 when empty
	scroll      int
	keys        agendaKeyMap
	theme       Theme
	width       int
	height      int
	// selectedColor highlights the focused event row. Set to theme.Selected.
	selectedColor color.Color
	// anchorDay, when non-zero, is the day the agenda wants to scroll back
	// to after the next SetEvents — used to keep the viewport stable across
	// infinite-scroll window expansions.
	anchorDay time.Time
	// reloadPending prevents firing a second AgendaReloadMsg while the
	// previous one is still in-flight; it's cleared by SetEvents.
	reloadPending bool
	// showEmptyDays, when true, renders a placeholder row for each day
	// in the window that has no events. Toggled by the "o" key.
	showEmptyDays bool
	// pendingSelectNow, when non-zero, asks the next SetEvents to select
	// the first event on the cursor day that's current (ends after now)
	// or upcoming, instead of falling back to the day's first event.
	// One-shot — consumed and cleared on the next SetEvents.
	pendingSelectNow time.Time
}

func NewAgendaModel(today time.Time) AgendaModel {
	t := dayAligned(today.Local())
	return AgendaModel{
		cursor:      t,
		today:       t,
		windowStart: t,
		windowEnd:   t.AddDate(0, 0, AgendaWindowDays),
		selected:    -1,
		keys:        defaultAgendaKeys(),
	}
}

func (m AgendaModel) Cursor() time.Time { return m.cursor }

// WindowStart returns the first day included in the current agenda window.
func (m AgendaModel) WindowStart() time.Time { return m.windowStart }

// WindowEnd returns the exclusive end of the current agenda window.
func (m AgendaModel) WindowEnd() time.Time { return m.windowEnd }

// ResetWindow re-centers the window around day with the default initial
// size. Use this after a "jump" navigation (today, sidebar click,
// h/l/[/] keys) so the next load reads a tight range around the target.
// Clears the prior selection so the next SetEvents lands the cursor day
// (or first event on/after it) at the top of the viewport — the prior
// selection's identity no longer applies after an explicit jump.
func (m AgendaModel) ResetWindow(day time.Time) AgendaModel {
	d := dayAligned(day)
	m.windowStart = d
	m.windowEnd = d.AddDate(0, 0, AgendaWindowDays)
	m.anchorDay = time.Time{}
	m.reloadPending = false
	m.selected = -1
	return m
}

func dayAligned(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

func firstOfMonth(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
}

func daysBetween(a, b time.Time) int {
	return int(b.Sub(a).Hours()/24 + 0.5)
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

// ShowEmptyDays reports whether empty-day placeholder rows are rendered.
func (m AgendaModel) ShowEmptyDays() bool { return m.showEmptyDays }

// SetShowEmptyDays sets the visibility of empty-day placeholder rows
// without rebuilding — callers should follow with SetEvents when the
// change is user-facing.
func (m AgendaModel) SetShowEmptyDays(v bool) AgendaModel {
	m.showEmptyDays = v
	return m
}

// SelectCurrentOrNext marks the next SetEvents to pick the first event
// on the cursor day whose end time is after now (or any all-day event),
// instead of the day's first event. One-shot — used when the user lands
// on the agenda view on today so the cursor sits on what's happening
// now or next, not on a meeting that already ended.
func (m AgendaModel) SelectCurrentOrNext(now time.Time) AgendaModel {
	m.pendingSelectNow = now
	return m
}

// SetEvents updates the cached event slice, the calendar info used for color
// and name lookups, and rebuilds the rendered rows. The previously-selected
// event is re-located by identity so background reloads and infinite-scroll
// expansions don't yank the user's selection away; when an anchor day was
// set (by an edge expansion), scroll also restores to that day so the
// viewport stays visually stable. Falls back to the cursor day when there
// was no prior selection.
func (m AgendaModel) SetEvents(events []event.Event, calendars map[int64]CalendarInfo) AgendaModel {
	m.events = events
	m.calendars = calendars
	days := daysBetween(m.windowStart, m.windowEnd)
	if days < 1 {
		days = AgendaWindowDays
	}

	// Snapshot the current selection by identity so we can re-find it in
	// the rebuilt rows.
	var prevDay, prevStart time.Time
	var prevID int64
	var prevEmpty, hadSel bool
	if m.selected >= 0 && m.selected < len(m.rows) {
		r := m.rows[m.selected]
		prevDay = r.day
		prevEmpty = r.emptyDay
		prevID = r.event.ID
		prevStart = r.event.StartTime
		hadSel = true
	}

	m.rows = buildAgendaRows(events, m.windowStart, days, m.showEmptyDays)
	anchor := m.anchorDay
	m.anchorDay = time.Time{}
	m.reloadPending = false

	m.selected = -1
	if hadSel {
		for i, r := range m.rows {
			if !isSelectableRow(r) || !sameDay(r.day, prevDay) {
				continue
			}
			if prevEmpty && r.emptyDay {
				m.selected = i
				break
			}
			if !prevEmpty && !r.emptyDay &&
				r.event.ID == prevID && r.event.StartTime.Equal(prevStart) {
				m.selected = i
				break
			}
		}
		if m.selected < 0 {
			m.selected = firstSelectableOnOrAfter(m.rows, prevDay)
		}
	}
	if m.selected < 0 {
		fallback := anchor
		if fallback.IsZero() {
			fallback = m.cursor
		}
		m.selected = firstSelectableOnOrAfter(m.rows, fallback)
	}

	if !m.pendingSelectNow.IsZero() {
		nowT := m.pendingSelectNow
		if idx := firstCurrentOrNextOn(m.rows, m.cursor, nowT); idx >= 0 {
			m.selected = idx
			m.pendingSelectNow = time.Time{}
		} else if hasEventOn(m.rows, m.cursor) {
			// Cursor day has events but none are current/upcoming — accept
			// the regular fallback (first event of today) and clear the
			// flag so subsequent loads don't second-guess the user.
			m.pendingSelectNow = time.Time{}
		}
		// else: no events on the cursor day yet (e.g., calendarsLoadedMsg
		// arrived before eventsLoadedMsg at startup). Keep the flag so the
		// next load can still apply it.
	}

	if !anchor.IsZero() {
		if idx := firstSelectableOnOrAfter(m.rows, anchor); idx >= 0 {
			m.scroll = idx
		}
	} else {
		// Full refresh after a jump (`[`/`]`/`t`/sidebar click) — scroll
		// so the newly-landed selection is at the top of the viewport.
		// Without this the scroll keeps its stale value and the user can
		// land on (for example) today's first event while the viewport
		// still shows rows well below today.
		if m.selected >= 0 {
			target := m.selected
			for target > 0 && !isSelectableRow(m.rows[target-1]) {
				target--
			}
			m.scroll = target
		} else {
			m.scroll = 0
		}
	}
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
	if !hasEvent(r) {
		return event.Event{}, false
	}
	return r.event, true
}

// isSelectableRow reports whether r can be the current selection — event
// rows and empty-day placeholders both qualify; separators and month
// headers don't.
func isSelectableRow(r agendaRow) bool {
	return !r.separator && !r.monthHeader
}

// hasEvent reports whether r carries a real event (as opposed to an
// empty-day placeholder).
func hasEvent(r agendaRow) bool {
	return isSelectableRow(r) && !r.emptyDay
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
		return m, m.maybeExpandBackward()
	case key.Matches(kp, m.keys.Down):
		m.selected = nextSelectable(m.rows, m.selected)
		m.ensureVisible()
		return m, m.maybeExpandForward()
	case key.Matches(kp, m.keys.PrevDay):
		return m.moveCursor(m.cursor.AddDate(0, 0, -1))
	case key.Matches(kp, m.keys.NextDay):
		return m.moveCursor(m.cursor.AddDate(0, 0, 1))
	case key.Matches(kp, m.keys.PrevMonth):
		return m.moveCursor(firstOfMonth(m.cursor).AddDate(0, -1, 0))
	case key.Matches(kp, m.keys.NextMonth):
		return m.moveCursor(firstOfMonth(m.cursor).AddDate(0, 1, 0))
	case key.Matches(kp, m.keys.Today):
		// Unconditional: the cursor stays at today during scrolling, so
		// gating on sameDay(cursor, today) would make `t` a no-op even
		// when the user has scrolled far away from today. Always reset
		// the window so the viewport snaps back to today's events.
		m.cursor = m.today
		cursor := m.cursor
		return m, func() tea.Msg { return AgendaCursorChangedMsg{Day: cursor} }
	case key.Matches(kp, m.keys.Select):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return EventViewRequestedMsg{Event: ev} }
		}
		// Empty list, or empty-day placeholder selected: treat Enter as
		// "create event on the selected day".
		if len(m.rows) == 0 ||
			(m.selected >= 0 && m.selected < len(m.rows) && m.rows[m.selected].emptyDay) {
			day := m.SelectedDay()
			return m, func() tea.Msg { return EventCreateMsg{Day: day} }
		}
		return m, nil
	case key.Matches(kp, m.keys.Create):
		day := m.SelectedDay()
		return m, func() tea.Msg { return EventCreateMsg{Day: day} }
	case key.Matches(kp, m.keys.Edit):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return EventEditMsg{Event: ev} }
		}
		return m, nil
	case key.Matches(kp, m.keys.Duplicate):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return EventDuplicateMsg{Event: ev} }
		}
		return m, nil
	case key.Matches(kp, m.keys.Delete):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return EventDeleteMsg{Event: ev} }
		}
		return m, nil
	case key.Matches(kp, m.keys.ToggleEmpty):
		m.showEmptyDays = !m.showEmptyDays
		days := daysBetween(m.windowStart, m.windowEnd)
		if days < 1 {
			days = AgendaWindowDays
		}
		m.rows = buildAgendaRows(m.events, m.windowStart, days, m.showEmptyDays)
		m.selected = firstSelectableOnOrAfter(m.rows, m.cursor)
		m.clampScroll()
		show := m.showEmptyDays
		return m, func() tea.Msg { return AgendaEmptyDaysToggledMsg{ShowEmptyDays: show} }
	}
	return m, nil
}

func (m AgendaModel) moveCursor(to time.Time) (AgendaModel, tea.Cmd) {
	m.cursor = to
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
		out.WriteString(lipgloss.NewStyle().Foreground(m.theme.TextDim).Render("No events this month."))
		out.WriteString("\n\n")
		out.WriteString(DefaultButtonStyles().Normal.Normal.Render("+ Create event"))
		return out.String()
	}

	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.rows))

	// Sticky title uses position:sticky semantics — it reflects the most
	// recent monthHeader at or above the viewport top. When none has
	// scrolled past yet, use the day of the first visible row instead of
	// windowStart: events can start in a later month than windowStart if
	// earlier months are empty (common after a backward expansion), and
	// falling back to windowStart would advertise a month the user can't
	// see any events from.
	headerDay := m.windowStart
	foundAbove := false
	for i := min(start, len(m.rows)-1); i >= 0; i-- {
		if m.rows[i].monthHeader {
			headerDay = m.rows[i].day
			foundAbove = true
			break
		}
	}
	if !foundAbove && start < len(m.rows) {
		headerDay = m.rows[start].day
	}
	stickyMonth := monthKey(headerDay)
	out.WriteString(m.renderMonthHeader(headerDay))
	out.WriteString("\n\n")

	// Skip any leading separator/monthHeader rows that label the sticky's
	// month — otherwise the user sees the month name twice back-to-back
	// (once in the sticky, once inline). Extend the render range so the
	// viewport stays filled.
	renderStart := start
	for renderStart < end {
		r := m.rows[renderStart]
		if !(r.monthHeader || r.separator) || monthKey(r.day) != stickyMonth {
			break
		}
		renderStart++
		end = min(end+1, len(m.rows))
	}

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
		if m.rows[i].emptyDay {
			out.WriteString(m.renderEmptyDayRow(m.rows[i], i == m.selected))
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

// renderEmptyDayRow draws a placeholder for a day with no events. When
// selected, surfaces a "+ Create event" affordance to invite the user
// to create on that day.
func (m AgendaModel) renderEmptyDayRow(r agendaRow, selected bool) string {
	// Day column stays unpainted; the selection highlight starts where the
	// time column would begin and runs to the end of the line.
	unpainted := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.selectedColor)
	}
	dayCol := m.renderDayColumn(r, unpainted, false)
	prefix := unpainted.Render(strings.Repeat(" ", agendaLeftPad)) +
		dayCol +
		unpainted.Render(" "+strings.Repeat(" ", agendaDotColWidth))

	tail := ""
	if selected {
		tail = highlight.Foreground(m.theme.Primary).Bold(true).Render("+ Create event")
	}
	tailW := max(m.width-lipgloss.Width(prefix), 0)
	tailFg := m.theme.Text
	if selected {
		tailFg = m.theme.SelectedText
	}
	return prefix + highlight.Width(tailW).Foreground(tailFg).Render(tail)
}

// renderEventRow composes a single agenda line. When selected, the
// highlight starts at the time column and paints to the end of the line;
// the day column is intentionally left unpainted so the date badge
// remains visually anchored.
func (m AgendaModel) renderEventRow(r agendaRow, selected bool) string {
	ev := r.event

	unpainted := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.selectedColor).Bold(true)
	}

	dayCol := m.renderDayColumn(r, unpainted, false)

	cal := m.calendars[ev.CalendarID]
	dotColor := m.theme.Muted
	if cal.Color != "" {
		dotColor = lipgloss.Color(cal.Color)
	}
	dot := unpainted.Foreground(dotColor).Render(Glyphs["dot"])

	timeText := agendaTimeText(ev, r.dayIndex, r.totalDays)
	timeFg := m.theme.TextDim
	if selected {
		timeFg = m.theme.SelectedText
	}
	timeStyle := highlight.Foreground(timeFg).Width(agendaTimeColWidth)
	if ev.AllDay {
		timeStyle = timeStyle.Italic(true)
	}
	timeCol := timeStyle.Render(timeText)

	title := ev.Title
	if r.totalDays > 1 {
		title += fmt.Sprintf(" (day %d/%d)", r.dayIndex, r.totalDays)
	}

	prefix := unpainted.Render(strings.Repeat(" ", agendaLeftPad)) +
		dayCol +
		unpainted.Render(" ") +
		unpainted.Width(agendaDotColWidth).Render(" "+dot+" ")

	titleW := max(m.width-lipgloss.Width(prefix)-agendaTimeColWidth, 1)
	titleFg := m.theme.Text
	if selected {
		titleFg = m.theme.SelectedText
	}
	titleCol := highlight.
		Foreground(titleFg).
		Width(titleW).
		Render(truncateTo(title, titleW))

	return prefix + timeCol + titleCol
}

// renderDayColumn returns the 8-column-wide day label shown at the start
// of the first event row of a calendar day. Continuation rows get a blank
// column. Today's day number is rendered in a filled pill using the theme
// "today" color. The day column is never painted by the selection
// highlight — callers pass an empty base so the date badge stays visually
// anchored regardless of row state.
func (m AgendaModel) renderDayColumn(r agendaRow, base lipgloss.Style, _ bool) string {
	if !r.firstOfDay {
		return base.Render(strings.Repeat(" ", agendaDayColWidth))
	}
	d := r.day
	weekday := d.Format("Mon")
	dayNum := fmt.Sprintf("%d", d.Day())

	isToday := sameDay(d, m.today)

	var weekdayStyle, numStyle lipgloss.Style
	switch {
	case isToday:
		weekdayStyle = base.Foreground(m.theme.Today).Bold(true)
		numStyle = lipgloss.NewStyle().
			Background(m.theme.Today).
			Foreground(m.theme.Surface).
			Bold(true).
			PaddingRight(1)
	default:
		weekdayStyle = base.Foreground(m.theme.TextDim)
		numStyle = base.Foreground(m.theme.Text).Bold(true)
	}

	body := numStyle.Render(dayNum) + base.Render(" ") + weekdayStyle.Render(weekday)
	return base.Width(agendaDayColWidth).Render(body)
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
	for target > 0 && !isSelectableRow(m.rows[target-1]) {
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

// MaybeFillViewport returns a forward-expansion command when the loaded
// rows don't fill the visible area — used by the host after a fresh
// SetEvents (e.g. after `[`/`]` jumps) so sparse months automatically
// pull in the next month's events instead of leaving blank rows below.
func (m *AgendaModel) MaybeFillViewport() tea.Cmd {
	return m.maybeExpandForward()
}

// ScrollBy advances the viewport by delta rows (positive scrolls down,
// negative scrolls up) without moving the selection. Used by the mouse
// wheel so scrolling feels decoupled from keyboard-driven selection.
// Returns a reload command only when the scroll direction matched an
// edge the window can still grow toward.
func (m *AgendaModel) ScrollBy(delta int) tea.Cmd {
	if delta == 0 {
		return nil
	}
	m.scroll += delta
	m.clampScroll()
	if delta < 0 {
		return m.maybeExpandBackward()
	}
	return m.maybeExpandForward()
}

// maybeExpandBackward grows the window toward older dates when the
// scroll or selection is within AgendaPreloadRows of the top. The far
// edge (windowEnd) is held fixed — sliding it backward would drop
// content the user is still looking at, and when the newly-included
// earlier range is empty in the DB the agenda would appear to "lose"
// all its data. The window has no hard cap so infinite scroll keeps
// working; memory stays bounded by the user's scrolling.
func (m *AgendaModel) maybeExpandBackward() tea.Cmd {
	if m.reloadPending || len(m.rows) == 0 {
		return nil
	}
	atTop := m.scroll <= AgendaPreloadRows ||
		(m.selected >= 0 && m.selected <= AgendaPreloadRows)
	if !atTop {
		return nil
	}
	m.windowStart = m.windowStart.AddDate(0, 0, -AgendaExpandStep)
	m.stampAnchor()
	m.reloadPending = true
	return func() tea.Msg { return AgendaReloadMsg{} }
}

// maybeExpandForward is the mirror of maybeExpandBackward for the
// bottom edge — the near edge (windowStart) is held fixed for the same
// reason. It also fires when the loaded rows don't fill the viewport
// (e.g. after `[`/`]` jumps land on a sparse month), so the next month
// flows in automatically instead of waiting for the user to navigate.
func (m *AgendaModel) maybeExpandForward() tea.Cmd {
	if m.reloadPending || len(m.rows) == 0 {
		return nil
	}
	viewportH := m.viewportH()
	underfilled := len(m.rows) < viewportH
	maxScroll := m.maxScroll(viewportH)
	atBottom := underfilled ||
		(maxScroll > 0 && m.scroll >= maxScroll-AgendaPreloadRows) ||
		(m.selected >= 0 && m.selected >= len(m.rows)-AgendaPreloadRows)
	if !atBottom {
		return nil
	}
	m.windowEnd = m.windowEnd.AddDate(0, 0, AgendaExpandStep)
	m.stampAnchor()
	m.reloadPending = true
	return func() tea.Msg { return AgendaReloadMsg{} }
}

func (m *AgendaModel) stampAnchor() {
	switch {
	case m.scroll >= 0 && m.scroll < len(m.rows):
		m.anchorDay = m.rows[m.scroll].day
	case m.selected >= 0 && m.selected < len(m.rows):
		m.anchorDay = m.rows[m.selected].day
	}
}

func (m AgendaModel) viewportH() int {
	return max(m.height-2, 1)
}

// HandleClick routes a mouse click at (x, y) — in agenda-local
// coordinates — to the event row under the cursor. When the click lands
// on an event row, selection moves to that row and an
// EventViewRequestedMsg is returned so the host opens the view dialog,
// mirroring the Enter key binding. In the empty state, clicks on the
// "+ Create event" button emit EventCreateMsg instead.
func (m AgendaModel) HandleClick(x, y int) (AgendaModel, tea.Cmd) {
	headerLines := 2
	if y < headerLines || y >= m.height {
		return m, nil
	}
	if len(m.rows) == 0 {
		btnW, btnY := m.emptyButtonBounds()
		if y == btnY && x >= 2 && x < 2+btnW {
			day := m.SelectedDay()
			return m, func() tea.Msg { return EventCreateMsg{Day: day} }
		}
		return m, nil
	}
	viewportH := max(m.height-headerLines, 1)
	if y-headerLines >= viewportH {
		return m, nil
	}
	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	idx := start + (y - headerLines)
	if idx < 0 || idx >= len(m.rows) || !isSelectableRow(m.rows[idx]) {
		return m, nil
	}
	m.selected = idx
	r := m.rows[idx]
	if r.emptyDay {
		day := r.day
		return m, func() tea.Msg { return EventCreateMsg{Day: day} }
	}
	return m, func() tea.Msg { return EventViewRequestedMsg{Event: r.event} }
}

// emptyButtonBounds returns the visible width and local Y-line of the
// "+ Create event" button rendered in the empty state.
func (m AgendaModel) emptyButtonBounds() (int, int) {
	btn := DefaultButtonStyles().Normal.Normal.Render("+ Create event")
	// Header(1) + blank(1) + "No events"(1) + blank(1) + button line = y=4.
	return lipgloss.Width(btn), 4
}

// buildAgendaRows expands events into per-day rows covering the window
// [start, start+days). Events that span multiple days produce one row per
// day they touch. The first event of each day is tagged firstOfDay so the
// renderer can show the day-column label above it. When showEmpty is true,
// days with no events get a non-selectable emptyDay placeholder row.
func buildAgendaRows(events []event.Event, start time.Time, days int, showEmpty bool) []agendaRow {
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
	firstMonth := monthKey(start)
	prevMonth := ""
	for i := range days {
		d := start.AddDate(0, 0, i)
		key := d.Format("2006-01-02")
		entries := byDay[key]
		if len(entries) == 0 && !showEmpty {
			continue
		}
		if mk := monthKey(d); mk != prevMonth && mk != firstMonth {
			rows = append(rows, agendaRow{day: d, separator: true})
			rows = append(rows, agendaRow{day: d, monthHeader: true})
			rows = append(rows, agendaRow{day: d, separator: true})
		}
		prevMonth = monthKey(d)
		if len(entries) == 0 {
			rows = append(rows, agendaRow{day: d, emptyDay: true, firstOfDay: true})
			continue
		}
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

// nextSelectable returns the next selectable row index.
func nextSelectable(rows []agendaRow, from int) int {
	for i := from + 1; i < len(rows); i++ {
		if isSelectableRow(rows[i]) {
			return i
		}
	}
	return from
}

// prevSelectable returns the previous selectable row index.
func prevSelectable(rows []agendaRow, from int) int {
	for i := from - 1; i >= 0; i-- {
		if isSelectableRow(rows[i]) {
			return i
		}
	}
	return from
}

// hasEventOn reports whether any selectable event row lies on day.
func hasEventOn(rows []agendaRow, day time.Time) bool {
	for _, r := range rows {
		if hasEvent(r) && sameDay(r.day, day) {
			return true
		}
	}
	return false
}

// firstCurrentOrNextOn returns the first selectable event row on day
// whose event is current (ends after now, or all-day) or upcoming (starts
// at or after now). Returns -1 when no event row qualifies. Used to land
// the cursor on what's happening now or next, not on a meeting that
// already ended.
func firstCurrentOrNextOn(rows []agendaRow, day, now time.Time) int {
	for i, r := range rows {
		if !hasEvent(r) || !sameDay(r.day, day) {
			continue
		}
		ev := r.event
		if ev.AllDay {
			return i
		}
		end := ev.EndTime
		if end.IsZero() {
			end = ev.StartTime
		}
		if end.After(now) {
			return i
		}
	}
	return -1
}

// firstSelectableOnOrAfter returns the first selectable row index whose
// day is on or after the cursor. Falls back to the first selectable
// row, or -1 when rows has none.
func firstSelectableOnOrAfter(rows []agendaRow, cursor time.Time) int {
	anchor := time.Date(cursor.Year(), cursor.Month(), cursor.Day(), 0, 0, 0, 0, cursor.Location())
	first := -1
	for i, r := range rows {
		if !isSelectableRow(r) {
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
