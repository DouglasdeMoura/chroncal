package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TrashReloadMsg asks the host to re-query ListDeleted and push the result
// back via SetEvents. Emitted after a successful restore/purge so the row
// disappears from the list.
type TrashReloadMsg struct{}

// TrashRestoreRequestedMsg asks the host to call Service.RestoreByID.
type TrashRestoreRequestedMsg struct{ ID int64 }

// TrashPurgeRequestedMsg asks the host to confirm and hard-delete one row
// via Service.PurgeByID.
type TrashPurgeRequestedMsg struct{ ID int64 }

// TrashViewRequestedMsg asks the host to open the read-only detail view
// for a soft-deleted event.
type TrashViewRequestedMsg struct{ Event event.Event }

type trashKeyMap struct {
	Up, Down key.Binding
	Restore  key.Binding
	Purge    key.Binding
	View     key.Binding
}

func defaultTrashKeys() trashKeyMap {
	return trashKeyMap{
		Up:      key.NewBinding(key.WithKeys("up", "k"), key.WithHelp("↑/k", "up")),
		Down:    key.NewBinding(key.WithKeys("down", "j"), key.WithHelp("↓/j", "down")),
		Restore: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
		Purge:   key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "purge")),
		View:    key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "view")),
	}
}

// TrashModel lists soft-deleted events for the currently-selected calendar,
// newest first. It's a read-mostly view: the user can restore (r), purge
// forever (x), or open the detail view (Enter). Actions are emitted as
// messages so the host owns the Service call and reload sequencing.
type TrashModel struct {
	events    []event.Event
	calendars map[int64]CalendarInfo
	selected  int
	scroll    int
	keys      trashKeyMap
	theme     Theme
	width     int
	height    int
}

// NewTrashModel returns an empty trash model. SetEvents populates it.
func NewTrashModel() TrashModel {
	return TrashModel{
		keys:     defaultTrashKeys(),
		selected: -1,
	}
}

func (m TrashModel) SetSize(w, h int) TrashModel {
	m.width = w
	m.height = h
	m.clampScroll()
	return m
}

func (m TrashModel) SetTheme(t Theme) TrashModel {
	m.theme = t
	return m
}

// SetEvents updates the row list. Keeps the selection anchored to the same
// event ID when possible so reloads after a restore/purge don't yank the
// cursor away; falls back to the previous index clamped to the new length.
func (m TrashModel) SetEvents(events []event.Event, calendars map[int64]CalendarInfo) TrashModel {
	var prevID int64
	if m.selected >= 0 && m.selected < len(m.events) {
		prevID = m.events[m.selected].ID
	}
	m.events = events
	m.calendars = calendars

	m.selected = -1
	if prevID != 0 {
		for i, ev := range events {
			if ev.ID == prevID {
				m.selected = i
				break
			}
		}
	}
	if m.selected < 0 && len(events) > 0 {
		m.selected = 0
	}
	m.clampScroll()
	return m
}

// SelectedEvent returns the event under the cursor, if any.
func (m TrashModel) SelectedEvent() (event.Event, bool) {
	if m.selected < 0 || m.selected >= len(m.events) {
		return event.Event{}, false
	}
	return m.events[m.selected], true
}

// Len returns the number of rows currently rendered.
func (m TrashModel) Len() int { return len(m.events) }

func (m TrashModel) Update(msg tea.Msg) (TrashModel, tea.Cmd) {
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch {
	case key.Matches(kp, m.keys.Up):
		if m.selected > 0 {
			m.selected--
			m.ensureVisible()
		}
		return m, nil
	case key.Matches(kp, m.keys.Down):
		if m.selected >= 0 && m.selected < len(m.events)-1 {
			m.selected++
			m.ensureVisible()
		}
		return m, nil
	case key.Matches(kp, m.keys.Restore):
		if ev, ok := m.SelectedEvent(); ok {
			id := ev.ID
			return m, func() tea.Msg { return TrashRestoreRequestedMsg{ID: id} }
		}
	case key.Matches(kp, m.keys.Purge):
		if ev, ok := m.SelectedEvent(); ok {
			id := ev.ID
			return m, func() tea.Msg { return TrashPurgeRequestedMsg{ID: id} }
		}
	case key.Matches(kp, m.keys.View):
		if ev, ok := m.SelectedEvent(); ok {
			return m, func() tea.Msg { return TrashViewRequestedMsg{Event: ev} }
		}
	}
	return m, nil
}

func (m TrashModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Text)
	hintStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)

	var out strings.Builder
	out.WriteString(headerStyle.Render("Recently deleted"))
	out.WriteByte('\n')
	out.WriteString(hintStyle.Render("r restore · x purge · enter view · esc back"))
	out.WriteString("\n\n")

	if len(m.events) == 0 {
		out.WriteString(hintStyle.Render("No deleted events."))
		return out.String()
	}

	headerLines := 4
	viewportH := max(m.height-headerLines, 1)
	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.events))
	for i := start; i < end; i++ {
		if i > start {
			out.WriteByte('\n')
		}
		out.WriteString(m.renderRow(i, i == m.selected))
	}
	return out.String()
}

// renderRow draws one deleted-event line: [deleted-date] [calendar dot] [title].
func (m TrashModel) renderRow(idx int, selected bool) string {
	ev := m.events[idx]

	base := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.theme.Selected).Bold(true)
	}

	deletedText := "—"
	if ev.DeletedAt != nil {
		deletedText = ev.DeletedAt.Local().Format("2006-01-02 15:04")
	}
	dateCol := base.Foreground(m.theme.TextDim).Width(18).Render(deletedText)

	cal := m.calendars[ev.CalendarID]
	dotColor := m.theme.Muted
	if cal.Color != "" {
		dotColor = lipgloss.Color(cal.Color)
	}
	dot := base.Foreground(dotColor).Render(Glyphs["dot"])

	prefix := dateCol + base.Render(" ") + base.Width(3).Render(" "+dot+" ")
	titleW := max(m.width-lipgloss.Width(prefix), 1)
	title := highlight.Foreground(m.theme.Text).Width(titleW).Render(truncateTo(ev.Title, titleW))

	return prefix + title
}

func (m *TrashModel) ensureVisible() {
	headerLines := 4
	viewportH := max(m.height-headerLines, 1)
	if m.selected < 0 {
		m.scroll = 0
		return
	}
	if m.selected < m.scroll {
		m.scroll = m.selected
	}
	bottom := m.scroll + viewportH - 1
	if m.selected > bottom {
		m.scroll = m.selected - viewportH + 1
	}
	m.clampScroll()
}

func (m *TrashModel) clampScroll() {
	headerLines := 4
	viewportH := max(m.height-headerLines, 1)
	ms := m.maxScroll(viewportH)
	if m.scroll > ms {
		m.scroll = ms
	}
	if m.scroll < 0 {
		m.scroll = 0
	}
}

func (m TrashModel) maxScroll(viewportH int) int {
	return max(len(m.events)-viewportH, 0)
}

