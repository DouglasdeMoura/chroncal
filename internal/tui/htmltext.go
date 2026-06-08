package tui

import (
	"regexp"
	"strings"

	lipgloss "charm.land/lipgloss/v2"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// htmlTagPattern detects HTML markup worth handing to the parser. CalDAV
// servers (Google, Outlook) routinely store event descriptions as HTML;
// plain-text descriptions should skip the parser and keep their literal
// newlines.
var htmlTagPattern = regexp.MustCompile(`(?i)<(/?)(a|b|i|u|p|br|hr|div|span|strong|em|ul|ol|li|h[1-6]|table|tr|td|th|tbody|thead|blockquote|pre|code|img|font|sub|sup|small|big|s|strike|del|ins)(\s|/?>)`)

// looksLikeHTML reports whether s appears to contain HTML markup.
func looksLikeHTML(s string) bool {
	return htmlTagPattern.MatchString(s)
}

// descriptionLines renders a possibly-HTML description into terminal lines
// wrapped to width w. HTML is converted to styled terminal text (bold,
// italic, underline, bullets, links); plain text keeps its newlines.
//
// interactive enables URL handling for read-only surfaces that sweep their
// own mouse zones (the event view dialog): plain-text URLs are linkified and
// HTML links become clickable mouseMark zones. Non-interactive callers
// (event day dialog, trash) still get OSC 8 hyperlinks for HTML anchors —
// clickable in terminals that honor OSC 8 — but no mouseMark zones, which
// would leak markers on surfaces that don't sweep them.
func descriptionLines(desc string, w int, rw urlRewriter, interactive bool) []string {
	if looksLikeHTML(desc) {
		if lines := renderHTMLDescription(desc, w, rw, interactive); lines != nil {
			return lines
		}
	}
	var out []string
	for raw := range strings.SplitSeq(desc, "\n") {
		wrapped := wrapLine(raw, w)
		if interactive {
			for _, ln := range wrapped {
				out = append(out, linkifyText(ln, rw))
			}
		} else {
			out = append(out, wrapped...)
		}
	}
	return out
}

// htmlInline carries the active inline styling as the renderer walks the DOM.
type htmlInline struct {
	bold      bool
	italic    bool
	underline bool
	strike    bool
	pre       bool
	href      string
}

// htmlWord is a single whitespace-delimited token plus the style it inherits.
type htmlWord struct {
	text  string
	style htmlInline
}

// htmlRenderer flattens an HTML tree into wrapped, styled terminal lines.
type htmlRenderer struct {
	width int
	rw    urlRewriter
	zones bool
	out   []string
	cur   []htmlWord
}

// renderHTMLDescription parses s as an HTML fragment and renders it to styled
// terminal lines wrapped to width w. Returns nil when the input can't be
// parsed or w is non-positive, so callers can fall back to plain rendering.
func renderHTMLDescription(s string, w int, rw urlRewriter, zones bool) []string {
	if w <= 0 {
		return nil
	}
	ctx := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(s), ctx)
	if err != nil {
		return nil
	}
	r := &htmlRenderer{width: w, rw: rw, zones: zones}
	for _, n := range nodes {
		r.walk(n, htmlInline{})
	}
	r.flushCur()
	for len(r.out) > 0 && r.out[0] == "" {
		r.out = r.out[1:]
	}
	for len(r.out) > 0 && r.out[len(r.out)-1] == "" {
		r.out = r.out[:len(r.out)-1]
	}
	if r.out == nil {
		return []string{}
	}
	return r.out
}

func (r *htmlRenderer) walk(n *html.Node, st htmlInline) {
	switch n.Type {
	case html.TextNode:
		r.text(n.Data, st)
	case html.ElementNode:
		r.element(n, st)
	default:
		r.children(n, st)
	}
}

func (r *htmlRenderer) children(n *html.Node, st htmlInline) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		r.walk(c, st)
	}
}

func (r *htmlRenderer) element(n *html.Node, st htmlInline) {
	switch n.Data {
	case "br":
		r.lineBreak()
		return
	case "hr":
		r.flushCur()
		r.out = append(r.out, strings.Repeat("─", min(r.width, 24)))
		return
	case "img":
		if alt := strings.TrimSpace(htmlAttr(n, "alt")); alt != "" {
			r.text("["+alt+"]", st)
		}
		return
	case "head", "style", "script", "title":
		return // non-visible content
	}

	block := htmlBlock(n.Data)
	if block {
		r.flushCur()
	}

	ns := st
	switch n.Data {
	case "b", "strong", "h1", "h2", "h3", "h4", "h5", "h6", "th":
		ns.bold = true
	case "i", "em", "cite", "var", "dfn":
		ns.italic = true
	case "u", "ins":
		ns.underline = true
	case "s", "strike", "del":
		ns.strike = true
	case "pre":
		ns.pre = true
	case "a":
		if href := strings.TrimSpace(htmlAttr(n, "href")); href != "" {
			ns.href = href
			ns.underline = true
		}
	}

	if n.Data == "li" {
		r.cur = append(r.cur, htmlWord{text: "•", style: st})
	}

	r.children(n, ns)

	if block {
		r.flushCur()
	}
}

