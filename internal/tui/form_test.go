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

func TestCheckboxField_Render(t *testing.T) {
	f := NewCheckboxField("TLS", true)
	assert.Equal(t, Glyphs["checkbox.on"] + " TLS", f.View())

	f.Update(keyPressMsg("space"))
	assert.Equal(t, Glyphs["checkbox.off"] + " TLS", f.View())
}

func TestCheckboxField_DisabledWhen(t *testing.T) {
	disabled := true
	f := NewCheckboxField("TLS", false)
	f.SetDisabledWhen(func() (bool, string) {
		return disabled, "Not available"
	})

	f.Update(keyPressMsg("space"))
	assert.False(t, f.Checked(), "toggle ignored when disabled")
	assert.Equal(t, "Not available", f.View())

	disabled = false
	f.Update(keyPressMsg("space"))
	assert.True(t, f.Checked(), "toggle works when enabled")
	assert.Equal(t, Glyphs["checkbox.on"] + " TLS", f.View())
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
	form.SetActionButton("Delete", func() tea.Msg {
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
		FormItem{Label: "N", Field: NewTextField("a")},       // 1 char
		FormItem{Label: "Email", Field: NewTextField("b")},   // 5 chars (longest)
	)

	// Both left-placed labels should be padded to the longest label width,
	// with a 1-column space between label and field.
	shortLabel := styles.Label.Width(5).Render("N")
	longLabel := styles.Label.Width(5).Render("Email")
	assert.Equal(t, lipgloss.Width(shortLabel), lipgloss.Width(longLabel),
		"labels should have equal rendered width")

	view := form.View()
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

	view := form.View()

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
// label text does NOT also contain a mouse marker (meaning the field is
// on the next line). Mouse markers (\x1b[Nz) are only emitted around
// field views, never around labels.
func labelAndFieldOnSeparateLines(view, label string) bool {
	for _, line := range strings.Split(view, "\n") {
		if strings.Contains(line, label) {
			return !strings.Contains(line, "z") || !strings.Contains(line, "\x1b[")
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
	form.SetActionButton("Delete", func() tea.Msg {
		actionFired = true
		return nil
	})

	form, cmd := form.Update(MouseEvent{IsClick: true, Target: "action"})
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
