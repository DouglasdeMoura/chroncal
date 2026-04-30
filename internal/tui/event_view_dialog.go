package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// EventViewRequestedMsg requests opening the read-only event view dialog.
type EventViewRequestedMsg struct{ Event event.Event }

// EventViewClosedMsg is emitted when the view dialog is dismissed.
type EventViewClosedMsg struct{}

type eventViewKeyMap struct {
	Edit       key.Binding
	Duplicate  key.Binding
	Delete     key.Binding
	Close      key.Binding
	Tab        key.Binding
	ShiftTab   key.Binding
	Left       key.Binding
	Right      key.Binding
	Enter      key.Binding
	RSVPYes    key.Binding
	RSVPNo     key.Binding
	RSVPMaybe  key.Binding
	ScrollUp   key.Binding
	ScrollDown key.Binding
	PageUp     key.Binding
	PageDown   key.Binding
	Home       key.Binding
	End        key.Binding
}

func defaultEventViewKeys() eventViewKeyMap {
	return eventViewKeyMap{
		Edit:       key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit")),
		Duplicate:  key.NewBinding(key.WithKeys("d"), key.WithHelp("d", "duplicate")),
		Delete:     key.NewBinding(key.WithKeys("x", "delete"), key.WithHelp("x", "delete")),
		Close:      key.NewBinding(key.WithKeys("esc", "q"), key.WithHelp("esc", "close")),
		Tab:        key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next")),
		ShiftTab:   key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev")),
		Left:       key.NewBinding(key.WithKeys("left", "h")),
		Right:      key.NewBinding(key.WithKeys("right", "l")),
		Enter:      key.NewBinding(key.WithKeys("enter", " "), key.WithHelp("enter", "select")),
		RSVPYes:    key.NewBinding(key.WithKeys("y"), key.WithHelp("y", "RSVP yes")),
		RSVPNo:     key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "RSVP no")),
		RSVPMaybe:  key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "RSVP maybe")),
		ScrollUp:   key.NewBinding(key.WithKeys("up", "k")),
		ScrollDown: key.NewBinding(key.WithKeys("down", "j")),
		PageUp:     key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
		PageDown:   key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
		Home:       key.NewBinding(key.WithKeys("home")),
		End:        key.NewBinding(key.WithKeys("end")),
	}
}

const (
	viewZoneActions = 0
	viewZoneRSVP    = 1
)

// eventViewLabelWidth pads labels so values align in a column like the
// edit form does. Matches the form's inline-label layout.
const eventViewLabelWidth = 10

// EventViewDialogModel is a read-only dialog presenting a single event.
// It mirrors the Edit Event layout (label: value lines) and exposes
// action buttons to edit, duplicate, delete, or RSVP.
type EventViewDialogModel struct {
	event         event.Event
	calendar      CalendarInfo
	keys          eventViewKeyMap
	help          help.Model
	dialog        Dialog
	body          viewport.Model
	width         int
	height        int
	theme         Theme
	focusZone     int
	focusedAction int
	focusedRSVP   int
}

// eventViewActionEditIdx, eventViewActionDupIdx, and eventViewActionDeleteIdx
// index the entries returned by actions(). Delete lives in the action bar but
// is visually separated (right-aligned) from the left-side Edit/Duplicate pair.
const (
	eventViewActionEditIdx   = 0
	eventViewActionDupIdx    = 1
	eventViewActionDeleteIdx = 2
)

// NewEventViewDialogModel builds a read-only view of the given event.
// The dialog omits a chrome title and relies on the event's own title
// (rendered by paneTitle) as the visual heading.
func NewEventViewDialogModel(ev event.Event, cal CalendarInfo, theme Theme) EventViewDialogModel {
	dialog := NewDialog("", DefaultDialogStyles())
	dialog.SetWidth(60)
	vp := viewport.New()
	vp.MouseWheelEnabled = true
	return EventViewDialogModel{
		event:    ev,
		calendar: cal,
		keys:     defaultEventViewKeys(),
		help:     newThemedHelp(theme),
		dialog:   dialog,
		body:     vp,
		theme:    theme,
	}
}

