package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// EventFormSaveMsg is emitted when the user saves the event form.
// When EventID > 0 the save is an update; otherwise it is a create.
type EventFormSaveMsg struct {
	EventID        int64
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
}

// EventFormClosedMsg is emitted when the user closes the event form.
type EventFormClosedMsg struct{}

type formField int

const (
	fieldTitle formField = iota
	fieldStart
	fieldEnd
	fieldDate
	fieldAllDay
	fieldRepeat
	fieldEnds
	fieldEndsCount
	fieldCalendar
	fieldLocation
	fieldDescription
	fieldSave
	fieldCancel
)

type repeatPreset struct {
	Label string
	Rule  string // RRULE value without prefix, empty for "None"
}

var repeatPresets = []repeatPreset{
	{"None", ""},
	{"Every day", "FREQ=DAILY"},
	{"Every week", "FREQ=WEEKLY"},
	{"Every 2 weeks", "FREQ=WEEKLY;INTERVAL=2"},
	{"Every month", "FREQ=MONTHLY"},
	{"Every year", "FREQ=YEARLY"},
	{"Weekdays", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR"},
	{"Custom...", ""},
}

const repeatCustomIdx = 7 // index of the "Custom..." entry

type endsMode int

const (
	endsNever endsMode = iota
	endsAfter
	endsOnDate
)

type calendarOption struct {
	ID    int64
	Name  string
	Color string
}

type eventFormKeyMap struct {
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
	Close    key.Binding
}

func (k eventFormKeyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Tab, k.Enter, k.Close}
}

func (k eventFormKeyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Tab, k.ShiftTab, k.Enter, k.Close},
	}
}

func datePickerHelpKeys() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("left", "up", "right", "down"), key.WithHelp("arrows", "navigate")),
		key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[/]", "month")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
	}
}

// EventFormModel is the Bubble Tea model for the event creation/edit form.
type EventFormModel struct {
	editID      int64 // 0 = create mode, >0 = editing this event ID
	day         time.Time
	calendars   []calendarOption
	calendarIdx int

	title       textinput.Model
	startTime   textinput.Model
	endTime     textinput.Model
	location    textinput.Model
	description textarea.Model

	allDay         bool
	repeatIdx      int // index into repeatPresets
	customRule     string
	rruleEditor    RecurrenceEditorModel
	rruleEditorOpen bool
	ends           endsMode
	endsCount      textinput.Model
	endsDate       time.Time
	endsDatePicker bool // reuse date picker for ends-on-date
	focusField     formField
	datePickerOpen bool
	errMsg         string

	keys   eventFormKeyMap
	help   help.Model
	width  int
	height int
	theme  Theme
}

// NewEventFormModel creates a new event form for the given day.
func NewEventFormModel(day time.Time, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	title := textinput.New()
	title.Placeholder = "Event title"
	title.CharLimit = 200
	cmd := title.Focus()

	startInput := textinput.New()
	startInput.Placeholder = "HH:MM"
	startInput.CharLimit = 5

	endInput := textinput.New()
	endInput.Placeholder = "HH:MM"
	endInput.CharLimit = 5

	locationInput := textinput.New()
	locationInput.Placeholder = "Add location"
	locationInput.CharLimit = 200

	descInput := textarea.New()
	descInput.Placeholder = "Add description"
	descInput.CharLimit = 500
	descInput.ShowLineNumbers = false
	descInput.Prompt = ""
	descInput.SetHeight(3)

	endsCountInput := textinput.New()
	endsCountInput.Placeholder = "10"
	endsCountInput.CharLimit = 4

	// Default times: next half hour, 1 hour duration.
	now := time.Now()
	startHour, startMin := now.Hour(), 30
	if now.Minute() >= 30 {
		startHour++
		startMin = 0
	}
	if startHour >= 24 {
		startHour = 0
	}
	endHour := startHour + 1
	if endHour >= 24 {
		endHour -= 24
	}
	startInput.SetValue(fmt.Sprintf("%02d:%02d", startHour, startMin))
	endInput.SetValue(fmt.Sprintf("%02d:%02d", endHour, startMin))

	var calOpts []calendarOption
	for id, info := range calendars {
		calOpts = append(calOpts, calendarOption{ID: id, Name: info.Name, Color: info.Color})
	}
	sort.Slice(calOpts, func(i, j int) bool { return calOpts[i].Name < calOpts[j].Name })

	return EventFormModel{
		day:         day,
		calendars:   calOpts,
		title:       title,
		startTime:   startInput,
		endTime:     endInput,
		location:    locationInput,
		description: descInput,
		endsCount:   endsCountInput,
		endsDate:    day.AddDate(0, 1, 0),
		focusField: fieldTitle,
		theme:     theme,
		help: newThemedHelp(theme),
		keys: eventFormKeyMap{
			Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
			ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
			Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
			Close:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		},
	}, cmd
}

