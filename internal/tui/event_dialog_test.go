package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// TestRsvpButtonWidthMatchesRendered is a regression test for issue #346.
//
// rsvpButtonWidth() was returning rsvpMaxLabelWidth()+2, which only accounted
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