// text appends a run of text as whitespace-delimited words. Outside <pre>,
// HTML collapses runs of whitespace to a single space, which strings.Fields
// reproduces. Inside <pre>, newlines become line breaks and each line is kept
// as a single token so its internal spacing survives.
func (r *htmlRenderer) text(s string, st htmlInline) {
	if s == "" {
		return
	}
	if st.pre {
		for i, ln := range strings.Split(s, "\n") {
			if i > 0 {
				r.lineBreak()
			}
			if ln != "" {
				r.cur = append(r.cur, htmlWord{text: ln, style: st})
			}
		}
		return
	}
	for _, f := range strings.Fields(s) {
		r.cur = append(r.cur, htmlWord{text: f, style: st})
	}
}

// lineBreak ends the current line. An empty pending line (e.g. consecutive
// <br>) becomes a blank output line so paragraph spacing survives.
func (r *htmlRenderer) lineBreak() {
	if len(r.cur) == 0 {
		r.out = append(r.out, "")
		return
	}
	r.flushCur()
}

// flushCur greedily word-wraps the pending words to width and appends the
// resulting styled lines to the output.
func (r *htmlRenderer) flushCur() {
	if len(r.cur) == 0 {
		return
	}
	words := r.cur
	r.cur = nil

	var line strings.Builder
	lineW := 0
	for _, wd := range words {
		for _, seg := range r.splitWord(wd) {
			sw := lipgloss.Width(seg.text)
			if lineW > 0 && lineW+1+sw > r.width {
				r.out = append(r.out, line.String())
				line.Reset()
				lineW = 0
			}
			if lineW > 0 {
				line.WriteByte(' ')
				lineW++
			}
			line.WriteString(r.styleWord(seg))
			lineW += sw
		}
	}
	if line.Len() > 0 {
		r.out = append(r.out, line.String())
	}
}

// splitWord hard-breaks a single token wider than the wrap width into
// width-sized chunks so an over-long word can't overflow the dialog.
func (r *htmlRenderer) splitWord(wd htmlWord) []htmlWord {
	if lipgloss.Width(wd.text) <= r.width {
		return []htmlWord{wd}
	}
	var out []htmlWord
	runes := []rune(wd.text)
	for len(runes) > 0 {
		n, width := 0, 0
		for n < len(runes) {
			cw := lipgloss.Width(string(runes[n]))
			if width+cw > r.width && n > 0 {
				break
			}
			width += cw
			n++
		}
		out = append(out, htmlWord{text: string(runes[:n]), style: wd.style})
		runes = runes[n:]
	}
	return out
}

// styleWord renders one token with its inline styling and, for anchors, wraps
// it as an OSC 8 hyperlink (plus a clickable mouseMark zone when zones are on).
func (r *htmlRenderer) styleWord(wd htmlWord) string {
	text := wd.text
	st := lipgloss.NewStyle()
	styled := false
	if wd.style.bold {
		st, styled = st.Bold(true), true
	}
	if wd.style.italic {
		st, styled = st.Italic(true), true
	}
	if wd.style.underline {
		st, styled = st.Underline(true), true
	}
	if wd.style.strike {
		st, styled = st.Strikethrough(true), true
	}
	if styled {
		text = st.Render(text)
	}
	if wd.style.href != "" {
		target := r.rw.rewrite(wd.style.href)
		if isSafeHTTPURL(target) {
			text = hyperlink(target, text)
			if r.zones {
				text = mouseMark(linkZonePrefix+target, text)
			}
		}
	}
	return text
}

// isSafeHTTPURL reports whether s is an http(s) URL free of control bytes.
// HTML hrefs are attacker-controlled — descriptions sync from shared
// calendars — and a raw ESC or BEL inside the URL would terminate the OSC 8
// escape early, letting a description inject arbitrary terminal sequences.
func isSafeHTTPURL(s string) bool {
	if !strings.HasPrefix(s, "http://") && !strings.HasPrefix(s, "https://") {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

// htmlAttr returns the value of the named attribute, or "" when absent.
func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// htmlBlock reports whether a tag forces a line break before and after its
// content (block-level), as opposed to flowing inline.
func htmlBlock(name string) bool {
	switch name {
	case "p", "div", "li", "ul", "ol", "table", "tr", "thead", "tbody",
		"h1", "h2", "h3", "h4", "h5", "h6", "blockquote", "pre", "section",
		"article", "header", "footer", "dl", "dt", "dd", "figure":
		return true
	}
	return false
}
