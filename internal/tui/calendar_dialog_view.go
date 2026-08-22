package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

func (m CalendarDialogModel) SetSize(w, h int) CalendarDialogModel {
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	if m.discoveryPicker != nil {
		picker := m.discoveryPicker.SetSize(w, h)
		m.discoveryPicker = &picker
		return m
	}
	m.form.SetWidth(m.dialog.ContentWidth())
	if m.contentWidth != nil {
		*m.contentWidth = m.dialog.ContentWidth()
	}
	m.syncBodyViewport(true)
	return m
}

func (m CalendarDialogModel) formViewportHeight() int {
	const chromeLines = 2 + // top + bottom border
		1 + // top padding (PaddingY)
		2 + // dialog title + blank line
		2 // blank line + help footer
	extra := 0
	if m.testStatus != "" {
		extra = 1
	}
	actionLines := 1 + max(lipgloss.Height(m.form.ButtonRowView()), 1) // separator + buttons
	return max(m.dialog.height-chromeLines-actionLines-extra, 1)
}

func (m *CalendarDialogModel) syncBodyViewport(keepFocusVisible bool) {
	cw := m.dialog.ContentWidth()
	if cw <= 0 || m.dialog.height <= 0 {
		return
	}
	bodyLines := strings.Split(m.form.BodyView(), "\n")
	m.body.SetWidth(cw)
	m.body.SetHeight(min(len(bodyLines), m.formViewportHeight()))
	m.body.SetContentLines(bodyLines)
	if keepFocusVisible {
		m.keepFocusedFieldVisible()
	}
}

func (m *CalendarDialogModel) keepFocusedFieldVisible() {
	if m.body.Height() <= 0 {
		return
	}
	line := m.form.FocusedLine()
	if line < 0 {
		// Focus is on the button row, not a body field; leave the
		// scroll position where the last field left it.
		return
	}
	if line < m.body.YOffset() {
		m.body.ScrollUp(m.body.YOffset() - line)
		return
	}
	bottom := m.body.YOffset() + m.body.Height() - 1
	if line > bottom {
		m.body.ScrollDown(line - bottom)
	}
}

// SetInspectorSize prepares the stored form for a borderless render inside
// the Calendars manager. Render stays pure. Body content and viewport
// dimensions are refreshed here and after Update, never from InspectorView.
// Blur returns a copy whose form holds no keyboard focus. The manager can
// then render it as the root selection preview while the source list owns
// input.
func (m CalendarDialogModel) Blur() CalendarDialogModel {
	m.form = m.form.Blur()
	return m
}

func (m CalendarDialogModel) SetInspectorSize(w, h int) CalendarDialogModel {
	w = max(w, 1)
	h = max(h, 1)
	m.form.SetWidth(w)
	if m.contentWidth != nil {
		*m.contentWidth = w
	}
	bodyLines := strings.Split(m.form.BodyView(), "\n")
	statusLines := 0
	if m.testStatus != "" {
		statusLines = 1
	}
	buttonLines := max(lipgloss.Height(m.form.ButtonRowView()), 1)
	bodyHeight := max(h-2-statusLines-1-buttonLines, 1)
	m.body.SetWidth(w)
	m.body.SetHeight(min(len(bodyLines), bodyHeight))
	m.body.SetContentLines(bodyLines)
	m.keepFocusedFieldVisible()
	if m.discoveryPicker != nil {
		picker := m.discoveryPicker.SetSize(w, h)
		m.discoveryPicker = &picker
	}
	return m
}

// InspectorView renders the calendar/add-account form without another dialog
// border so the manager's grouped hierarchy remains mounted beside it.
func (m CalendarDialogModel) InspectorView(w, h int) string {
	if m.discoveryPicker != nil {
		return padLines(strings.Split(m.discoveryPicker.View(), "\n"), w, h)
	}
	parts := []string{lipgloss.NewStyle().Bold(true).Render(truncateTo(m.dialog.title, w)), m.body.View()}
	if m.testStatus != "" {
		parts = append(parts, truncateTo(m.testStatus, w))
	}
	parts = append(parts, m.actionsSeparator(w), m.form.ButtonRowView())
	return padLines(strings.Split(strings.Join(parts, "\n"), "\n"), w, h)
}

// HelpBindings returns the footer help this pushed detail owns while it is
// the manager's active screen. An embedded discovery picker advertises its
// own compact set, otherwise the form's field keys. The manager footer
// defers here so a rebound key no longer drifts between the child and the
// footer. These are display-only. Input route is unchanged.
func (m CalendarDialogModel) HelpBindings() []key.Binding {
	if m.discoveryPicker != nil {
		return m.discoveryPicker.HelpBindings()
	}
	return []key.Binding{
		footerBinding("tab", "next field"),
		footerBinding("enter", "confirm"),
		footerBinding("esc", "back"),
	}
}

func (m CalendarDialogModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

func (m CalendarDialogModel) scrollHint() string {
	if !m.bodyOverflows() {
		return ""
	}
	switch {
	case m.body.AtTop():
		return "↓ more"
	case m.body.AtBottom():
		return "↑ more"
	default:
		return "↑↓ more"
	}
}

func (m CalendarDialogModel) actionsSeparator(w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	hint := m.scrollHint()
	hw := lipgloss.Width(hint)
	if hint == "" || w <= hw+2 {
		return faint.Render(strings.Repeat("─", w))
	}
	left := (w - hw - 2) / 2
	right := w - hw - 2 - left
	return faint.Render(strings.Repeat("─", left)) + " " + faint.Render(hint) + " " + faint.Render(strings.Repeat("─", right))
}

func (m CalendarDialogModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m CalendarDialogModel) View() string {
	if m.discoveryPicker != nil {
		return m.discoveryPicker.View()
	}
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	m.syncBodyViewport(false)
	cw := m.dialog.ContentWidth()
	parts := []string{m.body.View()}
	if m.testStatus != "" {
		parts = append(parts, truncateTo(m.testStatus, cw))
	}
	parts = append(parts, m.actionsSeparator(cw), m.form.ButtonRowView())
	body := strings.Join(parts, "\n")
	content := mouseSweep(m.dialog.Box(body))
	return content
}
