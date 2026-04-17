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
	Timezone       string
}

// EventFormClosedMsg is emitted when the user closes the event form.
type EventFormClosedMsg struct{}

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

// ---------------------------------------------------------------------------
// Named field keys for OnFieldEnter lookup
// ---------------------------------------------------------------------------

const (
	efKeyTitle       = "title"
	efKeyTime        = "time"
	efKeyDate        = "date"
	efKeyAllDay      = "allday"
	efKeyTimezone    = "timezone"
	efKeyRepeat      = "repeat"
	efKeyEnds        = "ends"
	efKeyEndsCount   = "endscount"
	efKeyCalendar    = "calendar"
	efKeyLocation    = "location"
	efKeyDescription = "description"
)

// EventFormModel is the Bubble Tea model for the event creation/edit form.
type EventFormModel struct {
	editID      int64 // 0 = create mode, >0 = editing this event ID
	day         time.Time
	calendars   []calendarOption
	calendarIdx int

	// Fields (pointer types survive form rebuilds)
	titleField     *TextField
	timeField      *TimeRangeField
	dateField      *DatePickerField
	allDayField    *CheckboxField
	timezoneField  *TimezoneField
	repeatField    *SelectField
	endsField      *SelectField
	endsCountField *TextField
	calendarField  *SelectField
	locationField  *TextField
	descField      *TextAreaField

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

	// Mini-month models for date picker overlays
	datePicker          MiniMonthModel
	endsDatePickerModel MiniMonthModel
	dpBtnFocus          int // -1 = calendar focused, 0 = Cancel, 1 = Ok

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
		calOpts = append(calOpts, calendarOption{ID: id, Name: info.Name, Color: info.Color})
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

	if len(calOpts) > 1 {
		calSelectOpts := make([]SelectOption, len(calOpts))
		for i, c := range calOpts {
			calSelectOpts[i] = SelectOption{Label: c.Name, Value: fmt.Sprintf("%d", c.ID)}
		}
		m.calendarField = NewSelectField(calSelectOpts)
	}

	m.locationField = NewTextField("Add location")
	m.locationField.SetCharLimit(200)

	m.descField = NewTextAreaField("Add description")
	m.descField.SetCharLimit(500)
	m.descField.SetHeight(3)

	// Build dialog + form
	m.buildDialogAndForm()

	cmd := m.form.Init()
	return m, cmd
}

