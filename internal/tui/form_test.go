package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

// ---------------------------------------------------------------------------
// TextField tests
// ---------------------------------------------------------------------------

func TestTextField_SetAndGetValue(t *testing.T) {
	f := NewTextField("name")
	f.SetValue("hello")
	assert.Equal(t, "hello", f.Value())
}

func TestTextField_DigitsOnly(t *testing.T) {
	f := NewTextField("number")
	f.SetDigitsOnly()
	f.Focus()

	f.Update(keyPressMsg("5"))
	assert.Equal(t, "5", f.Value())

	f.Update(keyPressMsg("a"))
	assert.Equal(t, "5", f.Value(), "non-digit rejected")

	f.Update(keyPressMsg("3"))
	assert.Equal(t, "53", f.Value())
}

func TestFilterDigits_MultiRuneText(t *testing.T) {
	// FilterDigits must inspect every rune in a multi-character key event
	// (e.g. a paste delivered as a single KeyPressMsg). The old implementation
	// only checked k.Text[0] cast to rune, so a paste like "12ab" slipped
	// through because the leading '1' is a digit.

	tests := []struct {
		text    string
		wantVal string
		desc    string
	}{
		{"12ab", "", "leading digit followed by letters must be rejected"},
		{"1a2", "", "non-digit in the middle must be rejected"},
		{"123", "123", "all-digit paste must be accepted"},
		{"a12", "", "leading non-digit must be rejected"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			f := NewTextField("n")
			f.SetDigitsOnly()
			f.Focus()
			f.Update(keyPressMsg(tc.text))
			assert.Equal(t, tc.wantVal, f.Value(), tc.desc)
		})
	}
}

func TestTextField_PasteIsFiltered(t *testing.T) {
	// A bracketed paste arrives as tea.PasteMsg, not tea.KeyPressMsg. The
	// filter must apply to it too, otherwise pasted text bypasses the filter
	// entirely (issue #411).
	tests := []struct {
		content string
		wantVal string
		desc    string
	}{
		{"123", "123", "all-digit paste accepted"},
		{"12ab", "", "paste with letters rejected wholesale"},
		{"xyz", "", "non-digit paste rejected"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			f := NewTextField("n")
			f.SetDigitsOnly()
			f.Focus()
			f.Update(tea.PasteMsg{Content: tc.content})
			assert.Equal(t, tc.wantVal, f.Value(), tc.desc)
		})
	}
}

func TestHexColorField_PasteIsFiltered(t *testing.T) {
	// The hex-color field must reject pasted non-hex strings, not just
	// rejected keystrokes (issue #411).
	f := NewHexColorField("#rrggbb", lipgloss.Color("8"))
	f.Focus()

	f.Update(tea.PasteMsg{Content: "xyz"})
	assert.Equal(t, "", f.Value(), "non-hex paste rejected")

	f.Update(tea.PasteMsg{Content: "#aabbcc"})
	assert.Equal(t, "#aabbcc", f.Value(), "valid hex paste accepted")
}

