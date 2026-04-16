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
	efKeyStart       = "start"
	efKeyEnd         = "end"
	efKeyDate        = "date"
	efKeyAllDay      = "allday"
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
	titleField *TextField
	startField *TextField
	endField   *TextField
	dateField  *StaticField
	allDayField *CheckboxField
	repeatField *SelectField
	endsField   *SelectField
	endsCountField *TextField
	calendarField  *SelectField
	locationField  *TextField
	descField      *TextAreaField

	// Overlay state
	repeatIdx       int // index into repeatPresets
	customRule      string
	rruleEditor     RecurrenceEditorModel
	rruleEditorOpen bool
	ends            endsMode
	endsDate        time.Time
	endsDatePicker  bool
	datePickerOpen  bool

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

func datePickerHelpKeys() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("left", "up", "right", "down"), key.WithHelp("arrows", "navigate")),
		key.NewBinding(key.WithKeys("[", "]"), key.WithHelp("[/]", "month")),
		key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "today")),
	}
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

	m.startField = NewTextField("HH:MM")
	m.startField.SetCharLimit(5)
	m.startField.SetFilter(FilterTimeInput)
	m.startField.SetValue(fmt.Sprintf("%02d:%02d", startHour, startMin))

	m.endField = NewTextField("HH:MM")
	m.endField.SetCharLimit(5)
	m.endField.SetFilter(FilterTimeInput)
	m.endField.SetValue(fmt.Sprintf("%02d:%02d", endHour, startMin))

	m.dateField = NewStaticField(m.day.Format("Mon, Jan 2, 2006"), nil)

	m.allDayField = NewCheckboxField("All day", false)

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

	if ev.AllDay {
		m.allDayField.SetChecked(true)
	}
	if !ev.AllDay {
		m.startField.SetValue(ev.StartTime.Local().Format("15:04"))
		m.endField.SetValue(ev.EndTime.Local().Format("15:04"))
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

	if !m.allDayField.Checked() {
		items = append(items, FormItem{Label: "Start", Field: m.startField})
		keys = append(keys, efKeyStart)
		items = append(items, FormItem{Label: "End", Field: m.endField})
		keys = append(keys, efKeyEnd)
	}

	m.dateField.SetValue(m.day.Format("Mon, Jan 2, 2006"))
	items = append(items, FormItem{Label: "Date", Field: m.dateField})
	keys = append(keys, efKeyDate)

	items = append(items, FormItem{Label: "All day", Field: m.allDayField})
	keys = append(keys, efKeyAllDay)

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
	prevAllDay := m.allDayField.Checked()
	prevRepeatIdx := m.repeatIdx
	prevEnds := m.ends

	m.repeatIdx = m.repeatField.Selected()
	m.ends = endsMode(m.endsField.Selected())
	if m.calendarField != nil {
		m.calendarIdx = m.calendarField.Selected()
	}

	// Rebuild form items if dynamic fields changed.
	needRebuild := m.allDayField.Checked() != prevAllDay ||
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

func (m *EventFormModel) handleFieldEnter(fieldKey string) tea.Cmd {
	switch fieldKey {
	case efKeyDate:
		m.datePickerOpen = true
		return noopCmd
	case efKeyRepeat:
		if m.repeatField.Selected() == repeatCustomIdx {
			m.rruleEditor = NewRecurrenceEditorModel(m.day, m.width, m.height, m.theme)
			m.rruleEditorOpen = true
			return noopCmd
		}
	case efKeyEnds:
		if endsMode(m.endsField.Selected()) == endsOnDate {
			m.endsDatePicker = true
			return noopCmd
		}
	}
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
	}

	// Forward mouse clicks through mouse tracker.
	if mc, ok := msg.(tea.MouseClickMsg); ok {
		if mc.Button == tea.MouseLeft {
			target := mouseResolve(mc.X, mc.Y)
			m.form, _ = m.form.Update(MouseEvent{IsClick: true, Target: target})
			return m, nil
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

func (m EventFormModel) updateDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleDatePickerMouse(mc)
	}
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleDatePickerKey(kp)
	}
	return m, nil
}

