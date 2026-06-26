package tui

import (
	"strings"
	"testing"
)

// stripANSI must remove OSC sequences (ESC ]) just like CSI sequences,
// matching textsafe.Display. A raw OSC byte left in the output counts as a
// visible rune and leaks an escape to the terminal. See issue #262.
func TestStripANSI_RemovesOSCSequences(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "OSC 8 hyperlink terminated by BEL",
			in:   "\x1b]8;;https://example.com\x07Visible\x1b]8;;\x07",
			want: "Visible",
		},
		{
			name: "OSC terminated by ST (ESC backslash)",
			in:   "\x1b]0;window title\x1b\\after",
			want: "after",
		},
		{
			name: "CSI still stripped",
			in:   "\x1b[31mred\x1b[0m",
			want: "red",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := stripANSI(tc.in)
			if got != tc.want {
				t.Fatalf("stripANSI(%q) = %q, want %q", tc.in, got, tc.want)
			}
			if strings.ContainsRune(got, '\x1b') {
				t.Fatalf("stripANSI(%q) left an escape byte: %q", tc.in, got)
			}
		})
	}
}
