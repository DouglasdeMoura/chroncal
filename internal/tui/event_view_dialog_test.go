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

	for _, want := range []string{ev.Title, "Date", "Time", "Duration", "Calendar", "Where", "Zoom", "Notes", "Edit", "Duplicate", "Delete"} {
		assert.Contains(t, out, want, "expected rendered dialog to contain %q", want)
	}
	// Close is not a button — dismissal is by esc, surfaced only in the footer hint.
	assert.Contains(t, out, "esc close")
}

func TestEventViewDialog_BareURLLocationIsClickable(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	ev := testViewEvent()
	ev.Location = "https://zoom.us/j/123456789"
	cal := CalendarInfo{Name: "Work"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(120, 40)
	out := m.View()

	// OSC 8 hyperlink target carries the full URL (survives the render sweep).
	assert.Contains(t, out, "\x1b]8;;"+ev.Location)
	// The render registered a clickable mouse zone for the URL.
	assert.True(t, hasMouseZone(defaultMouseTracker, linkZonePrefix+ev.Location))
}

func TestEventViewDialog_URLInsideLocationIsClickable(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	ev := testViewEvent()
	ev.Location = "Room 4: https://meet.example.com/abc"
	cal := CalendarInfo{Name: "Work"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(160, 40)
	out := m.View()

	assert.Contains(t, out, "Room 4")
	// The embedded URL is the OSC 8 / click target; surrounding text is plain.
	assert.Contains(t, out, "\x1b]8;;https://meet.example.com/abc")
	assert.True(t, hasMouseZone(defaultMouseTracker, linkZonePrefix+"https://meet.example.com/abc"))
}

func TestEventViewDialog_PlainTextLocationHasNoLink(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	ev := testViewEvent()
	ev.Location = "Conference Room B"
	cal := CalendarInfo{Name: "Work"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(120, 40)
	out := m.View()

	assert.Contains(t, out, "Conference Room B")
	assert.NotContains(t, out, "\x1b]8;;")
}

func TestEventViewDialog_OverflowingLinkifiedLocationStaysTerminated(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	ev := testViewEvent()
	// A text+URL Location far wider than the dialog column, forcing truncation
	// inside detailLinkifiedLine.
	ev.Location = "Room 4: https://meet.example.com/" + strings.Repeat("abcdef", 12)
	cal := CalendarInfo{Name: "Work"}

	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(60, 40)
	out := m.View()

	// Every OSC 8 sequence (open and close) starts with this introducer; a
	// balanced (even) count means no hyperlink was sliced mid-sequence and
	// left unterminated to corrupt the rest of the dialog.
	assert.Equal(t, 0, strings.Count(out, "\x1b]8;;")%2,
		"OSC 8 hyperlink sequences must stay balanced after truncation")
}

func TestEventViewDialog_OverflowingEmbeddedURLKeepsFullClickTarget(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	ev := testViewEvent()
	fullURL := "https://meet.example.com/" + strings.Repeat("abcdef", 12)
	ev.Location = "Room 4: " + fullURL
	cal := CalendarInfo{Name: "Work"}

	// Narrow enough to ellipsize the visible URL text.
	m := NewEventViewDialogModel(ev, cal, Theme{}).SetSize(60, 40)
	out := m.View()

	// Visible text is truncated, but the click target (OSC 8 + mouse zone)
	// must stay the full URL — clicking an ellipsized link still opens the
	// right place.
	assert.Contains(t, out, "\x1b]8;;"+fullURL)
	assert.True(t, hasMouseZone(defaultMouseTracker, linkZonePrefix+fullURL))
}

// hasMouseZone reports whether the render registered a clickable zone with the
// given name. mouseSweep (run inside View) moves marked regions into zones.
func hasMouseZone(mt *mouseTracker, name string) bool {
	for _, z := range mt.zones {
		if z.name == name {
			return true
		}
	}
	return false
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

func TestEventViewDialog_TitleRowIsTitleOnly(t *testing.T) {
	// The title row carries only the event title. Destructive actions
	// live in the bottom action bar, spatially separated.
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
	assert.Equal(t, "Weekly sync", titleRow, "title row should contain only the event title")
	assert.NotContains(t, titleRow, "Delete", "Delete must not live in the title row")
}

func TestEventViewDialog_DeleteRendersInActionBar(t *testing.T) {
	// Delete sits in the bottom action bar, right-aligned with a gap
	// separating it from Edit/Duplicate. Exactly one Delete button is
	// rendered (the footer hint says "delete", lowercase).
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	plain := stripANSI(m.View())
	assert.Equal(t, 1, strings.Count(plain, "Delete"), "expected exactly one Delete button")
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

func TestEventViewDialog_LongContentFitsInTerminal(t *testing.T) {
	// Events with many attendees and long descriptions used to render a
	// box taller than the terminal, so compositeOverlay clipped the top
	// and bottom rows (including the action bar). The body now lives in
	// a viewport that bounds the dialog to the terminal height.
	ev := testViewEvent()
	ev.Description = strings.Repeat("Lorem ipsum dolor sit amet.\n", 100)
	for i := 0; i < 50; i++ {
		ev.Attendees = append(ev.Attendees, model.Attendee{Email: "att@example.com"})
	}
	const termH = 24
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, termH)

	_, bh := m.BoxSize()
	require.LessOrEqual(t, bh, termH, "rendered box must fit inside the terminal")

	// Action bar must still render even with huge content.
	out := m.View()
	assert.Contains(t, out, "Edit")
	assert.Contains(t, out, "Delete")
	// And the title rule should advertise more content below.
	assert.Contains(t, out, "more")
}

func TestEventViewDialog_ScrollKeysMoveBody(t *testing.T) {
	ev := testViewEvent()
	ev.Description = strings.Repeat("scroll line\n", 100)
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 20)
	require.True(t, m.bodyOverflows(), "test precondition: body must overflow")

	require.Equal(t, 0, m.body.YOffset())
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	assert.Equal(t, 1, m.body.YOffset(), "down arrow should scroll body by 1")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnd})
	assert.True(t, m.body.AtBottom(), "end should jump to bottom")

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyHome})
	assert.True(t, m.body.AtTop(), "home should jump to top")
}

func TestEventViewDialog_MouseWheelScrollsBody(t *testing.T) {
	ev := testViewEvent()
	ev.Description = strings.Repeat("scroll line\n", 100)
	m := NewEventViewDialogModel(ev, CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 20)
	require.True(t, m.bodyOverflows(), "test precondition: body must overflow")
	require.Equal(t, 0, m.body.YOffset())

	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	assert.Greater(t, m.body.YOffset(), 0, "wheel down should scroll the body forward")

	prev := m.body.YOffset()
	m, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	assert.Less(t, m.body.YOffset(), prev, "wheel up should scroll the body back")
}

func TestEventViewDialog_XKeyTriggersDelete(t *testing.T) {
	// The x key (and the Delete key) must trigger the delete binding across
	// all three dialogs after the t→x sweep.
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)
	// Focus the delete action.
	m.focusZone = viewZoneActions
	m.focusedAction = eventViewActionDeleteIdx
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("x should produce a command (delete request)")
	}
}
