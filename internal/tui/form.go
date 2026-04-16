package tui

import (
	"strconv"
	"strings"
	"unicode"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
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
	input  textinput.Model
	filter func(tea.Key) bool
}

func NewTextField(placeholder string) *TextField {
	input := textinput.New()
	input.Prompt = ""
	input.Placeholder = placeholder
	input.CharLimit = 256
	return &TextField{input: input}
}

func (f *TextField) Value() string     { return f.input.Value() }
func (f *TextField) SetValue(v string) { f.input.SetValue(v) }

func (f *TextField) SetPlaceholder(p string) { f.input.Placeholder = p }
func (f *TextField) SetCharLimit(n int)      { f.input.CharLimit = n }
func (f *TextField) Position() int           { return f.input.Position() }

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

func (f *TextField) Update(msg tea.Msg) tea.Cmd {
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

func (f *TextField) View() string      { return f.input.View() }
func (f *TextField) Focus() tea.Cmd    { return f.input.Focus() }
func (f *TextField) Blur()             { f.input.Blur() }
func (f *TextField) SetWidth(w int)    { f.input.SetWidth(w) }
func (f *TextField) IsFocusable() bool { return true }

// FilterDigits allows only digit characters (0-9).
func FilterDigits(k tea.Key) bool {
	return k.Text == "" || unicode.IsDigit(rune(k.Text[0]))
}

// ---------------------------------------------------------------------------
// CheckboxField
// ---------------------------------------------------------------------------

// CheckboxField is a focusable toggle rendered as [✓] or [ ].
type CheckboxField struct {
	label      string
	checked    bool
	disabledFn func() (disabled bool, text string)
}

func NewCheckboxField(label string, checked bool) *CheckboxField {
	return &CheckboxField{label: label, checked: checked}
}

func (f *CheckboxField) Checked() bool { return f.checked }

func (f *CheckboxField) SetChecked(v bool) { f.checked = v }

// SetDisabledWhen registers a function that is evaluated on every Toggle and
// View call. When it returns disabled=true the toggle is inert and View
// renders the returned text instead of the normal [✓]/[ ] label.
func (f *CheckboxField) SetDisabledWhen(fn func() (disabled bool, text string)) {
	f.disabledFn = fn
}

func (f *CheckboxField) Toggle() {
	if f.disabledFn != nil {
		if disabled, _ := f.disabledFn(); disabled {
			return
		}
	}
	f.checked = !f.checked
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
	if f.checked {
		return Glyphs["checkbox.on"] + " " + f.label
	}
	return Glyphs["checkbox.off"] + " " + f.label
}

func (f *CheckboxField) Focus() tea.Cmd    { return nil }
func (f *CheckboxField) Blur()             {}
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
}{
	Tab:      key.NewBinding(key.WithKeys("tab")),
	ShiftTab: key.NewBinding(key.WithKeys("shift+tab")),
	Enter:    key.NewBinding(key.WithKeys("enter")),
}

// valuer is satisfied by fields that expose a text value (TextField,
// StaticField). Used by Form.validate to check required fields.
type valuer interface {
	Value() string
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
	ShowInputBorder *bool        // nil = use the form-level default
}

// ButtonVariant selects which style pair a button uses.
type ButtonVariant int

const (
	ButtonPrimary   ButtonVariant = iota // submit / main action
	ButtonSecondary                      // cancel / neutral
	ButtonDanger                         // destructive action
	ButtonGhost                          // minimal / text-only
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
	Primary   ButtonStyle
	Secondary ButtonStyle
	Danger    ButtonStyle
	Ghost     ButtonStyle
}

// Get returns the ButtonStyle for the given variant.
func (bs ButtonStyles) Get(v ButtonVariant) ButtonStyle {
	switch v {
	case ButtonPrimary:
		return bs.Primary
	case ButtonDanger:
		return bs.Danger
	case ButtonGhost:
		return bs.Ghost
	default:
		return bs.Secondary
	}
}

