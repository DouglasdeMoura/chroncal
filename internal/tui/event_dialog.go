package tui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// EventDialogClosedMsg is emitted when the dialog requests to close.
type EventDialogClosedMsg struct{}

type eventDialogKeyMap struct {
	Up    key.Binding
	Down  key.Binding
	Close key.Binding
}

func defaultEventDialogKeys() eventDialogKeyMap {
	return eventDialogKeyMap{
		Up:    key.NewBinding(key.WithKeys("up", "k")),
		Down:  key.NewBinding(key.WithKeys("down", "j")),
		Close: key.NewBinding(key.WithKeys("esc", "q")),
	}
}

// CalendarInfo holds the display-relevant fields of a calendar.
type CalendarInfo struct {
	Name  string
	Color string
}

// EventDialogModel shows a day's events in a two-column dialog: a list on
// the left and the selected event's details on the right. On narrow screens
// it switches to a stacked single-column layout.
type EventDialogModel struct {
	day       time.Time
	events    []event.Event
	calendars map[int64]CalendarInfo
	selected  int
	scroll    int
	keys      eventDialogKeyMap
	width     int
	height    int
}

const narrowThreshold = 90

func NewEventDialogModel(day time.Time, events []event.Event, calendars map[int64]CalendarInfo) EventDialogModel {
	slices.SortStableFunc(events, func(a, b event.Event) int {
		if a.AllDay != b.AllDay {
			if a.AllDay {
				return -1
			}
			return 1
		}
		return a.StartTime.Compare(b.StartTime)
	})
	return EventDialogModel{
		day:       day,
		events:    events,
		calendars: calendars,
		keys:      defaultEventDialogKeys(),
	}
}

func (m EventDialogModel) SetSize(w, h int) EventDialogModel {
	m.width = w
	m.height = h
	return m
}

func (m EventDialogModel) Update(msg tea.Msg) (EventDialogModel, tea.Cmd) {
	keyMsg, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Matches(keyMsg, m.keys.Close):
		return m, func() tea.Msg { return EventDialogClosedMsg{} }
	case key.Matches(keyMsg, m.keys.Up):
		if m.selected > 0 {
			m.selected--
		}
	case key.Matches(keyMsg, m.keys.Down):
		if m.selected < len(m.events)-1 {
			m.selected++
		}
	}
	return m, nil
}

func (m EventDialogModel) isNarrow() bool {
	return m.width < narrowThreshold
}

func (m EventDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 10)
	innerH := max(boxH-4, 6)

	title := lipgloss.NewStyle().
		Bold(true).
		Width(innerW).
		Render(m.day.Format("Monday, January 2, 2006"))

	help := lipgloss.NewStyle().
		Faint(true).
		Width(innerW).
		Render("↑/↓: navigate  ·  esc: close")

	bodyH := max(innerH-4, 3)

	var body string
	if m.isNarrow() {
		body = m.viewStacked(innerW, bodyH)
	} else {
		body = m.viewColumns(innerW, bodyH)
	}

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

func (m *EventDialogModel) viewColumns(innerW, bodyH int) string {
	listW := max(min(max(innerW/4, 18), innerW-24), 10)
	dividerW := 3
	detailsW := max(innerW-listW-dividerW, 10)

	m.adjustScroll(bodyH)
	list := m.renderList(listW, bodyH)
	divider := m.renderDivider(dividerW, bodyH)
	details := m.renderDetails(detailsW, bodyH)

	return lipgloss.JoinHorizontal(lipgloss.Top, list, divider, details)
}

func (m *EventDialogModel) viewStacked(innerW, bodyH int) string {
	listH := min(max(len(m.events)+1, 3), max(bodyH/3, 3))
	detailsH := max(bodyH-listH-1, 3)

	m.adjustScroll(listH)
	list := m.renderList(innerW, listH)
	sep := lipgloss.NewStyle().Faint(true).Width(innerW).
		Render(strings.Repeat("─", innerW))
	details := m.renderDetails(innerW, detailsH)

	return lipgloss.JoinVertical(lipgloss.Left, list, sep, details)
}

// BoxSize returns the rendered dialog's outer dimensions (w, h) so the caller
// can position it on screen.
func (m EventDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return m.boxSize()
}

func (m EventDialogModel) boxSize() (int, int) {
	if m.isNarrow() {
		boxW := max(m.width-4, 20)
		boxH := max(m.height-4, 14)
		return boxW, boxH
	}
	boxW := min(max(m.width*2/3, 50), m.width-2)
	boxH := min(max(m.height*2/3, 14), m.height-2)
	return boxW, boxH
}

