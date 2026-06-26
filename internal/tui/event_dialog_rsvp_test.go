package tui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// rsvpDialogModel returns an EventDialogModel with a single event that
// includes the calendar owner as a non-organizer attendee, so rsvpActions()
// returns the full [Yes, No, Maybe] slice.
func rsvpDialogModel() EventDialogModel {
	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	ev := event.Event{
		ID:         1,
		CalendarID: 1,
		Title:      "Team sync",
		StartTime:  time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		Attendees: []model.Attendee{
			{Email: "me@example.com", RSVPStatus: "NEEDS-ACTION"},
		},
	}
	cals := map[int64]CalendarInfo{
		1: {Name: "Work", Color: "#a6e3a1", OwnerEmail: "me@example.com"},
	}
	return NewEventDialogModel(day, []event.Event{ev}, cals, help.New()).SetSize(120, 40)
}

// TestEventDialog_RSVPYesKeyEmitsAccepted checks that 'y' fires ACCEPTED.
func TestEventDialog_RSVPYesKeyEmitsAccepted(t *testing.T) {
	m := rsvpDialogModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	require.NotNil(t, cmd, "'y' should return a command")
	msg := cmd()
	rsvp, ok := msg.(EventRSVPMsg)
	require.True(t, ok, "expected EventRSVPMsg, got %T", msg)
	assert.Equal(t, "ACCEPTED", rsvp.Status)
}

// TestEventDialog_RSVPNoKeyEmitsDeclined is the regression test for #345:
// 'n' was a dead key — handleKey handled Yes and Maybe but omitted No.
func TestEventDialog_RSVPNoKeyEmitsDeclined(t *testing.T) {
	m := rsvpDialogModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	require.NotNil(t, cmd, "'n' should return a command (was a dead key before fix)")
	msg := cmd()
	rsvp, ok := msg.(EventRSVPMsg)
	require.True(t, ok, "expected EventRSVPMsg, got %T", msg)
	assert.Equal(t, "DECLINED", rsvp.Status)
}

// TestEventDialog_RSVPMaybeKeyEmitsTentative checks that 'm' fires TENTATIVE.
func TestEventDialog_RSVPMaybeKeyEmitsTentative(t *testing.T) {
	m := rsvpDialogModel()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'm', Text: "m"})
	require.NotNil(t, cmd, "'m' should return a command")
	msg := cmd()
	rsvp, ok := msg.(EventRSVPMsg)
	require.True(t, ok, "expected EventRSVPMsg, got %T", msg)
	assert.Equal(t, "TENTATIVE", rsvp.Status)
}