// NewEventFormModelForEdit creates a form pre-filled with an existing event's data.
func NewEventFormModelForEdit(ev event.Event, calendars map[int64]CalendarInfo, theme Theme) (EventFormModel, tea.Cmd) {
	m, cmd := NewEventFormModel(ev.StartTime, calendars, theme)
	m.editID = ev.ID
	m.titleField.SetValue(ev.Title)
	m.locationField.SetValue(ev.Location)
	m.descField.SetValue(ev.Description)

	// Restore timezone.
	if ev.Timezone != "" {
		m.timezoneField.SetValue(ev.Timezone)
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
	title := "New Event"
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

	editID := m.editID
	m.form.OnSubmit(func(f *Form) tea.Cmd {
		return m.save(f, editID)
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
	title := "New Event"
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

	if m.calendarField != nil {
		items = append(items, FormItem{Label: "Calendar", Field: m.calendarField})
		keys = append(keys, efKeyCalendar)
	}

	items = append(items, FormItem{Label: "Location", Field: m.locationField})
	keys = append(keys, efKeyLocation)

	items = append(items, FormItem{Label: "Notes", Field: m.descField})
	keys = append(keys, efKeyDescription)

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
			m.rruleEditorOpen = true
			return noopCmd
		}
	case efKeyEnds:
		if endsMode(m.endsField.Selected()) == endsOnDate {
			m.openEndsDatePicker()
			return noopCmd
		}
	}
	return nil
}

// openDatePicker initialises the MiniMonthModel and opens the overlay.
func (m *EventFormModel) openDatePicker() {
	m.datePicker = NewMiniMonthModel(m.day).Focus().FocusGrid().
		SetTheme(m.theme.Selected, m.theme.Today, m.theme.Text, m.theme.Muted)
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

	// Overlays capture all input when open.
	if m.rruleEditorOpen {
		return m.updateRRuleEditor(msg)
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
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		var cmd tea.Cmd
		m.rruleEditor, cmd = m.rruleEditor.Update(kp)
		if m.rruleEditor.Done() {
			m.customRule = m.rruleEditor.BuildRule()
			m.rruleEditorOpen = false
		} else if m.rruleEditor.Cancelled() {
			m.rruleEditorOpen = false
		}
		return m, cmd
	}
	return m, nil
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
	case "tab":
		m, m.datePicker = m.dpAdvanceFocus(m.datePicker)
		return m, nil
	case "shift+tab":
		m, m.datePicker = m.dpRetreatFocus(m.datePicker)
		return m, nil
	case "enter", "space":
		switch {
		case m.dpBtnFocus == 0: // Cancel
			m.datePickerOpen = false
		case m.dpBtnFocus == 1: // Ok
			m.day = m.datePicker.Cursor()
			m.dateField.SetDate(m.day)
			m.datePickerOpen = false
		case !m.datePicker.AtEnd(): // Chevron focused: let MiniMonth handle it
			m.datePicker, _ = m.datePicker.Update(kp)
		default: // Grid focused: confirm date
			m.day = m.datePicker.Cursor()
			m.dateField.SetDate(m.day)
			m.datePickerOpen = false
		}
		return m, nil
	}
	// Forward navigation keys only when calendar is focused.
	if m.dpBtnFocus == -1 {
		m.datePicker, _ = m.datePicker.Update(kp)
	}
	return m, nil
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

// dpAdvanceFocus moves focus forward: ‹ → › → grid → Cancel → Ok → ‹.
func (m EventFormModel) dpAdvanceFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	if m.dpBtnFocus >= 0 {
		if m.dpBtnFocus < 1 {
			m.dpBtnFocus++
		} else {
			m.dpBtnFocus = -1
			mm = mm.Focus().FocusFirst()
		}
		return m, mm
	}
	if mm.AtEnd() {
		mm = mm.Blur()
		m.dpBtnFocus = 0
	} else {
		mm = mm.AdvanceFocus()
	}
	return m, mm
}

