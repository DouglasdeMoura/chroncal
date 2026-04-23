package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TrashDialogClosedMsg is emitted when the dialog requests to close.
type TrashDialogClosedMsg struct{}

// TrashReloadMsg asks the host to re-query ListTrash and push the result
// back via SetEntries.
type TrashReloadMsg struct{}

// TrashRestoreRequestedMsg asks the host to call Service.RestoreTrash.
type TrashRestoreRequestedMsg struct{ Entry event.TrashEntry }

// TrashPurgeRequestedMsg asks the host to confirm and hard-remove the
// selected entry via Service.PurgeTrashEntry.
type TrashPurgeRequestedMsg struct{ Entry event.TrashEntry }

// TrashViewRequestedMsg asks the host to open the read-only detail view
// for a soft-deleted event. Only fires for TrashKindEvent — instance and
// truncation entries have no full row to show.
type TrashViewRequestedMsg struct{ Entry event.TrashEntry }

type trashKeyMap struct {
	Restore key.Binding
	Purge   key.Binding
}

func defaultTrashKeys() trashKeyMap {
	return trashKeyMap{
		Restore: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
		Purge:   key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "purge")),
	}
}

// TrashModel renders the "Recently deleted" two-column dialog. It's a
// thin wrapper around ListDialogModel that owns the trash-specific state
// (entries, calendar lookup) and translates shell events into trash-
// domain messages. Actions: Restore (primary), Purge (danger).
type TrashModel struct {
	shell     ListDialogModel
	entries   []event.TrashEntry
	calendars map[int64]CalendarInfo
	keys      trashKeyMap
}

// NewTrashModel builds an empty trash dialog. Call SetEntries to populate
// rows once the host has fetched them via Service.ListTrash.
func NewTrashModel(calendars map[int64]CalendarInfo, h help.Model) TrashModel {
	m := TrashModel{
		shell: NewListDialogModel(h).
			SetTitle("Recently deleted"),
		calendars: calendars,
		keys:      defaultTrashKeys(),
	}
	return m.refresh()
}

func (m TrashModel) SetSize(w, h int) TrashModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m TrashModel) SetSelectedColor(c color.Color) TrashModel {
	m.shell = m.shell.SetSelectedColor(c)
	return m
}

// SetEntries replaces the row list, preserving selection by kind+ID.
func (m TrashModel) SetEntries(entries []event.TrashEntry, calendars map[int64]CalendarInfo) TrashModel {
	prev, hadSel := m.selectedEntry()
	m.entries = entries
	m.calendars = calendars

	newSel := 0
	if hadSel {
		for i, e := range entries {
			if e.Kind == prev.Kind && e.ID == prev.ID {
				newSel = i
				break
			}
		}
	}
	m.shell = m.shell.SetSelected(newSel)
	return m.refresh()
}

func (m TrashModel) BoxSize() (int, int) { return m.shell.BoxSize() }
func (m TrashModel) View() string        { return m.shell.View() }
func (m TrashModel) Len() int            { return len(m.entries) }

func (m TrashModel) selectedEntry() (event.TrashEntry, bool) {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.entries) {
		return event.TrashEntry{}, false
	}
	return m.entries[idx], true
}

func (m TrashModel) labelWidth() int {
	if m.shell.isNarrow() {
		return 9
	}
	return 11
}