// NewEventFormModelForEdit creates a form pre-filled with an existing event's data.
func NewEventFormModelForEdit(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModel(ev.StartTime, calendars, theme)
	m.editID = ev.ID
	m.title.SetValue(ev.Title)
	m.location.SetValue(ev.Location)
	m.description.SetValue(ev.Description)
	m.allDay = ev.AllDay

	if !ev.AllDay {
		m.startTime.SetValue(ev.StartTime.Local().Format("15:04"))
		m.endTime.SetValue(ev.EndTime.Local().Format("15:04"))
	}

	// Select the correct calendar.
	for i, c := range m.calendars {
		if c.ID == ev.CalendarID {
			m.calendarIdx = i
			break
		}
	}

	// Parse recurrence rule into form state.
	if ev.RecurrenceRule != "" {
		m.repeatIdx, m.customRule, m.ends, m.endsDate = parseRecurrenceRule(ev.RecurrenceRule, m.day)
		if m.ends == endsAfter {
			if count := rruleParam(ev.RecurrenceRule, "COUNT"); count != "" {
				m.endsCount.SetValue(count)
			}
		}
	}

	return m, cmd
}

// parseRecurrenceRule matches a recurrence rule against the form presets and
// extracts ending conditions. Returns presetIdx, customRule, ends mode, and
// ends date.
func parseRecurrenceRule(rule string, fallbackDate time.Time) (int, string, endsMode, time.Time) {
	// Strip COUNT and UNTIL to match the base rule against presets.
	base := rule
	var ends endsMode
	endsDate := fallbackDate.AddDate(0, 1, 0)

	parts := strings.Split(rule, ";")
	var baseParts []string
	for _, p := range parts {
		upper := strings.ToUpper(p)
		switch {
		case strings.HasPrefix(upper, "COUNT="):
			ends = endsAfter
		case strings.HasPrefix(upper, "UNTIL="):
			ends = endsOnDate
			val := strings.TrimPrefix(upper, "UNTIL=")
			if t, err := time.Parse("20060102T150405Z", val); err == nil {
				endsDate = t
			} else if t, err := time.Parse("20060102", val); err == nil {
				endsDate = t
			}
		default:
			baseParts = append(baseParts, p)
		}
	}
	base = strings.Join(baseParts, ";")

	// Try to match against presets (skip index 0 "None" and 7 "Custom...").
	for i := 1; i < len(repeatPresets); i++ {
		if i == repeatCustomIdx {
			continue
		}
		if strings.EqualFold(base, repeatPresets[i].Rule) {
			return i, "", ends, endsDate
		}
	}

	// No preset matched — use Custom.
	return repeatCustomIdx, rule, ends, endsDate
}

