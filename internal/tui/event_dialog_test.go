package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// TestRsvpButtonWidthMatchesRendered is a regression test for issue #346.
//
// rsvpButtonWidth() returned rsvpMaxLabelWidth()+2, which only accounted
// for one side of the button padding.  DefaultButtonStyles uses
// Padding(0,2).MarginRight(1), so the real rendered cell-width of a button
// whose label has been padded to rsvpMaxLabelWidth() is label_w+2+2+1 = label_w+5.
//
// hitRSVPBtn computes hit zones as [cx, cx+btnW) and advances by btnW+1 (the
// +1 is the join-space from strings.Join).  Both the zone width and the
// advance must use the same measured value, so rsvpButtonWidth() must equal
// the lipgloss.Width of a freshly rendered button.
func TestRsvpButtonWidthMatchesRendered(t *testing.T) {
	fixedW := rsvpMaxLabelWidth()
	label := strings.Repeat(" ", fixedW)
	want := lipgloss.Width(DefaultButtonStyles().Normal.Render(label, false))
	got := rsvpButtonWidth()
	if got != want {
		t.Errorf("rsvpButtonWidth() = %d, want %d (rendered width including padding+margin)", got, want)
	}
}

// TestFormatAlarm verifies the human-readable render of RFC 5545 alarm
// triggers, with emphasis on the singular/plural agreement that pluralize()
// now handles (issue #548). Before the fix, n>1 produced ungrammatical
// "2 week"/"2 day"/"2 hour" strings because pluralize() never inflected the
// unit; the "min."/"sec." abbreviations were only correct by accident.
func TestFormatAlarm(t *testing.T) {
	cases := []struct {
		name  string
		alarm model.Alarm
		want  string
	}{
		{"empty is event time", model.Alarm{TriggerValue: ""}, "at event time"},

		// Singular form for n == 1.
		{"one week before", model.Alarm{TriggerValue: "-P1W"}, "1 week before"},
		{"one day before", model.Alarm{TriggerValue: "-P1D"}, "1 day before"},
		{"one hour before", model.Alarm{TriggerValue: "-PT1H"}, "1 hour before"},
		{"one minute before", model.Alarm{TriggerValue: "-PT1M"}, "1 min. before"},
		{"one second before", model.Alarm{TriggerValue: "-PT1S"}, "1 sec. before"},

		// Plural form for n > 1 — the actual bug under test.
		{"two weeks before", model.Alarm{TriggerValue: "-P2W"}, "2 weeks before"},
		{"two days before", model.Alarm{TriggerValue: "-P2D"}, "2 days before"},
		{"two hours before", model.Alarm{TriggerValue: "-PT2H"}, "2 hours before"},
		{"two minutes before", model.Alarm{TriggerValue: "-PT2M"}, "2 min. before"},
		{"two seconds before", model.Alarm{TriggerValue: "-PT2S"}, "2 sec. before"},

		// Plural form for n == 0 (English pluralizes zero).
		{"zero weeks before", model.Alarm{TriggerValue: "-P0W"}, "0 weeks before"},

		// Positive ("after") sign is preserved.
		{"two hours after", model.Alarm{TriggerValue: "PT2H"}, "2 hours after"},

		// Mixed durations join each component with its own agreement.
		{"mixed day and hours before", model.Alarm{TriggerValue: "-P1DT2H"}, "1 day 2 hours before"},

		// A trigger with no parseable components is returned verbatim.
		{"absolute fallback", model.Alarm{TriggerValue: "20260101T000000Z"}, "20260101T000000Z"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatAlarm(tc.alarm); got != tc.want {
				t.Errorf("formatAlarm(%q) = %q, want %q", tc.alarm.TriggerValue, got, tc.want)
			}
		})
	}
}

// TestPluralize locks the noun-form contract of pluralize() (issue #548). One
// yields the singular form. Everything else (zero included) yields the plural
// form. Irregular nouns such as the "min." abbreviation are passed through
// explicitly rather than mangled by a "+s" rule.
func TestPluralize(t *testing.T) {
	cases := []struct {
		name     string
		n        int
		singular string
		plural   string
		want     string
	}{
		{"one is singular", 1, "week", "weeks", "week"},
		{"two is plural", 2, "week", "weeks", "weeks"},
		{"zero is plural", 0, "week", "weeks", "weeks"},
		{"many is plural", 5, "calendar", "calendars", "calendars"},
		{"irregular abbreviation passes through", 3, "min.", "min.", "min."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := pluralize(tc.n, tc.singular, tc.plural); got != tc.want {
				t.Errorf("pluralize(%d, %q, %q) = %q, want %q", tc.n, tc.singular, tc.plural, got, tc.want)
			}
		})
	}
}
