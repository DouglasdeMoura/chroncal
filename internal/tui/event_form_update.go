package tui

import (
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// Update handles messages for the event form.
func (m EventFormModel) Update(msg tea.Msg) (EventFormModel, tea.Cmd) {
	// Window resize
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	// Form OnSubmit emits this after validation. Run save against the live
	// model here so we don't read stale state from the OnSubmit closure's
	// captured receiver.
	if _, ok := msg.(eventFormSubmitNowMsg); ok {
		return m, m.save(&m.form)
	}

	// Overlays capture all input when open.
	if m.rruleEditorOpen {
		return m.updateRRuleEditor(msg)
	}
	if m.alarmEditorOpen {
		return m.updateAlarmEditor(msg)
	}
	if m.timezonePickerOpen {
		return m.updateTimezonePicker(msg)
	}
	if m.datePickerOpen {
		return m.updateDatePicker(msg)
	}
	if m.endsDatePicker {
		return m.updateEndsDatePicker(msg)
	}

	if kp, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(kp, m.keys.Save):
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		case key.Matches(kp, m.keys.Close):
			return m, func() tea.Msg { return EventFormClosedMsg{} }
		}

		// Handle overlay-opening Enter presses directly (not via the
		// form's onFieldEnter callback) so mutations survive the
		// value-receiver copy.
		if kp.String() == "enter" {
			if cmd := m.tryOpenOverlay(); cmd != nil {
				return m, cmd
			}
		}
	}

	// Forward mouse clicks through mouse tracker.
	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			// Translate screen coordinates to dialog-local coordinates.
			bw, bh := m.BoxSize()
			ox := (m.width - bw) / 2
			oy := (m.height - bh) / 2
			target := mouseResolve(mc.X-ox, mc.Y-oy)
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			// Sync dynamic fields against the live receiver (issue #496):
			// a click that toggles All-day or changes Repeat/Ends must
			// rebuild the live form, not a stale construction-time copy.
			m.syncFromForm()
			// A click landing on the focused field's row opens its overlay,
			// mirroring the Enter path (tryOpenOverlay) so mouse and keyboard
			// behave identically for every opener field (Date, Timezone,
			// custom Repeat, on-date Ends, Alarms).
			if target == fieldTarget(m.form.Focused()) {
				if c := m.tryOpenOverlay(); c != nil {
					return m, c
				}
			}
			return m, cmd
		}
		return m, nil
	}
	if mw, ok := msg.(tea.MouseWheelMsg); ok {
		var cmd tea.Cmd
		m.syncBodyViewport(false)
		m.body, cmd = m.body.Update(mw)
		return m, cmd
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	// Rebuild dynamic fields against the live receiver (issue #496). The
	// OnRebuild closure could only mutate a stale construction-time copy, so
	// the live form never gained the Ends selector / Ends-count field and
	// the All-day toggle never re-disabled the Time field.
	m.syncFromForm()
	m.syncBodyViewport(true)
	return m, cmd
}

func (m EventFormModel) updateRRuleEditor(msg tea.Msg) (EventFormModel, tea.Cmd) {
	var cmd tea.Cmd
	m.rruleEditor, cmd = m.rruleEditor.Update(msg)
	if m.rruleEditor.Done() {
		m.customRule = m.rruleEditor.BuildRule()
		m.rruleEditorOpen = false
	} else if m.rruleEditor.Cancelled() {
		m.rruleEditorOpen = false
	}
	return m, cmd
}

func (m EventFormModel) updateAlarmEditor(msg tea.Msg) (EventFormModel, tea.Cmd) {
	var cmd tea.Cmd
	m.alarmEditor, cmd = m.alarmEditor.Update(msg)
	if m.alarmEditor.Done() {
		m.alarms = m.alarmEditor.Alarms()
		if m.alarmField != nil {
			m.alarmField.SetValue(alarmSummary(m.alarms))
		}
		m.alarmEditorOpen = false
	} else if m.alarmEditor.Cancelled() {
		m.alarmEditorOpen = false
	}
	return m, cmd
}

