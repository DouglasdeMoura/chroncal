package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/douglasdemoura/chroncal/internal/calendar"
)

type sidebar struct {
	calendars []calendar.Calendar
	visible   map[int64]bool
	selected  int
}

func newSidebar() sidebar {
	return sidebar{
		visible: make(map[int64]bool),
	}
}

func (s *sidebar) setCalendars(cals []calendar.Calendar) {
	s.calendars = cals
	for _, c := range cals {
		if _, ok := s.visible[c.ID]; !ok {
			s.visible[c.ID] = true
		}
	}
}

func (s *sidebar) toggle() {
	if len(s.calendars) == 0 {
		return
	}
	cal := s.calendars[s.selected]
	s.visible[cal.ID] = !s.visible[cal.ID]
}

func (s *sidebar) next() {
	if s.selected < len(s.calendars)-1 {
		s.selected++
	}
}

func (s *sidebar) prev() {
	if s.selected > 0 {
		s.selected--
	}
}

func (s *sidebar) isVisible(calID int64) bool {
	vis, ok := s.visible[calID]
	return !ok || vis
}

func (s sidebar) view(width int) string {
	var b strings.Builder

	header := titleStyle.Render("Calendars")
	b.WriteString(header)
	b.WriteString("\n\n")

	for i, cal := range s.calendars {
		check := "✓"
		if !s.visible[cal.ID] {
			check = " "
		}

		colorIdx := int(cal.ID-1) % len(CalendarColors)
		colorSwatch := lipgloss.NewStyle().
			Foreground(CalendarColors[colorIdx]).
			Render("●")

		prefix := "  "
		if i == s.selected {
			prefix = "▸ "
		}

		line := fmt.Sprintf("%s[%s] %s %s", prefix, check, colorSwatch, cal.Name)
		b.WriteString(line)
		b.WriteString("\n")
	}

	return sidebarStyle.Width(width).Render(b.String())
}
