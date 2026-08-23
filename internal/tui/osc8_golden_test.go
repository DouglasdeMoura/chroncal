package tui

import (
	"strings"
	"testing"

	"image/color"

	lipgloss "charm.land/lipgloss/v2"
)

// This file is the designated golden for OSC 8 hyperlink framing and
// reverse-video SGR. Other TUI tests call these helpers instead of matching
// raw escape sequences.

const osc8Introducer = "\x1b]8;;"
const osc8ST = "\x1b\\"

func hasOSC8(s string) bool {
	return strings.Contains(s, osc8Introducer)
}

func osc8Open(url string) string {
	return osc8Introducer + url + osc8ST
}

func osc8Close() string {
	return osc8Introducer + osc8ST
}

func osc8Count(s string) int {
	return strings.Count(s, osc8Introducer)
}

func hasReverseVideo(s string) bool {
	sample := lipgloss.NewStyle().Reverse(true).Render("x")
	i := strings.Index(sample, "x")
	if i <= 0 {
		return strings.Contains(s, "\x1b[7m")
	}
	return strings.Contains(s, sample[:i])
}

func backgroundSeq(c color.Color) string {
	sample := lipgloss.NewStyle().Background(c).Render("x")
	i := strings.Index(sample, "x")
	if i <= 1 {
		return sample
	}
	seq := strings.TrimPrefix(sample[:i], "\x1b[")
	return strings.TrimSuffix(seq, "m")
}

func TestOSC8Golden_LinkifyTextFraming(t *testing.T) {
	isolateMouseTracker(t)
	out := linkifyText("see https://example.com/foo for details.", nil)
	if !strings.Contains(out, osc8Open("https://example.com/foo")) {
		t.Fatalf("expected OSC 8 open for the URL, got %q", out)
	}
	if !strings.Contains(out, osc8Close()) {
		t.Fatalf("expected OSC 8 close, got %q", out)
	}
}