func (m EventFormModel) updateTimezonePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	var cmd tea.Cmd
	m.timezonePicker, cmd = m.timezonePicker.Update(msg)
	if m.timezonePicker.Done() {
		m.timezoneField.SetValue(m.timezonePicker.Selected())
		m.timezonePickerOpen = false
	} else if m.timezonePicker.Cancelled() {
		m.timezonePickerOpen = false
	}
	return m, cmd
}

func (m EventFormModel) updateDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleDatePickerMouse(mc)
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch kp.String() {
	case "esc", "q":
		m.datePickerOpen = false
		return m, nil
	case "r", "R":
		// Global shortcut: toggle range mode from anywhere in the overlay.
		m.toggleRangeMode()
		return m, nil
	case "tab":
		m, m.datePicker = m.dpAdvanceFocus(m.datePicker)
		return m, nil
	case "shift+tab":
		m, m.datePicker = m.dpRetreatFocus(m.datePicker)
		return m, nil
	case "enter", "space":
		switch {
		case m.dpBtnFocus == 2: // Range checkbox
			m.toggleRangeMode()
		case m.dpBtnFocus == 0: // Cancel
			m.datePickerOpen = false
		case m.dpBtnFocus == 1: // Ok
			m.commitDatePickerSelection()
		case !m.datePicker.AtEnd(): // Chevron focused: let MiniMonth handle it
			m.datePicker, _ = m.datePicker.Update(kp)
		case m.rangeMode: // Grid + range mode: pin an endpoint
			m.pinRangeEndpoint(m.datePicker.Cursor())
		default: // Grid focused: confirm single date
			m.commitDatePickerSelection()
		}
		return m, nil
	}
	// Forward navigation keys only when calendar is focused.
	if m.dpBtnFocus == -1 {
		m.datePicker, _ = m.datePicker.Update(kp)
	}
	return m, nil
}

// toggleRangeMode flips range-mode on/off inside the date picker. On
// auto-pins the current cursor as the range start so the user sees
// immediate feedback. Off clears the end pin and keeps start as
// the plain single-date selection.
func (m *EventFormModel) toggleRangeMode() {
	m.rangeMode = !m.rangeMode
	if m.rangeMode {
		m.rangeStart = m.datePicker.Cursor()
		m.rangeEnd = time.Time{}
		m.rangePickEnd = true
		m.datePicker = m.datePicker.SetRange(true, m.rangeStart, m.rangeEnd)
	} else {
		m.rangeEnd = time.Time{}
		m.rangePickEnd = false
		m.datePicker = m.datePicker.SetRange(false, time.Time{}, time.Time{})
	}
}

// pinRangeEndpoint commits the current cursor position as either the start
// or the end of the range, based on which endpoint is picked.
// After a pin of end, the next Enter on a day re-pins start (reset cycle).
func (m *EventFormModel) pinRangeEndpoint(d time.Time) {
	if m.rangePickEnd {
		m.rangeEnd = d
		m.rangePickEnd = false
	} else {
		m.rangeStart = d
		m.rangeEnd = time.Time{}
		m.rangePickEnd = true
	}
	m.datePicker = m.datePicker.SetRange(true, m.rangeStart, m.rangeEnd)
}

// commitDatePickerSelection closes the overlay. It writes the current cursor
// (or range) back to the form. In range mode with both endpoints pinned,
// the earlier endpoint becomes the event date. The later endpoint is
// stored as rangeEndDate for the save path.
func (m *EventFormModel) commitDatePickerSelection() {
	if m.rangeMode && !m.rangeStart.IsZero() && !m.rangeEnd.IsZero() {
		lo, hi := m.rangeStart, m.rangeEnd
		if hi.Before(lo) {
			lo, hi = hi, lo
		}
		m.day = lo
		m.rangeEndDate = hi
		m.rangeHasEnd = !sameDay(lo, hi)
		m.dateField.SetDate(m.day)
		if m.rangeHasEnd {
			m.dateField.SetRangeEnd(m.rangeEndDate)
		} else {
			m.dateField.ClearRangeEnd()
		}
	} else {
		m.day = m.datePicker.Cursor()
		m.rangeHasEnd = false
		m.rangeEndDate = time.Time{}
		m.dateField.SetDate(m.day)
		m.dateField.ClearRangeEnd()
	}
	m.datePickerOpen = false
}

