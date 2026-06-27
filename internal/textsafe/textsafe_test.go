package textsafe

import "testing"

func TestStripEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello world", "hello world"},
		{"csi color", "\x1b[31mred\x1b[0m", "red"},
		{"osc 8 hyperlink bel", "\x1b]8;;https://example.com\x07link\x1b]8;;\x07", "link"},
		{"osc terminated by st", "\x1b]0;title\x1b\\rest", "rest"},
		{"lone esc dropped", "a\x1bb", "ab"},
		{"whitespace preserved", "a\tb\n c", "a\tb\n c"},
		{"multibyte preserved", "café — \x1b[1mok\x1b[0m", "café — ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := StripEscapes(tc.in); got != tc.want {
				t.Fatalf("StripEscapes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestDisplayStripsEscapesAndCollapsesWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"osc hyperlink", "\x1b]8;;https://example.com\x07click\x1b]8;;\x07 here", "click here"},
		{"csi and newlines", "\x1b[31mline1\x1b[0m\nline2", "line1 line2"},
		{"control chars dropped", "a\x00b\x07c", "abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Display(tc.in); got != tc.want {
				t.Fatalf("Display(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestDisplayStripsBidiFormatControls verifies that Display removes Unicode
// category Cf characters (bidi overrides/isolates, zero-width spaces, BOM) to
// prevent Trojan-Source spoofing (CVE-2021-42574).  unicode.IsControl only
// covers category Cc; Cf characters were previously written through unchanged.
func TestDisplayStripsBidiFormatControls(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// U+202E RIGHT-TO-LEFT OVERRIDE — canonical Trojan Source char.
		{"RLO stripped", "hello\u202eworld", "helloworld"},
		// U+202A LEFT-TO-RIGHT EMBEDDING.
		{"LRE stripped", "\u202ahello\u202c", "hello"},
		// U+202B RIGHT-TO-LEFT EMBEDDING.
		{"RLE stripped", "\u202bhello\u202c", "hello"},
		// U+202D LEFT-TO-RIGHT OVERRIDE.
		{"LRO stripped", "\u202dhello", "hello"},
		// U+202C POP DIRECTIONAL FORMATTING (closes embeddings/overrides).
		{"PDF stripped", "ab\u202ccd", "abcd"},
		// U+200E LEFT-TO-RIGHT MARK.
		{"LRM stripped", "a\u200eb", "ab"},
		// U+200F RIGHT-TO-LEFT MARK.
		{"RLM stripped", "a\u200fb", "ab"},
		// U+200B ZERO WIDTH SPACE.
		{"ZWSP stripped", "a\u200bb", "ab"},
		// U+FEFF BOM / ZERO WIDTH NO-BREAK SPACE.
		{"BOM stripped", "\uFEFFfoo", "foo"},
		// U+2066 LEFT-TO-RIGHT ISOLATE / U+2069 POP DIRECTIONAL ISOLATE.
		{"LRI and PDI stripped", "\u2066hello\u2069", "hello"},
		// U+2067 RIGHT-TO-LEFT ISOLATE.
		{"RLI stripped", "\u2067hello\u2069", "hello"},
		// U+2068 FIRST STRONG ISOLATE.
		{"FSI stripped", "\u2068hello\u2069", "hello"},
		// U+2069 POP DIRECTIONAL ISOLATE standalone.
		{"PDI stripped", "a\u2069b", "ab"},
		// Combination that would spoof a filename extension in terminals.
		{"trojan-source filename spoof", "evil\u202e\u2066gnp.exe", "evilgnp.exe"},
		// Legitimate accented/non-ASCII text must not be touched.
		{"plain text preserved", "Meeting with Ángela at café", "Meeting with Ángela at café"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Display(tc.in); got != tc.want {
				t.Fatalf("Display(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
