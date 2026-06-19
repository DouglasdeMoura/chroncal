package tui

import (
	"cmp"
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// CalendarListDialogClosedMsg is emitted when the dialog requests to close.
type CalendarListDialogClosedMsg struct{}

// CalendarListDialogRequestedMsg opens the manage-calendars dialog.
type CalendarListDialogRequestedMsg struct{}

// CalendarSetDefaultRequestedMsg asks the app to promote the given
// calendar to default. The app handler updates the database and
// reloads the calendar list.
type CalendarSetDefaultRequestedMsg struct {
	ID   int64
	Name string
}

type calendarListDialogKeyMap struct {
	Edit             key.Binding
	Delete           key.Binding
	New              key.Binding
	SetDefault       key.Binding
	MoveUp, MoveDown key.Binding
}

func defaultCalendarListDialogKeys() calendarListDialogKeyMap {
	return calendarListDialogKeyMap{
		Edit:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:     key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		New:        key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "add")),
		SetDefault: key.NewBinding(key.WithKeys("*"), key.WithHelp("*", "set default")),
		MoveUp:     key.NewBinding(key.WithKeys("shift+up", "K"), key.WithHelp("shift+↑/K", "move up")),
		MoveDown:   key.NewBinding(key.WithKeys("shift+down", "J"), key.WithHelp("shift+↓/J", "move down")),
	}
}

// CalendarListDialogModel renders the list of calendars with a details pane
// and action buttons for creating, editing, and deleting calendars. It is a
// thin wrapper around ListDialogModel that carries the calendar-specific
// state (sorted IDs, hidden set) and translates shell events into
// calendar-domain messages.
type CalendarListDialogModel struct {
	shell     ListDialogModel
	calendars map[int64]CalendarInfo
	order     []int64
	hidden    map[int64]bool
	keys      calendarListDialogKeyMap
}

// NewCalendarListDialogModel builds a dialog populated from the given calendar
// map and hidden set. Calendars follow the persisted sidebar display order
// (name as a tiebreak) so this dialog matches the sidebar.
func NewCalendarListDialogModel(calendars map[int64]CalendarInfo, hidden map[int64]bool, h help.Model) CalendarListDialogModel {
	newAction := ListDialogAction{
		Label:   "+ Add Calendar",
		Primary: true,
		Msg:     func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} },
	}
	m := CalendarListDialogModel{
		shell: NewListDialogModel(h).
			SetTitle("Calendars").
			SetTitleAction(&newAction),
		calendars: calendars,
		order:     sortedCalendarIDs(calendars),
		hidden:    hidden,
		keys:      defaultCalendarListDialogKeys(),
	}
	return m.refresh()
}

// calendarPromotionCandidate is a stable (id, name) pair used to populate
// the default-promotion picker — buttons are addressed by index, so the
// order must match the slice that gets stored in the model.
type calendarPromotionCandidate struct {
	id   int64
	name string
}

// defaultPromotionCandidates returns every calendar other than excludeID, in
// the persisted sidebar display order so the picker matches the list. Lives
// next to the dialog so the row label and the picker share their sort rule
// via sortedCalendarIDs.
func defaultPromotionCandidates(calendars map[int64]CalendarInfo, excludeID int64) []calendarPromotionCandidate {
	ids := sortedCalendarIDs(calendars)
	out := make([]calendarPromotionCandidate, 0, len(ids))
	for _, id := range ids {
		if id == excludeID {
			continue
		}
		out = append(out, calendarPromotionCandidate{id: id, name: calendars[id].Name})
	}
	return out
}

// compareCalendarOrder orders calendars by their persisted sidebar position,
// falling back to name for ties. Shared by the sidebar list and the
// manage-calendars dialog so both render calendars in the same order.
func compareCalendarOrder(aOrder int64, aName string, bOrder int64, bName string) int {
	return cmp.Or(cmp.Compare(aOrder, bOrder), strings.Compare(aName, bName))
}

func sortedCalendarIDs(calendars map[int64]CalendarInfo) []int64 {
	ids := make([]int64, 0, len(calendars))
	for id := range calendars {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(a, b int64) int {
		return compareCalendarOrder(calendars[a].DisplayOrder, calendars[a].Name, calendars[b].DisplayOrder, calendars[b].Name)
	})
	return ids
}

