package tui

import (
	"strings"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// copyEventDetailsCmd writes event details to the system clipboard via OSC 52
// and the native clipboard, matching Chroncal Bar's `p` shortcut.
func copyEventDetailsCmd(text string) tea.Cmd {
	if text == "" {
		return nil
	}
	return tea.Batch(
		tea.SetClipboard(text),
		func() tea.Msg {
			_ = clipboard.WriteAll(text)
			return nil
		},
	)
}

func formatEventDetailsText(ev event.Event, calendarName string) string {
	title := strings.TrimSpace(ev.Title)
	if title == "" {
		title = "Untitled"
	}
	lines := []string{title}
	if date := formatEventDateRange(ev); date != "" {
		lines = append(lines, date)
	}
	if ev.AllDay {
		lines = append(lines, "All day")
	} else if rng := formatEventTimeRange(ev); rng != "" {
		lines = append(lines, rng)
	}
	if name := strings.TrimSpace(calendarName); name != "" {
		lines = append(lines, name)
	}
	if loc := strings.TrimSpace(ev.Location); loc != "" {
		lines = append(lines, loc)
	}
	if desc := strings.TrimSpace(ev.Description); desc != "" {
		lines = append(lines, desc)
	}
	for _, att := range ev.Attendees {
		lines = append(lines, formatAttendeeCopy(att))
	}
	if url := strings.TrimSpace(ev.URL); url != "" {
		lines = append(lines, url)
	}
	if conf := strings.TrimSpace(ev.ConferenceURI); conf != "" && conf != strings.TrimSpace(ev.URL) {
		lines = append(lines, conf)
	}
	return strings.Join(lines, "\n")
}

func formatAttendeeCopy(att model.Attendee) string {
	identity := strings.TrimSpace(att.Name)
	email := strings.TrimSpace(att.Email)
	if identity == "" {
		identity = email
	}
	if identity == "" {
		identity = "Guest"
	}
	out := identity
	if email != "" && strings.TrimSpace(att.Name) != "" {
		out += " <" + email + ">"
	}
	return out + " · " + formatRSVPCopy(att.RSVPStatus)
}

func formatRSVPCopy(status string) string {
	switch strings.ToUpper(strings.TrimSpace(status)) {
	case "ACCEPTED":
		return "Accepted"
	case "DECLINED":
		return "Declined"
	case "TENTATIVE":
		return "Tentative"
	case "NEEDS-ACTION", "":
		return "No response"
	default:
		// A server can send any PARTSTAT value, so the first character
		// can take more than one byte. Split on the first rune. A byte
		// split would cut a multi-byte character in half and render a
		// replacement character.
		s := strings.ToLower(strings.TrimSpace(status))
		first, size := utf8.DecodeRuneInString(s)
		if size == 0 {
			return s
		}
		return strings.ToUpper(string(first)) + s[size:]
	}
}
