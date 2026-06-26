package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestTruncateToRespectsDisplayWidth(t *testing.T) {
	tests := []struct {
		name string
		in   string
		w    int
	}{
		// A full-width (CJK) title is 8 runes but 16 display cells. Cutting
		// by rune count would leave it far wider than the budget and overflow
		// the column; truncation must respect display width.
		{"cjk_full_width", "你好世界一二三四", 10},
		{"cjk_tight", "你好世界一二三四", 5},
		// Emoji are also wide; the budget is in display cells, not runes.
		{"emoji", "😀😀😀😀😀😀", 7},
		// Mixed ASCII + CJK.
		{"mixed", "abc你好世界xyz", 8},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := truncateTo(tc.in, tc.w)
			if w := lipgloss.Width(got); w > tc.w {
				t.Fatalf("truncateTo(%q, %d) = %q, display width %d exceeds budget %d",
					tc.in, tc.w, got, w, tc.w)
			}
			if !strings.HasSuffix(got, "…") {
				t.Fatalf("truncateTo(%q, %d) = %q, expected ellipsis suffix", tc.in, tc.w, got)
			}
		})
	}
}

func TestTruncateToFitsUnchanged(t *testing.T) {
	tests := []struct {
		name string
		in   string
		w    int
	}{
		{"ascii", "hello", 10},
		{"cjk_exact", "你好", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := truncateTo(tc.in, tc.w); got != tc.in {
				t.Fatalf("truncateTo(%q, %d) = %q, want unchanged", tc.in, tc.w, got)
			}
		})
	}
}

func TestTruncateToBreaksOnWhitespace(t *testing.T) {
	got := truncateTo("hello world foo", 12)
	if w := lipgloss.Width(got); w > 12 {
		t.Fatalf("display width %d exceeds 12: %q", w, got)
	}
	if strings.Contains(got, "wor…") {
		t.Fatalf("expected word-boundary break, got mid-word cut: %q", got)
	}
}