// rruleParam extracts a named parameter value from an RRULE string.
func rruleParam(rule, name string) string {
	for _, p := range strings.Split(rule, ";") {
		if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

func (m EventFormModel) SetSize(w, h int) EventFormModel {
	m.width = w
	m.height = h
	if m.width > 0 && m.height > 0 && m.title.CharLimit > 0 {
		m.updateInputWidths()
	}
	return m
}

func (m *EventFormModel) updateInputWidths() {
	boxW, _ := m.boxSize()
	innerW := max(boxW-6, 20)
	lw := 12
	inputW := max(innerW-lw, 10)
	m.title.SetWidth(inputW)
	m.startTime.SetWidth(5)
	m.endTime.SetWidth(5)
	m.location.SetWidth(inputW)
	m.description.SetWidth(innerW - 2) // account for left border + padding
	m.endsCount.SetWidth(4)
}

// BoxSize returns the outer dimensions of the form dialog.
func (m EventFormModel) BoxSize() (int, int) {
	return lipgloss.Size(m.View())
}

func (m EventFormModel) boxSize() (int, int) {
	boxW := min(56, max(m.width-4, 30))
	boxH := 27 // base with repeat line
	if m.allDay {
		boxH = 25
	}
	if m.repeatIdx > 0 {
		boxH += 2 // ends line
	}
	if len(m.calendars) <= 1 {
		boxH -= 2
	}
	if m.errMsg != "" {
		boxH += 2
	}
	boxH = min(boxH, max(m.height-4, 14))
	return boxW, boxH
}

func (m EventFormModel) focusableFields() []formField {
	fields := []formField{fieldTitle}
	if !m.allDay {
		fields = append(fields, fieldStart, fieldEnd)
	}
	fields = append(fields, fieldDate, fieldAllDay, fieldRepeat)
	if m.repeatIdx > 0 && m.repeatIdx != repeatCustomIdx {
		fields = append(fields, fieldEnds)
		if m.ends == endsAfter {
			fields = append(fields, fieldEndsCount)
		}
	}
	if len(m.calendars) > 1 {
		fields = append(fields, fieldCalendar)
	}
	fields = append(fields, fieldLocation, fieldDescription, fieldSave, fieldCancel)
	return fields
}

func (m EventFormModel) nextField() formField {
	fields := m.focusableFields()
	for i, f := range fields {
		if f == m.focusField {
			return fields[(i+1)%len(fields)]
		}
	}
	return fields[0]
}

func (m EventFormModel) prevField() formField {
	fields := m.focusableFields()
	for i, f := range fields {
		if f == m.focusField {
			return fields[(i-1+len(fields))%len(fields)]
		}
	}
	return fields[0]
}

func (m EventFormModel) withFocus(f formField) (EventFormModel, tea.Cmd) {
	m.focusField = f
	m.title.Blur()
	m.startTime.Blur()
	m.endTime.Blur()
	m.location.Blur()
	m.description.Blur()
	m.endsCount.Blur()
	m.errMsg = ""
	var cmd tea.Cmd
	switch f {
	case fieldTitle:
		cmd = m.title.Focus()
	case fieldStart:
		cmd = m.startTime.Focus()
	case fieldEnd:
		cmd = m.endTime.Focus()
	case fieldLocation:
		cmd = m.location.Focus()
	case fieldDescription:
		cmd = m.description.Focus()
	case fieldEndsCount:
		cmd = m.endsCount.Focus()
	}
	return m, cmd
}

func (m EventFormModel) isTextInput() bool {
	switch m.focusField {
	case fieldTitle, fieldStart, fieldEnd, fieldLocation, fieldDescription, fieldEndsCount:
		return true
	}
	return false
}

// Update handles messages for the event form.
func (m EventFormModel) Update(msg tea.Msg) (EventFormModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	case tea.MouseClickMsg:
		if msg.Button == tea.MouseLeft {
			if m.rruleEditorOpen && m.rruleEditor.EndsDatePickerOpen() {
				pw, ph := m.rruleEditor.EndsDatePickerBoxSize()
				m.rruleEditor = m.rruleEditor.HandleEndsDateMouse(msg, pw, ph)
				return m, nil
			}
			if m.endsDatePicker {
				return m.handleEndsDatePickerMouse(msg)
			}
			if m.datePickerOpen {
				return m.handleDatePickerMouse(msg)
			}
		}
		return m, nil
	}
	// Forward non-key messages (cursor blink, etc.) to the active textinput.
	if m.isTextInput() {
		var cmd tea.Cmd
		switch m.focusField {
		case fieldTitle:
			m.title, cmd = m.title.Update(msg)
		case fieldStart:
			m.startTime, cmd = m.startTime.Update(msg)
		case fieldEnd:
			m.endTime, cmd = m.endTime.Update(msg)
		case fieldLocation:
			m.location, cmd = m.location.Update(msg)
		case fieldDescription:
			m.description, cmd = m.description.Update(msg)
		case fieldEndsCount:
			m.endsCount, cmd = m.endsCount.Update(msg)
		}
		return m, cmd
	}
	return m, nil
}

func (m EventFormModel) handleKey(msg tea.KeyPressMsg) (EventFormModel, tea.Cmd) {
	// Overlays capture all input when open.
	if m.rruleEditorOpen {
		var cmd tea.Cmd
		m.rruleEditor, cmd = m.rruleEditor.Update(msg)
		if m.rruleEditor.Done() {
			m.customRule = m.rruleEditor.BuildRule()
			m.rruleEditorOpen = false
		} else if m.rruleEditor.Cancelled() {
			m.rruleEditorOpen = false
		}
		return m, cmd
	}
	if m.datePickerOpen {
		return m.handleDatePickerKey(msg)
	}
	if m.endsDatePicker {
		return m.handleEndsDatePickerKey(msg)
	}

	switch {
	case key.Matches(msg, m.keys.Close):
		return m, func() tea.Msg { return EventFormClosedMsg{} }

	case key.Matches(msg, m.keys.ShiftTab):
		return m.withFocus(m.prevField())

	case key.Matches(msg, m.keys.Tab):
		return m.withFocus(m.nextField())

	case key.Matches(msg, m.keys.Enter):
		switch m.focusField {
		case fieldSave:
			return m.save()
		case fieldCancel:
			return m, func() tea.Msg { return EventFormClosedMsg{} }
		case fieldAllDay:
			m.allDay = !m.allDay
			return m, nil
		case fieldDate:
			m.datePickerOpen = true
			return m, nil
		case fieldRepeat:
			if m.repeatIdx == repeatCustomIdx {
				m.rruleEditor = NewRecurrenceEditorModel(m.day, m.width, m.height, m.theme)
				m.rruleEditorOpen = true
				return m, nil
			}
		default:
			if m.isTextInput() && m.focusField != fieldDescription {
				return m.withFocus(m.nextField())
			}
		}
	}

	// All day toggle via space.
	if m.focusField == fieldAllDay && msg.String() == "space" {
		m.allDay = !m.allDay
		return m, nil
	}

	// Open date picker via space.
	if m.focusField == fieldDate && msg.String() == "space" {
		m.datePickerOpen = true
		return m, nil
	}

	// Repeat preset cycling.
	if m.focusField == fieldRepeat {
		n := len(repeatPresets)
		switch msg.String() {
		case "left", "h":
			m.repeatIdx = (m.repeatIdx - 1 + n) % n
			return m, nil
		case "right", "l":
			m.repeatIdx = (m.repeatIdx + 1) % n
			return m, nil
		}
	}

	// Ends mode cycling.
	if m.focusField == fieldEnds {
		switch msg.String() {
		case "left", "h":
			m.ends = endsMode((int(m.ends) - 1 + 3) % 3)
			return m, nil
		case "right", "l":
			m.ends = endsMode((int(m.ends) + 1) % 3)
			return m, nil
		case "enter", "space":
			if m.ends == endsOnDate {
				m.endsDatePicker = true
				return m, nil
			}
		}
	}

	// Calendar cycling via arrow keys.
	if m.focusField == fieldCalendar && len(m.calendars) > 0 {
		switch msg.String() {
		case "left", "h":
			m.calendarIdx = (m.calendarIdx - 1 + len(m.calendars)) % len(m.calendars)
			return m, nil
		case "right", "l":
			m.calendarIdx = (m.calendarIdx + 1) % len(m.calendars)
			return m, nil
		}
	}

	// Forward to active textinput.
	if m.isTextInput() {
		var cmd tea.Cmd
		switch m.focusField {
		case fieldTitle:
			m.title, cmd = m.title.Update(msg)
		case fieldStart:
			dur := m.currentDuration()
			m.startTime, cmd = m.startTime.Update(msg)
			m.adjustEndTime(dur)
		case fieldEnd:
			m.endTime, cmd = m.endTime.Update(msg)
		case fieldLocation:
			m.location, cmd = m.location.Update(msg)
		case fieldDescription:
			m.description, cmd = m.description.Update(msg)
		case fieldEndsCount:
			m.endsCount, cmd = m.endsCount.Update(msg)
		}
		return m, cmd
	}

	return m, nil
}

// currentDuration returns the duration between start and end if both are valid.
// Falls back to 1 hour when either value is unparseable.
func (m EventFormModel) handleDatePickerKey(msg tea.KeyPressMsg) (EventFormModel, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		m.day = m.day.AddDate(0, 0, -1)
	case "right", "l":
		m.day = m.day.AddDate(0, 0, 1)
	case "up", "k":
		m.day = m.day.AddDate(0, 0, -7)
	case "down", "j":
		m.day = m.day.AddDate(0, 0, 7)
	case "[":
		m.day = addMonthClamped(m.day, -1)
	case "]":
		m.day = addMonthClamped(m.day, 1)
	case "t":
		m.day = time.Now()
	case "enter", "space":
		m.datePickerOpen = false
	case "esc", "q":
		m.datePickerOpen = false
	}
	return m, nil
}

