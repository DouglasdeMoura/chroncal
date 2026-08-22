package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// PaletteField
// ---------------------------------------------------------------------------

// PaletteField is a FormField that cycles through color swatches with
// left/right arrows. The selected swatch is wrapped in brackets.
type PaletteField struct {
	swatches []string
	selected int // -1 for custom/off-palette
	focused  bool
}

func NewPaletteField(swatches []string, selected int) *PaletteField {
	return &PaletteField{
		swatches: swatches,
		selected: selected,
	}
}

func (f *PaletteField) Selected() int     { return f.selected }
func (f *PaletteField) SetSelected(i int) { f.selected = i }

func (f *PaletteField) Value() string {
	if f.selected >= 0 && f.selected < len(f.swatches) {
		return f.swatches[f.selected]
	}
	return ""
}

func (f *PaletteField) Update(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		n := len(f.swatches)
		switch msg.String() {
		case "left", "h":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx > 0 {
				idx--
			}
			f.selected = idx
		case "right", "l":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx < n-1 {
				idx++
			}
			f.selected = idx
		}
	}
	return nil
}

func (f *PaletteField) View() string {
	dot := func(c string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(Glyphs["dot"])
	}
	br := lipgloss.NewStyle().Bold(true)
	parts := make([]string, 0, len(f.swatches))
	for i, c := range f.swatches {
		target := "palette:" + strconv.Itoa(i)
		if i == f.selected {
			parts = append(parts, mouseMark(target, br.Render("[")+dot(c)+br.Render("]")))
		} else {
			parts = append(parts, mouseMark(target, dot(c)))
		}
	}
	out := strings.Join(parts, " ")
	// Reserve the same width whether a swatch is selected or not so the
	// trailing hex input keeps a fixed column.
	if f.selected < 0 {
		out += "  "
	}
	return out
}

func (f *PaletteField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *PaletteField) Blur() {
	f.focused = false
}

func (f *PaletteField) SetWidth(int)      {}
func (f *PaletteField) IsFocusable() bool { return true }
