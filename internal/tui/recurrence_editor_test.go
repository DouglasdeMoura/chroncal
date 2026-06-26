package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecurrenceEditor_UsesSharedFormFields(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})

	eachIdx, onIdx, endsIdx := -1, -1, -1
	for i, item := range m.form.items {
		switch item.Label {
		case "Repeat every":
			eachIdx = i
		case "On":
			onIdx = i
		case "Ends":
			endsIdx = i
		}
	}

	require.NotEqual(t, -1, eachIdx)
	require.NotEqual(t, -1, onIdx)
	require.NotEqual(t, -1, endsIdx)
	assert.IsType(t, &QuantitySelectField{}, m.form.Field(eachIdx))
	assert.IsType(t, &RecurrenceOnField{}, m.form.Field(onIdx))
	assert.IsType(t, &SelectField{}, m.form.Field(endsIdx))
}

func TestRecurrenceEditor_DefaultBuildRule(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})

	assert.Equal(t, "FREQ=WEEKLY;BYDAY=FR", m.BuildRule())
}

func TestRecurrenceEditor_EndsAfterIncludesCount(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.endsField.SetSelected(1)
	m.endsCountField.SetValue("10")
	m.syncFromForm()

	assert.Equal(t, "FREQ=WEEKLY;BYDAY=FR;COUNT=10", m.BuildRule())
}

func TestRecurrenceEditor_EndsAfterBlocksEmptyAndZeroCount(t *testing.T) {
	for _, count := range []string{"", "0"} {
		m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
		m.endsField.SetSelected(int(endsAfter))
		m.syncFromForm() // rebuild items so the count field is present
		m.endsCountField.SetValue(count)

		_, valid := m.form.validate()
		assert.Falsef(t, valid, "Ends=After with count %q must block submit", count)
	}
}

func TestRecurrenceEditor_MonthlyOnFieldUpdatesRule(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetSelected(2)
	m.syncFromForm()
	m.onField.SetMonthly(m.startDate, 1)

	assert.Equal(t, "FREQ=MONTHLY;BYDAY=4FR", m.BuildRule())
}

func TestRecurrenceEditor_MonthlyOnFieldArrowClickAdvancesSelection(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetSelected(2) // Monthly
	m.syncFromForm()

	onIdx := -1
	for i, item := range m.form.items {
		if item.Label == "On" {
			onIdx = i
			break
		}
	}
	require.NotEqual(t, -1, onIdx)

	// Focus the On field so its nested monthly select is focused, matching
	// the state when the user clicks its ◀ / ▶ arrows.
	m.form, _ = m.form.focusIndex(onIdx)
	require.Equal(t, RecurrenceOnMonthly, m.onField.Mode())
	require.Equal(t, 0, m.onField.MonthlyMode())

	// Clicking the ▶ arrow must advance the selection (day N → Nth-weekday).
	m.form, _ = m.form.handleClick("select:next")
	assert.Equal(t, 1, m.onField.MonthlyMode())

	// Clicking the ◀ arrow must move it back.
	m.form, _ = m.form.handleClick("select:prev")
	assert.Equal(t, 0, m.onField.MonthlyMode())
}

func TestRecurrenceEditor_EachFieldIntervalAndUnitUpdateRule(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetAmount("2")
	m.eachField.SetSelected(3)
	m.syncFromForm()

	assert.Equal(t, "FREQ=YEARLY;INTERVAL=2", m.BuildRule())
}

