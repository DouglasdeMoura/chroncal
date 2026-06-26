package tui

import (
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
)

// TestHandleEndsDateMouse_HitTestOrigin is a regression test for issue #343.
//
// The mini-calendar inside the Ends-date picker is rendered left-aligned at
// content column 0 inside the bordered box, so the grid's first column sits
// at screen column ox+2 (border 1 + left-pad 1). The buggy code used
// gridX = ox+3+gridPad = ox+10 (eight columns too far right), causing clicks
// on the visible left columns to be rejected (rx < 0) and clicks on the
// right to select the wrong day.
func TestHandleEndsDateMouse_HitTestOrigin(t *testing.T) {
	const screenW, screenH = 120, 40

	// Start date: 2026-04-24. endsDate default = startDate+3 months = 2026-07-24.
	// Override endsDate to 2026-07-01 (Wednesday) for predictable layout.
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	m := NewRecurrenceEditorModel(start, screenW, screenH, Theme{})
	m.endsDate = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)

	// July 2026 starts on Wednesday (startDow=3). Layout of week row 0:
	//   Su Mo Tu We Th Fr Sa
	//            1  2  3  4
	// Week row 1 (ry=1):
	//   5  6  7  8  9 10 11
	//
	// Clicking "7" (Tuesday, dow=2, ry=1):
	//   rx = 2*3 = 6  →  x = gridX + 6
	//   ry = 1        →  y = gridY + 1
	//
	// Correct gridX = ox+2 = (120-40)/2 + 2 = 42
	// Buggy   gridX = ox+10 = 50  → rx = 48-50 = -2 → rejected
	boxW, boxH := m.EndsDatePickerBoxSize()
	ox := (screenW - boxW) / 2 // = 40
	oy := (screenH - boxH) / 2 // = 13
	gridX := ox + 2            // correct origin: border(1) + left-pad(1)
	gridY := oy + 4            // border(1) + top-pad(1) + month-header(1) + day-header(1)

	clickX := gridX + 2*3 // dow=2 (Tuesday), 3 chars per cell
	clickY := gridY + 1   // week row 1

	msg := tea.MouseClickMsg(tea.Mouse{X: clickX, Y: clickY, Button: tea.MouseLeft})
	got := m.HandleEndsDateMouse(msg, boxW, boxH)

	want := time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, want, got.endsDate,
		"clicking on July 7 should select that day; wrong gridX offset is the bug")
	assert.False(t, got.endsDatePicker,
		"picker should close after a valid day click")
}

// TestHandleEndsDateMouse_OutOfBoundsIgnored verifies that clicks clearly
// outside the grid are silently discarded without changing the selected date.
func TestHandleEndsDateMouse_OutOfBoundsIgnored(t *testing.T) {
	const screenW, screenH = 120, 40
	start := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	m := NewRecurrenceEditorModel(start, screenW, screenH, Theme{})
	m.endsDate = time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	m.endsDatePicker = true
	original := m.endsDate

	boxW, boxH := m.EndsDatePickerBoxSize()

	// Click far to the left of the box — should be ignored.
	msg := tea.MouseClickMsg(tea.Mouse{X: 0, Y: 20, Button: tea.MouseLeft})
	got := m.HandleEndsDateMouse(msg, boxW, boxH)
	assert.Equal(t, original, got.endsDate, "out-of-bounds click must not change endsDate")
	assert.True(t, got.endsDatePicker, "out-of-bounds click must not close picker")
}
