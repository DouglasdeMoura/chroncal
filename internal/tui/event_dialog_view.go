package tui

import (
	"strings"

	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/model"
)

func (m EventDialogModel) View() string { return m.shell.View() }

func (m EventDialogModel) labelWidth() int {
	if m.shell.isNarrow() {
		return 7
	}
	return 10
}

func (m EventDialogModel) detailWidth() int {
	boxW, _ := m.shell.BoxSize()
	if boxW == 0 {
		return 40
	}
	innerW := max(boxW-5, 10)
	if m.shell.isNarrow() {
		return innerW
	}
	return detailColumnWidth(innerW)
}

// listRowWidth mirrors the shell's list-column math so a selected row can
// extend its reverse-video background to the right edge.
func (m EventDialogModel) listRowWidth() int {
	boxW, _ := m.shell.BoxSize()
	if boxW == 0 {
		return 0
	}
	innerW := max(boxW-5, 10)
	if m.shell.isNarrow() {
		return innerW
	}
	return listColumnWidth(innerW)
}

// refresh rebuilds the shell's rows, detail lines, actions, and help row
// from the current day, events, and focus state.
func (m EventDialogModel) refresh() EventDialogModel {
	rows := make([]string, len(m.events))
	sel := m.shell.Selected()
	listFocused := m.shell.FocusZone() == ListZoneList
	rowW := m.listRowWidth()
	selBG := m.shell.SelectedColor()
	// Reuse pre-formatted labels (built once per events change in
	// buildEventLabels). Only the selected row gets re-styled per
	// keystroke; the other rows are direct pointer copies.
	if len(m.eventLabels) != len(m.events) {
		m = m.buildEventLabels()
	}
	copy(rows, m.eventLabels)
	if sel >= 0 && sel < len(rows) {
		label := rows[sel]
		if rowW > 0 {
			label = truncateTo(label, rowW)
		}
		style := lipgloss.NewStyle()
		switch {
		case listFocused:
			style = style.Reverse(true).Bold(true)
		case selBG != nil:
			style = style.Background(selBG).Foreground(activeTheme.SelectedText)
		}
		if rowW > 0 {
			style = style.Width(rowW)
		}
		rows[sel] = style.Render(label)
	}
	m.shell = m.shell.SetRows(rows)

	w := m.detailWidth()
	if ev, ok := m.selectedEvent(); ok {
		cal := m.calendars[ev.CalendarID]
		rw := eventMetaURLRewriter(cal)
		rsvpLine := ""
		if rsvp := m.rsvpActions(); len(rsvp) > 0 {
			att, _ := m.userAttendee()
			rsvpLine = m.renderRSVPLine(att, rsvp, w)
		}
		lines, _ := eventDetailLines(ev, cal, w, m.labelWidth(), rsvpLine, rw)
		m.shell = m.shell.SetDetailTitle(ev.Title).SetDetailLines(lines)
	} else {
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	}

	if len(m.events) == 0 {
		faint := lipgloss.NewStyle().Faint(true)
		m.shell = m.shell.SetEmptyList("", []string{faint.Render("No events on this day.")})
		m.shell = m.shell.SetActions(nil)
	} else {
		m.shell = m.shell.SetActions(m.actions())
	}

	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

// renderRSVPLine renders the "Your RSVP  [Yes] [No] [Maybe]" row with the
// focused button highlighted when the RSVP zone owns focus.
func (m EventDialogModel) renderRSVPLine(att model.Attendee, rsvp []dialogAction, w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	lw := m.labelWidth()

	label := "Your RSVP"
	padded := strings.Repeat(" ", max(lw-len(label), 0)) + label + "  "

	fixedW := rsvpMaxLabelWidth()
	parts := make([]string, 0, len(rsvp))
	for i, a := range rsvp {
		l := rsvpButtonLabel(a.label, att.RSVPStatus)
		if pad := fixedW - lipgloss.Width(l); pad > 0 {
			leftPad := pad / 2
			right := pad - leftPad
			l = strings.Repeat(" ", leftPad) + l + strings.Repeat(" ", right)
		}
		parts = append(parts, DefaultButtonStyles().Normal.Render(l, m.rsvpFocused && i == m.focusedRSVP))
	}
	value := strings.Join(parts, " ")

	return truncateTo(faint.Render(padded)+value, w)
}
