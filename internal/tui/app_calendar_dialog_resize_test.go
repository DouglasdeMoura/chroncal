package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestWindowSizeResizesCalendarDialog reproduces issue #310: a WindowSizeMsg
// resizes every overlay except the calendar create/edit dialog. With the
// calendar dialog open, resizing the terminal must propagate the new
// dimensions to the manager calendar form so it stops rendering at the old size.
func TestWindowSizeResizesCalendarDialog(t *testing.T) {
	m := Model{width: 80, height: 24}
	openCalendarManagerForTest(&m, CalendarDialogParams{})

	next, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	got := next.(Model)

	if w := got.calendarManager.calendarForm.dialog.width; w != 120 {
		t.Fatalf("calendar dialog width not resized: got %d, want 120 (issue #310)", w)
	}
	if h := got.calendarManager.calendarForm.dialog.height; h != 40 {
		t.Fatalf("calendar dialog height not resized: got %d, want 40 (issue #310)", h)
	}
}
