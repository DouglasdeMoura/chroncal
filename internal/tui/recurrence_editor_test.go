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

func TestRecurrenceEditor_MonthlyOnFieldUpdatesRule(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetSelected(2)
	m.syncFromForm()
	m.onField.SetMonthly(m.startDate, 1)

	assert.Equal(t, "FREQ=MONTHLY;BYDAY=4FR", m.BuildRule())
}

func TestRecurrenceEditor_EachFieldIntervalAndUnitUpdateRule(t *testing.T) {
	m := NewRecurrenceEditorModel(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC), 120, 40, Theme{})
	m.eachField.SetAmount("2")
	m.eachField.SetSelected(3)
	m.syncFromForm()

	assert.Equal(t, "FREQ=YEARLY;INTERVAL=2", m.BuildRule())
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
	assert.NotContains(t, view, "click")

	f.Focus()
	view = f.View()
	assert.Contains(t, view, "click")
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