// TestRecurrenceEditor_EndsDatePickerButtonRowMouseClickable is the TDD
// regression test for issue #344: clicking Cancel or Ok in the ends-date
// picker overlay had no effect because HandleEndsDateMouse returned early
// for any click with ry >= 6 (the button row is at ry == 8).
func TestRecurrenceEditor_EndsDatePickerButtonRowMouseClickable(t *testing.T) {
	startDate := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	const screenW, screenH = 120, 40

	newPickerModel := func() RecurrenceEditorModel {
		m := NewRecurrenceEditorModel(startDate, screenW, screenH, Theme{})
		m.endsField.SetSelected(int(endsOnDate))
		m.syncFromForm()
		m.endsDatePicker = true
		return m
	}

	pickerBoxW, _ := newPickerModel().EndsDatePickerBoxSize() // 40
	ox := (screenW - pickerBoxW) / 2                          // 40
	oy := (screenH - 14) / 2                                  // 13
	gridY := oy + 4                                           // 17

	// Button row is 8 lines below the start of the grid
	// (6 calendar rows + 1 blank line + 1 separator line).
	const buttonRowRY = 8
	btnY := gridY + buttonRowRY // 25

	// Compute button x positions to match EndsDatePickerView rendering.
	innerW := pickerBoxW - 4 // 36, matching EndsDatePickerView's boxW-4
	bs := DefaultButtonStyles()
	cancelW := lipgloss.Width(bs.Normal.Render("Cancel", false))
	okW := lipgloss.Width(bs.Normal.Render("Ok", false))
	totalW := cancelW + 1 + okW
	btnPad := max(innerW-totalW, 0)
	contentX := ox + 2 // absolute x of content area left edge

	cancelX := contentX + btnPad           // leftmost pixel of Cancel button
	okX := contentX + btnPad + cancelW + 1 // leftmost pixel of Ok button

	t.Run("Cancel_closes_picker", func(t *testing.T) {
		m := newPickerModel()
		m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{
			X: cancelX, Y: btnY, Button: tea.MouseLeft,
		}))
		assert.False(t, m.endsDatePicker, "clicking Cancel must close the ends-date picker")
	})

	t.Run("Ok_closes_picker", func(t *testing.T) {
		m := newPickerModel()
		m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{
			X: okX, Y: btnY, Button: tea.MouseLeft,
		}))
		assert.False(t, m.endsDatePicker, "clicking Ok must close the ends-date picker")
	})

	t.Run("separator_row_does_not_close_picker", func(t *testing.T) {
		m := newPickerModel()
		separatorY := gridY + 7 // ry == 7, separator row
		m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{
			X: cancelX, Y: separatorY, Button: tea.MouseLeft,
		}))
		assert.True(t, m.endsDatePicker, "clicking separator row must not close the picker")
	})
}

func TestRecurrenceEditor_EnterOnEndsOnDateOpensPicker(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.endsField.SetSelected(2)
	m.syncFromForm()

	endsIdx := -1
	for i, item := range m.form.items {
		if item.Label == "Ends" {
			endsIdx = i
			break
		}
	}
	require.NotEqual(t, -1, endsIdx)
	m.form.focused = endsIdx

	m, _ = m.Update(keyPressMsg("enter"))

	assert.True(t, m.endsDatePicker)
}

func TestRecurrenceEditor_ClickOnWeekdayTogglesThroughViewMouseZones(t *testing.T) {
	saved := *defaultMouseTracker
	defer func() { *defaultMouseTracker = saved }()
	*defaultMouseTracker = mouseTracker{}

	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	_ = m.View()

	var zone mouseZone
	found := false
	for _, z := range defaultMouseTracker.zones {
		if z.name == "recurrenceon:1" {
			zone = z
			found = true
			break
		}
	}
	require.True(t, found, "expected weekday zone to be registered after view render")

	bw, bh := m.BoxSize()
	ox := (m.width - bw) / 2
	oy := (m.height - bh) / 2
	m, _ = m.Update(tea.MouseClickMsg(tea.Mouse{X: ox + zone.startX, Y: oy + zone.startY, Button: tea.MouseLeft}))

	assert.True(t, m.onField.WeekDays()[1])
	assert.Equal(t, 1, m.onField.WeekDayCursor())
}

func TestRecurrenceOnField_FocusedViewShowsRightAlignedHelp(t *testing.T) {
	f := NewRecurrenceOnField(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC))
	f.SetWidth(28)

	view := f.View()
	assert.NotContains(t, view, "space")

	f.Focus()
	view = f.View()
	assert.Contains(t, view, "space")
}