func TestTextField_CustomFilter(t *testing.T) {
	f := NewTextField("hex")
	f.SetFilter(func(k tea.Key) bool {
		if k.Text == "" {
			return true
		}
		r := rune(k.Text[0])
		return (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
	})
	f.Focus()

	f.Update(keyPressMsg("a"))
	assert.Equal(t, "a", f.Value())

	f.Update(keyPressMsg("g"))
	assert.Equal(t, "a", f.Value(), "non-hex rejected")

	f.Update(keyPressMsg("3"))
	assert.Equal(t, "a3", f.Value())
}

func TestTextField_NoFilterAcceptsAll(t *testing.T) {
	f := NewTextField("any")
	f.Focus()

	f.Update(keyPressMsg("h"))
	f.Update(keyPressMsg("i"))
	assert.Equal(t, "hi", f.Value())
}

func TestTextField_CharLimit(t *testing.T) {
	f := NewTextField("short")
	f.SetCharLimit(3)
	f.Focus()

	f.Update(keyPressMsg("a"))
	f.Update(keyPressMsg("b"))
	f.Update(keyPressMsg("c"))
	f.Update(keyPressMsg("d"))
	assert.Equal(t, "abc", f.Value())
}

func TestTextField_IsFocusable(t *testing.T) {
	f := NewTextField("test")
	assert.True(t, f.IsFocusable())
}

func TestTextField_ImplementsFormField(t *testing.T) {
	var _ FormField = NewTextField("test")
}

// ---------------------------------------------------------------------------
// TextAreaField tests
// ---------------------------------------------------------------------------

func TestTextAreaField_SetAndGetValue(t *testing.T) {
	f := NewTextAreaField("desc")
	f.SetValue("hello\nworld")
	assert.Equal(t, "hello\nworld", f.Value())
}

func TestTextAreaField_IsFocusable(t *testing.T) {
	f := NewTextAreaField("desc")
	assert.True(t, f.IsFocusable())
}

func TestTextAreaField_ImplementsFormField(t *testing.T) {
	var _ FormField = NewTextAreaField("test")
}

// ---------------------------------------------------------------------------
// SelectField tests
// ---------------------------------------------------------------------------

func testSelectOptions() []SelectOption {
	return []SelectOption{
		{Label: "None", Value: ""},
		{Label: "Daily", Value: "daily"},
		{Label: "Weekly", Value: "weekly"},
	}
}

func TestSelectField_DefaultSelection(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	assert.Equal(t, 0, f.Selected())
	assert.Equal(t, "None", f.SelectedOption().Label)
	assert.Equal(t, "", f.Value())
}

func TestSelectField_CycleRight(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	f.Update(keyPressMsg("right"))
	assert.Equal(t, 1, f.Selected())
	assert.Equal(t, "daily", f.Value())

	f.Update(keyPressMsg("right"))
	assert.Equal(t, 2, f.Selected())

	// Wraps around
	f.Update(keyPressMsg("right"))
	assert.Equal(t, 0, f.Selected())
}

func TestSelectField_CycleLeft(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	// Wraps backward from first
	f.Update(keyPressMsg("left"))
	assert.Equal(t, 2, f.Selected())

	f.Update(keyPressMsg("left"))
	assert.Equal(t, 1, f.Selected())
}

func TestSelectField_VimKeys(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	f.Update(keyPressMsg("l"))
	assert.Equal(t, 1, f.Selected())

	f.Update(keyPressMsg("h"))
	assert.Equal(t, 0, f.Selected())
}

func TestSelectField_SetSelected(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	f.SetSelected(2)
	assert.Equal(t, "weekly", f.Value())
}

func TestSelectField_ViewAlwaysShowsArrows(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	view := f.View()
	assert.Contains(t, view, Glyphs["select.prev"], "arrows visible when unfocused")
	assert.Contains(t, view, Glyphs["select.next"], "arrows visible when unfocused")

	f.Focus()
	view = f.View()
	assert.Contains(t, view, Glyphs["select.prev"], "arrows visible when focused")
	assert.Contains(t, view, Glyphs["select.next"], "arrows visible when focused")
}

func TestSelectField_CustomRenderLabel(t *testing.T) {
	f := NewSelectField(testSelectOptions())
	f.SetRenderLabel(func(opt SelectOption, focused bool) string {
		if focused {
			return "<" + opt.Label + ">"
		}
		return "[" + opt.Label + "]"
	})

	view := f.View()
	assert.Contains(t, view, "[None]")

	f.Update(keyPressMsg("right"))
	view = f.View()
	assert.Contains(t, view, "[Daily]")

	f.Focus()
	view = f.View()
	assert.Contains(t, view, "<Daily>")
}

func TestSelectField_ImplementsFormField(t *testing.T) {
	var _ FormField = NewSelectField(testSelectOptions())
}

func TestSelectField_EmptyOptionsNoPanic(t *testing.T) {
	f := NewSelectField(nil)

	// Arrow/vim input on an empty option set must not panic
	// (would divide by zero via (selected +/- 1 + n) % n).
	assert.NotPanics(t, func() {
		f.Update(keyPressMsg("right"))
		f.Update(keyPressMsg("left"))
		f.Update(keyPressMsg("l"))
		f.Update(keyPressMsg("h"))
	})
	assert.Equal(t, 0, f.Selected())

	// Value()/SelectedOption() must not panic on an empty option set
	// (would index f.options[f.selected] out of range).
	assert.NotPanics(t, func() {
		assert.Equal(t, "", f.Value())
		assert.Equal(t, SelectOption{}, f.SelectedOption())
	})

	// An empty select must not capture focus or input.
	assert.False(t, f.IsFocusable())
}

// ---------------------------------------------------------------------------
// CheckboxField tests
// ---------------------------------------------------------------------------

func TestCheckboxField_Toggle(t *testing.T) {
	f := NewCheckboxField("Enable", false)
	assert.False(t, f.Checked())

	f.Update(keyPressMsg("space"))
	assert.True(t, f.Checked())

	f.Update(keyPressMsg("space"))
	assert.False(t, f.Checked())
}

func TestCheckboxField_SetChecked(t *testing.T) {
	f := NewCheckboxField("Enable", false)
	f.SetChecked(true)
	assert.True(t, f.Checked())
}

func TestCheckboxField_IgnoresNonSpaceKeys(t *testing.T) {
	f := NewCheckboxField("Test", false)
	f.Update(keyPressMsg("a"))
	assert.False(t, f.Checked())

	f.Update(keyPressMsg("enter"))
	assert.False(t, f.Checked())
}

func TestCheckboxField_IsFocusable(t *testing.T) {
	f := NewCheckboxField("Test", false)
	assert.True(t, f.IsFocusable())
}

func TestCheckboxField_ImplementsFormField(t *testing.T) {
	var _ FormField = NewCheckboxField("test", false)
}

// ---------------------------------------------------------------------------
// StaticField tests
// ---------------------------------------------------------------------------

func TestStaticField_ValueAndView(t *testing.T) {
	f := NewStaticField("hello", strings.ToUpper)
	assert.Equal(t, "hello", f.Value())
	assert.Equal(t, "HELLO", f.View())
}

func TestStaticField_SetValue(t *testing.T) {
	f := NewStaticField("old", nil)
	f.SetValue("new")
	assert.Equal(t, "new", f.Value())
	assert.Equal(t, "new", f.View())
}

func TestStaticField_NilStyleFn(t *testing.T) {
	f := NewStaticField("plain", nil)
	assert.Equal(t, "plain", f.View())
}

func TestStaticField_IsNotFocusable(t *testing.T) {
	f := NewStaticField("x", nil)
	assert.False(t, f.IsFocusable())
}

func TestStaticField_UpdateIsNoop(t *testing.T) {
	f := NewStaticField("x", nil)
	cmd := f.Update(keyPressMsg("a"))
	assert.Nil(t, cmd)
	assert.Equal(t, "x", f.Value(), "value unchanged after Update")
}

func TestStaticField_ImplementsFormField(t *testing.T) {
	var _ FormField = NewStaticField("test", nil)
}

// ---------------------------------------------------------------------------
// Form tests
// ---------------------------------------------------------------------------

func newTestForm(items ...FormItem) Form {
	return NewForm("Submit", DefaultFormStyles(), items...)
}

func TestForm_FocusCycling(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)
	assert.Equal(t, 0, form.Focused())

	formPressTab(&form)
	assert.Equal(t, 1, form.Focused())

	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "submit button")

	formPressTab(&form)
	assert.Equal(t, 3, form.Focused(), "cancel button")

	formPressTab(&form)
	assert.Equal(t, 0, form.Focused(), "wraps to first field")
}

