package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestEventViewDialogShowsTitleWithoutSetSize renders the event view
// dialog before a SetSize with a valid content width. SetSize computes the
// wrapped title, so a model that gets its size another way showed no
// title. View must compute the title when SetSize did not.
func TestEventViewDialogShowsTitleWithoutSetSize(t *testing.T) {
	start := time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC)
	m := NewEventViewDialogModel(event.Event{
		ID: 1, Title: "Quarterly planning", StartTime: start, EndTime: start.Add(time.Hour),
	}, CalendarInfo{Name: "Work"}, Theme{})
	m.width, m.height = 120, 40
	if view := stripANSI(m.View()); !strings.Contains(view, "Quarterly planning") {
		t.Fatalf("view = %q, want the event title", view)
	}
}
