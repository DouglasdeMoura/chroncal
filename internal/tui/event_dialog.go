package tui

import (
	"fmt"
	"image/color"
	"slices"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
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
	AccountServerURL string
	AccountID        int64
	AccountName      string
	AccountOrder     int64
	// AccountAuthType is the linked account's normalized auth type
	// (e.g. "oauth2", "basic"), cached at calendar-load time so later
	// ownership validation and re-auth routing can read it without
	// re-querying accounts. Empty for local-only calendars.
	AccountAuthType     string
	RemoteAccess        string
	RemoteComponents    string // comma-separated VEVENT/VTODO/VJOURNAL; empty = unknown (all allowed)
	RemoteMissing       bool
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
// (selected day, sorted events) and the RSVP row. That row is composed into
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

// --- Domain-format helpers (used here and by event_view_dialog.go) ---

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
	// Measure through the actual rendered style (Padding(0,2).MarginRight(1))
	// so that hitRSVPBtn computes hit zones and advances with the correct width.
	label := strings.Repeat(" ", rsvpMaxLabelWidth())
	return lipgloss.Width(DefaultButtonStyles().Normal.Render(label, false))
}

func rsvpButtonLabel(baseLabel, rsvpStatus string) string {
	if mapped, ok := rsvpIndicators[strings.ToUpper(rsvpStatus)]; ok && strings.HasPrefix(mapped, baseLabel) {
		return mapped
	}
	return baseLabel
}

type eventMetaDetailLinesOptions struct {
	labelStyle  lipgloss.Style
	width       int
	labelWidth  int
	urlRewriter urlRewriter
	// interactive enables mouse-zone markers on the linkified URL rows. Only
	// set it on surfaces that MouseSweep their own output (the full event
	// view). The list-pane and trash detail panes composite plain shell.View()
	// output after the app's single MouseSweep. Their markers would then never
	// be stripped. They leave it false and emit OSC 8 links only.
	interactive bool

	calendar   CalendarInfo
	location   string
	conference string
	url        string
	status     string
	tags       string
	repeat     string
	showAs     string
	visibility string
}

// eventMetaURLRewriter returns a link rewriter for event metadata rows. It
// rewrites supported Google URLs to include authuser when possible; otherwise
// it returns an identity rewriter so URL fields are still clickable.
func eventMetaURLRewriter(cal CalendarInfo) urlRewriter {
	if isGoogleAccountServer(cal.AccountServerURL) {
		if rw := googleAuthuserRewriter(cal.OwnerEmail); rw != nil {
			return rw
		}
	}
	return func(raw string) string { return raw }
}

// eventMetaDetailLines renders shared metadata rows used by list-pane detail,
// full event view, and trash details.
func eventMetaDetailLines(opts eventMetaDetailLinesOptions) []string {
	lines := make([]string, 0, 9)
	add := func(label, value string) {
		if value == "" {
			return
		}
		lines = append(lines, detailLine(opts.labelStyle, label, value, opts.labelWidth, opts.width))
	}
	// addLinkified renders a free-text field (Where) that may merely *contain*
	// a URL. addURL renders a known URL-valued field (Conference, URL) whose
	// whole value is a single URI, kept exact and scheme-agnostic. Both fall
	// back to plain text when there is no rewriter and only emit mouse zones
	// on interactive (swept) surfaces.
	addLinkified := func(label, value string) {
		if value == "" {
			return
		}
		if opts.urlRewriter == nil {
			add(label, value)
			return
		}
		lines = append(lines, detailLinkifiedLine(opts.labelStyle, label, value, opts.labelWidth, opts.width, opts.urlRewriter, opts.interactive))
	}
	addURL := func(label, value string) {
		if value == "" {
			return
		}
		if opts.urlRewriter == nil {
			add(label, value)
			return
		}
		lines = append(lines, detailURLField(opts.labelStyle, label, value, opts.labelWidth, opts.width, opts.urlRewriter, opts.interactive))
	}

	if opts.calendar.Name != "" {
		dot := "●"
		if opts.calendar.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(opts.calendar.Color)).Render("●")
		}
		add("Calendar", dot+" "+opts.calendar.Name)
	}

	addLinkified("Where", opts.location)
	addURL("Conference", opts.conference)
	addURL("URL", opts.url)
	if opts.status != "" {
		add("Status", statusBadge(opts.status))
	}
	add("Tags", opts.tags)
	add("Repeat", opts.repeat)
	add("Show as", opts.showAs)
	add("Visibility", opts.visibility)

	return lines
}

