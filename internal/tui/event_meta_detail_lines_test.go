package tui

import (
	"regexp"
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

// mouseZoneMarker matches the private CSI marker mouseMark emits (ESC[<id>z).
// These are only meaningful on surfaces that MouseSweep their own output.
var mouseZoneMarker = regexp.MustCompile(`\x1b\[\d+z`)

func metaOptsWithURLs(interactive bool) eventMetaDetailLinesOptions {
	return eventMetaDetailLinesOptions{
		labelStyle:  lipgloss.NewStyle(),
		width:       80,
		labelWidth:  12,
		urlRewriter: func(s string) string { return s },
		interactive: interactive,
		location:    "Join at https://meet.example.com/room",
		conference:  "zoommtg://zoom.us/join?confno=1",
		url:         "https://example.com/event",
	}
}

// The list-pane and trash detail panes render plain shell.View() output that is
// composited AFTER the app's single MouseSweep, so any mouse-zone markers would
// leak as raw escapes. Non-interactive rows must emit OSC 8 links only.
func TestEventMetaDetailLines_NonInteractiveEmitsNoMouseZones(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	out := strings.Join(eventMetaDetailLines(metaOptsWithURLs(false)), "\n")

	assert.Contains(t, out, "\x1b]8;;", "non-interactive rows should still carry OSC 8 links")
	assert.NotRegexp(t, mouseZoneMarker, out, "non-interactive rows must not emit mouse-zone markers")
}

// The full event view sweeps its own output, so it opts into clickable zones.
func TestEventMetaDetailLines_InteractiveEmitsMouseZones(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	out := strings.Join(eventMetaDetailLines(metaOptsWithURLs(true)), "\n")

	assert.Regexp(t, mouseZoneMarker, out, "interactive rows should emit mouse-zone markers")
}

// The Conference field holds a whole URI (here a non-http scheme): it must be
// wrapped as an OSC 8 link verbatim rather than regressing to plain text.
func TestEventMetaDetailLines_ConferenceNonHTTPStaysLinked(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	out := strings.Join(eventMetaDetailLines(metaOptsWithURLs(false)), "\n")

	assert.Contains(t, out, "\x1b]8;;zoommtg://zoom.us/join?confno=1\x1b\\",
		"a non-http Conference URI must still get an OSC 8 link")
}
