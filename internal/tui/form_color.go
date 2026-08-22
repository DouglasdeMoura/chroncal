package tui

import (
	"image/color"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// ColorField
// ---------------------------------------------------------------------------

// ColorField composes a PaletteField and a HexColorField on a single row.
// Tab cycles from palette to hex before it leaves the field. Changes to
// either sub-field are mirrored to the other so the preview stays in sync.
type ColorField struct {
	palette  *PaletteField
	hex      *HexColorField
	subFocus int // 0 = palette, 1 = hex
	focused  bool
}

// NewColorField builds a composite palette+hex control pre-seeded with the
// given hex value.
func NewColorField(swatches []string, hex string, dim color.Color) *ColorField {
	idx := -1
	for i, c := range swatches {
		if strings.EqualFold(strings.TrimSpace(hex), c) {
			idx = i
			break
		}
	}
	p := NewPaletteField(swatches, idx)
	h := NewHexColorField("#rrggbb", dim)
	h.SetValue(hex)
	h.input.SetCharLimit(7)
	h.SetPaletteIdx(idx)
	return &ColorField{palette: p, hex: h}
}

// Value returns the current hex string.
func (f *ColorField) Value() string { return f.hex.Value() }

// Validate delegates to the hex field.
func (f *ColorField) Validate() string { return f.hex.Validate() }

func (f *ColorField) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch f.subFocus {
	case 0:
		cmd = f.palette.Update(msg)
		if v := f.palette.Value(); v != "" && v != f.hex.Value() {
			f.hex.SetValue(v)
		}
	default:
		cmd = f.hex.Update(msg)
	}
	f.syncFromHex()
	return cmd
}

// syncFromHex reconciles the palette index with the current hex value
// whenever the hex changes.
func (f *ColorField) syncFromHex() {
	h := strings.TrimSpace(f.hex.Value())
	idx := -1
	for i, c := range f.palette.swatches {
		if strings.EqualFold(c, h) {
			idx = i
			break
		}
	}
	f.palette.SetSelected(idx)
	f.hex.SetPaletteIdx(idx)
}

func (f *ColorField) View() string {
	// Keep the row compact. Skip the hex field's "(custom)" trailer.
	// Inside the composite the palette always neighbors the hex. The
	// preview dot is then sufficient. The label would push the row to wrap.
	base := f.hex.input.View()
	hexVal := strings.TrimSpace(f.hex.Value())
	hexRendered := base
	if hexRE.MatchString(hexVal) {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render(Glyphs["dot"])
		hexRendered = base + "  " + dot
	}
	return f.palette.View() + "  " + hexRendered
}

func (f *ColorField) Focus() tea.Cmd {
	f.focused = true
	f.subFocus = 0
	f.hex.Blur()
	return f.palette.Focus()
}

func (f *ColorField) Blur() {
	f.focused = false
	f.palette.Blur()
	f.hex.Blur()
}

func (f *ColorField) SetWidth(int)      {}
func (f *ColorField) IsFocusable() bool { return true }

func (f *ColorField) SubFocusNext() (bool, tea.Cmd) {
	if f.subFocus == 0 {
		f.palette.Blur()
		f.subFocus = 1
		return true, f.hex.Focus()
	}
	return false, nil
}

func (f *ColorField) SubFocusPrev() (bool, tea.Cmd) {
	if f.subFocus == 1 {
		f.hex.Blur()
		f.subFocus = 0
		return true, f.palette.Focus()
	}
	return false, nil
}

// HandleClickTarget routes palette/hex targets to the correct sub-field
// and moves subFocus to match the click.
func (f *ColorField) HandleClickTarget(target string) tea.Cmd {
	if strings.HasPrefix(target, "palette:") {
		f.hex.Blur()
		f.subFocus = 0
		_ = f.palette.Focus()
		return f.palette.Update(keyMsg(""))
	}
	f.palette.Blur()
	f.subFocus = 1
	return f.hex.Focus()
}