func (m *EventDialogModel) adjustScroll(visibleH int) {
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	if m.selected >= m.scroll+visibleH {
		m.scroll = m.selected - visibleH + 1
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m EventDialogModel) renderList(w, h int) string {
	total := len(m.events)

	visibleStart := m.scroll
	visibleEnd := min(visibleStart+h, total)

	lines := make([]string, 0, h)
	for i := visibleStart; i < visibleEnd; i++ {
		ev := m.events[i]
		label := formatEventLabel(ev)
		label = truncateTo(label, w)
		style := lipgloss.NewStyle().Width(w)
		if i == m.selected {
			style = style.Reverse(true).Bold(true)
		}
		lines = append(lines, style.Render(label))
	}

	if total > h {
		indicator := fmt.Sprintf(" %d/%d ", m.selected+1, total)
		arrows := ""
		if m.scroll > 0 {
			arrows += "▲"
		}
		if visibleEnd < total {
			if arrows != "" {
				arrows += " "
			}
			arrows += "▼"
		}
		if arrows != "" {
			indicator += arrows + " "
		}
		indicator = truncateTo(indicator, w)

		if len(lines) >= h {
			lines[h-1] = lipgloss.NewStyle().Width(w).Faint(true).Render(indicator)
		} else {
			lines = append(lines, lipgloss.NewStyle().Width(w).Faint(true).Render(indicator))
		}
	}

	return padLines(lines, w, h)
}

func (m EventDialogModel) renderDivider(w, h int) string {
	bar := lipgloss.NewStyle().Faint(true).Render("│")
	pad := strings.Repeat(" ", (w-1)/2)
	rest := strings.Repeat(" ", w-len(pad)-1)
	line := pad + bar + rest
	lines := make([]string, h)
	for i := range lines {
		lines[i] = line
	}
	return strings.Join(lines, "\n")
}

func (m EventDialogModel) labelWidth() int {
	if m.isNarrow() {
		return 7
	}
	return 10
}

func (m EventDialogModel) renderDetails(w, h int) string {
	if len(m.events) == 0 || m.selected < 0 || m.selected >= len(m.events) {
		return padLines(nil, w, h)
	}
	ev := m.events[m.selected]

	faint := lipgloss.NewStyle().Faint(true)
	bold := lipgloss.NewStyle().Bold(true)
	lw := m.labelWidth()

	var lines []string
	lines = append(lines, truncateTo(bold.Render(ev.Title), w))
	lines = append(lines, "")

	lines = append(lines, detailLine(faint, "When", formatWhen(ev), lw, w))

	dur := formatDuration(ev)
	if dur != "" {
		lines = append(lines, detailLine(faint, "Duration", dur, lw, w))
	}

	if cal, ok := m.calendars[ev.CalendarID]; ok && cal.Name != "" {
		dot := "●"
		if cal.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("●")
		}
		lines = append(lines, detailLine(faint, "Cal", dot+" "+cal.Name, lw, w))
	}

	if ev.Location != "" {
		lines = append(lines, detailLine(faint, "Where", ev.Location, lw, w))
	}
	if ev.Status != "" {
		lines = append(lines, detailLine(faint, "Status", ev.Status, lw, w))
	}
	if ev.Categories != "" {
		lines = append(lines, detailLine(faint, "Tags", ev.Categories, lw, w))
	}
	if ev.URL != "" {
		lines = append(lines, detailLine(faint, "URL", ev.URL, lw, w))
	}

	if len(ev.Attendees) > 0 {
		lines = append(lines, "")
		lines = append(lines, faint.Render("Attendees:"))
		for _, att := range ev.Attendees {
			lines = append(lines, truncateTo(formatAttendee(att), w))
		}
	}

	if len(ev.Alarms) > 0 {
		lines = append(lines, "")
		lines = append(lines, faint.Render("Reminders:"))
		for _, a := range ev.Alarms {
			lines = append(lines, truncateTo("  "+formatAlarm(a), w))
		}
	}

	if ev.Description != "" {
		lines = append(lines, "")
		for raw := range strings.SplitSeq(ev.Description, "\n") {
			lines = append(lines, wrapLine(raw, w)...)
		}
	}

	if len(lines) > h {
		lines = lines[:h]
	}
	return padLines(lines, w, h)
}

func detailLine(labelStyle lipgloss.Style, label, value string, lw, w int) string {
	padded := label + strings.Repeat(" ", max(lw-len(label), 1))
	return truncateTo(labelStyle.Render(padded)+value, w)
}

