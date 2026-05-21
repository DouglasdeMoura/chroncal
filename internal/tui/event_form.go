package tui

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
)

// EventFormSaveMsg is emitted when the user saves the event form. The parent
// decides whether this is an update or a create by reading the live form's
// editID (m.form.editID) — the message cannot carry that value reliably
// because the save closure is bound at form build time, before editID is set
// in NewEventFormModelForEdit.
//
// InstanceTime is the original (un-edited) occurrence time when the form was
// opened on a recurring instance — used by the parent to dispatch a scope
// prompt (this event / this and following / all events). Zero means the form
// was opened on a non-recurring event or a fresh create.
type EventFormSaveMsg struct {
	CalendarID     int64
	Title          string
	Description    string
	Location       string
	ConferenceURI  string
	StartTime      time.Time
	EndTime        time.Time
	AllDay         bool
	RecurrenceRule string
	Timezone       string
	Transp         string
	Class          string
	Categories     string
	Attendees      []model.Attendee
	Alarms         []model.Alarm
	InstanceTime   time.Time
}

// EventFormClosedMsg is emitted when the user closes the event form.
type EventFormClosedMsg struct{}

// eventFormSubmitNowMsg is emitted by the form's OnSubmit closure after
// validation passes. It is intercepted by EventFormModel.Update so the
// save runs against the up-to-date model rather than the stale captured
// receiver inside the OnSubmit closure (see EventFormSaveMsg note).
type eventFormSubmitNowMsg struct{}

func newEventFormSeparator() *StaticField {
	return NewStaticField("", nil)
}

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
	ID         int64
	Name       string
	Color      string
	OwnerEmail string
}

// ---------------------------------------------------------------------------
// Named field keys for OnFieldEnter lookup
// ---------------------------------------------------------------------------

const (
	efKeyTitle       = "title"
	efKeyTime        = "time"
	efKeyDate        = "date"
	efKeyAllDay      = "allday"
	efKeyTimezone    = "timezone"
	efKeyTransp      = "transp"
	efKeyClass       = "class"
	efKeyRepeat      = "repeat"
	efKeyEnds        = "ends"
	efKeyEndsCount   = "endscount"
	efKeyAlarms      = "alarms"
	efKeyCalendar    = "calendar"
	efKeyPeople      = "people"
	efKeyLocation    = "location"
	efKeyConference  = "conference"
	efKeyDescription = "description"
	efKeyTags        = "tags"
)

// EventFormModel is the Bubble Tea model for the event creation/edit form.
type EventFormModel struct {
	editID      int64 // 0 = create mode, >0 = editing this event ID
	day         time.Time
	calendars   []calendarOption
	calendarIdx int

	// instanceTime is the un-edited occurrence start when the form is editing
	// one instance of a recurring series. Zero for fresh creates, non-recurring
	// events, or when editing the master from a non-instance entry point.
	instanceTime time.Time

	// Fields (pointer types survive form rebuilds)
	titleField        *TextField
	timeField         *TimeRangeField
	dateField         *DatePickerField
	allDayField       *CheckboxField
	timezoneField     *TimezoneField
	transparencyField *SelectField
	visibilityField   *SelectField
	repeatField       *SelectField
	endsField         *SelectField
	endsCountField    *TextField
	alarmField        *OpenerField
	calendarField     *SelectField
	peopleField       *TextField
	locationField     *TextField
	conferenceField   *TextField
	descField         *TextAreaField
	tagsField         *TextField

	// Overlay state
	allDay             bool
	timezonePicker     TimezonePickerModel
	timezonePickerOpen bool
	repeatIdx          int // index into repeatPresets
	customRule         string
	rruleEditor        RecurrenceEditorModel
	rruleEditorOpen    bool
	ends               endsMode
	endsDate           time.Time
	endsDatePicker     bool
	datePickerOpen     bool
	alarms             []model.Alarm
	alarmEditor        AlarmListEditorModel
	alarmEditorOpen    bool

	// Mini-month models for date picker overlays
	datePicker          MiniMonthModel
	endsDatePickerModel MiniMonthModel
	// dpBtnFocus: -1 grid, 0 Cancel, 1 Ok, 2 range checkbox.
	// 2 is only reachable inside the event-date picker (not ends-date).
	dpBtnFocus int

	// Multi-day range state (event-date picker only). rangeEnd is zero
	// until the user pins it; when non-zero the form treats the event as
	// spanning startDate..endDate.
	rangeMode    bool
	rangeStart   time.Time // pinned start (zero when no pin yet)
	rangeEnd     time.Time // pinned end (zero when only start is pinned)
	rangePickEnd bool      // true = next Enter pins end; false = next Enter (re-)pins start
	rangeEndDate time.Time // persisted across picker opens: the end date the form will save
	rangeHasEnd  bool      // true when rangeEndDate is meaningful (set on Ok in range mode)

	// Dialog + Form
	dialog Dialog
	form   Form
	// fieldKeys maps form item index → field key for OnFieldEnter
	fieldKeys []string

	keys   eventFormKeyMap
	help   help.Model
	width  int
	height int
	theme  Theme
}

type eventFormKeyMap struct {
	Save  key.Binding
	Close key.Binding
}