func (m CalendarListDialogModel) SetSize(w, h int) CalendarListDialogModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m CalendarListDialogModel) SetSelectedColor(c color.Color) CalendarListDialogModel {
	m.shell = m.shell.SetSelectedColor(c)
	return m
}

func (m CalendarListDialogModel) SetMutedColor(color.Color) CalendarListDialogModel { return m }

// SetCalendars replaces the calendar map and hidden set, preserving the
// selected ID when possible so edits don't jump the cursor.
func (m CalendarListDialogModel) SetCalendars(calendars map[int64]CalendarInfo, hidden map[int64]bool) CalendarListDialogModel {
	var prevID int64
	if idx := m.shell.Selected(); idx >= 0 && idx < len(m.order) {
		prevID = m.order[idx]
	}
	m.calendars = calendars
	m.hidden = hidden
	m.order = sortedCalendarIDs(calendars)

	newSel := 0
	for i, id := range m.order {
		if id == prevID {
			newSel = i
			break
		}
	}
	m.shell = m.shell.SetSelected(newSel)
	return m.refresh()
}

// BoxSize returns the dialog's outer dimensions.
func (m CalendarListDialogModel) BoxSize() (int, int) { return m.shell.BoxSize() }

func (m CalendarListDialogModel) selectedID() (int64, bool) {
	idx := m.shell.Selected()
	if idx < 0 || idx >= len(m.order) {
		return 0, false
	}
	return m.order[idx], true
}

// refresh rebuilds the shell's rows, detail lines, and actions from the
// current calendar list and selection.
func (m CalendarListDialogModel) refresh() CalendarListDialogModel {
	rows := make([]string, len(m.order))
	sel := m.shell.Selected()
	listFocused := m.shell.FocusZone() == ListZoneList
	rowW := m.listRowWidth()
	selBG := m.shell.SelectedColor()
	for i, id := range m.order {
		info := m.calendars[id]
		rows[i] = calendarRowLabel(info, m.hidden[id], i == sel, listFocused, selBG, rowW)
	}
	m.shell = m.shell.SetRows(rows)

	if id, ok := m.selectedID(); ok {
		info := m.calendars[id]
		m.shell = m.shell.SetDetailTitle(info.Name).SetDetailLines(calendarDetailLines(info, m.detailWidth(), m.labelWidth()))
	} else {
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	}
	if len(m.order) == 0 {
		m.shell = m.shell.SetEmptyList("", []string{lipgloss.NewStyle().Faint(true).Render("No calendars yet.")})
	}

	m.shell = m.shell.SetActions(m.buildActions())
	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

func (m CalendarListDialogModel) buildActions() []ListDialogAction {
	id, ok := m.selectedID()
	if !ok {
		return nil
	}
	info := m.calendars[id]
	actions := []ListDialogAction{
		{Label: "Edit", Msg: func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }},
	}
	// Hide "Set as Default" when the selected row is already the default —
	// showing a button that no-ops is exactly the kind of dead control the
	// HIG warns against.
	if !info.IsDefault {
		actions = append(actions, ListDialogAction{
			Label: "Set as Default",
			Msg: func() tea.Msg {
				return CalendarSetDefaultRequestedMsg{ID: id, Name: info.Name}
			},
		})
	}
	actions = append(actions, ListDialogAction{
		Label: "Delete", Danger: true,
		Msg: func() tea.Msg { return CalendarDeleteRequestedMsg{ID: id, Name: info.Name} },
	})
	return actions
}

func (m CalendarListDialogModel) shortHelp() []key.Binding {
	sk := m.shell.Keys()
	nav := key.NewBinding(
		key.WithKeys("up", "down", "k", "j"),
		key.WithHelp("↑↓", "navigate"),
	)
	reorder := key.NewBinding(
		key.WithKeys(append(m.keys.MoveUp.Keys(), m.keys.MoveDown.Keys()...)...),
		key.WithHelp("shift+↑↓", "reorder"),
	)
	return []key.Binding{nav, reorder, sk.Tab, m.keys.New, m.keys.Edit, m.keys.SetDefault, m.keys.Delete, sk.Close}
}

