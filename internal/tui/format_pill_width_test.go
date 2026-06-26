package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// Wide glyphs (CJK/emoji) measure two display cells each, so a title whose rune
// count fits the cell-width budget can still overflow it. The pill renderers
// must clip by display width, not rune count, or the styled cell soft-wraps onto
// a second line and corrupts the surrounding grid row (#312).
func TestRenderEventPill_WideGlyphsDoNotWrap(t *testing.T) {
	const cellW = 10
	ev := CalendarEvent{Title: "会議会議会議会議"} // 8 CJK runes = 16 display cells

	out := renderEventPill(ev, cellW, false)

	if h := lipgloss.Height(out); h != 1 {
		t.Fatalf("pill wrapped to %d lines; want 1 (out=%q)", h, out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("pill contains a newline, breaking the grid row: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > cellW {
			t.Fatalf("pill line width %d exceeds cell width %d: %q", w, cellW, line)
		}
	}
}

func TestRenderTimeCellContent_WideGlyphsDoNotWrap(t *testing.T) {
	const cellW = 10
	p := placedEvent{event: CalendarEvent{Title: "会議会議会議会議"}}

	out := renderTimeCellContent(p, 0, cellW)

	if h := lipgloss.Height(out); h != 1 {
		t.Fatalf("time cell wrapped to %d lines; want 1 (out=%q)", h, out)
	}
	if strings.Contains(out, "\n") {
		t.Fatalf("time cell contains a newline, breaking the grid row: %q", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if w := lipgloss.Width(line); w > cellW {
			t.Fatalf("time cell line width %d exceeds cell width %d: %q", w, cellW, line)
		}
	}
}
