package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// MiniMonthDateSelectedMsg is emitted when the user presses Enter on a day.
// The parent (app.go) decides what to do — typically move the active main view.
type MiniMonthDateSelectedMsg struct{ Date time.Time }

// MiniMonthMonthChangedMsg is emitted whenever the displayed month changes
// (via cursor crossing a boundary, chevron / [ / ] shifts, or snap-to-today).
// The parent uses this to (re)load the per-day event-density map for the new
// month. Month changes are preview-only and do NOT carry into the main view;
// only an explicit day selection (MiniMonthDateSelectedMsg) drives the main
// view's cursor.
type MiniMonthMonthChangedMsg struct{ Month time.Time }

type miniMonthKeyMap struct {
	Up, Down, Left, Right key.Binding
	PrevMonth, NextMonth  key.Binding
	Today, Select         key.Binding
}

func defaultMiniMonthKeys() miniMonthKeyMap {
	return miniMonthKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:      key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "left")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "right")),
		PrevMonth: key.NewBinding(key.WithKeys("["), key.WithHelp("[", "prev month")),
		NextMonth: key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next month")),
		Today:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
		Select:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "jump main view")),
	}
}

// miniInnerFocus tracks which sub-element of the mini-month has focus for
// keyboard input. Tab order is: prev chevron → next chevron → day grid,
// matching a reading-order traversal of the widget's visual layout. After
// the grid the sidebar hands off focus to the calendar list.
type miniInnerFocus int

const (
	innerFocusPrev miniInnerFocus = iota
	innerFocusNext
	innerFocusGrid
)

// MiniMonthModel is a compact month picker that lives in the sidebar.
// Its cursor is independent of the main calendar view; navigation only
// affects the main view when the user presses Enter.
type MiniMonthModel struct {
	cursor       time.Time // selected day
	displayMonth time.Time // first-of-month for the rendered grid
	keys         miniMonthKeyMap
	focused      bool
	innerFocus   miniInnerFocus
	accentColor  color.Color
	todayColor   color.Color
	textColor    color.Color
	mutedColor   color.Color
	// eventDays holds "YYYY-MM-DD" keys for days that have at least one
	// visible event; rendered as a combining dot below the day number.
	eventDays map[string]bool
}

func NewMiniMonthModel(initial time.Time) MiniMonthModel {
	d := initial
	return MiniMonthModel{
		cursor:       d,
		displayMonth: time.Date(d.Year(), d.Month(), 1, 0, 0, 0, 0, d.Location()),
		keys:         defaultMiniMonthKeys(),
		innerFocus:   innerFocusGrid,
	}
}

func (m MiniMonthModel) SetTheme(accent, today, text, muted color.Color) MiniMonthModel {
	m.accentColor = accent
	m.todayColor = today
	m.textColor = text
	m.mutedColor = muted
	return m
}

// SetEventDays replaces the set of days (keyed "YYYY-MM-DD") that should be
// marked as having at least one visible event. Passing nil clears the set.
func (m MiniMonthModel) SetEventDays(days map[string]bool) MiniMonthModel {
	m.eventDays = days
	return m
}

func (m MiniMonthModel) Focus() MiniMonthModel { m.focused = true; return m }
func (m MiniMonthModel) Blur() MiniMonthModel  { m.focused = false; return m }
func (m MiniMonthModel) Focused() bool         { return m.focused }

func (m MiniMonthModel) Cursor() time.Time       { return m.cursor }
func (m MiniMonthModel) DisplayMonth() time.Time { return m.displayMonth }

// Tab-stop traversal API used by SidebarModel to integrate the mini-month's
// internal sub-widgets (prev chevron, day grid, next chevron) into the
// sidebar's forward/backward Tab routing.

// AtStart reports whether inner focus is on the first tab stop (prev chevron).
func (m MiniMonthModel) AtStart() bool { return m.innerFocus == innerFocusPrev }

// AtEnd reports whether inner focus is on the last tab stop (day grid).
func (m MiniMonthModel) AtEnd() bool { return m.innerFocus == innerFocusGrid }

// FocusFirst resets inner focus to the first tab stop (prev chevron).
func (m MiniMonthModel) FocusFirst() MiniMonthModel {
	m.innerFocus = innerFocusPrev
	return m
}

