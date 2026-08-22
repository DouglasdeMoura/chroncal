package tui

import (
	"image/color"
	"sort"
	"time"

	"charm.land/bubbles/v2/key"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// Agenda window sizing constants. The window grows as the user scrolls
// near either edge (infinite scroll). There is no hard cap. The opposite
// edge is never slid. That would drop content the user still looks at.
// Memory is bounded by the user's scroll. Each expansion adds
// AgendaExpandStep days. A typical session then stays well under any
// meaningful limit. Initial loads use AgendaWindowDays.
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
		Select:      key.NewBinding(key.WithKeys("enter", "space"), key.WithHelp("enter", "view")),
		Create:      key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "new")),
		Edit:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Duplicate:   key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "duplicate")),
		Delete:      key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		ToggleEmpty: key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "empty days")),
	}
}

// agendaRow is one rendered line in the agenda. Event rows are selectable
// and show the dot/time/title/calendar layout. Separator rows are blank
// spacers drawn between day groups. monthHeader rows repeat the top-of-
// view title at each month boundary. emptyDay rows surface days with no
// events (shown only when the toggle is on). They are rendered with the
// day column and a faint "no events" label.
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
	// anchorDay, when non-zero, is the day the agenda must scroll back
	// to after the next SetEvents. That keeps the viewport stable across
	// infinite-scroll window expansions.
	anchorDay time.Time
	// reloadPending prevents firing a second AgendaReloadMsg while the
	// previous one is still in-flight; it's cleared by SetEvents.
	reloadPending bool
	// fillExpandRows bounds the underfill auto-fill (see MaybeFillViewport).
	// It records len(rows) at the last auto-fill expansion. The next
	// expansion is then suppressed unless the row count grew. Otherwise a
	// sparse calendar in a tall terminal would extend windowEnd with no
	// bound. -1 means "no auto-fill yet". Reset to -1 on a jump
	// (ResetWindow) or once the viewport fills.
	fillExpandRows int
	// showEmptyDays, when true, renders a placeholder row for each day
	// in the window that has no events. Toggled by the "o" key.
	showEmptyDays bool
	// pendingSelectNow, when non-zero, asks the next SetEvents to select
	// the first event on the cursor day that is current (ends after now)
	// or upcoming. It does not use the day's first event as a fallback.
	// One-shot — consumed and cleared on the next SetEvents.
	pendingSelectNow time.Time
}

func NewAgendaModel(today time.Time) AgendaModel {
	t := dayAligned(today.Local())
	return AgendaModel{
		cursor:         t,
		today:          t,
		windowStart:    t,
		windowEnd:      t.AddDate(0, 0, AgendaWindowDays),
		selected:       -1,
		keys:           defaultAgendaKeys(),
		fillExpandRows: -1,
	}
}

func (m AgendaModel) Cursor() time.Time { return m.cursor }

// WindowStart returns the first day included in the current agenda window.
func (m AgendaModel) WindowStart() time.Time { return m.windowStart }

// WindowEnd returns the exclusive end of the current agenda window.
func (m AgendaModel) WindowEnd() time.Time { return m.windowEnd }

// ResetWindow re-centers the window around day with the default initial
// size. Use this after a "jump" navigation (today, sidebar click,
// h/l/[/] keys). The next load then reads a tight range around the target.
// It clears the prior selection so the next SetEvents lands the cursor day
// (or first event on/after it) at the top of the viewport. The prior
// selection's identity no longer applies after an explicit jump.
func (m AgendaModel) ResetWindow(day time.Time) AgendaModel {
	d := dayAligned(day)
	m.windowStart = d
	m.windowEnd = d.AddDate(0, 0, AgendaWindowDays)
	m.anchorDay = time.Time{}
	m.reloadPending = false
	m.fillExpandRows = -1
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
// with no rebuild. Callers should follow with SetEvents when the
// change is user-facing.
func (m AgendaModel) SetShowEmptyDays(v bool) AgendaModel {
	m.showEmptyDays = v
	return m
}

// SelectCurrentOrNext marks the next SetEvents to pick the first event
// on the cursor day whose end time is after now (or any all-day event).
// It does not pick the day's first event. One-shot. Used when the user
// lands on the agenda view on today. The cursor then sits on what
// happens now or next. It does not sit on a meeting that already ended.
func (m AgendaModel) SelectCurrentOrNext(now time.Time) AgendaModel {
	m.pendingSelectNow = now
	return m
}

// SetEvents updates the cached event slice and the calendar info used for
// color and name lookups. It rebuilds the rendered rows. The previously-
// selected event is re-located by identity. Background reloads and
// infinite-scroll expansions then do not yank the user's selection away.
// When an anchor day was set (by an edge expansion), scroll also restores
// to that day so the viewport stays visually stable. It falls back to the
// cursor day when there was no prior selection.
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
			// Cursor day has events but none are current/upcoming. Accept
			// the regular fallback (first event of today). Clear the
			// flag so later loads do not second-guess the user.
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
		// Full refresh after a jump (`[`/`]`/`t`/sidebar click). Scroll
		// so the new selection is at the top of the viewport.
		// Without this the scroll keeps its stale value. The user can
		// then land on today's first event while the viewport still
		// shows rows well below today.
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

// SelectedDay returns the day associated with the current selection. It
// falls back to the cursor when no row is selected.
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

// SelectEvent moves the cursor to the row for id at start. Exact StartTime
// wins, then the same local day, then the first row with that ID.
func (m AgendaModel) SelectEvent(id int64, start time.Time) AgendaModel {
	if id == 0 {
		return m
	}
	exact, dayMatch, anyMatch := -1, -1, -1
	for i, r := range m.rows {
		if !hasEvent(r) || r.event.ID != id {
			continue
		}
		if anyMatch < 0 {
			anyMatch = i
		}
		if !start.IsZero() && r.event.StartTime.Equal(start) {
			exact = i
			break
		}
		if !start.IsZero() && sameDay(r.event.StartTime, start) && dayMatch < 0 {
			dayMatch = i
		}
	}
	idx := exact
	if idx < 0 {
		idx = dayMatch
	}
	if idx < 0 {
		idx = anyMatch
	}
	if idx < 0 {
		return m
	}
	m.selected = idx
	m.ensureVisible()
	return m
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

// buildAgendaRows expands events into per-day rows that cover the window
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
// whose event is current or next. Current means the event ends after now,
// or is all-day. Next means the event starts at or after now. Returns -1
// when no event row qualifies. Used to land the cursor on what happens
// now or next. Not on a meeting that already ended.
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
