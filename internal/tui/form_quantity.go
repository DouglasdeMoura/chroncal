package tui

import (
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// QuantitySelectField
// ---------------------------------------------------------------------------

// QuantitySelectField is a composite FormField that renders a positive integer
// input followed by a select on the same row, e.g. "1 Week". It implements
// subFocuser so Tab/Enter cycle between the amount and unit before they leave.
type QuantitySelectField struct {
	amount   *TextField
	unit     *SelectField
	suffix   string
	subFocus int // 0 = amount, 1 = unit
	focused  bool
	width    int
}

func NewQuantitySelectField(options []SelectOption, defaultSelected int) *QuantitySelectField {
	amount := NewTextField("1")
	amount.SetValue("1")
	amount.SetCharLimit(3)
	amount.SetDigitsOnly()

	unit := NewSelectField(options)
	unit.SetSelected(defaultSelected)

	return &QuantitySelectField{
		amount: amount,
		unit:   unit,
		width:  4,
	}
}

func (f *QuantitySelectField) Amount() string     { return f.amount.Value() }
func (f *QuantitySelectField) SetAmount(v string) { f.amount.SetValue(v) }
func (f *QuantitySelectField) Selected() int      { return f.unit.Selected() }
func (f *QuantitySelectField) SetSelected(i int)  { f.unit.SetSelected(i) }
func (f *QuantitySelectField) Value() string      { return f.unit.Value() }
func (f *QuantitySelectField) SetSuffix(s string) { f.suffix = s }

func (f *QuantitySelectField) Update(msg tea.Msg) tea.Cmd {
	if f.subFocus == 0 {
		return f.amount.Update(msg)
	}
	return f.unit.Update(msg)
}

func (f *QuantitySelectField) View() string {
	amountView := f.amountText()
	unitView := f.unitText()
	out := amountView + " " + unitView
	if f.suffix != "" {
		out += " " + f.suffix
	}
	return out
}

func (f *QuantitySelectField) amountText() string {
	style := lipgloss.NewStyle().Width(f.width)
	if f.focused && f.subFocus == 0 {
		return mouseMark("quantityselect:amount", style.Render(f.amount.View()))
	}
	v := f.amount.Value()
	if strings.TrimSpace(v) == "" {
		v = f.amount.input.Placeholder
	}
	return mouseMark("quantityselect:amount", style.Render(v))
}

// amountIsOne reports whether the entered amount is exactly 1, which
// governs singular/plural agreement of the unit label. An empty input
// falls back to the placeholder ("1"), which matches what amountText shows.
func (f *QuantitySelectField) amountIsOne() bool {
	v := strings.TrimSpace(f.amount.Value())
	if v == "" {
		v = f.amount.input.Placeholder
	}
	n, err := strconv.Atoi(v)
	return err == nil && n == 1
}

func (f *QuantitySelectField) unitText() string {
	if len(f.unit.options) == 0 {
		return ""
	}
	unitFocused := f.focused && f.subFocus == 1
	labelStyle := lipgloss.NewStyle().Width(f.unit.maxWidth)
	if unitFocused && f.unit.renderLabel == nil {
		labelStyle = labelStyle.Reverse(true)
	}
	opt := f.unit.options[f.unit.selected]
	if opt.PluralLabel != "" && !f.amountIsOne() {
		opt.Label = opt.PluralLabel
	}
	label := labelStyle.Render(f.unit.renderOptionLabel(opt, unitFocused))

	flash := lipgloss.NewStyle().Foreground(activeTheme.FormHighlight)
	prev := Glyphs["select.prev"]
	next := Glyphs["select.next"]
	if f.unit.highlight == selectLeft {
		prev = flash.Render(prev)
	}
	if f.unit.highlight == selectRight {
		next = flash.Render(next)
	}

	return mouseMark("quantityselect:unit", label) +
		"  " +
		mouseMark("quantityselect:prev", prev) +
		" " +
		mouseMark("quantityselect:next", next)
}

func (f *QuantitySelectField) Focus() tea.Cmd {
	f.focused = true
	f.subFocus = 0
	f.unit.Blur()
	return f.amount.Focus()
}

func (f *QuantitySelectField) Blur() {
	f.focused = false
	f.amount.Blur()
	f.unit.Blur()
}

func (f *QuantitySelectField) SetWidth(int) {
	f.width = 4
	f.amount.SetWidth(f.width)
}

func (f *QuantitySelectField) IsFocusable() bool { return true }

func (f *QuantitySelectField) SubFocusNext() (bool, tea.Cmd) {
	if f.subFocus == 0 {
		f.amount.Blur()
		f.subFocus = 1
		return true, f.unit.Focus()
	}
	return false, nil
}

func (f *QuantitySelectField) SubFocusPrev() (bool, tea.Cmd) {
	if f.subFocus == 1 {
		f.unit.Blur()
		f.subFocus = 0
		return true, f.amount.Focus()
	}
	return false, nil
}

func (f *QuantitySelectField) HandleClickTarget(target string) tea.Cmd {
	switch target {
	case "quantityselect:amount":
		f.unit.Blur()
		f.subFocus = 0
		return f.amount.Focus()
	case "quantityselect:unit":
		f.amount.Blur()
		f.subFocus = 1
		return f.unit.Focus()
	case "quantityselect:prev":
		f.amount.Blur()
		f.subFocus = 1
		_ = f.unit.Focus()
		return f.unit.Update(keyMsg("left"))
	case "quantityselect:next":
		f.amount.Blur()
		f.subFocus = 1
		_ = f.unit.Focus()
		return f.unit.Update(keyMsg("right"))
	default:
		return nil
	}
}

func (f *QuantitySelectField) Validate() string {
	return validatePositiveInt(strings.TrimSpace(f.amount.Value()))
}

// validatePositiveInt reports an error message when raw is not a whole
// number greater than zero, or "" when it is. Shared by the quantity
// field and the recurrence "Ends: After N times" count fields. An
// empty or zero count can then never persist an unbounded or invalid RRULE.
func validatePositiveInt(raw string) string {
	n, err := strconv.Atoi(raw)
	if err != nil {
		return "Value must be a whole number"
	}
	if n <= 0 {
		return "Value must be greater than 0"
	}
	return ""
}