// FocusLast resets inner focus to the last tab stop (day grid).
func (m MiniMonthModel) FocusLast() MiniMonthModel {
	m.innerFocus = innerFocusGrid
	return m
}

// FocusGrid resets inner focus to the day grid (e.g. after a click on a day
// or when entering the sidebar via the `s` toggle rather than via Tab).
func (m MiniMonthModel) FocusGrid() MiniMonthModel {
	m.innerFocus = innerFocusGrid
	return m
}

// AdvanceFocus moves inner focus one tab stop forward (prev → next → grid).
// Caller is responsible for checking AtEnd() first.
func (m MiniMonthModel) AdvanceFocus() MiniMonthModel {
	if m.innerFocus < innerFocusGrid {
		m.innerFocus++
	}
	return m
}

// RetreatFocus moves inner focus one tab stop backward.
func (m MiniMonthModel) RetreatFocus() MiniMonthModel {
	if m.innerFocus > innerFocusPrev {
		m.innerFocus--
	}
	return m
}

// monthChangedCmd returns a cmd that emits MiniMonthMonthChangedMsg with the
// current displayMonth, or nil if prev is already in the same month.
func (m MiniMonthModel) monthChangedCmd(prev time.Time) tea.Cmd {
	if prev.Year() == m.displayMonth.Year() && prev.Month() == m.displayMonth.Month() {
		return nil
	}
	month := m.displayMonth
	return func() tea.Msg { return MiniMonthMonthChangedMsg{Month: month} }
}

func (m MiniMonthModel) moveCursor(dx, dy int) (MiniMonthModel, tea.Cmd) {
	prev := m.displayMonth
	next := m.cursor.AddDate(0, 0, dy*7+dx)
	m.cursor = next
	if next.Year() != m.displayMonth.Year() || next.Month() != m.displayMonth.Month() {
		m.displayMonth = time.Date(next.Year(), next.Month(), 1, 0, 0, 0, 0, next.Location())
	}
	return m, m.monthChangedCmd(prev)
}

func (m MiniMonthModel) shiftMonth(delta int) (MiniMonthModel, tea.Cmd) {
	prev := m.displayMonth
	m.displayMonth = m.displayMonth.AddDate(0, delta, 0)
	// Snap the cursor to the first of the new month so that when Tab
	// arrives at the day grid after a chevron shift, the selection is the
	// first day of the newly displayed month instead of a date that's no
	// longer in view.
	m.cursor = m.displayMonth
	return m, m.monthChangedCmd(prev)
}

func (m MiniMonthModel) snapToday() (MiniMonthModel, tea.Cmd) {
	prev := m.displayMonth
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	m.cursor = today
	m.displayMonth = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
	return m, m.monthChangedCmd(prev)
}

func (m MiniMonthModel) Update(msg tea.Msg) (MiniMonthModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	// Arrow keys are tied to the grid cursor; ignore them when inner focus
	// is on a chevron so Tab navigation doesn't silently re-select a day.
	gridFocused := m.innerFocus == innerFocusGrid
	switch {
	case gridFocused && key.Matches(kp, m.keys.Up):
		return m.moveCursor(0, -1)
	case gridFocused && key.Matches(kp, m.keys.Down):
		return m.moveCursor(0, 1)
	case gridFocused && key.Matches(kp, m.keys.Left):
		return m.moveCursor(-1, 0)
	case gridFocused && key.Matches(kp, m.keys.Right):
		return m.moveCursor(1, 0)
	case key.Matches(kp, m.keys.PrevMonth):
		return m.shiftMonth(-1)
	case key.Matches(kp, m.keys.NextMonth):
		return m.shiftMonth(1)
	case key.Matches(kp, m.keys.Today):
		return m.snapToday()
	case key.Matches(kp, m.keys.Select):
		// Enter semantics depend on which sub-widget is focused.
		switch m.innerFocus {
		case innerFocusPrev:
			return m.shiftMonth(-1)
		case innerFocusNext:
			return m.shiftMonth(1)
		default:
			sel := m.cursor
			return m, func() tea.Msg { return MiniMonthDateSelectedMsg{Date: sel} }
		}
	}
	return m, nil
}

// miniMonthHeaderWidth is the header row width in display columns. It matches
// the weekday row ("Su Mo Tu We Th Fr Sa") and the seven day cells below it so
// the chevrons align with the right edge of the grid.
const miniMonthHeaderWidth = 20

