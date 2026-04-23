package tui

import (
	"fmt"
	"image/color"
	"strings"
	"time"

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
		label := formatTrashRowLabel(e)
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
	return []ListDialogAction{
		{Label: "Restore", Primary: true, Msg: func() tea.Msg { return TrashRestoreRequestedMsg{Entry: entry} }},
		{Label: "Purge", Danger: true, Msg: func() tea.Msg { return TrashPurgeRequestedMsg{Entry: entry} }},
	}
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

// formatTrashRowLabel builds the list-column row: "MMM DD HH:MM  <title>".
// The leading time is the when-it-happened moment — the event's scheduled
// start for TrashKindEvent, the excluded occurrence for TrashKindInstance,
// or the truncation cutoff for TrashKindTruncation. Falls back to the
// deleted-at timestamp when the scheduled time is unknown. No calendar
// dot: the row renders as a single pre-formatted string so the selection
// highlight paints the full width without mid-row foreground resets.
func formatTrashRowLabel(e event.TrashEntry) string {
	title := e.Title
	if title == "" {
		title = "(untitled)"
	}

	when := trashRowTimestamp(e)
	datePart := ""
	timePart := ""
	if !when.IsZero() {
		datePart = when.Local().Format("Jan 02")
		if e.AllDay {
			timePart = "all day"
		} else {
			timePart = when.Local().Format("15:04")
		}
	}
	if datePart == "" {
		return title
	}
	return fmt.Sprintf("%s %s  %s", datePart, timePart, title)
}

// trashRowTimestamp returns the "when" to lead the row with, chosen per
// kind. DeletedAt is the last-resort fallback for event rows created
// without a StartTime (defensive).
func trashRowTimestamp(e event.TrashEntry) time.Time {
	switch e.Kind {
	case event.TrashKindInstance:
		if !e.InstanceTime.IsZero() {
			return e.InstanceTime
		}
	case event.TrashKindTruncation:
		if !e.CutoffTime.IsZero() {
			return e.CutoffTime
		}
	case event.TrashKindEvent:
		if !e.StartTime.IsZero() {
			return e.StartTime
		}
	}
	return e.DeletedAt
}

// trashDetailLines renders the right-pane fields for a TrashEntry. The
// dialog is the ONLY place this data shows, so callers should not offer a
// secondary "open view" action — everything the user needs to decide
// whether to restore or purge lives here.
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

	// Kind-specific pointer into the original series timeline.
	switch e.Kind {
	case event.TrashKindInstance:
		if !e.InstanceTime.IsZero() {
			lines = append(lines, detailLine(faint, "Instance", e.InstanceTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
	case event.TrashKindTruncation:
		if !e.CutoffTime.IsZero() {
			lines = append(lines, detailLine(faint, "Cutoff", e.CutoffTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
	}

	// When / duration / all-day, when we know them.
	if when := trashWhen(e); when != "" {
		lines = append(lines, detailLine(faint, "When", when, labelWidth, w))
	}
	if dur := trashDuration(e); dur != "" {
		lines = append(lines, detailLine(faint, "Duration", dur, labelWidth, w))
	}
	if e.AllDay {
		lines = append(lines, detailLine(faint, "All day", Glyphs["checkbox.on"], labelWidth, w))
	}

	if cal.Name != "" {
		dot := "●"
		if cal.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("●")
		}
		lines = append(lines, detailLine(faint, "Calendar", dot+" "+cal.Name, labelWidth, w))
	}
	if e.Location != "" {
		lines = append(lines, detailLine(faint, "Where", e.Location, labelWidth, w))
	}
	if e.Status != "" {
		lines = append(lines, detailLine(faint, "Status", statusBadge(e.Status), labelWidth, w))
	}
	if e.Categories != "" {
		lines = append(lines, detailLine(faint, "Tags", e.Categories, labelWidth, w))
	}
	if e.Kind == event.TrashKindTruncation && e.PreviousRRule != "" {
		lines = append(lines, detailLine(faint, "Repeat", e.PreviousRRule, labelWidth, w))
	}

	if e.Description != "" {
		lines = append(lines, "")
		for raw := range strings.SplitSeq(e.Description, "\n") {
			lines = append(lines, wrapLine(raw, w)...)
		}
	}

	return lines
}

// trashWhen renders a "Fri, Apr 3 09:00 – 09:30" style range when the
// entry carries start/end times, matching the event view dialog's "When"
// row so deleted and live rows read the same way.
func trashWhen(e event.TrashEntry) string {
	if e.AllDay {
		return "all day"
	}
	if e.StartTime.IsZero() {
		return ""
	}
	start := e.StartTime.Local()
	end := e.EndTime.Local()
	if end.IsZero() {
		return start.Format("Mon, Jan 2 15:04")
	}
	if start.Format("2006-01-02") == end.Format("2006-01-02") {
		return fmt.Sprintf("%s – %s", start.Format("Mon, Jan 2 15:04"), end.Format("15:04"))
	}
	return fmt.Sprintf("%s – %s", start.Format("Mon, Jan 2 15:04"), end.Format("Mon, Jan 2 15:04"))
}

func trashDuration(e event.TrashEntry) string {
	if e.AllDay || e.StartTime.IsZero() || e.EndTime.IsZero() {
		return ""
	}
	d := e.EndTime.Sub(e.StartTime)
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
