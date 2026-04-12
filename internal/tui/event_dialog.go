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

// DialogDayChangedMsg is emitted when the user navigates to another day from the dialog.
type DialogDayChangedMsg struct{ Day time.Time }

// EventEditMsg is emitted when the user requests to edit the selected event.
type EventEditMsg struct{ Event event.Event }

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
	Up        key.Binding
	Down      key.Binding
	Left      key.Binding
	Right     key.Binding
	Close     key.Binding
	Edit      key.Binding
	Delete    key.Binding
	RSVPYes   key.Binding
	RSVPNo    key.Binding
	RSVPMaybe key.Binding
	Tab       key.Binding
	ShiftTab  key.Binding
	Enter     key.Binding
}

func defaultEventDialogKeys() eventDialogKeyMap {
	return eventDialogKeyMap{
		Up:        key.NewBinding(key.WithKeys("up", "k")),
		Down:      key.NewBinding(key.WithKeys("down", "j")),
		Left:      key.NewBinding(key.WithKeys("left", "h")),
		Right:     key.NewBinding(key.WithKeys("right", "l")),
		Close:     key.NewBinding(key.WithKeys("esc", "q")),
		Edit:      key.NewBinding(key.WithKeys("e")),
		Delete:    key.NewBinding(key.WithKeys("d")),
		RSVPYes:   key.NewBinding(key.WithKeys("y")),
		RSVPNo:    key.NewBinding(key.WithKeys("n")),
		RSVPMaybe: key.NewBinding(key.WithKeys("m")),
		Tab:       key.NewBinding(key.WithKeys("tab")),
		ShiftTab:  key.NewBinding(key.WithKeys("shift+tab")),
		Enter:     key.NewBinding(key.WithKeys("enter", " ")),
	}
}

type dialogAction struct {
	label string
	msg   func() tea.Msg
}

// CalendarInfo holds the display-relevant fields of a calendar.
type CalendarInfo struct {
	Name       string
	Color      string
	OwnerEmail string
}

// EventDialogModel shows a day's events in a two-column dialog: a list on
// the left and the selected event's details on the right. On narrow screens
// it switches to a stacked single-column layout.
type EventDialogModel struct {
	day           time.Time
	events        []event.Event
	calendars     map[int64]CalendarInfo
	selected      int
	scroll        int
	focusedAction int
	focusedRSVP   int
	focusZone     int // 0 = action bar, 1 = RSVP buttons
	rsvpLineIdx   int // line index of RSVP row within details (set during render)
	keys          eventDialogKeyMap
	width         int
	height        int
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

func (m EventDialogModel) SetEvents(events []event.Event) EventDialogModel {
	m.events = events
	if m.selected >= len(m.events) {
		m.selected = max(0, len(m.events)-1)
	}
	m.clampFocus()
	return m
}

func (m EventDialogModel) selectedEvent() (event.Event, bool) {
	if len(m.events) == 0 || m.selected < 0 || m.selected >= len(m.events) {
		return event.Event{}, false
	}
	return m.events[m.selected], true
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
		{"Yes", func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "ACCEPTED"} }},
		{"No", func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "DECLINED"} }},
		{"Maybe", func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "TENTATIVE"} }},
	}
}

func (m EventDialogModel) visibleActions() []dialogAction {
	ev, ok := m.selectedEvent()
	if !ok {
		return nil
	}
	return []dialogAction{
		{"Edit", func() tea.Msg { return EventEditMsg{Event: ev} }},
		{"Delete", func() tea.Msg { return EventDeleteMsg{Event: ev} }},
	}
}

func (m *EventDialogModel) clampFocus() {
	n := len(m.visibleActions())
	if m.focusedAction >= n {
		m.focusedAction = max(n-1, 0)
	}
	rn := len(m.rsvpActions())
	if rn == 0 {
		m.focusZone = 0
	} else if m.focusZone == 0 && m.focusedAction == 0 {
		m.focusZone = 1
		m.focusedRSVP = 0
	}
	if m.focusedRSVP >= rn {
		m.focusedRSVP = max(rn-1, 0)
	}
}

