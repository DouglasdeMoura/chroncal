package tui

import (
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/teambition/rrule-go"
)

type recurrenceEditorMsg int

const (
	recurrenceEditorDone recurrenceEditorMsg = iota
	recurrenceEditorCancel
)

const (
	recFieldEnds = "ends"
)

const recurrenceEditorBoxWidth = 52

var recFrequencies = []struct {
	Label string
	Freq  string
	Unit  [2]string // singular, plural
}{
	{"Daily", "DAILY", [2]string{"day", "days"}},
	{"Weekly", "WEEKLY", [2]string{"week", "weeks"}},
	{"Monthly", "MONTHLY", [2]string{"month", "months"}},
	{"Yearly", "YEARLY", [2]string{"year", "years"}},
}

var weekDayLabels = [7]string{"Su", "Mo", "Tu", "We", "Th", "Fr", "Sa"}
var weekDayRRule = [7]string{"SU", "MO", "TU", "WE", "TH", "FR", "SA"}

// RecurrenceEditorModel is the model for the advanced recurrence editor overlay.
type RecurrenceEditorModel struct {
	startDate time.Time

	eachField      *QuantitySelectField
	onField        *RecurrenceOnField
	endsField      *SelectField
	endsCountField *TextField

	endsDate       time.Time
	endsDatePicker bool

	form      Form
	fieldKeys []string

	preview   []time.Time
	done      bool
	cancelled bool

	help   help.Model
	width  int
	height int
	theme  Theme
}

// NewRecurrenceEditorModel creates a new editor pre-configured from the event date.
func NewRecurrenceEditorModel(startDate time.Time, w, h int, theme Theme) RecurrenceEditorModel {
	eachOpts := make([]SelectOption, len(recFrequencies))
	for i, freq := range recFrequencies {
		label := strings.ToUpper(freq.Unit[0][:1]) + freq.Unit[0][1:]
		eachOpts[i] = SelectOption{Label: label, Value: freq.Freq}
	}

	endsField := NewSelectField(nil)
	endsField.SetOptions([]SelectOption{
		{Label: "Never", Value: "never"},
		{Label: "After", Value: "after"},
		{Label: "On " + startDate.AddDate(0, 3, 0).Format("Jan 2, 2006"), Value: "ondate"},
	})

	endsCountField := NewTextField("1")
	endsCountField.SetCharLimit(4)
	endsCountField.SetDigitsOnly()
	endsCountField.SetSuffix("times")
	endsCountField.SetValue("1")

	m := RecurrenceEditorModel{
		startDate:      startDate,
		eachField:      NewQuantitySelectField(eachOpts, 1),
		onField:        NewRecurrenceOnField(startDate),
		endsField:      endsField,
		endsCountField: endsCountField,
		endsDate:       startDate.AddDate(0, 3, 0),
		help:           newThemedHelp(theme),
		width:          w,
		height:         h,
		theme:          theme,
	}
	m.updatePreview()
	m.buildForm()
	return m
}

func (m RecurrenceEditorModel) SetSize(w, h int) RecurrenceEditorModel {
	m.width = w
	m.height = h
	m.form.SetWidth(m.formWidth())
	return m
}

func (m RecurrenceEditorModel) Done() bool               { return m.done }
func (m RecurrenceEditorModel) Cancelled() bool          { return m.cancelled }
func (m RecurrenceEditorModel) EndsDatePickerOpen() bool { return m.endsDatePicker }

func (m *RecurrenceEditorModel) buildForm() {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	styles.ShowFocusMarker = true
	styles.ButtonAlign = ButtonAlignRight
	styles.ButtonRule = true

	items, keys := m.buildFormItems()
	m.fieldKeys = keys

	if m.form.ItemCount() == 0 {
		m.form = NewForm("Ok", styles, items...)
		m.form.OnSubmit(func(f *Form) tea.Cmd {
			return func() tea.Msg { return recurrenceEditorDone }
		})
		m.form.OnCancel(func(f *Form) tea.Cmd {
			return func() tea.Msg { return recurrenceEditorCancel }
		})
	} else {
		m.form.RemoveItems(0)
		m.form.AppendItems(items...)
	}
	m.form.SetWidth(m.formWidth())
}