func (m EventViewDialogModel) SetSize(w, h int) EventViewDialogModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	cw := m.dialog.ContentWidth()
	m.body.SetWidth(cw)
	m.body.SetHeight(m.viewportHeight())
	if cw > 0 {
		m.body.SetContentLines(m.buildBodyLines(cw))
	}
	return m
}

// viewportHeight returns the height available for the scrollable body,
// after subtracting the dialog's chrome (border, top padding), the
// pinned title row + rule, the action separator + button row, and the
// help footer with its leading blank line. Clamped to a minimum of 1
// so a too-small terminal still renders something.
func (m EventViewDialogModel) viewportHeight() int {
	const chromeLines = 2 + // top + bottom border
		1 + // top padding (PaddingY)
		2 + // pinned title + faint rule
		1 + // action separator
		1 + // action row
		2 // blank line + footer
	return max(m.height-chromeLines, 1)
}

func (m EventViewDialogModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

func (m EventViewDialogModel) userAttendee() (model.Attendee, bool) {
	if m.calendar.OwnerEmail == "" {
		return model.Attendee{}, false
	}
	for _, att := range m.event.Attendees {
		if strings.EqualFold(att.Email, m.calendar.OwnerEmail) && !att.Organizer {
			return att, true
		}
	}
	return model.Attendee{}, false
}

func (m EventViewDialogModel) rsvpActions() []dialogAction {
	if _, ok := m.userAttendee(); !ok {
		return nil
	}
	ev := m.event
	return []dialogAction{
		{label: "Yes", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "ACCEPTED"} }},
		{label: "No", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "DECLINED"} }},
		{label: "Maybe", msg: func() tea.Msg { return EventRSVPMsg{Event: ev, Status: "TENTATIVE"} }},
	}
}

type eventViewAction struct {
	label   string
	variant ButtonVariant
	zone    string
	msg     func() tea.Msg
}

// isDeleted reports whether the viewed event is soft-deleted. Opened from
// the trash view for a post-mortem look — editing or re-deleting makes no
// sense in that state.
func (m EventViewDialogModel) isDeleted() bool {
	return m.event.DeletedAt != nil
}

// actions returns the buttons rendered in the bottom action bar.
// Edit and Duplicate sit on the left (primary → secondary), Delete
// is last and rendered with spatial separation so destructive intent
// reads from position, not only color. Returns nil for soft-deleted
// rows — restore must happen from the trash dialog first.
func (m EventViewDialogModel) actions() []eventViewAction {
	if m.isDeleted() {
		return nil
	}
	ev := m.event
	return []eventViewAction{
		{label: "Edit", variant: ButtonPrimary, zone: "action:edit",
			msg: func() tea.Msg { return EventEditMsg{Event: ev} }},
		{label: "Duplicate", variant: ButtonGhost, zone: "action:duplicate",
			msg: func() tea.Msg { return EventDuplicateMsg{Event: ev} }},
		{label: "Delete", variant: ButtonDanger, zone: "action:delete",
			msg: func() tea.Msg { return EventDeleteMsg{Event: ev} }},
	}
}

