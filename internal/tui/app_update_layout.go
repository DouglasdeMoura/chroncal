package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

func (m Model) handleBackgroundColor(msg tea.BackgroundColorMsg) (tea.Model, tea.Cmd) {
	m.theme = LoadTheme(m.themeName, msg.IsDark())
	// System theme opts into a terminal-adaptive Selected color.
	// Derive it from the actual terminal background reported over
	// OSC 11 by a shift of OKLCh lightness ±8 %. That guarantees a
	// visible-but-subtle highlight on any terminal theme. The
	// static Dracula Selection hexes in system.toml are the
	// fallback when OSC 11 does not answer (e.g. tmux without
	// passthrough).
	if m.themeName == "system" && msg.Color != nil {
		delta := 0.08
		if !msg.IsDark() {
			delta = -0.08
		}
		m.theme.Selected = oklch.ShiftLightness(msg.Color, delta)
		// Secondary buttons need more weight than a list-row fill —
		// at ±0.08 the pill barely separates from the terminal bg on
		// low-contrast themes. Use ±0.18 so the button reads as a
		// tappable surface, while list-row Selected stays subtle.
		btnDelta := 0.18
		if !msg.IsDark() {
			btnDelta = -0.18
		}
		m.theme.ButtonBg = oklch.ShiftLightness(msg.Color, btnDelta)
	}
	SetActiveTheme(m.theme)
	// Month/week/day views use the selected-color as a vibrant
	// BORDER stroke around the cursor cell; Primary (the brand
	// accent) always stands out against the cell background. The
	// neutral theme.Selected is reserved for list-row FILLS
	// (trash, event list, palette, …) where a muted highlight is
	// what you want.
	m.calendar = m.calendar.SetSelectedColor(m.theme.Primary)
	m.week = m.week.SetSelectedColor(m.theme.Primary)
	m.day = m.day.SetSelectedColor(m.theme.Primary)
	m.agenda = m.agenda.SetTheme(m.theme)
	m.sidebar = m.sidebar.SetTheme(m.theme)
	m.toast.SetTheme(m.theme)
	m.oauthFlow = m.oauthFlow.SetTheme(m.theme)
	m.footer.SetTheme(m.theme)
	m.calendarManager = m.calendarManager.SetTheme(m.theme)
	if m.trashOpen {
		m.trash = m.trash.SetSelectedColor(m.theme.Selected)
	}
	return m, nil
}

func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width = msg.Width
	m.height = msg.Height
	iw, ih := m.innerDims()
	m.calendar = m.calendar.SetSize(iw, ih)
	m.week = m.week.SetSize(iw, ih)
	m.day = m.day.SetSize(iw, ih)
	m.agenda = m.agenda.SetSize(iw, ih)
	m.trash = m.trash.SetSize(m.width, m.height)
	m.dialog = m.dialog.SetSize(m.width, m.height)
	m.viewDialog = m.viewDialog.SetSize(m.width, m.height)
	m.confirmDialog = m.confirmDialog.SetSize(m.width, m.height)
	m.choiceDialog = m.choiceDialog.SetSize(m.width, m.height)
	m.calendarManager = m.calendarManager.SetSize(m.width, m.height)
	m.accountRename = m.accountRename.SetSize(m.width, m.height)
	m.accountOAuthConfig = m.accountOAuthConfig.SetSize(m.width, m.height)
	m.accountCredentials = m.accountCredentials.SetSize(m.width, m.height)
	m.form = m.form.SetSize(m.width, m.height)
	m.palette = m.palette.SetSize(m.width, m.height)
	m.helpDialog = m.helpDialog.SetSize(m.width, m.height)
	m.oauthFlow = m.oauthFlow.SetSize(m.width, m.height)
	m.ready = true
	return m, nil
}