func (m EventFormModel) updateEndsDatePicker(msg tea.Msg) (EventFormModel, tea.Cmd) {
	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		return m.handleEndsDatePickerMouse(mc)
	}
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		return m.handleEndsDatePickerKey(kp)
	}
	return m, nil
}

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
		m.dateField.SetValue(m.day.Format("Mon, Jan 2, 2006"))
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

	ox := (m.width - boxW) / 2
	oy := (m.height - boxH) / 2
	gridX := ox + 3 + gridPad
	gridY := oy + 4

	rx := msg.X - gridX
	ry := msg.Y - gridY
	if rx < 0 || rx >= gridW || ry < 0 || ry >= 6 {
		return m, nil
	}

	dow := min(rx/3, 6)
	week := ry

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
	m.dateField.SetValue(m.day.Format("Mon, Jan 2, 2006"))
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

	dow := min(rx/3, 6)
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
func (m EventFormModel) EndsDatePickerOpen() bool { return m.endsDatePicker }

// DatePickerOpen reports whether the date picker overlay should be shown.
func (m EventFormModel) DatePickerOpen() bool { return m.datePickerOpen }

// RRuleEditorOpen reports whether the recurrence editor overlay should be shown.
func (m EventFormModel) RRuleEditorOpen() bool { return m.rruleEditorOpen }

// DatePickerBoxSize returns the outer dimensions of the date picker dialog.
func (m EventFormModel) DatePickerBoxSize() (int, int) { return 50, 13 }

// DatePickerView renders the date picker as a standalone bordered dialog.
func (m EventFormModel) DatePickerView() string {
	boxW, boxH := m.DatePickerBoxSize()
	innerW := boxW - 6
	bold := lipgloss.NewStyle().Bold(true)

	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)
	lines := make([]string, 0, 3)
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
		Width(boxW).Height(boxH).Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m EventFormModel) EndsDatePickerView() string {
	boxW, _ := m.DatePickerBoxSize()
	innerW := boxW - 6
	bold := lipgloss.NewStyle().Bold(true)

	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)
	lines := make([]string, 0, 3)
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
		Width(boxW).Height(boxH).Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(content)
}

// View renders the event form dialog.
func (m EventFormModel) View() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
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
			}
		}
	}

	startVal := strings.TrimSpace(m.startField.Value())
	endVal := strings.TrimSpace(m.endField.Value())

	st, err := time.Parse("15:04", startVal)
	if err != nil {
		// Find the start field index
		for i, k := range m.fieldKeys {
			if k == efKeyStart {
				f.SetError(i, "Invalid start time (use HH:MM)")
				return nil
			}
		}
		return nil
	}
	et, err := time.Parse("15:04", endVal)
	if err != nil {
		for i, k := range m.fieldKeys {
			if k == efKeyEnd {
				f.SetError(i, "Invalid end time (use HH:MM)")
				return nil
			}
		}
		return nil
	}

	start := time.Date(day.Year(), day.Month(), day.Day(),
		st.Hour(), st.Minute(), 0, 0, time.UTC)
	end := time.Date(day.Year(), day.Month(), day.Day(),
		et.Hour(), et.Minute(), 0, 0, time.UTC)
	if !end.After(start) {
		end = end.AddDate(0, 0, 1)
	}

	desc := strings.TrimSpace(m.descField.Value())
	loc := strings.TrimSpace(m.locationField.Value())

	return func() tea.Msg {
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

// addMonthClamped shifts t by months, clamping the day so it stays valid.
func addMonthClamped(t time.Time, months int) time.Time {
	y, m, d := t.Date()
	newMonth := time.Month(int(m) + months)
	maxDay := time.Date(y, newMonth+1, 0, 0, 0, 0, 0, t.Location()).Day()
	if d > maxDay {
		d = maxDay
	}
	return time.Date(y, newMonth, d, 0, 0, 0, 0, t.Location())
}

// renderMiniCalendar draws a compact month grid.
func renderMiniCalendar(selected, today time.Time, indent int, theme Theme) string {
	y, mo, _ := selected.Date()
	loc := selected.Location()

	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday())
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	pad := strings.Repeat(" ", indent)
	faint := lipgloss.NewStyle().Faint(true)

	var lines []string
	lines = append(lines, pad+faint.Render("Su Mo Tu We Th Fr Sa"))

	dayNum := 1
	for week := range 6 {
		var cells []string
		for dow := range 7 {
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
			for week++; week < 6; week++ {
				lines = append(lines, pad+strings.Repeat(" ", 20))
			}
			break
		}
	}

	return strings.Join(lines, "\n")
}

const formLabelWidth = 12

func formLabel(s string) string {
	if len(s) >= formLabelWidth {
		return s
	}
	return s + strings.Repeat(" ", formLabelWidth-len(s))
}

func sameDay(a, b time.Time) bool {
	ay, am, ad := a.Date()
	by, bm, bd := b.Date()
	return ay == by && am == bm && ad == bd
}
