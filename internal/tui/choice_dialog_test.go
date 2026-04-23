package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestChoiceDialogSetSizeUsesFixedWidth(t *testing.T) {
	m := NewChoiceDialogModel(
		`Delete "Team Standup"?`,
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
		"This event", "This and following", "All events",
	)

	if got, want := m.form.Focused(), m.form.submitIndex(); got != want {
		t.Fatalf("focused control = %d, want submit button index %d", got, want)
	}
}
