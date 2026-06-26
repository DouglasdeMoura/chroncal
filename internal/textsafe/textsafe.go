package textsafe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// StripEscapes removes terminal escape sequences (CSI `ESC [ … final` and OSC
// `ESC ] … BEL|ST`) and lone ESC bytes, leaving every other byte untouched. It
// preserves whitespace and other runes, so callers that measure or wrap text
// can rely on byte/rune positions matching the visible output.
func StripEscapes(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		// No ESC byte: nothing to strip, avoid allocating a copy.
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			switch {
			case i+1 < len(s) && s[i+1] == '[':
				// CSI: runs until a final byte in 0x40–0x7e.
				i += 2
				for i < len(s) {
					c := s[i]
					i++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
			case i+1 < len(s) && s[i+1] == ']':
				// OSC: runs until BEL or ST (ESC \).
				i += 2
				for i < len(s) {
					if s[i] == 0x07 {
						i++
						break
					}
					if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
						i += 2
						break
					}
					i++
				}
			default:
				// Lone ESC: drop just the escape byte.
				i++
			}
			continue
		}

		b.WriteByte(s[i])
		i++
	}

	return b.String()
}

// Display removes terminal escape sequences and collapses control characters
// into safe plain text for human-facing terminal, notification, and email
// rendering.
func Display(s string) string {
	s = StripEscapes(s)

	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size == 1 {
			i++
			continue
		}
		switch {
		case r == '\r' || r == '\n' || r == '\t':
			b.WriteByte(' ')
		case unicode.IsControl(r):
			// Drop remaining control characters entirely.
		default:
			b.WriteRune(r)
		}
		i += size
	}

	return strings.Join(strings.Fields(b.String()), " ")
}