func (m EventFormModel) handleDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	innerW := boxW - 6
	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)

	// Picker is centered on screen.
	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2

	// Grid origin inside the box: border(1) + padding(2) + gridPad for x,
	// border(1) + padding(1) + month header(1) + weekday header(1) for y.
	gridX := ox + 3 + gridPad
	gridY := oy + 4

	// Translate click to grid-relative coordinates.
	rx := msg.X - gridX
	ry := msg.Y - gridY
	if rx < 0 || rx >= gridW || ry < 0 || ry >= 6 {
		return m, nil
	}

	// Each cell is 3 chars wide (2 digit + 1 space), last column has no trailing space.
	dow := rx / 3
	if dow > 6 {
		dow = 6
	}
	week := ry

	// Map to day number.
	y, mo, _ := m.day.Date()
	loc := m.day.Location()
	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday())
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	dayNum := week*7 + dow - startDow + 1
	if dayNum < 1 || dayNum > daysInMonth {
		return m, nil
	}

	m.day = time.Date(y, mo, dayNum, 0, 0, 0, 0, loc)
	m.datePickerOpen = false
	return m, nil
}

func (m EventFormModel) handleEndsDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	innerW := boxW - 6
	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)

	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2
	gridX := ox + 3 + gridPad
	gridY := oy + 4

	rx := msg.X - gridX
	ry := msg.Y - gridY
	if rx < 0 || rx >= gridW || ry < 0 || ry >= 6 {
		return m, nil
	}

	dow := rx / 3
	if dow > 6 {
		dow = 6
	}
	week := ry

	y, mo, _ := m.endsDate.Date()
	loc := m.endsDate.Location()
	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday())
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	dayNum := week*7 + dow - startDow + 1
	if dayNum < 1 || dayNum > daysInMonth {
		return m, nil
	}

	m.endsDate = time.Date(y, mo, dayNum, 0, 0, 0, 0, loc)
	m.endsDatePicker = false
	return m, nil
}

