package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/teambition/rrule-go"
)

type recEditorField int

const (
	refFreq recEditorField = iota
	refInterval
	refWeekDays
	refMonthlyOn
	refEnds
	refEndsCount
	refDone
	refCancel
)

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
	freqIdx       int // index into recFrequencies
	interval      textinput.Model
	weekDays      [7]bool // Sun=0 .. Sat=6
	weekDayCursor int
	monthlyMode   int // 0=day of month, 1=Nth weekday

	ends           endsMode
	endsCount      textinput.Model
	endsDate       time.Time
	endsDatePicker bool

	focusField recEditorField
	done       bool
	cancelled  bool

	startDate time.Time
	preview   []time.Time

	help   help.Model
	width  int
	height int
	theme  Theme
}

// NewRecurrenceEditorModel creates a new editor pre-configured from the event date.
func NewRecurrenceEditorModel(startDate time.Time, w, h int, theme Theme) RecurrenceEditorModel {
	intervalInput := textinput.New()
	intervalInput.Placeholder = "1"
	intervalInput.CharLimit = 3
	intervalInput.SetValue("1")

	endsCountInput := textinput.New()
	endsCountInput.Placeholder = "10"
	endsCountInput.CharLimit = 4

	var weekDays [7]bool
	weekDays[int(startDate.Weekday())] = true

	m := RecurrenceEditorModel{
		freqIdx:       1, // Weekly
		interval:      intervalInput,
		weekDays:      weekDays,
		weekDayCursor: int(startDate.Weekday()),
		ends:          endsNever,
		endsCount:     endsCountInput,
		endsDate:      startDate.AddDate(0, 3, 0),
		focusField:    refFreq,
		startDate:     startDate,
		help:          newThemedHelp(theme),
		width:         w,
		height:        h,
		theme:         theme,
	}
	m.updatePreview()
	return m
}

func (m RecurrenceEditorModel) SetSize(w, h int) RecurrenceEditorModel {
	m.width = w
	m.height = h
	return m
}

func (m RecurrenceEditorModel) Done() bool      { return m.done }
func (m RecurrenceEditorModel) Cancelled() bool  { return m.cancelled }
func (m RecurrenceEditorModel) EndsDatePickerOpen() bool { return m.endsDatePicker }

func (m RecurrenceEditorModel) focusableFields() []recEditorField {
	fields := []recEditorField{refFreq, refInterval}
	switch m.freqIdx {
	case 1: // Weekly
		fields = append(fields, refWeekDays)
	case 2: // Monthly
		fields = append(fields, refMonthlyOn)
	}
	fields = append(fields, refEnds)
	if m.ends == endsAfter {
		fields = append(fields, refEndsCount)
	}
	fields = append(fields, refDone, refCancel)
	return fields
}

func (m RecurrenceEditorModel) nextField() recEditorField {
	fields := m.focusableFields()
	for i, f := range fields {
		if f == m.focusField {
			return fields[(i+1)%len(fields)]
		}
	}
	return fields[0]
}

func (m RecurrenceEditorModel) prevField() recEditorField {
	fields := m.focusableFields()
	for i, f := range fields {
		if f == m.focusField {
			return fields[(i-1+len(fields))%len(fields)]
		}
	}
	return fields[0]
}

func (m RecurrenceEditorModel) withFocus(f recEditorField) RecurrenceEditorModel {
	m.focusField = f
	m.interval.Blur()
	m.endsCount.Blur()
	switch f {
	case refInterval:
		m.interval.Focus()
	case refEndsCount:
		m.endsCount.Focus()
	default:
		// non-text fields: no input focus
	}
	return m
}

// Update handles all messages for the recurrence editor.
func (m RecurrenceEditorModel) Update(msg tea.Msg) (RecurrenceEditorModel, tea.Cmd) {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if m.endsDatePicker {
			return m.handleEndsDateKey(msg), nil
		}
		return m.handleKey(msg)
	}
	// Forward cursor blink etc. to active textinput.
	var cmd tea.Cmd
	switch m.focusField {
	case refInterval:
		m.interval, cmd = m.interval.Update(msg)
	case refEndsCount:
		m.endsCount, cmd = m.endsCount.Update(msg)
	default:
		// non-text fields: nothing to forward
	}
	return m, cmd
}

