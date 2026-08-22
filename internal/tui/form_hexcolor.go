package tui

import (
	"image/color"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// HexColorField
// ---------------------------------------------------------------------------

var hexRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// HexColorField wraps a TextField and appends a live color preview dot
// and "(custom)" label when the value doesn't match any palette swatch.
type HexColorField struct {
	input      *TextField
	paletteIdx int // -1 when off-palette
	dimColor   color.Color
}

func NewHexColorField(placeholder string, dimColor color.Color) *HexColorField {
	f := &HexColorField{
		input:    NewTextField(placeholder),
		dimColor: dimColor,
	}
	f.input.SetFilter(func(k tea.Key) bool {
		if k.Text == "" {
			return true
		}
		return isHexInputAllowed(k.Text, f.input.Position(), f.input.Value())
	})
	return f
}

func (f *HexColorField) Value() string              { return f.input.Value() }
func (f *HexColorField) SetValue(v string)          { f.input.SetValue(v) }
func (f *HexColorField) SetPaletteIdx(idx int)      { f.paletteIdx = idx }
func (f *HexColorField) Update(msg tea.Msg) tea.Cmd { return f.input.Update(msg) }
func (f *HexColorField) Focus() tea.Cmd             { return f.input.Focus() }
func (f *HexColorField) Blur()                      { f.input.Blur() }
func (f *HexColorField) SetWidth(w int)             { f.input.SetWidth(9) } // #rrggbb + cursor + 1
func (f *HexColorField) IsFocusable() bool          { return true }

func (f *HexColorField) Validate() string {
	hexVal := strings.TrimSpace(f.input.Value())
	if hexVal == "" {
		return "" // emptiness is handled by Required
	}
	if !hexRE.MatchString(hexVal) {
		return "Color must be #rrggbb"
	}
	return ""
}

func (f *HexColorField) View() string {
	base := f.input.View()
	hexVal := strings.TrimSpace(f.input.Value())
	if !hexRE.MatchString(hexVal) {
		return base
	}
	dot := lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render(Glyphs["dot"])
	if f.paletteIdx < 0 {
		customLabel := lipgloss.NewStyle().Foreground(f.dimColor).Italic(true).Render("(custom)")
		return base + "  " + dot + "  " + customLabel
	}
	return base + "  " + dot
}

// isHexInputAllowed reports whether the printable text t can be inserted
// into the hex input at cursor position pos given the current value.
func isHexInputAllowed(t string, pos int, current string) bool {
	for _, r := range t {
		switch {
		case r == '#':
			if pos != 0 || strings.ContainsRune(current, '#') {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			// ok
		default:
			return false
		}
	}
	return true
}
