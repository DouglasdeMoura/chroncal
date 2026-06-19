package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// EventDialogClosedMsg is emitted when the dialog requests to close.
type EventDialogClosedMsg struct{}

// DialogDayChangedMsg is emitted when the user navigates to another day from the dialog.
type DialogDayChangedMsg struct{ Day time.Time }

// EventEditMsg is emitted when the user requests to edit the selected event.
type EventEditMsg struct{ Event event.Event }

// EventDuplicateMsg is emitted when the user requests to duplicate the selected event.
type EventDuplicateMsg struct{ Event event.Event }

// EventDeleteMsg is emitted when the user requests to delete the selected event.
type EventDeleteMsg struct{ Event event.Event }

// EventCreateMsg is emitted when the user requests to create an event on a day.
type EventCreateMsg struct {
	Day time.Time
}

// EventRSVPMsg is emitted when the user changes their RSVP status.
type EventRSVPMsg struct {
	Event  event.Event
	Status string // "ACCEPTED", "DECLINED", "TENTATIVE"
}

type eventDialogKeyMap struct {
	Left      key.Binding
	Right     key.Binding
	Edit      key.Binding
	Delete    key.Binding
	Duplicate key.Binding
	Create    key.Binding
	RSVPYes   key.Binding
	RSVPNo    key.Binding
	RSVPMaybe key.Binding
}

func defaultEventDialogKeys() eventDialogKeyMap {
	return eventDialogKeyMap{
		Left:      key.NewBinding(key.WithKeys("left", "h"), key.WithHelp("←/h", "previous")),
		Right:     key.NewBinding(key.WithKeys("right", "l"), key.WithHelp("→/l", "next")),
		Edit:      key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Delete:    key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		Duplicate: key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "duplicate")),
		Create:    key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "create")),
		RSVPYes:   key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "RSVP yes")),
		RSVPNo:    key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "RSVP no")),
		RSVPMaybe: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "RSVP maybe")),
	}
}

// dialogAction is the RSVP button representation shared with
// event_view_dialog.go.
type dialogAction struct {
	label string
	msg   func() tea.Msg
}

// CalendarInfo holds the display-relevant fields of a calendar.
type CalendarInfo struct {
	Name        string
	Color       string
	OwnerEmail  string
	Description string
	EventCount  int64
	// DisplayOrder is the persisted sidebar sort position (lower sorts first).
	DisplayOrder int64
	// Synced reports whether the calendar is linked to a CalDAV account.
	// Drives opportunistic save-time push: local-only calendars skip it.
	Synced bool
	// AccountServerURL is the linked CalDAV account's principal URL, used by
	// the event view to detect Google-hosted calendars so meeting links can
	// pre-select the right account. Empty for local-only calendars.
	AccountServerURL    string
	LastSyncAt          string // RFC 3339, empty when never synced
	LastSyncAttemptedAt string // RFC 3339, empty when never attempted
	LastSyncError       string
	CreatedAt           time.Time
	UpdatedAt           time.Time
	IsDefault           bool
}

const narrowThreshold = 90

// EventDialogModel shows a day's events in a two-column dialog. It is a thin
// wrapper around ListDialogModel that owns the event-specific state
// (selected day, sorted events) and the RSVP row, which is composed into
// the detail lines rather than handled by the shell.
type EventDialogModel struct {
	shell ListDialogModel
	day   time.Time
	// events is the sorted list shown in the dialog.
	// eventLabels is the per-row "HH:MM  Title" string pre-formatted once
	// per events change. Refresh restyles only the selected row instead of
	// rebuilding every label per keystroke — a measurable win when the
	// dialog has dozens of events.
	events      []event.Event
	eventLabels []string
	calendars   map[int64]CalendarInfo
	keys        eventDialogKeyMap
	focusedRSVP int
	rsvpFocused bool
}

