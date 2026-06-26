package tui

import (
	"fmt"
	"image/color"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"

	"github.com/douglasdemoura/chroncal/internal/tui/oklch"
)

// ---------------------------------------------------------------------------
// FormField interface
// ---------------------------------------------------------------------------

// FormField is the interface that all form field types must implement.
// It provides the minimal surface needed for a Form to manage focus cycling,
// rendering, and message dispatch across heterogeneous field types.
type FormField interface {
	Update(tea.Msg) tea.Cmd
	View() string
	Focus() tea.Cmd
	Blur()
	SetWidth(int)
	IsFocusable() bool
}

// ---------------------------------------------------------------------------
// TextField
// ---------------------------------------------------------------------------

// TextField wraps a bubbles textinput with optional keystroke filtering.
type TextField struct {
	input    textinput.Model
	filter   func(tea.Key) bool
	suffix   string
	disabled bool
	dimStyle lipgloss.Style
	validate func(string) string
}

func NewTextField(placeholder string) *TextField {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 256
	applyPlaceholderDefaults(&input)
	return &TextField{input: input}
}

// applyPlaceholderDefaults styles the placeholder in both focus states
// so hints read as hints — italicized and faint, distinct from entered
// values which use the upright text style. Drops the bubbles default
// colour so the terminal's own faint attribute drives the dimness.
func applyPlaceholderDefaults(input *textinput.Model) {
	hint := lipgloss.NewStyle().Italic(true).Faint(true)
	s := input.Styles()
	s.Focused.Placeholder = hint
	s.Blurred.Placeholder = hint
	input.SetStyles(s)
}

func (f *TextField) Value() string     { return f.input.Value() }
func (f *TextField) SetValue(v string) { f.input.SetValue(v) }

func (f *TextField) SetPlaceholder(p string) { f.input.Placeholder = p }
func (f *TextField) SetCharLimit(n int)      { f.input.CharLimit = n }
func (f *TextField) Position() int           { return f.input.Position() }
func (f *TextField) SetCursor(pos int)       { f.input.SetCursor(pos) }
func (f *TextField) SetSuffix(s string)      { f.suffix = s }

// SetValidate installs a validation hook run by Form.validate before
// submit. The hook receives the trimmed field value and returns an error
// message, or "" when the value is acceptable. When no hook is set the
// field always validates.
func (f *TextField) SetValidate(fn func(string) string) { f.validate = fn }

// Validate implements the validator interface. It delegates to the hook
// installed via SetValidate, returning "" (valid) when none is set.
func (f *TextField) Validate() string {
	if f.validate == nil {
		return ""
	}
	return f.validate(strings.TrimSpace(f.input.Value()))
}

// SetFilter sets a function that gates printable keystrokes. When set, a key
// with non-empty Text is forwarded to the underlying input only if fn returns
// true. Special keys (tab, enter, backspace, …) are never filtered.
func (f *TextField) SetFilter(fn func(tea.Key) bool) {
	f.filter = fn
}

// SetDigitsOnly is shorthand for SetFilter(FilterDigits).
func (f *TextField) SetDigitsOnly() {
	f.filter = FilterDigits
}

// SetEchoPassword toggles password masking for the underlying input.
func (f *TextField) SetEchoPassword(on bool) {
	if on {
		f.input.EchoMode = textinput.EchoPassword
	} else {
		f.input.EchoMode = textinput.EchoNormal
	}
}