func (m EventFormModel) handleEndsDatePickerKey(msg tea.KeyPressMsg) (EventFormModel, tea.Cmd) {
	switch msg.String() {
	case "left", "h":
		m.endsDate = m.endsDate.AddDate(0, 0, -1)
	case "right", "l":
		m.endsDate = m.endsDate.AddDate(0, 0, 1)
	case "up", "k":
		m.endsDate = m.endsDate.AddDate(0, 0, -7)
	case "down", "j":
		m.endsDate = m.endsDate.AddDate(0, 0, 7)
	case "[":
		m.endsDate = addMonthClamped(m.endsDate, -1)
	case "]":
		m.endsDate = addMonthClamped(m.endsDate, 1)
	case "t":
		m.endsDate = time.Now()
	case "enter", "space":
		m.endsDatePicker = false
	case "esc", "q":
		m.endsDatePicker = false
	}
	return m, nil
}

// EndsDatePickerOpen reports whether the ends-date picker overlay should be shown.
func (m EventFormModel) EndsDatePickerOpen() bool {
	return m.endsDatePicker
}

// EndsDatePickerView renders the ends-date picker using the same layout as the main date picker.
func (m EventFormModel) EndsDatePickerView() string {
	boxW, _ := m.DatePickerBoxSize()
	innerW := boxW - 6

	bold := lipgloss.NewStyle().Bold(true)

	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)
	var lines []string
	monthStr := m.endsDate.Format("January 2006")
	monthPad := gridPad + max((gridW-len(monthStr))/2, 0)
	lines = append(lines, strings.Repeat(" ", monthPad)+bold.Render(monthStr))
	lines = append(lines, renderMiniCalendar(m.endsDate, time.Now(), gridPad, m.theme))
	m.help.SetWidth(innerW)
	dpHelp := m.help.ShortHelpView(datePickerHelpKeys())
	dpHelpPad := max((innerW-lipgloss.Width(dpHelp))/2, 0)
	lines = append(lines, strings.Repeat(" ", dpHelpPad)+dpHelp)

	content := strings.Join(lines, "\n")
	boxH := 13
	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// DatePickerOpen reports whether the date picker overlay should be shown.
func (m EventFormModel) DatePickerOpen() bool {
	return m.datePickerOpen
}

