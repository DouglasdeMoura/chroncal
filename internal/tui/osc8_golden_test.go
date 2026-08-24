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

func canonSGR(p string) string {
	p = strings.TrimLeft(p, "0")
	if p == "" {
		return "0"
	}
	return p
}

func sgrParamSets(s string) []map[string]bool {
	var sets []map[string]bool
	for {
		i := strings.Index(s, "\x1b[")
		if i < 0 {
			return sets
		}
		s = s[i+2:]
		j := strings.IndexByte(s, 'm')
		if j < 0 {
			return sets
		}
		set := make(map[string]bool)
		for _, p := range strings.Split(s[:j], ";") {
			set[canonSGR(p)] = true
		}
		sets = append(sets, set)
		s = s[j+1:]
	}
}

func hasSGRCode(s, code string) bool {
	want := canonSGR(code)
	for _, set := range sgrParamSets(s) {
		if set[want] {
			return true
		}
	}
	return false
}

func hasSGRCodesTogether(s string, codes ...string) bool {
	for _, set := range sgrParamSets(s) {
		ok := true
		for _, c := range codes {
			if !set[canonSGR(c)] {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

// hasReverseVideo reports SGR reverse (parameter 7), including combined
// attributes such as bold+reverse (`\x1b[1;7m`). Matching a lone `\x1b[7m`
// would miss those sequences.
func hasReverseVideo(s string) bool {
	return hasSGRCode(s, "7")
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
