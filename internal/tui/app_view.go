package tui

import (
	"image"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/douglasdemoura/chroncal/internal/config"
)

const sidebarWidth = 24

// footerHeight returns the total rows the footer occupies. The footer is
// always a single line: the "? help" hint (and optional sync status).
func (m Model) footerHeight() int {
	return 1
}

func (m Model) mainDims() (int, int) {
	padding := 1
	contentHeight := m.height - m.footerHeight()
	mainWidth := m.width - padding*2
	if m.showSidebar {
		mainWidth -= sidebarWidth
	}
	return mainWidth, contentHeight
}

func (m Model) calendarOffset() (int, int) {
	padding := 1
	x := padding
	if m.showSidebar {
		x += sidebarWidth
	}
	return x, padding
}

// innerDims returns the space available inside the padded main box,
// which is what the calendar renderer should fill.
func (m Model) innerDims() (int, int) {
	mw, mh := m.mainDims()
	padding := 1
	return mw - padding*2, mh - padding*2
}

// currentFooterContext maps the app's focus/view state to a FooterContext,
// the input the pure-render FooterModel wants.
func (m Model) currentFooterContext() FooterContext {
	switch {
	case m.calendarManagerOpen:
		return FooterCalendarPopup
	case m.viewDialogOpen:
		return FooterEventPopup
	case m.focus == focusSidebar:
		return FooterSidebar
	}
	switch m.viewMode {
	case viewAgenda:
		if _, ok := m.agenda.SelectedEvent(); ok {
			return FooterAgenda
		}
		return FooterAgendaEmpty
	case viewWeek:
		return FooterWeek
	case viewDay:
		return FooterDay
	default:
		return FooterMonth
	}
}

// currentFooterHasRSVP reports whether the event-popup footer should advertise
// RSVP keys. Only meaningful when the event view dialog is open.
func (m Model) currentFooterHasRSVP() bool {
	if !m.viewDialogOpen {
		return false
	}
	// The event view dialog exposes RSVP only when the user is an invited
	// attendee; defer to its own rsvpActions helper via the dialog model.
	return len(m.viewDialog.rsvpActions()) > 0
}

// currentFooterShowsTodayHint reports whether the active view's selected day
// differs from today, making the `t today` footer hint actionable.
func (m Model) currentFooterShowsTodayHint() bool {
	switch m.currentFooterContext() {
	case FooterMonth, FooterWeek, FooterDay, FooterAgendaEmpty:
		cursor, today := m.viewCursorAndToday()
		return !sameDay(cursor, today)
	case FooterAgenda:
		// Agenda navigation moves the selected row, not m.cursor. Look
		// at the selected day. If nothing is selected, use the cursor.
		// Then compare against today.
		_, today := m.viewCursorAndToday()
		return !sameDay(m.agenda.SelectedDay(), today)
	default:
		return false
	}
}