// RRuleEditorOpen reports whether the recurrence editor overlay should be shown.
func (m EventFormModel) RRuleEditorOpen() bool {
	return m.rruleEditorOpen
}

// DatePickerBoxSize returns the outer dimensions of the date picker dialog.
func (m EventFormModel) DatePickerBoxSize() (int, int) {
	// width: help text (~44 chars) + padding(4) + border(2) = 50
	// height: 1 month header + 1 weekday header + 6 week rows + 1 help + padding(2) + border(2) = 13
	return 50, 13
}

// DatePickerView renders the date picker as a standalone bordered dialog.
func (m EventFormModel) DatePickerView() string {
	boxW, boxH := m.DatePickerBoxSize()
	innerW := boxW - 6

	bold := lipgloss.NewStyle().Bold(true)

	const gridW = 20 // width of "Su Mo Tu We Th Fr Sa"
	gridPad := max((innerW-gridW)/2, 0)
	var lines []string
	monthStr := m.day.Format("January 2006")
	monthPad := gridPad + max((gridW-len(monthStr))/2, 0)
	lines = append(lines, strings.Repeat(" ", monthPad)+bold.Render(monthStr))
	lines = append(lines, renderMiniCalendar(m.day, time.Now(), gridPad, m.theme))
	m.help.SetWidth(innerW)
	dpHelp := m.help.ShortHelpView(datePickerHelpKeys())
	dpHelpPad := max((innerW-lipgloss.Width(dpHelp))/2, 0)
	lines = append(lines, strings.Repeat(" ", dpHelpPad)+dpHelp)

	content := strings.Join(lines, "\n")

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

func (m EventFormModel) currentDuration() time.Duration {
	s, err1 := time.Parse("15:04", m.startTime.Value())
	e, err2 := time.Parse("15:04", m.endTime.Value())
	if err1 != nil || err2 != nil {
		return time.Hour
	}
	d := e.Sub(s)
	if d <= 0 {
		d += 24 * time.Hour
	}
	return d
}

// adjustEndTime sets end = start + dur whenever the start value is a valid time.
func (m *EventFormModel) adjustEndTime(dur time.Duration) {
	s, err := time.Parse("15:04", m.startTime.Value())
	if err != nil {
		return
	}
	end := s.Add(dur)
	m.endTime.SetValue(end.Format("15:04"))
}

func (m EventFormModel) buildRecurrenceRule() string {
	if m.repeatIdx == 0 {
		return ""
	}
	if m.repeatIdx == repeatCustomIdx {
		return m.customRule
	}
	rule := repeatPresets[m.repeatIdx].Rule
	switch m.ends {
	case endsAfter:
		count := strings.TrimSpace(m.endsCount.Value())
		if count != "" {
			rule += ";COUNT=" + count
		}
	case endsOnDate:
		rule += ";UNTIL=" + m.endsDate.UTC().Format("20060102T150405Z")
	}
	return rule
}

func (m EventFormModel) save() (EventFormModel, tea.Cmd) {
	title := strings.TrimSpace(m.title.Value())
	if title == "" {
		m.errMsg = "Title is required"
		return m, nil
	}
	if len(m.calendars) == 0 {
		m.errMsg = "No calendars available"
		return m, nil
	}

	calID := m.calendars[m.calendarIdx].ID
	day := m.day
	rrule := m.buildRecurrenceRule()

	editID := m.editID

	if m.allDay {
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 0, 1)
		return m, func() tea.Msg {
			return EventFormSaveMsg{
				EventID:        editID,
				CalendarID:     calID,
				Title:          title,
				Description:    strings.TrimSpace(m.description.Value()),
				Location:       strings.TrimSpace(m.location.Value()),
				StartTime:      start,
				EndTime:        end,
				AllDay:         true,
				RecurrenceRule: rrule,
			}
		}
	}

	startVal := strings.TrimSpace(m.startTime.Value())
	endVal := strings.TrimSpace(m.endTime.Value())

	st, err := time.Parse("15:04", startVal)
	if err != nil {
		m.errMsg = "Invalid start time (use HH:MM)"
		return m, nil
	}
	et, err := time.Parse("15:04", endVal)
	if err != nil {
		m.errMsg = "Invalid end time (use HH:MM)"
		return m, nil
	}

	start := time.Date(day.Year(), day.Month(), day.Day(),
		st.Hour(), st.Minute(), 0, 0, time.UTC)
	end := time.Date(day.Year(), day.Month(), day.Day(),
		et.Hour(), et.Minute(), 0, 0, time.UTC)
	if !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}

	desc := strings.TrimSpace(m.description.Value())
	loc := strings.TrimSpace(m.location.Value())

	return m, func() tea.Msg {
		return EventFormSaveMsg{
			EventID:        editID,
			CalendarID:     calID,
			Title:          title,
			Description:    desc,
			Location:       loc,
			StartTime:      start,
			EndTime:        end,
			RecurrenceRule: rrule,
		}
	}
}