func (m EventDialogModel) allFocusable() int {
	return len(m.visibleActions()) + len(m.rsvpActions())
}

func (m EventDialogModel) flatFocusIndex() int {
	if m.focusZone == 1 {
		return m.focusedRSVP
	}
	return len(m.rsvpActions()) + m.focusedAction
}

func (m *EventDialogModel) setFlatFocusIndex(idx int) {
	rn := len(m.rsvpActions())
	if rn > 0 && idx < rn {
		m.focusZone = 1
		m.focusedRSVP = idx
	} else {
		m.focusZone = 0
		m.focusedAction = idx - rn
	}
}

func (m EventDialogModel) Update(msg tea.Msg) (EventDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	}
	return m, nil
}

func (m EventDialogModel) handleKey(msg tea.KeyPressMsg) (EventDialogModel, tea.Cmd) {
	actions := m.visibleActions()
	rsvp := m.rsvpActions()
	total := len(actions) + len(rsvp)

	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return EventDialogClosedMsg{} }
	case key.Matches(msg, m.keys.Left):
		prev := m.day.AddDate(0, 0, -1)
		return m, func() tea.Msg { return DialogDayChangedMsg{Day: prev} }
	case key.Matches(msg, m.keys.Right):
		next := m.day.AddDate(0, 0, 1)
		return m, func() tea.Msg { return DialogDayChangedMsg{Day: next} }
	case key.Matches(msg, m.keys.Up):
		if m.selected > 0 {
			m.selected--
			m.clampFocus()
		}
	case key.Matches(msg, m.keys.Down):
		if m.selected < len(m.events)-1 {
			m.selected++
			m.clampFocus()
		}
	case key.Matches(msg, m.keys.Tab):
		if total > 0 {
			m.setFlatFocusIndex((m.flatFocusIndex() + 1) % total)
		}
	case key.Matches(msg, m.keys.ShiftTab):
		if total > 0 {
			m.setFlatFocusIndex((m.flatFocusIndex() - 1 + total) % total)
		}
	case key.Matches(msg, m.keys.Enter):
		if len(m.events) == 0 {
			day := m.day
			return m, func() tea.Msg { return EventCreateMsg{Day: day} }
		}
		if m.focusZone == 1 && m.focusedRSVP >= 0 && m.focusedRSVP < len(rsvp) {
			return m, rsvp[m.focusedRSVP].msg
		}
		if m.focusZone == 0 && m.focusedAction >= 0 && m.focusedAction < len(actions) {
			return m, actions[m.focusedAction].msg
		}
	case key.Matches(msg, m.keys.Edit):
		if _, ok := m.selectedEvent(); ok && len(actions) > 0 {
			m.setFlatFocusIndex(len(rsvp))
			return m, actions[0].msg
		}
	case key.Matches(msg, m.keys.Delete):
		if _, ok := m.selectedEvent(); ok && len(actions) > 1 {
			m.setFlatFocusIndex(len(rsvp) + 1)
			return m, actions[1].msg
		}
	case key.Matches(msg, m.keys.RSVPYes):
		if len(rsvp) > 0 {
			m.setFlatFocusIndex(0)
			return m, rsvp[0].msg
		}
	case key.Matches(msg, m.keys.RSVPNo):
		if len(rsvp) > 1 {
			m.setFlatFocusIndex(1)
			return m, rsvp[1].msg
		}
	case key.Matches(msg, m.keys.RSVPMaybe):
		if len(rsvp) > 2 {
			m.setFlatFocusIndex(2)
			return m, rsvp[2].msg
		}
	}
	return m, nil
}

