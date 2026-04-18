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

func testViewEvent() event.Event {
	return event.Event{
		ID:          42,
		CalendarID:  1,
		Title:       "Weekly sync",
		Location:    "Zoom",
		Description: "Status updates and blockers.",
		StartTime:   time.Date(2026, 4, 20, 14, 0, 0, 0, time.UTC),
		EndTime:     time.Date(2026, 4, 20, 15, 0, 0, 0, time.UTC),
		Timezone:    "UTC",
		Transp:      "OPAQUE",
		Class:       "PUBLIC",
	}
}

func TestEventViewDialog_RendersCoreFields(t *testing.T) {
	ev := testViewEvent()
	cal := CalendarInfo{Name: "Work", Color: "#a6e3a1", OwnerEmail: "me@example.com"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(120, 40)
	out := m.View()
	require.NotEmpty(t, out)

	for _, want := range []string{ev.Title, "Date", "Time", "Duration", "Calendar", "Where", "Zoom", "Notes", "Edit", "Duplicate", "Delete", "Close"} {
		assert.Contains(t, out, want, "expected rendered dialog to contain %q", want)
	}
}

func TestEventViewDialog_ShowsRSVPForAttendee(t *testing.T) {
	ev := testViewEvent()
	ev.Attendees = []model.Attendee{
		{Email: "me@example.com", RSVPStatus: "NEEDS-ACTION"},
	}
	cal := CalendarInfo{Name: "Work", Color: "#a6e3a1", OwnerEmail: "me@example.com"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(120, 40)
	out := m.View()

	assert.Contains(t, out, "Your RSVP")
	assert.Contains(t, out, "Yes")
	assert.Contains(t, out, "No")
	assert.Contains(t, out, "Maybe")
}

func TestEventViewDialog_EditKeyEmitsEditMsg(t *testing.T) {
	ev := testViewEvent()
	cal := CalendarInfo{Name: "Work"}
	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(120, 40)

	_, cmd := m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	require.NotNil(t, cmd)
	msg := cmd()
	edit, ok := msg.(EventEditMsg)
	require.True(t, ok, "expected EventEditMsg, got %T", msg)
	assert.Equal(t, ev.ID, edit.Event.ID)
}

func TestEventViewDialog_CloseKeyEmitsClosedMsg(t *testing.T) {
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{}, Theme{}).SetSize(120, 40)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	require.NotNil(t, cmd)
	_, ok := cmd().(EventViewClosedMsg)
	assert.True(t, ok)
}

func TestEventViewDialog_TabCyclesFocusedAction(t *testing.T) {
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{}, Theme{}).SetSize(120, 40)
	m2, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	assert.Equal(t, 1, m2.focusedAction)
}

func TestEventViewDialog_OnlyShowsRecurrenceWhenRepeating(t *testing.T) {
	ev := testViewEvent()
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	assert.NotContains(t, m.View(), "Repeat")

	ev.RecurrenceRule = "FREQ=WEEKLY"
	m = NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	out := m.View()
	assert.Contains(t, out, "Repeat")
	assert.Contains(t, out, "Every week")
}

func TestDescribeRecurrence(t *testing.T) {
	cases := map[string]string{
		"":                                 "",
		"FREQ=DAILY":                       "Every day",
		"FREQ=WEEKLY":                      "Every week",
		"FREQ=MONTHLY":                     "Every month",
		"FREQ=YEARLY":                      "Every year",
		"FREQ=HOURLY":                      "Custom",
		"FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR": "Weekdays",
	}
	for rule, want := range cases {
		got := describeRecurrence(rule)
		assert.Equal(t, want, got, "describeRecurrence(%q)", rule)
	}
}

func TestFormatShowAsVisibility(t *testing.T) {
	assert.Equal(t, "Busy", formatShowAs("OPAQUE"))
	assert.Equal(t, "Free", formatShowAs("TRANSPARENT"))
	assert.Equal(t, "", formatShowAs(""))

	assert.Equal(t, "Private", formatVisibility("PRIVATE"))
	assert.Equal(t, "Public", formatVisibility("public"))
	assert.Equal(t, "", formatVisibility(""))
}

func TestFormatEventDateRange(t *testing.T) {
	// Single all-day event.
	ev := event.Event{
		StartTime: time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 18, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
	}
	assert.Equal(t, "Fri, Apr 17, 2026", formatEventDateRange(ev))

	// Multi-day all-day event (Fri–Sun): exclusive end subtracts a day.
	ev = event.Event{
		StartTime: time.Date(2026, 4, 17, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
	}
	assert.Equal(t, "Fri, Apr 17 – Sun, Apr 19, 2026", formatEventDateRange(ev))

	// Timed event spanning two days.
	ev = event.Event{
		StartTime: time.Date(2026, 4, 17, 9, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 18, 8, 0, 0, 0, time.UTC),
	}
	assert.Equal(t, "Fri, Apr 17 – Sat, Apr 18, 2026", formatEventDateRange(ev))

	// Cross-year.
	ev = event.Event{
		StartTime: time.Date(2026, 12, 30, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC),
		AllDay:    true,
	}
	assert.Equal(t, "Wed, Dec 30, 2026 – Fri, Jan 1, 2027", formatEventDateRange(ev))
}

func TestFormatEventTimeRange(t *testing.T) {
	loc := time.FixedZone("UTC", 0)
	ev := event.Event{
		StartTime: time.Date(2026, 4, 17, 9, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 4, 18, 8, 0, 0, 0, loc),
	}
	// Output is in local time; test by comparing against expected local render.
	start := ev.StartTime.Local().Format("15:04")
	end := ev.EndTime.Local().Format("15:04")
	assert.Equal(t, start+" – "+end, formatEventTimeRange(ev))

	// All-day events have no time.
	ev.AllDay = true
	assert.Equal(t, "", formatEventTimeRange(ev))
}

func TestEventViewDialog_TitleRowHasDeleteButton(t *testing.T) {
	// No chrome heading: the event title leads the content, with the
	// Delete button pinned to the right of that same row.
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	out := m.View()

	var titleRow string
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(stripANSI(line))
		if trimmed == "" || strings.HasPrefix(trimmed, "╭") {
			continue
		}
		trimmed = strings.TrimLeft(trimmed, "│ ")
		trimmed = strings.TrimRight(trimmed, " │")
		if trimmed == "" {
			continue
		}
		titleRow = trimmed
		break
	}
	require.NotEmpty(t, titleRow)
	assert.True(t, strings.HasPrefix(titleRow, "Weekly sync"), "title row should start with event title: %q", titleRow)
	assert.Contains(t, titleRow, "Delete")
}

func TestEventViewDialog_DeleteNotInBottomActions(t *testing.T) {
	// With Delete pinned to the title row, the bottom action bar must
	// not render a second Delete button.
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	plain := stripANSI(m.View())
	count := strings.Count(plain, "Delete")
	assert.Equal(t, 1, count, "expected exactly one Delete button, got %d", count)
}

func TestEventViewDialog_ViewIsStableAcrossRenders(t *testing.T) {
	// mouseSweep mutates the default tracker; rendering twice should
	// produce identical output and not accumulate markers.
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	first := m.View()
	second := m.View()
	assert.Equal(t, first, second)
	assert.False(t, strings.Contains(first, "\x1b[0z"), "mouse markers should be swept")
}
