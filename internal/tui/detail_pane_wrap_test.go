package tui

import (
	"strings"
	"testing"
)

// TestWrapTitleLinesKeepsSplitWordsWhole wraps a title with a word longer
// than the line. wrapLine splits that word with no separator. The last
// line must continue the title text, so no space appears inside the split
// word. A join of the wrapped lines with " " puts a space after a full
// chunk, and today truncateTo always cuts before that space. The test
// guards the result if that cut rule changes.
func TestWrapTitleLinesKeepsSplitWordsWhole(t *testing.T) {
	for _, tc := range []struct {
		name  string
		title string
		w     int
		want  []string
	}{
		{
			name:  "long URL",
			title: "https://example.com/a/very/long/path/to/a/meeting/room",
			w:     10,
			want:  []string{"https://ex", "ample.com…"},
		},
		{
			name:  "CJK title",
			title: "東京で開かれる年次総会の準備会議について",
			w:     8,
			want:  []string{"東京で開", "かれる…"},
		},
		{
			name:  "words then a long word",
			title: "Call https://example.com/meeting/room/42",
			w:     10,
			want:  []string{"Call", "https://e…"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := wrapTitleLines(tc.title, tc.w, 2)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("wrapTitleLines(%q) = %q, want %q", tc.title, got, tc.want)
			}
		})
	}
}