// buildEventLabels precomputes the unstyled row labels for the current
// events slice. Called from the constructor and SetEvents so refresh
// can reuse the labels across navigation keystrokes.
func (m EventDialogModel) buildEventLabels() EventDialogModel {
	m.eventLabels = make([]string, len(m.events))
	for i, ev := range m.events {
		m.eventLabels[i] = formatEventLabel(ev)
	}
	return m
}

func NewEventDialogModel(day time.Time, events []event.Event, calendars map[int64]CalendarInfo, h help.Model) EventDialogModel {
	slices.SortStableFunc(events, func(a, b event.Event) int {
		if a.AllDay != b.AllDay {
			if a.AllDay {
				return -1
			}
			return 1
		}
		return a.StartTime.Compare(b.StartTime)
	})
	newAction := ListDialogAction{
		Label:   "Create Event",
		Primary: true,
		Msg:     func() tea.Msg { return EventCreateMsg{Day: day} },
	}
	m := EventDialogModel{
		shell: NewListDialogModel(h).
			SetTitle(day.Format("Monday, January 2, 2006")).
			SetTitleAction(&newAction),
		day:       day,
		events:    events,
		calendars: calendars,
		keys:      defaultEventDialogKeys(),
	}
	m = m.buildEventLabels()
	return m.refresh()
}

func (m EventDialogModel) SetSize(w, h int) EventDialogModel {
	m.shell = m.shell.SetSize(w, h)
	return m.refresh()
}

func (m EventDialogModel) SetSelectedColor(c color.Color) EventDialogModel {
	m.shell = m.shell.SetSelectedColor(c)
	return m
}

func (m EventDialogModel) SetEvents(events []event.Event) EventDialogModel {
	m.events = events
	m = m.buildEventLabels()
	if sel := m.shell.Selected(); sel >= len(events) {
		m.shell = m.shell.SetSelected(max(0, len(events)-1))
	}
	return m.refresh()
}

func (m EventDialogModel) BoxSize() (int, int) { return m.shell.BoxSize() }

func (m EventDialogModel) View() string { return m.shell.View() }

func (m EventDialogModel) selectedEvent() (event.Event, bool) {
	idx := m.shell.Selected()
	if len(m.events) == 0 || idx < 0 || idx >= len(m.events) {
		return event.Event{}, false
	}
	return m.events[idx], true
}

func (m EventDialogModel) userAttendee() (model.Attendee, bool) {
	ev, ok := m.selectedEvent()
	if !ok {
		return model.Attendee{}, false
	}
	cal := m.calendars[ev.CalendarID]
	if cal.OwnerEmail == "" {
		return model.Attendee{}, false
	}
	for _, att := range ev.Attendees {
		if strings.EqualFold(att.Email, cal.OwnerEmail) && !att.Organizer {
			return att, true
		}
	}
	return model.Attendee{}, false
}

func (m EventDialogModel) rsvpActions() []dialogAction {
	ev, ok := m.selectedEvent()
	if !ok {
		return nil
	}
	if _, ok := m.userAttendee(); !ok {
		return nil
	}
	return []dialogAction{
		{label: "Yes", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "ACCEPTED"} }},
		{label: "No", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "DECLINED"} }},
		{label: "Maybe", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "TENTATIVE"} }},
	}
}

func (m EventDialogModel) actions() []ListDialogAction {
	ev, ok := m.selectedEvent()
	if !ok {
		return nil
	}
	return []ListDialogAction{
		{Label: "Edit", Msg: func() tea.Msg { return EventEditMsg{Event: ev} }},
		{Label: "Duplicate", Msg: func() tea.Msg { return EventDuplicateMsg{Event: ev} }},
		{Label: "Delete", Danger: true, Msg: func() tea.Msg { return EventDeleteMsg{Event: ev} }},
	}
}

func (m EventDialogModel) labelWidth() int {
	if m.shell.isNarrow() {
		return 7
	}
	return 10
}

