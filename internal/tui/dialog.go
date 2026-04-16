package tui

import (
	"image/color"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// DialogStyles controls the visual appearance of a Dialog.
type DialogStyles struct {
	Title    lipgloss.Style
	Footer   lipgloss.Style
	Border   lipgloss.Border
	BorderFg color.Color
	PaddingY int // vertical padding (top and bottom)
	PaddingX int // horizontal padding (left and right)
}

// DefaultDialogStyles returns sensible defaults with a rounded border.
func DefaultDialogStyles() DialogStyles {
	return DialogStyles{
		Title:    lipgloss.NewStyle().Bold(true),
		Footer:   lipgloss.NewStyle(),
		Border:   lipgloss.RoundedBorder(),
		BorderFg: lipgloss.NoColor{},
		PaddingY: 1,
		PaddingX: 2,
	}
}

// Dialog is a reusable container that wraps arbitrary content in a
// bordered box with an optional title and footer, centered on screen.
//
// By default the box shrinks to fit its content. Use SetWidth to set a
// fixed inner width, or SetFullWidth to fill the terminal.
type Dialog struct {
	title      string
	footer     string
	styles     DialogStyles
	width      int // terminal width
	height     int // terminal height
	fixedWidth int // 0 = auto, -1 = full width, >0 = fixed inner width
}

// NewDialog creates a Dialog with the given title and styles.
func NewDialog(title string, styles DialogStyles) Dialog {
	return Dialog{title: title, styles: styles}
}

// SetTitle updates the dialog title.
func (d *Dialog) SetTitle(s string) { d.title = s }

// SetFooter sets the footer text displayed below the content.
func (d *Dialog) SetFooter(s string) { d.footer = s }

// SetWidth sets a fixed inner content width for the dialog box.
func (d *Dialog) SetWidth(w int) { d.fixedWidth = w }

// SetFullWidth makes the dialog box fill the terminal width.
func (d *Dialog) SetFullWidth() { d.fixedWidth = -1 }

// Update processes messages. Tracks terminal dimensions from WindowSizeMsg.
func (d Dialog) Update(msg tea.Msg) Dialog {
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		d.width = msg.Width
		d.height = msg.Height
	}
	return d
}

// ContentWidth returns the horizontal space available for content
// inside the dialog box. When a fixed width is set, returns that value.
// Otherwise returns the maximum content width based on terminal size.
// Returns 0 before the first WindowSizeMsg.
func (d Dialog) ContentWidth() int {
	if d.fixedWidth > 0 {
		// fixedWidth is the total box width set via lipgloss.Width().
		// Border (1 char each side) and padding are applied inside that width.
		w := d.fixedWidth - 2 - 2*d.styles.PaddingX
		if w < 0 {
			return 0
		}
		return w
	}
	if d.width == 0 {
		return 0
	}
	// border: 1 char each side; padding: PaddingX each side
	w := d.width - 2 - 2*d.styles.PaddingX
	if w < 0 {
		return 0
	}
	return w
}

// Box returns the bordered box without centering. Use this when
// composing multiple dialogs before centering them together.
func (d Dialog) Box(content string) string {
	var sections []string

	if d.title != "" {
		sections = append(sections, d.styles.Title.Render(d.title), "")
	}
	sections = append(sections, content)
	if d.footer != "" {
		cw := d.ContentWidth()
		if cw <= 0 {
			cw = lipgloss.Width(lipgloss.JoinVertical(lipgloss.Left, sections...))
		}
		centered := lipgloss.NewStyle().Width(cw).Align(lipgloss.Center).Render(d.styles.Footer.Render(d.footer))
		sections = append(sections, "", centered)
	}

	inner := lipgloss.JoinVertical(lipgloss.Left, sections...)

	padBottom := d.styles.PaddingY
	if d.footer != "" {
		padBottom = 0
	}

	box := lipgloss.NewStyle().
		Border(d.styles.Border).
		BorderForeground(d.styles.BorderFg).
		PaddingTop(d.styles.PaddingY).
		PaddingBottom(padBottom).
		PaddingLeft(d.styles.PaddingX).
		PaddingRight(d.styles.PaddingX)

	switch {
	case d.fixedWidth == -1:
		box = box.Width(d.ContentWidth())
	case d.fixedWidth > 0:
		box = box.Width(d.fixedWidth)
	}

	return box.Render(inner)
}

// Render wraps content in the dialog chrome and centers it on screen.
func (d Dialog) Render(content string) string {
	rendered := d.Box(content)
	if d.width > 0 && d.height > 0 {
		return lipgloss.Place(d.width, d.height, lipgloss.Center, lipgloss.Center, rendered)
	}
	return rendered
}
