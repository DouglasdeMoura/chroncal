package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTrimURLTail_StripsTrailingPunctuation(t *testing.T) {
	cases := map[string]string{
		"https://example.com.":           "https://example.com",
		"https://example.com,":           "https://example.com",
		"https://example.com?":           "https://example.com",
		"https://example.com!":           "https://example.com",
		"https://example.com)":           "https://example.com",
		"https://en.wikipedia.org/(foo)": "https://en.wikipedia.org/(foo)",
	}
	for in, want := range cases {
		assert.Equal(t, want, trimURLTail(in), in)
	}
}

func TestLinkifyText_WrapsURLsWithOSC8AndMouseZone(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	in := "see https://example.com/foo for details."
	out := linkifyText(in)

	assert.Contains(t, out, "\x1b]8;;https://example.com/foo\x1b\\", "expected OSC 8 hyperlink open")
	assert.Contains(t, out, "\x1b]8;;\x1b\\", "expected OSC 8 hyperlink close")
	assert.True(t, strings.HasSuffix(out, " for details."), "trailing text should remain")

	// mouseSweep removes the markers and records a clickable zone.
	cleaned := mouseSweep(out)
	assert.NotContains(t, cleaned, "\x1b[")
	// The URL text should still appear in the cleaned output.
	assert.Contains(t, cleaned, "https://example.com/foo")
	// Resolve a click in the middle of the URL.
	target := mouseResolve(len("see ")+5, 0)
	assert.Equal(t, "link:https://example.com/foo", target)
}

func TestLinkifyText_PreservesTrailingPunctuationOutsideZone(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	out := linkifyText("Open https://example.com.")
	cleaned := mouseSweep(out)
	assert.True(t, strings.HasSuffix(cleaned, "."), "period should survive outside link, got %q", cleaned)
	// Period sits one column past the link, so a click on it should miss the zone.
	target := mouseResolve(len("Open ")+len("https://example.com"), 0)
	assert.Equal(t, "", target, "trailing period must not be part of the clickable zone")
}

func TestLinkifyText_NoURLReturnsUnchanged(t *testing.T) {
	in := "no links here, just words"
	require.Equal(t, in, linkifyText(in))
}

func TestOpenURLCmd_RejectsNonHTTP(t *testing.T) {
	assert.Nil(t, openURLCmd("javascript:alert(1)"))
	assert.Nil(t, openURLCmd("file:///etc/passwd"))
	assert.Nil(t, openURLCmd(""))
	// http/https returns a non-nil command (we don't execute it here).
	assert.NotNil(t, openURLCmd("https://example.com"))
}