func (m EventDialogModel) detailWidth() int {
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

// listRowWidth mirrors the shell's list-column math so a selected row can
// extend its reverse-video background to the right edge.
func (m EventDialogModel) listRowWidth() int {
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

// refresh rebuilds the shell's rows, detail lines, actions, and help row
// from the current day, events, and focus state.
func (m EventDialogModel) refresh() EventDialogModel {
	rows := make([]string, len(m.events))
	sel := m.shell.Selected()
	listFocused := m.shell.FocusZone() == ListZoneList
	rowW := m.listRowWidth()
	selBG := m.shell.SelectedColor()
	// Reuse pre-formatted labels (built once per events change in
	// buildEventLabels). Only the selected row gets re-styled per
	// keystroke; the other rows are direct pointer copies.
	if len(m.eventLabels) != len(m.events) {
		m = m.buildEventLabels()
	}
	copy(rows, m.eventLabels)
	if sel >= 0 && sel < len(rows) {
		label := rows[sel]
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
		rows[sel] = style.Render(label)
	}
	m.shell = m.shell.SetRows(rows)

	w := m.detailWidth()
	if ev, ok := m.selectedEvent(); ok {
		cal := m.calendars[ev.CalendarID]
		rsvpLine := ""
		if rsvp := m.rsvpActions(); len(rsvp) > 0 {
			att, _ := m.userAttendee()
			rsvpLine = m.renderRSVPLine(att, rsvp, w)
		}
		lines, _ := eventDetailLines(ev, cal, w, m.labelWidth(), rsvpLine)
		m.shell = m.shell.SetDetailTitle(ev.Title).SetDetailLines(lines)
	} else {
		m.shell = m.shell.SetDetailTitle("").SetDetailLines(nil)
	}

	if len(m.events) == 0 {
		faint := lipgloss.NewStyle().Faint(true)
		m.shell = m.shell.SetEmptyList("", []string{faint.Render("No events on this day.")})
		m.shell = m.shell.SetActions(nil)
	} else {
		m.shell = m.shell.SetActions(m.actions())
	}

	m.shell = m.shell.SetShortHelp(m.shortHelp())
	return m
}

func (m EventDialogModel) shortHelp() []key.Binding {
	sk := m.shell.Keys()
	nav := key.NewBinding(
		key.WithKeys("up", "down", "k", "j"),
		key.WithHelp("↑↓", "navigate"),
	)
	days := key.NewBinding(
		key.WithKeys("left", "right", "h", "l"),
		key.WithHelp("←→", "days"),
	)
	if len(m.events) == 0 {
		return []key.Binding{days, sk.Enter, m.keys.Create, sk.Close}
	}
	return []key.Binding{nav, days, sk.Tab, m.keys.Create, m.keys.Delete, sk.Close}
}

// Focus-zone IDs used by currentZone/setZone. Kept as ints so existing
// switch-based callers in handleKey stay unchanged.
const (
	zoneList        = 0
	zoneRSVP        = 1
	zoneAction      = 2
	zoneTitleAction = 3
)

func (m EventDialogModel) currentZone() int {
	if m.rsvpFocused {
		return zoneRSVP
	}
	switch m.shell.FocusZone() {
	case ListZoneActions:
		return zoneAction
	case ListZoneTitleAction:
		return zoneTitleAction
	case ListZoneList, ListZoneCustom:
		return zoneList
	}
	return zoneList
}

// setZone moves focus to the head of a zone. Used by domain hotkeys (y/m for
// RSVP, e/d/t for actions) and mouse clicks; Tab uses focusStop below to
// land on a specific element.
func (m EventDialogModel) setZone(z int) EventDialogModel {
	switch z {
	case zoneList:
		m.rsvpFocused = false
		m.shell = m.shell.SetFocusZone(ListZoneList)
	case zoneRSVP:
		m.rsvpFocused = true
		m.shell = m.shell.SetFocusZone(ListZoneCustom)
	case zoneAction:
		m.rsvpFocused = false
		m.shell = m.shell.FocusAction(0)
	}
	return m
}

// tabStop identifies one focusable control in the dialog. Tab/Shift+Tab
// walk every stop in order so keyboard navigation reaches each element the
// way a web page's tab order would — list → each RSVP button → each action
// button → title action.
type tabStop struct {
	kind int // 0=list, 1=rsvp button, 2=action button, 3=title action
	idx  int
}

func (m EventDialogModel) tabOrder() []tabStop {
	stops := []tabStop{{kind: zoneList}}
	for i := range m.rsvpActions() {
		stops = append(stops, tabStop{kind: zoneRSVP, idx: i})
	}
	for i := range m.actions() {
		stops = append(stops, tabStop{kind: zoneAction, idx: i})
	}
	if m.shell.HasTitleAction() {
		stops = append(stops, tabStop{kind: zoneTitleAction})
	}
	return stops
}

func (m EventDialogModel) currentStop() tabStop {
	if m.rsvpFocused {
		return tabStop{kind: zoneRSVP, idx: m.focusedRSVP}
	}
	switch m.shell.FocusZone() {
	case ListZoneActions:
		return tabStop{kind: zoneAction, idx: m.shell.FocusedAction()}
	case ListZoneTitleAction:
		return tabStop{kind: zoneTitleAction}
	case ListZoneList, ListZoneCustom:
		return tabStop{kind: zoneList}
	}
	return tabStop{kind: zoneList}
}

func (m EventDialogModel) focusStop(s tabStop) EventDialogModel {
	switch s.kind {
	case zoneList:
		m.rsvpFocused = false
		m.shell = m.shell.SetFocusZone(ListZoneList)
	case zoneRSVP:
		m.rsvpFocused = true
		m.focusedRSVP = s.idx
		m.shell = m.shell.SetFocusZone(ListZoneCustom)
	case zoneAction:
		m.rsvpFocused = false
		m.shell = m.shell.FocusAction(s.idx)
	case zoneTitleAction:
		m.rsvpFocused = false
		m.shell = m.shell.SetFocusZone(ListZoneTitleAction)
	}
	return m
}

func (m EventDialogModel) cycleZone(forward bool) EventDialogModel {
	order := m.tabOrder()
	if len(order) <= 1 {
		return m
	}
	cur := m.currentStop()
	idx := 0
	for i, s := range order {
		if s == cur {
			idx = i
			break
		}
	}
	delta := 1
	if !forward {
		delta = -1
	}
	return m.focusStop(order[(idx+delta+len(order))%len(order)])
}

func (m EventDialogModel) Update(msg tea.Msg) (EventDialogModel, tea.Cmd) {
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

func (m EventDialogModel) handleKey(msg tea.KeyPressMsg) (EventDialogModel, tea.Cmd) {
	sk := m.shell.Keys()
	rsvp := m.rsvpActions()
	acts := m.actions()

	switch {
	case key.Matches(msg, sk.Close):
		return m, func() tea.Msg { return EventDialogClosedMsg{} }

	case key.Matches(msg, sk.Tab):
		return m.cycleZone(true).refresh(), nil
	case key.Matches(msg, sk.ShiftTab):
		return m.cycleZone(false).refresh(), nil

	case key.Matches(msg, m.keys.Left):
		switch m.currentZone() {
		case zoneList:
			prev := m.day.AddDate(0, 0, -1)
			return m, func() tea.Msg { return DialogDayChangedMsg{Day: prev} }
		case zoneRSVP:
			if len(rsvp) > 0 {
				m.focusedRSVP = (m.focusedRSVP - 1 + len(rsvp)) % len(rsvp)
				return m.refresh(), nil
			}
		case zoneAction:
			if n := len(acts); n > 0 {
				m.shell = m.shell.FocusAction((m.shell.FocusedAction() - 1 + n) % n)
			}
		}
		return m, nil

	case key.Matches(msg, m.keys.Right):
		switch m.currentZone() {
		case zoneList:
			next := m.day.AddDate(0, 0, 1)
			return m, func() tea.Msg { return DialogDayChangedMsg{Day: next} }
		case zoneRSVP:
			if len(rsvp) > 0 {
				m.focusedRSVP = (m.focusedRSVP + 1) % len(rsvp)
				return m.refresh(), nil
			}
		case zoneAction:
			if n := len(acts); n > 0 {
				m.shell = m.shell.FocusAction((m.shell.FocusedAction() + 1) % n)
			}
		}
		return m, nil

	case key.Matches(msg, sk.Up):
		if m.currentZone() == zoneList {
			m.shell = m.shell.MoveUp()
			return m.refresh(), nil
		}
		m.shell = m.shell.ScrollDetailsUp()
		return m, nil

	case key.Matches(msg, sk.Down):
		if m.currentZone() == zoneList {
			m.shell = m.shell.MoveDown()
			return m.refresh(), nil
		}
		m.shell = m.shell.ScrollDetailsDown()
		return m, nil

	case key.Matches(msg, sk.Enter):
		switch m.currentZone() {
		case zoneList:
			if len(m.events) == 0 {
				day := m.day
				return m, func() tea.Msg { return EventCreateMsg{Day: day} }
			}
		case zoneRSVP:
			if m.focusedRSVP >= 0 && m.focusedRSVP < len(rsvp) {
				return m, rsvp[m.focusedRSVP].msg
			}
		case zoneAction, zoneTitleAction:
			return m, m.shell.ActivateFocused()
		}
		return m, nil

	case key.Matches(msg, m.keys.Edit):
		if _, ok := m.selectedEvent(); ok && len(acts) > 0 {
			m.shell = m.shell.FocusAction(0)
			m.rsvpFocused = false
			return m, acts[0].Msg
		}
	case key.Matches(msg, m.keys.Duplicate):
		if _, ok := m.selectedEvent(); ok && len(acts) > 1 {
			m.shell = m.shell.FocusAction(1)
			m.rsvpFocused = false
			return m, acts[1].Msg
		}
	case key.Matches(msg, m.keys.Delete):
		if _, ok := m.selectedEvent(); ok && len(acts) > 2 {
			m.shell = m.shell.FocusAction(2)
			m.rsvpFocused = false
			return m, acts[2].Msg
		}
	case key.Matches(msg, m.keys.Create):
		day := m.day
		return m, func() tea.Msg { return EventCreateMsg{Day: day} }
	case key.Matches(msg, m.keys.RSVPYes):
		if len(rsvp) > 0 {
			m = m.setZone(zoneRSVP)
			m.focusedRSVP = 0
			return m.refresh(), rsvp[0].msg
		}
	case key.Matches(msg, m.keys.RSVPMaybe):
		if len(rsvp) > 2 {
			m = m.setZone(zoneRSVP)
			m.focusedRSVP = 2
			return m.refresh(), rsvp[2].msg
		}
	}
	return m, nil
}

func (m EventDialogModel) handleMouse(msg tea.MouseClickMsg) (EventDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	if cmd, ok := m.shell.TitleActionAtPosition(msg.X, msg.Y); ok {
		return m, cmd
	}

	if len(m.events) == 0 {
		return m, nil
	}

	if idx, ok := m.shell.RowAtPosition(msg.X, msg.Y); ok {
		m.shell = m.shell.ClickRow(idx)
		m.rsvpFocused = false
		return m.refresh(), nil
	}
	if idx, ok := m.shell.ActionAtPosition(msg.X, msg.Y); ok {
		shell, cmd := m.shell.ClickAction(idx)
		m.shell = shell
		m.rsvpFocused = false
		return m.refresh(), cmd
	}
	if idx, cmd, hit := m.hitRSVPBtn(msg.X, msg.Y); hit {
		m = m.setZone(zoneRSVP)
		m.focusedRSVP = idx
		return m.refresh(), cmd
	}
	return m, nil
}

func (m EventDialogModel) hitRSVPBtn(x, y int) (int, tea.Cmd, bool) {
	rsvp := m.rsvpActions()
	if len(rsvp) == 0 {
		return 0, nil, false
	}
	ev, ok := m.selectedEvent()
	if !ok {
		return 0, nil, false
	}
	cal := m.calendars[ev.CalendarID]
	att, _ := m.userAttendee()
	rsvpLine := m.renderRSVPLine(att, rsvp, m.detailWidth())
	_, rowIdx := eventDetailLines(ev, cal, m.detailWidth(), m.labelWidth(), rsvpLine)
	if rowIdx < 0 {
		return 0, nil, false
	}

	rsvpY, ok := m.shell.BodyRowScreenY(rowIdx)
	if !ok {
		return 0, nil, false
	}
	ox, _ := m.shell.DetailsOrigin()
	rsvpX := ox + labelColWidth("Your RSVP", m.labelWidth())
	btnW := rsvpButtonWidth()

	if y != rsvpY {
		return 0, nil, false
	}
	cx := rsvpX
	for i, a := range rsvp {
		if x >= cx && x < cx+btnW {
			return i, a.msg, true
		}
		cx += btnW + 1
	}
	return 0, nil, false
}

// renderRSVPLine renders the "Your RSVP  [Yes] [No] [Maybe]" row with the
// focused button highlighted when the RSVP zone owns focus.
func (m EventDialogModel) renderRSVPLine(att model.Attendee, rsvp []dialogAction, w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	lw := m.labelWidth()

	label := "Your RSVP"
	padded := strings.Repeat(" ", max(lw-len(label), 0)) + label + "  "

	fixedW := rsvpMaxLabelWidth()
	parts := make([]string, 0, len(rsvp))
	for i, a := range rsvp {
		l := rsvpButtonLabel(a.label, att.RSVPStatus)
		if pad := fixedW - lipgloss.Width(l); pad > 0 {
			leftPad := pad / 2
			right := pad - leftPad
			l = strings.Repeat(" ", leftPad) + l + strings.Repeat(" ", right)
		}
		parts = append(parts, DefaultButtonStyles().Normal.Render(l, m.rsvpFocused && i == m.focusedRSVP))
	}
	value := strings.Join(parts, " ")

	return truncateTo(faint.Render(padded)+value, w)
}

// --- Domain-formatting helpers (used here and by event_view_dialog.go) ---

var rsvpIndicators = map[string]string{
	"ACCEPTED":  "Yes ✓",
	"DECLINED":  "No ✗",
	"TENTATIVE": "Maybe ?",
}

func rsvpMaxLabelWidth() int {
	maxW := 0
	for _, v := range rsvpIndicators {
		if w := lipgloss.Width(v); w > maxW {
			maxW = w
		}
	}
	return maxW
}

func rsvpButtonWidth() int {
	return rsvpMaxLabelWidth() + 2 // +2 for button padding
}

func rsvpButtonLabel(baseLabel, rsvpStatus string) string {
	if mapped, ok := rsvpIndicators[strings.ToUpper(rsvpStatus)]; ok && strings.HasPrefix(mapped, baseLabel) {
		return mapped
	}
	return baseLabel
}

// eventDetailLines returns detail lines and the index of the RSVP row (-1 if none).
// The event title is pinned by the shell via SetDetailTitle, so callers must
// not prepend it here — these lines scroll, the title does not.
func eventDetailLines(ev event.Event, cal CalendarInfo, w, labelWidth int, rsvpLine string) ([]string, int) {
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, detailLine(faint, "When", formatWhen(ev), labelWidth, w))

	dur := formatDuration(ev)
	if dur != "" {
		lines = append(lines, detailLine(faint, "Duration", dur, labelWidth, w))
	}

	if cal.Name != "" {
		dot := "●"
		if cal.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("●")
		}
		lines = append(lines, detailLine(faint, "Calendar", dot+" "+cal.Name, labelWidth, w))
	}

	if ev.Location != "" {
		lines = append(lines, detailLine(faint, "Where", ev.Location, labelWidth, w))
	}
	if ev.Status != "" {
		lines = append(lines, detailLine(faint, "Status", statusBadge(ev.Status), labelWidth, w))
	}
	if ev.Categories != "" {
		lines = append(lines, detailLine(faint, "Tags", ev.Categories, labelWidth, w))
	}
	if ev.URL != "" {
		lines = append(lines, detailLine(faint, "URL", ev.URL, labelWidth, w))
	}
	if ev.ConferenceURI != "" {
		lines = append(lines, detailLine(faint, "Conference", ev.ConferenceURI, labelWidth, w))
	}
	rsvpIdx := -1
	if rsvpLine != "" {
		rsvpIdx = len(lines)
		lines = append(lines, rsvpLine)
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
		lines = append(lines, descriptionLines(ev.Description, w, nil, false)...)
	}

	return lines, rsvpIdx
}

func detailLine(labelStyle lipgloss.Style, label, value string, lw, w int) string {
	padded := strings.Repeat(" ", max(lw-len(label), 0)) + label
	return truncateTo(labelStyle.Render(padded)+"  "+value, w)
}

// labelColWidth returns the on-screen cell width consumed by the label column
// plus its two-space gap, accounting for labels that exceed the nominal lw.
func labelColWidth(label string, lw int) int {
	return max(lw, len(label)) + 2
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
	out := "  " + name + " " + attendeeStatusSymbol(att.RSVPStatus)
	if att.Organizer {
		out += "  " + badge("organizer", badgeInfo)
	}
	return out
}

func attendeeStatusSymbol(status string) string {
	switch strings.ToUpper(status) {
	case "ACCEPTED":
		return lipgloss.NewStyle().Foreground(badgeBackground(badgeOK)).Render("✓")
	case "DECLINED":
		return lipgloss.NewStyle().Foreground(badgeBackground(badgeDanger)).Render("✗")
	case "TENTATIVE":
		return lipgloss.NewStyle().Foreground(badgeBackground(badgeWarn)).Render("?")
	default:
		return lipgloss.NewStyle().Faint(true).Render("○")
	}
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
		parts = append(parts, pluralize(n, "min."))
		raw = rest
	}
	if n, _, ok := parseLeadingInt(raw, 'S'); ok {
		parts = append(parts, pluralize(n, "sec."))
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
	if w == 1 {
		return "…"
	}

	plain := s
	if strings.ContainsRune(s, '\x1b') {
		plain = stripANSI(s)
	}
	r := []rune(plain)
	cut := min(w-1, len(r))

	// Prefer breaking at whitespace within a small look-back window so
	// truncation doesn't slice mid-word. If no whitespace is within reach
	// (e.g., a single long token), fall back to a hard cut.
	lookback := cut / 3
	for i := cut; i > cut-lookback && i > 1; i-- {
		if r[i-1] == ' ' || r[i-1] == '\t' {
			trimmed := strings.TrimRight(string(r[:i-1]), " \t")
			return trimmed + " …"
		}
	}
	return string(r[:cut]) + "…"
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

// padLines normalizes lines into exactly h rows, each w cells wide.
// Avoids lipgloss.NewStyle().Width(w).Render — that path wraps and
// re-measures each line through lipgloss's full layout machinery,
// which is ~30µs/line and adds up on dense dialogs. Plain
// measurement + space padding gives the same visual result.
func padLines(lines []string, w, h int) string {
	if w <= 0 {
		if h <= 0 {
			return ""
		}
		return strings.Repeat("\n", h-1)
	}
	blank := strings.Repeat(" ", w)
	var b strings.Builder
	b.Grow((w + 1) * h)
	for i := 0; i < h; i++ {
		if i > 0 {
			b.WriteByte('\n')
		}
		if i >= len(lines) {
			b.WriteString(blank)
			continue
		}
		l := lines[i]
		cw := lipgloss.Width(l)
		switch {
		case cw == w:
			b.WriteString(l)
		case cw < w:
			b.WriteString(l)
			b.WriteString(strings.Repeat(" ", w-cw))
		default:
			t := truncateTo(l, w)
			b.WriteString(t)
			if tw := lipgloss.Width(t); tw < w {
				b.WriteString(strings.Repeat(" ", w-tw))
			}
		}
	}
	return b.String()
}
