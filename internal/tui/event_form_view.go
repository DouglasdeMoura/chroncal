package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	lipgloss "charm.land/lipgloss/v2"
)

// EndsDatePickerOpen reports whether the ends-date picker overlay should be shown.
func (m EventFormModel) EndsDatePickerOpen() bool { return m.endsDatePicker }

// DatePickerOpen reports whether the date picker overlay should be shown.
func (m EventFormModel) DatePickerOpen() bool { return m.datePickerOpen }

// TimezonePickerOpen reports whether the timezone picker overlay should be shown.
func (m EventFormModel) TimezonePickerOpen() bool { return m.timezonePickerOpen }

// RRuleEditorOpen reports whether the recurrence editor overlay should be shown.
func (m EventFormModel) RRuleEditorOpen() bool { return m.rruleEditorOpen }

// AlarmEditorOpen reports whether the alarm editor overlay should be shown.
func (m EventFormModel) AlarmEditorOpen() bool { return m.alarmEditorOpen }

// TimezonePickerBoxSize returns the outer dimensions of the timezone picker dialog.
func (m EventFormModel) TimezonePickerBoxSize() (int, int) { return 50, 19 }

// TimezonePickerView renders the timezone picker as a standalone bordered dialog.
func (m EventFormModel) TimezonePickerView() string {
	boxW, boxH := m.TimezonePickerBoxSize()
	content := m.timezonePicker.View()

	innerW := boxW - 4

	sepStyle := lipgloss.NewStyle().Faint(true)
	sep := sepStyle.Render(strings.Repeat("─", innerW))

	// Action buttons right-aligned.
	bs := DefaultButtonStyles()
	focus := m.timezonePicker.BtnFocus()
	cancelBtn := bs.Normal.Render("Cancel", focus == tzFocusCancel)
	okBtn := bs.Normal.Render("Ok", focus == tzFocusOk)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	buttonRow = strings.Repeat(" ", btnPad) + buttonRow

	// Key hints: dark keys, dimmed description, centered.
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	dot := descStyle.Render(Glyphs["separator.dot"])
	hints := "↑↓" + " " + descStyle.Render("navigate") +
		dot + "tab" + " " + descStyle.Render("next") +
		dot + "esc" + " " + descStyle.Render("close")
	hintsWidth := lipgloss.Width(hints)
	hintsPad := max((innerW-hintsWidth)/2, 0)
	hints = strings.Repeat(" ", hintsPad) + hints

	content = content + "\n\n" + sep + "\n" + buttonRow + "\n" + "\n" + hints

	return lipgloss.NewStyle().
		Width(boxW).Height(boxH).Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// DatePickerBoxSize returns the outer dimensions of the date picker dialog.
// The event-date picker is taller to accommodate the Date-range checkbox
// row and Start/End status line. The ends-date picker keeps the compact
// size since it never shows range UI.
func (m EventFormModel) DatePickerBoxSize() (int, int) {
	if m.datePickerOpen {
		return 40, 17
	}
	return 40, 14
}

// datePickerButtonRowY is the Y coordinate of the Cancel/Ok row inside the
// date picker overlay's content area. Mouse handlers use it to detect
// clicks on the button row. Event-date picker: 8 cal + 3 range + 1 blank
// + 1 separator = 13. Ends-date picker: 8 + 1 + 1 = 10.
func (m EventFormModel) datePickerButtonRowY() int {
	if m.datePickerOpen {
		return 13
	}
	return 10
}

// datePickerOverlayView renders a MiniMonthModel inside a bordered dialog
// box with the calendar grid on the left and key hints stacked vertically
// on the right. When supportRange is true (event-date picker), a
// "Multi-day" checkbox row and a Start/End status line appear between
// the grid and the button row.
func (m EventFormModel) datePickerOverlayView(mm MiniMonthModel, supportRange bool) string {
	boxW, boxH := m.DatePickerBoxSize()

	calView := strings.TrimRight(mm.View(), "\n")
	calLines := strings.Split(calView, "\n")

	// Compute max display width of calendar lines for consistent padding.
	maxCalW := 0
	for _, line := range calLines {
		if w := lipgloss.Width(line); w > maxCalW {
			maxCalW = w
		}
	}

	// Vertically-stacked key hints: dark key, lighter description.
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	hintLines := []string{
		"←↓↑→" + " " + descStyle.Render("navigate"),
		"[]" + "   " + descStyle.Render("month"),
		"t" + "    " + descStyle.Render("today"),
	}
	if supportRange {
		hintLines = append(hintLines, "r"+"    "+descStyle.Render("range"))
	}

	// Bottom-align hints with calendar lines.
	hintStart := len(calLines) - len(hintLines)

	resultLines := make([]string, 0, len(calLines)+5)
	for i, line := range calLines {
		w := lipgloss.Width(line)
		padded := line + strings.Repeat(" ", max(maxCalW-w, 0))
		if i >= hintStart && i-hintStart < len(hintLines) {
			padded += "  " + hintLines[i-hintStart]
		}
		resultLines = append(resultLines, padded)
	}

	innerW := boxW - 4

	// Range checkbox + status line (event-date picker only). Always emit
	// 3 lines (blank + checkbox + status-or-blank) so the box height stays
	// stable when the user toggles range mode.
	if supportRange {
		resultLines = append(resultLines, "")
		resultLines = append(resultLines, m.renderRangeCheckbox())
		if m.rangeMode {
			resultLines = append(resultLines, m.renderRangeStatus())
		} else {
			resultLines = append(resultLines, "")
		}
	}

	// Action buttons right-aligned at the bottom, separated by a line.
	resultLines = append(resultLines, "")
	sepStyle := lipgloss.NewStyle().Faint(true)
	resultLines = append(resultLines, sepStyle.Render(strings.Repeat("─", innerW)))
	bs := DefaultButtonStyles()
	cancelBtn := bs.Normal.Render("Cancel", m.dpBtnFocus == 0)
	okBtn := bs.Normal.Render("Ok", m.dpBtnFocus == 1)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	resultLines = append(resultLines, strings.Repeat(" ", btnPad)+buttonRow)

	content := strings.Join(resultLines, "\n")
	return lipgloss.NewStyle().
		Width(boxW).Height(boxH).Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// renderRangeCheckbox returns a single line: "[x] Multi-day" (or "[ ]").
// It is reversed when the checkbox is the focused tab stop so the user sees
// where input will land.
func (m EventFormModel) renderRangeCheckbox() string {
	mark := "[ ]"
	if m.rangeMode {
		mark = "[x]"
	}
	label := mark + " Multi-day"
	if m.dpBtnFocus == 2 {
		return lipgloss.NewStyle().Reverse(true).Bold(true).Render(label)
	}
	return label
}

// renderRangeStatus returns a faint-labelled "Start: Apr 16   End: Apr 24"
// line that summarises what the user has pinned so far. The endpoint currently
// picked is emphasised. The other is shown as "—" when unpinned.
func (m EventFormModel) renderRangeStatus() string {
	labelStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	valueStyle := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(m.theme.TextDim)

	fmtPin := func(t time.Time) string {
		if t.IsZero() {
			return dim.Render("—")
		}
		return valueStyle.Render(t.Format("Jan 2"))
	}

	startCell := fmtPin(m.rangeStart)
	endCell := fmtPin(m.rangeEnd)

	// Highlight the endpoint currently being picked so the flow is legible.
	activeMark := lipgloss.NewStyle().Foreground(m.theme.Selected).Bold(true).Render("●")
	startMark := " "
	endMark := " "
	if m.rangePickEnd {
		endMark = activeMark
	} else if m.rangeMode {
		startMark = activeMark
	}

	return labelStyle.Render("Start:") + " " + startCell + startMark +
		"   " + labelStyle.Render("End:") + " " + endCell + endMark
}

// DatePickerView renders the date picker as a standalone bordered dialog.
func (m EventFormModel) DatePickerView() string {
	return m.datePickerOverlayView(m.datePicker, true)
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m EventFormModel) EndsDatePickerView() string {
	return m.datePickerOverlayView(m.endsDatePickerModel, false)
}

// View renders the event form dialog.
func (m EventFormModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		m.keys.Save,
		m.keys.Close,
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	m.syncBodyViewport(false)
	cw := m.dialog.ContentWidth()
	body := strings.Join([]string{
		m.body.View(),
		m.actionsSeparator(cw),
		m.form.ButtonRowView(),
	}, "\n")
	content := mouseSweep(m.dialog.Box(body))
	return content
}