func (m Model) View() tea.View {
	v := tea.View{AltScreen: true, MouseMode: tea.MouseModeCellMotion}

	if m.width == 0 {
		return v
	}

	if !m.ready {
		v.Content = "\n  Loading..."
		return v
	}

	padding := 1
	mainWidth, contentHeight := m.mainDims()

	var mainContent string
	switch m.viewMode {
	case viewDay:
		mainContent = m.day.View()
	case viewWeek:
		mainContent = m.week.View()
	case viewAgenda:
		mainContent = m.agenda.View()
	default:
		mainContent = m.calendar.View()
	}
	if m.err != nil {
		mainContent = lipgloss.NewStyle().Foreground(m.theme.Error).Render("Error: " + m.err.Error())
	}

	main := lipgloss.NewStyle().
		Width(mainWidth).
		Height(contentHeight).
		Padding(padding).
		Foreground(m.theme.Text).
		Render(mainContent)

	var body string
	if m.showSidebar {
		// Render content and border as two siblings joined horizontally.
		// Faint (used to match the calendar grid chrome when unfocused)
		// then applies to the border glyphs only. It never dims the
		// sidebar's inner text.
		sb := m.sidebar.SetSize(sidebarWidth-padding*2, contentHeight-padding*2)
		inner := lipgloss.NewStyle().
			Width(sidebarWidth).
			Height(contentHeight).
			Padding(padding).
			Foreground(m.theme.Text).
			Render(sb.View())

		borderStyle := lipgloss.NewStyle().Faint(true)
		if m.focus == focusSidebar {
			borderStyle = lipgloss.NewStyle().Foreground(m.theme.Primary)
		}
		borderChar := borderStyle.Render("│")
		borderLines := make([]string, contentHeight)
		for i := range borderLines {
			borderLines[i] = borderChar
		}
		border := strings.Join(borderLines, "\n")

		sidebar := lipgloss.JoinHorizontal(lipgloss.Top, inner, border)
		body = lipgloss.JoinHorizontal(lipgloss.Top, sidebar, main)
	} else {
		body = main
	}

	var statusText string
	if m.syncStatus != "" {
		statusColor := m.theme.Primary
		if m.syncing {
			statusColor = m.theme.Muted
		}
		statusText = lipgloss.NewStyle().Foreground(statusColor).Render(m.syncStatus)
		if m.syncing {
			m.syncSpinner.Style = lipgloss.NewStyle().Foreground(m.theme.TextDim)
			statusText = m.syncSpinner.View() + " " + statusText
		}
	}
	innerWidth := m.width - padding*2
	var footerLine string
	if m.anyOverlayOpen() {
		// A dialog owns the bottom of the screen; don't duplicate its hints.
		// "? help" is misleading while the help dialog itself is up.
		footerLine = m.footer.RenderMinimal(innerWidth, statusText, m.toast.View(), !m.helpDialogOpen)
	} else {
		footerLine = m.footer.Render(
			m.currentFooterContext(),
			innerWidth,
			statusText,
			m.toast.View(),
			m.currentFooterHasRSVP(),
			m.currentFooterShowsTodayHint(),
		)
	}
	footer := lipgloss.NewStyle().
		PaddingLeft(padding).
		PaddingRight(padding).
		Render(footerLine)
	v.Content = lipgloss.JoinVertical(lipgloss.Left, body, footer)
	// Strip mouse-track markers embedded by the footer label so they do not
	// leak into terminal output. Populate the tracker's zones so clicks
	// on the label can be resolved. Overlays, if any, will overwrite zones
	// during their own View() sweeps below. That is fine. The footer
	// label is not clickable while an overlay owns input.
	v.Content = MouseSweep(v.Content)

	if m.dialogOpen {
		v.Content = m.compositeDialog(v.Content)
	}
	if m.viewDialogOpen {
		bw, bh := m.viewDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.viewDialog.View(), bw, bh)
	}
	if m.formOpen {
		bw, bh := m.form.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.form.View(), bw, bh)
		if m.form.DatePickerOpen() {
			pw, ph := m.form.DatePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.DatePickerView(), pw, ph)
		}
		if m.form.EndsDatePickerOpen() {
			pw, ph := m.form.DatePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.EndsDatePickerView(), pw, ph)
		}
		if m.form.TimezonePickerOpen() {
			tw, th := m.form.TimezonePickerBoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.TimezonePickerView(), tw, th)
		}
		if m.form.RRuleEditorOpen() {
			ew, eh := m.form.rruleEditor.BoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.rruleEditor.View(), ew, eh)
			if m.form.rruleEditor.EndsDatePickerOpen() {
				pw, ph := m.form.rruleEditor.EndsDatePickerBoxSize()
				v.Content = m.compositeOverlay(v.Content, m.form.rruleEditor.EndsDatePickerView(), pw, ph)
			}
		}
		if m.form.AlarmEditorOpen() {
			ew, eh := m.form.alarmEditor.BoxSize()
			v.Content = m.compositeOverlay(v.Content, m.form.alarmEditor.View(), ew, eh)
		}
	}
	if m.calendarManagerOpen {
		bw, bh := m.calendarManager.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.calendarManager.View(), bw, bh)
	}
	if m.trashOpen {
		bw, bh := m.trash.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.trash.View(), bw, bh)
	}
	if m.accountRenameOpen {
		bw, bh := m.accountRename.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.accountRename.View(), bw, bh)
	}
	if m.accountOAuthConfigOpen {
		bw, bh := m.accountOAuthConfig.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.accountOAuthConfig.View(), bw, bh)
	}
	if m.accountCredentialsOpen {
		bw, bh := m.accountCredentials.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.accountCredentials.View(), bw, bh)
	}
	if m.oauthFlowOpen {
		bw, bh := m.oauthFlow.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.oauthFlow.View(), bw, bh)
	}
	if m.choiceOpen {
		bw, bh := m.choiceDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.choiceDialog.View(), bw, bh)
	}
	// Regular confirms belong in the normal stack. The quit guard must
	// sit above palette and help (which otherwise render on top). It
	// owns input whenever the armed quit action is set.
	if m.confirmOpen && m.pending.kind != pendingActionQuit {
		bw, bh := m.confirmDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.confirmDialog.View(), bw, bh)
	}
	if m.paletteOpen {
		bw, bh := m.palette.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.palette.View(), bw, bh)
	}
	if m.helpDialogOpen {
		bw, bh := m.helpDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.helpDialog.View(), bw, bh)
	}
	if m.confirmOpen && m.pending.kind == pendingActionQuit {
		bw, bh := m.confirmDialog.BoxSize()
		v.Content = m.compositeOverlay(v.Content, m.confirmDialog.View(), bw, bh)
	}

	return v
}

