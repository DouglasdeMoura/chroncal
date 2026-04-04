package textsafe

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// Display removes terminal escape sequences and collapses control characters
// into safe plain text for human-facing terminal, notification, and email
// rendering.
func Display(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			switch {
			case i+1 < len(s) && s[i+1] == '[':
				i += 2
				for i < len(s) {
					c := s[i]
					i++
					if c >= 0x40 && c <= 0x7e {
						break
					}
				}
				continue
			case i+1 < len(s) && s[i+1] == ']':
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
				continue
			default:
				i++
				continue
			}
		}

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
