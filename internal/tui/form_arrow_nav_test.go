package tui

import (
	"testing"
)

// The vertical arrow keys move between the rows of a form, next to tab and
// shift+tab. The rows of a dialog stack vertically, so a vertical key is the
// fast way between them (issue #627).
func TestForm_ArrowKeysMoveBetweenFields(t *testing.T) {
	f := newTestForm(
		FormItem{Label: "Name", Field: NewTextField("name")},
		FormItem{Label: "URL", Field: NewTextField("url")},
	)
	if got := f.Focused(); got != 0 {
		t.Fatalf("initial focus = %d, want 0", got)
	}

	f = updateForm(f, keyPressMsg("down"))
	if got := f.Focused(); got != 1 {
		t.Errorf("focus after down = %d, want 1", got)
	}
	f = updateForm(f, keyPressMsg("up"))
	if got := f.Focused(); got != 0 {
		t.Errorf("focus after up = %d, want 0", got)
	}
}

// Down from the last field reaches the buttons, exactly like tab.
func TestForm_ArrowDownReachesTheButtons(t *testing.T) {
	f := newTestForm(FormItem{Label: "Name", Field: NewTextField("name")})

	f = updateForm(f, keyPressMsg("down"))
	if got, want := f.Focused(), f.submitIndex(); got != want {
		t.Errorf("focus after down = %d, want the submit button at %d", got, want)
	}
	f = updateForm(f, keyPressMsg("down"))
	if got, want := f.Focused(), f.cancelIndex(); got != want {
		t.Errorf("focus after a second down = %d, want the cancel button at %d", got, want)
	}
}

// A text area holds more than one line, so it keeps the vertical keys for
// its own cursor. The focus stays on the area. Tab still leaves it.
func TestForm_ArrowKeysStayInATextArea(t *testing.T) {
	body := NewTextAreaField("body")
	f := newTestForm(
		FormItem{Label: "Body", Field: body},
		FormItem{Label: "Name", Field: NewTextField("name")},
	)

	f = updateForm(f, keyPressMsg("down"))
	if got := f.Focused(); got != 0 {
		t.Errorf("focus after down in a text area = %d, want 0", got)
	}
	formPressTab(&f)
	if got := f.Focused(); got != 1 {
		t.Errorf("focus after tab in a text area = %d, want 1", got)
	}
}

// The horizontal arrow keys belong to the field. A select cycles its options
// with left and right, so the focus must not move.
func TestForm_HorizontalArrowsStayInAField(t *testing.T) {
	sel := NewSelectField(testSelectOptions())
	f := newTestForm(
		FormItem{Label: "Repeat", Field: sel},
		FormItem{Label: "Name", Field: NewTextField("name")},
	)

	f = updateForm(f, keyPressMsg("right"))
	if got := f.Focused(); got != 0 {
		t.Errorf("focus after right on a select = %d, want 0", got)
	}
	if got := sel.Selected(); got != 1 {
		t.Errorf("selected option after right = %d, want 1", got)
	}
	f = updateForm(f, keyPressMsg("left"))
	if got := f.Focused(); got != 0 {
		t.Errorf("focus after left on a select = %d, want 0", got)
	}
}

// A button slot keeps the old behaviour: every arrow key moves the focus.
func TestForm_ArrowKeysMoveBetweenButtons(t *testing.T) {
	f := newTestForm(FormItem{Label: "Name", Field: NewTextField("name")})
	formFocusSubmit(&f)

	f = updateForm(f, keyPressMsg("right"))
	if got, want := f.Focused(), f.cancelIndex(); got != want {
		t.Errorf("focus after right on a button = %d, want %d", got, want)
	}
	f = updateForm(f, keyPressMsg("left"))
	if got, want := f.Focused(), f.submitIndex(); got != want {
		t.Errorf("focus after left on a button = %d, want %d", got, want)
	}
}