func (m EventViewDialogModel) Update(msg tea.Msg) (EventViewDialogModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		return m.SetSize(msg.Width, msg.Height), nil
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		return m.handleMouse(msg)
	case tea.MouseWheelMsg:
		var cmd tea.Cmd
		m.body, cmd = m.body.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m EventViewDialogModel) handleKey(msg tea.KeyPressMsg) (EventViewDialogModel, tea.Cmd) {
	actions := m.actions()
	rsvp := m.rsvpActions()

	switch {
	case key.Matches(msg, m.keys.ScrollUp):
		m.body.ScrollUp(1)
		return m, nil
	case key.Matches(msg, m.keys.ScrollDown):
		m.body.ScrollDown(1)
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.body.PageUp()
		return m, nil
	case key.Matches(msg, m.keys.PageDown):
		m.body.PageDown()
		return m, nil
	case key.Matches(msg, m.keys.Home):
		m.body.GotoTop()
		return m, nil
	case key.Matches(msg, m.keys.End):
		m.body.GotoBottom()
		return m, nil

	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return EventViewClosedMsg{} }

	case key.Matches(msg, m.keys.Edit):
		if len(actions) > eventViewActionEditIdx {
			return m, actions[eventViewActionEditIdx].msg
		}
	case key.Matches(msg, m.keys.Duplicate):
		if len(actions) > eventViewActionDupIdx {
			return m, actions[eventViewActionDupIdx].msg
		}
	case key.Matches(msg, m.keys.Delete):
		if len(actions) > eventViewActionDeleteIdx {
			return m, actions[eventViewActionDeleteIdx].msg
		}

	case key.Matches(msg, m.keys.RSVPYes):
		if len(rsvp) > 0 {
			return m, rsvp[0].msg
		}
	case key.Matches(msg, m.keys.RSVPNo):
		if len(rsvp) > 1 {
			return m, rsvp[1].msg
		}
	case key.Matches(msg, m.keys.RSVPMaybe):
		if len(rsvp) > 2 {
			return m, rsvp[2].msg
		}

	case key.Matches(msg, m.keys.Tab):
		m = m.advanceFocus(actions, rsvp, +1)
	case key.Matches(msg, m.keys.ShiftTab):
		m = m.advanceFocus(actions, rsvp, -1)

	case key.Matches(msg, m.keys.Left):
		switch {
		case m.focusZone == viewZoneRSVP && len(rsvp) > 0:
			m.focusedRSVP = (m.focusedRSVP - 1 + len(rsvp)) % len(rsvp)
		case len(actions) > 0:
			m.focusedAction = (m.focusedAction - 1 + len(actions)) % len(actions)
		}
	case key.Matches(msg, m.keys.Right):
		switch {
		case m.focusZone == viewZoneRSVP && len(rsvp) > 0:
			m.focusedRSVP = (m.focusedRSVP + 1) % len(rsvp)
		case len(actions) > 0:
			m.focusedAction = (m.focusedAction + 1) % len(actions)
		}

	case key.Matches(msg, m.keys.Enter):
		switch m.focusZone {
		case viewZoneRSVP:
			if m.focusedRSVP < len(rsvp) {
				return m, rsvp[m.focusedRSVP].msg
			}
		default:
			if m.focusedAction < len(actions) {
				return m, actions[m.focusedAction].msg
			}
		}
	}
	return m, nil
}

// advanceFocus moves focus in Tab order: Actions → RSVP (if present)
// → Actions (wrap). Shift-Tab moves in the reverse direction.
func (m EventViewDialogModel) advanceFocus(actions []eventViewAction, rsvp []dialogAction, dir int) EventViewDialogModel {
	switch m.focusZone {
	case viewZoneActions:
		idx := m.focusedAction + dir
		if idx >= 0 && idx < len(actions) {
			m.focusedAction = idx
			return m
		}
		if len(rsvp) > 0 {
			if dir > 0 {
				m.focusZone = viewZoneRSVP
				m.focusedRSVP = 0
			} else {
				m.focusZone = viewZoneRSVP
				m.focusedRSVP = len(rsvp) - 1
			}
		} else {
			if dir > 0 {
				m.focusedAction = 0
			} else {
				m.focusedAction = len(actions) - 1
			}
		}
	case viewZoneRSVP:
		idx := m.focusedRSVP + dir
		if idx >= 0 && idx < len(rsvp) {
			m.focusedRSVP = idx
			return m
		}
		m.focusZone = viewZoneActions
		if dir > 0 {
			m.focusedAction = 0
		} else {
			m.focusedAction = len(actions) - 1
		}
	}
	return m
}

func (m EventViewDialogModel) handleMouse(msg tea.MouseClickMsg) (EventViewDialogModel, tea.Cmd) {
	if msg.Button != tea.MouseLeft {
		return m, nil
	}
	bw, bh := m.BoxSize()
	ox := (m.width - bw) / 2
	oy := (m.height - bh) / 2
	target := mouseResolve(msg.X-ox, msg.Y-oy)
	if target == "" {
		return m, nil
	}
	actions := m.actions()
	for i, a := range actions {
		if target == a.zone {
			m.focusZone = viewZoneActions
			m.focusedAction = i
			return m, a.msg
		}
	}
	rsvp := m.rsvpActions()
	for i, a := range rsvp {
		if target == "rsvp:"+a.label {
			m.focusZone = viewZoneRSVP
			m.focusedRSVP = i
			return m, a.msg
		}
	}
	return m, nil
}