// detailWidth returns the width of the detail column for the current shell
// size, matching the shell's layout math.
func (m CalendarListDialogModel) detailWidth() int {
	boxW, _ := m.shell.BoxSize()
	innerW := max(boxW-5, 10)
	if boxW == 0 {
		return 40
	}
	if m.shell.isNarrow() {
		return innerW
	}
	return detailColumnWidth(innerW)
}

func (m CalendarListDialogModel) labelWidth() int {
	if m.shell.isNarrow() {
		return 7
	}
	return 10
}

// listRowWidth mirrors the shell's list-column math so callers can pad rows
// to the full visible width (needed when painting the selected row's
// background all the way to the right edge).
func (m CalendarListDialogModel) listRowWidth() int {
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

func (m CalendarListDialogModel) Update(msg tea.Msg) (CalendarListDialogModel, tea.Cmd) {
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

func (m CalendarListDialogModel) handleKey(msg tea.KeyPressMsg) (CalendarListDialogModel, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.New):
		return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: 0} }
	case key.Matches(msg, m.keys.Edit):
		if id, ok := m.selectedID(); ok {
			return m, func() tea.Msg { return CalendarDialogRequestedMsg{ID: id} }
		}
		return m, nil
	case key.Matches(msg, m.keys.Delete):
		if id, ok := m.selectedID(); ok {
			info := m.calendars[id]
			return m, func() tea.Msg { return CalendarDeleteRequestedMsg{ID: id, Name: info.Name} }
		}
		return m, nil
	case key.Matches(msg, m.keys.SetDefault):
		if id, ok := m.selectedID(); ok {
			info := m.calendars[id]
			if info.IsDefault {
				return m, nil
			}
			return m, func() tea.Msg { return CalendarSetDefaultRequestedMsg{ID: id, Name: info.Name} }
		}
		return m, nil
	// Reorder only when the list itself owns focus; when the user has Tabbed
	// to the action buttons or the title action, fall through so shift+↑/↓ and
	// K/J don't silently reorder (and persist) a row the user isn't looking at.
	case m.shell.FocusZone() == ListZoneList && key.Matches(msg, m.keys.MoveUp):
		return m.moveSelected(-1)
	case m.shell.FocusZone() == ListZoneList && key.Matches(msg, m.keys.MoveDown):
		return m.moveSelected(1)
	}

	shell, cmd, _ := m.shell.HandleKey(msg, func() tea.Msg { return CalendarListDialogClosedMsg{} })
	m.shell = shell
	return m.refresh(), cmd
}

// moveSelected swaps the selected calendar with its neighbour delta rows away
// (±1), keeps the selection on the moved calendar, and emits
// CalendarReorderedMsg with the new top-to-bottom ID order so the app persists
// it and keeps the sidebar in sync. The local swap mirrors the sidebar list's
// optimistic behavior so the dialog updates without waiting for the round-trip.
func (m CalendarListDialogModel) moveSelected(delta int) (CalendarListDialogModel, tea.Cmd) {
	i := m.shell.Selected()
	j := i + delta
	if i < 0 || i >= len(m.order) || j < 0 || j >= len(m.order) {
		return m, nil
	}
	order := slices.Clone(m.order)
	order[i], order[j] = order[j], order[i]
	m.order = order
	m.shell = m.shell.SetSelected(j)
	return m.refresh(), func() tea.Msg { return CalendarReorderedMsg{IDs: order} }
}

func (m CalendarListDialogModel) handleMouse(msg tea.MouseClickMsg) (CalendarListDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	if cmd, ok := m.shell.TitleActionAtPosition(msg.X, msg.Y); ok {
		return m, cmd
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

func (m CalendarListDialogModel) View() string { return m.shell.View() }

// calendarRowLabel builds "<dot> <name>": the leading circle keeps its
// calendar-color foreground with no background change; when the row is
// selected the name — plus the remaining width of the row — takes on a
// tinted background so the highlight stretches to the right edge. While
// the list owns focus, the background is the terminal's reverse-video
// inversion plus bold; when focus is elsewhere, the row keeps a subtler
// themed tint so the selection remains visible without drawing the eye
// away from whichever control currently has focus. rowW is the full
// list-column width; when 0 the chip falls back to just sizing to the
// name.
func calendarRowLabel(info CalendarInfo, hidden, selected, listFocused bool, selBG color.Color, rowW int) string {
	glyph := "●"
	if hidden {
		glyph = "○"
	}
	swatch := lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color)).Render(glyph)

	nameStyle := lipgloss.NewStyle()
	if hidden {
		nameStyle = nameStyle.Faint(true)
	}
	label := info.Name
	if info.IsDefault {
		label += " (Default)"
	}
	if selected {
		switch {
		case listFocused:
			nameStyle = nameStyle.Reverse(true).Bold(true)
		case selBG != nil:
			nameStyle = nameStyle.Background(selBG).Foreground(activeTheme.SelectedText)
		}
		// Reserve the swatch (1 cell) + separator space (1 cell) and let the
		// chip style fill the rest, so trailing pad cells pick up the tint.
		if remaining := rowW - 2; remaining > 0 {
			nameStyle = nameStyle.Width(remaining)
		}
	}
	name := nameStyle.Render(" " + label + " ")

	return fmt.Sprintf("%s %s", swatch, name)
}

