package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// TestWrapLineDisplayWidth reproduces issue #356: wrapLine measured line width
// in rune count rather than display columns, so CJK characters and emoji
// (whose display width is 2 cells per rune) overflowed the requested width w.
func TestWrapLineDisplayWidth(t *testing.T) {
	cases := []struct {
		name string
		s    string
		w    int
	}{
		// Single CJK word wider than w — hard-break path.
		// "世界会议" = 4 runes but 8 display columns.
		// With w=6, rune-count says 4 <= 6 (no break needed), but display width
		// 8 > 6, so the line overflows.
		{"cjk_word_wide_no_break", "世界会议", 6},

		// Two CJK words that fit individually but overflow when combined.
		// "会议 讨论" = 5 runes, 9 display columns.
		// With w=5, rune-count 5 <= 5 (keeps together), display width 9 > 5.
		{"cjk_words_overflow_combined", "会议 讨论", 5},

		// Long CJK-only word that needs multiple hard breaks.
		{"cjk_long_word", strings.Repeat("世", 10), 8},

		// Emoji — each emoji is 1 rune but 2 display columns.
		{"emoji_single_word", strings.Repeat("😀", 6), 8},

		// Mixed ASCII and CJK — ASCII words are fine, but CJK words overflow.
		{"mixed_cjk_ascii", "foo 世界 bar 讨论", 6},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			lines := wrapLine(tc.s, tc.w)
			for _, ln := range lines {
				if got := lipgloss.Width(ln); got > tc.w {
					t.Errorf("wrapLine(%q, %d): line %q has display width %d > %d",
						tc.s, tc.w, ln, got, tc.w)
				}
			}
		})
	}
}

// TestWrapLineASCIIUnchanged verifies that fixing display-width measuring does
// not regress plain-ASCII word-wrap behaviour (rune width == display width for
// ASCII, so results should be identical to the original code).
func TestWrapLineASCIIUnchanged(t *testing.T) {
	cases := []struct {
		name string
		s    string
		w    int
		want []string
	}{
		{"empty", "", 10, []string{""}},
		{"short_fits", "hello", 10, []string{"hello"}},
		{"two_words_wrap", "hello world", 8, []string{"hello", "world"}},
		{"two_words_fit", "hi there", 10, []string{"hi there"}},
		{"long_word_breaks", "abcdefghij", 4, []string{"abcd", "efgh", "ij"}},
		{"zero_width", "abc", 0, []string{""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapLine(tc.s, tc.w)
			if len(got) != len(tc.want) {
				t.Fatalf("wrapLine(%q, %d) = %v, want %v", tc.s, tc.w, got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("wrapLine(%q, %d)[%d] = %q, want %q",
						tc.s, tc.w, i, got[i], tc.want[i])
				}
			}
		})
	}
}
