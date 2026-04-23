package tui

import "testing"

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