// DefaultButtonStyles returns minimal button styles suitable for testing.
func DefaultButtonStyles() ButtonStyles {
	base := lipgloss.NewStyle().Padding(0, 2).MarginRight(1)
	return ButtonStyles{
		Primary: ButtonStyle{
			Normal:  base.Background(lipgloss.Color("61")).Foreground(lipgloss.Color("255")).Bold(true),
			Focused: base.Background(lipgloss.Color("63")).Foreground(lipgloss.Color("255")).Bold(true),
		},
		Secondary: ButtonStyle{
			Normal:  base.Background(lipgloss.Color("240")).Foreground(lipgloss.Color("255")),
			Focused: base.Background(lipgloss.Color("63")).Foreground(lipgloss.Color("255")),
		},
		Danger: ButtonStyle{
			Normal:  base.Background(lipgloss.Color("52")).Foreground(lipgloss.Color("255")),
			Focused: base.Background(lipgloss.Color("160")).Foreground(lipgloss.Color("255")),
		},
		Ghost: ButtonStyle{
			Normal:  base.Foreground(lipgloss.Color("240")),
			Focused: base.Foreground(lipgloss.Color("255")).Background(lipgloss.Color("63")),
		},
	}
}

// FormStyles controls how the Form renders labels, errors, and buttons.
type FormStyles struct {
	Label           lipgloss.Style
	Input           lipgloss.Style // style wrapping unfocused fields (e.g. border)
	InputFocused    lipgloss.Style // style wrapping the focused field
	ShowInputBorder bool           // when true, wrap text fields with Input/InputFocused styles
	ShowFocusMarker bool           // when true, render focus glyph before the focused field
	Error           lipgloss.Style
	LabelLayout     LabelLayout    // default layout for all fields
	Buttons         ButtonStyles   // styles for all button variants
}

// DefaultFormStyles returns minimal styles suitable for testing or as a
// starting point.
func DefaultFormStyles() FormStyles {
	return FormStyles{
		Label:   lipgloss.NewStyle().Bold(true),
		Error:   lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
		Buttons: DefaultButtonStyles(),
	}
}

// FormActionButton is an optional third button between Submit and Cancel.
type FormActionButton struct {
	Label   string
	Variant ButtonVariant
	OnPress func() tea.Msg
}

// Form manages a list of form fields with focus cycling, validation,
// and submit/cancel handling.
type Form struct {
	items         []FormItem
	styles        FormStyles
	submitLabel   string
	cancelVariant ButtonVariant
	actionButtons []FormActionButton
	focused       int
	width        int
	errorField   int
	error        string
	onSubmit     func(f *Form) tea.Cmd
	onCancel     func(f *Form) tea.Cmd
	onRebuild    func(f *Form)
}