func (m EventDialogModel) handleMouse(msg tea.MouseClickMsg) (EventDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}

	if len(m.events) == 0 {
		ox, oy := m.createBtnOrigin()
		btnW := lipgloss.Width(button("Create Event", 0, false))
		if msg.Y == oy && msg.X >= ox && msg.X < ox+btnW {
			day := m.day
			return m, func() tea.Msg { return EventCreateMsg{Day: day} }
		}
		return m, nil
	}

	// Check action bar (Edit/Delete)
	actions := m.visibleActions()
	ox, oy := m.actionBarOrigin()
	if msg.Y == oy {
		x := ox
		for i, a := range actions {
			w := len(a.label) + 2
			if msg.X >= x && msg.X < x+w {
				m.focusZone = 0
				m.focusedAction = i
				return m, a.msg
			}
			x += w + 1
		}
	}

	// Check RSVP buttons in details pane
	rsvp := m.rsvpActions()
	if len(rsvp) > 0 {
		rx, ry := m.rsvpBarOrigin()
		if msg.Y == ry {
			btnW := rsvpButtonWidth()
			x := rx
			for i, a := range rsvp {
				if msg.X >= x && msg.X < x+btnW {
					m.focusZone = 1
					m.focusedRSVP = i
					return m, a.msg
				}
				x += btnW + 1
			}
		}
	}

	return m, nil
}

func (m EventDialogModel) actionBarOrigin() (int, int) {
	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 10)
	innerH := max(boxH-4, 6)
	bodyH := max(innerH-4, 3)

	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - boxH) / 2

	// border(1) + padding(top:1, left:2)
	contentX := dialogX + 3
	actionsY := dialogY + bodyH + 3

	if m.isNarrow() {
		return contentX, actionsY
	}

	listW := max(min(max(innerW/4, 18), innerW-24), 10)
	dividerW := 3
	return contentX + listW + dividerW, actionsY
}

func (m EventDialogModel) rsvpBarOrigin() (int, int) {
	if m.rsvpLineIdx < 0 {
		return 0, 0
	}

	boxW, _ := m.boxSize()
	innerW := max(boxW-6, 10)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - m.boxH()) / 2

	contentX := dialogX + 3
	detailsStartY := dialogY + 2

	if m.isNarrow() {
		listH := min(max(len(m.events)+1, 3), max(m.bodyH()/3, 3))
		detailsStartY += listH + 1
	}

	lw := m.labelWidth()
	padded := "Your RSVP" + strings.Repeat(" ", max(lw-len("Your RSVP"), 1))
	rsvpButtonsX := contentX + len(padded)

	if !m.isNarrow() {
		listW := max(min(max(innerW/4, 18), innerW-24), 10)
		dividerW := 3
		rsvpButtonsX += listW + dividerW
	}

	return rsvpButtonsX, detailsStartY + m.rsvpLineIdx
}

func (m EventDialogModel) boxH() int {
	_, h := m.boxSize()
	return h
}

