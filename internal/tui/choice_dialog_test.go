package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestChoiceDialogSetSizeUsesFixedWidth(t *testing.T) {
	m := NewChoiceDialogModel(
		`Delete "Team Standup"?`,
		ActiveTheme(),
		"This event", "This and following", "All events",
	).SetSize(120, 40)

	w, _ := m.BoxSize()
	if w != choiceDialogMaxWidth {
		t.Fatalf("dialog width = %d, want %d", w, choiceDialogMaxWidth)
	}
}

func TestChoiceDialogSetSizeCapsToViewport(t *testing.T) {
	m := NewChoiceDialogModel(
		`Delete "Team Standup"?`,
		ActiveTheme(),
		"This event", "This and following", "All events",
	).SetSize(50, 20)

	w, _ := m.BoxSize()
	if w != 50 {
		t.Fatalf("dialog width = %d, want viewport width 50", w)
	}
}

func TestChoiceDialogCentersMessage(t *testing.T) {
	message := `Delete "Team Standup"?`
	m := NewChoiceDialogModel(
		message,
		ActiveTheme(),
		"This event", "This and following", "All events",
	).SetSize(50, 20)

	want := lipgloss.NewStyle().Width(m.dialog.ContentWidth()).Align(lipgloss.Center).Render(message)
	if !strings.Contains(m.View(), want) {
		t.Fatalf("dialog view did not contain centered message %q\nview:\n%s", want, m.View())
	}
}

func TestChoiceDialogFocusesFirstButton(t *testing.T) {
	m := NewChoiceDialogModel(
		`Delete "Team Standup"?`,
		ActiveTheme(),
		"This event", "This and following", "All events",
	)

	if got, want := m.form.Focused(), m.form.submitIndex(); got != want {
		t.Fatalf("focused control = %d, want submit button index %d", got, want)
	}
}

func TestChoiceDialogQClosesAsCancel(t *testing.T) {
	// Choice dialogs are button-only — no text input — so vim-style `q`
	// is safe to bind as a cancel. This guards against a regression
	// where someone removes the q binding thinking it could swallow
	// a real typed character.
	m := NewChoiceDialogModel("Pick one", ActiveTheme(), "A", "B")
	_, cmd := m.Update(keyPressMsg("q"))
	if cmd == nil {
		t.Fatal("q produced no command, want cancel")
	}
	msg, ok := cmd().(ChoiceDialogResultMsg)
	if !ok {
		t.Fatalf("q produced %T, want ChoiceDialogResultMsg", cmd())
	}
	if msg.Choice != -1 {
		t.Errorf("q.Choice = %d, want -1 (cancel)", msg.Choice)
	}
}

func TestConfirmDialogQClosesAsCancel(t *testing.T) {
	m := NewConfirmDialogModel("Delete?", "Delete", ActiveTheme())
	_, cmd := m.Update(keyPressMsg("q"))
	if cmd == nil {
		t.Fatal("q produced no command, want cancel")
	}
	msg, ok := cmd().(ConfirmDialogResultMsg)
	if !ok {
		t.Fatalf("q produced %T, want ConfirmDialogResultMsg", cmd())
	}
	if msg.Confirmed {
		t.Error("q.Confirmed = true, want false (cancel)")
	}
}