// NewEventFormModel creates a new event form for the given day.
func NewEventFormModel(day time.Time, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	calOpts := make([]calendarOption, 0, len(calendars))
	for id, info := range calendars {
		calOpts = append(calOpts, calendarOption{ID: id, Name: info.Name, Color: info.Color, OwnerEmail: info.OwnerEmail})
	}
	sort.Slice(calOpts, func(i, j int) bool { return calOpts[i].Name < calOpts[j].Name })

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

	m := EventFormModel{
		day:       day,
		calendars: calOpts,
		endsDate:  day.AddDate(0, 1, 0),
		theme:     theme,
		help:      newThemedHelp(theme),
		keys: eventFormKeyMap{
			Save:  key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
			Close: key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
		},
	}

	// Build fields
	m.titleField = NewTextField("Event title")
	m.titleField.SetCharLimit(200)

	m.timeField = NewTimeRangeField(theme.TextDim)
	m.timeField.SetStartValue(fmt.Sprintf("%02d:%02d", startHour, startMin))
	m.timeField.SetEndValue(fmt.Sprintf("%02d:%02d", endHour, startMin))

	m.dateField = NewDatePickerField(m.day)

	m.allDayField = NewCheckboxField("All day", false)

	m.timezoneField = NewTimezoneField(LocalIANATimezone())

	m.transparencyField = NewSelectField([]SelectOption{
		{Label: "Busy", Value: "OPAQUE"},
		{Label: "Free", Value: "TRANSPARENT"},
	})

	m.visibilityField = NewSelectField([]SelectOption{
		{Label: "Public", Value: "PUBLIC"},
		{Label: "Private", Value: "PRIVATE"},
		{Label: "Confidential", Value: "CONFIDENTIAL"},
	})

	repeatOpts := make([]SelectOption, len(repeatPresets))
	for i, p := range repeatPresets {
		repeatOpts[i] = SelectOption{Label: p.Label, Value: p.Rule}
	}
	m.repeatField = NewSelectField(repeatOpts)

	endsOpts := []SelectOption{
		{Label: "Never", Value: "never"},
		{Label: "After", Value: "after"},
		{Label: "On date", Value: "ondate"},
	}
	m.endsField = NewSelectField(endsOpts)

	m.endsCountField = NewTextField("10")
	m.endsCountField.SetCharLimit(4)
	m.endsCountField.SetDigitsOnly()
	m.endsCountField.suffix = "times"

	if len(calOpts) > 1 {
		calSelectOpts := make([]SelectOption, len(calOpts))
		calendarColors := make(map[string]string, len(calOpts))
		for i, c := range calOpts {
			label := c.Name
			if c.OwnerEmail != "" && strings.EqualFold(c.Name, c.OwnerEmail) {
				label = c.OwnerEmail
			} else if c.OwnerEmail != "" {
				label += " (" + c.OwnerEmail + ")"
			}
			calSelectOpts[i] = SelectOption{Label: label, Value: fmt.Sprintf("%d", c.ID)}
			calendarColors[calSelectOpts[i].Value] = c.Color
		}
		m.calendarField = NewSelectField(calSelectOpts)
		m.calendarField.SetRenderLabel(func(opt SelectOption, focused bool) string {
			dot := Glyphs["dot"]
			if color := calendarColors[opt.Value]; color != "" {
				dot = lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render(Glyphs["dot"])
			}
			name := opt.Label
			if focused {
				name = lipgloss.NewStyle().Reverse(true).Render(name)
			}
			return dot + " " + name
		})
	}

	m.peopleField = NewTextField("Comma-separated emails")
	m.peopleField.SetCharLimit(500)

	m.locationField = NewTextField("Add location")
	m.locationField.SetCharLimit(200)

	m.conferenceField = NewTextField("Add conference link")
	m.conferenceField.SetCharLimit(500)

	m.descField = NewTextAreaField("Add description")
	m.descField.SetCharLimit(500)
	m.descField.SetHeight(3)

	m.tagsField = NewTextField("Comma-separated tags")
	m.tagsField.SetCharLimit(500)

	// Build dialog + form
	m.buildDialogAndForm()

	cmd := m.form.Init()
	return m, cmd
}

// multiDayEndDate reports whether the event spans more than one calendar
// day and, if so, returns the last included day (inclusive, local). For
// all-day events the stored end is exclusive midnight of the day after
// the last included day; for timed events the actual end instant is used.
func multiDayEndDate(ev event.Event) (time.Time, bool) {
	if ev.AllDay {
		s := ev.StartTime.UTC()
		e := ev.EndTime.UTC()
		startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)
		// End is exclusive: subtract a minute to get the last included day.
		last := e.Add(-time.Minute)
		lastDay := time.Date(last.Year(), last.Month(), last.Day(), 0, 0, 0, 0, time.UTC)
		if lastDay.After(startDay) {
			return lastDay, true
		}
		return time.Time{}, false
	}
	s := ev.StartTime.Local()
	e := ev.EndTime.Local()
	startDay := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, s.Location())
	endDay := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, e.Location())
	// If the end instant is exactly midnight of the next day, the event
	// doesn't actually touch that day (exclusive semantics).
	if e.Equal(endDay) && !endDay.Equal(startDay) {
		endDay = endDay.AddDate(0, 0, -1)
	}
	if endDay.After(startDay) {
		return endDay, true
	}
	return time.Time{}, false
}

// NewEventFormModelForEdit creates a form pre-filled with an existing event's data.
func NewEventFormModelForEdit(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	return NewEventFormModelForEditInstance(ev, time.Time{}, calendars, theme)
}

// NewEventFormModelForEditInstance is like NewEventFormModelForEdit but also
// records the instance time the user clicked on for a recurring event. The
// instance time travels with the form so the parent can prompt for scope
// (this event / this and following / all events) on save.
func NewEventFormModelForEditInstance(ev event.Event, instanceTime time.Time, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModel(ev.StartTime, calendars, theme)
	m.editID = ev.ID
	m.instanceTime = instanceTime
	m.titleField.SetValue(ev.Title)
	m.locationField.SetValue(ev.Location)
	m.conferenceField.SetValue(ev.ConferenceURI)
	m.descField.SetValue(ev.Description)

	// Restore timezone.
	if ev.Timezone != "" {
		m.timezoneField.SetValue(ev.Timezone)
	}
	if ev.Transp != "" {
		for i, opt := range m.transparencyField.options {
			if strings.EqualFold(opt.Value, ev.Transp) {
				m.transparencyField.SetSelected(i)
				break
			}
		}
	}
	if ev.Class != "" {
		for i, opt := range m.visibilityField.options {
			if strings.EqualFold(opt.Value, ev.Class) {
				m.visibilityField.SetSelected(i)
				break
			}
		}
	}

	// Resolve the display timezone for formatting start/end times.
	displayLoc := time.Local
	if ev.Timezone != "" {
		if loc, err := time.LoadLocation(ev.Timezone); err == nil {
			displayLoc = loc
		}
	}

	if ev.AllDay {
		m.allDayField.SetChecked(true)
		m.allDay = true
	}
	if !ev.AllDay {
		m.timeField.SetStartValue(ev.StartTime.In(displayLoc).Format("15:04"))
		m.timeField.SetEndValue(ev.EndTime.In(displayLoc).Format("15:04"))
	}

	// Detect multi-day events and pre-fill the range. For all-day events
	// the stored end is exclusive midnight of the day after the last
	// included day; for timed events it's the actual end instant.
	if endDate, ok := multiDayEndDate(ev); ok {
		m.rangeHasEnd = true
		m.rangeEndDate = endDate
		m.dateField.SetRangeEnd(endDate)
	}

	// Select the correct calendar.
	if m.calendarField != nil {
		for i, c := range m.calendars {
			if c.ID == ev.CalendarID {
				m.calendarField.SetSelected(i)
				m.calendarIdx = i
				break
			}
		}
	}

	// Pre-fill attendees.
	if len(ev.Attendees) > 0 {
		emails := make([]string, 0, len(ev.Attendees))
		for _, a := range ev.Attendees {
			emails = append(emails, a.Email)
		}
		m.peopleField.SetValue(strings.Join(emails, ", "))
	}

	if ev.Categories != "" {
		m.tagsField.SetValue(ev.Categories)
	}

	// Pre-fill alarms.
	if len(ev.Alarms) > 0 {
		m.alarms = append([]model.Alarm(nil), ev.Alarms...)
	}

	// Parse recurrence rule.
	if ev.RecurrenceRule != "" {
		m.repeatIdx, m.customRule, m.ends, m.endsDate = parseRecurrenceRule(ev.RecurrenceRule, m.day)
		m.repeatField.SetSelected(m.repeatIdx)
		m.endsField.SetSelected(int(m.ends))
		if m.ends == endsAfter {
			if count := rruleParam(ev.RecurrenceRule, "COUNT"); count != "" {
				m.endsCountField.SetValue(count)
			}
		}
	}

	m.rebuildDialog()
	return m, cmd
}