// dpRetreatFocus moves focus backward: Ok → Cancel → grid → › → ‹.
func (m EventFormModel) dpRetreatFocus(mm MiniMonthModel) (EventFormModel, MiniMonthModel) {
	if m.dpBtnFocus >= 0 {
		if m.dpBtnFocus > 0 {
			m.dpBtnFocus--
		} else {
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

	// Button row: 8 calendar lines + 1 blank + 1 separator = Y index 10.
	if mmY == 10 {
		if m.datePickerButtonHit(mmX) == "cancel" {
			m.datePickerOpen = false
		} else if m.datePickerButtonHit(mmX) == "ok" {
			m.day = m.datePicker.Cursor()
			m.dateField.SetDate(m.day)
			m.datePickerOpen = false
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
		m.day = m.datePicker.Cursor()
		m.dateField.SetDate(m.day)
		m.datePickerOpen = false
	}

	return m, nil
}

// datePickerButtonHit returns "cancel", "ok", or "" based on x position.
func (m EventFormModel) datePickerButtonHit(x int) string {
	boxW, _ := m.DatePickerBoxSize()
	innerW := boxW - 4
	bs := DefaultButtonStyles()
	cancelW := lipgloss.Width(bs.Secondary.Render("Cancel", false))
	okW := lipgloss.Width(bs.Primary.Render("Ok", false))
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

	// Button row: 8 calendar lines + 1 blank + 1 separator = Y index 10.
	if mmY == 10 {
		if m.datePickerButtonHit(mmX) == "cancel" {
			m.endsDatePicker = false
		} else if m.datePickerButtonHit(mmX) == "ok" {
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
	cancelBtn := bs.Secondary.Render("Cancel", focus == tzFocusCancel)
	okBtn := bs.Primary.Render("Ok", focus == tzFocusOk)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	buttonRow = strings.Repeat(" ", btnPad) + buttonRow

	// Key hints: dark keys, dimmed description, centered.
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
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
func (m EventFormModel) DatePickerBoxSize() (int, int) { return 40, 14 }

// datePickerOverlayView renders a MiniMonthModel inside a bordered dialog
// box with the calendar grid on the left and key hints stacked vertically
// on the right.
func (m EventFormModel) datePickerOverlayView(mm MiniMonthModel) string {
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
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	hintLines := []string{
		"←↓↑→" + " " + descStyle.Render("navigate"),
		"[]" + "   " + descStyle.Render("month"),
		"t" + "    " + descStyle.Render("today"),
	}

	// Bottom-align hints with calendar lines.
	hintStart := len(calLines) - len(hintLines)

	var resultLines []string
	for i, line := range calLines {
		w := lipgloss.Width(line)
		padded := line + strings.Repeat(" ", max(maxCalW-w, 0))
		if i >= hintStart && i-hintStart < len(hintLines) {
			padded += "  " + hintLines[i-hintStart]
		}
		resultLines = append(resultLines, padded)
	}

	// Action buttons right-aligned at the bottom, separated by a line.
	innerW := boxW - 4
	resultLines = append(resultLines, "")
	sepStyle := lipgloss.NewStyle().Faint(true)
	resultLines = append(resultLines, sepStyle.Render(strings.Repeat("─", innerW)))
	bs := DefaultButtonStyles()
	cancelBtn := bs.Secondary.Render("Cancel", m.dpBtnFocus == 0)
	okBtn := bs.Primary.Render("Ok", m.dpBtnFocus == 1)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	resultLines = append(resultLines, strings.Repeat(" ", btnPad)+buttonRow)

	content := strings.Join(resultLines, "\n")
	return lipgloss.NewStyle().
		Width(boxW).Height(boxH).Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// DatePickerView renders the date picker as a standalone bordered dialog.
func (m EventFormModel) DatePickerView() string {
	return m.datePickerOverlayView(m.datePicker)
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m EventFormModel) EndsDatePickerView() string {
	return m.datePickerOverlayView(m.endsDatePickerModel)
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
	if m.repeatIdx == 0 {
		return ""
	}
	if m.repeatIdx == repeatCustomIdx {
		return m.customRule
	}
	rule := repeatPresets[m.repeatIdx].Rule
	switch m.ends {
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

func (m EventFormModel) save(f *Form, editID int64) tea.Cmd {
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
		return func() tea.Msg {
			return EventFormSaveMsg{
				EventID:        editID,
				CalendarID:     calID,
				Title:          title,
				Description:    strings.TrimSpace(m.descField.Value()),
				Location:       strings.TrimSpace(m.locationField.Value()),
				StartTime:      start,
				EndTime:        end,
				AllDay:         true,
				RecurrenceRule: rrule,
				Timezone:       tzName,
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
	end := time.Date(day.Year(), day.Month(), day.Day(),
		et.Hour(), et.Minute(), 0, 0, loc).UTC()
	if !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}

	desc := strings.TrimSpace(m.descField.Value())
	location := strings.TrimSpace(m.locationField.Value())

	return func() tea.Msg {
		return EventFormSaveMsg{
			EventID:        editID,
			CalendarID:     calID,
			Title:          title,
			Description:    desc,
			Location:       location,
			StartTime:      start,
			EndTime:        end,
			RecurrenceRule: rrule,
			Timezone:       tzName,
		}
	}
}

const formLabelWidth = 12

func formLabel(s string) string {
	if len(s) >= formLabelWidth {
		return s
	}
	return s + strings.Repeat(" ", formLabelWidth-len(s))
}
