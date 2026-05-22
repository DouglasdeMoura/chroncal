package tui

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/trash"
)

// TrashViewRequestedMsg asks the host to open the trash overlay. Emitted by
// the command palette so its handling matches the shift+D keybinding.
type TrashViewRequestedMsg struct{}

// TrashDialogClosedMsg is emitted when the dialog requests to close.
type TrashDialogClosedMsg struct{}

// TrashReloadMsg asks the host to re-query the trash aggregator and push
// the result back via SetEntries.
type TrashReloadMsg struct{}

// TrashRestoreRequestedMsg asks the host to call Service.Restore on each
// entry. Single-entry when nothing is marked (acts on the cursor row),
// multi-entry when the user has toggled marks with space.
type TrashRestoreRequestedMsg struct{ Entries []trash.Entry }

// TrashPurgeRequestedMsg asks the host to confirm and hard-remove every
// entry in the slice via Service.Purge.
type TrashPurgeRequestedMsg struct{ Entries []trash.Entry }

type trashKeyMap struct {
	Restore  key.Binding
	Purge    key.Binding
	PurgeAll key.Binding
	Mark     key.Binding
}

func defaultTrashKeys() trashKeyMap {
	return trashKeyMap{
		Restore: key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "restore")),
		Purge:   key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "purge")),
		// shift+X uses the same root letter as purge with shift =
		// "broader scope", matching the c/C and d/D pairs the app
		// already uses (new event vs manage calendars, undo delete
		// vs Recently Deleted).
		PurgeAll: key.NewBinding(key.WithKeys("X", "shift+x"), key.WithHelp("X", "purge all")),
		Mark:     key.NewBinding(key.WithKeys("space"), key.WithHelp("space", "select")),
	}
}

// TrashModel renders the "Recently deleted" two-column dialog. It's a
// thin wrapper around ListDialogModel that owns the trash-specific state
// (entries, calendar lookup) and translates shell events into trash-
// domain messages. Actions: Restore (primary), Purge (danger).
type TrashModel struct {
	shell     ListDialogModel
	entries   []trash.Entry
	calendars map[int64]CalendarInfo
	keys      trashKeyMap
	// marked is the set of entries the user has toggled with space. When
	// non-empty, Restore/Purge act on every entry in the set instead of
	// just the cursor row, and list rows render a checkbox prefix so the
	// selection state is visible at a glance. Keyed by entryKey so the
	// mapping survives kind-differentiated IDs.
	marked map[string]bool
}

// NewTrashModel builds an empty trash dialog. Call SetEntries to populate
// rows once the host has fetched them via trash.Service.List.
func NewTrashModel(calendars map[int64]CalendarInfo, h help.Model) TrashModel {
	m := TrashModel{
		shell: NewListDialogModel(h).
			SetTitle("Recently deleted"),
		calendars: calendars,
		keys:      defaultTrashKeys(),
		marked:    map[string]bool{},
	}
	return m.refresh()
}

// entryKey identifies a trash row across kinds so marks don't collide
// between rows from different domains that happen to share an ID.
func entryKey(e trash.Entry) string {
	return fmt.Sprintf("%d:%d", e.Kind, e.ID)
}

func (m TrashModel) SetSize(w, h int) TrashModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m TrashModel) SetSelectedColor(c color.Color) TrashModel {
	m.shell = m.shell.SetSelectedColor(c)
	return m
}

