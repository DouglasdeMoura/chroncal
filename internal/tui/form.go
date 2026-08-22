package tui

import (
	"strconv"
	"strings"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

// ---------------------------------------------------------------------------
// FormField interface
// ---------------------------------------------------------------------------

// FormField is the interface that all form field types must implement.
// It provides the minimal surface a Form needs to manage focus cycle,
// render, and message dispatch across heterogeneous field types.
type FormField interface {
	Update(tea.Msg) tea.Cmd
	View() string
	Focus() tea.Cmd
	Blur()
	SetWidth(int)
	IsFocusable() bool
}

// ---------------------------------------------------------------------------
// MouseEvent
// ---------------------------------------------------------------------------

// MouseEvent is a pre-resolved mouse click. The parent is responsible for
// a call of mouse.Sweep on the rendered output and mouse.Resolve on clicks,
// then a forward of this message to Form.Update.
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
// focus positions. Form checks this before a focus cycle on Tab, Shift+Tab,
// and Enter. The field can then navigate between its sub-fields first.
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
	// AlignToFieldColumn indents an empty-label inline row to the shared
	// field column, so a control without its own label (e.g. a checkbox)
	// lines up under the neighbouring fields instead of hugging the left
	// edge. Ignored when the row has a label or the layout is LabelTop.
	AlignToFieldColumn bool
}

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
// When Leading is true, the button renders flush-left in the button row.
// That is typically used for destructive actions that need visual distance
// from the primary action. Utility lifts the button onto a quieter tier
// above the commit row instead. Use it for secondary actions that are
// neither destructive nor part of a commit of the form.
type FormActionButton struct {
	Label   string
	Variant ButtonVariant
	OnPress func() tea.Msg
	Leading bool
	Utility bool
}

