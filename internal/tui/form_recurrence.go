package tui

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// RecurrenceOnField
// ---------------------------------------------------------------------------

type RecurrenceOnMode int

const (
	RecurrenceOnWeekly RecurrenceOnMode = iota
	RecurrenceOnMonthly
)

type RecurrenceOnField struct {
	mode          RecurrenceOnMode
	startDate     time.Time
	weekDays      [7]bool
	weekDayCursor int
	monthly       *SelectField
	focused       bool
	width         int
}

func NewRecurrenceOnField(startDate time.Time) *RecurrenceOnField {
	f := &RecurrenceOnField{
		mode:          RecurrenceOnWeekly,
		startDate:     startDate,
		weekDayCursor: int(startDate.Weekday()),
		monthly:       NewSelectField(nil),
	}
	f.weekDays[f.weekDayCursor] = true
	f.syncMonthlyOptions()
	return f
}

func (f *RecurrenceOnField) SetWeekly(weekDays [7]bool, cursor int) {
	f.mode = RecurrenceOnWeekly
	f.weekDays = weekDays
	if cursor >= 0 && cursor < len(weekDayLabels) {
		f.weekDayCursor = cursor
	}
}

func (f *RecurrenceOnField) SetMonthly(startDate time.Time, monthlyMode int) {
	f.mode = RecurrenceOnMonthly
	f.startDate = startDate
	f.syncMonthlyOptions()
	f.monthly.SetSelected(monthlyMode)
}

func (f *RecurrenceOnField) Mode() RecurrenceOnMode { return f.mode }
func (f *RecurrenceOnField) WeekDays() [7]bool      { return f.weekDays }
func (f *RecurrenceOnField) WeekDayCursor() int     { return f.weekDayCursor }
func (f *RecurrenceOnField) MonthlyMode() int       { return f.monthly.Selected() }

func (f *RecurrenceOnField) ToggleWeekDay(idx int) {
	if idx < 0 || idx >= len(f.weekDays) {
		return
	}
	f.weekDayCursor = idx
	f.weekDays[idx] = !f.weekDays[idx]
}

func (f *RecurrenceOnField) syncMonthlyOptions() {
	f.monthly.SetOptions([]SelectOption{
		{Label: fmt.Sprintf("day %d", f.startDate.Day()), Value: "day"},
		{Label: nthWeekdayLabel(f.startDate), Value: "nth"},
	})
}

func (f *RecurrenceOnField) Update(msg tea.Msg) tea.Cmd {
	if f.mode == RecurrenceOnMonthly {
		return f.monthly.Update(msg)
	}
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		switch msg.String() {
		case "left", "h":
			f.weekDayCursor = (f.weekDayCursor - 1 + 7) % 7
		case "right", "l":
			f.weekDayCursor = (f.weekDayCursor + 1) % 7
		case "space":
			f.ToggleWeekDay(f.weekDayCursor)
		}
	}
	return nil
}

func (f *RecurrenceOnField) View() string {
	if f.mode == RecurrenceOnMonthly {
		return f.monthly.View()
	}
	dayParts := make([]string, 0, 7)
	plainParts := make([]string, 0, 7)
	for i := range 7 {
		label := weekDayLabels[i]
		style := lipgloss.NewStyle()
		cursorHere := f.focused && i == f.weekDayCursor
		if f.weekDays[i] {
			style = style.Reverse(true)
		} else if !cursorHere {
			style = style.Faint(true)
		}
		if cursorHere {
			style = style.Bold(true)
		}
		rendered := style.Render(label)
		plainParts = append(plainParts, rendered)
		dayParts = append(dayParts, mouseMark("recurrenceon:"+strconv.Itoa(i), rendered))
	}
	row := strings.Join(dayParts, " ")
	plainRow := strings.Join(plainParts, " ")
	if !f.focused {
		return row
	}

	hint := lipgloss.NewStyle().Faint(true).Render("space toggle")
	if rowWidth := lipgloss.Width(plainRow); f.width > 0 {
		hintWidth := lipgloss.Width(hint)
		if rowWidth+1+hintWidth > f.width {
			hint = lipgloss.NewStyle().Faint(true).Render("space")
		}
	}
	if f.width <= 0 {
		return row + " " + hint
	}

	rowWidth := lipgloss.Width(plainRow)
	hintWidth := lipgloss.Width(hint)
	if rowWidth+1+hintWidth > f.width {
		return row
	}
	return row + strings.Repeat(" ", f.width-rowWidth-hintWidth) + hint
}

func (f *RecurrenceOnField) Focus() tea.Cmd {
	f.focused = true
	if f.mode == RecurrenceOnMonthly {
		return f.monthly.Focus()
	}
	return nil
}

func (f *RecurrenceOnField) Blur() {
	f.focused = false
	f.monthly.Blur()
}

func (f *RecurrenceOnField) SetWidth(w int)    { f.width = w }
func (f *RecurrenceOnField) IsFocusable() bool { return true }