func (m EventFormModel) updateEndsDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleEndsDatePickerMouse(mc)
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch kp.String() {
	case "esc", "q":
		m.endsDatePicker = false
		return m, nil
	case "tab":
		m, m.endsDatePickerModel = m.dpAdvanceFocus(m.endsDatePickerModel)
		return m, nil
	case "shift+tab":
		m, m.endsDatePickerModel = m.dpRetreatFocus(m.endsDatePickerModel)
		return m, nil
	case "enter", "space":
		switch {
		case m.dpBtnFocus == 0: // Cancel
			m.endsDatePicker = false
		case m.dpBtnFocus == 1: // Ok
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		case !m.endsDatePickerModel.AtEnd(): // Chevron focused
			m.endsDatePickerModel, _ = m.endsDatePickerModel.Update(kp)
		default: // Grid focused
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		}
		return m, nil
	}
	if m.dpBtnFocus == -1 {
		m.endsDatePickerModel, _ = m.endsDatePickerModel.Update(kp)
	}
	return m, nil
}

// dpAdvanceFocus moves focus forward through the event-date picker's tab
// stops: ‹ → › → grid → [range checkbox] → Cancel → Ok → ‹.
// The range checkbox stop is skipped when mm is the ends-date picker.
// The caller tells event-date from ends-date.
func (m EventFormModel) dpAdvanceFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	// Only the event-date picker exposes the range checkbox stop.
	hasRange := m.datePickerOpen
	if m.dpBtnFocus >= 0 {
		switch {
		case hasRange && m.dpBtnFocus == 2: // checkbox → Cancel
			m.dpBtnFocus = 0
		case m.dpBtnFocus == 0: // Cancel → Ok
			m.dpBtnFocus = 1
		default: // Ok → ‹
			m.dpBtnFocus = -1
			mm = mm.Focus().FocusFirst()
		}
		return m, mm
	}
	if mm.AtEnd() {
		mm = mm.Blur()
		if hasRange {
			m.dpBtnFocus = 2 // grid → range checkbox
		} else {
			m.dpBtnFocus = 0 // grid → Cancel (ends-date picker)
		}
	} else {
		mm = mm.AdvanceFocus()
	}
	return m, mm
}

// dpRetreatFocus moves focus backward: Ok → Cancel → [range checkbox] → grid → › → ‹.
func (m EventFormModel) dpRetreatFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	hasRange := m.datePickerOpen
	if m.dpBtnFocus >= 0 {
		switch {
		case m.dpBtnFocus == 1: // Ok → Cancel
			m.dpBtnFocus = 0
		case m.dpBtnFocus == 0 && hasRange: // Cancel → checkbox
			m.dpBtnFocus = 2
		default: // Cancel (no range) or checkbox → grid
			m.dpBtnFocus = -1
			mm = mm.Focus().FocusLast()
		}
		return m, mm
	}
	if mm.AtStart() {
		mm = mm.Blur()
		m.dpBtnFocus = 1
	} else {
		mm = mm.RetreatFocus()
	}
	return m, mm
}

