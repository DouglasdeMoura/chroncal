package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

// TestTruncateToPreservesANSIStyling reproduces issue #350: truncateTo used to
// strip all ANSI escapes from lines it actually truncated. Both the hard-cut
// path (no whitespace in content) and the word-boundary path must preserve the
// original ANSI styling in the returned string.
func TestTruncateToPreservesANSIStyling(t *testing.T) {
	t.Run("hard_cut_preserves_styling", func(t *testing.T) {
		// "\x1b[31m...\x1b[0m" wraps "helloworldfoobar" (16 cells) in red.
		// w=8 forces a hard cut; the result must still contain escape bytes.
		styled := "\x1b[31mhelloworldfoobar\x1b[0m"
		got := truncateTo(styled, 8)
		if !strings.ContainsRune(got, '\x1b') {
			t.Fatalf("truncateTo stripped ANSI on hard cut: got %q", got)
		}
		if w := lipgloss.Width(got); w > 8 {
			t.Fatalf("display width %d > 8: %q", w, got)
		}
		// The plain content must end with "…"; ANSI reset sequences may follow it.
		if plain := stripANSI(got); !strings.HasSuffix(plain, "…") {
			t.Fatalf("expected ellipsis in plain content: got %q (raw: %q)", plain, got)
		}
	})
	t.Run("word_boundary_preserves_styling", func(t *testing.T) {
		// "hello world" (11 cells) styled red; w=8 triggers the word-boundary
		// lookback path. The result must still contain escape bytes.
		styled := "\x1b[31mhello world\x1b[0m"
		got := truncateTo(styled, 8)
		if !strings.ContainsRune(got, '\x1b') {
			t.Fatalf("truncateTo stripped ANSI on word-boundary cut: got %q", got)
		}
		if w := lipgloss.Width(got); w > 8 {
			t.Fatalf("display width %d > 8: %q", w, got)
		}
	})
}

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
