package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/stretchr/testify/assert"
)

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

func TestAccountDialogRendering_HTTPChecked(t *testing.T) {
	theme := Theme{}
	m := NewAccountDialogModel(theme).SetSize(120, 40)

	// Set a non-localhost URL and check the HTTP box.
	rebuild := func() {
		if m.form.onRebuild != nil {
			m.form.onRebuild(&m.form)
		}
	}
	m.form.Field(calDAVIdxServer).(*TextField).SetValue("https://cal.example.com/dav/")
	rebuild()
	m.form.Field(calDAVIdxAllowInsecure).(*CheckboxField).Toggle()
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
		ID:           7,
		AccountID:    3,
		AccountName:  "Work Account",
		Name:         "Work",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, theme).SetSize(120, 40)

	v := m.View()
	assert.Contains(t, v, "Account")
	assert.Contains(t, v, "Work Account ›")
	// Account calendars have no Delete: the footnote explains ownership and
	// points at the local alternative instead.
	assert.NotContains(t, v, "Delete Calendar…")
	assert.Contains(t, v, "lives in your Work Account account")
	assert.Contains(t, v, "Turn off Display calendar")
	assert.Contains(t, v, "Account › Manage Calendars")
	assert.NotContains(t, v, "Export Calendar…") // manager-only affordance
	assert.NotContains(t, v, "Manage Account…")
	assert.NotContains(t, v, "Disconnect…")
	assert.NotContains(t, v, "cal.example.com")
}
