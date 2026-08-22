package tui

import (
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/viewport"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/model"
)

// EventFormSaveMsg is emitted when the user saves the event form. The parent
// decides whether this is an update or a create from the live form's
// editID (m.form.editID). The message cannot carry that value reliably.
// The save closure is bound at form build time, before editID is set
// in NewEventFormModelForEdit.
//
// InstanceTime is the original (un-edited) occurrence time when the form was
// opened on a recurring instance. The parent uses it to dispatch a scope
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
// validation passes. EventFormModel.Update intercepts it so the
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

	// origAttendees retains the full attendee structs from the event being
	// edited. The People field only surfaces email addresses, so on save we
	// re-attach each email's original Role/RSVPStatus/CUType/CN instead of
	// flattening everyone back to defaults (issue #109).
	origAttendees []model.Attendee

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
	body   viewport.Model
	// fieldKeys maps form item index → field key for OnFieldEnter
	fieldKeys []string

	keys      eventFormKeyMap
	help      help.Model
	width     int
	height    int
	theme     Theme
	weekStart time.Weekday
}

type eventFormKeyMap struct {
	Save  key.Binding
	Close key.Binding
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
	// No OnRebuild closure: it would capture the construction-time value
	// receiver, so its syncFromForm would mutate a stale copy's form rather
	// than the live one the app holds (issue #496). EventFormModel.Update
	// calls m.syncFromForm() on the live receiver after every form.Update
	// instead, mirroring RecurrenceEditorModel.
	m.body = viewport.New()
	m.body.MouseWheelEnabled = true
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
		focused := m.form.Focused()
		items, keys := m.buildFormItems()
		m.fieldKeys = keys
		m.form.RemoveItems(0)
		m.form.AppendItems(items...)
		// Re-clamp focus: a rebuild can shrink the form (e.g. Repeat→None
		// drops the Ends field). The OnRebuild closure used to do this on the
		// live form; now that Update drives syncFromForm we own it here,
		// mirroring RecurrenceEditorModel.syncFromForm.
		if focused >= m.form.totalCount() {
			focused = m.form.totalCount() - 1
		}
		if focused < 0 {
			focused = 0
		}
		m.form.focused = focused
		if m.form.focused < len(m.form.items) && !m.form.items[m.form.focused].Field.IsFocusable() {
			m.form, _ = m.form.skipToFocusable(1)
		}
	}
}

// noopCmd is a non-nil Cmd that does nothing. Used to signal to Form's
// OnFieldEnter that the default focus-next behavior should be suppressed.
func noopCmd() tea.Msg { return nil }

// tryOpenOverlay checks whether the currently focused form field should open
// an overlay on Enter and, if so, opens it. Returns a non-nil cmd (noopCmd)
// when an overlay was opened. The caller can then skip a forward to the form.
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
			m.rruleEditor = NewRecurrenceEditorModel(m.day, m.width, m.height, m.theme).SetWeekStart(m.weekStart)
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
// It restores any previously committed range so a second open shows the
// user's last selection rather than a blank state.
func (m *EventFormModel) openDatePicker() {
	m.datePicker = NewMiniMonthModel(m.day).Focus().FocusGrid().
		SetTheme(m.theme.Selected, m.theme.Today, m.theme.Text, m.theme.Muted).
		SetRangeColor(m.theme.Selected).
		SetWeekStart(m.weekStart)
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
		SetTheme(m.theme.Selected, m.theme.Today, m.theme.Text, m.theme.Muted).
		SetWeekStart(m.weekStart)
	m.endsDatePicker = true
	m.dpBtnFocus = -1
}

// handleFieldEnter is the Form.OnFieldEnter callback. Overlay open is
// handled in EventFormModel.Update to avoid the value-receiver closure bug.
// This callback exists for non-overlay field-enter behavior.
func (m *EventFormModel) handleFieldEnter(fieldKey string) tea.Cmd {
	return nil // nil = proceed with default focus-next
}

// SetWeekStart sets the first day of the week for date-picker grids.
func (m EventFormModel) SetWeekStart(w time.Weekday) EventFormModel {
	m.weekStart = w
	return m
}

func (m EventFormModel) SetSize(w, h int) EventFormModel {
	m.width = w
	m.height = h
	m.dialog = m.dialog.Update(tea.WindowSizeMsg{Width: w, Height: h})
	m.form.SetWidth(m.dialog.ContentWidth())
	m.syncBodyViewport(true)
	return m
}

func (m EventFormModel) formViewportHeight() int {
	const chromeLines = 2 + // top + bottom border
		1 + // top padding (PaddingY)
		2 + // dialog title + blank line
		2 // blank line + help footer
	actionLines := 1 + max(lipgloss.Height(m.form.ButtonRowView()), 1) // separator + buttons
	return max(m.height-chromeLines-actionLines, 1)
}

func (m *EventFormModel) syncBodyViewport(keepFocusVisible bool) {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	cw := m.dialog.ContentWidth()
	if cw <= 0 {
		return
	}
	bodyLines := strings.Split(m.form.BodyView(), "\n")
	m.body.SetWidth(cw)
	m.body.SetHeight(min(len(bodyLines), m.formViewportHeight()))
	m.body.SetContentLines(bodyLines)
	if keepFocusVisible {
		m.keepFocusedFieldVisible()
	}
}

func (m *EventFormModel) keepFocusedFieldVisible() {
	if m.body.Height() <= 0 {
		return
	}
	line := m.form.FocusedLine()
	if line < 0 {
		// Focus is on the button row, not a body field; leave the
		// scroll position where the last field left it.
		return
	}
	if line < m.body.YOffset() {
		m.body.ScrollUp(m.body.YOffset() - line)
		return
	}
	bottom := m.body.YOffset() + m.body.Height() - 1
	if line > bottom {
		m.body.ScrollDown(line - bottom)
	}
}

func (m EventFormModel) bodyOverflows() bool {
	return m.body.TotalLineCount() > m.body.VisibleLineCount()
}

func (m EventFormModel) scrollHint() string {
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

func (m EventFormModel) actionsSeparator(w int) string {
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

// BoxSize returns the outer dimensions of the form dialog.
func (m EventFormModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}