// NewEventFormModelForDuplicate creates a form pre-filled with an existing
// event's data but in create mode (editID = 0).
func NewEventFormModelForDuplicate(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModelForEdit(ev, calendars, theme)
	m.editID = 0
	m.rebuildDialog()
	return m, cmd
}

// parseRecurrenceRule matches a recurrence rule against the form presets and
// extracts ending conditions.
func parseRecurrenceRule(rule string, fallbackDate time.Time) (int, string, endsMode, time.Time) {
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
	base := strings.Join(baseParts, ";")

	for i := 1; i < len(repeatPresets); i++ {
		if i == repeatCustomIdx {
			continue
		}
		if strings.EqualFold(base, repeatPresets[i].Rule) {
			return i, "", ends, endsDate
		}
	}

	return repeatCustomIdx, rule, ends, endsDate
}

func rruleParam(rule, name string) string {
	for p := range strings.SplitSeq(rule, ";") {
		if k, v, ok := strings.Cut(p, "="); ok && strings.EqualFold(k, name) {
			return v
		}
	}
	return ""
}

// FilterTimeInput allows digits and ':' for HH:MM time input.
func FilterTimeInput(k tea.Key) bool {
	if k.Text == "" {
		return true
	}
	r := rune(k.Text[0])
	return (r >= '0' && r <= '9') || r == ':'
}

func (m *EventFormModel) buildDialogAndForm() {
	title := "Create Event"
	if m.editID > 0 {
		title = "Edit Event"
	}

	styles := DefaultDialogStyles()
	m.dialog = NewDialog(title, styles)
	m.dialog.SetWidth(58) // 58 total = 2 border + 4 padding + 52 content

	formStyles := DefaultFormStyles()
	formStyles.LabelLayout = LabelInline
	formStyles.ShowFocusMarker = true
	formStyles.ButtonAlign = ButtonAlignRight
	formStyles.ButtonRule = true

	items, keys := m.buildFormItems()
	m.fieldKeys = keys

	m.form = NewForm("Save", formStyles, items...)

	// NOTE: this closure must NOT read state from the captured m. Any
	// value-typed field on EventFormModel (editID, customRule, alarms, day,
	// endsDate, range state…) lands on the caller's value copy, not on the
	// m captured here, so reading them would yield stale data. Instead the
	// closure just emits eventFormSubmitNowMsg; EventFormModel.Update
	// intercepts that message and runs save() on the live model.
	m.form.OnSubmit(func(f *Form) tea.Cmd {
		return func() tea.Msg { return eventFormSubmitNowMsg{} }
	})
	m.form.OnCancel(func(f *Form) tea.Cmd {
		return func() tea.Msg { return EventFormClosedMsg{} }
	})
	m.form.OnFieldEnter(func(f *Form, field int) tea.Cmd {
		if field < len(m.fieldKeys) {
			return m.handleFieldEnter(m.fieldKeys[field])
		}
		return nil
	})
	m.form.OnRebuild(func(f *Form) {
		m.syncFromForm()
	})
}

func (m *EventFormModel) rebuildDialog() {
	title := "Create Event"
	if m.editID > 0 {
		title = "Edit Event"
	}
	m.dialog.SetTitle(title)

	// Preserve callbacks by rebuilding items only.
	items, keys := m.buildFormItems()
	m.fieldKeys = keys
	m.form.RemoveItems(0)
	m.form.AppendItems(items...)
}

func (m *EventFormModel) buildFormItems() ([]FormItem, []string) {
	var items []FormItem
	var keys []string

	items = append(items, FormItem{Label: "Title", Field: m.titleField, Required: true})
	keys = append(keys, efKeyTitle)

	items = append(items, FormItem{Label: "", Field: newEventFormSeparator()})
	keys = append(keys, "")

	allDay := m.allDayField.Checked()
	m.timeField.SetDisabled(allDay)
	items = append(items, FormItem{Label: "Time", Field: m.timeField, Required: !allDay})
	keys = append(keys, efKeyTime)

	m.dateField.SetDate(m.day)
	items = append(items, FormItem{Label: "Date", Field: m.dateField})
	keys = append(keys, efKeyDate)

	items = append(items, FormItem{Label: "All day", Field: m.allDayField})
	keys = append(keys, efKeyAllDay)

	items = append(items, FormItem{Label: "Timezone", Field: m.timezoneField})
	keys = append(keys, efKeyTimezone)

	items = append(items, FormItem{Label: "Repeat", Field: m.repeatField})
	keys = append(keys, efKeyRepeat)

	m.repeatIdx = m.repeatField.Selected()
	if m.repeatIdx > 0 && m.repeatIdx != repeatCustomIdx {
		items = append(items, FormItem{Label: "Ends", Field: m.endsField})
		keys = append(keys, efKeyEnds)

		m.ends = endsMode(m.endsField.Selected())
		if m.ends == endsAfter {
			items = append(items, FormItem{Label: "", Field: m.endsCountField,
				LabelLayout: LayoutPtr(LabelInline), ShowFocusMarker: BoolPtr(true)})
			keys = append(keys, efKeyEndsCount)
		}
	}

	items = append(items, FormItem{Label: "", Field: newEventFormSeparator()})
	keys = append(keys, "")

	items = append(items, FormItem{Label: "People", Field: m.peopleField})
	keys = append(keys, efKeyPeople)

	items = append(items, FormItem{Label: "Location", Field: m.locationField})
	keys = append(keys, efKeyLocation)

	items = append(items, FormItem{Label: "Conference", Field: m.conferenceField})
	keys = append(keys, efKeyConference)

	items = append(items, FormItem{Label: "Notes", Field: m.descField})
	keys = append(keys, efKeyDescription)

	items = append(items, FormItem{Label: "Tags", Field: m.tagsField})
	keys = append(keys, efKeyTags)

	if m.calendarField != nil {
		items = append(items, FormItem{Label: "Calendar", Field: m.calendarField})
		keys = append(keys, efKeyCalendar)
	}

	items = append(items, FormItem{Label: "Show as", Field: m.transparencyField})
	keys = append(keys, efKeyTransp)

	items = append(items, FormItem{Label: "Visibility", Field: m.visibilityField})
	keys = append(keys, efKeyClass)

	items = append(items, FormItem{Label: "", Field: newEventFormSeparator()})
	keys = append(keys, "")

	if m.alarmField == nil {
		m.alarmField = NewOpenerField(alarmSummary(m.alarms))
	} else {
		m.alarmField.SetValue(alarmSummary(m.alarms))
	}
	items = append(items, FormItem{Label: "Alarms", Field: m.alarmField})
	keys = append(keys, efKeyAlarms)

	return items, keys
}