func NewForm(submitLabel string, styles FormStyles, items ...FormItem) Form {
	f := Form{
		items:         items,
		styles:        styles,
		submitLabel:   submitLabel,
		cancelVariant: ButtonSecondary,
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
		inputWidth := min(f.width-4, 60)
		for _, item := range f.items {
			item.Field.SetWidth(inputWidth)
		}

	case MouseEvent:
		if msg.IsClick {
			return f.handleClick(msg.Target)
		}

	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, formKeys.ShiftTab):
			return f.focusPrev()
		case key.Matches(msg, formKeys.Tab):
			return f.focusNext()
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

func (f Form) View() string {
	var parts []string

	// Compute the widest label among inline items so all inline labels
	// can be padded to the same column. The longest label gets exactly
	// one column of space before the field; shorter labels are padded
	// to match.
	maxLabelLen := 0
	for _, item := range f.items {
		if _, isStatic := item.Field.(*StaticField); isStatic {
			continue
		}
		layout := f.styles.LabelLayout
		if item.LabelLayout != nil {
			layout = *item.LabelLayout
		}
		if layout != LabelTop && len(item.Label) > maxLabelLen {
			maxLabelLen = len(item.Label)
		}
	}

	for i, item := range f.items {
		if _, isStatic := item.Field.(*StaticField); isStatic {
			parts = append(parts, item.Field.View())
			continue
		}

		fieldView := item.Field.View()
		showBorder := f.styles.ShowInputBorder
		if item.ShowInputBorder != nil {
			showBorder = *item.ShowInputBorder
		}
		if showBorder {
			if _, isText := item.Field.(*TextField); isText {
				if f.focused == i {
					fieldView = f.styles.InputFocused.Render(fieldView)
				} else {
					fieldView = f.styles.Input.Render(fieldView)
				}
			}
		}
		field := mouseMark(fieldTarget(i), fieldView)
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

		var row string
		switch {
		case (layout == LabelInline || layout == LabelInlineRight) && item.Label == "":
			row = lipgloss.JoinHorizontal(lipgloss.Center, marker, field)
		case layout == LabelInline:
			label := f.styles.Label.Width(maxLabelLen).Render(item.Label)
			row = lipgloss.JoinHorizontal(lipgloss.Center, label+" "+marker, field)
		case layout == LabelInlineRight:
			label := f.styles.Label.Width(maxLabelLen).Align(lipgloss.Right).Render(item.Label)
			row = lipgloss.JoinHorizontal(lipgloss.Center, label+" "+marker, field)
		default: // LabelTop
			label := f.styles.Label.Render(item.Label)
			row = label + "\n" + lipgloss.JoinHorizontal(lipgloss.Center, marker, field)
		}

		if hasError {
			parts = append(parts, row, f.styles.Error.Render(f.error), "")
		} else {
			parts = append(parts, row, "")
		}
	}

	bs := f.styles.Buttons
	buttonParts := []string{mouseMark("submit", bs.Primary.Render(f.submitLabel, f.focused == f.submitIndex()))}
	for i, ab := range f.actionButtons {
		style := bs.Get(ab.Variant)
		buttonParts = append(buttonParts, mouseMark(actionTarget(i), style.Render(ab.Label, f.focused == f.actionIndex(i))))
	}
	cancelStyle := bs.Get(f.cancelVariant)
	buttonParts = append(buttonParts, mouseMark("cancel", cancelStyle.Render("Cancel", f.focused == f.cancelIndex())))
	buttons := lipgloss.JoinHorizontal(lipgloss.Center, buttonParts...)
	parts = append(parts, "", buttons)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// SetActionButton adds an action button. Can be called multiple times to
// add several buttons between Submit and Cancel.
func (f *Form) SetActionButton(label string, variant ButtonVariant, onPress func() tea.Msg) {
	f.actionButtons = append(f.actionButtons, FormActionButton{Label: label, Variant: variant, OnPress: onPress})
}

func (f *Form) SetCancelVariant(v ButtonVariant) {
	f.cancelVariant = v
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

func (f *Form) AppendItems(items ...FormItem) {
	inputWidth := min(f.width-4, 60)
	for _, item := range items {
		item.Field.SetWidth(inputWidth)
	}
	f.items = append(f.items, items...)
}

func (f *Form) RemoveItems(from int) {
	if from < len(f.items) {
		f.items = f.items[:from]
	}
}

func (f Form) ItemCount() int { return len(f.items) }
func (f Form) Focused() int   { return f.focused }
func (f Form) HasError() bool { return f.error != "" }
func (f Form) Error() string  { return f.error }

func (f Form) Field(i int) FormField {
	return f.items[i].Field
}

func (f Form) FormTextField(i int) *TextField {
	return f.items[i].Field.(*TextField)
}

func (f Form) FormCheckboxField(i int) *CheckboxField {
	return f.items[i].Field.(*CheckboxField)
}

func (f Form) FormStaticField(i int) *StaticField {
	return f.items[i].Field.(*StaticField)
}

// Private

func (f Form) handleClick(target string) (Form, tea.Cmd) {
	if target == "" {
		return f, nil
	}

	for i := range f.items {
		if target == fieldTarget(i) {
			if cb, ok := f.items[i].Field.(*CheckboxField); ok {
				cb.Toggle()
			}
			return f.focusIndex(i)
		}
	}

	switch {
	case target == "submit":
		f.blurCurrent()
		f.focused = f.submitIndex()
		return f.submitIfValid()
	case target == "cancel":
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
		if !item.Required || !item.Field.IsFocusable() {
			continue
		}
		if v, ok := item.Field.(valuer); ok {
			if strings.TrimSpace(v.Value()) == "" {
				f.errorField = i
				f.error = item.Label + " is required"
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

func (f Form) submitIndex() int { return len(f.items) }

func (f Form) actionIndex(i int) int { return len(f.items) + 1 + i }

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