func (m *RecurrenceEditorModel) buildFormItems() ([]FormItem, []string) {
	var items []FormItem
	var keys []string

	freq := m.currentFreq()
	m.endsField.SetOptions([]SelectOption{
		{Label: "Never", Value: "never"},
		{Label: "After", Value: "after"},
		{Label: "On " + m.endsDate.Format("Jan 2, 2006"), Value: "ondate"},
	})

	items = append(items, FormItem{Label: "Repeat every", Field: m.eachField})
	keys = append(keys, "")

	switch freq {
	case "WEEKLY":
		m.onField.SetWeekly(m.onField.WeekDays(), m.onField.WeekDayCursor())
		items = append(items, FormItem{Label: "On", Field: m.onField})
		keys = append(keys, "")
	case "MONTHLY":
		m.onField.SetMonthly(m.startDate, m.onField.MonthlyMode())
		items = append(items, FormItem{Label: "On", Field: m.onField})
		keys = append(keys, "")
	}

	items = append(items, FormItem{Label: "Ends", Field: m.endsField})
	keys = append(keys, recFieldEnds)

	if m.currentEnds() == endsAfter {
		items = append(items, FormItem{
			Label:           " ",
			Field:           m.endsCountField,
			LabelLayout:     LayoutPtr(LabelInline),
			ShowFocusMarker: BoolPtr(true),
		})
		keys = append(keys, "")
	}

	items = append(items, FormItem{Label: "", Field: NewStaticField("", nil)})
	keys = append(keys, "")
	items = append(items, FormItem{
		Label: "",
		Field: NewStaticField("Preview", func(s string) string {
			return lipgloss.NewStyle().Faint(true).Render(s)
		}),
	})
	keys = append(keys, "")

	if len(m.preview) == 0 {
		items = append(items, FormItem{
			Label: "",
			Field: NewStaticField("  (no occurrences)", func(s string) string {
				return lipgloss.NewStyle().Faint(true).Render(s)
			}),
		})
		keys = append(keys, "")
	} else {
		for _, t := range m.preview {
			items = append(items, FormItem{
				Label: "",
				Field: NewStaticField("  "+t.Format("Mon, Jan 2, 2006"), nil),
			})
			keys = append(keys, "")
		}
	}

	return items, keys
}

func (m *RecurrenceEditorModel) syncFromForm() {
	m.updatePreview()
	focused := m.form.Focused()
	items, keys := m.buildFormItems()
	m.fieldKeys = keys
	m.form.RemoveItems(0)
	m.form.AppendItems(items...)
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
	m.form.SetWidth(m.formWidth())
}

func (m RecurrenceEditorModel) currentFreq() string {
	return m.eachField.Value()
}

func (m RecurrenceEditorModel) currentEnds() endsMode {
	return endsMode(m.endsField.Selected())
}

func (m RecurrenceEditorModel) intervalValue() int {
	n, err := strconv.Atoi(strings.TrimSpace(m.eachField.Amount()))
	if err != nil || n <= 0 {
		return 1
	}
	return n
}

func (m RecurrenceEditorModel) formWidth() int {
	styles := DefaultDialogStyles()
	return recurrenceEditorBoxWidth - 2 - 2*styles.PaddingX
}

func (m *RecurrenceEditorModel) tryOpenOverlay() tea.Cmd {
	idx := m.form.Focused()
	if idx >= len(m.fieldKeys) {
		return nil
	}
	if m.fieldKeys[idx] == recFieldEnds && m.currentEnds() == endsOnDate {
		m.endsDatePicker = true
		return noopCmd
	}
	return nil
}

// Update handles all messages for the recurrence editor.
func (m RecurrenceEditorModel) Update(msg tea.Msg) (RecurrenceEditorModel, tea.Cmd) {
	if ws, ok := msg.(tea.WindowSizeMsg); ok {
		return m.SetSize(ws.Width, ws.Height), nil
	}

	if m.endsDatePicker {
		if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
			w, h := m.EndsDatePickerBoxSize()
			m = m.HandleEndsDateMouse(mc, w, h)
			return m, nil
		}
		if kp, ok := msg.(tea.KeyPressMsg); ok {
			m = m.handleEndsDateKey(kp)
			return m, nil
		}
		return m, nil
	}

	switch msg {
	case recurrenceEditorDone:
		m.done = true
		return m, nil
	case recurrenceEditorCancel:
		m.cancelled = true
		return m, nil
	}

	if kp, ok := msg.(tea.KeyPressMsg); ok {
		switch kp.String() {
		case "ctrl+s":
			var cmd tea.Cmd
			m.form, cmd = m.form.Submit()
			return m, cmd
		case "esc":
			m.cancelled = true
			return m, nil
		case "enter":
			if cmd := m.tryOpenOverlay(); cmd != nil {
				return m, cmd
			}
		}
	}

	if mc, ok := msg.(tea.MouseClickMsg); ok && mc.Button == tea.MouseLeft {
		bw, bh := m.BoxSize()
		ox := (m.width - bw) / 2
		oy := (m.height - bh) / 2
		target := mouseResolve(mc.X-ox, mc.Y-oy)
		var cmd tea.Cmd
		m.form, cmd = m.form.Update(MouseEvent{IsClick: true, Target: target})
		m.syncFromForm()
		if idx := m.form.Focused(); idx < len(m.fieldKeys) &&
			m.fieldKeys[idx] == recFieldEnds &&
			m.currentEnds() == endsOnDate &&
			target == fieldTarget(idx) {
			m.endsDatePicker = true
		}
		return m, cmd
	}

	var cmd tea.Cmd
	m.form, cmd = m.form.Update(msg)
	m.syncFromForm()
	return m, cmd
}

