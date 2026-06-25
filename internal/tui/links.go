package tui

import (
	"context"
	"net/url"
	"os/exec"
	"regexp"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// linkZonePrefix tags mouseMark zones that point to a URL the user can open.
// handleMouse strips the prefix and hands the rest to openURLCmd.
const linkZonePrefix = "link:"

// urlRewriter transforms a URL before it becomes a click target. Returns the
// URL unchanged when no rewrite applies. Nil rewriters are a no-op.
type urlRewriter func(string) string

// rewrite applies rw when non-nil, otherwise returns raw.
func (rw urlRewriter) rewrite(raw string) string {
	if rw == nil {
		return raw
	}
	return rw(raw)
}

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
// Input must not contain newlines — call it on each wrapped line. The
// optional rewriter transforms the click target (e.g., to inject Google's
// authuser hint) without changing the visible URL text.
func linkifyText(s string, rw urlRewriter) string {
	return linkifyTextZoned(s, rw, true)
}

// linkifyTextZoned is linkifyText with control over mouse zones. When zones is
// false it emits OSC 8 hyperlinks only — clickable in terminals that honor
// OSC 8 — without the mouseMark markers, which would leak on surfaces that
// don't sweep them (the day and trash dialogs).
func linkifyTextZoned(s string, rw urlRewriter, zones bool) string {
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
		target := rw.rewrite(trimmed)
		b.WriteString(s[last:start])
		link := hyperlink(target, trimmed)
		if zones {
			link = mouseMark(linkZonePrefix+target, link)
		}
		b.WriteString(link)
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
// while the click/OSC 8 target stays the full URL. The optional rewriter
// rewrites the click target only — what the user sees stays the original.
func renderLinkValue(raw string, available int, rw urlRewriter) string {
	if raw == "" {
		return ""
	}
	visible := raw
	if available > 0 {
		r := []rune(raw)
		if len(r) > available {
			cut := max(available-1, 1)
			visible = string(r[:cut]) + "…"
		}
	}
	target := rw.rewrite(raw)
	return mouseMark(linkZonePrefix+target, hyperlink(target, visible))
}

// renderLinkifiedValue renders a free-text value that may contain URLs, sized
// to `available` cells. The value is truncated to width first, so the OSC 8 /
// mouse-zone escapes it emits are always complete (truncateTo cuts the plain
// text and stripANSI does not understand OSC 8, so linkifying before
// truncating could leave a hyperlink unterminated). Each URL keeps its FULL
// address as the click target even when its visible text is ellipsized by the
// truncation — so an overflowing embedded link still opens the right place,
// the same full-target guarantee renderLinkValue gives bare-URL fields. The
// optional rewriter transforms the click target only.
func renderLinkifiedValue(value string, available int, rw urlRewriter) string {
	if available <= 0 {
		return ""
	}
	visible := truncateTo(value, available)
	fullMatches := urlPattern.FindAllString(value, -1)
	idxs := urlPattern.FindAllStringIndex(visible, -1)
	if len(fullMatches) == 0 || len(idxs) == 0 {
		return visible
	}
	var b strings.Builder
	b.Grow(len(visible) + 64*len(idxs))
	last := 0
	for i, m := range idxs {
		start, end := m[0], m[1]
		seen := visible[start:end]
		b.WriteString(visible[last:start])
		last = end
		// trimURLTail leaves a truncation ellipsis in place but strips
		// over-captured punctuation, mirroring linkifyText.
		vis := trimURLTail(seen)
		if vis == "" {
			b.WriteString(seen)
			continue
		}
		// The i-th visible URL aligns with the i-th URL in the full value
		// (truncation only removes a suffix), so its full address is the
		// click target even when vis is an ellipsized prefix.
		target := vis
		if i < len(fullMatches) {
			if full := trimURLTail(fullMatches[i]); full != "" {
				target = full
			}
		}
		target = rw.rewrite(target)
		b.WriteString(mouseMark(linkZonePrefix+target, hyperlink(target, vis)))
		b.WriteString(seen[len(vis):]) // over-captured tail stays plain text
	}
	b.WriteString(visible[last:])
	return b.String()
}

// googleAuthuserRewriter returns a urlRewriter that appends authuser=<email>
// to URLs on Google services that honor it (Meet, Calendar, Docs, Drive,
// Mail). Returns nil when email is empty so callers can pass it through to
// the link helpers unconditionally. URLs that already have an authuser query
// param are passed through untouched.
func googleAuthuserRewriter(email string) urlRewriter {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil
	}
	return func(raw string) string {
		u, err := url.Parse(raw)
		if err != nil || u == nil || !isGoogleAuthuserHost(u.Host) {
			return raw
		}
		q := u.Query()
		if q.Get("authuser") != "" {
			return raw
		}
		q.Set("authuser", email)
		u.RawQuery = q.Encode()
		return u.String()
	}
}

// isGoogleAuthuserHost is the allowlist of Google hostnames where ?authuser=
// pre-selects an account. We allowlist rather than match *.google.com so a
// stray non-account-aware google.com URL (e.g., maps, fonts) doesn't pick up
// the hint.
func isGoogleAuthuserHost(host string) bool {
	host = strings.ToLower(host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	switch host {
	case "meet.google.com",
		"calendar.google.com",
		"docs.google.com",
		"drive.google.com",
		"mail.google.com":
		return true
	}
	return false
}

// isGoogleAccountServer reports whether a CalDAV server URL belongs to
// Google. Google's CalDAV endpoint lives under apidata.googleusercontent.com,
// so we match on that host to identify Google-linked calendars.
func isGoogleAccountServer(serverURL string) bool {
	if serverURL == "" {
		return false
	}
	u, err := url.Parse(serverURL)
	if err != nil || u == nil {
		return false
	}
	host := strings.ToLower(u.Host)
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	return host == "apidata.googleusercontent.com" || strings.HasSuffix(host, ".googleusercontent.com")
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
		// Fire-and-forget browser launch: there is no surrounding request
		// to inherit a deadline from, so Background is the honest context.
		ctx := context.Background()
		var cmd *exec.Cmd
		switch runtime.GOOS {
		case "darwin":
			cmd = exec.CommandContext(ctx, "open", url)
		case "windows":
			cmd = exec.CommandContext(ctx, "cmd", "/c", "start", "", url)
		default:
			cmd = exec.CommandContext(ctx, "xdg-open", url)
		}
		_ = cmd.Start()
		return nil
	}
}