func formatEventLabel(ev event.Event) string {
	if ev.AllDay {
		return "• " + ev.Title
	}
	return ev.StartTime.Local().Format("15:04") + "  " + ev.Title
}

func formatWhen(ev event.Event) string {
	if ev.AllDay {
		return "all day"
	}
	start := ev.StartTime.Local()
	end := ev.EndTime.Local()
	if end.IsZero() {
		return start.Format("15:04")
	}
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return fmt.Sprintf("%s – %s", start.Format("15:04"), end.Format("15:04"))
	}
	return fmt.Sprintf("%s – %s", start.Format("Mon, Jan 2 15:04"), end.Format("Mon, Jan 2 15:04"))
}

func formatDuration(ev event.Event) string {
	if ev.AllDay || ev.EndTime.IsZero() {
		return ""
	}
	d := ev.EndTime.Sub(ev.StartTime)
	if d <= 0 {
		return ""
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m)
	case m == 0:
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	default:
		return fmt.Sprintf("%dh %dm", h, m)
	}
}

func formatAttendee(att model.Attendee) string {
	name := att.Name
	if name == "" {
		name = att.Email
	}
	status := ""
	switch strings.ToUpper(att.RSVPStatus) {
	case "ACCEPTED":
		status = " ✓"
	case "DECLINED":
		status = " ✗"
	case "TENTATIVE":
		status = " ?"
	}
	role := ""
	if att.Organizer {
		role = " (organizer)"
	}
	return "  " + name + status + role
}

func formatAlarm(a model.Alarm) string {
	tv := a.TriggerValue
	if tv == "" {
		return "at event time"
	}
	neg := strings.HasPrefix(tv, "-")
	raw := strings.TrimPrefix(tv, "-")
	raw = strings.TrimPrefix(raw, "+")
	raw = strings.TrimPrefix(raw, "P")
	raw = strings.TrimPrefix(raw, "T")

	var parts []string
	if n, rest, ok := parseLeadingInt(raw, 'W'); ok {
		parts = append(parts, pluralize(n, "week"))
		raw = rest
	}
	if n, rest, ok := parseLeadingInt(raw, 'D'); ok {
		parts = append(parts, pluralize(n, "day"))
		raw = rest
	}
	raw = strings.TrimPrefix(raw, "T")
	if n, rest, ok := parseLeadingInt(raw, 'H'); ok {
		parts = append(parts, pluralize(n, "hour"))
		raw = rest
	}
	if n, rest, ok := parseLeadingInt(raw, 'M'); ok {
		parts = append(parts, pluralize(n, "min"))
		raw = rest
	}
	if n, _, ok := parseLeadingInt(raw, 'S'); ok {
		parts = append(parts, pluralize(n, "sec"))
	}

	if len(parts) == 0 {
		return tv
	}
	desc := strings.Join(parts, " ")
	if neg {
		return desc + " before"
	}
	return desc + " after"
}

func parseLeadingInt(s string, suffix byte) (int, string, bool) {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 || i >= len(s) || s[i] != suffix {
		return 0, s, false
	}
	n := 0
	for _, c := range s[:i] {
		n = n*10 + int(c-'0')
	}
	return n, s[i+1:], true
}

func pluralize(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return fmt.Sprintf("%d %s", n, unit)
}

func truncateTo(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	if !strings.ContainsRune(s, '\x1b') {
		r := []rune(s)
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	plain := stripANSI(s)
	r := []rune(plain)
	if w == 1 {
		return "…"
	}
	if len(r) > w-1 {
		return string(r[:w-1]) + "…"
	}
	return plain
}

func stripANSI(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) {
				c := s[j]
				if c >= 0x40 && c <= 0x7e {
					j++
					break
				}
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func wrapLine(s string, w int) []string {
	if w <= 0 {
		return []string{""}
	}
	if s == "" {
		return []string{""}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return []string{""}
	}
	var out []string
	var cur string
	for _, word := range words {
		if cur == "" {
			if len([]rune(word)) > w {
				r := []rune(word)
				for len(r) > w {
					out = append(out, string(r[:w]))
					r = r[w:]
				}
				cur = string(r)
				continue
			}
			cur = word
			continue
		}
		if len([]rune(cur))+1+len([]rune(word)) > w {
			out = append(out, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func padLines(lines []string, w, h int) string {
	blank := strings.Repeat(" ", w)
	out := make([]string, 0, h)
	for _, l := range lines {
		if len(out) >= h {
			break
		}
		padded := lipgloss.NewStyle().Width(w).Render(l)
		out = append(out, padded)
	}
	for len(out) < h {
		out = append(out, blank)
	}
	return strings.Join(out, "\n")
}