// compositeDialog draws the dialog box over the already-rendered main view
// using an ultraviolet screen buffer. The background content outside the
// dialog's rectangle is preserved unchanged.
func (m Model) compositeDialog(background string) string {
	if m.width <= 0 || m.height <= 0 {
		return background
	}

	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(background).Draw(buf, buf.Bounds())

	dialogView := m.dialog.View()
	if dialogView == "" {
		return buf.Render()
	}

	boxW, boxH := m.dialog.BoxSize()
	if boxW <= 0 || boxH <= 0 {
		return buf.Render()
	}
	x := (m.width - boxW) / 2
	y := (m.height - boxH) / 2
	rect := image.Rect(x, y, x+boxW, y+boxH)
	uv.NewStyledString(dialogView).Draw(buf, rect)

	return buf.Render()
}

func (m Model) compositeOverlay(background, overlay string, boxW, boxH int) string {
	if m.width <= 0 || m.height <= 0 || boxW <= 0 || boxH <= 0 || overlay == "" {
		return background
	}
	buf := uv.NewScreenBuffer(m.width, m.height)
	uv.NewStyledString(background).Draw(buf, buf.Bounds())
	x := (m.width - boxW) / 2
	y := (m.height - boxH) / 2
	rect := image.Rect(x, y, x+boxW, y+boxH)
	uv.NewStyledString(overlay).Draw(buf, rect)
	return buf.Render()
}

// viewCursorAndToday returns the cursor and today from whichever view is active.
func (m Model) viewCursorAndToday() (time.Time, time.Time) {
	switch m.viewMode {
	case viewDay:
		return m.day.cursor, m.day.today
	case viewWeek:
		return m.week.cursor, m.week.today
	case viewAgenda:
		return m.agenda.cursor, m.agenda.today
	default:
		return m.calendar.cursor, m.calendar.today
	}
}

// switchToView changes the active view mode and synchronizes cursor/today.
// Safe to call even when already in the requested mode (no-op).
func (m Model) switchToView(mode viewMode) (tea.Model, tea.Cmd) {
	if m.viewMode == mode {
		var cmd tea.Cmd
		m, cmd = m.scheduleClockTick()
		return m, cmd
	}
	if m.focus == focusSidebar {
		m.sidebar = m.sidebar.Blur()
		m.focus = focusCalendar
	}
	cursor, today := m.viewCursorAndToday()
	m.viewMode = mode
	switch mode {
	case viewMonth:
		m.calendar.cursor = cursor
		m.calendar.today = today
		if m.calendar.cursor.Year() != m.calendar.month.Year() || m.calendar.cursor.Month() != m.calendar.month.Month() {
			m.calendar.month = time.Date(cursor.Year(), cursor.Month(), 1, 0, 0, 0, 0, cursor.Location())
		}
	case viewWeek:
		m.week.cursor = cursor
		m.week.today = today
	case viewDay:
		m.day.cursor = cursor
		m.day.today = today
	case viewAgenda:
		m.agenda.cursor = cursor
		m.agenda.today = today
		m.agenda = m.agenda.ResetWindow(cursor)
		if sameDay(cursor, today) {
			m.agenda = m.agenda.SelectCurrentOrNext(time.Now())
		}
	}
	cmds := []tea.Cmd{m.switchView()}
	var cmd tea.Cmd
	m, cmd = m.scheduleClockTick()
	if cmd != nil {
		cmds = append(cmds, cmd)
	}
	return m, tea.Batch(cmds...)
}