func (m EventFormModel) handleDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2

	// Content-relative coordinates: border(1) + padding(left=1, top=1).
	mmX := msg.X - ox - 2
	mmY := msg.Y - oy - 2

	if mmX < 0 || mmY < 0 {
		return m, nil
	}

	// Checkbox row (Y=9 in event-date layout: 8 cal + 1 blank). Clicks on
	// the label itself toggle range mode.
	if mmY == 9 && mmX < len("[x] Multi-day")+2 {
		m.toggleRangeMode()
		return m, nil
	}

	if mmY == m.datePickerButtonRowY() {
		switch m.datePickerButtonHit(mmX) {
		case "cancel":
			m.datePickerOpen = false
		case "ok":
			m.commitDatePickerSelection()
		}
		return m, nil
	}

	// Calendar grid hit-testing. Day rows occupy content rows 2..7 (header
	// row 0, weekday row 1, then miniMonthGridRows day rows). Reject the X
	// overflow and any row below the grid so clicks in the dead space
	// (blank/status/separator) do not fall through and commit/pin a date.
	// Row 0 (chevrons) is intentionally kept so mouse month nav still works.
	if mmX >= miniMonthHeaderWidth || mmY >= 2+miniMonthGridRows {
		return m, nil
	}

	// Click on calendar: ensure calendar is focused, not buttons.
	m.dpBtnFocus = -1
	m.datePicker = m.datePicker.Focus()

	prevMonth := m.datePicker.DisplayMonth()
	m.datePicker, _ = m.datePicker.HandleClick(mmX, mmY)

	monthChanged := m.datePicker.DisplayMonth().Month() != prevMonth.Month() ||
		m.datePicker.DisplayMonth().Year() != prevMonth.Year()
	if !monthChanged && mmY >= 2 {
		if m.rangeMode {
			m.pinRangeEndpoint(m.datePicker.Cursor())
		} else {
			m.commitDatePickerSelection()
		}
	}

	return m, nil
}

// datePickerButtonHit returns "cancel", "ok", or "" based on x position.
func (m EventFormModel) datePickerButtonHit(x int) string {
	boxW, _ := m.DatePickerBoxSize()
	innerW := boxW - 4
	bs := DefaultButtonStyles()
	cancelW := lipgloss.Width(bs.Normal.Render("Cancel", false))
	okW := lipgloss.Width(bs.Normal.Render("Ok", false))
	totalW := cancelW + 1 + okW
	pad := max(innerW-totalW, 0)
	if x >= pad && x < pad+cancelW {
		return "cancel"
	}
	if x >= pad+cancelW+1 && x < pad+cancelW+1+okW {
		return "ok"
	}
	return ""
}

func (m EventFormModel) handleEndsDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2

	mmX := msg.X - ox - 2
	mmY := msg.Y - oy - 2

	if mmX < 0 || mmY < 0 {
		return m, nil
	}

	if mmY == m.datePickerButtonRowY() {
		switch m.datePickerButtonHit(mmX) {
		case "cancel":
			m.endsDatePicker = false
		case "ok":
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		}
		return m, nil
	}

	// Calendar grid hit-testing. Day rows occupy content rows 2..7 (header
	// row 0, weekday row 1, then miniMonthGridRows day rows). Reject the X
	// overflow and any row below the grid so clicks in the dead space
	// (blank/separator) do not fall through and commit a date.
	// Row 0 (chevrons) is intentionally kept so mouse month nav still works.
	if mmX >= miniMonthHeaderWidth || mmY >= 2+miniMonthGridRows {
		return m, nil
	}

	// Click on calendar: ensure calendar is focused, not buttons.
	m.dpBtnFocus = -1
	m.endsDatePickerModel = m.endsDatePickerModel.Focus()

	prevMonth := m.endsDatePickerModel.DisplayMonth()
	m.endsDatePickerModel, _ = m.endsDatePickerModel.HandleClick(mmX, mmY)

	monthChanged := m.endsDatePickerModel.DisplayMonth().Month() != prevMonth.Month() ||
		m.endsDatePickerModel.DisplayMonth().Year() != prevMonth.Year()
	if !monthChanged && mmY >= 2 {
		m.endsDate = m.endsDatePickerModel.Cursor()
		m.endsDatePicker = false
	}

	return m, nil
}