// View renders the event form dialog.
func (m EventFormModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}

	boxW, boxH := m.boxSize()
	innerW := max(boxW-6, 20) // border + padding
	lw := 12

	faint := lipgloss.NewStyle().Faint(true)
	bold := lipgloss.NewStyle().Bold(true)

	var lines []string
	header := "New Event"
	if m.editID > 0 {
		header = "Edit Event"
	}
	lines = append(lines, bold.Render(header))
	lines = append(lines, "")

	// Title
	lines = append(lines, faint.Render(formLabel("Title", lw))+m.title.View())
	lines = append(lines, "")

	if !m.allDay {
		// Start → End with duration
		timeLine := faint.Render(formLabel("Time", lw)) +
			m.startTime.View() + faint.Render("  \u2192  ") + m.endTime.View()
		if dur := m.durationStr(); dur != "" {
			timeLine += faint.Render("  " + dur)
		}
		lines = append(lines, truncateTo(timeLine, innerW))
		lines = append(lines, "")
	}

	// Date
	dateStr := m.day.Format("Mon, Jan 2, 2006")
	if m.focusField == fieldDate {
		dateStr = lipgloss.NewStyle().Reverse(true).Render(dateStr) + faint.Render("  enter: pick")
	}
	lines = append(lines, faint.Render(formLabel("Date", lw))+dateStr)
	lines = append(lines, "")

	// All day toggle
	toggle := "[ ]"
	if m.allDay {
		toggle = "[x]"
	}
	if m.focusField == fieldAllDay {
		toggle = lipgloss.NewStyle().Reverse(true).Render(toggle)
	}
	lines = append(lines, faint.Render(formLabel("All day", lw))+toggle)
	lines = append(lines, "")

	// Repeat selector
	repeatLabel := repeatPresets[m.repeatIdx].Label
	if m.repeatIdx == repeatCustomIdx && m.customRule != "" {
		repeatLabel = m.rruleEditor.RuleSummary()
	}
	if m.focusField == fieldRepeat {
		hint := faint.Render("  \u25c0 \u25b6")
		if m.repeatIdx == repeatCustomIdx {
			hint = faint.Render("  enter: edit  \u25c0 \u25b6")
		}
		repeatLabel = lipgloss.NewStyle().Reverse(true).Render(repeatLabel) + hint
	}
	lines = append(lines, faint.Render(formLabel("Repeat", lw))+repeatLabel)
	lines = append(lines, "")

	// Ends condition (only when a simple preset is active, not Custom)
	if m.repeatIdx > 0 && m.repeatIdx != repeatCustomIdx {
		var endsLabel string
		switch m.ends {
		case endsNever:
			endsLabel = "Never"
		case endsAfter:
			endsLabel = "After " + m.endsCount.View() + " times"
		case endsOnDate:
			endsLabel = "On " + m.endsDate.Format("Jan 2, 2006")
			if m.focusField == fieldEnds {
				endsLabel += faint.Render("  enter: pick")
			}
		}
		if m.focusField == fieldEnds && m.ends != endsAfter {
			endsLabel = lipgloss.NewStyle().Reverse(true).Render(endsLabel) + faint.Render("  \u25c0 \u25b6")
		} else if m.focusField == fieldEnds {
			endsLabel = endsLabel + faint.Render("  \u25c0 \u25b6")
		}
		lines = append(lines, faint.Render(formLabel("Ends", lw))+endsLabel)
		lines = append(lines, "")
	}

	// Calendar selector
	if len(m.calendars) > 1 {
		cal := m.calendars[m.calendarIdx]
		dot := "\u25cf"
		if cal.Color != "" {
			dot = lipgloss.NewStyle().Foreground(lipgloss.Color(cal.Color)).Render("\u25cf")
		}
		calVal := dot + " " + cal.Name
		if m.focusField == fieldCalendar {
			calVal += faint.Render("  \u25c0 \u25b6")
		}
		lines = append(lines, faint.Render(formLabel("Calendar", lw))+calVal)
		lines = append(lines, "")
	}

	// Location
	lines = append(lines, faint.Render(formLabel("Location", lw))+m.location.View())
	lines = append(lines, "")

	// Description (with left border)
	lines = append(lines, faint.Render("Description"))
	descBorder := lipgloss.NewStyle().
		BorderLeft(true).
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(m.theme.Border).
		PaddingLeft(1)
	lines = append(lines, descBorder.Render(m.description.View()))

	// Error message
	if m.errMsg != "" {
		lines = append(lines, "")
		lines = append(lines, lipgloss.NewStyle().Foreground(m.theme.Error).Render(m.errMsg))
	}

	lines = append(lines, "")

	// Save / Cancel buttons (right-aligned, primary styling on Save)
	saveBtn := buttonStyled("Save", 0, m.focusField == fieldSave, true)
	cancelBtn := button("Cancel", 0, m.focusField == fieldCancel)
	buttons := cancelBtn + "   " + saveBtn
	pad := max(innerW-lipgloss.Width(buttons), 0)
	lines = append(lines, strings.Repeat(" ", pad)+buttons)

	lines = append(lines, "")

	// Help
	m.help.SetWidth(innerW)
	helpText := m.help.ShortHelpView(m.keys.ShortHelp())
	lines = append(lines, truncateTo(helpText, innerW))

	content := strings.Join(lines, "\n")

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