// cycleView advances to the next view mode in the order month → week → day
// → agenda → month. It keeps cursor/today across the switch.
func (m Model) cycleView() (tea.Model, tea.Cmd) {
	var next viewMode
	switch m.viewMode {
	case viewMonth:
		next = viewWeek
	case viewWeek:
		next = viewDay
	case viewDay:
		next = viewAgenda
	default:
		next = viewMonth
	}
	return m.switchToView(next)
}

// goToToday moves the cursor in the active view to today and reloads events.
func (m Model) goToToday() (tea.Model, tea.Cmd) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	switch m.viewMode {
	case viewDay:
		m.day.cursor = today
		m.day.today = today
	case viewWeek:
		m.week.cursor = today
		m.week.today = today
	case viewAgenda:
		m.agenda.cursor = today
		m.agenda.today = today
		m.agenda = m.agenda.ResetWindow(today)
	default:
		m.calendar.cursor = today
		m.calendar.today = today
		if m.calendar.month.Year() != today.Year() || m.calendar.month.Month() != today.Month() {
			m.calendar.month = time.Date(today.Year(), today.Month(), 1, 0, 0, 0, 0, today.Location())
		}
	}
	return m, m.loadEvents()
}

// toggleWeekNumbers toggles the ISO week-number gutter in month/week views.
func (m Model) toggleWeekNumbers() (tea.Model, tea.Cmd) {
	m.showWeekNumbers = !m.showWeekNumbers
	m.calendar = m.calendar.SetShowWeekNumbers(m.showWeekNumbers)
	m.week = m.week.SetShowWeekNumbers(m.showWeekNumbers)
	m.saveUIState()
	return m, nil
}

// toggleWeekStart switches the first day of the week between Sunday and Monday.
// Month and week query ranges follow the new grid, so events reload.
func (m Model) toggleWeekStart() (tea.Model, tea.Cmd) {
	if m.weekStart == time.Monday {
		m.weekStart = time.Sunday
	} else {
		m.weekStart = time.Monday
	}
	m.calendar = m.calendar.SetWeekStart(m.weekStart)
	m.week = m.week.SetWeekStart(m.weekStart)
	m.sidebar = m.sidebar.SetWeekStart(m.weekStart)
	m.saveUIState()
	return m, m.loadEvents()
}

// toggleSidebar toggles the sidebar panel and resyncs view sizes.
func (m Model) toggleSidebar() (tea.Model, tea.Cmd) {
	m.showSidebar = !m.showSidebar
	if !m.showSidebar {
		m.focus = focusCalendar
	}
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.saveUIState()
	return m, nil
}

// openEventForm mounts an event form with the session week start and size.
func (m Model) openEventForm(form EventFormModel, cmd tea.Cmd) (Model, tea.Cmd) {
	m.form = form.SetWeekStart(m.weekStart).SetSize(m.width, m.height)
	m.formOpen = true
	return m, cmd
}

// openPalette initializes and shows the command palette.
func (m Model) openPalette() (tea.Model, tea.Cmd) {
	cmds := buildPaletteCommands(m)
	palette, cmd := NewPaletteModel(cmds, m.theme, makePaletteSearchFunc(m))
	palette = palette.SetSize(m.width, m.height)
	m.palette = palette
	m.paletteOpen = true
	return m, cmd
}

// switchView resizes all views and reloads events after a view mode change.
func (m *Model) switchView() tea.Cmd {
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.saveUIState()
	return m.loadEvents()
}

func (m Model) saveUIState() {
	var vm string
	switch m.viewMode {
	case viewWeek:
		vm = "week"
	case viewDay:
		vm = "day"
	case viewAgenda:
		vm = "agenda"
	default:
		vm = "month"
	}
	ids := make([]int64, 0, len(m.hiddenCalendars))
	for id, hidden := range m.hiddenCalendars {
		if hidden {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	_ = config.SaveUIState(config.UIState{
		ShowSidebar:         m.showSidebar,
		ViewMode:            vm,
		HiddenCalendars:     ids,
		AgendaShowEmptyDays: m.agenda.ShowEmptyDays(),
		ShowWeekNumbers:     m.showWeekNumbers,
		WeekStart:           config.FormatWeekStart(m.weekStart),
	})
}
