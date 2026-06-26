package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/douglasdemoura/chroncal/internal/textsafe"
)

// truncateTo shortens s so its terminal display width is at most w cells,
// appending "…" when truncation occurs. Width is measured with lipgloss so
// full-width (CJK) runes and emoji count as the columns they actually occupy,
// rather than as a single rune each. When the cut would land mid-word it backs
// up to the nearest whitespace within a small look-back window so words aren't
// sliced; a single long token is cut hard. ANSI escape sequences are preserved
// in the returned string regardless of whether truncation was necessary.
//
// This is the single truncation helper for the TUI: every column that needs to
// fit text into a fixed width routes through here so clipping stays consistent
// and never overflows on wide glyphs.
func truncateTo(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}

	// Detect word-boundary break positions using plain text so we don't have
	// to count invisible ANSI bytes. The actual cut is then delegated to
	// ansi.Truncate / ansi.Cut so the original escape sequences are preserved.
	plain := s
	if strings.ContainsRune(s, '\x1b') {
		plain = stripANSI(s)
	}
	r := []rune(plain)

	// Reserve one cell for the ellipsis, then include as many leading runes as
	// fit in the remaining budget by measured display width.
	budget := w - 1
	cut, width := 0, 0
	for cut < len(r) {
		rw := lipgloss.Width(string(r[cut]))
		if width+rw > budget {
			break
		}
		width += rw
		cut++
	}

	// Prefer breaking at whitespace within a small look-back window so
	// truncation doesn't slice mid-word. If no whitespace is within reach
	// (e.g., a single long token), fall back to a hard cut.
	lookback := cut / 3
	for i := cut; i > cut-lookback && i > 1; i-- {
		if r[i-1] == ' ' || r[i-1] == '\t' {
			trimmed := strings.TrimRight(string(r[:i-1]), " \t")
			trimmedWidth := lipgloss.Width(trimmed)
			return ansi.Truncate(s, trimmedWidth, "") + " …"
		}
	}
	return ansi.Truncate(s, w, "…")
}

// stripANSI removes terminal escape sequences from s, leaving plain text. It is
// used to measure and slice the visible content of already-styled strings. It
// delegates to textsafe.StripEscapes so OSC sequences (e.g. OSC 8 hyperlinks)
// are handled the same way as in the rest of the codebase.
func stripANSI(s string) string {
	return textsafe.StripEscapes(s)
}