func TestForm_FocusedLine(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)

	// Focus on a field returns that field's first body line.
	assert.Equal(t, 0, form.FocusedLine(), "first field")
	formPressTab(&form)
	assert.Greater(t, form.FocusedLine(), 0, "second field is below the first")

	// Focus on the Submit/Cancel button row is not a body field, so
	// FocusedLine must not point back into the body (regression #408:
	// returning 0 scrolled the body to the top when Tab reached the buttons).
	formPressTab(&form)
	assert.Equal(t, form.submitIndex(), form.Focused(), "submit button")
	assert.Less(t, form.FocusedLine(), 0, "submit button is not a body field")

	formPressTab(&form)
	assert.Equal(t, form.cancelIndex(), form.Focused(), "cancel button")
	assert.Less(t, form.FocusedLine(), 0, "cancel button is not a body field")
}

func TestForm_ShiftTabCycling(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)

	formPressShiftTab(&form)
	assert.Equal(t, 3, form.Focused(), "cancel button")

	formPressShiftTab(&form)
	assert.Equal(t, 2, form.Focused(), "submit button")

	formPressShiftTab(&form)
	assert.Equal(t, 1, form.Focused())

	formPressShiftTab(&form)
	assert.Equal(t, 0, form.Focused())
}

func TestForm_EnterAdvancesFocus(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)

	formPressEnter(&form)
	assert.Equal(t, 1, form.Focused())

	formPressEnter(&form)
	assert.Equal(t, 2, form.Focused(), "submit button")
}