// SetEntries replaces the row list, preserving selection by kind+ID and
// pruning marks for entries no longer in the list (e.g. rows that were
// just restored or purged by an earlier bulk action).
func (m TrashModel) SetEntries(entries []trash.Entry, calendars map[int64]CalendarInfo) TrashModel {
	prev, hadSel := m.selectedEntry()
	m.entries = entries
	m.calendars = calendars

	if len(m.marked) > 0 {
		present := make(map[string]bool, len(entries))
		for _, e := range entries {
			present[entryKey(e)] = true
		}
		for k := range m.marked {
			if !present[k] {
				delete(m.marked, k)
			}
		}
	}

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

// ClearMarks discards the current multi-select set. Call after a bulk
// action resolves so the next round starts clean.
func (m TrashModel) ClearMarks() TrashModel {
	if len(m.marked) == 0 {
		return m
	}
	m.marked = map[string]bool{}
	return m.refresh()
}

// markedEntries returns the entries targeted by the next action: the
// marked set when non-empty, otherwise a single-element slice for the
// cursor row. Empty slice when the list is empty.
func (m TrashModel) markedEntries() []trash.Entry {
	if len(m.marked) > 0 {
		out := make([]trash.Entry, 0, len(m.marked))
		for _, e := range m.entries {
			if m.marked[entryKey(e)] {
				out = append(out, e)
			}
		}
		return out
	}
	if e, ok := m.selectedEntry(); ok {
		return []trash.Entry{e}
	}
	return nil
}

// toggleMark flips the mark on the cursor row.
func (m TrashModel) toggleMark() TrashModel {
	e, ok := m.selectedEntry()
	if !ok {
		return m
	}
	k := entryKey(e)
	if m.marked[k] {
		delete(m.marked, k)
	} else {
		m.marked[k] = true
	}
	return m.refresh()
}

func (m TrashModel) BoxSize() (int, int) { return m.shell.BoxSize() }
func (m TrashModel) View() string        { return m.shell.View() }
func (m TrashModel) Len() int            { return len(m.entries) }

func (m TrashModel) selectedEntry() (trash.Entry, bool) {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.entries) {
		return trash.Entry{}, false
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
		// Checkboxes are always visible — the empty box teaches users
		// that batch-select exists without forcing them to discover
		// Space first. Mirrors Finder's list-view checkbox mode and
		// Mail's edit-mode circles, which stay drawn whether anything
		// is selected or not.
		prefix := Glyphs["checkbox.off"] + " "
		if m.marked[entryKey(e)] {
			prefix = Glyphs["checkbox.on"] + " "
		}
		label := prefix + formatTrashRowLabel(e)
		if i == sel {
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
			label = style.Render(label)
		}
		rows[i] = label
	}
	m.shell = m.shell.SetRows(rows)

	if e, ok := m.selectedEntry(); ok {
		cal := m.calendars[e.CalendarID]
		title := e.Title
		if title == "" {
			title = "(untitled)"
		}
		lines := trashDetailLines(e, cal, m.detailWidth(), m.labelWidth())
		m.shell = m.shell.SetDetailTitle(title).SetDetailLines(lines)
	} else {
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	}

	if len(m.entries) == 0 {
		faint := lipgloss.NewStyle().Faint(true)
		m.shell = m.shell.SetEmptyList("", []string{faint.Render("Nothing in the trash.")})
		m.shell = m.shell.SetActions(nil)
	} else {
		m.shell = m.shell.SetActions(m.buildActions())
	}

	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

func (m TrashModel) buildActions() []ListDialogAction {
	targets := m.markedEntries()
	if len(targets) == 0 {
		return nil
	}
	restoreLabel := "Restore"
	purgeLabel := "Purge"
	if len(targets) > 1 {
		restoreLabel = fmt.Sprintf("Restore (%d)", len(targets))
		purgeLabel = fmt.Sprintf("Purge (%d)", len(targets))
	}
	actions := []ListDialogAction{
		{Label: restoreLabel, Primary: true, Msg: func() tea.Msg { return TrashRestoreRequestedMsg{Entries: targets} }},
		{Label: purgeLabel, Danger: true, Msg: func() tea.Msg { return TrashPurgeRequestedMsg{Entries: targets} }},
	}
	// Purge All sits last in the action row so it's both rightmost
	// visually (matching Photos.app / Notes.app's "Delete All" placement
	// at the far edge) and tabbed last (so a reflex Tab from Restore
	// doesn't land on the broadest destructive action first). The (N)
	// count keeps the scope distinct from per-row Purge at a glance.
	// Hidden when the trash is empty: buildActions returns nil up top
	// for that case, so the empty-list guard already covers it.
	all := make([]trash.Entry, len(m.entries))
	copy(all, m.entries)
	purgeAllLabel := fmt.Sprintf("Purge All (%d)", len(m.entries))
	actions = append(actions, ListDialogAction{
		Label: purgeAllLabel, Danger: true,
		Msg: func() tea.Msg { return TrashPurgeRequestedMsg{Entries: all} },
	})
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
	return []key.Binding{nav, sk.Tab, m.keys.Mark, m.keys.Restore, m.keys.Purge, m.keys.PurgeAll, sk.Close}
}

func (m TrashModel) Update(msg tea.Msg) (TrashModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	case tea.MouseWheelMsg:
		shell, cmd := m.shell.HandleMouseWheel(msg)
		m.shell = shell
		return m, cmd
	}
	return m, nil
}