// eventDetailLines returns detail lines and the index of the RSVP row (-1 if none).
// The event title is pinned by the shell via SetDetailTitle, so callers must
// not prepend it here — these lines scroll, the title does not.
func eventDetailLines(ev event.Event, cal CalendarInfo, w, labelWidth int, rsvpLine string, rw urlRewriter) ([]string, int) {
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, detailLine(faint, "When", formatWhen(ev), labelWidth, w))

	dur := formatDuration(ev)
	if dur != "" {
		lines = append(lines, detailLine(faint, "Duration", dur, labelWidth, w))
	}

	lines = append(lines, eventMetaDetailLines(eventMetaDetailLinesOptions{
		labelStyle:  faint,
		width:       w,
		labelWidth:  labelWidth,
		urlRewriter: rw,
		calendar:    cal,
		location:    ev.Location,
		conference:  ev.ConferenceURI,
		url:         ev.URL,
		status:      ev.Status,
		tags:        ev.Categories,
	})...)
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
			lines = append(lines, truncateTo("  "+formatAlarmWithAction(a), w))
		}
	}

	if ev.Description != "" {
		lines = append(lines, "")
		lines = append(lines, descriptionLines(ev.Description, w, nil, false)...)
	}

	return lines, rsvpIdx
}

// detailLabelPrefix renders the right-aligned label column plus its two-space
// gap and returns the rendered prefix together with the cell width left for
// the value.
func detailLabelPrefix(labelStyle lipgloss.Style, label string, lw, w int) (prefix string, available int) {
	padded := strings.Repeat(" ", max(lw-len(label), 0)) + label
	return labelStyle.Render(padded) + "  ", w - labelColWidth(label, lw)
}

func detailLine(labelStyle lipgloss.Style, label, value string, lw, w int) string {
	prefix, _ := detailLabelPrefix(labelStyle, label, lw, w)
	return truncateTo(prefix+value, w)
}

// labelColWidth returns the on-screen cell width consumed by the label column
// plus its two-space gap. It accounts for labels that exceed the nominal lw.
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
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize(n, "week", "weeks")))
		raw = rest
	}
	if n, rest, ok := parseLeadingInt(raw, 'D'); ok {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize(n, "day", "days")))
		raw = rest
	}
	raw = strings.TrimPrefix(raw, "T")
	if n, rest, ok := parseLeadingInt(raw, 'H'); ok {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize(n, "hour", "hours")))
		raw = rest
	}
	if n, rest, ok := parseLeadingInt(raw, 'M'); ok {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize(n, "min.", "min.")))
		raw = rest
	}
	if n, _, ok := parseLeadingInt(raw, 'S'); ok {
		parts = append(parts, fmt.Sprintf("%d %s", n, pluralize(n, "sec.", "sec.")))
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

// pluralize returns the singular form of a noun when n is one, and the plural
// form otherwise (zero included). Both forms are supplied explicitly. Then
// irregular nouns are handled truthfully. Abbreviations such as "min." that
// do not take an "-s" suffix are one case. Do not use a fake general-purpose
// inflector. Callers own the count. The bare form then composes into both
// plain "%d %s" counts and copy with an adjective like "%d downloaded %s".
// See issue #548.
func pluralize(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

// wrapWordByWidth hard-breaks word into display-width chunks each at most w
// cells wide. All chunks except the last are returned in out. The last is
// returned as remainder so the caller can try to append later words to it.
func wrapWordByWidth(word string, w int) (out []string, last string) {
	r := []rune(word)
	for len(r) > 0 {
		n, width := 0, 0
		for n < len(r) {
			cw := lipgloss.Width(string(r[n]))
			if width+cw > w && n > 0 {
				break
			}
			width += cw
			n++
		}
		chunk := string(r[:n])
		r = r[n:]
		if len(r) == 0 {
			return out, chunk
		}
		out = append(out, chunk)
	}
	return out, ""
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
		ww := lipgloss.Width(word)
		if cur == "" {
			if ww > w {
				chunks, last := wrapWordByWidth(word, w)
				out = append(out, chunks...)
				cur = last
				continue
			}
			cur = word
			continue
		}
		if lipgloss.Width(cur)+1+ww > w {
			out = append(out, cur)
			if ww > w {
				chunks, last := wrapWordByWidth(word, w)
				out = append(out, chunks...)
				cur = last
			} else {
				cur = word
			}
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
