package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

func TestFormatEventDetailsText_IncludesCoreFields(t *testing.T) {
	ev := event.Event{
		Title:       "Weekly sync",
		Location:    "Zoom",
		Description: "Status updates and blockers.",
		StartTime:   time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		URL:         "https://meet.example.com/sync",
		Attendees: []model.Attendee{
			{Name: "Alex", Email: "alex@example.com", RSVPStatus: "ACCEPTED"},
			{Email: "sam@example.com", RSVPStatus: "NEEDS-ACTION"},
		},
	}

	got := formatEventDetailsText(ev, "Work")
	assert.Equal(t, "Weekly sync", strings.Split(got, "\n")[0])
	assert.Contains(t, got, formatEventDateRange(ev))
	assert.Contains(t, got, formatEventTimeRange(ev))
	assert.Contains(t, got, "Work")
	assert.Contains(t, got, "Zoom")
	assert.Contains(t, got, "Status updates and blockers.")
	assert.Contains(t, got, "Alex <alex@example.com> · Accepted")
	assert.Contains(t, got, "sam@example.com · No response")
	assert.Contains(t, got, "https://meet.example.com/sync")
}

func TestFormatEventDetailsText_UntitledWhenEmpty(t *testing.T) {
	got := formatEventDetailsText(event.Event{}, "")
	assert.True(t, strings.HasPrefix(got, "Untitled"), got)
}

func TestEventViewDialog_PCopiesEventDetails(t *testing.T) {
	ev := testViewEvent()
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	require.NotNil(t, cmd, "'p' should copy event details")
}

func TestEventDialog_PCopiesSelectedEventDetails(t *testing.T) {
	m := rsvpDialogModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	require.NotNil(t, cmd, "'p' should copy the selected event")
}

func TestHelpDialog_EventsSectionDocumentsCopy(t *testing.T) {
	if got := findHelpEntry(t, "Events", "copy event details"); got != "p" {
		t.Fatalf("copy key = %q, want %q", got, "p")
	}
}

// A server can send any PARTSTAT value. formatRSVPCopy must split the
// first character on a rune, so a multi-byte value keeps its characters.
func TestFormatRSVPCopy_MultiByteValue(t *testing.T) {
	tests := []struct {
		name   string
		status string
		want   string
	}{
		{"multi-byte first rune", "ÉBAUCHE", "Ébauche"},
		{"known value", "accepted", "Accepted"},
		{"single byte fallback", "X-CUSTOM", "X-custom"},
		{"empty", "", "No response"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, formatRSVPCopy(tc.status))
		})
	}
}