func (m EventViewDialogModel) renderRSVPRow(w int) string {
	rsvp := m.rsvpActions()
	if len(rsvp) == 0 {
		return ""
	}
	att, _ := m.userAttendee()
	faint := lipgloss.NewStyle().Faint(true)

	label := "Your RSVP"
	padded := strings.Repeat(" ", max(eventViewLabelWidth-len(label), 0)) + label + "  "

	fixedW := rsvpMaxLabelWidth()
	parts := make([]string, 0, len(rsvp))
	for i, a := range rsvp {
		l := rsvpButtonLabel(a.label, att.RSVPStatus)
		if pad := fixedW - lipgloss.Width(l); pad > 0 {
			leftPad := pad / 2
			right := pad - leftPad
			l = strings.Repeat(" ", leftPad) + l + strings.Repeat(" ", right)
		}
		focused := m.focusZone == viewZoneRSVP && i == m.focusedRSVP
		parts = append(parts, mouseMark("rsvp:"+a.label, DefaultButtonStyles().Secondary.Render(l, focused)))
	}
	value := strings.Join(parts, " ")
	return truncateTo(faint.Render(padded)+value, w)
}

// renderActions lays out the action bar: Edit and Duplicate packed on
// the left (primary → ghost), with Delete right-aligned and a gap that
// spatially signals the destructive category. Falls back to simple
// concatenation if the row can't fit within `w`.
func (m EventViewDialogModel) renderActions(w int) string {
	bs := DefaultButtonStyles()
	actions := m.actions()
	rendered := make([]string, len(actions))
	for i, a := range actions {
		focused := m.focusZone == viewZoneActions && i == m.focusedAction
		rendered[i] = mouseMark(a.zone, bs.Get(a.variant).Render(a.label, focused))
	}
	if len(rendered) < 3 {
		return strings.Join(rendered, " ")
	}
	left := strings.Join(rendered[:2], " ")
	right := rendered[2]
	gap := max(w-lipgloss.Width(left)-lipgloss.Width(right), 1)
	return left + strings.Repeat(" ", gap) + right
}

// titleRow renders the bold event title above a faint dividing rule.
// Destructive actions are deliberately absent here — they live in the
// bottom action bar, away from the dialog's visual anchor.
func (m EventViewDialogModel) titleRow(w int) []string {
	faint := lipgloss.NewStyle().Faint(true)
	title := lipgloss.NewStyle().Bold(true).Render(truncateTo(m.event.Title, w))
	rule := faint.Render(strings.Repeat("─", w))
	return []string{title, rule}
}

// actionsSeparator renders the faint rule that sits between the body
// and the action bar. When the body has scrolled-away content above or
// below, a small "↑↓ more" glyph is centered on the rule to advertise
// the scroll affordance — placed here (not on the title rule) so it
// sits next to the help footer where users look for controls.
func (m EventViewDialogModel) actionsSeparator(w int) string {
	faint := lipgloss.NewStyle().Faint(true)
	hint := m.scrollHint()
	hw := lipgloss.Width(hint)
	if hint == "" || w <= hw+2 {
		return faint.Render(strings.Repeat("─", w))
	}
	left := (w - hw - 2) / 2
	right := w - hw - 2 - left
	return faint.Render(strings.Repeat("─", left)) + " " + faint.Render(hint) + " " + faint.Render(strings.Repeat("─", right))
}

// scrollHint returns " ↑↓" / " ↑" / " ↓" depending on what the user can
// still scroll to. Empty when the body fits without scrolling.
func (m EventViewDialogModel) scrollHint() string {
	if !m.bodyOverflows() {
		return ""
	}
	switch {
	case m.body.AtTop():
		return "↓ more"
	case m.body.AtBottom():
		return "↑ more"
	default:
		return "↑↓ more"
	}
}