func (m RecurrenceEditorModel) handleKey(msg tea.KeyPressMsg) (RecurrenceEditorModel, tea.Cmd) {
	keys := struct {
		Tab, ShiftTab, Enter, Save, Close key.Binding
	}{
		Tab:      key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		ShiftTab: key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("shift+tab", "prev field")),
		Enter:    key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		Save:     key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("ctrl+s", "save")),
		Close:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}

	switch {
	case key.Matches(msg, keys.Save):
		m.done = true
		return m, nil
	case key.Matches(msg, keys.Close):
		m.cancelled = true
		return m, nil
	case key.Matches(msg, keys.ShiftTab):
		m = m.withFocus(m.prevField())
		return m, nil
	case key.Matches(msg, keys.Tab):
		m = m.withFocus(m.nextField())
		return m, nil
	case key.Matches(msg, keys.Enter):
		switch m.focusField {
		case refDone:
			m.done = true
			return m, nil
		case refCancel:
			m.cancelled = true
			return m, nil
		case refEnds:
			if m.ends == endsOnDate {
				m.endsDatePicker = true
				return m, nil
			}
		default:
			if m.focusField == refInterval || m.focusField == refEndsCount {
				m = m.withFocus(m.nextField())
				m.updatePreview()
				return m, nil
			}
		}
	}

	switch m.focusField {
	case refDone, refCancel:
		return m, nil
	case refFreq:
		n := len(recFrequencies)
		switch msg.String() {
		case "left", "h":
			m.freqIdx = (m.freqIdx - 1 + n) % n
			m.updatePreview()
		case "right", "l":
			m.freqIdx = (m.freqIdx + 1) % n
			m.updatePreview()
		}
		return m, nil

	case refInterval:
		var cmd tea.Cmd
		m.interval, cmd = m.interval.Update(msg)
		m.updatePreview()
		return m, cmd

	case refWeekDays:
		switch msg.String() {
		case "left", "h":
			m.weekDayCursor = (m.weekDayCursor - 1 + 7) % 7
		case "right", "l":
			m.weekDayCursor = (m.weekDayCursor + 1) % 7
		case "space":
			m.weekDays[m.weekDayCursor] = !m.weekDays[m.weekDayCursor]
			m.updatePreview()
		}
		return m, nil

	case refMonthlyOn:
		switch msg.String() {
		case "left", "h", "right", "l":
			m.monthlyMode = 1 - m.monthlyMode // toggle 0 ↔ 1
			m.updatePreview()
		}
		return m, nil

	case refEnds:
		switch msg.String() {
		case "left", "h":
			m.ends = endsMode((int(m.ends) - 1 + 3) % 3)
			m.updatePreview()
		case "right", "l":
			m.ends = endsMode((int(m.ends) + 1) % 3)
			m.updatePreview()
		case "space":
			if m.ends == endsOnDate {
				m.endsDatePicker = true
			}
		}
		return m, nil

	case refEndsCount:
		var cmd tea.Cmd
		m.endsCount, cmd = m.endsCount.Update(msg)
		m.updatePreview()
		return m, cmd
	}

	return m, nil
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
		m.updatePreview()
	}
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
	return m
}

// BuildRule generates the RRULE string from the editor state.
func (m RecurrenceEditorModel) BuildRule() string {
	freq := recFrequencies[m.freqIdx].Freq
	rule := "FREQ=" + freq

	if n, err := strconv.Atoi(strings.TrimSpace(m.interval.Value())); err == nil && n > 1 {
		rule += ";INTERVAL=" + strconv.Itoa(n)
	}

	switch freq {
	case "WEEKLY":
		var days []string
		for i, on := range m.weekDays {
			if on {
				days = append(days, weekDayRRule[i])
			}
		}
		if len(days) > 0 {
			rule += ";BYDAY=" + strings.Join(days, ",")
		}
	case "MONTHLY":
		if m.monthlyMode == 1 {
			nth, dayName := nthWeekdayOf(m.startDate)
			rule += fmt.Sprintf(";BYDAY=%d%s", nth, dayName)
		}
	}

	switch m.ends {
	case endsAfter:
		if c := strings.TrimSpace(m.endsCount.Value()); c != "" {
			rule += ";COUNT=" + c
		}
	case endsOnDate:
		rule += ";UNTIL=" + m.endsDate.UTC().Format("20060102T150405Z")
	default:
		// endsNever: no COUNT/UNTIL appended
	}

	return rule
}

// RuleSummary returns a short human-readable summary of the rule.
func (m RecurrenceEditorModel) RuleSummary() string {
	f := recFrequencies[m.freqIdx]
	n, _ := strconv.Atoi(strings.TrimSpace(m.interval.Value()))
	if n <= 0 {
		n = 1
	}
	unit := f.Unit[0]
	if n > 1 {
		unit = f.Unit[1]
	}
	s := fmt.Sprintf("Every %d %s", n, unit)
	if n == 1 {
		s = f.Label
	}

	if f.Freq == "WEEKLY" {
		var days []string
		for i, on := range m.weekDays {
			if on {
				days = append(days, weekDayLabels[i])
			}
		}
		if len(days) > 0 && len(days) < 7 {
			s += " on " + strings.Join(days, ", ")
		}
	}
	if f.Freq == "MONTHLY" && m.monthlyMode == 1 {
		s += " on " + nthWeekdayLabel(m.startDate)
	}
	return s
}

func (m *RecurrenceEditorModel) updatePreview() {
	rule := m.BuildRule()
	rruleStr := "RRULE:" + rule
	set, err := rrule.StrToRRuleSet(rruleStr)
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
	return 52, 24
}