// calendarDetailLines returns the scrollable body of the detail pane.
// The calendar name is pinned by the shell via SetDetailTitle.
func calendarDetailLines(info CalendarInfo, w, labelWidth int) []string {
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string

	dot := "●"
	if info.Color != "" {
		dot = lipgloss.NewStyle().Foreground(lipgloss.Color(info.Color)).Render("●")
	}
	colorVal := dot
	if info.Color != "" {
		colorVal = dot + "  " + info.Color
	}
	lines = append(lines, detailLine(faint, "Color", colorVal, labelWidth, w))

	if info.OwnerEmail != "" {
		lines = append(lines, detailLine(faint, "Owner", info.OwnerEmail, labelWidth, w))
	}

	lines = append(lines, detailLine(faint, "Events", formatEventCount(info.EventCount), labelWidth, w))

	lines = append(lines, detailLine(faint, "Source", formatCalendarSource(info.Synced), labelWidth, w))

	if info.Synced {
		lines = append(lines, detailLine(faint, "Last sync", formatSyncTimestamp(info.LastSyncAt), labelWidth, w))
		if info.LastSyncError != "" {
			errStyle := lipgloss.NewStyle().Foreground(activeTheme.Error)
			lines = append(lines, detailLine(faint, "Error", errStyle.Render(info.LastSyncError), labelWidth, w))
		}
	}

	if !info.CreatedAt.IsZero() {
		lines = append(lines, detailLine(faint, "Created", formatCalendarDate(info.CreatedAt), labelWidth, w))
	}
	if !info.UpdatedAt.IsZero() && !info.UpdatedAt.Equal(info.CreatedAt) {
		lines = append(lines, detailLine(faint, "Updated", formatCalendarDate(info.UpdatedAt), labelWidth, w))
	}

	if info.Description != "" {
		lines = append(lines, "")
		for raw := range strings.SplitSeq(info.Description, "\n") {
			lines = append(lines, wrapLine(raw, w)...)
		}
	}

	return lines
}

func formatEventCount(n int64) string {
	switch n {
	case 0:
		return "No events"
	case 1:
		return "1 event"
	default:
		return fmt.Sprintf("%d events", n)
	}
}

func formatCalendarSource(synced bool) string {
	if synced {
		return "CalDAV"
	}
	return "Local only"
}

func formatSyncTimestamp(ts string) string {
	if ts == "" {
		return "Never"
	}
	t, err := time.Parse(time.RFC3339, ts)
	if err != nil {
		return ts
	}
	rel := formatRelativeTime(time.Since(t))
	abs := t.Local().Format("2006-01-02 15:04")
	return fmt.Sprintf("%s (%s)", rel, abs)
}

func formatCalendarDate(t time.Time) string {
	return t.Local().Format("2006-01-02")
}

// formatRelativeTime clamps negative durations (future timestamps from clock
// skew) to "just now" so they don't render as nonsense like "-3m ago".
func formatRelativeTime(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	case d < 7*24*time.Hour:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	case d < 30*24*time.Hour:
		return fmt.Sprintf("%dw ago", int(d.Hours()/(24*7)))
	case d < 365*24*time.Hour:
		return fmt.Sprintf("%dmo ago", int(d.Hours()/(24*30)))
	default:
		return fmt.Sprintf("%dy ago", int(d.Hours()/(24*365)))
	}
}
