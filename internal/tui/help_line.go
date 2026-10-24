package tui

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	lipgloss "charm.land/lipgloss/v2"
)

// shortHelpLine renders bindings on one line of at most w cells. The help
// model of bubbles adds an item past its width when the ellipsis does not
// fit, and the line then wraps. This function drops items from the end
// until the line fits, and then adds the ellipsis when it fits.
func shortHelpLine(h help.Model, bindings []key.Binding, w int) string {
	if w <= 0 {
		return h.ShortHelpView(bindings)
	}
	h.SetWidth(w)
	for n := len(bindings); n > 0; n-- {
		line := h.ShortHelpView(bindings[:n])
		if lipgloss.Width(line) > w {
			continue
		}
		if n < len(bindings) {
			tail := " " + h.Styles.Ellipsis.Render(h.Ellipsis)
			if lipgloss.Width(line)+lipgloss.Width(tail) <= w {
				line += tail
			}
		}
		return line
	}
	return ""
}

// SetHelpFooter sets the footer to the short help of bindings. The line
// fits in the content width of the dialog, so it never wraps and breaks
// the frame (issue #881). Size the dialog before the call.
func (d *Dialog) SetHelpFooter(h help.Model, bindings []key.Binding) {
	d.SetFooter(shortHelpLine(h, bindings, d.ContentWidth()))
}