// EndsDatePickerView renders the ends-date picker overlay.
func (m RecurrenceEditorModel) EndsDatePickerView() string {
	const boxW = 50
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

	return lipgloss.NewStyle().
		Width(boxW).
		Height(13).
		Padding(1, 2).
		Border(lipgloss.RoundedBorder()).
		Render(strings.Join(lines, "\n"))
}

// EndsDatePickerBoxSize returns the outer dimensions of the ends date picker.
func (m RecurrenceEditorModel) EndsDatePickerBoxSize() (int, int) {
	return 50, 13
}

// View renders the recurrence editor dialog.
func (m RecurrenceEditorModel) View() string {
	boxW, _ := m.BoxSize()
	innerW := boxW - 2 - 2 // border + padding

	faint := lipgloss.NewStyle().Faint(true)

	var lines []string

	// Frequency
	freqLabel := recFrequencies[m.freqIdx].Label
	if m.focusField == refFreq {
		freqLabel = lipgloss.NewStyle().Reverse(true).Render(freqLabel) + faint.Render("  \u25c0 \u25b6")
	}
	lines = append(lines, faint.Render(formLabel("Frequency"))+freqLabel)

	// Interval
	n, _ := strconv.Atoi(strings.TrimSpace(m.interval.Value()))
	if n <= 0 {
		n = 1
	}
	unit := recFrequencies[m.freqIdx].Unit[0]
	if n != 1 {
		unit = recFrequencies[m.freqIdx].Unit[1]
	}
	lines = append(lines, faint.Render(formLabel("Every"))+m.interval.View()+" "+faint.Render(unit))
	lines = append(lines, "")

	// Weekly: day toggles
	if m.freqIdx == 1 {
		dayParts := make([]string, 0, 7)
		for i := range 7 {
			label := weekDayLabels[i]
			style := lipgloss.NewStyle().Faint(true)
			if m.weekDays[i] {
				style = lipgloss.NewStyle().Bold(true)
			}
			if m.focusField == refWeekDays && i == m.weekDayCursor {
				style = style.Reverse(true)
			}
			dayParts = append(dayParts, style.Render(label))
		}
		lines = append(lines, faint.Render(formLabel("On"))+strings.Join(dayParts, " "))
		if m.focusField == refWeekDays {
			lines = append(lines, faint.Render(formLabel("")+"\u2190/\u2192: move  \u00b7  space: toggle"))
		}
		lines = append(lines, "")
	}

	// Monthly: on selector
	if m.freqIdx == 2 {
		var onLabel string
		if m.monthlyMode == 0 {
			onLabel = fmt.Sprintf("day %d", m.startDate.Day())
		} else {
			onLabel = nthWeekdayLabel(m.startDate)
		}
		if m.focusField == refMonthlyOn {
			onLabel = lipgloss.NewStyle().Reverse(true).Render(onLabel) + faint.Render("  \u25c0 \u25b6")
		}
		lines = append(lines, faint.Render(formLabel("On"))+onLabel)
		lines = append(lines, "")
	}

	// Ends
	var endsLabel string
	switch m.ends {
	case endsNever:
		endsLabel = "Never"
	case endsAfter:
		endsLabel = "After " + m.endsCount.View() + " times"
	case endsOnDate:
		endsLabel = "On " + m.endsDate.Format("Jan 2, 2006")
	}
	if m.focusField == refEnds {
		if m.ends != endsAfter {
			endsLabel = lipgloss.NewStyle().Reverse(true).Render(endsLabel)
		}
		endsLabel += faint.Render("  \u25c0 \u25b6")
	}
	lines = append(lines, faint.Render(formLabel("Ends"))+endsLabel)
	lines = append(lines, "")

	// Preview
	lines = append(lines, faint.Render("Preview"))
	if len(m.preview) == 0 {
		lines = append(lines, faint.Render("  (no occurrences)"))
	}
	for _, t := range m.preview {
		lines = append(lines, "  "+t.Format("Mon, Jan 2, 2006"))
	}
	lines = append(lines, "")

	// Buttons
	bs := DefaultButtonStyles()
	doneBtn := bs.Primary.Render("Done", m.focusField == refDone)
	cancelBtn := bs.Secondary.Render("Cancel", m.focusField == refCancel)
	buttons := cancelBtn + " " + doneBtn
	pad := max(innerW-lipgloss.Width(buttons), 0)
	lines = append(lines, strings.Repeat(" ", pad)+buttons)

	m.help.SetWidth(innerW)
	helpKeys := []key.Binding{
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "next field")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "confirm")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
	helpText := m.help.ShortHelpView(helpKeys)

	content := strings.Join(lines, "\n")

	styles := DefaultDialogStyles()
	dialog := NewDialog("Custom Repeat", styles)
	dialog = dialog.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
	dialog.SetFooter(helpText)

	return dialog.Box(content)
}

// nthWeekdayOf returns the Nth occurrence number and RFC 5545 day code
// for the given date's position within its month (e.g. 3rd Monday → 3, "MO").
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
