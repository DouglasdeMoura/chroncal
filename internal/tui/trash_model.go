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
// for a soft-deleted event. Only fires for TrashKindEvent — instance and
// truncation deletes don't have a full row to show.
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

// trashDialogMaxWidth caps the dialog at a readable size on wide terminals.
const trashDialogMaxWidth = 80

// trashDialogMinHeight is the minimum body height when the terminal is small.
const trashDialogMinHeight = 6

// TrashModel is the "Recently deleted" dialog. It's a centered bordered
// box (not a full-view replacement) listing soft-deleted events, EXDATE-
// based instance deletes, and RRULE truncations, newest first.
type TrashModel struct {
	entries   []event.TrashEntry
	calendars map[int64]CalendarInfo
	selected  int
	scroll    int
	keys      trashKeyMap
	theme     Theme
	dialog    Dialog
	termW     int
	termH     int
}

// NewTrashModel returns an empty trash dialog. SetEntries populates it.
func NewTrashModel() TrashModel {
	styles := DefaultDialogStyles()
	d := NewDialog("Recently deleted", styles)
	return TrashModel{
		keys:     defaultTrashKeys(),
		selected: -1,
		dialog:   d,
	}
}

// SetSize takes the full terminal width/height; the dialog picks its own
// inner box from there.
func (m TrashModel) SetSize(w, h int) TrashModel {
	m.termW = w
	m.termH = h
	dw := w - 4
	if dw > trashDialogMaxWidth {
		dw = trashDialogMaxWidth
	}
	if dw < 20 {
		dw = 20
	}
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.dialog.SetWidth(dw)
	m.clampScroll()
	return m
}

func (m TrashModel) SetTheme(t Theme) TrashModel {
	m.theme = t
	return m
}

// SetEntries updates the row list, anchoring the selection to the same
// entry (by kind + ID) when possible so restore/purge reloads don't yank
// the cursor away.
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

// BoxSize returns the rendered dialog box size for compositeOverlay.
func (m TrashModel) BoxSize() (int, int) {
	if m.termW <= 0 || m.termH <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

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
	hintStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)

	var body strings.Builder
	body.WriteString(hintStyle.Render("r restore · x purge · enter view · esc back"))
	body.WriteString("\n\n")

	if len(m.entries) == 0 {
		body.WriteString(hintStyle.Render("No deleted events."))
		return m.dialog.Box(body.String())
	}

	viewportH := m.bodyHeight()
	start := min(max(m.scroll, 0), m.maxScroll(viewportH))
	end := min(start+viewportH, len(m.entries))
	for i := start; i < end; i++ {
		if i > start {
			body.WriteByte('\n')
		}
		body.WriteString(m.renderRow(i, i == m.selected))
	}
	return m.dialog.Box(body.String())
}

// renderRow draws one line: [deleted-date] [calendar dot] [title (· occurrence or truncation cutoff)].
func (m TrashModel) renderRow(idx int, selected bool) string {
	e := m.entries[idx]
	w := m.dialog.ContentWidth()
	if w <= 0 {
		w = trashDialogMaxWidth
	}

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
	switch e.Kind {
	case event.TrashKindInstance:
		if !e.InstanceTime.IsZero() {
			title += "  · " + e.InstanceTime.Local().Format("2006-01-02 15:04")
		}
	case event.TrashKindTruncation:
		if !e.CutoffTime.IsZero() {
			title += "  · truncated from " + e.CutoffTime.Local().Format("2006-01-02 15:04")
		}
	}

	prefix := dateCol + base.Render(" ") + base.Width(3).Render(" "+dot+" ")
	titleW := max(w-lipgloss.Width(prefix), 1)
	titleCell := highlight.Foreground(m.theme.Text).Width(titleW).Render(truncateTo(title, titleW))

	return prefix + titleCell
}

// bodyHeight returns the number of entry rows that fit in the dialog.
// The dialog chrome reserves space for the title, the hint line, and a
// blank separator; the rest is body.
func (m TrashModel) bodyHeight() int {
	if m.termH <= 0 {
		return trashDialogMinHeight
	}
	// Reserve room for border (2), padding (2 top/bottom), title row (1),
	// blank separator after title (1), hint row (1), blank after hint (1).
	chrome := 8
	h := m.termH - chrome - 4
	if h < trashDialogMinHeight {
		return trashDialogMinHeight
	}
	return h
}

func (m *TrashModel) ensureVisible() {
	viewportH := m.bodyHeight()
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
	viewportH := m.bodyHeight()
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