func (m RecurrenceEditorModel) handleEndsDateKey(msg tea.KeyPressMsg) RecurrenceEditorModel {
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
	case "enter", "space", "esc", "q":
		m.endsDatePicker = false
	}
	m.updatePreview()
	m.syncFromForm()
	return m
}

// HandleEndsDateMouse handles mouse clicks on the ends date picker.
func (m RecurrenceEditorModel) HandleEndsDateMouse(msg tea.MouseClickMsg, pickerBoxW, pickerBoxH int) RecurrenceEditorModel {
	innerW := pickerBoxW - 6
	const gridW = 20
	gridPad := max((innerW-gridW)/2, 0)

	ox := (m.width - pickerBoxW) / 2
	oy := (m.height - pickerBoxH) / 2
	gridX := ox + 3 + gridPad
	gridY := oy + 4

	rx := msg.X - gridX
	ry := msg.Y - gridY
	if rx < 0 || rx >= gridW || ry < 0 || ry >= 6 {
		return m
	}

	dow := min(rx/3, 6)

	y, mo, _ := m.endsDate.Date()
	loc := m.endsDate.Location()
	first := time.Date(y, mo, 1, 0, 0, 0, 0, loc)
	startDow := int(first.Weekday())
	daysInMonth := time.Date(y, mo+1, 0, 0, 0, 0, 0, loc).Day()

	dayNum := ry*7 + dow - startDow + 1
	if dayNum < 1 || dayNum > daysInMonth {
		return m
	}

	m.endsDate = time.Date(y, mo, dayNum, 0, 0, 0, 0, loc)
	m.endsDatePicker = false
	m.updatePreview()
	m.syncFromForm()
	return m
}

// BuildRule generates the RRULE string from the editor state.
func (m RecurrenceEditorModel) BuildRule() string {
	rule := "FREQ=" + m.currentFreq()

	if n := m.intervalValue(); n > 1 {
		rule += ";INTERVAL=" + strconv.Itoa(n)
	}

	switch m.currentFreq() {
	case "WEEKLY":
		var days []string
		for i, on := range m.onField.WeekDays() {
			if on {
				days = append(days, weekDayRRule[i])
			}
		}
		if len(days) > 0 {
			rule += ";BYDAY=" + strings.Join(days, ",")
		}
	case "MONTHLY":
		if m.onField.MonthlyMode() == 1 {
			nth, dayName := nthWeekdayOf(m.startDate)
			rule += ";BYDAY=" + strconv.Itoa(nth) + dayName
		}
	}

	switch m.currentEnds() {
	case endsNever:
		// no COUNT or UNTIL
	case endsAfter:
		if c := strings.TrimSpace(m.endsCountField.Value()); c != "" {
			rule += ";COUNT=" + c
		}
	case endsOnDate:
		rule += ";UNTIL=" + m.endsDate.UTC().Format("20060102T150405Z")
	}

	return rule
}

// RuleSummary returns a short human-readable summary of the rule.
func (m RecurrenceEditorModel) RuleSummary() string {
	freq := m.currentFreq()
	n := m.intervalValue()

	var summary string
	for _, f := range recFrequencies {
		if f.Freq == freq {
			unit := f.Unit[0]
			if n > 1 {
				unit = f.Unit[1]
			}
			if n == 1 {
				summary = f.Label
			} else {
				summary = "Every " + strconv.Itoa(n) + " " + unit
			}
			break
		}
	}

	if freq == "WEEKLY" {
		var days []string
		for i, on := range m.onField.WeekDays() {
			if on {
				days = append(days, weekDayLabels[i])
			}
		}
		if len(days) > 0 && len(days) < 7 {
			summary += " on " + strings.Join(days, ", ")
		}
	}

	if freq == "MONTHLY" && m.onField.MonthlyMode() == 1 {
		summary += " on " + nthWeekdayLabel(m.startDate)
	}

	return summary
}

