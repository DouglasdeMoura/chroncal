package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

func (m CalendarDialogModel) Update(msg tea.Msg) (CalendarDialogModel, tea.Cmd) {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	if m.discoveryPicker != nil {
		picker, cmd := m.discoveryPicker.Update(msg)
		m.discoveryPicker = &picker
		return m, cmd
	}

	if _, ok := msg.(testConnectionPressedMsg); ok {
		m, cmd := m.handleTestPressed()
		m.syncBodyViewport(true)
		return m, cmd
	}

	if _, ok := msg.(calendarSavePromotePressedMsg); ok {
		if m.saveMakeDefault != nil {
			*m.saveMakeDefault = true
		}
		var cmd tea.Cmd
		m.form, cmd = m.form.Submit()
		return m, cmd
	}

	if tr, ok := msg.(CalendarTestResultMsg); ok {
		if tr.OK {
			m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Accent).
				Render("✓ " + tr.Message)
		} else {
			m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Error).
				Render("✗ " + tr.Message)
		}
		m.syncBodyViewport(true)
		return m, nil
	}

	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(msg, key.NewBinding(key.WithKeys("esc"))):
			return m, func() tea.Msg { return CalendarDialogClosedMsg{} }
		case key.Matches(msg, key.NewBinding(key.WithKeys("ctrl+s"))):
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		}
	}

	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			bw, bh := m.BoxSize()
			ox := (m.dialog.width - bw) / 2
			oy := (m.dialog.height - bh) / 2
			target := mouseResolve(mc.X-ox, mc.Y-oy)
			// A click on the Display Calendar checkbox toggles it inside the
			// form. Compare pre/post state. The mouse path then emits the
			// same CalendarVisibilityToggledMsg as the keyboard path.
			preVisible := m.visibilityChecked()
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			m.syncBodyViewport(true)
			return m.applyVisibilityToggle(preVisible, cmd)
		}
		return m, nil
	}
	if mw, ok := msg.(tea.MouseWheelMsg); ok {
		var cmd tea.Cmd
		m.syncBodyViewport(false)
		m.body, cmd = m.body.Update(mw)
		return m, cmd
	}

	preVisible := m.visibilityChecked()
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	m.syncBodyViewport(true)
	return m.applyVisibilityToggle(preVisible, cmd)
}

// visibilityChecked reports the Display Calendar checkbox's current state, or
// true when there is no checkbox (create mode). A no-op comparison then never
// reports a spurious change.
func (m CalendarDialogModel) visibilityChecked() bool {
	if m.visibilityCb == nil {
		return true
	}
	return m.visibilityCb.Checked()
}

// applyVisibilityToggle compares the checkbox state before and after a form
// update. A change emits CalendarVisibilityToggledMsg with the DESIRED hidden
// state. It mirrors it into the local model so the dot flips with no reload.
// It batches with any cmd the form update produced. Metadata Save/Cancel
// never persists visibility.
func (m CalendarDialogModel) applyVisibilityToggle(preVisible bool, cmd tea.Cmd) (CalendarDialogModel, tea.Cmd) {
	if m.visibilityCb == nil {
		return m, cmd
	}
	postVisible := m.visibilityCb.Checked()
	if postVisible == preVisible {
		return m, cmd
	}
	m.hidden = !postVisible
	id := m.id
	toggle := func() tea.Msg { return CalendarVisibilityToggledMsg{ID: id, Hidden: !postVisible} }
	if cmd == nil {
		return m, toggle
	}
	return m, tea.Batch(cmd, toggle)
}

// handleTestPressed validates the remote fields. When they have values,
// it emits a CalendarTestRequestedMsg so the parent can run the
// authenticated ping. Errors show inline. The server is not contacted.
func (m CalendarDialogModel) handleTestPressed() (CalendarDialogModel, tea.Cmd) {
	if !m.accountConnection {
		return m, nil
	}
	// The oauth2 layout has no password to ping with — there is no token
	// until the browser flow runs, which happens on sign-in.
	if calendarAuthIsOAuth(m.form.Field(calDAVIdxAuth).(*SelectField).Value()) {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
			Render("Connection test runs after Google authorization — sign in to continue")
		return m, nil
	}
	url := strings.TrimSpace(m.form.Field(calDAVIdxServer).(*TextField).Value())
	user := strings.TrimSpace(m.form.Field(calDAVIdxUsername).(*TextField).Value())
	auth := m.form.Field(calDAVIdxAuth).(*SelectField).Value()
	pass := m.form.Field(calDAVIdxSecret).(*TextField).Value()
	ins := m.form.Field(calDAVIdxAllowInsecure).(*CheckboxField).Checked()

	if url == "" || user == "" || pass == "" {
		m.testStatus = lipgloss.NewStyle().Foreground(m.theme.Error).
			Render("✗ Fill URL, Username, and Password first")
		return m, nil
	}

	m.testStatus = lipgloss.NewStyle().Foreground(m.theme.TextDim).Italic(true).
		Render("Testing…")
	return m, func() tea.Msg {
		return CalendarTestRequestedMsg{
			URL:           url,
			Username:      user,
			AuthType:      auth,
			Password:      pass,
			AllowInsecure: ins,
		}
	}
}
