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