func TestForm_SubmitAction(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formPressTab(&form)
	assert.Equal(t, 1, form.Focused(), "submit button")

	form, _ = form.Update(keyPressMsg("enter"))
	assert.True(t, submitted)
}

func TestForm_CancelAction(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	cancelled := false
	form.OnCancel(func(f *Form) tea.Cmd {
		cancelled = true
		return nil
	})

	formPressTab(&form)
	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "cancel button")

	form, _ = form.Update(keyPressMsg("enter"))
	assert.True(t, cancelled)
}

func TestForm_TabVisitsLeadingActionsBeforeSubmit(t *testing.T) {
	// Layout (visual reading order, left-to-right):
	//   [Set as Default] [Disconnect]   Field   [Save] [Test] [Cancel]
	// Tab order should mirror that: field → Set as Default → Disconnect →
	// Save → Test → Cancel, then wrap.
	form := newTestForm(FormItem{Label: "Field", Field: NewTextField("v")})
	form.SetLeadingActionButton("Set as Default", Button, func() tea.Msg { return nil })
	form.SetLeadingActionButton("Disconnect", ButtonDanger, func() tea.Msg { return nil })
	form.SetActionButton("Test", Button, func() tea.Msg { return nil })

	assert.Equal(t, 0, form.Focused(), "field")
	formPressTab(&form)
	assert.Equal(t, 1, form.Focused(), "Set as Default (first leading)")
	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "Disconnect (second leading)")
	formPressTab(&form)
	assert.Equal(t, 3, form.Focused(), "Save (submit)")
	formPressTab(&form)
	assert.Equal(t, 4, form.Focused(), "Test (trailing)")
	formPressTab(&form)
	assert.Equal(t, 5, form.Focused(), "Cancel")
	formPressTab(&form)
	assert.Equal(t, 0, form.Focused(), "wraps to field")
}

func TestForm_ShiftTabReversesLeadingActions(t *testing.T) {
	form := newTestForm(FormItem{Label: "Field", Field: NewTextField("v")})
	form.SetLeadingActionButton("Set as Default", Button, func() tea.Msg { return nil })
	form.SetLeadingActionButton("Disconnect", ButtonDanger, func() tea.Msg { return nil })

	// Cancel is last; Shift+Tab walks back through Save → Disconnect →
	// Set as Default → Field, matching visual reading order in reverse.
	formPressShiftTab(&form)
	assert.Equal(t, 4, form.Focused(), "Cancel")
	formPressShiftTab(&form)
	assert.Equal(t, 3, form.Focused(), "Save")
	formPressShiftTab(&form)
	assert.Equal(t, 2, form.Focused(), "Disconnect")
	formPressShiftTab(&form)
	assert.Equal(t, 1, form.Focused(), "Set as Default")
	formPressShiftTab(&form)
	assert.Equal(t, 0, form.Focused(), "Field")
}

func TestForm_NoFields(t *testing.T) {
	form := newTestForm()
	assert.Equal(t, 0, form.Focused(), "submit button")

	formPressTab(&form)
	assert.Equal(t, 1, form.Focused(), "cancel button")

	formPressTab(&form)
	assert.Equal(t, 0, form.Focused(), "wraps to submit")
}

