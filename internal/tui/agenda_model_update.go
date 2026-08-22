package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

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
		// Unconditional: the cursor stays at today during scroll. A
		// gate on sameDay(cursor, today) would make `t` a no-op even
		// when the user has scrolled far from today. Always reset
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
// rows do not fill the visible area. The host uses it after a fresh
// SetEvents (for example after `[`/`]` jumps). Sparse months then
// pull in the next month's events instead of blank rows below.
//
// The host calls this after every load, the load it triggers included.
// It must self-terminate. Unlike scroll-driven expansion there is no
// scroll position to bound it. It stops once an expansion fails to add
// rows. Without that guard a calendar with a few events in a tall
// terminal — underfilled but non-empty — would grow windowEnd forward
// without bound. It would re-query an ever-larger range on every step.
func (m *AgendaModel) MaybeFillViewport() tea.Cmd {
	if m.reloadPending || len(m.rows) == 0 {
		return nil
	}
	if len(m.rows) >= m.viewportH() {
		// Viewport is full; nothing to auto-fill. Clear the guard so a
		// later jump to another sparse month can auto-fill afresh.
		m.fillExpandRows = -1
		return nil
	}
	// Underfilled: pull in the next step, but only while each expansion
	// keeps adding rows. Once the row count stops growing, the future is
	// empty (or sparse enough that no step adds anything) — stop.
	if m.fillExpandRows >= 0 && len(m.rows) <= m.fillExpandRows {
		return nil
	}
	m.fillExpandRows = len(m.rows)
	m.windowEnd = m.windowEnd.AddDate(0, 0, AgendaExpandStep)
	return m.requestReload()
}

// requestReload stamps the scroll anchor, marks a reload in flight, and
// returns the command that asks the host to re-query the current window.
// All three window-growth paths funnel through here. The reloadPending
// handshake (cleared by SetEvents) is then asserted in exactly one place.
func (m *AgendaModel) requestReload() tea.Cmd {
	m.stampAnchor()
	m.reloadPending = true
	return func() tea.Msg { return AgendaReloadMsg{} }
}

// ScrollBy advances the viewport by delta rows (positive scrolls down,
// negative scrolls up) with no move of the selection. Used by the mouse
// wheel so scroll feels decoupled from keyboard-driven selection.
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
// edge (windowEnd) is held fixed. A slide of it backward would drop
// content the user still looks at. When the newly-included earlier
// range is empty in the DB the agenda would appear to "lose" all its
// data. The window has no hard cap so infinite scroll still works.
// Memory stays bounded by the user's scroll.
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
	return m.requestReload()
}

// maybeExpandForward is the mirror of maybeExpandBackward for the
// bottom edge. The near edge (windowStart) is held fixed for the same
// reason. It fires when the scroll or selection is within
// AgendaPreloadRows of the last row. Infinite scroll then keeps a feed of
// the next month as the user moves down. The underfill case — a sparse
// month that never fills the viewport — is handled by MaybeFillViewport.
// That bounds itself so it cannot loop forever.
func (m *AgendaModel) maybeExpandForward() tea.Cmd {
	if m.reloadPending || len(m.rows) == 0 {
		return nil
	}
	viewportH := m.viewportH()
	maxScroll := m.maxScroll(viewportH)
	atBottom := (maxScroll > 0 && m.scroll >= maxScroll-AgendaPreloadRows) ||
		(m.selected >= 0 && m.selected >= len(m.rows)-AgendaPreloadRows)
	if !atBottom {
		return nil
	}
	m.windowEnd = m.windowEnd.AddDate(0, 0, AgendaExpandStep)
	return m.requestReload()
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
// EventViewRequestedMsg is returned so the host opens the view dialog.
// That mirrors the Enter key binding. In the empty state, clicks on the
// "+ Create event" button emit EventCreateMsg instead.
func (m AgendaModel) HandleClick(x, y int) (AgendaModel, tea.Cmd) {
	headerLines := 2
	if y < headerLines || y >= m.height {
		return m, nil
	}
	if len(m.rows) == 0 {
		btnW, btnY := m.emptyButtonBounds()
		if y == btnY && x < btnW {
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
