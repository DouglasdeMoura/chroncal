package tui

import (
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// zoneCenterByName returns the center coordinates of the most recently swept
// mouse zone with the given name, or ok=false when no such zone exists.
func zoneCenterByName(name string) (x, y int, ok bool) {
	for _, z := range defaultMouseTracker.zones {
		if z.name == name {
			return (z.startX + z.endX) / 2, (z.startY + z.endY) / 2, true
		}
	}
	return 0, 0, false
}

// TestForm_ClickDisabledTimeRangeDoesNotFocusOrEdit is a regression test for
// issue #497: a mouse click on a disabled field must behave like Tab, which
// skips non-focusable fields. Clicking a disabled (all-day) time-range field
// previously focused its inner text input and let keystrokes mutate the locked
// start/end.
func TestForm_ClickDisabledTimeRangeDoesNotFocusOrEdit(t *testing.T) {
	tr := NewTimeRangeField(lipgloss.Color("240"))
	tr.SetStartValue("09:00")
	tr.SetEndValue("10:00")
	tr.SetDisabled(true)

	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Time", Field: tr},
	)
	require.Equal(t, 0, form.Focused())

	// Click directly on the disabled time-range field's registered zone.
	formClickTarget(&form, "field:1")
	assert.Equal(t, 0, form.Focused(),
		"clicking a disabled field must not move focus to it")

	// A subsequent keystroke must not edit the locked start/end values.
	form = updateForm(form, keyPressMsg("9"))
	assert.Equal(t, "09:00", tr.StartValue(), "disabled start must stay locked")
	assert.Equal(t, "10:00", tr.EndValue(), "disabled end must stay locked")
}

// TestForm_ClickUnfocusedSelectArrowFocusesAndAdvances is a regression test for
// issue #498: clicking the prev/next arrow of an unfocused SelectField was a
// mouse dead zone. The arrow targets are now keyed by field index, so a single
// click both focuses the field and applies the keypress.
func TestForm_ClickUnfocusedSelectArrowFocusesAndAdvances(t *testing.T) {
	sel := NewSelectField([]SelectOption{
		{Label: "Alpha", Value: "a"},
		{Label: "Bravo", Value: "b"},
		{Label: "Charlie", Value: "c"},
	})

	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Choice", Field: sel},
	)
	require.Equal(t, 0, form.Focused(), "focus starts on the first field")
	require.Equal(t, 0, sel.Selected())
	require.False(t, sel.focused, "the select must be unfocused for this test")

	// Render and sweep so the arrow zones are registered, then resolve the
	// next arrow's screen position the way a real mouse click would.
	_ = mouseSweep(form.View())
	x, y, ok := zoneCenterByName("select:next:1")
	require.True(t, ok, "the next arrow must render an index-keyed mouse zone")

	target := mouseResolve(x, y)
	require.Equal(t, "select:next:1", target,
		"resolving the arrow position must yield the index-keyed target")

	form, _ = form.handleClick(target)

	assert.Equal(t, 1, form.Focused(),
		"clicking an unfocused select's arrow must focus the field")
	assert.Equal(t, 1, sel.Selected(),
		"clicking the next arrow must advance the value in the same click")
}