func (m TrashModel) handleKey(msg tea.KeyPressMsg) (TrashModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Mark):
		return m.toggleMark(), nil
	}
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
	case key.Matches(msg, m.keys.PurgeAll):
		// Purge All is always the last action when entries exist;
		// buildActions returns nil for an empty trash, so the
		// len(acts) check also guards against an empty list.
		if len(acts) > 2 {
			m.shell = m.shell.FocusAction(len(acts) - 1)
			return m.refresh(), acts[len(acts)-1].Msg
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

// trashBulkTitle summarises the action target for toast + confirm prompts.
// Single-entry: the entry's title. Multi-entry: an "N items" count.
func trashBulkTitle(entries []trash.Entry) string {
	switch len(entries) {
	case 0:
		return ""
	case 1:
		return entries[0].Title
	default:
		return fmt.Sprintf("%d items", len(entries))
	}
}

// formatTrashRowLabel renders the list row as just the title. The detail
// pane on the right carries every timestamp the user might need, so the
// left column stays compact and the selection highlight paints a clean
// unbroken bar across the row.
func formatTrashRowLabel(e trash.Entry) string {
	if e.Title == "" {
		return "(untitled)"
	}
	return e.Title
}

// trashDetailLines renders the right-pane fields for a trash entry. The
// dialog is the ONLY place this data shows, so callers should not offer a
// secondary "open view" action — everything the user needs to decide
// whether to restore or purge lives here. Detail content branches on
// Kind so todos show due-date/status/progress and journals show a start
// date where events would show a time range. The entry title is pinned
// by the shell via SetDetailTitle and must not be prepended here.
func trashDetailLines(e trash.Entry, cal CalendarInfo, w, labelWidth int) []string {
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, detailLine(faint, "Kind", e.Kind.Label(), labelWidth, w))
	if !e.DeletedAt.IsZero() {
		lines = append(lines, detailLine(faint, "Deleted", e.DeletedAt.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
	}

	switch e.Kind {
	case trash.KindEventInstance:
		if !e.InstanceTime.IsZero() {
			lines = append(lines, detailLine(faint, "Instance", e.InstanceTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
	case trash.KindEventSeriesTail:
		if !e.CutoffTime.IsZero() {
			lines = append(lines, detailLine(faint, "Cutoff", e.CutoffTime.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
	case trash.KindTodo:
		if !e.DueDate.IsZero() {
			lines = append(lines, detailLine(faint, "Due", e.DueDate.Local().Format("Mon, Jan 2 15:04"), labelWidth, w))
		}
		if e.PercentComplete > 0 {
			lines = append(lines, detailLine(faint, "Progress", fmt.Sprintf("%d%%", e.PercentComplete), labelWidth, w))
		}
	case trash.KindJournal:
		if !e.StartTime.IsZero() {
			lines = append(lines, detailLine(faint, "Entry date", e.StartTime.Local().Format("Mon, Jan 2"), labelWidth, w))
		}
	}

	if e.Kind.IsEvent() {
		if when := trashWhen(e); when != "" {
			lines = append(lines, detailLine(faint, "When", when, labelWidth, w))
		}
		if dur := trashDuration(e); dur != "" {
			lines = append(lines, detailLine(faint, "Duration", dur, labelWidth, w))
		}
		if e.AllDay {
			lines = append(lines, detailLine(faint, "All day", Glyphs["checkbox.on"], labelWidth, w))
		}
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
	if e.Kind == trash.KindEventSeriesTail && e.PreviousRRule != "" {
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

// trashWhen renders a "Fri, Apr 3 09:00 – 09:30" style range for event-
// kind entries. Returns empty for non-event kinds (todos/journals use
// their own Due / Entry-date rows).
func trashWhen(e trash.Entry) string {
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

func trashDuration(e trash.Entry) string {
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