func (m *EventFormModel) syncFromForm() {
	prevAllDay := m.allDay
	prevRepeatIdx := m.repeatIdx
	prevEnds := m.ends

	m.allDay = m.allDayField.Checked()
	m.repeatIdx = m.repeatField.Selected()
	m.ends = endsMode(m.endsField.Selected())
	if m.calendarField != nil {
		m.calendarIdx = m.calendarField.Selected()
	}

	// Rebuild form items if dynamic fields changed.
	needRebuild := m.allDay != prevAllDay ||
		m.repeatIdx != prevRepeatIdx ||
		m.ends != prevEnds

	if needRebuild {
		items, keys := m.buildFormItems()
		m.fieldKeys = keys
		m.form.RemoveItems(0)
		m.form.AppendItems(items...)
	}
}

// noopCmd is a non-nil Cmd that does nothing. Used to signal to Form's
// OnFieldEnter that the default focus-next behavior should be suppressed.
func noopCmd() tea.Msg { return nil }

// tryOpenOverlay checks whether the currently focused form field should open
// an overlay on Enter and, if so, opens it. Returns a non-nil cmd (noopCmd)
// when an overlay was opened so the caller can skip forwarding to the form.
func (m *EventFormModel) tryOpenOverlay() tea.Cmd {
	idx := m.form.Focused()
	if idx >= len(m.fieldKeys) {
		return nil
	}
	switch m.fieldKeys[idx] {
	case efKeyDate:
		m.openDatePicker()
		return noopCmd
	case efKeyTimezone:
		m.timezonePicker = NewTimezonePickerModel(m.timezoneField.Value(), m.theme)
		m.timezonePickerOpen = true
		return noopCmd
	case efKeyRepeat:
		if m.repeatField.Selected() == repeatCustomIdx {
			m.rruleEditor = NewRecurrenceEditorModel(m.day, m.width, m.height, m.theme)
			if m.customRule != "" {
				m.rruleEditor.LoadRule(m.customRule)
			}
			m.rruleEditorOpen = true
			return noopCmd
		}
	case efKeyEnds:
		if endsMode(m.endsField.Selected()) == endsOnDate {
			m.openEndsDatePicker()
			return noopCmd
		}
	case efKeyAlarms:
		m.alarmEditor = NewAlarmListEditorModel(m.alarms, m.width, m.height, m.theme)
		m.alarmEditorOpen = true
		return noopCmd
	}
	return nil
}

// openDatePicker initialises the MiniMonthModel and opens the overlay.
// Restores any previously committed range so re-opening shows the user's
// last selection rather than a blank state.
func (m *EventFormModel) openDatePicker() {
	m.datePicker = NewMiniMonthModel(m.day).Focus().FocusGrid().
		SetTheme(m.theme.Selected, m.theme.Today, m.theme.Text, m.theme.Muted).
		SetRangeColor(m.theme.Selected)
	m.rangeMode = m.rangeHasEnd
	m.rangeStart = time.Time{}
	m.rangeEnd = time.Time{}
	m.rangePickEnd = false
	if m.rangeHasEnd {
		m.rangeStart = m.day
		m.rangeEnd = m.rangeEndDate
		m.rangePickEnd = false
		m.datePicker = m.datePicker.SetRange(true, m.rangeStart, m.rangeEnd)
	}
	m.datePickerOpen = true
	m.dpBtnFocus = -1
}

// openEndsDatePicker initialises the ends-date MiniMonthModel and opens the overlay.
func (m *EventFormModel) openEndsDatePicker() {
	m.endsDatePickerModel = NewMiniMonthModel(m.endsDate).Focus().FocusGrid().
		SetTheme(m.theme.Selected, m.theme.Today, m.theme.Text, m.theme.Muted)
	m.endsDatePicker = true
	m.dpBtnFocus = -1
}

// handleFieldEnter is the Form.OnFieldEnter callback. Overlay opening is
// handled in EventFormModel.Update to avoid the value-receiver closure bug;
// this callback exists for non-overlay field-enter behavior.
func (m *EventFormModel) handleFieldEnter(fieldKey string) tea.Cmd {
	return nil // nil = proceed with default focus-next
}

func (m EventFormModel) SetSize(w, h int) EventFormModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.form.SetWidth(m.dialog.ContentWidth())
	return m
}

// BoxSize returns the outer dimensions of the form dialog.
func (m EventFormModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

// Update handles messages for the event form.
func (m EventFormModel) Update(msg tea.Msg) (EventFormModel, tea.Cmd) {
	// Window resize
	if msg, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(msg.Width, msg.Height), nil
	}

	// Form OnSubmit emits this after validation. Run save against the live
	// model here so we don't read stale state from the OnSubmit closure's
	// captured receiver.
	if _, ok := msg.(eventFormSubmitNowMsg); ok {
		return m, m.save(&m.form)
	}

	// Overlays capture all input when open.
	if m.rruleEditorOpen {
		return m.updateRRuleEditor(msg)
	}
	if m.alarmEditorOpen {
		return m.updateAlarmEditor(msg)
	}
	if m.timezonePickerOpen {
		return m.updateTimezonePicker(msg)
	}
	if m.datePickerOpen {
		return m.updateDatePicker(msg)
	}
	if m.endsDatePicker {
		return m.updateEndsDatePicker(msg)
	}

	if kp, ok := msg.(tea.KeyPressMsg); ok {
		switch {
		case key.Matches(kp, m.keys.Save):
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		case key.Matches(kp, m.keys.Close):
			return m, func() tea.Msg { return EventFormClosedMsg{} }
		}

		// Handle overlay-opening Enter presses directly (not via the
		// form's onFieldEnter callback) so mutations survive the
		// value-receiver copy.
		if kp.String() == "enter" {
			if cmd := m.tryOpenOverlay(); cmd != nil {
				return m, cmd
			}
		}
	}

	// Forward mouse clicks through mouse tracker.
	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			// Translate screen coordinates to dialog-local coordinates.
			bw, bh := m.BoxSize()
			ox := (m.width - bw) / 2
			oy := (m.height - bh) / 2
			target := mouseResolve(mc.X-ox, mc.Y-oy)
			var cmd tea.Cmd
			m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
			// Click on DatePickerField opens the date picker overlay.
			if idx := m.form.Focused(); idx < len(m.fieldKeys) && m.fieldKeys[idx] == efKeyDate {
				m.openDatePicker()
			}
			return m, cmd
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	return m, cmd
}