func TestForm_SkipsStaticFields(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Info", Field: NewStaticField("read-only", nil)},
		FormItem{Label: "Email", Field: NewTextField("email")},
	)
	assert.Equal(t, 0, form.Focused())

	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "skips static field to Email")
}

func TestForm_ShiftTabSkipsStaticFieldsBackward(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Info", Field: NewStaticField("read-only", nil)},
		FormItem{Label: "Email", Field: NewTextField("email")},
	)

	// Tab to Email (skips static), then shift-tab back to Name (skips static).
	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "Email")

	formPressShiftTab(&form)
	assert.Equal(t, 0, form.Focused(), "back to Name, skipping static")
}

func TestForm_FieldValuesAccessible(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
	)

	formTypeText(&form, "hello")
	assert.Equal(t, "hello", form.FormTextField(0).Value())
}

func TestForm_ValidationBlocksSubmitWhenRequiredEmpty(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.False(t, submitted)
	assert.True(t, form.HasError())
	assert.Equal(t, "Name is required", form.Error())
}

func TestForm_ValidationAllowsSubmitWhenRequiredFilled(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formTypeText(&form, "hello")
	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.True(t, submitted)
	assert.False(t, form.HasError())
}

func TestForm_ValidationTreatsWhitespaceAsEmpty(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formTypeText(&form, "   ")
	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.False(t, submitted)
	assert.Equal(t, "Name is required", form.Error())
}

func TestForm_ValidationErrorClearsOnInput(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	form.OnSubmit(func(f *Form) tea.Cmd { return nil })

	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.True(t, form.HasError())
	assert.Equal(t, 0, form.Focused())

	formTypeText(&form, "x")

	assert.False(t, form.HasError())
}

func TestForm_ValidationNonRequiredDoesNotBlock(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Optional", Field: NewTextField("opt")},
		FormItem{Label: "Required", Field: NewTextField("req"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formPressTab(&form)
	formTypeText(&form, "filled")
	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.True(t, submitted)
	assert.False(t, form.HasError())
}

func TestForm_ValidationFocusesFirstError(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second"), Required: true},
		FormItem{Label: "Third", Field: NewTextField("third"), Required: true},
	)
	form.OnSubmit(func(f *Form) tea.Cmd { return nil })

	formFocusSubmit(&form)
	formPressEnter(&form)

	assert.Equal(t, 1, form.Focused(), "focused on first errored field")
	assert.Equal(t, "Second is required", form.Error())
}

func TestForm_FocusedLineAccountsForErrorLine(t *testing.T) {
	// With LabelTop each field row is two lines tall; a validation error
	// inserts an extra one-line part after its field's row. FocusedLine must
	// count items (not parts) so the error line shifts later fields down by
	// exactly one rather than letting the part index run ahead of the item
	// index.
	newForm := func() Form {
		return newTestForm(
			FormItem{Label: "First", Field: NewTextField("first")},
			FormItem{Label: "Second", Field: NewTextField("second")},
			FormItem{Label: "Third", Field: NewTextField("third")},
		)
	}

	noError := newForm()
	noError.focused = 2
	assert.Equal(t, 4, noError.FocusedLine(),
		"two field rows of height 2 precede the third field")

	withError := newForm()
	withError.SetError(0, "bad value")
	withError.focused = 2
	assert.Equal(t, 5, withError.FocusedLine(),
		"the error line below the first field shifts the third field down one")
}

func TestForm_OnRebuildCalledOnKeyPress(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	called := false
	form.OnRebuild(func(f *Form) { called = true })

	formTypeText(&form, "x")
	assert.True(t, called)
}

func TestForm_OnRebuildNotCalledOnTab(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)
	called := false
	form.OnRebuild(func(f *Form) { called = true })

	formPressTab(&form)
	assert.False(t, called)
}

func TestForm_OnRebuildNotCalledOnWindowSize(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	called := false
	form.OnRebuild(func(f *Form) { called = true })

	form, _ = form.Update(tea.WindowSizeMsg{Width: 80, Height: 40})
	assert.False(t, called)
}