// buildDetailLines composes the read-only field list for the dialog,
// skipping empty fields so the layout only surfaces meaningful values.
// The title row is included at the top so callers that render without a
// scrollable viewport (tests, simple snapshots) see the same layout as
// before. View() pins the title separately and feeds only the body to
// the viewport.
func (m EventViewDialogModel) buildDetailLines(w int) []string {
	lines := append([]string{}, m.titleRow(w)...)
	lines = append(lines, m.buildBodyLines(w)...)
	return lines
}

// buildBodyLines returns every line below the title row — the part that
// scrolls inside the viewport when content exceeds available height.
// A leading blank line preserves the visual gap between the title rule
// and the first detail row that the un-scrolled rendering relied on.
func (m EventViewDialogModel) buildBodyLines(w int) []string {
	faint := lipgloss.NewStyle().Faint(true)
	ev := m.event

	lines := []string{""}

	if m.isDeleted() && ev.DeletedAt != nil {
		warn := lipgloss.NewStyle().Foreground(m.theme.Error).Bold(true)
		banner := warn.Render("⊘ Deleted " + ev.DeletedAt.Local().Format("Mon, Jan 2 15:04") + " — restore from trash to edit")
		lines = append(lines, truncateTo(banner, w))
		lines = append(lines, "")
	}

	lines = append(lines, detailLine(faint, "Date", formatEventDateRange(ev), eventViewLabelWidth, w))
	if t := formatEventTimeRange(ev); t != "" {
		lines = append(lines, detailLine(faint, "Time", t, eventViewLabelWidth, w))
	}
	if dur := formatDuration(ev); dur != "" {
		lines = append(lines, detailLine(faint, "Duration", dur, eventViewLabelWidth, w))
	}
	if ev.AllDay {
		lines = append(lines, detailLine(faint, "All day", Glyphs["checkbox.on"], eventViewLabelWidth, w))
	}
	if ev.Timezone != "" {
		lines = append(lines, detailLine(faint, "Timezone", ev.Timezone, eventViewLabelWidth, w))
	}
	if rep := describeRecurrence(ev.RecurrenceRule); rep != "" {
		lines = append(lines, detailLine(faint, "Repeat", rep, eventViewLabelWidth, w))
	}

	hasMeta := m.calendar.Name != "" || ev.Location != "" || ev.ConferenceURI != "" ||
		ev.URL != "" || ev.Status != "" || ev.Categories != "" ||
		ev.Transp != "" || ev.Class != ""
	if hasMeta {
		lines = append(lines, "")
	}
	if m.calendar.Name != "" {
		dot := Glyphs["dot"]
		if m.calendar.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(m.calendar.Color)).Render(Glyphs["dot"])
		}
		lines = append(lines, detailLine(faint, "Calendar", dot+" "+m.calendar.Name, eventViewLabelWidth, w))
	}
	if ev.Location != "" {
		lines = append(lines, detailLine(faint, "Where", ev.Location, eventViewLabelWidth, w))
	}
	if ev.ConferenceURI != "" {
		lines = append(lines, detailLine(faint, "Conference", ev.ConferenceURI, eventViewLabelWidth, w))
	}
	if ev.URL != "" {
		lines = append(lines, detailLine(faint, "URL", ev.URL, eventViewLabelWidth, w))
	}
	if ev.Status != "" {
		lines = append(lines, detailLine(faint, "Status", statusBadge(ev.Status), eventViewLabelWidth, w))
	}
	if ev.Categories != "" {
		lines = append(lines, detailLine(faint, "Tags", ev.Categories, eventViewLabelWidth, w))
	}
	if v := formatShowAs(ev.Transp); v != "" {
		lines = append(lines, detailLine(faint, "Show as", v, eventViewLabelWidth, w))
	}
	if v := formatVisibility(ev.Class); v != "" {
		lines = append(lines, detailLine(faint, "Visibility", v, eventViewLabelWidth, w))
	}

	if rsvpLine := m.renderRSVPRow(w); rsvpLine != "" {
		lines = append(lines, "")
		lines = append(lines, rsvpLine)
	}

	if len(ev.Attendees) > 0 {
		lines = append(lines, "")
		lines = append(lines, faint.Render("People"))
		for _, att := range ev.Attendees {
			lines = append(lines, truncateTo(formatAttendee(att), w))
		}
	}

	if len(ev.Alarms) > 0 {
		lines = append(lines, "")
		lines = append(lines, faint.Render("Alarms"))
		for _, a := range ev.Alarms {
			lines = append(lines, truncateTo("  "+formatAlarm(a), w))
		}
	}

	if ev.Description != "" {
		lines = append(lines, "")
		lines = append(lines, faint.Render("Notes"))
		for raw := range strings.SplitSeq(ev.Description, "\n") {
			lines = append(lines, wrapLine(raw, w)...)
		}
	}

	return lines
}

