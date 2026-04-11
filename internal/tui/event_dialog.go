package tui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
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

// EventDialogModel shows a day's events in a two-column dialog: a list on
// the left and the selected event's details on the right.
type EventDialogModel struct {
	day      time.Time
	events   []event.Event
	selected int
	keys     eventDialogKeyMap
	width    int
	height   int
}

func NewEventDialogModel(day time.Time, events []event.Event) EventDialogModel {
	return EventDialogModel{
		day:    day,
		events: events,
		keys:   defaultEventDialogKeys(),
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

// View renders the dialog as a self-contained box. The returned string has
// no outer padding, so the caller is free to composite it over other content
// at an arbitrary position.
func (m EventDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	boxW, boxH := m.boxSize()

	// Border (2) + horizontal padding (2*2) = 6 overhead on width.
	// Border (2) + vertical padding (1*2) = 4 overhead on height.
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

	// Body = innerH minus title row + blank + blank + help = 4 reserved.
	bodyH := max(innerH-4, 3)

	listW := max(min(max(innerW/3, 20), innerW-24), 10)
	dividerW := 3
	detailsW := max(innerW-listW-dividerW, 10)

	list := m.renderList(listW, bodyH)
	divider := m.renderDivider(dividerW, bodyH)
	details := m.renderDetails(detailsW, bodyH)

	body := lipgloss.JoinHorizontal(lipgloss.Top, list, divider, details)

	content := lipgloss.JoinVertical(lipgloss.Left, title, "", body, "", help)

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
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
	boxW := min(max(m.width*4/5, 50), m.width-2)
	boxH := min(max(m.height*4/5, 14), m.height-2)
	return boxW, boxH
}

func (m EventDialogModel) renderList(w, h int) string {
	lines := make([]string, 0, h)
	for i, ev := range m.events {
		if len(lines) >= h {
			break
		}
		label := formatEventLabel(ev)
		label = truncateTo(label, w)
		style := lipgloss.NewStyle().Width(w)
		if i == m.selected {
			style = style.Reverse(true).Bold(true)
		}
		lines = append(lines, style.Render(label))
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

func (m EventDialogModel) renderDetails(w, h int) string {
	if len(m.events) == 0 || m.selected < 0 || m.selected >= len(m.events) {
		return padLines(nil, w, h)
	}
	ev := m.events[m.selected]

	labelStyle := lipgloss.NewStyle().Faint(true)
	titleStyle := lipgloss.NewStyle().Bold(true)

	var lines []string
	lines = append(lines, truncateTo(titleStyle.Render(ev.Title), w))
	lines = append(lines, "")

	when := formatWhen(ev)
	lines = append(lines, truncateTo(labelStyle.Render("When:  ")+when, w))

	if ev.Location != "" {
		lines = append(lines, truncateTo(labelStyle.Render("Where: ")+ev.Location, w))
	}
	if ev.Status != "" {
		lines = append(lines, truncateTo(labelStyle.Render("Status: ")+ev.Status, w))
	}
	if ev.Categories != "" {
		lines = append(lines, truncateTo(labelStyle.Render("Tags:  ")+ev.Categories, w))
	}
	if ev.URL != "" {
		lines = append(lines, truncateTo(labelStyle.Render("URL:   ")+ev.URL, w))
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
		return start.Format("Mon, Jan 2 15:04")
	}
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return fmt.Sprintf("%s – %s", start.Format("Mon, Jan 2 15:04"), end.Format("15:04"))
	}
	return fmt.Sprintf("%s – %s", start.Format("Mon, Jan 2 15:04"), end.Format("Mon, Jan 2 15:04"))
}

func truncateTo(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	// lipgloss.Width respects ANSI; use it iteratively to avoid breaking escapes.
	// Fall back to a plain rune truncation when s has no ANSI.
	if !strings.ContainsRune(s, '\x1b') {
		r := []rune(s)
		if w == 1 {
			return "…"
		}
		return string(r[:w-1]) + "…"
	}
	// Leave ANSI-styled strings intact if they already fit; otherwise
	// drop to a best-effort truncation that strips styling.
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
		// Pad to exact width with plain style so columns align.
		padded := lipgloss.NewStyle().Width(w).Render(l)
		out = append(out, padded)
	}
	for len(out) < h {
		out = append(out, blank)
	}
	return strings.Join(out, "\n")
}