func (m EventFormModel) updateRRuleEditor(msg tea.Msg) (EventFormModel, tea.Cmd) {
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

func (m EventFormModel) updateAlarmEditor(msg tea.Msg) (EventFormModel, tea.Cmd) {
	var cmd tea.Cmd
	m.alarmEditor, cmd = m.alarmEditor.Update(msg)
	if m.alarmEditor.Done() {
		m.alarms = m.alarmEditor.Alarms()
		if m.alarmField != nil {
			m.alarmField.SetValue(alarmSummary(m.alarms))
		}
		m.alarmEditorOpen = false
	} else if m.alarmEditor.Cancelled() {
		m.alarmEditorOpen = false
	}
	return m, cmd
}

func (m EventFormModel) updateTimezonePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	var cmd tea.Cmd
	m.timezonePicker, cmd = m.timezonePicker.Update(msg)
	if m.timezonePicker.Done() {
		m.timezoneField.SetValue(m.timezonePicker.Selected())
		m.timezonePickerOpen = false
	} else if m.timezonePicker.Cancelled() {
		m.timezonePickerOpen = false
	}
	return m, cmd
}

func (m EventFormModel) updateDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleDatePickerMouse(mc)
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch kp.String() {
	case "esc", "q":
		m.datePickerOpen = false
		return m, nil
	case "r", "R":
		// Global shortcut: toggle range mode from anywhere in the overlay.
		m.toggleRangeMode()
		return m, nil
	case "tab":
		m, m.datePicker = m.dpAdvanceFocus(m.datePicker)
		return m, nil
	case "shift+tab":
		m, m.datePicker = m.dpRetreatFocus(m.datePicker)
		return m, nil
	case "enter", "space":
		switch {
		case m.dpBtnFocus == 2: // Range checkbox
			m.toggleRangeMode()
		case m.dpBtnFocus == 0: // Cancel
			m.datePickerOpen = false
		case m.dpBtnFocus == 1: // Ok
			m.commitDatePickerSelection()
		case !m.datePicker.AtEnd(): // Chevron focused: let MiniMonth handle it
			m.datePicker, _ = m.datePicker.Update(kp)
		case m.rangeMode: // Grid + range mode: pin an endpoint
			m.pinRangeEndpoint(m.datePicker.Cursor())
		default: // Grid focused: confirm single date
			m.commitDatePickerSelection()
		}
		return m, nil
	}
	// Forward navigation keys only when calendar is focused.
	if m.dpBtnFocus == -1 {
		m.datePicker, _ = m.datePicker.Update(kp)
	}
	return m, nil
}

// toggleRangeMode flips range-mode on/off inside the date picker. Turning
// it on auto-pins the current cursor as the range start so the user sees
// immediate feedback; turning it off clears the end pin and keeps start as
// the plain single-date selection.
func (m *EventFormModel) toggleRangeMode() {
	m.rangeMode = !m.rangeMode
	if m.rangeMode {
		m.rangeStart = m.datePicker.Cursor()
		m.rangeEnd = time.Time{}
		m.rangePickEnd = true
		m.datePicker = m.datePicker.SetRange(true, m.rangeStart, m.rangeEnd)
	} else {
		m.rangeEnd = time.Time{}
		m.rangePickEnd = false
		m.datePicker = m.datePicker.SetRange(false, time.Time{}, time.Time{})
	}
}

// pinRangeEndpoint commits the current cursor position as either the start
// or the end of the range, depending on which endpoint is being picked.
// After pinning end, the next Enter on a day re-pins start (reset cycle).
func (m *EventFormModel) pinRangeEndpoint(d time.Time) {
	if m.rangePickEnd {
		m.rangeEnd = d
		m.rangePickEnd = false
	} else {
		m.rangeStart = d
		m.rangeEnd = time.Time{}
		m.rangePickEnd = true
	}
	m.datePicker = m.datePicker.SetRange(true, m.rangeStart, m.rangeEnd)
}

// commitDatePickerSelection closes the overlay, writing the current cursor
// (or range) back to the form. In range mode with both endpoints pinned,
// the earlier endpoint becomes the event date and the later endpoint is
// stored as rangeEndDate for the save path.
func (m *EventFormModel) commitDatePickerSelection() {
	if m.rangeMode && !m.rangeStart.IsZero() && !m.rangeEnd.IsZero() {
		lo, hi := m.rangeStart, m.rangeEnd
		if hi.Before(lo) {
			lo, hi = hi, lo
		}
		m.day = lo
		m.rangeEndDate = hi
		m.rangeHasEnd = !sameDay(lo, hi)
		m.dateField.SetDate(m.day)
		if m.rangeHasEnd {
			m.dateField.SetRangeEnd(m.rangeEndDate)
		} else {
			m.dateField.ClearRangeEnd()
		}
	} else {
		m.day = m.datePicker.Cursor()
		m.rangeHasEnd = false
		m.rangeEndDate = time.Time{}
		m.dateField.SetDate(m.day)
		m.dateField.ClearRangeEnd()
	}
	m.datePickerOpen = false
}

func (m EventFormModel) updateEndsDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleEndsDatePickerMouse(mc)
	}
	kp, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch kp.String() {
	case "esc", "q":
		m.endsDatePicker = false
		return m, nil
	case "tab":
		m, m.endsDatePickerModel = m.dpAdvanceFocus(m.endsDatePickerModel)
		return m, nil
	case "shift+tab":
		m, m.endsDatePickerModel = m.dpRetreatFocus(m.endsDatePickerModel)
		return m, nil
	case "enter", "space":
		switch {
		case m.dpBtnFocus == 0: // Cancel
			m.endsDatePicker = false
		case m.dpBtnFocus == 1: // Ok
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		case !m.endsDatePickerModel.AtEnd(): // Chevron focused
			m.endsDatePickerModel, _ = m.endsDatePickerModel.Update(kp)
		default: // Grid focused
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		}
		return m, nil
	}
	if m.dpBtnFocus == -1 {
		m.endsDatePickerModel, _ = m.endsDatePickerModel.Update(kp)
	}
	return m, nil
}

