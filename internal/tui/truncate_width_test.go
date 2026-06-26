package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// TestTruncateToWideRunesBounded reproduces issue #213: truncateTo gated on
// display width but cut by rune index, so wide (CJK/emoji) runes overflowed
// the requested column width. The result must never be wider than w cells.
func TestTruncateToWideRunesBounded(t *testing.T) {
	cases := []struct {
		name string
		s    string
		w    int
	}{
		{"cjk", strings.Repeat("世", 20), 10},
		{"emoji", strings.Repeat("😀", 20), 8},
		{"mixed", "会議 " + strings.Repeat("界", 30), 12},
		{"ascii", strings.Repeat("a", 50), 10},
		{"narrow", strings.Repeat("世", 5), 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateTo(tc.s, tc.w)
			if w := lipgloss.Width(got); w > tc.w {
				t.Fatalf("truncateTo(%q, %d) = %q has display width %d > %d",
					tc.s, tc.w, got, w, tc.w)
			}
		})
	}
}

// TestRenderEventPillWideRunesBounded covers the same rune-vs-width mismatch in
// renderEventPill (format.go), which shares the truncation logic.
func TestRenderEventPillWideRunesBounded(t *testing.T) {
	ev := CalendarEvent{Title: strings.Repeat("世", 20)}
	got := renderEventPill(ev, 10, false)
	if w := lipgloss.Width(got); w != 10 {
		t.Fatalf("renderEventPill width = %d, want 10 (content %q)", w, got)
	}
	if n := strings.Count(got, "\n"); n != 0 {
		t.Fatalf("renderEventPill wrapped to %d extra lines; pill must stay one row (content %q)", n, got)
	}
}

// TestRenderTimeCellContentWideRunesBounded covers renderTimeCellContent.
func TestRenderTimeCellContentWideRunesBounded(t *testing.T) {
	p := placedEvent{event: CalendarEvent{Title: strings.Repeat("世", 20)}}
	got := renderTimeCellContent(p, 0, 10)
	if w := lipgloss.Width(got); w != 10 {
		t.Fatalf("renderTimeCellContent width = %d, want 10 (content %q)", w, got)
	}
	if n := strings.Count(got, "\n"); n != 0 {
		t.Fatalf("renderTimeCellContent wrapped to %d extra lines; cell must stay one row (content %q)", n, got)
	}
}