// miniMonthGridRows is the fixed number of day-grid rows rendered. A Gregorian
// month spans at most 6 weeks, so padding every month to 6 rows keeps the
// widget height constant and prevents the sidebar's calendar list from
// shifting when the displayed month changes.
const miniMonthGridRows = 6

// chevronPositions returns the 0-indexed columns (relative to the widget's
// top-left) for the left and right chevrons. Header layout:
// "<name><padding><‹> <›>" — name is flush left, chevrons flush right.
func (m MiniMonthModel) chevronPositions() (leftX, rightX int) {
	name := m.displayMonth.Format("January 2006")
	nameWidth := lipgloss.Width(name)
	// "‹ ›" is three display columns. Keep at least one space between the
	// name and the chevrons so very long month names don't butt up against
	// the glyphs.
	padding := max(miniMonthHeaderWidth-nameWidth-3, 1)
	leftX = nameWidth + padding
	rightX = leftX + 2
	return leftX, rightX
}

// HandleClick hit-tests the click at (x, y) in the widget's local coordinates
// (top-left of the rendered view is (0, 0)) and, if it lands on a chevron,
// shifts the month. Clicks on a day cell move the cursor and select it.
func (m MiniMonthModel) HandleClick(x, y int) (MiniMonthModel, tea.Cmd) {
	if y == 0 {
		// Header row: chevrons sit together on the right. Each hit zone is
		// the glyph's own column for precise targeting.
		leftX, rightX := m.chevronPositions()
		switch x {
		case leftX:
			m.innerFocus = innerFocusPrev
			return m.shiftMonth(-1)
		case rightX:
			m.innerFocus = innerFocusNext
			return m.shiftMonth(1)
		}
		return m, nil
	}
	// Day grid rows start at y=2 (header is 0, weekday row is 1). Each cell
	// occupies 3 display columns ("%2d" + separator).
	gridY := y - 2
	if gridY < 0 {
		return m, nil
	}
	col := x / 3
	if col < 0 || col > 6 {
		return m, nil
	}
	leading := int(m.displayMonth.Weekday())
	dayIndex := gridY*7 + col - leading
	if dayIndex < 0 {
		return m, nil
	}
	candidate := m.displayMonth.AddDate(0, 0, dayIndex)
	if candidate.Month() != m.displayMonth.Month() {
		return m, nil
	}
	m.cursor = candidate
	m.innerFocus = innerFocusGrid
	sel := candidate
	return m, func() tea.Msg { return MiniMonthDateSelectedMsg{Date: sel} }
}

// eventDotSuffix is the Unicode combining dot-below character. Appended to a
// day number it renders as the same number with a small dot directly beneath
// it, giving an event-density cue without changing cell width.
const eventDotSuffix = "\u0323"

