package tui

import (
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// linkZonePrefix tags mouseMark zones that point to a URL the user can open.
// handleMouse strips the prefix and hands the rest to openURLCmd.
const linkZonePrefix = "link:"

// urlPattern matches http/https URLs in plain text. The character class
// excludes whitespace and common trailing-punctuation/bracket pairs so the
// match stops at sentence boundaries instead of swallowing them.
var urlPattern = regexp.MustCompile(`https?://[^\s<>"'\x60\{\}]+`)

// trimURLTail trims punctuation a regex match commonly over-captures, e.g.
// "(see https://example.com.)" — the trailing period and paren are not part
// of the URL. We strip greedy trailing characters but keep a closing paren
// when one was opened inside the URL (Wikipedia-style links).
func trimURLTail(u string) string {
	for len(u) > 0 {
		last := u[len(u)-1]
		switch last {
		case '.', ',', ';', ':', '!', '?', '"', '\'', '`':
			u = u[:len(u)-1]
		case ')':
			if strings.Count(u, "(") >= strings.Count(u, ")") {
				return u
			}
			u = u[:len(u)-1]
		case ']':
			if strings.Count(u, "[") >= strings.Count(u, "]") {
				return u
			}
			u = u[:len(u)-1]
		default:
			return u
		}
	}
	return u
}

// hyperlink wraps text in an OSC 8 escape sequence so terminals that honor
// it can open the URL on click — even when the TUI captures mouse events.
// Falls back to plain text rendering on terminals that ignore OSC 8.
func hyperlink(url, text string) string {
	return "\x1b]8;;" + url + "\x1b\\" + text + "\x1b]8;;\x1b\\"
}

// linkifyText finds URLs in a single line of plain text and wraps each one
// with an OSC 8 hyperlink and a mouseMark zone tagged with linkZonePrefix.
// Input must not contain newlines — call it on each wrapped line.
func linkifyText(s string) string {
	if s == "" || !strings.Contains(s, "http") {
		return s
	}
	idxs := urlPattern.FindAllStringIndex(s, -1)
	if len(idxs) == 0 {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 64*len(idxs))
	last := 0
	for _, m := range idxs {
		start, end := m[0], m[1]
		raw := s[start:end]
		trimmed := trimURLTail(raw)
		if trimmed == "" {
			b.WriteString(s[last:end])
			last = end
			continue
		}
		tailLen := len(raw) - len(trimmed)
		b.WriteString(s[last:start])
		b.WriteString(mouseMark(linkZonePrefix+trimmed, hyperlink(trimmed, trimmed)))
		if tailLen > 0 {
			b.WriteString(raw[len(trimmed):])
		}
		last = end
	}
	b.WriteString(s[last:])
	return b.String()
}

// renderLinkValue wraps a known URL value (e.g., ev.URL or ev.ConferenceURI)
// as a clickable link. The visible text is truncated to fit available width
// while the click/OSC 8 target stays the full URL.
func renderLinkValue(url string, available int) string {
	if url == "" {
		return ""
	}
	visible := url
	if available > 0 {
		r := []rune(url)
		if len(r) > available {
			cut := max(available-1, 1)
			visible = string(r[:cut]) + "…"
		}
	}
	return mouseMark(linkZonePrefix+url, hyperlink(url, visible))
}

// openURLCmd returns a Bubble Tea command that opens the URL with the
// platform's default handler. Only http/https URLs are accepted; anything
// else is dropped silently so a malformed zone name can't be turned into
// arbitrary command execution.
func openURLCmd(url string) tea.Cmd {
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return nil
	}
	return func() tea.Msg {
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.Command("open", url)
		case "windows":
			cmd = exec.Command("cmd", "/c", "start", "", url)
		default:
			cmd = exec.Command("xdg-open", url)
		}
		_ = cmd.Start()
		return nil
	}
}