func TestRecurrenceOnField_FocusedDayDoesNotUseFaintText(t *testing.T) {
	f := NewRecurrenceOnField(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC))
	var weekDays [7]bool
	f.SetWeekly(weekDays, 1)
	f.Focus()

	view := f.View()

	assert.Contains(t, view, "Mo")
	assert.NotContains(t, view, "[2;7mMo")
	assert.False(t, strings.Contains(view, "\x1b[2;7mMo"), "focused day should invert colors without faint text")
}

func TestQuantitySelectField_DefaultsToOneWeek(t *testing.T) {
	f := NewQuantitySelectField([]SelectOption{
		{Label: "Day", Value: "DAILY"},
		{Label: "Week", Value: "WEEKLY"},
		{Label: "Month", Value: "MONTHLY"},
		{Label: "Year", Value: "YEARLY"},
	}, 1)

	assert.Equal(t, "1", f.Amount())
	assert.Equal(t, "WEEKLY", f.Value())
}

func TestQuantitySelectField_PluralizesUnitByAmount(t *testing.T) {
	f := NewQuantitySelectField([]SelectOption{
		{Label: "Day", PluralLabel: "Days", Value: "DAILY"},
		{Label: "Week", PluralLabel: "Weeks", Value: "WEEKLY"},
	}, 1) // default Week

	f.SetAmount("1")
	one := mouseSweep(f.View())
	assert.Contains(t, one, "Week")
	assert.NotContains(t, one, "Weeks")

	f.SetAmount("2")
	many := mouseSweep(f.View())
	assert.Contains(t, many, "Weeks")
}

func TestRecurrenceEditor_RepeatEveryPluralizesUnit(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetSelected(0) // Daily

	m.eachField.SetAmount("1")
	one := mouseSweep(m.eachField.View())
	assert.Contains(t, one, "Day")
	assert.NotContains(t, one, "Days")

	m.eachField.SetAmount("2")
	many := mouseSweep(m.eachField.View())
	assert.Contains(t, many, "Days")
}

func TestQuantitySelectField_ValidateRequiresPositiveInteger(t *testing.T) {
	f := NewQuantitySelectField([]SelectOption{
		{Label: "Day", Value: "DAILY"},
		{Label: "Week", Value: "WEEKLY"},
	}, 1)

	f.SetAmount("0")
	assert.Equal(t, "Value must be greater than 0", f.Validate())

	f.SetAmount("abc")
	assert.Equal(t, "Value must be a whole number", f.Validate())
}

func TestQuantitySelectField_UnitStaysInSameColumnAcrossFocusStates(t *testing.T) {
	f := NewQuantitySelectField([]SelectOption{
		{Label: "Day", Value: "DAILY"},
		{Label: "Week", Value: "WEEKLY"},
		{Label: "Month", Value: "MONTHLY"},
		{Label: "Year", Value: "YEARLY"},
	}, 1)

	f.Focus()
	amountFocused := mouseSweep(f.View())

	_, _ = f.SubFocusNext()
	unitFocused := mouseSweep(f.View())

	amountParts := strings.SplitN(amountFocused, "Week", 2)
	unitParts := strings.SplitN(unitFocused, "Week", 2)
	require.Len(t, amountParts, 2)
	require.Len(t, unitParts, 2)
	assert.Equal(t, lipgloss.Width(amountParts[0]), lipgloss.Width(unitParts[0]))
}

func TestTextField_FocusedViewShowsSuffix(t *testing.T) {
	f := NewTextField("10")
	f.SetSuffix("times")
	f.SetValue("10")
	f.SetWidth(12)
	f.Focus()

	view := mouseSweep(f.View())

	assert.Contains(t, view, "times")
}

func TestRecurrenceEditor_EndsAfterViewShowsTimesSuffix(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.endsField.SetSelected(1)
	m.endsCountField.SetValue("10")
	m.syncFromForm()

	view := m.View()

	assert.Contains(t, view, "times")
}
