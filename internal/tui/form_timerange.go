package tui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// TimeRangeField
// ---------------------------------------------------------------------------

// TimeRangeField is a composite FormField that displays two time inputs
// (start → end) on a single row with an auto-calculated duration label.
// It implements subFocuser so Tab/Enter cycle between start and end before
// the Form advances to the next field.
type TimeRangeField struct {
	start    *TextField
	end      *TextField
	subFocus int // 0 = start, 1 = end
	focused  bool
	disabled bool
	dimColor color.Color
}

func NewTimeRangeField(dimColor color.Color) *TimeRangeField {
	start := NewTextField("HH:MM")
	start.SetCharLimit(5)
	start.SetFilter(FilterDigits)

	end := NewTextField("HH:MM")
	end.SetCharLimit(5)
	end.SetFilter(FilterDigits)

	return &TimeRangeField{
		start:    start,
		end:      end,
		dimColor: dimColor,
	}
}

func (f *TimeRangeField) StartValue() string     { return f.start.Value() }
func (f *TimeRangeField) EndValue() string       { return f.end.Value() }
func (f *TimeRangeField) SetStartValue(v string) { f.start.SetValue(v) }
func (f *TimeRangeField) SetEndValue(v string)   { f.end.SetValue(v) }
func (f *TimeRangeField) SetDisabled(v bool)     { f.disabled = v }

// Value returns the start value. It satisfies the valuer interface for
// Required field checks.
func (f *TimeRangeField) Value() string { return f.start.Value() }

func (f *TimeRangeField) Update(msg tea.Msg) tea.Cmd {
	if f.disabled {
		return nil
	}
	active := f.start
	if f.subFocus != 0 {
		active = f.end
	}
	prev := active.Value()
	cmd := active.Update(msg)
	if active.Value() != prev {
		f.autoFormatTime(active)
	}
	return cmd
}

func (f *TimeRangeField) timeText(tf *TextField, dim bool) string {
	if dim {
		v := tf.Value()
		if v == "" {
			v = tf.input.Placeholder
		}
		return lipgloss.NewStyle().Foreground(f.dimColor).Render(v)
	}
	return tf.View()
}

func (f *TimeRangeField) View() string {
	// Use the live textinput View only for the actively focused sub-field
	// so the cursor is visible. All other sub-fields render as plain text
	// to avoid the extra padding/cursor space that textinput always adds.
	startDim := f.disabled // || !f.focused || f.subFocus != 0
	endDim := f.disabled   // || !f.focused || f.subFocus != 1

	startView := f.timeText(f.start, startDim)
	endView := f.timeText(f.end, endDim)
	arrow := lipgloss.NewStyle().Foreground(f.dimColor).Render(Glyphs["time.arrow"])

	result := startView + "  " + arrow + "  " + endView

	dur := f.formatDuration()
	if dur != "" {
		durStyle := lipgloss.NewStyle().Foreground(f.dimColor).Italic(true)
		result += "  " + durStyle.Render(dur)
	}

	return result
}

func (f *TimeRangeField) Focus() tea.Cmd {
	if f.disabled {
		return nil
	}
	f.focused = true
	f.subFocus = 0
	f.end.Blur()
	return f.start.Focus()
}

func (f *TimeRangeField) Blur() {
	f.focused = false
	f.start.Blur()
	f.end.Blur()
}

func (f *TimeRangeField) SetWidth(int) {
	f.start.SetWidth(6) // HH:MM + cursor
	f.end.SetWidth(6)
}

func (f *TimeRangeField) IsFocusable() bool { return !f.disabled }

// subFocuser implementation

func (f *TimeRangeField) SubFocusNext() (bool, tea.Cmd) {
	if f.subFocus == 0 {
		f.autoAdjustEnd()
		f.start.Blur()
		f.subFocus = 1
		return true, f.end.Focus()
	}
	return false, nil
}

func (f *TimeRangeField) SubFocusPrev() (bool, tea.Cmd) {
	if f.subFocus == 1 {
		f.end.Blur()
		f.subFocus = 0
		return true, f.start.Focus()
	}
	return false, nil
}

// Validate checks both times are valid HH:MM format.
func (f *TimeRangeField) Validate() string {
	sv := strings.TrimSpace(f.start.Value())
	ev := strings.TrimSpace(f.end.Value())
	if sv == "" && ev == "" {
		return "" // emptiness handled by Required
	}
	if sv != "" {
		if _, err := time.Parse("15:04", sv); err != nil {
			return "Invalid start time (use HH:MM)"
		}
	}
	if ev != "" {
		if _, err := time.Parse("15:04", ev); err != nil {
			return "Invalid end time (use HH:MM)"
		}
	}
	if sv != "" && ev == "" {
		return "End time is required"
	}
	if sv == "" && ev != "" {
		return "Start time is required"
	}
	return ""
}

func (f *TimeRangeField) formatDuration() string {
	sv := strings.TrimSpace(f.start.Value())
	ev := strings.TrimSpace(f.end.Value())
	st, err1 := time.Parse("15:04", sv)
	et, err2 := time.Parse("15:04", ev)
	if err1 != nil || err2 != nil {
		return ""
	}
	d := et.Sub(st)
	if d < 0 {
		d += 24 * time.Hour
	}
	if d == 0 {
		return ""
	}
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	switch {
	case h > 0 && m > 0:
		return strconv.Itoa(h) + "h" + strconv.Itoa(m) + "min"
	case h > 0:
		return strconv.Itoa(h) + "h"
	default:
		return strconv.Itoa(m) + "min"
	}
}

// autoFormatTime inserts a colon after the 2nd digit so the user only needs
// to type digits (e.g. "1030" → "10:30").
func (f *TimeRangeField) autoFormatTime(field *TextField) {
	val := field.Value()
	digits := strings.ReplaceAll(val, ":", "")
	if len(digits) > 4 {
		digits = digits[:4]
	}

	var formatted string
	if len(digits) > 2 {
		formatted = digits[:2] + ":" + digits[2:]
	} else {
		formatted = digits
	}

	if formatted == val {
		return
	}

	pos := field.Position()
	safePos := min(pos, len(val))
	colonsBefore := strings.Count(val[:safePos], ":")
	digitPos := pos - colonsBefore

	newPos := digitPos
	if digitPos > 2 && len(digits) > 2 {
		newPos = digitPos + 1
	}

	field.SetValue(formatted)
	field.SetCursor(min(newPos, len(formatted)))
}

// autoAdjustEnd sets end = start + 1h when end is not after start.
func (f *TimeRangeField) autoAdjustEnd() {
	st, err1 := time.Parse("15:04", f.start.Value())
	et, err2 := time.Parse("15:04", f.end.Value())
	if err1 != nil || err2 != nil {
		return
	}
	if !et.After(st) {
		f.end.SetValue(st.Add(time.Hour).Format("15:04"))
	}
}