func (m TrashModel) detailWidth() int {
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

func (m TrashModel) listRowWidth() int {
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

// refresh rebuilds rows, detail lines, actions, and help from the current
// entry list and selection.
func (m TrashModel) refresh() TrashModel {
	rows := make([]string, len(m.entries))
	sel := m.shell.Selected()
	listFocused := m.shell.FocusZone() == ListZoneList
	rowW := m.listRowWidth()
	selBG := m.shell.SelectedColor()
	for i, e := range m.entries {
		label := formatTrashRowLabel(e, m.calendars)
		if i == sel {
			style := lipgloss.NewStyle()
			switch {
			case listFocused:
				style = style.Reverse(true).Bold(true)
			case selBG != nil:
				style = style.Background(selBG)
			}
			if rowW > 0 {
				style = style.Width(rowW)
			}
			label = style.Render(label)
		}
		rows[i] = label
	}
	m.shell = m.shell.SetRows(rows)

	if e, ok := m.selectedEntry(); ok {
		cal := m.calendars[e.CalendarID]
		lines := trashDetailLines(e, cal, m.detailWidth(), m.labelWidth())
		m.shell = m.shell.SetDetailLines(lines)
	} else {
		m.shell = m.shell.SetDetailLines(nil)
	}

	if len(m.entries) == 0 {
		faint := lipgloss.NewStyle().Faint(true)
		m.shell = m.shell.SetEmptyList("", []string{faint.Render("No deleted events.")})
		m.shell = m.shell.SetActions(nil)
	} else {
		m.shell = m.shell.SetActions(m.buildActions())
	}

	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

func (m TrashModel) buildActions() []ListDialogAction {
	e, ok := m.selectedEntry()
	if !ok {
		return nil
	}
	entry := e
	actions := []ListDialogAction{
		{Label: "Restore", Primary: true, Msg: func() tea.Msg { return TrashRestoreRequestedMsg{Entry: entry} }},
		{Label: "Purge", Danger: true, Msg: func() tea.Msg { return TrashPurgeRequestedMsg{Entry: entry} }},
	}
	if entry.Kind == event.TrashKindEvent {
		actions = append(actions,
			ListDialogAction{Label: "View", Msg: func() tea.Msg { return TrashViewRequestedMsg{Entry: entry} }},
		)
	}
	return actions
}

func (m TrashModel) shortHelp() []key.Binding {
	sk := m.shell.Keys()
	nav := key.NewBinding(
		key.WithKeys("up", "down", "k", "j"),
		key.WithHelp("↑↓", "navigate"),
	)
	if len(m.entries) == 0 {
		return []key.Binding{sk.Close}
	}
	return []key.Binding{nav, sk.Tab, m.keys.Restore, m.keys.Purge, sk.Close}
}

func (m TrashModel) Update(msg tea.Msg) (TrashModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m TrashModel) handleKey(msg tea.KeyPressMsg) (TrashModel, tea.Cmd) {
	sk := m.shell.Keys()
	acts := m.buildActions()
	switch {
	case key.Matches(msg, m.keys.Restore):
		if len(acts) > 0 {
			m.shell = m.shell.FocusAction(0)
			return m.refresh(), acts[0].Msg
		}
		return m, nil
	case key.Matches(msg, m.keys.Purge):
		if len(acts) > 1 {
			m.shell = m.shell.FocusAction(1)
			return m.refresh(), acts[1].Msg
		}
		return m, nil
	case key.Matches(msg, sk.Enter):
		// Enter on an event-kind row in the list zone opens the detail view.
		// Instance and truncation entries have no full row to open, and
		// Enter on those (or on a focused action button) falls through to the
		// shell's default handler, which activates the focused button.
		if m.shell.FocusZone() == ListZoneList {
			if e, ok := m.selectedEntry(); ok && e.Kind == event.TrashKindEvent {
				entry := e
				return m, func() tea.Msg { return TrashViewRequestedMsg{Entry: entry} }
			}
		}
	}

	shell, cmd, _ := m.shell.HandleKey(msg, func() tea.Msg { return TrashDialogClosedMsg{} })
	m.shell = shell
	return m.refresh(), cmd
}

func (m TrashModel) handleMouse(msg tea.MouseClickMsg) (TrashModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
		m.shell = m.shell.ClickRow(idx)
		return m.refresh(), nil
	}
	if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
		shell, cmd := m.shell.ClickAction(idx)
		m.shell = shell
		return m.refresh(), cmd
	}
	return m, nil
}

// formatTrashRowLabel builds the list-column row: "HH:MM  <title>" for
// entries that carry a meaningful instance/cutoff time, otherwise the
// deleted_at time and title. Matches event_dialog's row density so both
// dialogs feel consistent.
func formatTrashRowLabel(e event.TrashEntry, calendars map[int64]CalendarInfo) string {
	title := e.Title
	if title == "" {
		title = "(untitled)"
	}
	timeLabel := ""
	switch e.Kind {
	case event.TrashKindInstance:
		if !e.InstanceTime.IsZero() {
			timeLabel = e.InstanceTime.Local().Format("15:04")
		}
	case event.TrashKindTruncation:
		if !e.CutoffTime.IsZero() {
			timeLabel = e.CutoffTime.Local().Format("15:04")
		}
	case event.TrashKindEvent:
		if !e.DeletedAt.IsZero() {
			timeLabel = e.DeletedAt.Local().Format("15:04")
		}
	}
	if timeLabel == "" {
		timeLabel = "• "
	}

	dot := ""
	if cal, ok := calendars[e.CalendarID]; ok && cal.Color != "" {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("●") + " "
	}
	return fmt.Sprintf("%s  %s%s", timeLabel, dot, title)
}

// trashDetailLines renders the right-pane fields for a TrashEntry.
func trashDetailLines(e event.TrashEntry, cal CalendarInfo, w, labelWidth int) []string {
	faint := lipgloss.NewStyle().Faint(true)

	title := e.Title
	if title == "" {
		title = "(untitled)"
	}

	var lines []string
	lines = append(lines, strings.Split(paneTitle(title, w), "\n")...)
	lines = append(lines, "")

	lines = append(lines, detailLine(faint, "Kind", trashKindLabel(e.Kind), labelWidth, w))
	if !e.DeletedAt.IsZero() {
		lines = append(lines, detailLine(faint, "Deleted", e.DeletedAt.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
	}
	if cal.Name != "" {
		dot := "●"
		if cal.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("●")
		}
		lines = append(lines, detailLine(faint, "Calendar", dot+" "+cal.Name, labelWidth, w))
	}
	if e.UID != "" {
		lines = append(lines, detailLine(faint, "UID", e.UID, labelWidth, w))
	}

	switch e.Kind {
	case event.TrashKindInstance:
		if !e.InstanceTime.IsZero() {
			lines = append(lines, detailLine(faint, "Instance", e.InstanceTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
	case event.TrashKindTruncation:
		if !e.CutoffTime.IsZero() {
			lines = append(lines, detailLine(faint, "Cutoff", e.CutoffTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
		if e.PreviousRRule != "" {
			lines = append(lines, detailLine(faint, "Prev RRULE", e.PreviousRRule, labelWidth, w))
		}
	}

	return lines
}

func trashKindLabel(k event.TrashKind) string {
	switch k {
	case event.TrashKindEvent:
		return "Event"
	case event.TrashKindInstance:
		return "Instance"
	case event.TrashKindTruncation:
		return "Series tail"
	default:
		return "Unknown"
	}
}