// dpAdvanceFocus moves focus forward through the event-date picker's tab
// stops: ‹ → › → grid → [range checkbox] → Cancel → Ok → ‹.
// The range checkbox stop is skipped when mm is the ends-date picker —
// detected via the caller distinguishing event-date vs ends-date.
func (m EventFormModel) dpAdvanceFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	// Only the event-date picker exposes the range checkbox stop.
	hasRange := m.datePickerOpen
	if m.dpBtnFocus >= 0 {
		switch {
		case hasRange && m.dpBtnFocus == 2: // checkbox → Cancel
			m.dpBtnFocus = 0
		case m.dpBtnFocus == 0: // Cancel → Ok
			m.dpBtnFocus = 1
		default: // Ok → ‹
			m.dpBtnFocus = -1
			mm = mm.Focus().FocusFirst()
		}
		return m, mm
	}
	if mm.AtEnd() {
		mm = mm.Blur()
		if hasRange {
			m.dpBtnFocus = 2 // grid → range checkbox
		} else {
			m.dpBtnFocus = 0 // grid → Cancel (ends-date picker)
		}
	} else {
		mm = mm.AdvanceFocus()
	}
	return m, mm
}

// dpRetreatFocus moves focus backward: Ok → Cancel → [range checkbox] → grid → › → ‹.
func (m EventFormModel) dpRetreatFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	hasRange := m.datePickerOpen
	if m.dpBtnFocus >= 0 {
		switch {
		case m.dpBtnFocus == 1: // Ok → Cancel
			m.dpBtnFocus = 0
		case m.dpBtnFocus == 0 && hasRange: // Cancel → checkbox
			m.dpBtnFocus = 2
		default: // Cancel (no range) or checkbox → grid
			m.dpBtnFocus = -1
			mm = mm.Focus().FocusLast()
		}
		return m, mm
	}
	if mm.AtStart() {
		mm = mm.Blur()
		m.dpBtnFocus = 1
	} else {
		mm = mm.RetreatFocus()
	}
	return m, mm
}

func (m EventFormModel) handleDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2

	// Content-relative coordinates: border(1) + padding(left=1, top=1).
	mmX := msg.X - ox - 2
	mmY := msg.Y - oy - 2

	if mmX < 0 || mmY < 0 {
		return m, nil
	}

	// Checkbox row (Y=9 in event-date layout: 8 cal + 1 blank). Clicks on
	// the label itself toggle range mode.
	if mmY == 9 && mmX < len("[x] Multi-day")+2 {
		m.toggleRangeMode()
		return m, nil
	}

	if mmY == m.datePickerButtonRowY() {
		switch m.datePickerButtonHit(mmX) {
		case "cancel":
			m.datePickerOpen = false
		case "ok":
			m.commitDatePickerSelection()
		}
		return m, nil
	}

	// Calendar grid hit-testing.
	if mmX >= miniMonthHeaderWidth {
		return m, nil
	}

	// Click on calendar: ensure calendar is focused, not buttons.
	m.dpBtnFocus = -1
	m.datePicker = m.datePicker.Focus()

	prevMonth := m.datePicker.DisplayMonth()
	m.datePicker, _ = m.datePicker.HandleClick(mmX, mmY)

	monthChanged := m.datePicker.DisplayMonth().Month() != prevMonth.Month() ||
		m.datePicker.DisplayMonth().Year() != prevMonth.Year()
	if !monthChanged && mmY >= 2 {
		if m.rangeMode {
			m.pinRangeEndpoint(m.datePicker.Cursor())
		} else {
			m.commitDatePickerSelection()
		}
	}

	return m, nil
}

// datePickerButtonHit returns "cancel", "ok", or "" based on x position.
func (m EventFormModel) datePickerButtonHit(x int) string {
	boxW, _ := m.DatePickerBoxSize()
	innerW := boxW - 4
	bs := DefaultButtonStyles()
	cancelW := lipgloss.Width(bs.Normal.Render("Cancel", false))
	okW := lipgloss.Width(bs.Normal.Render("Ok", false))
	totalW := cancelW + 1 + okW
	pad := max(innerW-totalW, 0)
	if x >= pad && x < pad+cancelW {
		return "cancel"
	}
	if x >= pad+cancelW+1 && x < pad+cancelW+1+okW {
		return "ok"
	}
	return ""
}

func (m EventFormModel) handleEndsDatePickerMouse(msg tea.MouseClickMsg) (EventFormModel, tea.Cmd) {
	boxW, boxH := m.DatePickerBoxSize()
	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2

	mmX := msg.X - ox - 2
	mmY := msg.Y - oy - 2

	if mmX < 0 || mmY < 0 {
		return m, nil
	}

	if mmY == m.datePickerButtonRowY() {
		switch m.datePickerButtonHit(mmX) {
		case "cancel":
			m.endsDatePicker = false
		case "ok":
			m.endsDate = m.endsDatePickerModel.Cursor()
			m.endsDatePicker = false
		}
		return m, nil
	}

	// Calendar grid hit-testing.
	if mmX >= miniMonthHeaderWidth {
		return m, nil
	}

	// Click on calendar: ensure calendar is focused, not buttons.
	m.dpBtnFocus = -1
	m.endsDatePickerModel = m.endsDatePickerModel.Focus()

	prevMonth := m.endsDatePickerModel.DisplayMonth()
	m.endsDatePickerModel, _ = m.endsDatePickerModel.HandleClick(mmX, mmY)

	monthChanged := m.endsDatePickerModel.DisplayMonth().Month() != prevMonth.Month() ||
		m.endsDatePickerModel.DisplayMonth().Year() != prevMonth.Year()
	if !monthChanged && mmY >= 2 {
		m.endsDate = m.endsDatePickerModel.Cursor()
		m.endsDatePicker = false
	}

	return m, nil
}

// EndsDatePickerOpen reports whether the ends-date picker overlay should be shown.
func (m EventFormModel) EndsDatePickerOpen() bool { return m.endsDatePicker }

// DatePickerOpen reports whether the date picker overlay should be shown.
func (m EventFormModel) DatePickerOpen() bool { return m.datePickerOpen }

// TimezonePickerOpen reports whether the timezone picker overlay should be shown.
func (m EventFormModel) TimezonePickerOpen() bool { return m.timezonePickerOpen }