func TestForm_AppendItems(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
	)
	assert.Equal(t, 1, form.ItemCount())

	form.AppendItems(
		FormItem{Label: "Second", Field: NewTextField("second")},
		FormItem{Label: "Third", Field: NewTextField("third")},
	)
	assert.Equal(t, 3, form.ItemCount())
	assert.Equal(t, "", form.FormTextField(1).Value())
	assert.Equal(t, "", form.FormTextField(2).Value())
}

func TestForm_AppendItemsFocusStability(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)
	formPressTab(&form)
	assert.Equal(t, 1, form.Focused())

	form.AppendItems(FormItem{Label: "Third", Field: NewTextField("third")})
	assert.Equal(t, 1, form.Focused(), "focus unchanged after append")
}

func TestForm_RemoveItems(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
		FormItem{Label: "Third", Field: NewTextField("third")},
	)
	form.RemoveItems(1)
	assert.Equal(t, 1, form.ItemCount())
}

func TestForm_ActionButton(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	var actionFired bool
	form.SetActionButton("Delete", ButtonDanger, func() tea.Msg {
		actionFired = true
		return nil
	})

	formPressTab(&form)
	assert.Equal(t, 1, form.Focused(), "submit")

	formPressTab(&form)
	assert.Equal(t, 2, form.Focused(), "action")

	formPressTab(&form)
	assert.Equal(t, 3, form.Focused(), "cancel")

	formPressShiftTab(&form)
	assert.Equal(t, 2, form.Focused(), "action")

	form, cmd := form.Update(keyPressMsg("enter"))
	if cmd != nil {
		cmd()
	}
	assert.True(t, actionFired)
}

func TestForm_CheckboxFieldAccessor(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Toggle", Field: NewCheckboxField("Enable", false)},
	)
	assert.False(t, form.FormCheckboxField(0).Checked())

	form, _ = form.Update(keyPressMsg("space"))
	assert.True(t, form.FormCheckboxField(0).Checked())
}

// ---------------------------------------------------------------------------
// Form label placement tests
// ---------------------------------------------------------------------------

func TestForm_LabelTopByDefault(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
	)
	view := form.View()
	// With LabelTop (the default) the label and field are on separate lines.
	assert.True(t, labelAndFieldOnSeparateLines(view, "Name"),
		"label should be on its own line above the field")
}

func TestForm_LabelLeftAtFormLevel(t *testing.T) {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline
	form := NewForm("Submit", styles,
		FormItem{Label: "Name", Field: NewTextField("name")},
	)
	view := form.View()
	assert.False(t, labelAndFieldOnSeparateLines(view, "Name"),
		"label should be inline with the field")
}

func TestForm_LabelPlacementPerItemOverride(t *testing.T) {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelTop
	form := NewForm("Submit", styles,
		FormItem{Label: "Top", Field: NewTextField("a")},
		FormItem{Label: "Left", Field: NewTextField("b"), LabelLayout: LayoutPtr(LabelInline)},
	)
	view := form.View()
	assert.True(t, labelAndFieldOnSeparateLines(view, "Top"),
		"first field uses form default (top)")
	assert.False(t, labelAndFieldOnSeparateLines(view, "Left"),
		"second field overrides to inline")
}

func TestForm_LabelLeftAlignment(t *testing.T) {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInline

	form := NewForm("Submit", styles,
		FormItem{Label: "N", Field: NewTextField("a")},     // 1 char
		FormItem{Label: "Email", Field: NewTextField("b")}, // 5 chars (longest)
	)

	// Both left-placed labels should be padded to the longest label width,
	// with a 1-column space between label and field.
	shortLabel := styles.Label.Width(5).Render("N")
	longLabel := styles.Label.Width(5).Render("Email")
	assert.Equal(t, lipgloss.Width(shortLabel), lipgloss.Width(longLabel),
		"labels should have equal rendered width")

	// Sweep markers so we can check for the rendered label text.
	view := mouseSweep(form.View())
	assert.Contains(t, view, shortLabel+" ")
	assert.Contains(t, view, longLabel+" ")
}