func (f *TextField) Update(msg tea.Msg) tea.Cmd {
	if f.disabled {
		return nil
	}
	if f.filter != nil {
		if msg, ok := msg.(tea.KeyPressMsg); ok {
			if k := msg.Key(); k.Text != "" && !f.filter(k) {
				return nil
			}
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *TextField) View() string {
	if f.disabled {
		val := f.input.Value()
		if val == "" {
			val = f.input.Placeholder
		}
		out := f.dimStyle.Render(val)
		if f.suffix != "" {
			out += " " + f.suffix
		}
		return out
	}
	if f.suffix == "" {
		return f.input.View()
	}
	return f.input.View() + " " + f.suffix
}
func (f *TextField) Focus() tea.Cmd {
	if f.disabled {
		return nil
	}
	return f.input.Focus()
}
func (f *TextField) Blur() { f.input.Blur() }
func (f *TextField) SetWidth(w int) {
	if f.suffix != "" {
		// Pin the input to CharLimit so the suffix sits at a fixed column
		// regardless of the value's length. Falls back to w minus suffix
		// width when CharLimit is unset.
		if f.input.CharLimit > 0 {
			f.input.SetWidth(f.input.CharLimit)
			return
		}
		w -= lipgloss.Width(f.suffix) + 1
	}
	f.input.SetWidth(max(w, 1))
}
func (f *TextField) IsFocusable() bool { return !f.disabled }

// SetDisabled toggles disabled state. Disabled fields skip focus during
// Tab navigation, ignore input, and render the value in a dimmed style.
func (f *TextField) SetDisabled(v bool) {
	if f.disabled == v {
		return
	}
	f.disabled = v
	if v {
		f.input.Blur()
	}
}

// SetDimStyle sets the style used to render the value when disabled.
// Defaults to the zero style (no visual change beyond skipping the cursor).
func (f *TextField) SetDimStyle(s lipgloss.Style) { f.dimStyle = s }

// FilterDigits allows only digit characters (0-9).
// Every rune in the key text must be a digit; a multi-rune event (e.g. a
// paste) is rejected if any rune fails the check.
func FilterDigits(k tea.Key) bool {
	for _, r := range k.Text {
		if !unicode.IsDigit(r) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// TextAreaField
// ---------------------------------------------------------------------------

// TextAreaField wraps a bubbles textarea for multi-line text input.
type TextAreaField struct {
	input textarea.Model
}

func NewTextAreaField(placeholder string) *TextAreaField {
	input := textarea.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 500
	input.ShowLineNumbers = false
	input.SetHeight(3)
	hint := lipgloss.NewStyle().Italic(true).Faint(true)
	s := input.Styles()
	s.Focused.Placeholder = hint
	s.Blurred.Placeholder = hint
	input.SetStyles(s)
	return &TextAreaField{input: input}
}

func (f *TextAreaField) Value() string     { return f.input.Value() }
func (f *TextAreaField) SetValue(v string) { f.input.SetValue(v) }

func (f *TextAreaField) SetPlaceholder(p string) { f.input.Placeholder = p }
func (f *TextAreaField) SetCharLimit(n int)      { f.input.CharLimit = n }
func (f *TextAreaField) SetHeight(h int)         { f.input.SetHeight(h) }

func (f *TextAreaField) Update(msg tea.Msg) tea.Cmd {
	if kp, ok := msg.(tea.KeyPressMsg); ok {
		k := kp.Key()
		// Block plain Enter so the Form can use it for focus cycling.
		// Shift+Enter inserts a newline by forwarding as a plain Enter.
		if k.Code == '\r' {
			if k.Mod&tea.ModShift == 0 {
				return nil
			}
			plain := tea.Key{Code: '\r'}
			msg = tea.KeyPressMsg(plain)
		}
	}
	var cmd tea.Cmd
	f.input, cmd = f.input.Update(msg)
	return cmd
}

func (f *TextAreaField) View() string      { return f.input.View() }
func (f *TextAreaField) Focus() tea.Cmd    { return f.input.Focus() }
func (f *TextAreaField) Blur()             { f.input.Blur() }
func (f *TextAreaField) SetWidth(w int)    { f.input.SetWidth(w) }
func (f *TextAreaField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// SelectField
// ---------------------------------------------------------------------------

// SelectOption is a single entry in a SelectField.
type SelectOption struct {
	Label string
	// PluralLabel, when set, is shown by QuantitySelectField in place of
	// Label whenever the amount is not exactly 1 (e.g. "Days" for "2").
	// Empty means the option has no separate plural form.
	PluralLabel string
	Value       string
}

// selectHighlight tracks which arrow was just pressed.
type selectHighlight int

const (
	selectNone selectHighlight = iota
	selectLeft
	selectRight
)

// selectFlashMsg is sent by a tick to clear the arrow highlight.
type selectFlashMsg struct{ id int }

const selectFlashDuration = 150 * time.Millisecond

// SelectField cycles through a list of options with left/right arrows.
type SelectField struct {
	options     []SelectOption
	selected    int
	maxWidth    int
	focused     bool
	renderLabel func(SelectOption, bool) string
	highlight   selectHighlight
	flashID     int // incremented per flash; stale ticks are ignored
}

func NewSelectField(options []SelectOption) *SelectField {
	f := &SelectField{options: options}
	f.updateMaxWidth()
	return f
}

func (f *SelectField) Selected() int     { return f.selected }
func (f *SelectField) SetSelected(i int) { f.selected = i }
func (f *SelectField) SelectedOption() SelectOption {
	if f.selected < 0 || f.selected >= len(f.options) {
		return SelectOption{}
	}
	return f.options[f.selected]
}
func (f *SelectField) Value() string { return f.SelectedOption().Value }
func (f *SelectField) SetOptions(options []SelectOption) {
	f.options = options
	if len(f.options) == 0 {
		f.selected = 0
		f.maxWidth = 0
		return
	}
	if f.selected >= len(f.options) {
		f.selected = len(f.options) - 1
	}
	if f.selected < 0 {
		f.selected = 0
	}
	f.updateMaxWidth()
}
func (f *SelectField) SetRenderLabel(fn func(SelectOption, bool) string) {
	f.renderLabel = fn
	f.updateMaxWidth()
}

func (f *SelectField) renderOptionLabel(opt SelectOption, focused bool) string {
	if f.renderLabel != nil {
		return f.renderLabel(opt, focused)
	}
	return opt.Label
}

func (f *SelectField) updateMaxWidth() {
	maxW := 0
	for _, o := range f.options {
		if w := lipgloss.Width(f.renderOptionLabel(o, false)); w > maxW {
			maxW = w
		}
		// Reserve room for the plural form too so the unit column does
		// not shift when QuantitySelectField swaps "Day" for "Days".
		if o.PluralLabel != "" {
			plural := SelectOption{Label: o.PluralLabel, Value: o.Value}
			if w := lipgloss.Width(f.renderOptionLabel(plural, false)); w > maxW {
				maxW = w
			}
		}
	}
	f.maxWidth = maxW
}

func (f *SelectField) Update(msg tea.Msg) tea.Cmd {
	switch msg := msg.(type) {
	case selectFlashMsg:
		if msg.id == f.flashID {
			f.highlight = selectNone
		}
		return nil
	case tea.KeyPressMsg:
		n := len(f.options)
		if n == 0 {
			return nil
		}
		switch msg.String() {
		case "left", "h":
			f.selected = (f.selected - 1 + n) % n
			return f.flash(selectLeft)
		case "right", "l":
			f.selected = (f.selected + 1) % n
			return f.flash(selectRight)
		}
	}
	return nil
}

func (f *SelectField) flash(dir selectHighlight) tea.Cmd {
	f.highlight = dir
	f.flashID++
	id := f.flashID
	return tea.Tick(selectFlashDuration, func(time.Time) tea.Msg {
		return selectFlashMsg{id: id}
	})
}

func (f *SelectField) View() string {
	if len(f.options) == 0 {
		return ""
	}
	labelStyle := lipgloss.NewStyle().Width(f.maxWidth)
	if f.focused && f.renderLabel == nil {
		labelStyle = labelStyle.Reverse(true)
	}
	label := labelStyle.Render(f.renderOptionLabel(f.options[f.selected], f.focused))

	flash := lipgloss.NewStyle().Foreground(activeTheme.FormHighlight)
	prev := Glyphs["select.prev"]
	next := Glyphs["select.next"]

	if f.highlight == selectLeft {
		prev = flash.Render(prev)
	}
	if f.highlight == selectRight {
		next = flash.Render(next)
	}

	return label + "  " + mouseMark("select:prev", prev) + " " + mouseMark("select:next", next)
}

func (f *SelectField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *SelectField) Blur() {
	f.focused = false
	f.highlight = selectNone
}

func (f *SelectField) SetWidth(int) {}

// IsFocusable reports false for an empty option set: a select with nothing
// to choose has no useful interaction and must not capture focus or input.
func (f *SelectField) IsFocusable() bool { return len(f.options) > 0 }

// ---------------------------------------------------------------------------
// QuantitySelectField
// ---------------------------------------------------------------------------

// QuantitySelectField is a composite FormField that renders a positive integer
// input followed by a select on the same row, e.g. "1 Week". It implements
// subFocuser so Tab/Enter cycle between the amount and unit before leaving.
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
// falls back to the placeholder ("1"), matching what amountText shows.
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
// field and the recurrence "Ends: After N times" count fields so an
// empty or zero count can never persist an unbounded or invalid RRULE.
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

// ---------------------------------------------------------------------------
// CheckboxField
// ---------------------------------------------------------------------------

// CheckboxField is a focusable toggle rendered as [✓] or [ ].
type CheckboxField struct {
	label       string
	content     string
	suffix      string
	checked     bool
	autoChecked bool // true when checked was set by the form, not the user
	focused     bool
	quietFocus  bool // when true, focus does not apply reverse styling
	disabledFn  func() (disabled bool, text string)
}

func NewCheckboxField(label string, checked bool) *CheckboxField {
	return &CheckboxField{label: label, checked: checked}
}

func (f *CheckboxField) Checked() bool { return f.checked }

func (f *CheckboxField) SetChecked(v bool) { f.checked = v }

// SetContent sets the text rendered to the right of the checkbox glyph.
// When empty (default), only the glyph is shown.
func (f *CheckboxField) SetContent(v string) { f.content = v }

// SetSuffix sets text rendered after the content, outside the focus
// highlight. Useful for warnings or hints that shouldn't invert when
// the row is focused. Caller is responsible for any styling.
func (f *CheckboxField) SetSuffix(v string) { f.suffix = v }

// AutoChecked reports whether the current checked state was set
// programmatically by the form rather than by the user, which lets the
// form revert the state when the upstream condition changes.
func (f *CheckboxField) AutoChecked() bool     { return f.autoChecked }
func (f *CheckboxField) SetAutoChecked(v bool) { f.autoChecked = v }

// SetDisabledWhen registers a function that is evaluated on every Toggle and
// View call. When it returns disabled=true the toggle is inert and View
// renders the returned text instead of the normal [✓]/[ ] label.
func (f *CheckboxField) SetDisabledWhen(fn func() (disabled bool, text string)) {
	f.disabledFn = fn
}

// SetQuietFocus suppresses the default reverse-style highlight the checkbox
// applies when focused. Useful for non-primary toggles where the focus
// affordance comes from the form's focus marker.
func (f *CheckboxField) SetQuietFocus(v bool) { f.quietFocus = v }

func (f *CheckboxField) Toggle() {
	if f.disabledFn != nil {
		if disabled, _ := f.disabledFn(); disabled {
			return
		}
	}
	f.checked = !f.checked
	f.autoChecked = false
}

func (f *CheckboxField) Update(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		if s := msg.String(); s == "space" || s == " " {
			f.Toggle()
		}
	}
	return nil
}

func (f *CheckboxField) View() string {
	if f.disabledFn != nil {
		if disabled, text := f.disabledFn(); disabled {
			return text
		}
	}
	glyph := Glyphs["checkbox.off"]
	if f.checked {
		glyph = Glyphs["checkbox.on"]
	}
	style := lipgloss.NewStyle()
	if f.focused && !f.quietFocus {
		style = style.Reverse(true)
	}

	var out string
	if len(f.content) > 0 {
		out = style.Render(glyph + " " + f.content)
	} else {
		out = style.Render(glyph)
	}
	if f.suffix != "" {
		out += "  " + f.suffix
	}
	return out
}

func (f *CheckboxField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *CheckboxField) Blur() {
	f.focused = false
}
func (f *CheckboxField) SetWidth(int)      {}
func (f *CheckboxField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// StaticField
// ---------------------------------------------------------------------------

// StaticField is a non-focusable, display-only form field. It renders its
// value through an optional style function and ignores all input.
type StaticField struct {
	value   string
	styleFn func(string) string
}

// NewStaticField creates a display-only field. styleFn is applied to the value
// on every View call; pass nil for unstyled output.
func NewStaticField(value string, styleFn func(string) string) *StaticField {
	if styleFn == nil {
		styleFn = func(s string) string { return s }
	}
	return &StaticField{value: value, styleFn: styleFn}
}

func (f *StaticField) Value() string     { return f.value }
func (f *StaticField) SetValue(v string) { f.value = v }

func (f *StaticField) Update(tea.Msg) tea.Cmd { return nil }
func (f *StaticField) View() string           { return f.styleFn(f.value) }
func (f *StaticField) Focus() tea.Cmd         { return nil }
func (f *StaticField) Blur()                  {}
func (f *StaticField) SetWidth(int)           {}
func (f *StaticField) IsFocusable() bool      { return false }

// ---------------------------------------------------------------------------
// OpenerField
// ---------------------------------------------------------------------------

// OpenerField is a focusable, display-only field whose Enter handling is
// owned by the parent (typically to open an overlay). Looks like a
// StaticField but participates in focus cycling.
type OpenerField struct {
	value   string
	focused bool
}

func NewOpenerField(value string) *OpenerField {
	return &OpenerField{value: value}
}

func (f *OpenerField) Value() string     { return f.value }
func (f *OpenerField) SetValue(v string) { f.value = v }

func (f *OpenerField) Update(tea.Msg) tea.Cmd { return nil }
func (f *OpenerField) View() string {
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(f.value)
	}
	return f.value
}
func (f *OpenerField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *OpenerField) Blur()             { f.focused = false }
func (f *OpenerField) SetWidth(int)      {}
func (f *OpenerField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// PaletteField
// ---------------------------------------------------------------------------

// PaletteField is a FormField that cycles through color swatches with
// left/right arrows. The selected swatch is wrapped in brackets.
type PaletteField struct {
	swatches []string
	selected int // -1 for custom/off-palette
	focused  bool
}

func NewPaletteField(swatches []string, selected int) *PaletteField {
	return &PaletteField{
		swatches: swatches,
		selected: selected,
	}
}

func (f *PaletteField) Selected() int     { return f.selected }
func (f *PaletteField) SetSelected(i int) { f.selected = i }

func (f *PaletteField) Value() string {
	if f.selected >= 0 && f.selected < len(f.swatches) {
		return f.swatches[f.selected]
	}
	return ""
}

func (f *PaletteField) Update(msg tea.Msg) tea.Cmd {
	if msg, ok := msg.(tea.KeyPressMsg); ok {
		n := len(f.swatches)
		switch msg.String() {
		case "left", "h":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx > 0 {
				idx--
			}
			f.selected = idx
		case "right", "l":
			idx := f.selected
			if idx < 0 {
				idx = 0
			} else if idx < n-1 {
				idx++
			}
			f.selected = idx
		}
	}
	return nil
}

func (f *PaletteField) View() string {
	dot := func(c string) string {
		return lipgloss.NewStyle().Foreground(lipgloss.Color(c)).Render(Glyphs["dot"])
	}
	br := lipgloss.NewStyle().Bold(true)
	parts := make([]string, 0, len(f.swatches))
	for i, c := range f.swatches {
		target := "palette:" + strconv.Itoa(i)
		if i == f.selected {
			parts = append(parts, mouseMark(target, br.Render("[")+dot(c)+br.Render("]")))
		} else {
			parts = append(parts, mouseMark(target, dot(c)))
		}
	}
	out := strings.Join(parts, " ")
	// Reserve the same width whether a swatch is selected or not so the
	// trailing hex input keeps a fixed column.
	if f.selected < 0 {
		out += "  "
	}
	return out
}

func (f *PaletteField) Focus() tea.Cmd {
	f.focused = true
	return nil
}

func (f *PaletteField) Blur() {
	f.focused = false
}

func (f *PaletteField) SetWidth(int)      {}
func (f *PaletteField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// HexColorField
// ---------------------------------------------------------------------------

var hexRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// HexColorField wraps a TextField and appends a live color preview dot
// and "(custom)" label when the value doesn't match any palette swatch.
type HexColorField struct {
	input      *TextField
	paletteIdx int // -1 when off-palette
	dimColor   color.Color
}

func NewHexColorField(placeholder string, dimColor color.Color) *HexColorField {
	f := &HexColorField{
		input:    NewTextField(placeholder),
		dimColor: dimColor,
	}
	f.input.SetFilter(func(k tea.Key) bool {
		if k.Text == "" {
			return true
		}
		return isHexInputAllowed(k.Text, f.input.Position(), f.input.Value())
	})
	return f
}

func (f *HexColorField) Value() string              { return f.input.Value() }
func (f *HexColorField) SetValue(v string)          { f.input.SetValue(v) }
func (f *HexColorField) SetPaletteIdx(idx int)      { f.paletteIdx = idx }
func (f *HexColorField) Update(msg tea.Msg) tea.Cmd { return f.input.Update(msg) }
func (f *HexColorField) Focus() tea.Cmd             { return f.input.Focus() }
func (f *HexColorField) Blur()                      { f.input.Blur() }
func (f *HexColorField) SetWidth(w int)             { f.input.SetWidth(9) } // #rrggbb + cursor + 1
func (f *HexColorField) IsFocusable() bool          { return true }

func (f *HexColorField) Validate() string {
	hexVal := strings.TrimSpace(f.input.Value())
	if hexVal == "" {
		return "" // emptiness is handled by Required
	}
	if !hexRE.MatchString(hexVal) {
		return "Color must be #rrggbb"
	}
	return ""
}

func (f *HexColorField) View() string {
	base := f.input.View()
	hexVal := strings.TrimSpace(f.input.Value())
	if !hexRE.MatchString(hexVal) {
		return base
	}
	dot := lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render(Glyphs["dot"])
	if f.paletteIdx < 0 {
		customLabel := lipgloss.NewStyle().Foreground(f.dimColor).Italic(true).Render("(custom)")
		return base + "  " + dot + "  " + customLabel
	}
	return base + "  " + dot
}

// ---------------------------------------------------------------------------
// ColorField
// ---------------------------------------------------------------------------

// ColorField composes a PaletteField and a HexColorField on a single row.
// Tab cycles from palette to hex before leaving the field. Changes to
// either sub-field are mirrored to the other so the preview stays in sync.
type ColorField struct {
	palette  *PaletteField
	hex      *HexColorField
	subFocus int // 0 = palette, 1 = hex
	focused  bool
}

// NewColorField builds a composite palette+hex control pre-seeded with the
// given hex value.
func NewColorField(swatches []string, hex string, dim color.Color) *ColorField {
	idx := -1
	for i, c := range swatches {
		if strings.EqualFold(strings.TrimSpace(hex), c) {
			idx = i
			break
		}
	}
	p := NewPaletteField(swatches, idx)
	h := NewHexColorField("#rrggbb", dim)
	h.SetValue(hex)
	h.input.SetCharLimit(7)
	h.SetPaletteIdx(idx)
	return &ColorField{palette: p, hex: h}
}

// Value returns the current hex string.
func (f *ColorField) Value() string { return f.hex.Value() }

// Validate delegates to the hex field.
func (f *ColorField) Validate() string { return f.hex.Validate() }

func (f *ColorField) Update(msg tea.Msg) tea.Cmd {
	var cmd tea.Cmd
	switch f.subFocus {
	case 0:
		cmd = f.palette.Update(msg)
		if v := f.palette.Value(); v != "" && v != f.hex.Value() {
			f.hex.SetValue(v)
		}
	default:
		cmd = f.hex.Update(msg)
	}
	f.syncFromHex()
	return cmd
}

// syncFromHex reconciles the palette index with the current hex value
// whenever the hex changes.
func (f *ColorField) syncFromHex() {
	h := strings.TrimSpace(f.hex.Value())
	idx := -1
	for i, c := range f.palette.swatches {
		if strings.EqualFold(c, h) {
			idx = i
			break
		}
	}
	f.palette.SetSelected(idx)
	f.hex.SetPaletteIdx(idx)
}

func (f *ColorField) View() string {
	// Keep the row compact by skipping the hex field's "(custom)" trailer:
	// inside the composite the palette always neighbors the hex, so the
	// preview dot is sufficient and the label would push the row to wrap.
	base := f.hex.input.View()
	hexVal := strings.TrimSpace(f.hex.Value())
	hexRendered := base
	if hexRE.MatchString(hexVal) {
		dot := lipgloss.NewStyle().Foreground(lipgloss.Color(hexVal)).Render(Glyphs["dot"])
		hexRendered = base + "  " + dot
	}
	return f.palette.View() + "  " + hexRendered
}

func (f *ColorField) Focus() tea.Cmd {
	f.focused = true
	f.subFocus = 0
	f.hex.Blur()
	return f.palette.Focus()
}

func (f *ColorField) Blur() {
	f.focused = false
	f.palette.Blur()
	f.hex.Blur()
}

func (f *ColorField) SetWidth(int)      {}
func (f *ColorField) IsFocusable() bool { return true }

func (f *ColorField) SubFocusNext() (bool, tea.Cmd) {
	if f.subFocus == 0 {
		f.palette.Blur()
		f.subFocus = 1
		return true, f.hex.Focus()
	}
	return false, nil
}

func (f *ColorField) SubFocusPrev() (bool, tea.Cmd) {
	if f.subFocus == 1 {
		f.hex.Blur()
		f.subFocus = 0
		return true, f.palette.Focus()
	}
	return false, nil
}

// HandleClickTarget routes palette/hex targets to the correct sub-field
// and moves subFocus to match the click.
func (f *ColorField) HandleClickTarget(target string) tea.Cmd {
	if strings.HasPrefix(target, "palette:") {
		f.hex.Blur()
		f.subFocus = 0
		_ = f.palette.Focus()
		return f.palette.Update(keyMsg(""))
	}
	f.palette.Blur()
	f.subFocus = 1
	return f.hex.Focus()
}

// isHexInputAllowed reports whether the printable text t can be inserted
// into the hex input at cursor position pos given the current value.
func isHexInputAllowed(t string, pos int, current string) bool {
	for _, r := range t {
		switch {
		case r == '#':
			if pos != 0 || strings.ContainsRune(current, '#') {
				return false
			}
		case (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F'):
			// ok
		default:
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// DatePickerField
// ---------------------------------------------------------------------------

// DatePickerField is a focusable field that displays a formatted date. The
// actual date selection happens via an overlay managed by the parent; this
// field only renders the current value and toggles a focus highlight.
// When a range end is set, the field renders "Jan 2 → Jan 5, 2026".
type DatePickerField struct {
	value    time.Time
	endValue time.Time
	hasEnd   bool
	focused  bool
}

// NewDatePickerField creates a field that displays the given date.
func NewDatePickerField(value time.Time) *DatePickerField {
	return &DatePickerField{value: value}
}

func (f *DatePickerField) Date() time.Time     { return f.value }
func (f *DatePickerField) SetDate(t time.Time) { f.value = t }

// RangeEnd returns the range end date and whether one is set.
func (f *DatePickerField) RangeEnd() (time.Time, bool) { return f.endValue, f.hasEnd }

// SetRangeEnd records the range end for multi-day events. Pass the end
// date inclusive of the last day.
func (f *DatePickerField) SetRangeEnd(t time.Time) { f.endValue = t; f.hasEnd = true }

// ClearRangeEnd removes any range end, reverting to single-date display.
func (f *DatePickerField) ClearRangeEnd() { f.endValue = time.Time{}; f.hasEnd = false }

func (f *DatePickerField) Value() string { return f.formatted() }

func (f *DatePickerField) formatted() string {
	if !f.hasEnd {
		return f.value.Format("Mon, Jan 2, 2006")
	}
	// Compact range: drop weekday, collapse common year/month where possible.
	start, end := f.value, f.endValue
	if end.Before(start) {
		start, end = end, start
	}
	if start.Year() == end.Year() && start.Month() == end.Month() {
		return fmt.Sprintf("%s %d → %d, %d",
			start.Format("Jan"), start.Day(), end.Day(), start.Year())
	}
	if start.Year() == end.Year() {
		return fmt.Sprintf("%s %d → %s %d, %d",
			start.Format("Jan"), start.Day(), end.Format("Jan"), end.Day(), start.Year())
	}
	return fmt.Sprintf("%s %d, %d → %s %d, %d",
		start.Format("Jan"), start.Day(), start.Year(),
		end.Format("Jan"), end.Day(), end.Year())
}

func (f *DatePickerField) Update(tea.Msg) tea.Cmd { return nil }
func (f *DatePickerField) View() string {
	s := f.formatted()
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(s)
	}
	return s
}
func (f *DatePickerField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *DatePickerField) Blur()             { f.focused = false }
func (f *DatePickerField) SetWidth(int)      {}
func (f *DatePickerField) IsFocusable() bool { return true }

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

// Value returns the start value, satisfying the valuer interface for
// Required field checks.
func (f *TimeRangeField) Value() string { return f.start.Value() }

func (f *TimeRangeField) Update(msg tea.Msg) tea.Cmd {
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

// ---------------------------------------------------------------------------
// TimezoneField
// ---------------------------------------------------------------------------

// TimezoneField is a focusable field that displays an IANA timezone name. The
// actual timezone selection happens via an overlay managed by the parent; this
// field only renders the current value and toggles a focus highlight.
type TimezoneField struct {
	value   string
	focused bool
}

// NewTimezoneField creates a field displaying the given timezone name.
func NewTimezoneField(tz string) *TimezoneField {
	return &TimezoneField{value: tz}
}

func (f *TimezoneField) Value() string     { return f.value }
func (f *TimezoneField) SetValue(v string) { f.value = v }

func (f *TimezoneField) Update(tea.Msg) tea.Cmd { return nil }
func (f *TimezoneField) View() string {
	s := f.value
	if loc, err := time.LoadLocation(f.value); err == nil {
		_, off := time.Now().In(loc).Zone()
		s += "  (" + formatTZOffset(off) + ")"
	}
	if f.focused {
		return lipgloss.NewStyle().Reverse(true).Render(s)
	}
	return s
}

// formatTZOffset formats a seconds-from-UTC offset as "UTC+HH:MM" or "UTC-HH:MM".
func formatTZOffset(offsetSec int) string {
	sign := "+"
	if offsetSec < 0 {
		sign = "-"
		offsetSec = -offsetSec
	}
	h := offsetSec / 3600
	m := (offsetSec % 3600) / 60
	if m == 0 {
		return fmt.Sprintf("UTC%s%02d", sign, h)
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, h, m)
}
func (f *TimezoneField) Focus() tea.Cmd    { f.focused = true; return nil }
func (f *TimezoneField) Blur()             { f.focused = false }
func (f *TimezoneField) SetWidth(int)      {}
func (f *TimezoneField) IsFocusable() bool { return true }

// ---------------------------------------------------------------------------
// MouseEvent
// ---------------------------------------------------------------------------

// MouseEvent is a pre-resolved mouse click. The parent is responsible for
// calling mouse.Sweep on the rendered output and mouse.Resolve on clicks,
// then forwarding this message to Form.Update.
type MouseEvent struct {
	IsClick bool
	Target  string
}

// ---------------------------------------------------------------------------
// Form
// ---------------------------------------------------------------------------

var formKeys = struct {
	Tab      key.Binding
	ShiftTab key.Binding
	Enter    key.Binding
	ArrowFwd key.Binding
	ArrowBwd key.Binding
}{
	Tab:      key.NewBinding(key.WithKeys("tab")),
	ShiftTab: key.NewBinding(key.WithKeys("shift+tab")),
	Enter:    key.NewBinding(key.WithKeys("enter")),
	ArrowFwd: key.NewBinding(key.WithKeys("right", "down")),
	ArrowBwd: key.NewBinding(key.WithKeys("left", "up")),
}

// valuer is satisfied by fields that expose a text value (TextField,
// StaticField). Used by Form.validate to check required fields.
type valuer interface {
	Value() string
}

// validator is optionally implemented by fields that perform their own
// validation. Form.validate calls Validate after the required check
// and uses the returned message (if non-empty) as the field error.
type validator interface {
	Validate() string
}

// subFocuser is optionally implemented by composite fields with internal
// focus positions. Form checks this before cycling focus on Tab, Shift+Tab,
// and Enter, allowing the field to navigate between its sub-fields first.
type subFocuser interface {
	SubFocusNext() (consumed bool, cmd tea.Cmd)
	SubFocusPrev() (consumed bool, cmd tea.Cmd)
}

// LabelLayout controls where and how the label is rendered relative to
// the field.
type LabelLayout int

const (
	LabelTop         LabelLayout = iota // label on its own line above the field
	LabelInline                         // inline left-aligned:  "Name      [field]"
	LabelInlineRight                    // inline right-aligned: "     Name [field]"
)

// FormItem pairs a label with a field and an optional required constraint.
type FormItem struct {
	Label           string
	Field           FormField
	Required        bool
	LabelLayout     *LabelLayout // nil = use the form-level default
	ShowFocusMarker *bool        // nil = use the form-level default
}

// ButtonVariant selects which style pair a button uses.
type ButtonVariant int

const (
	Button       ButtonVariant = iota // neutral default (submit / cancel / routine action)
	ButtonDanger                      // destructive action
)

// ButtonStyle holds the normal and focused styles for a button variant.
type ButtonStyle struct {
	Normal  lipgloss.Style
	Focused lipgloss.Style
}

// Render returns the styled button label.
func (bs ButtonStyle) Render(label string, focused bool) string {
	if focused {
		return bs.Focused.Render(label)
	}
	return bs.Normal.Render(label)
}

// ButtonStyles holds style pairs for every button variant.
type ButtonStyles struct {
	Normal ButtonStyle
	Danger ButtonStyle
}

// Get returns the ButtonStyle for the given variant.
func (bs ButtonStyles) Get(v ButtonVariant) ButtonStyle {
	if v == ButtonDanger {
		return bs.Danger
	}
	return bs.Normal
}

// DefaultButtonStyles returns button styles driven by the active theme.
//
// At rest, Danger is a quiet variant of Normal: same pill shape and
// background, only the label is bold red (Theme.Error). This matches
// Apple's iOS pattern of red text on a neutral pill rather than a
// flashing red button.
//
// On focus, the two variants diverge deliberately. Normal flips to
// FormHighlight (the theme's selection accent), but Danger inverts to a
// red-on-contrasting-fg pill. That divergence costs us "single focus
// signal" uniformity, but it's the only way to keep destructive intent
// readable across themes whose FormHighlight lands in the warm/red end
// of the spectrum (Dracula's pink, Osaka's orange, Flexoki's coral) —
// red text on a warm highlight is unreadable. Putting the red on the
// background and computing a contrasting label via oklch.ContrastingFg
// guarantees legibility on every theme and emphasizes the destructive
// signal exactly when the user is about to commit it.
func DefaultButtonStyles() ButtonStyles {
	base := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	t := activeTheme
	return ButtonStyles{
		Normal: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(oklch.ContrastingFg(t.ButtonBg)),
			Focused: base.Background(t.FormHighlight).Foreground(oklch.ContrastingFg(t.FormHighlight)),
		},
		Danger: ButtonStyle{
			Normal:  base.Background(t.ButtonBg).Foreground(t.Error).Bold(true),
			Focused: base.Background(t.Error).Foreground(oklch.ContrastingFg(t.Error)).Bold(true),
		},
	}
}

// ButtonAlign controls horizontal placement of the button row.
type ButtonAlign int

const (
	ButtonAlignRight  ButtonAlign = iota // default: right-aligned
	ButtonAlignCenter                    // centered (for dialogs)
	ButtonAlignLeft                      // left-aligned
)

// FormStyles controls how the Form renders labels, errors, and buttons.
type FormStyles struct {
	Label           lipgloss.Style
	Required        lipgloss.Style // style for the "*" marker on required fields
	ShowFocusMarker bool           // when true, render focus glyph before the focused field
	Error           lipgloss.Style
	LabelLayout     LabelLayout  // default layout for all fields
	Buttons         ButtonStyles // styles for all button variants
	ButtonAlign     ButtonAlign  // horizontal placement of button row (default: right)
	ButtonRule      bool         // when true, render a horizontal rule above buttons
}

// DefaultFormStyles returns form styles driven by the active theme.
func DefaultFormStyles() FormStyles {
	t := activeTheme
	return FormStyles{
		Label:    lipgloss.NewStyle().Foreground(t.FormLabel),
		Required: lipgloss.NewStyle().Foreground(t.FormRequired),
		Error:    lipgloss.NewStyle().Foreground(t.FormError),
		Buttons:  DefaultButtonStyles(),
	}
}

// FormActionButton is an optional third button between Submit and Cancel.
// When Leading is true, the button renders flush-left in the button row
// (typically used for destructive actions that need visual distance
// from the primary action).
type FormActionButton struct {
	Label   string
	Variant ButtonVariant
	OnPress func() tea.Msg
	Leading bool
}

// Form manages a list of form fields with focus cycling, validation,
// and submit/cancel handling.
type Form struct {
	items         []FormItem
	styles        FormStyles
	submitLabel   string
	submitVariant ButtonVariant
	cancelVariant ButtonVariant
	actionButtons []FormActionButton
	focused       int
	width         int
	errorField    int
	error         string
	onSubmit      func(f *Form) tea.Cmd
	onCancel      func(f *Form) tea.Cmd
	onRebuild     func(f *Form)
	onFieldEnter  func(f *Form, field int) tea.Cmd
}

func NewForm(submitLabel string, styles FormStyles, items ...FormItem) Form {
	f := Form{
		items:         items,
		styles:        styles,
		submitLabel:   submitLabel,
		submitVariant: Button,
		cancelVariant: Button,
	}
	for i, item := range items {
		if item.Field.IsFocusable() {
			f.focused = i
			item.Field.Focus()
			break
		}
	}
	return f
}

func (f Form) Init() tea.Cmd {
	if f.focused < len(f.items) {
		return f.items[f.focused].Field.Focus()
	}
	return nil
}

func (f Form) Update(msg tea.Msg) (Form, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		f.width = msg.Width
		f.applyFieldWidths()

	case MouseEvent:
		if msg.IsClick {
			return f.handleClick(msg.Target)
		}

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, formKeys.ShiftTab):
			if f.focused < len(f.items) {
				if sf, ok := f.items[f.focused].Field.(subFocuser); ok {
					if consumed, cmd := sf.SubFocusPrev(); consumed {
						return f, cmd
					}
				}
			}
			return f.focusPrev()
		case key.Matches(msg, formKeys.Tab):
			if f.focused < len(f.items) {
				if sf, ok := f.items[f.focused].Field.(subFocuser); ok {
					if consumed, cmd := sf.SubFocusNext(); consumed {
						return f, cmd
					}
				}
			}
			return f.focusNext()
		case key.Matches(msg, formKeys.ArrowBwd):
			// Arrow keys act as alternate Tab/Shift-Tab, but only when the
			// focus is on a button slot — fields (text inputs, selects,
			// date pickers) still consume their own arrows for cursor or
			// option movement.
			if f.focused >= len(f.items) {
				return f.focusPrev()
			}
		case key.Matches(msg, formKeys.ArrowFwd):
			if f.focused >= len(f.items) {
				return f.focusNext()
			}
		case key.Matches(msg, formKeys.Enter):
			return f.handleEnter()
		}
	}

	if f.focused < len(f.items) {
		_, isKey := msg.(tea.KeyPressMsg)
		_, isPaste := msg.(tea.PasteMsg)
		if isKey {
			f.clearErrorOnInput()
		}
		cmd := f.items[f.focused].Field.Update(msg)
		if (isKey || isPaste) && f.onRebuild != nil {
			f.onRebuild(&f)
			f.focused = min(f.focused, f.totalCount()-1)
		}
		return f, cmd
	}

	return f, nil
}

func (f Form) fieldParts() []string {
	var parts []string

	// Compute the widest label among inline items so all inline labels
	// can be padded to the same column. The longest label gets exactly
	// one column of space before the field; shorter labels are padded
	// to match.
	maxLabelLen := 0
	anyRequired := false
	for _, item := range f.items {
		if _, isStatic := item.Field.(*StaticField); isStatic {
			continue
		}
		layout := f.styles.LabelLayout
		if item.LabelLayout != nil {
			layout = *item.LabelLayout
		}
		if layout != LabelTop {
			w := lipgloss.Width(item.Label)
			if w > maxLabelLen {
				maxLabelLen = w
			}
		}
		if item.Required {
			anyRequired = true
		}
	}
	// Reserve a trailing column for the "*" suffix so required and
	// optional rows align to the same field column.
	requiredPad := 0
	if anyRequired {
		requiredPad = 1
	}

	for i, item := range f.items {
		if _, isStatic := item.Field.(*StaticField); isStatic {
			parts = append(parts, item.Field.View())
			continue
		}

		field := mouseMark(fieldTarget(i), item.Field.View())
		hasError := f.error != "" && i == f.errorField

		layout := f.styles.LabelLayout
		if item.LabelLayout != nil {
			layout = *item.LabelLayout
		}

		focused := f.focused == i
		showMarker := f.styles.ShowFocusMarker
		if item.ShowFocusMarker != nil {
			showMarker = *item.ShowFocusMarker
		}
		marker := f.focusMarkerFor(focused, showMarker)

		target := fieldTarget(i)

		var row string
		switch {
		case (layout == LabelInline || layout == LabelInlineRight) && item.Label == "":
			row = lipgloss.JoinHorizontal(lipgloss.Top, marker, field)
		case layout == LabelInline:
			label := mouseMark(target, f.renderInlineLabel(item, maxLabelLen, requiredPad, false))
			row = lipgloss.JoinHorizontal(lipgloss.Top, label+" "+marker, field)
		case layout == LabelInlineRight:
			label := mouseMark(target, f.renderInlineLabel(item, maxLabelLen, requiredPad, true))
			row = lipgloss.JoinHorizontal(lipgloss.Top, label+" "+marker, field)
		default: // LabelTop
			label := mouseMark(target, f.renderTopLabel(item))
			row = label + "\n" + lipgloss.JoinHorizontal(lipgloss.Top, marker, field)
		}

		if hasError {
			parts = append(parts, row, f.styles.Error.Render(f.error))
		} else {
			parts = append(parts, row)
		}
	}

	return parts
}

func (f Form) buttonRow() string {
	bs := f.styles.Buttons
	leadParts := make([]string, 0, len(f.actionButtons))
	rightParts := make([]string, 0, len(f.actionButtons)+2)
	submitStyle := bs.Get(f.submitVariant)
	rightParts = append(rightParts, mouseMark("submit", submitStyle.Render(f.submitLabel, f.focused == f.submitIndex())))
	for i, ab := range f.actionButtons {
		style := bs.Get(ab.Variant)
		btn := mouseMark(actionTarget(i), style.Render(ab.Label, f.focused == f.actionIndex(i)))
		if ab.Leading {
			leadParts = append(leadParts, btn)
		} else {
			rightParts = append(rightParts, btn)
		}
	}
	cancelStyle := bs.Get(f.cancelVariant)
	rightParts = append(rightParts, mouseMark("cancel", cancelStyle.Render("Cancel", f.focused == f.cancelIndex())))

	rightGroup := lipgloss.JoinHorizontal(lipgloss.Top, rightParts...)

	// Use the form's width (typically set from Dialog.ContentWidth()) so
	// buttons align relative to the container, not the field rows. Fall
	// back to the natural content width when no explicit width is set.
	alignWidth := f.buttonAlignWidth()

	var buttons string
	if len(leadParts) > 0 {
		leadGroup := lipgloss.JoinHorizontal(lipgloss.Top, leadParts...)
		needed := lipgloss.Width(leadGroup) + lipgloss.Width(rightGroup)
		if alignWidth > 0 && needed+1 > alignWidth {
			// The action set doesn't fit beside Submit/Cancel. Degrade to
			// two rows instead of letting the container wrap the joined
			// line mid-pill: leading actions spread across their own row
			// (space-between, so e.g. three actions sit left / centered /
			// right), a blank line for breathing room, then Submit/Cancel
			// aligned on their own row.
			right := rightGroup
			if f.styles.ButtonAlign != ButtonAlignLeft {
				align := lipgloss.Right
				if f.styles.ButtonAlign == ButtonAlignCenter {
					align = lipgloss.Center
				}
				right = lipgloss.NewStyle().Width(alignWidth).Align(align).Render(rightGroup)
			}
			buttons = spreadRow(leadParts, alignWidth) + "\n\n" + right
		} else {
			spacerW := max(alignWidth-lipgloss.Width(leadGroup)-lipgloss.Width(rightGroup), 1)
			spacer := lipgloss.NewStyle().Width(spacerW).Render("")
			buttons = leadGroup + spacer + rightGroup
		}
	} else {
		buttons = rightGroup
		if alignWidth > 0 && f.styles.ButtonAlign != ButtonAlignLeft {
			align := lipgloss.Right
			if f.styles.ButtonAlign == ButtonAlignCenter {
				align = lipgloss.Center
			}
			buttons = lipgloss.NewStyle().Width(alignWidth).Align(align).Render(buttons)
		}
	}

	return buttons
}

func (f Form) buttonAlignWidth() int {
	alignWidth := f.width
	if alignWidth <= 0 {
		alignWidth = lipgloss.Width(lipgloss.JoinVertical(lipgloss.Left, f.fieldParts()...))
	}
	return alignWidth
}

func (f Form) buttonParts() []string {
	buttons := f.buttonRow()
	if f.styles.ButtonRule {
		ruleWidth := f.buttonAlignWidth()
		if ruleWidth <= 0 {
			ruleWidth = lipgloss.Width(buttons)
		}
		rule := strings.Repeat(Glyphs["separator.horizontal"], ruleWidth)
		return []string{lipgloss.NewStyle().Faint(true).Render(rule), buttons}
	}
	return []string{buttons}
}

// BodyView renders only the form fields, excluding the bottom action row.
// Dialogs with constrained height can put this body in a viewport while
// keeping Save/Cancel pinned below it.
func (f Form) BodyView() string {
	return lipgloss.JoinVertical(lipgloss.Left, f.fieldParts()...)
}

// ActionsView renders the form action separator and buttons without the
// leading blank line used by the full form view.
func (f Form) ActionsView() string {
	return lipgloss.JoinVertical(lipgloss.Left, f.buttonParts()...)
}

// ButtonRowView renders only the form buttons. Scrollable dialogs use this
// with their own separator so they can include scroll state in the rule.
func (f Form) ButtonRowView() string {
	return f.buttonRow()
}

// FocusedLine returns the first rendered body line for the focused item,
// or -1 when focus is on an action button (Submit/Cancel/etc.) rather than
// a body field. It is used by scrollable dialogs to keep the active field
// visible; callers must leave the scroll position untouched for buttons,
// otherwise reaching the button row would yank the body back to line 0.
func (f Form) FocusedLine() int {
	if f.focused >= len(f.items) {
		return -1
	}
	parts := f.fieldParts()
	line := 0
	for i, part := range parts {
		if i == f.focused {
			return line
		}
		line += max(lipgloss.Height(part), 1)
	}
	return -1
}

func (f Form) View() string {
	parts := f.fieldParts()
	parts = append(parts, "")
	parts = append(parts, f.buttonParts()...)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// spreadRow lays parts out across width with space-between justification:
// the first part flush left, the last flush right, the rest spaced evenly
// in between (three parts read as left / center / right). Falls back to
// single-space gaps when the parts don't fit.
func spreadRow(parts []string, width int) string {
	if len(parts) == 1 {
		return parts[0]
	}
	total := 0
	for _, p := range parts {
		total += lipgloss.Width(p)
	}
	gaps := len(parts) - 1
	space := width - total
	if space < gaps {
		// Too tight to justify; keep the pills readable with minimal gaps.
		return strings.Join(parts, " ")
	}
	base := space / gaps
	extra := space % gaps
	var b strings.Builder
	for i, p := range parts {
		if i > 0 {
			gap := base
			if i <= extra {
				gap++
			}
			b.WriteString(strings.Repeat(" ", gap))
		}
		b.WriteString(p)
	}
	return b.String()
}

// SetActionButton adds an action button. Can be called multiple times to
// add several buttons between Submit and Cancel.
func (f *Form) SetActionButton(label string, variant ButtonVariant, onPress func() tea.Msg) {
	f.actionButtons = append(f.actionButtons, FormActionButton{Label: label, Variant: variant, OnPress: onPress})
}

// ClearActionButtons removes every registered action button. Typically used
// when the set of buttons must track dynamic form state (e.g. showing a Test
// button only while a sync section is visible).
func (f *Form) ClearActionButtons() {
	f.actionButtons = nil
}

// SetLeadingActionButton adds an action button rendered on the left side
// of the button row, separated from Submit/Cancel. Typical use: destructive
// actions whose placement should not invite misclicks on the primary action.
func (f *Form) SetLeadingActionButton(label string, variant ButtonVariant, onPress func() tea.Msg) {
	f.actionButtons = append(f.actionButtons, FormActionButton{Label: label, Variant: variant, OnPress: onPress, Leading: true})
}

func (f *Form) SetCancelVariant(v ButtonVariant) {
	f.cancelVariant = v
}

func (f *Form) SetSubmitVariant(v ButtonVariant) {
	f.submitVariant = v
}

func (f *Form) OnSubmit(fn func(f *Form) tea.Cmd) {
	f.onSubmit = fn
}

func (f *Form) OnCancel(fn func(f *Form) tea.Cmd) {
	f.onCancel = fn
}

func (f *Form) OnRebuild(fn func(f *Form)) {
	f.onRebuild = fn
}

// OnFieldEnter registers a callback that fires when Enter is pressed on a
// form field (not a button or checkbox). If the callback returns a non-nil
// Cmd, it replaces the default focus-next behavior. Return nil to keep
// the default. The field parameter is the index of the focused field.
func (f *Form) OnFieldEnter(fn func(f *Form, field int) tea.Cmd) {
	f.onFieldEnter = fn
}

func (f *Form) AppendItems(items ...FormItem) {
	f.items = append(f.items, items...)
	f.applyFieldWidths()
}

func (f *Form) RemoveItems(from int) {
	if from < len(f.items) {
		f.items = f.items[:from]
	}
}

// SetItemLabel updates the label of the item at index i. Useful for labels
// that depend on another field's value.
func (f *Form) SetItemLabel(i int, label string) {
	if i < 0 || i >= len(f.items) {
		return
	}
	if f.items[i].Label == label {
		return
	}
	f.items[i].Label = label
	f.applyFieldWidths()
}

func (f Form) ItemCount() int { return len(f.items) }
func (f Form) Focused() int   { return f.focused }
func (f Form) HasError() bool { return f.error != "" }
func (f Form) Error() string  { return f.error }

// SetWidth explicitly sets the form's content width. Use this instead of
// relying on WindowSizeMsg when the form is embedded inside a Dialog or
// other container whose width differs from the terminal width.
func (f *Form) SetWidth(w int) {
	if w <= 0 {
		return
	}
	f.width = w
	f.applyFieldWidths()
}

// applyFieldWidths sets each field's width based on the form width,
// accounting for inline label columns and focus markers.
func (f *Form) applyFieldWidths() {
	if f.width <= 0 {
		return
	}

	// Compute the widest inline label (same logic as View).
	maxLabelLen := 0
	anyRequired := false
	for _, item := range f.items {
		if _, isStatic := item.Field.(*StaticField); isStatic {
			continue
		}
		layout := f.styles.LabelLayout
		if item.LabelLayout != nil {
			layout = *item.LabelLayout
		}
		if layout != LabelTop {
			w := lipgloss.Width(item.Label)
			if w > maxLabelLen {
				maxLabelLen = w
			}
		}
		if item.Required {
			anyRequired = true
		}
	}
	requiredPad := 0
	if anyRequired {
		requiredPad = 1
	}

	for _, item := range f.items {
		layout := f.styles.LabelLayout
		if item.LabelLayout != nil {
			layout = *item.LabelLayout
		}
		showMarker := f.styles.ShowFocusMarker
		if item.ShowFocusMarker != nil {
			showMarker = *item.ShowFocusMarker
		}

		w := f.width - 1 // reserve 1 col so textinput cursor doesn't overflow
		if layout == LabelInline || layout == LabelInlineRight {
			// Subtract: label column + "*" pad + " " gap + marker
			w -= maxLabelLen + requiredPad + 1
			if showMarker {
				w -= 2 // "> " or "  "
			}
		} else if showMarker {
			w -= 2
		}
		item.Field.SetWidth(max(w, 1))
	}
}

// SetError displays an error message on the given field index.
// Use this for domain-specific validation in OnSubmit callbacks.
func (f *Form) SetError(field int, msg string) {
	f.errorField = field
	f.error = msg
}

// ClearError removes the current error message.
func (f *Form) ClearError() {
	f.error = ""
}

func (f Form) Field(i int) FormField {
	return f.items[i].Field
}

func (f Form) FormTextField(i int) *TextField {
	return f.items[i].Field.(*TextField)
}

func (f Form) FormTextAreaField(i int) *TextAreaField {
	return f.items[i].Field.(*TextAreaField)
}

func (f Form) FormSelectField(i int) *SelectField {
	return f.items[i].Field.(*SelectField)
}

func (f Form) FormCheckboxField(i int) *CheckboxField {
	return f.items[i].Field.(*CheckboxField)
}

func (f Form) FormStaticField(i int) *StaticField {
	return f.items[i].Field.(*StaticField)
}

// Private

// focusedSelect returns the SelectField behind a generic "select:prev/next"
// arrow click for the given field, or nil if the field doesn't currently own
// a focused select. It unwraps the RecurrenceOnField composite, whose nested
// monthly select renders those same generic targets.
func focusedSelect(field FormField) *SelectField {
	switch fld := field.(type) {
	case *SelectField:
		if fld.focused {
			return fld
		}
	case *RecurrenceOnField:
		if fld.mode == RecurrenceOnMonthly && fld.monthly.focused {
			return fld.monthly
		}
	}
	return nil
}

func (f Form) handleClick(target string) (Form, tea.Cmd) {
	if target == "" {
		return f, nil
	}

	// Select arrow clicks: find the SelectField that owns the arrow,
	// focus it, and simulate the corresponding keypress.
	if target == "select:prev" || target == "select:next" {
		key := "right"
		if target == "select:prev" {
			key = "left"
		}
		for i := range f.items {
			// A focused RecurrenceOnField in monthly mode renders its nested
			// monthly select's arrows with these same generic targets, so
			// resolve against it too — not only top-level SelectFields.
			sf := focusedSelect(f.items[i].Field)
			if sf == nil {
				continue
			}
			cmd := sf.Update(keyMsg(key))
			if f.onRebuild != nil {
				f.onRebuild(&f)
			}
			return f, cmd
		}
		return f, nil
	}

	// Palette swatch clicks: "palette:N" selects swatch N and focuses the
	// field. Works for both standalone PaletteField and the composite
	// ColorField.
	if strings.HasPrefix(target, "palette:") {
		if idx, err := strconv.Atoi(strings.TrimPrefix(target, "palette:")); err == nil {
			for i := range f.items {
				switch pf := f.items[i].Field.(type) {
				case *PaletteField:
					pf.selected = idx
					f, cmd := f.focusIndex(i)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f, cmd
				case *ColorField:
					pf.palette.SetSelected(idx)
					if v := pf.palette.Value(); v != "" {
						pf.hex.SetValue(v)
					}
					pf.syncFromHex()
					f, cmd := f.focusIndex(i)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f, cmd
				}
			}
		}
		return f, nil
	}

	if strings.HasPrefix(target, "recurrenceon:") {
		if idx, err := strconv.Atoi(strings.TrimPrefix(target, "recurrenceon:")); err == nil {
			for i := range f.items {
				if rf, ok := f.items[i].Field.(*RecurrenceOnField); ok {
					rf.ToggleWeekDay(idx)
					if f.onRebuild != nil {
						f.onRebuild(&f)
					}
					return f.focusIndex(i)
				}
			}
		}
		return f, nil
	}

	if strings.HasPrefix(target, "quantityselect:") {
		for i := range f.items {
			if qf, ok := f.items[i].Field.(*QuantitySelectField); ok {
				f, _ = f.focusIndex(i)
				cmd := qf.HandleClickTarget(target)
				if f.onRebuild != nil {
					f.onRebuild(&f)
				}
				return f, cmd
			}
		}
		return f, nil
	}

	for i := range f.items {
		if target == fieldTarget(i) {
			if cb, ok := f.items[i].Field.(*CheckboxField); ok {
				cb.Toggle()
				if f.onRebuild != nil {
					f.onRebuild(&f)
				}
			}
			return f.focusIndex(i)
		}
	}

	switch target {
	case "submit":
		f.blurCurrent()
		f.focused = f.submitIndex()
		return f.submitIfValid()
	case "cancel":
		f.blurCurrent()
		f.focused = f.cancelIndex()
		if f.onCancel != nil {
			return f, f.onCancel(&f)
		}
	default:
		for i := range f.actionButtons {
			if target == actionTarget(i) {
				f.blurCurrent()
				f.focused = f.actionIndex(i)
				ab := f.actionButtons[i]
				return f, func() tea.Msg { return ab.OnPress() }
			}
		}
	}

	return f, nil
}

// FocusCancel moves focus to the Cancel button. Used by dialogs that want
// the safe default when the dialog opens (e.g. destructive confirmations).
func (f Form) FocusCancel() Form {
	f.blurCurrent()
	f.focused = f.cancelIndex()
	return f
}

func (f Form) focusNext() (Form, tea.Cmd) {
	f.blurCurrent()
	f.focused = (f.focused + 1) % f.totalCount()
	return f.skipToFocusable(1)
}

func (f Form) focusPrev() (Form, tea.Cmd) {
	f.blurCurrent()
	f.focused = (f.focused - 1 + f.totalCount()) % f.totalCount()
	return f.skipToFocusable(-1)
}

func (f Form) blurCurrent() {
	if f.focused < len(f.items) {
		f.items[f.focused].Field.Blur()
	}
}

func (f Form) focusCurrent() tea.Cmd {
	if f.focused < len(f.items) {
		return f.items[f.focused].Field.Focus()
	}
	return nil
}

// skipToFocusable scans in the given direction (+1 or -1) until it lands
// on a focusable field or a button slot. This ensures that focusPrev
// correctly skips non-focusable items backward instead of forward.
func (f Form) skipToFocusable(dir int) (Form, tea.Cmd) {
	start := f.focused
	for {
		if f.focused < len(f.items) {
			if f.items[f.focused].Field.IsFocusable() {
				return f, f.focusCurrent()
			}
			f.focused = (f.focused + dir + f.totalCount()) % f.totalCount()
		} else {
			return f, nil
		}
		if f.focused == start {
			return f, nil
		}
	}
}

func (f Form) handleEnter() (Form, tea.Cmd) {
	switch {
	case f.focused < len(f.items):
		// CheckboxField: Enter toggles.
		if cb, ok := f.items[f.focused].Field.(*CheckboxField); ok {
			cb.Toggle()
			if f.onRebuild != nil {
				f.onRebuild(&f)
				f.focused = min(f.focused, f.totalCount()-1)
			}
			return f, nil
		}
		// Composite field: advance internal focus before leaving.
		if sf, ok := f.items[f.focused].Field.(subFocuser); ok {
			if consumed, cmd := sf.SubFocusNext(); consumed {
				return f, cmd
			}
		}
		// Custom field-enter handler.
		if f.onFieldEnter != nil {
			if cmd := f.onFieldEnter(&f, f.focused); cmd != nil {
				return f, cmd
			}
		}
		return f.focusNext()
	case f.focused == f.submitIndex():
		return f.submitIfValid()
	case f.focused == f.cancelIndex():
		if f.onCancel != nil {
			return f, f.onCancel(&f)
		}
		return f, nil
	default:
		for i := range f.actionButtons {
			if f.focused == f.actionIndex(i) {
				ab := f.actionButtons[i]
				return f, func() tea.Msg { return ab.OnPress() }
			}
		}
	}
	return f, nil
}

func (f *Form) clearErrorOnInput() {
	if f.error != "" && f.focused == f.errorField {
		f.error = ""
	}
}

// Submit triggers validation and, if valid, calls the OnSubmit callback.
// Use this for external submit triggers like ctrl+s shortcuts.
func (f Form) Submit() (Form, tea.Cmd) {
	return f.submitIfValid()
}

func (f Form) submitIfValid() (Form, tea.Cmd) {
	var valid bool
	f, valid = f.validate()
	if !valid {
		return f.focusIndex(f.errorField)
	}
	if f.onSubmit != nil {
		return f, f.onSubmit(&f)
	}
	return f, nil
}

func (f Form) validate() (Form, bool) {
	f.error = ""
	for i, item := range f.items {
		if !item.Field.IsFocusable() {
			continue
		}
		if item.Required {
			if v, ok := item.Field.(valuer); ok {
				if strings.TrimSpace(v.Value()) == "" {
					f.errorField = i
					f.error = item.Label + " is required"
					return f, false
				}
			}
		}
		if v, ok := item.Field.(validator); ok {
			if msg := v.Validate(); msg != "" {
				f.errorField = i
				f.error = msg
				return f, false
			}
		}
	}
	return f, true
}

func (f Form) focusIndex(i int) (Form, tea.Cmd) {
	if i == f.focused {
		return f, nil
	}
	f.blurCurrent()
	f.focused = i
	return f, f.focusCurrent()
}

// Focus / Tab order is visual reading order, left-to-right:
//
//	fields → leading actions → submit → trailing actions → cancel
//
// Leading buttons render on the left of the button row (Disconnect, Set
// as Default, …), so they are tabbed through before Submit. Trailing
// buttons (Test, …) render on the right of Submit and are tabbed after
// it, before Cancel. Cancel is always last — Esc shortcut still wins for
// the safe exit. This mirrors AppKit's "Full Keyboard Access" order
// where Tab follows visual position rather than registration order.
func (f Form) leadingCount() int {
	n := 0
	for _, ab := range f.actionButtons {
		if ab.Leading {
			n++
		}
	}
	return n
}

func (f Form) submitIndex() int { return len(f.items) + f.leadingCount() }

// actionIndex maps a position in f.actionButtons to its focus index,
// taking the leading/trailing split into account so each button's tab
// position matches where it renders on screen.
func (f Form) actionIndex(i int) int {
	leading := f.actionButtons[i].Leading
	pos := 0
	for j := 0; j < i; j++ {
		if f.actionButtons[j].Leading == leading {
			pos++
		}
	}
	if leading {
		return len(f.items) + pos
	}
	return f.submitIndex() + 1 + pos
}

func (f Form) cancelIndex() int { return len(f.items) + 1 + len(f.actionButtons) }

func (f Form) totalCount() int { return f.cancelIndex() + 1 }

// Helpers

// focusMarkerFor returns the focus indicator string for a field.
func (f Form) focusMarkerFor(focused, showMarker bool) string {
	if !showMarker {
		return ""
	}
	if focused {
		return f.styles.Label.Render(Glyphs["focus"]) + " "
	}
	return "  "
}

// renderInlineLabel returns the label text for an inline row, padded to
// maxLabelLen+requiredPad so all rows share a column, with the required
// "*" marker rendered in its own style on required rows.
func (f Form) renderInlineLabel(item FormItem, maxLabelLen, requiredPad int, rightAlign bool) string {
	labelText := f.styles.Label.Render(item.Label)
	suffix := strings.Repeat(" ", requiredPad)
	if item.Required && requiredPad > 0 {
		suffix = f.styles.Required.Render("*")
	}
	composed := labelText + suffix
	style := lipgloss.NewStyle().Width(maxLabelLen + requiredPad)
	if rightAlign {
		style = style.Align(lipgloss.Right)
	}
	return style.Render(composed)
}

// renderTopLabel returns the label text for a top-layout row with the
// required marker appended inline (no column padding needed).
func (f Form) renderTopLabel(item FormItem) string {
	labelText := f.styles.Label.Render(item.Label)
	if item.Required {
		return labelText + f.styles.Required.Render("*")
	}
	return labelText
}

// LayoutPtr returns a pointer to a LabelLayout value, for use in
// FormItem.LabelLayout overrides.
func LayoutPtr(l LabelLayout) *LabelLayout { return &l }

// BoolPtr returns a pointer to a bool, for use in FormItem overrides.
func BoolPtr(b bool) *bool { return &b }

func fieldTarget(i int) string {
	return "field:" + strconv.Itoa(i)
}

func actionTarget(i int) string {
	return "action:" + strconv.Itoa(i)
}

func keyMsg(s string) tea.KeyPressMsg {
	k := tea.Key{Text: s}
	if r := []rune(s); len(r) == 1 {
		k.Code = r[0]
	}
	return tea.KeyPressMsg(k)
}
