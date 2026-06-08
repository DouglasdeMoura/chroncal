package tui

import (
	"strings"
	"testing"
)

func TestLooksLikeHTML(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"Just plain text", false},
		{"a < b and c > d", false},
		{"Meet at <b>noon</b>", true},
		{"line one<br>line two", true},
		{"<p>Paragraph</p>", true},
		{`Click <a href="https://x.test">here</a>`, true},
		{"<DIV>caps</DIV>", true},
		{"first\nsecond", false},
	}
	for _, c := range cases {
		if got := looksLikeHTML(c.in); got != c.want {
			t.Errorf("looksLikeHTML(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRenderHTMLDescription_Basic(t *testing.T) {
	lines := renderHTMLDescription("<p>Hello <b>world</b></p>", 80, nil, false)
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d: %#v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "Hello") || !strings.Contains(lines[0], "world") {
		t.Errorf("missing text: %q", lines[0])
	}
}

func TestRenderHTMLDescription_BlocksAndBreaks(t *testing.T) {
	lines := renderHTMLDescription("<p>one</p><p>two</p>", 80, nil, false)
	if len(lines) != 2 || lines[0] != "one" || lines[1] != "two" {
		t.Fatalf("block split wrong: %#v", lines)
	}

	br := renderHTMLDescription("a<br>b", 80, nil, false)
	if len(br) != 2 || br[0] != "a" || br[1] != "b" {
		t.Fatalf("br split wrong: %#v", br)
	}

	dbl := renderHTMLDescription("a<br><br>b", 80, nil, false)
	if len(dbl) != 3 || dbl[1] != "" {
		t.Fatalf("double br should yield blank line: %#v", dbl)
	}
}

func TestRenderHTMLDescription_Entities(t *testing.T) {
	lines := renderHTMLDescription("<p>Tom &amp; Jerry &lt;3 &nbsp;done</p>", 80, nil, false)
	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "Tom & Jerry") || !strings.Contains(joined, "<3") {
		t.Errorf("entities not decoded: %q", joined)
	}
}

func TestRenderHTMLDescription_ListBullets(t *testing.T) {
	lines := renderHTMLDescription("<ul><li>apples</li><li>pears</li></ul>", 80, nil, false)
	if len(lines) != 2 {
		t.Fatalf("expected 2 bullet lines, got %#v", lines)
	}
	for _, ln := range lines {
		if !strings.HasPrefix(ln, "•") {
			t.Errorf("missing bullet prefix: %q", ln)
		}
	}
}

func TestRenderHTMLDescription_LinkHyperlink(t *testing.T) {
	lines := renderHTMLDescription(`<a href="https://example.test/x">site</a>`, 80, nil, false)
	out := strings.Join(lines, "\n")
	// The OSC 8 target carries the href; the visible label survives once ANSI
	// styling (per-grapheme underline resets) is stripped.
	if !strings.Contains(out, "https://example.test/x") {
		t.Errorf("link target missing: %q", out)
	}
	if !strings.Contains(stripANSI(out), "site") {
		t.Errorf("link label missing: %q", out)
	}
	if !strings.Contains(out, "\x1b]8;;") {
		t.Errorf("expected OSC 8 sequence, got %q", out)
	}
}

func TestRenderHTMLDescription_Wrapping(t *testing.T) {
	lines := renderHTMLDescription("<p>alpha beta gamma delta</p>", 11, nil, false)
	for _, ln := range lines {
		if w := len([]rune(stripANSI(ln))); w > 11 {
			t.Errorf("line exceeds width 11: %q (%d)", ln, w)
		}
	}
	if len(lines) < 2 {
		t.Errorf("expected wrapped output, got %#v", lines)
	}
}

func TestRenderHTMLDescription_BareURLLinkified(t *testing.T) {
	// A URL in the text but outside any <a> tag should still be clickable.
	out := strings.Join(renderHTMLDescription(
		"<p>Join here: https://meet.test/abc before noon.</p>", 80, nil, false), "\n")
	if !strings.Contains(out, "\x1b]8;;https://meet.test/abc\x1b\\") {
		t.Errorf("bare URL not linkified: %q", out)
	}
	// Surrounding words stay intact.
	if !strings.Contains(stripANSI(out), "Join here:") || !strings.Contains(stripANSI(out), "before noon.") {
		t.Errorf("surrounding text lost: %q", stripANSI(out))
	}
}

func TestRenderHTMLDescription_BareURLTrailingPunctuation(t *testing.T) {
	// Trailing punctuation must not be swallowed into the link target.
	out := strings.Join(renderHTMLDescription("<p>see https://x.test/p.</p>", 80, nil, false), "\n")
	if !strings.Contains(out, "\x1b]8;;https://x.test/p\x1b\\") {
		t.Errorf("expected trimmed URL target: %q", out)
	}
}

func TestRenderHTMLDescription_RejectsUnsafeHref(t *testing.T) {
	// javascript: and control-byte URLs must never become OSC 8 hyperlinks.
	for _, href := range []string{
		"javascript:alert(1)",
		"https://evil.test/\x1b]0;pwned\x07",
		"https://evil.test/\x07bell",
	} {
		out := strings.Join(renderHTMLDescription(`<a href="`+href+`">x</a>`, 80, nil, true), "\n")
		if strings.Contains(out, "\x1b]8;;") {
			t.Errorf("href %q produced an OSC 8 hyperlink: %q", href, out)
		}
	}
}

func TestRenderHTMLDescription_ZonesAddMouseMark(t *testing.T) {
	withZones := strings.Join(renderHTMLDescription(`<a href="https://ok.test">x</a>`, 80, nil, true), "\n")
	noZones := strings.Join(renderHTMLDescription(`<a href="https://ok.test">x</a>`, 80, nil, false), "\n")
	// Both render the OSC 8 hyperlink; only the interactive variant adds a
	// clickable mouse zone (swept by the host dialog).
	if !strings.Contains(withZones, "\x1b]8;;") || !strings.Contains(noZones, "\x1b]8;;") {
		t.Fatalf("expected OSC 8 in both: zones=%q noZones=%q", withZones, noZones)
	}
	zoneClean := mouseSweep(withZones)
	if !strings.Contains(zoneClean, "https://ok.test") {
		t.Errorf("mouse zone target missing after sweep: %q", withZones)
	}
}

func TestDescriptionLines_PlainFallback(t *testing.T) {
	lines := descriptionLines("line one\nline two", 80, nil, false)
	if len(lines) != 2 || lines[0] != "line one" || lines[1] != "line two" {
		t.Fatalf("plain text should keep newlines: %#v", lines)
	}
}
