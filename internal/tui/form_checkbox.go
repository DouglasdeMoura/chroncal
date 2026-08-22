package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
)

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
// highlight. Useful for warnings or hints that should not invert when
// the row is focused. The caller is responsible for any style.
func (f *CheckboxField) SetSuffix(v string) { f.suffix = v }

// AutoChecked reports whether the current checked state was set
// programmatically by the form rather than by the user. The form can
// then revert the state when the upstream condition changes.
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