func (m *RecurrenceEditorModel) updatePreview() {
	rule := m.BuildRule()
	set, err := rrule.StrToRRuleSet("RRULE:" + rule)
	if err != nil {
		m.preview = nil
		return
	}
	set.DTStart(m.startDate)
	to := m.startDate.AddDate(2, 0, 0)
	instances := set.Between(m.startDate, to, true)
	if len(instances) > 5 {
		instances = instances[:5]
	}
	m.preview = instances
}

// BoxSize returns the outer dimensions of the editor dialog.
func (m RecurrenceEditorModel) BoxSize() (int, int) {
	if m.width <= 0 || m.height <= 0 {
		return 0, 0
	}
	return lipgloss.Size(m.View())
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m RecurrenceEditorModel) EndsDatePickerView() string {
	const boxW = 40
	const boxH = 14
	bold := lipgloss.NewStyle().Bold(true)

	monthStr := m.endsDate.Format("January 2006")
	calGrid := renderMiniCalendar(m.endsDate, time.Now(), 0, m.theme)
	gridLines := strings.Split(calGrid, "\n")
	calLines := make([]string, 0, len(gridLines)+1)
	calLines = append(calLines, bold.Render(monthStr))
	calLines = append(calLines, gridLines...)

	maxCalW := 0
	for _, line := range calLines {
		if w := lipgloss.Width(line); w > maxCalW {
			maxCalW = w
		}
	}

	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	hintLines := []string{
		"←↓↑→" + " " + descStyle.Render("navigate"),
		"[]" + "   " + descStyle.Render("month"),
		"t" + "    " + descStyle.Render("today"),
	}
	hintStart := len(calLines) - len(hintLines)

	resultLines := make([]string, 0, len(calLines)+3)
	for i, line := range calLines {
		w := lipgloss.Width(line)
		padded := line + strings.Repeat(" ", max(maxCalW-w, 0))
		if i >= hintStart && i-hintStart < len(hintLines) {
			padded += "  " + hintLines[i-hintStart]
		}
		resultLines = append(resultLines, padded)
	}

	innerW := boxW - 4
	resultLines = append(resultLines, "")
	sepStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	resultLines = append(resultLines, sepStyle.Render(strings.Repeat("─", innerW)))
	bs := DefaultButtonStyles()
	cancelBtn := bs.Secondary.Render("Cancel", false)
	okBtn := bs.Primary.Render("Ok", false)
	buttonRow := cancelBtn + " " + okBtn
	btnPad := max(innerW-lipgloss.Width(buttonRow), 0)
	resultLines = append(resultLines, strings.Repeat(" ", btnPad)+buttonRow)

	return lipgloss.NewStyle().
		Width(boxW).
		Height(boxH).
		Padding(1, 1, 0, 1).
		Border(lipgloss.RoundedBorder()).
		Render(strings.Join(resultLines, "\n"))
}

// EndsDatePickerBoxSize returns the outer dimensions of the ends date picker.
func (m RecurrenceEditorModel) EndsDatePickerBoxSize() (int, int) {
	return 40, 14
}

// View renders the recurrence editor dialog.
func (m RecurrenceEditorModel) View() string {
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}

	styles := DefaultDialogStyles()
	dialog := NewDialog("Custom Repeat", styles)
	dialog = dialog.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	dialog.SetWidth(recurrenceEditorBoxWidth)
	dialog.SetFooter(m.help.ShortHelpView(helpKeys))

	form := m.form
	form.SetWidth(dialog.ContentWidth())
	return mouseSweep(dialog.Box(form.View()))
}

// nthWeekdayOf returns the Nth occurrence number and RFC 5545 day code
// for the given date's position within its month (e.g. 3rd Monday -> 3, "MO").
func nthWeekdayOf(t time.Time) (int, string) {
	nth := (t.Day()-1)/7 + 1
	return nth, weekDayRRule[int(t.Weekday())]
}

func nthWeekdayLabel(t time.Time) string {
	nth, _ := nthWeekdayOf(t)
	ordinals := []string{"", "1st", "2nd", "3rd", "4th", "5th"}
	ord := "nth"
	if nth > 0 && nth < len(ordinals) {
		ord = ordinals[nth]
	}
	return ord + " " + t.Weekday().String()
}
