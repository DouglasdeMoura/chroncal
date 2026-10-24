package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestChoiceDialogClickEmitsSelectedChoice(t *testing.T) {
	tests := []struct {
		name   string
		target string
		want   int
	}{
		{name: "first option", target: "submit", want: 0},
		{name: "second option", target: "action:0", want: 1},
		{name: "third option", target: "action:1", want: 2},
		{name: "cancel", target: "cancel", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			isolateMouseTracker(t)
			m := NewChoiceDialogModel(
				`Delete "Team Standup"?`,
				ActiveTheme(),
				"This event", "This and following", "All events",
			).SetSize(120, 40)

			_ = m.View()
			x, y, ok := zoneCenterByName(tt.target)
			if !ok {
				t.Fatalf("rendered dialog has no mouse zone %q", tt.target)
			}

			bw, bh := m.BoxSize()
			click := tea.MouseClickMsg{
				X:      x + (m.dialog.width-bw)/2,
				Y:      y + (m.dialog.height-bh)/2,
				Button: tea.MouseLeft,
			}
			_, cmd := m.Update(click)
			if cmd == nil {
				t.Fatalf("click on %q produced no command", tt.target)
			}
			msg, ok := cmd().(ChoiceDialogResultMsg)
			if !ok {
				t.Fatalf("click on %q produced %T, want ChoiceDialogResultMsg", tt.target, cmd())
			}
			if msg.Choice != tt.want {
				t.Errorf("click on %q chose %d, want %d", tt.target, msg.Choice, tt.want)
			}
		})
	}
}

// TestChoiceDialogDestructiveFocusesCancel guards issue #877. A destructive
// choice dialog gives the focus to Cancel, so a quick Enter cancels. Each
// choice uses ButtonDanger.
func TestChoiceDialogDestructiveFocusesCancel(t *testing.T) {
	plain := NewChoiceDialogModel(
		`Delete "Team Standup"?`,
		ActiveTheme(),
		"This event", "This and following", "All events",
	)
	m := plain.Destructive()

	if got, want := m.form.Focused(), m.form.cancelIndex(); got != want {
		t.Fatalf("focused control = %d, want cancel index %d", got, want)
	}
	if m.form.submitVariant != ButtonDanger {
		t.Errorf("submit variant = %v, want ButtonDanger", m.form.submitVariant)
	}
	for _, b := range m.form.actionButtons {
		if b.Variant != ButtonDanger {
			t.Errorf("button %q variant = %v, want ButtonDanger", b.Label, b.Variant)
		}
	}
	for _, b := range plain.form.actionButtons {
		if b.Variant != Button {
			t.Errorf("Destructive changed the source dialog: button %q variant = %v", b.Label, b.Variant)
		}
	}

	_, cmd := m.Update(keyPressMsg("enter"))
	if cmd == nil {
		t.Fatal("enter produced no command, want cancel")
	}
	if msg, ok := cmd().(ChoiceDialogResultMsg); !ok || msg.Choice != -1 {
		t.Fatalf("enter produced %#v, want ChoiceDialogResultMsg{Choice: -1}", cmd())
	}
}
