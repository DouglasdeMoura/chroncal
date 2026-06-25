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
	out := linkifyText(in, nil)

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
	out := linkifyText("Open https://example.com.", nil)
	cleaned := mouseSweep(out)
	assert.True(t, strings.HasSuffix(cleaned, "."), "period should survive outside link, got %q", cleaned)
	// Period sits one column past the link, so a click on it should miss the zone.
	target := mouseResolve(len("Open ")+len("https://example.com"), 0)
	assert.Equal(t, "", target, "trailing period must not be part of the clickable zone")
}

func TestLinkifyText_NoURLReturnsUnchanged(t *testing.T) {
	in := "no links here, just words"
	require.Equal(t, in, linkifyText(in, nil))
}

func TestGoogleAuthuserRewriter(t *testing.T) {
	rw := googleAuthuserRewriter("me@example.com")
	require.NotNil(t, rw)

	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			"Meet link gets authuser appended",
			"https://meet.google.com/abc-defg-hij",
			"https://meet.google.com/abc-defg-hij?authuser=me%40example.com",
		},
		{
			"Calendar link gets authuser appended",
			"https://calendar.google.com/calendar/event?eid=xyz",
			"https://calendar.google.com/calendar/event?authuser=me%40example.com&eid=xyz",
		},
		{
			"Docs link gets authuser appended",
			"https://docs.google.com/document/d/abc/edit",
			"https://docs.google.com/document/d/abc/edit?authuser=me%40example.com",
		},
		{
			"Existing authuser is preserved",
			"https://meet.google.com/abc?authuser=other@example.com",
			"https://meet.google.com/abc?authuser=other@example.com",
		},
		{
			"Non-account google host is left alone",
			"https://maps.google.com/?q=foo",
			"https://maps.google.com/?q=foo",
		},
		{
			"Non-google host is left alone",
			"https://example.com/meeting",
			"https://example.com/meeting",
		},
	}
	for _, tc := range cases {
		assert.Equal(t, tc.want, rw(tc.in), tc.name)
	}
}

func TestGoogleAuthuserRewriter_EmptyEmailReturnsNil(t *testing.T) {
	assert.Nil(t, googleAuthuserRewriter(""))
	assert.Nil(t, googleAuthuserRewriter("   "))
}

func TestIsGoogleAccountServer(t *testing.T) {
	assert.True(t, isGoogleAccountServer("https://apidata.googleusercontent.com/caldav/v2/me%40example.com/user/"))
	assert.False(t, isGoogleAccountServer("https://caldav.icloud.com/"))
	assert.False(t, isGoogleAccountServer("https://example.com/dav/"))
	assert.False(t, isGoogleAccountServer(""))
}

func TestRenderLinkValue_AppliesRewriterToTargetNotVisibleText(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	rw := googleAuthuserRewriter("me@example.com")
	out := renderLinkValue("https://meet.google.com/abc", 80, rw, true)

	// OSC 8 target carries the rewritten URL so modifier-click in honoring
	// terminals opens the right account.
	assert.Contains(t, out, "\x1b]8;;https://meet.google.com/abc?authuser=me%40example.com\x1b\\")

	// Visible text (between the OSC 8 open and close) stays the original URL.
	assert.Contains(t, out, "\\https://meet.google.com/abc\x1b]8;;")

	// MouseMark click target also uses the rewritten URL. mouseSweep must
	// run first to register the zone with the tracker.
	_ = mouseSweep(out)
	target := mouseResolve(0, 0)
	assert.Equal(t, "link:https://meet.google.com/abc?authuser=me%40example.com", target)
}

func TestRenderLinkValue_KeepsExactTargetForTrailingSubDelimiter(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	// A structured URL field whose value legitimately ends in a URL
	// sub-delimiter must keep that character in the click target — the prose
	// trimURLTail behavior would wrongly drop it.
	raw := "https://example.com/confirm!"
	out := renderLinkValue(raw, 80, nil, true)

	assert.Contains(t, out, "\x1b]8;;"+raw+"\x1b\\", "OSC 8 target must keep the trailing '!'")

	_ = mouseSweep(out)
	assert.Equal(t, "link:"+raw, mouseResolve(0, 0), "mouse target must keep the trailing '!'")
}

func TestRenderLinkValue_WrapsNonHTTPScheme(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	// Known URI fields may hold non-http schemes (an imported CONFERENCE
	// zoommtg:// link, a mailto: URL). renderLinkValue wraps the whole value
	// regardless of scheme rather than regressing to plain text.
	raw := "zoommtg://zoom.us/join?confno=123"
	out := renderLinkValue(raw, 80, nil, true)

	assert.Contains(t, out, "\x1b]8;;"+raw+"\x1b\\", "non-http URI must still get an OSC 8 link")

	_ = mouseSweep(out)
	assert.Equal(t, "link:"+raw, mouseResolve(0, 0))
}

func TestRenderLinkifiedValue_TruncationMidURLKeepsFullClickTarget(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	value := "https://example.com/really/long/path/that/overflows"

	out := renderLinkifiedValue(value, 20, nil, true)
	cleaned := mouseSweep(out)

	assert.Contains(t, cleaned, "https://example.com…")
	assert.Equal(t, "link:https://example.com/really/long/path/that/overflows", mouseResolve(5, 0))
}

func TestRenderLinkifiedValue_TwoURLsSecondTruncatedAwayFirstStillClickable(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	first := "https://one.example.com/path"
	second := "https://two.example.com/path"
	value := "See " + first + " and " + second

	out := renderLinkifiedValue(value, 34, nil, true)
	cleaned := mouseSweep(out)

	assert.Contains(t, cleaned, first)
	assert.NotContains(t, cleaned, second)
	assert.Equal(t, "link:"+first, mouseResolve(len("See ")+2, 0))
}

func TestRenderLinkifiedValue_TrailingPunctuationAfterTruncateToStaysOutsideZone(t *testing.T) {
	defaultMouseTracker = &mouseTracker{}
	value := "Open https://example.com/path), and additional text"

	out := renderLinkifiedValue(value, 34, nil, true)
	cleaned := mouseSweep(out)
	url := "https://example.com/path"
	punctX := len("Open ") + len(url)

	assert.Contains(t, cleaned, url)
	assert.Contains(t, cleaned, "\x1b]8;;\x1b\\),")
	assert.Equal(t, "link:"+url, mouseResolve(len("Open ")+3, 0))
	assert.Equal(t, "", mouseResolve(punctX, 0))
}

func TestRenderLinkifiedValue_NoURLReturnsOriginalText(t *testing.T) {
	value := "no links here, just words"
	assert.Equal(t, value, renderLinkifiedValue(value, 80, nil, true))
}

func TestOpenURLCmd_RejectsNonHTTP(t *testing.T) {
	assert.Nil(t, openURLCmd("javascript:alert(1)"))
	assert.Nil(t, openURLCmd("file:///etc/passwd"))
	assert.Nil(t, openURLCmd(""))
	// http/https returns a non-nil command (we don't execute it here).
	assert.NotNil(t, openURLCmd("https://example.com"))
}