func (m EventDialogModel) bodyH() int {
	_, boxH := m.boxSize()
	innerH := max(boxH-4, 6)
	return max(innerH-4, 3)
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

	bodyH := max(innerH-4, 3)

	var help, body string

	if len(m.events) == 0 {
		help = lipgloss.NewStyle().
			Faint(true).
			Width(innerW).
			Render("←/→: day  ·  enter: create  ·  esc: close")
	} else {
		help = lipgloss.NewStyle().
			Faint(true).
			Width(innerW).
			Render("←/→: day  ·  ↑/↓: navigate  ·  esc: close")
	}

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

func (m EventDialogModel) createBtnOrigin() (int, int) {
	boxW, _ := m.boxSize()
	innerW := max(boxW-6, 10)
	dialogX := (m.width - boxW) / 2
	dialogY := (m.height - m.boxH()) / 2
	// border(1) + padding(top:1, left:2) + title(1) + blank(1) + "No events"(1) + blank(1)
	btnY := dialogY + 6

	if m.isNarrow() {
		listH := min(max(1, 3), max(m.bodyH()/3, 3))
		btnY = dialogY + 2 + listH + 1 + 2
		return dialogX + 3, btnY
	}

	listW := max(min(max(innerW/4, 18), innerW-24), 10)
	dividerW := 3
	return dialogX + 3 + listW + dividerW, btnY
}

func (m EventDialogModel) renderEmptyDetails(w, h int) string {
	faint := lipgloss.NewStyle().Faint(true)
	msg := faint.Render("No events on this day.")
	createBtn := button("Create Event", 0, true)
	lines := []string{msg, "", createBtn}
	return padLines(lines, w, h)
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

func button(text string, underlineIndex int, focused bool) string {
	bg := lipgloss.Color("240")
	if focused {
		bg = lipgloss.Color("63")
	}
	style := lipgloss.NewStyle().
		Background(bg).
		Foreground(lipgloss.Color("255"))

	rendered := style.Padding(0, 1).Render(text)
	if underlineIndex >= 0 && underlineIndex < len(text) {
		rendered = lipgloss.StyleRanges(rendered,
			lipgloss.NewRange(1+underlineIndex, 1+underlineIndex+1, style.Underline(true)))
	}
	return rendered
}

func (m EventDialogModel) renderActions(w int) string {
	actions := m.visibleActions()
	parts := make([]string, len(actions))
	for i, a := range actions {
		parts[i] = button(a.label, 0, m.focusZone == 0 && i == m.focusedAction)
	}
	return truncateTo(strings.Join(parts, " "), w)
}

func (m EventDialogModel) labelWidth() int {
	if m.isNarrow() {
		return 7
	}
	return 10
}

func (m EventDialogModel) renderDetails(w, h int) string {
	if len(m.events) == 0 {
		return m.renderEmptyDetails(w, h)
	}
	if m.selected < 0 || m.selected >= len(m.events) {
		return padLines(nil, w, h)
	}
	ev := m.events[m.selected]
	cal := m.calendars[ev.CalendarID]

	actionsLine := m.renderActions(w)
	detailsH := max(h-2, 1)

	rsvpLine := ""
	if rsvp := m.rsvpActions(); len(rsvp) > 0 {
		att, _ := m.userAttendee()
		rsvpLine = m.renderRSVPLine(att, rsvp, w)
	}

	lines, rsvpIdx := eventDetailLines(ev, cal, w, m.labelWidth(), rsvpLine)
	m.rsvpLineIdx = rsvpIdx

	if len(lines) > detailsH {
		lines = lines[:detailsH]
	}
	details := padLines(lines, w, detailsH)
	blank := strings.Repeat(" ", w)

	return details + "\n" + blank + "\n" + actionsLine
}

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

func (m EventDialogModel) renderRSVPLine(att model.Attendee, rsvp []dialogAction, w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	lw := m.labelWidth()

	label := "Your RSVP"
	padded := label + strings.Repeat(" ", max(lw-len(label), 1))

	fixedW := rsvpMaxLabelWidth()
	var parts []string
	for i, a := range rsvp {
		l := rsvpButtonLabel(a.label, att.RSVPStatus)
		leftPad := 0
		if pad := fixedW - lipgloss.Width(l); pad > 0 {
			leftPad = pad / 2
			right := pad - leftPad
			l = strings.Repeat(" ", leftPad) + l + strings.Repeat(" ", right)
		}
		parts = append(parts, button(l, leftPad, m.focusZone == 1 && i == m.focusedRSVP))
	}
	value := strings.Join(parts, " ")

	return truncateTo(faint.Render(padded)+value, w)
}

// eventDetailLines returns detail lines and the index of the RSVP row (-1 if none).
func eventDetailLines(ev event.Event, cal CalendarInfo, w, labelWidth int, rsvpLine string) ([]string, int) {
	faint := lipgloss.NewStyle().Faint(true)
	bold := lipgloss.NewStyle().Bold(true)

	var lines []string
	lines = append(lines, truncateTo(bold.Render(ev.Title), w))
	lines = append(lines, "")

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
		lines = append(lines, detailLine(faint, "Status", ev.Status, labelWidth, w))
	}
	if ev.Categories != "" {
		lines = append(lines, detailLine(faint, "Tags", ev.Categories, labelWidth, w))
	}
	if ev.URL != "" {
		lines = append(lines, detailLine(faint, "URL", ev.URL, labelWidth, w))
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
		for raw := range strings.SplitSeq(ev.Description, "\n") {
			lines = append(lines, wrapLine(raw, w)...)
		}
	}

	return lines, rsvpIdx
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