func TestForm_LabelInlineRight(t *testing.T) {
	styles := DefaultFormStyles()
	styles.LabelLayout = LabelInlineRight

	form := NewForm("Submit", styles,
		FormItem{Label: "N", Field: NewTextField("a")},
		FormItem{Label: "Email", Field: NewTextField("b")},
	)

	view := mouseSweep(form.View())

	// "N" right-aligned in a 5-wide column: "    N", then " " gap before field.
	shortLabel := styles.Label.Width(5).Align(lipgloss.Right).Render("N")
	assert.Contains(t, view, shortLabel+" ",
		"short label should be right-aligned with 1-col gap")

	// "Email" fills the column exactly: "Email", then " " gap.
	longLabel := styles.Label.Width(5).Align(lipgloss.Right).Render("Email")
	assert.Contains(t, view, longLabel+" ",
		"longest label should start at column 0 with 1-col gap")
}

// ---------------------------------------------------------------------------
// Form focus indicator tests
// ---------------------------------------------------------------------------

func TestForm_FocusIndicatorOnFocusedField(t *testing.T) {
	styles := DefaultFormStyles()
	styles.ShowFocusMarker = true

	form := NewForm("Submit", styles,
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Email", Field: NewTextField("email")},
	)

	view := form.View()
	styledMarker := styles.Label.Render(Glyphs["focus"]) + " "
	assert.Contains(t, view, styledMarker, "focused field should show > marker")
}

func TestForm_FocusIndicatorMovesWithFocus(t *testing.T) {
	styles := DefaultFormStyles()
	styles.ShowFocusMarker = true
	styles.LabelLayout = LabelInline

	form := NewForm("Submit", styles,
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "Email", Field: NewTextField("email")},
	)

	styledMarker := styles.Label.Render(Glyphs["focus"]) + " "

	view := form.View()
	assert.Equal(t, 1, strings.Count(view, styledMarker),
		"exactly one field should have the focus marker")

	formPressTab(&form)
	view = form.View()
	assert.Equal(t, 1, strings.Count(view, styledMarker),
		"exactly one field should have the focus marker after tab")
}

func TestForm_NoFocusIndicatorByDefault(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
	)
	view := form.View()
	styledMarker := DefaultFormStyles().Label.Render(Glyphs["focus"]) + " "
	assert.NotContains(t, view, styledMarker,
		"no focus marker when ShowFocusMarker is false")
}

// labelAndFieldOnSeparateLines returns true when the line containing the
// label text does NOT also contain field content. Field content carries
// either the textinput reverse-video cursor (\x1b[7;37m) or the
// italic+faint placeholder combo (\x1b[3;2m). Labels now render in 240
// without italic/faint, so those markers uniquely identify a field.
func labelAndFieldOnSeparateLines(view, label string) bool {
	clean := mouseSweep(view)
	for _, line := range strings.Split(clean, "\n") {
		if strings.Contains(line, label) {
			return !strings.Contains(line, "\x1b[7;37m") && !strings.Contains(line, "\x1b[3;2m")
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// Form mouse tests
// ---------------------------------------------------------------------------

func TestForm_ClickSubmitValidates(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formClickTarget(&form, "submit")

	assert.False(t, submitted)
	assert.True(t, form.HasError())
}

func TestForm_ClickSubmitSucceeds(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name"), Required: true},
	)
	submitted := false
	form.OnSubmit(func(f *Form) tea.Cmd {
		submitted = true
		return nil
	})

	formTypeText(&form, "hello")
	formClickTarget(&form, "submit")

	assert.True(t, submitted)
	assert.False(t, form.HasError())
}

func TestForm_ClickCancel(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	cancelled := false
	form.OnCancel(func(f *Form) tea.Cmd {
		cancelled = true
		return nil
	})

	formClickTarget(&form, "cancel")
	assert.True(t, cancelled)
}

func TestForm_ClickField(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "First", Field: NewTextField("first")},
		FormItem{Label: "Second", Field: NewTextField("second")},
	)
	assert.Equal(t, 0, form.Focused())

	formClickTarget(&form, "field:1")
	assert.Equal(t, 1, form.Focused())
}

