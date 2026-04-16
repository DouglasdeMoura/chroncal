package tui

import (
	"fmt"
	"strings"

	"github.com/mattn/go-runewidth"
)

// ---------------------------------------------------------------------------
// ANSI lexer
// ---------------------------------------------------------------------------

type ansiTokenType int

const (
	ansiText ansiTokenType = iota
	ansiCSI
	ansiOSC
	ansiESC
	ansiEOF
)

type ansiToken struct {
	kind   ansiTokenType
	text   string
	params string // CSI parameter bytes
	final  byte   // CSI/ESC final byte
}

type ansiLexer struct {
	input string
	pos   int
}

func newAnsiLexer(input string) ansiLexer {
	return ansiLexer{input: input}
}

func (l *ansiLexer) next() ansiToken {
	if l.pos >= len(l.input) {
		return ansiToken{kind: ansiEOF}
	}
	if l.input[l.pos] == '\x1b' {
		return l.readEscape()
	}
	return l.readText()
}

func (l *ansiLexer) readText() ansiToken {
	start := l.pos
	if i := strings.IndexByte(l.input[l.pos:], '\x1b'); i >= 0 {
		l.pos += i
	} else {
		l.pos = len(l.input)
	}
	return ansiToken{kind: ansiText, text: l.input[start:l.pos]}
}

func (l *ansiLexer) readEscape() ansiToken {
	start := l.pos
	l.pos++ // consume ESC

	if l.pos >= len(l.input) {
		return ansiToken{kind: ansiText, text: l.input[start:l.pos]}
	}

	switch l.input[l.pos] {
	case '[':
		return l.readCSI(start)
	case ']':
		return l.readOSC(start)
	}

	final := l.input[l.pos]
	l.pos++
	return ansiToken{kind: ansiESC, text: l.input[start:l.pos], final: final}
}

func (l *ansiLexer) readOSC(start int) ansiToken {
	l.pos++ // consume ']'
	for l.pos < len(l.input) {
		b := l.input[l.pos]
		if b == '\x07' {
			l.pos++
			break
		}
		if b == '\x1b' && l.pos+1 < len(l.input) && l.input[l.pos+1] == '\\' {
			l.pos += 2
			break
		}
		l.pos++
	}
	return ansiToken{kind: ansiOSC, text: l.input[start:l.pos]}
}

func (l *ansiLexer) readCSI(start int) ansiToken {
	l.pos++ // consume '['
	paramStart := l.pos

	for l.pos < len(l.input) {
		b := l.input[l.pos]
		if (b >= 0x30 && b <= 0x3F) || (b >= 0x20 && b <= 0x2F) {
			l.pos++
		} else {
			break
		}
	}
	paramEnd := l.pos

	var final byte
	if l.pos < len(l.input) {
		b := l.input[l.pos]
		if b >= 0x40 && b <= 0x7E {
			final = b
			l.pos++
		}
	}

	return ansiToken{
		kind:   ansiCSI,
		text:   l.input[start:l.pos],
		params: l.input[paramStart:paramEnd],
		final:  final,
	}
}

func parseCSIParam(s string) int {
	n := 0
	for i := range len(s) {
		b := s[i]
		if b >= '0' && b <= '9' {
			n = n*10 + int(b-'0')
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Mouse tracker
// ---------------------------------------------------------------------------

type mouseZone struct {
	name           string
	startX, startY int
	endX, endY     int
}

func (z mouseZone) contains(x, y int) bool {
	return x >= z.startX && x <= z.endX && y >= z.startY && y <= z.endY
}

// mouseTracker tracks clickable regions in rendered content using zero-width
// ANSI markers. mouseMark wraps content with marker pairs during View,
// mouseSweep strips them and builds a coordinate map, and mouseResolve
// matches mouse clicks to the innermost marked region.
type mouseTracker struct {
	nextID int
	names  map[int]string
	zones  []mouseZone
}

var defaultMouseTracker = &mouseTracker{}

// mouseMark wraps content with mouse-tracking markers using the default tracker.
func mouseMark(name, content string) string {
	return defaultMouseTracker.mark(name, content)
}

// mouseSweep strips markers and records screen coordinates using the default tracker.
func mouseSweep(content string) string {
	return defaultMouseTracker.sweep(content)
}

// mouseResolve returns the innermost zone at (x, y) using the default tracker.
func mouseResolve(x, y int) string {
	return defaultMouseTracker.resolve(x, y)
}

// MouseSweep is the exported version of mouseSweep for use by parent models
// outside this package.
func MouseSweep(content string) string { return mouseSweep(content) }

// MouseResolve is the exported version of mouseResolve for use by parent models
// outside this package.
func MouseResolve(x, y int) string { return mouseResolve(x, y) }

func (mt *mouseTracker) mark(name, content string) string {
	id := mt.nextID
	mt.nextID++
	if mt.names == nil {
		mt.names = make(map[int]string)
	}
	mt.names[id] = name
	marker := fmt.Sprintf("\x1b[%dz", id)
	return marker + content + marker
}

// sweep scans rendered content, strips mouse-tracking markers, and records
// the screen coordinates of each marked region. Returns cleaned content
// suitable for screen rendering.
func (mt *mouseTracker) sweep(content string) string {
	mt.zones = mt.zones[:0]

	if len(mt.names) == 0 {
		mt.nextID = 0
		return content
	}

	type pending struct {
		name   string
		startX int
		startY int
	}

	open := make(map[int]pending)
	var zones []mouseZone

	var out strings.Builder
	out.Grow(len(content))

	row, col := 0, 0
	lexer := newAnsiLexer(content)

	for {
		tok := lexer.next()
		if tok.kind == ansiEOF {
			break
		}

		switch tok.kind {
		case ansiText:
			for _, r := range tok.text {
				if r == '\n' {
					out.WriteRune(r)
					row++
					col = 0
				} else {
					out.WriteRune(r)
					col += runewidth.RuneWidth(r)
				}
			}

		case ansiCSI:
			if tok.final == 'z' {
				id := parseCSIParam(tok.params)
				if name, ok := mt.names[id]; ok {
					if p, opened := open[id]; opened {
						zones = append(zones, mouseZone{
							name:   p.name,
							startX: p.startX, startY: p.startY,
							endX: col - 1, endY: row,
						})
						delete(open, id)
					} else {
						open[id] = pending{name: name, startX: col, startY: row}
					}
					continue
				}
			}
			out.WriteString(tok.text)

		case ansiOSC, ansiESC:
			out.WriteString(tok.text)
		}
	}

	mt.zones = zones
	mt.names = nil
	mt.nextID = 0

	return out.String()
}

// resolve returns the name of the innermost zone containing (x, y), or ""
// if no zone matches. Inner zones appear before outer zones in the list
// (their end markers are encountered first during sweep), so the first
// match is the most deeply nested.
func (mt *mouseTracker) resolve(x, y int) string {
	for _, z := range mt.zones {
		if z.contains(x, y) {
			return z.name
		}
	}
	return ""
}