// RRuleEditorOpen reports whether the recurrence editor overlay should be shown.
func (m EventFormModel) RRuleEditorOpen() bool { return m.rruleEditorOpen }

// AlarmEditorOpen reports whether the alarm editor overlay should be shown.
func (m EventFormModel) AlarmEditorOpen() bool { return m.alarmEditorOpen }

// TimezonePickerBoxSize returns the outer dimensions of the timezone picker dialog.
func (m EventFormModel) TimezonePickerBoxSize() (int, int) { return 50, 19 }

// TimezonePickerView renders the timezone picker as a standalone bordered dialog.
func (m EventFormModel) TimezonePickerView() string {
	boxW, boxH := m.TimezonePickerBoxSize()
	content := m.timezonePicker.View()

	innerW := boxW - 4

	sepStyle := lipgloss.NewStyle().Faint(true)
	sep := sepStyle.Render(strings.Repeat("─", innerW))

	// Action buttons right-aligned.
	bs := DefaultButtonStyles()
	focus := m.timezonePicker.BtnFocus()
	cancelBtn := bs.Normal.Render("Cancel", focus == tzFocusCancel)
	okBtn := bs.Normal.Render("Ok", focus == tzFocusOk)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	buttonRow = strings.Repeat(" ", btnPad) + buttonRow

	// Key hints: dark keys, dimmed description, centered.
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	dot := descStyle.Render(Glyphs["separator.dot"])
	hints := "↑↓" + " " + descStyle.Render("navigate") +
		dot + "tab" + " " + descStyle.Render("next") +
		dot + "esc" + " " + descStyle.Render("close")
	hintsWidth := lipgloss.Width(hints)
	hintsPad := max((innerW-hintsWidth)/2, 0)
	hints = strings.Repeat(" ", hintsPad) + hints

	content = content + "\n\n" + sep + "\n" + buttonRow + "\n" + "\n" + hints

	return lipgloss.NewStyle().
		Width(boxW).Height(boxH).Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// DatePickerBoxSize returns the outer dimensions of the date picker dialog.
// The event-date picker is taller to accommodate the Date-range checkbox
// row and Start/End status line; the ends-date picker keeps the compact
// size since it never shows range UI.
func (m EventFormModel) DatePickerBoxSize() (int, int) {
	if m.datePickerOpen {
		return 40, 17
	}
	return 40, 14
}

// datePickerButtonRowY is the Y coordinate of the Cancel/Ok row inside the
// date picker overlay's content area. Mouse handlers use it to detect
// clicks on the button row. Event-date picker: 8 cal + 3 range + 1 blank
// + 1 separator = 13. Ends-date picker: 8 + 1 + 1 = 10.
func (m EventFormModel) datePickerButtonRowY() int {
	if m.datePickerOpen {
		return 13
	}
	return 10
}

// datePickerOverlayView renders a MiniMonthModel inside a bordered dialog
// box with the calendar grid on the left and key hints stacked vertically
// on the right. When supportRange is true (event-date picker), a
// "Multi-day" checkbox row and a Start/End status line appear between
// the grid and the button row.
func (m EventFormModel) datePickerOverlayView(mm MiniMonthModel, supportRange bool) string {
	boxW, boxH := m.DatePickerBoxSize()

	calView := strings.TrimRight(mm.View(), "\n")
	calLines := strings.Split(calView, "\n")

	// Compute max display width of calendar lines for consistent padding.
	maxCalW := 0
	for _, line := range calLines {
		if w := lipgloss.Width(line); w > maxCalW {
			maxCalW = w
		}
	}

	// Vertically-stacked key hints: dark key, lighter description.
	descStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	hintLines := []string{
		"←↓↑→" + " " + descStyle.Render("navigate"),
		"[]" + "   " + descStyle.Render("month"),
		"t" + "    " + descStyle.Render("today"),
	}
	if supportRange {
		hintLines = append(hintLines, "r"+"    "+descStyle.Render("range"))
	}

	// Bottom-align hints with calendar lines.
	hintStart := len(calLines) - len(hintLines)

	resultLines := make([]string, 0, len(calLines)+5)
	for i, line := range calLines {
		w := lipgloss.Width(line)
		padded := line + strings.Repeat(" ", max(maxCalW-w, 0))
		if i >= hintStart && i-hintStart < len(hintLines) {
			padded += "  " + hintLines[i-hintStart]
		}
		resultLines = append(resultLines, padded)
	}

	innerW := boxW - 4

	// Range checkbox + status line (event-date picker only). Always emit
	// 3 lines (blank + checkbox + status-or-blank) so the box height stays
	// stable when the user toggles range mode.
	if supportRange {
		resultLines = append(resultLines, "")
		resultLines = append(resultLines, m.renderRangeCheckbox())
		if m.rangeMode {
			resultLines = append(resultLines, m.renderRangeStatus())
		} else {
			resultLines = append(resultLines, "")
		}
	}

	// Action buttons right-aligned at the bottom, separated by a line.
	resultLines = append(resultLines, "")
	sepStyle := lipgloss.NewStyle().Faint(true)
	resultLines = append(resultLines, sepStyle.Render(strings.Repeat("─", innerW)))
	bs := DefaultButtonStyles()
	cancelBtn := bs.Normal.Render("Cancel", m.dpBtnFocus == 0)
	okBtn := bs.Normal.Render("Ok", m.dpBtnFocus == 1)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	resultLines = append(resultLines, strings.Repeat(" ", btnPad)+buttonRow)

	content := strings.Join(resultLines, "\n")
	return lipgloss.NewStyle().
		Width(boxW).Height(boxH).Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// renderRangeCheckbox returns a single line: "[x] Multi-day" (or "[ ]"),
// reversed when the checkbox is the focused tab stop so the user sees
// where input will land.
func (m EventFormModel) renderRangeCheckbox() string {
	mark := "[ ]"
	if m.rangeMode {
		mark = "[x]"
	}
	label := mark + " Multi-day"
	if m.dpBtnFocus == 2 {
		return lipgloss.NewStyle().Reverse(true).Bold(true).Render(label)
	}
	return label
}

// renderRangeStatus returns a faint-labelled "Start: Apr 16   End: Apr 24"
// line summarising what the user has pinned so far. The endpoint currently
// being picked is emphasised; the other is shown as "—" when unpinned.
func (m EventFormModel) renderRangeStatus() string {
	labelStyle := lipgloss.NewStyle().Foreground(m.theme.TextDim)
	valueStyle := lipgloss.NewStyle().Bold(true)
	dim := lipgloss.NewStyle().Foreground(m.theme.TextDim)

	fmtPin := func(t time.Time) string {
		if t.IsZero() {
			return dim.Render("—")
		}
		return valueStyle.Render(t.Format("Jan 2"))
	}

	startCell := fmtPin(m.rangeStart)
	endCell := fmtPin(m.rangeEnd)

	// Highlight the endpoint currently being picked so the flow is legible.
	activeMark := lipgloss.NewStyle().Foreground(m.theme.Selected).Bold(true).Render("●")
	startMark := " "
	endMark := " "
	if m.rangePickEnd {
		endMark = activeMark
	} else if m.rangeMode {
		startMark = activeMark
	}

	return labelStyle.Render("Start:") + " " + startCell + startMark +
		"   " + labelStyle.Render("End:") + " " + endCell + endMark
}

// DatePickerView renders the date picker as a standalone bordered dialog.
func (m EventFormModel) DatePickerView() string {
	return m.datePickerOverlayView(m.datePicker, true)
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m EventFormModel) EndsDatePickerView() string {
	return m.datePickerOverlayView(m.endsDatePickerModel, false)
}

// View renders the event form dialog.
func (m EventFormModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		m.keys.Save,
		m.keys.Close,
	}
	m.dialog.SetFooter(m.help.ShortHelpView(helpKeys))
	content := mouseSweep(m.dialog.Box(m.form.View()))
	return content
}

func (m EventFormModel) buildRecurrenceRule() string {
	// Read straight from the pointer-backed fields so we don't depend on
	// the cached m.repeatIdx / m.ends, which are populated by syncFromForm
	// on the form's captured receiver — not on every value copy of the
	// model that reaches save().
	repeatIdx := m.repeatField.Selected()
	if repeatIdx == 0 {
		return ""
	}
	if repeatIdx == repeatCustomIdx {
		return m.customRule
	}
	rule := repeatPresets[repeatIdx].Rule
	switch endsMode(m.endsField.Selected()) {
	case endsAfter:
		count := strings.TrimSpace(m.endsCountField.Value())
		if count != "" {
			rule += ";COUNT=" + count
		}
	case endsOnDate:
		rule += ";UNTIL=" + m.endsDate.UTC().Format("20060102T150405Z")
	default:
		// endsNever
	}
	return rule
}

func (m EventFormModel) save(f *Form) tea.Cmd {
	calIdx := 0
	if m.calendarField != nil {
		calIdx = m.calendarField.Selected()
	}
	if calIdx >= len(m.calendars) || len(m.calendars) == 0 {
		f.SetError(0, "No calendars available")
		return nil
	}
	calID := m.calendars[calIdx].ID
	day := m.day
	rrule := m.buildRecurrenceRule()
	title := strings.TrimSpace(m.titleField.Value())
	tzName := m.timezoneField.Value()

	// Parse comma-separated emails into attendees.
	var attendees []model.Attendee
	if raw := strings.TrimSpace(m.peopleField.Value()); raw != "" {
		for _, part := range strings.Split(raw, ",") {
			email := strings.TrimSpace(part)
			if email != "" {
				attendees = append(attendees, model.Attendee{
					Email:      email,
					Role:       "REQ-PARTICIPANT",
					RSVPStatus: "NEEDS-ACTION",
					CUType:     "INDIVIDUAL",
				})
			}
		}
	}

	// Resolve the time.Location for the selected timezone.
	loc := time.UTC
	if tzName != "" && tzName != "UTC" {
		if parsed, err := time.LoadLocation(tzName); err == nil {
			loc = parsed
		}
	}

	if m.allDayField.Checked() {
		start := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 0, 1)
		if m.rangeHasEnd {
			endDay := time.Date(m.rangeEndDate.Year(), m.rangeEndDate.Month(), m.rangeEndDate.Day(), 0, 0, 0, 0, time.UTC)
			end = endDay.AddDate(0, 0, 1)
		}
		return func() tea.Msg {
			return EventFormSaveMsg{
				CalendarID:     calID,
				Title:          title,
				Description:    strings.TrimSpace(m.descField.Value()),
				Location:       strings.TrimSpace(m.locationField.Value()),
				ConferenceURI:  strings.TrimSpace(m.conferenceField.Value()),
				StartTime:      start,
				EndTime:        end,
				AllDay:         true,
				RecurrenceRule: rrule,
				Timezone:       tzName,
				Transp:         m.transparencyField.Value(),
				Class:          m.visibilityField.Value(),
				Categories:     strings.TrimSpace(m.tagsField.Value()),
				Attendees:      attendees,
				Alarms:         m.alarms,
				InstanceTime:   m.instanceTime,
			}
		}
	}

	startVal := strings.TrimSpace(m.timeField.StartValue())
	endVal := strings.TrimSpace(m.timeField.EndValue())

	st, err := time.Parse("15:04", startVal)
	if err != nil {
		for i, k := range m.fieldKeys {
			if k == efKeyTime {
				f.SetError(i, "Invalid start time (use HH:MM)")
				return nil
			}
		}
		return nil
	}
	et, err := time.Parse("15:04", endVal)
	if err != nil {
		for i, k := range m.fieldKeys {
			if k == efKeyTime {
				f.SetError(i, "Invalid end time (use HH:MM)")
				return nil
			}
		}
		return nil
	}

	// Interpret entered times in the selected timezone, then convert to UTC.
	start := time.Date(day.Year(), day.Month(), day.Day(),
		st.Hour(), st.Minute(), 0, 0, loc).UTC()
	endDay := day
	if m.rangeHasEnd {
		endDay = m.rangeEndDate
	}
	end := time.Date(endDay.Year(), endDay.Month(), endDay.Day(),
		et.Hour(), et.Minute(), 0, 0, loc).UTC()
	// Same-day cross-midnight fallback (HH:MM→HH:MM where end ≤ start).
	if !m.rangeHasEnd && !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}

	desc := strings.TrimSpace(m.descField.Value())
	location := strings.TrimSpace(m.locationField.Value())
	conference := strings.TrimSpace(m.conferenceField.Value())

	return func() tea.Msg {
		return EventFormSaveMsg{
			CalendarID:     calID,
			Title:          title,
			Description:    desc,
			Location:       location,
			ConferenceURI:  conference,
			StartTime:      start,
			EndTime:        end,
			RecurrenceRule: rrule,
			Timezone:       tzName,
			Transp:         m.transparencyField.Value(),
			Class:          m.visibilityField.Value(),
			Categories:     strings.TrimSpace(m.tagsField.Value()),
			Attendees:      attendees,
			Alarms:         m.alarms,
			InstanceTime:   m.instanceTime,
		}
	}
}