// Form manages a list of form fields with focus cycle, validation,
// and submit/cancel handle.
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
			// focus is on a button slot. Fields (text inputs, selects,
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
			// A labeled static joins the shared inline label column like any
			// other row (e.g. the calendar detail's "Location  Local");
			// unlabeled statics stay full-width status/footnote lines.
			layout := f.styles.LabelLayout
			if item.LabelLayout != nil {
				layout = *item.LabelLayout
			}
			if item.Label != "" && (layout == LabelInline || layout == LabelInlineRight) {
				showMarker := f.styles.ShowFocusMarker
				if item.ShowFocusMarker != nil {
					showMarker = *item.ShowFocusMarker
				}
				label := f.renderInlineLabel(item, maxLabelLen, requiredPad, layout == LabelInlineRight)
				marker := f.focusMarkerFor(false, showMarker)
				parts = append(parts, lipgloss.JoinHorizontal(lipgloss.Top, label+" "+marker, item.Field.View()))
				continue
			}
			parts = append(parts, item.Field.View())
			continue
		}

		// Key a top-level select's arrow targets by field index so clicking an
		// unfocused arrow resolves to the owning field (issue #498).
		if sf, ok := item.Field.(*SelectField); ok {
			sf.SetArrowIndex(i)
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
			indent := ""
			if item.AlignToFieldColumn && maxLabelLen > 0 {
				indent = strings.Repeat(" ", maxLabelLen+requiredPad+1)
			}
			row = lipgloss.JoinHorizontal(lipgloss.Top, indent+marker, field)
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
	utilParts := make([]string, 0, len(f.actionButtons))
	leadParts := make([]string, 0, len(f.actionButtons))
	rightParts := make([]string, 0, len(f.actionButtons)+2)
	submitStyle := bs.Get(f.submitVariant)
	rightParts = append(rightParts, mouseMark("submit", submitStyle.Render(f.submitLabel, f.focused == f.submitIndex())))
	for i, ab := range f.actionButtons {
		style := bs.Get(ab.Variant)
		btn := mouseMark(actionTarget(i), style.Render(ab.Label, f.focused == f.actionIndex(i)))
		switch {
		case ab.Utility:
			utilParts = append(utilParts, btn)
		case ab.Leading:
			leadParts = append(leadParts, btn)
		default:
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
	rightAligned := func(s string) string {
		if alignWidth > 0 && f.styles.ButtonAlign != ButtonAlignLeft {
			align := lipgloss.Right
			if f.styles.ButtonAlign == ButtonAlignCenter {
				align = lipgloss.Center
			}
			return lipgloss.NewStyle().Width(alignWidth).Align(align).Render(s)
		}
		return s
	}

	// Commit row: leading (non-utility) actions sit flush-left beside the
	// right-aligned commit controls — the sheet's bottom-left corner, the
	// canonical spot for a destructive action. When the row cannot fit them
	// without wrapping a pill, they degrade into the utility tier instead.
	commit := rightAligned(rightGroup)
	if len(leadParts) > 0 {
		leadGroup := lipgloss.JoinHorizontal(lipgloss.Top, leadParts...)
		needed := lipgloss.Width(leadGroup) + lipgloss.Width(rightGroup)
		if alignWidth > 0 && needed+1 > alignWidth {
			utilParts = append(utilParts, leadParts...)
		} else {
			spacerW := max(alignWidth-lipgloss.Width(leadGroup)-lipgloss.Width(rightGroup), 1)
			commit = leadGroup + lipgloss.NewStyle().Width(spacerW).Render("") + rightGroup
		}
	}
	if len(utilParts) == 0 {
		return commit
	}

	// Utility tier: secondary actions on their own quieter row above the
	// commit controls. Side by side when they fit. Stacked one per line
	// otherwise. A rich set then never overflows the dialog width.
	tier := lipgloss.JoinHorizontal(lipgloss.Top, utilParts...)
	if alignWidth > 0 && lipgloss.Width(tier) > alignWidth {
		tier = strings.Join(utilParts, "\n")
	}
	return tier + "\n\n" + commit
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

// BodyView renders only the form fields. It excludes the bottom action row.
// Dialogs with constrained height can put this body in a viewport. Save/Cancel
// then stay pinned below it.
func (f Form) BodyView() string {
	return lipgloss.JoinVertical(lipgloss.Left, f.fieldParts()...)
}

// ActionsView renders the form action separator and buttons without the
// blank line at the start used by the full form view.
func (f Form) ActionsView() string {
	return lipgloss.JoinVertical(lipgloss.Left, f.buttonParts()...)
}

// ButtonRowView renders only the form buttons. Scrollable dialogs use this
// with their own separator so they can include scroll state in the rule.
func (f Form) ButtonRowView() string {
	return f.buttonRow()
}

// FocusedLine returns the first rendered body line for the focused item.
// It returns -1 when focus is on an action button (Submit/Cancel/etc.)
// rather than a body field. Scrollable dialogs use it to keep the active
// field visible. Callers must leave the scroll position untouched for
// buttons. Otherwise a move to the button row would yank the body back
// to line 0.
func (f Form) FocusedLine() int {
	if f.focused >= len(f.items) {
		return -1
	}
	parts := f.fieldParts()
	line := 0
	part := 0
	// fieldParts emits one part per item, except the errored field which
	// emits a second part for its error line. Walk items (not parts) so the
	// returned offset tracks f.focused, which is an item index.
	for item := 0; item < len(f.items) && part < len(parts); item++ {
		if item == f.focused {
			return line
		}
		line += max(lipgloss.Height(parts[part]), 1)
		part++
		if f.error != "" && item == f.errorField && part < len(parts) {
			line += max(lipgloss.Height(parts[part]), 1)
			part++
		}
	}
	return -1
}

func (f Form) View() string {
	parts := f.fieldParts()
	parts = append(parts, "")
	parts = append(parts, f.buttonParts()...)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// SetActionButton adds an action button. Can be called multiple times to
// add several buttons between Submit and Cancel.
func (f *Form) SetActionButton(label string, variant ButtonVariant, onPress func() tea.Msg) {
	f.actionButtons = append(f.actionButtons, FormActionButton{Label: label, Variant: variant, OnPress: onPress})
}

// ClearActionButtons removes every registered action button. Typically used
// when the set of buttons must track dynamic form state (for example a Test
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

// SetUtilityActionButton adds a secondary action to the quiet utility tier
// above the commit row. Utilities render side by side when they fit the form
// width. They stack vertically otherwise. Utility buttons take Leading focus
// order. Tab then visits them before the commit controls.
func (f *Form) SetUtilityActionButton(label string, variant ButtonVariant, onPress func() tea.Msg) {
	f.actionButtons = append(f.actionButtons, FormActionButton{Label: label, Variant: variant, OnPress: onPress, Leading: true, Utility: true})
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
// WindowSizeMsg when the form is embedded inside a Dialog or
// other container whose width differs from the terminal width.
func (f *Form) SetWidth(w int) {
	if w <= 0 {
		return
	}
	f.width = w
	f.applyFieldWidths()
}

// applyFieldWidths sets each field's width based on the form width.
// It accounts for inline label columns and focus markers.
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

// Blur removes keyboard focus from every field and button so the form renders
// as an inert preview while input is owned elsewhere (e.g. the Calendars
// manager's root selection preview). The next focus cycle re-enters at the
// first focusable item.
func (f Form) Blur() Form {
	f.blurCurrent()
	f.focused = -1
	return f
}

func (f Form) blurCurrent() {
	if f.focused >= 0 && f.focused < len(f.items) {
		f.items[f.focused].Field.Blur()
	}
}

func (f Form) focusCurrent() tea.Cmd {
	if f.focused >= 0 && f.focused < len(f.items) {
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

// Focus / Tab order is visual read order, left-to-right:
//
//	fields → leading actions → submit → trailing actions → cancel
//
// Leading buttons render on the left of the button row (Disconnect, Set
// as Default, …). Tab walks them before Submit. Trailing buttons (Test,
// …) render on the right of Submit. Tab walks them after it, before
// Cancel. Cancel is always last. The Esc shortcut still wins for the
// safe exit. This mirrors AppKit's "Full Keyboard Access" order. Tab
// follows visual position rather than registration order.
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

// actionIndex maps a position in f.actionButtons to its focus index.
// It takes the leading/trailing split into account. Each button's tab
// position then matches where it renders on screen.
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

// FirstFocusable returns the first focus slot Tab visits: the first focusable
// field, or the first button slot when no field is focusable. Hosts use the
// boundary slots to hand Tab traversal back to controls around the form.
// Tab does not wrap inside the form.
func (f Form) FirstFocusable() int {
	for i, item := range f.items {
		if item.Field.IsFocusable() {
			return i
		}
	}
	return len(f.items)
}

// LastFocusable returns the final focus slot in the Tab order: the Cancel
// button, which every form places last.
func (f Form) LastFocusable() int { return f.cancelIndex() }

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
// maxLabelLen+requiredPad so all rows share a column. The required
// "*" marker is rendered in its own style on required rows.
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