func TestForm_ClickCheckboxToggles(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Toggle", Field: NewCheckboxField("Enable", false)},
	)
	assert.False(t, form.FormCheckboxField(0).Checked())

	formClickTarget(&form, "field:0")
	assert.True(t, form.FormCheckboxField(0).Checked())
}

func TestForm_ClickActionButton(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	var actionFired bool
	form.SetActionButton("Delete", ButtonDanger, func() tea.Msg {
		actionFired = true
		return nil
	})

	form, cmd := form.Update(MouseEvent{IsClick: true, Target: "action:0"})
	if cmd != nil {
		cmd()
	}
	assert.True(t, actionFired)
}

func TestForm_ClickEmptyTargetIsNoop(t *testing.T) {
	form := newTestForm(
		FormItem{Label: "Field", Field: NewTextField("val")},
	)
	before := form.Focused()
	formClickTarget(&form, "")
	assert.Equal(t, before, form.Focused())
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

func keyPressMsg(s string) tea.KeyPressMsg {
	k := tea.Key{Text: s}
	if r := []rune(s); len(r) == 1 {
		k.Code = r[0]
	}
	return tea.KeyPressMsg(k)
}

func updateForm(f Form, msg tea.Msg) Form {
	f, _ = f.Update(msg)
	return f
}

func formPressTab(form *Form) {
	*form = updateForm(*form, keyPressMsg("tab"))
}

func formPressShiftTab(form *Form) {
	*form = updateForm(*form, keyPressMsg("shift+tab"))
}

func formPressEnter(form *Form) {
	*form = updateForm(*form, keyPressMsg("enter"))
}

func formTypeText(form *Form, text string) {
	for _, r := range text {
		*form = updateForm(*form, keyPressMsg(string(r)))
	}
}

func formFocusSubmit(form *Form) {
	for form.Focused() != form.submitIndex() {
		formPressTab(form)
	}
}

func formClickTarget(form *Form, target string) {
	*form = updateForm(*form, MouseEvent{IsClick: true, Target: target})
}

func TestCalendarDialogRendering(t *testing.T) {
	theme := Theme{}
	m := NewCalendarDialogModel(CalendarDialogParams{Color: "#a6e3a1"}, theme)
	m = m.SetSize(120, 40)

	v := m.View()
	assert.NotEmpty(t, v)

	// All form lines must fit within the dialog content width.
	fv := m.form.View()
	cw := m.dialog.ContentWidth()
	for i, l := range strings.Split(fv, "\n") {
		w := lipgloss.Width(l)
		assert.LessOrEqual(t, w, cw, "form line %d is %d cols, exceeds content width %d", i, w, cw)
	}
}

func TestCalendarDialogRendering_SyncOnHTTPChecked(t *testing.T) {
	theme := Theme{}
	m := NewCalendarDialogModel(CalendarDialogParams{Color: "#a6e3a1"}, theme)
	m = m.SetSize(120, 40)

	// Enable Sync, set a non-localhost URL, check the HTTP box.
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}
	m.form.Field(cdIdxSync).(*CheckboxField).Toggle()
	rebuild()
	m.form.Field(cdIdxRemoteURL).(*TextField).SetValue("https://cal.example.com/dav/")
	rebuild()
	m.form.Field(cdIdxAllowInsecure).(*CheckboxField).Toggle()
	rebuild()

	assert.Contains(t, m.form.View(), "unencrypted", "warning appears when checked")

	cw := m.dialog.ContentWidth()
	for i, l := range strings.Split(m.form.View(), "\n") {
		w := lipgloss.Width(l)
		assert.LessOrEqual(t, w, cw, "form line %d is %d cols, exceeds content width %d", i, w, cw)
	}
}

func TestCalendarDialogRendering_EditLinked(t *testing.T) {
	theme := Theme{}
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             7,
		Name:           "Work",
		Color:          "#a6e3a1",
		RemoteURL:      "https://cal.example.com/dav/calendars/work/",
		RemoteLinked:   true,
		RemoteAuthType: "basic",
		RemoteUsername: "alice",
	}, theme)
	m = m.SetSize(120, 40)

	v := m.View()
	assert.Contains(t, v, "Disconnect")
	assert.Contains(t, v, "cal.example.com")
}