func (m EventViewDialogModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	cw := m.dialog.ContentWidth()
	if cw <= 0 {
		return ""
	}

	titleLines := m.titleRow(cw)
	bodyLines := m.buildBodyLines(cw)
	actionsRow := m.renderActions(cw)

	m.body.SetWidth(cw)
	m.body.SetHeight(m.viewportHeight())
	m.body.SetContentLines(bodyLines)

	sep := m.actionsSeparator(cw)
	parts := append(titleLines, m.body.View(), sep, actionsRow)
	body := strings.Join(parts, "\n")

	var helpKeys []key.Binding
	if m.isDeleted() {
		helpKeys = []key.Binding{m.keys.Close}
	} else {
		helpKeys = []key.Binding{m.keys.Edit, m.keys.Duplicate, m.keys.Delete, m.keys.Close}
	}
	m.help.SetWidth(cw)
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))

	return mouseSweep(m.dialog.Box(body))
}

// bodyOverflows reports whether the scrollable body has more content
// than the viewport can show at once.
func (m EventViewDialogModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

// formatEventDateRange returns a single date or date range string for the
// event. Single-day events render as "Fri, Apr 17, 2026"; multi-day events
// compress the year/month when possible (e.g. "Fri, Apr 17 – Sat, Apr 18, 2026").
// All-day events use UTC (datestamp semantics) and subtract a day from the
// exclusive end to render the inclusive last day.
func formatEventDateRange(ev event.Event) string {
	start := ev.StartTime
	end := ev.EndTime
	if ev.AllDay {
		start = start.UTC()
		end = end.UTC()
	} else {
		start = start.Local()
		end = end.Local()
	}
	if ev.AllDay && !end.IsZero() && end.After(start) {
		end = end.AddDate(0, 0, -1)
	}
	if end.IsZero() || sameDay(start, end) {
		return start.Format("Mon, Jan 2, 2006")
	}
	if start.Year() == end.Year() {
		return start.Format("Mon, Jan 2") + " – " + end.Format("Mon, Jan 2, 2006")
	}
	return start.Format("Mon, Jan 2, 2006") + " – " + end.Format("Mon, Jan 2, 2006")
}

// formatEventTimeRange returns the event's clock-time span (e.g. "09:00 – 10:00")
// in the local timezone, or empty for all-day events.
func formatEventTimeRange(ev event.Event) string {
	if ev.AllDay {
		return ""
	}
	start := ev.StartTime.Local()
	if start.IsZero() {
		return ""
	}
	end := ev.EndTime.Local()
	if end.IsZero() {
		return start.Format("15:04")
	}
	return start.Format("15:04") + " – " + end.Format("15:04")
}

// describeRecurrence renders a recurrence rule using the same presets the
// event form offers. Unknown rules fall back to "Custom".
func describeRecurrence(rule string) string {
	if strings.TrimSpace(rule) == "" {
		return ""
	}
	idx, _, _, _ := parseRecurrenceRule(rule, time.Time{})
	if idx >= 0 && idx < len(repeatPresets) {
		if idx == repeatCustomIdx {
			return "Custom"
		}
		return repeatPresets[idx].Label
	}
	return "Custom"
}

func formatShowAs(transp string) string {
	switch strings.ToUpper(strings.TrimSpace(transp)) {
	case "OPAQUE":
		return "Busy"
	case "TRANSPARENT":
		return "Free"
	}
	return ""
}

func formatVisibility(class string) string {
	switch strings.ToUpper(strings.TrimSpace(class)) {
	case "PUBLIC":
		return "Public"
	case "PRIVATE":
		return "Private"
	case "CONFIDENTIAL":
		return "Confidential"
	}
	return ""
}