// View renders a 7-column day grid with a header row showing the month.
// Cursor is highlighted; today is bolded; days with events get a subtle dot
// below the digit.
func (m MiniMonthModel) View() string {
	var b strings.Builder
	// Header: chevrons are real tab stops (Tab / click) for month navigation.
	// Each gets a filled highlight when inner focus lands on it.
	mutedStyle := lipgloss.NewStyle().Foreground(m.mutedColor)
	focusedChevronStyle := lipgloss.NewStyle().Background(m.accentColor).Foreground(m.textColor).Bold(true)
	var leftChev, rightChev string
	if m.focused && m.innerFocus == innerFocusPrev {
		leftChev = focusedChevronStyle.Render("‹")
	} else {
		leftChev = mutedStyle.Render("‹")
	}
	if m.focused && m.innerFocus == innerFocusNext {
		rightChev = focusedChevronStyle.Render("›")
	} else {
		rightChev = mutedStyle.Render("›")
	}
	name := m.displayMonth.Format("January 2006")
	headerName := lipgloss.NewStyle().Bold(true).Render(name)
	// Pad between the month name (left) and the two chevrons (right) so the
	// chevrons align with the right edge of the day grid.
	padding := max(miniMonthHeaderWidth-lipgloss.Width(name)-3, 1)
	b.WriteString(headerName)
	b.WriteString(strings.Repeat(" ", padding))
	b.WriteString(leftChev)
	b.WriteString(" ")
	b.WriteString(rightChev)
	b.WriteString("\n")
	// Weekday row dimmed so the day numbers carry the hierarchy.
	b.WriteString(mutedStyle.Render("Su Mo Tu We Th Fr Sa"))
	b.WriteString("\n")

	first := m.displayMonth
	// Pad to align first-of-month under its weekday column.
	leading := int(first.Weekday())
	for range leading {
		b.WriteString("   ")
	}

	today := time.Now()
	cursorDay := m.cursor.Format("2006-01-02")
	todayKey := today.Format("2006-01-02")

	cur := first
	col := leading
	rows := 0
	for cur.Month() == first.Month() {
		key := cur.Format("2006-01-02")
		num := fmt.Sprintf("%2d", cur.Day())
		// Combining dot below attaches to the last rune of the number without
		// taking a display column, so the grid geometry is preserved.
		if m.eventDays[key] {
			num += eventDotSuffix
		}
		cell := num
		isCursor := key == cursorDay
		isToday := key == todayKey
		// Treat the grid as "focused" only when widget focus is on the grid
		// sub-widget; otherwise show the cursor in the unfocused style so the
		// active tab stop (a chevron) is the only filled highlight on screen.
		gridFocused := m.focused && m.innerFocus == innerFocusGrid
		switch {
		case isCursor && gridFocused:
			cell = lipgloss.NewStyle().Background(m.accentColor).Foreground(m.textColor).Bold(true).Render(num)
		case isCursor:
			// Unfocused cursor: underline + bold so selection is still visible.
			cell = lipgloss.NewStyle().Foreground(m.textColor).Bold(true).Underline(true).Render(num)
		case isToday:
			cell = lipgloss.NewStyle().Foreground(m.todayColor).Bold(true).Render(num)
		}
		b.WriteString(cell)
		col++
		if col == 7 {
			b.WriteString("\n")
			col = 0
			rows++
		} else {
			b.WriteString(" ")
		}
		cur = cur.AddDate(0, 0, 1)
	}
	if col != 0 {
		// Pad the trailing partial row so its width matches full rows; this
		// keeps widget width stable and avoids relying on terminal whitespace
		// normalization.
		for ; col < 7; col++ {
			b.WriteString("   ")
		}
		b.WriteString("\n")
		rows++
	}
	// Always emit miniMonthGridRows grid rows so the sidebar list below never
	// shifts when the month changes (Feb non-leap = 4 rows, most months = 5,
	// some = 6).
	for ; rows < miniMonthGridRows; rows++ {
		b.WriteString(strings.Repeat("   ", 7))
		b.WriteString("\n")
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Shared calendar helpers
// ---------------------------------------------------------------------------

// addMonthClamped shifts t by months, clamping the day so it stays valid.
func addMonthClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	newMonth := time.Month(int(m) + months)
	maxDay := time.Date(y, newMonth+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > maxDay {
		d = maxDay
	}
	return time.Date(y, newMonth, d, 0, 0, 0, 0, t.Location())
}

// renderMiniCalendar draws a compact month grid.
func renderMiniCalendar(selected, today time.Time, indent int, theme Theme) string {
	y, mo, _ := selected.Date()
	loc := selected.Location()

	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday())
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	pad := strings.Repeat(" ", indent)
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, pad+faint.Render("Su Mo Tu We Th Fr Sa"))

	dayNum := 1
	for week := range 6 {
		var cells []string
		for dow := range 7 {
			pos := week*7 + dow
			if pos < startDow || dayNum > daysInMonth {
				cells = append(cells, "  ")
			} else {
				cell := fmt.Sprintf("%2d", dayNum)
				d := time.Date(y, mo, dayNum, 0, 0, 0, 0, loc)
				if sameDay(d, selected) {
					cell = lipgloss.NewStyle().Reverse(true).Bold(true).Render(cell)
				} else if sameDay(d, today) {
					cell = lipgloss.NewStyle().Foreground(theme.Today).Bold(true).Render(cell)
				}
				cells = append(cells, cell)
				dayNum++
			}
		}
		lines = append(lines, pad+strings.Join(cells, " "))
		if dayNum > daysInMonth {
			for week++; week < 6; week++ {
				lines = append(lines, pad+strings.Repeat(" ", 20))
			}
			break
		}
	}

	return strings.Join(lines, "\n")
}

// sameDay reports whether a and b represent the same calendar date.
func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