func (m EventFormModel) durationStr() string {
	s, err1 := time.Parse("15:04", m.startTime.Value())
	e, err2 := time.Parse("15:04", m.endTime.Value())
	if err1 != nil || err2 != nil {
		return ""
	}
	d := e.Sub(s)
	if d <= 0 {
		d += 24 * time.Hour
	}
	h := int(d.Hours())
	m2 := int(d.Minutes()) % 60
	switch {
	case h == 0:
		return fmt.Sprintf("%d min", m2)
	case m2 == 0:
		if h == 1 {
			return "1 hour"
		}
		return fmt.Sprintf("%d hours", h)
	default:
		return fmt.Sprintf("%dh %dm", h, m2)
	}
}

func formLabel(s string, w int) string {
	if len(s) >= w {
		return s
	}
	return s + strings.Repeat(" ", w-len(s))
}

// addMonthClamped shifts t by months, clamping the day so it stays valid
// (e.g. Jan 31 + 1 month → Feb 28, not Mar 3).
func addMonthClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	newMonth := time.Month(int(m) + months)
	maxDay := time.Date(y, newMonth+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > maxDay {
		d = maxDay
	}
	return time.Date(y, newMonth, d, 0, 0, 0, 0, t.Location())
}

// renderMiniCalendar draws a compact month grid with the selected day
// highlighted and today marked.
func renderMiniCalendar(selected, today time.Time, indent int, theme Theme) string {
	y, mo, _ := selected.Date()
	loc := selected.Location()

	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday()) // 0=Sun
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	pad := strings.Repeat(" ", indent)
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, pad+faint.Render("Su Mo Tu We Th Fr Sa"))

	dayNum := 1
	for week := 0; week < 6; week++ {
		var cells []string
		for dow := 0; dow < 7; dow++ {
			pos := week*7 + dow
			if pos < startDow || dayNum > daysInMonth {
				cells = append(cells, "  ")
			} else {
				cell := fmt.Sprintf("%2d", dayNum)
				d := time.Date(y, mo, dayNum, 0, 0, 0, 0, loc)
				if sameDay(d, selected) {
					cell = lipgloss.NewStyle().Reverse(true).Bold(true).Render(cell)
				} else if sameDay(d, today) {
					cell = lipgloss.NewStyle().Foreground(theme.Today).Bold(true).Render(cell)
				}
				cells = append(cells, cell)
				dayNum++
			}
		}
		lines = append(lines, pad+strings.Join(cells, " "))
		if dayNum > daysInMonth {
			// Pad remaining empty weeks so height is stable.
			for week++; week < 6; week++ {
				lines = append(lines, pad+strings.Repeat(" ", 20))
			}
			break
		}
	}

	return strings.Join(lines, "\n")
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
