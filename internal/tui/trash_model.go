package tui

import (
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TrashReloadMsg asks the host to re-query ListTrash and push the result
// back via SetEntries. Emitted after a successful restore/purge so the
// row disappears from the list.
type TrashReloadMsg struct{}

// TrashRestoreRequestedMsg asks the host to call Service.RestoreTrash
// for the selected entry.
type TrashRestoreRequestedMsg struct{ Entry event.TrashEntry }

// TrashPurgeRequestedMsg asks the host to confirm and hard-remove the
// selected entry via Service.PurgeTrashEntry.
type TrashPurgeRequestedMsg struct{ Entry event.TrashEntry }

// TrashViewRequestedMsg asks the host to open the read-only detail view
// for a soft-deleted event. Only fires for TrashKindEvent — instance
// deletes don't have a full row to show.
type TrashViewRequestedMsg struct{ Entry event.TrashEntry }

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

// TrashModel lists trash entries (soft-deleted events and EXDATE-based
// instance deletes) for the visible calendars, newest first.
type TrashModel struct {
	entries   []event.TrashEntry
	calendars map[int64]CalendarInfo
	selected  int
	scroll    int
	keys      trashKeyMap
	theme     Theme
	width     int
	height    int
}

// NewTrashModel returns an empty trash model. SetEntries populates it.
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

// SetEntries updates the row list. Keeps the selection anchored to the
// same entry (by kind + ID) when possible so reloads after a restore or
// purge don't yank the cursor away.
func (m TrashModel) SetEntries(entries []event.TrashEntry, calendars map[int64]CalendarInfo) TrashModel {
	var prevKind event.TrashKind
	var prevID int64
	var hadSel bool
	if m.selected >= 0 && m.selected < len(m.entries) {
		prevKind = m.entries[m.selected].Kind
		prevID = m.entries[m.selected].ID
		hadSel = true
	}
	m.entries = entries
	m.calendars = calendars

	m.selected = -1
	if hadSel {
		for i, e := range entries {
			if e.Kind == prevKind && e.ID == prevID {
				m.selected = i
				break
			}
		}
	}
	if m.selected < 0 && len(entries) > 0 {
		m.selected = 0
	}
	m.clampScroll()
	return m
}

// Selected returns the entry under the cursor, if any.
func (m TrashModel) Selected() (event.TrashEntry, bool) {
	if m.selected < 0 || m.selected >= len(m.entries) {
		return event.TrashEntry{}, false
	}
	return m.entries[m.selected], true
}

// Len returns the number of rows currently rendered.
func (m TrashModel) Len() int { return len(m.entries) }

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
		if m.selected >= 0 && m.selected < len(m.entries)-1 {
			m.selected++
			m.ensureVisible()
		}
		return m, nil
	case key.Matches(kp, m.keys.Restore):
		if e, ok := m.Selected(); ok {
			return m, func() tea.Msg { return TrashRestoreRequestedMsg{Entry: e} }
		}
	case key.Matches(kp, m.keys.Purge):
		if e, ok := m.Selected(); ok {
			return m, func() tea.Msg { return TrashPurgeRequestedMsg{Entry: e} }
		}
	case key.Matches(kp, m.keys.View):
		if e, ok := m.Selected(); ok && e.Kind == event.TrashKindEvent {
			return m, func() tea.Msg { return TrashViewRequestedMsg{Entry: e} }
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

	if len(m.entries) == 0 {
		out.WriteString(hintStyle.Render("No deleted events."))
		return out.String()
	}

	headerLines := 4
	viewportH := max(m.height-headerLines, 1)
	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.entries))
	for i := start; i < end; i++ {
		if i > start {
			out.WriteByte('\n')
		}
		out.WriteString(m.renderRow(i, i == m.selected))
	}
	return out.String()
}

// renderRow draws one line: [deleted-date] [calendar dot] [title (· instance time for instance deletes)].
func (m TrashModel) renderRow(idx int, selected bool) string {
	e := m.entries[idx]

	base := lipgloss.NewStyle()
	highlight := lipgloss.NewStyle()
	if selected {
		highlight = highlight.Background(m.theme.Selected).Bold(true)
	}

	deletedText := "—"
	if !e.DeletedAt.IsZero() {
		deletedText = e.DeletedAt.Local().Format("2006-01-02 15:04")
	}
	dateCol := base.Foreground(m.theme.TextDim).Width(18).Render(deletedText)

	cal := m.calendars[e.CalendarID]
	dotColor := m.theme.Muted
	if cal.Color != "" {
		dotColor = lipgloss.Color(cal.Color)
	}
	dot := base.Foreground(dotColor).Render(Glyphs["dot"])

	title := e.Title
	if title == "" {
		title = "(untitled)"
	}
	if e.Kind == event.TrashKindInstance && !e.InstanceTime.IsZero() {
		title = title + "  · " + e.InstanceTime.Local().Format("2006-01-02 15:04")
	}

	prefix := dateCol + base.Render(" ") + base.Width(3).Render(" "+dot+" ")
	titleW := max(m.width-lipgloss.Width(prefix), 1)
	titleCell := highlight.Foreground(m.theme.Text).Width(titleW).Render(truncateTo(title, titleW))

	return prefix + titleCell
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
	return max(len(m.entries)-viewportH, 0)
}
