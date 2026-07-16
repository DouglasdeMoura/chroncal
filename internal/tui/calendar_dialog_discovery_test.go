package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestCalendarDialogEnableSyncSwitchesToConnectionStep(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{Color: "#a6e3a1"}, Theme{}).SetSize(120, 40)
	m.form.Field(cdIdxSync).(*CheckboxField).Toggle()
	m.form.onRebuild(&m.form)
	body := m.form.View()

	for _, want := range []string{"Server URL", "Username", "Auth"} {
		if !strings.Contains(body, want) {
			t.Errorf("connection step missing %q:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{"Name", "Color", "Description", "Owner email"} {
		if strings.Contains(body, unwanted) {
			t.Errorf("connection step still contains local field %q:\n%s", unwanted, body)
		}
	}
}

func TestCalendarDialogGroupsLinkedAccountActionsBehindMenu(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             11,
		AccountID:      7,
		Name:           "Personal",
		Color:          "#a6e3a1",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, Theme{}).SetSize(65, 20)

	labels := make([]string, 0, len(m.form.actionButtons))
	for _, button := range m.form.actionButtons {
		labels = append(labels, button.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Account…"; got != want {
		t.Fatalf("edit actions = %q, want %q", got, want)
	}
	_, ok := m.form.actionButtons[1].OnPress().(CalendarAccountActionsRequestedMsg)
	if !ok {
		t.Fatalf("Account action message = %T", m.form.actionButtons[1].OnPress())
	}

	plain := stripANSI(m.View())
	utilityRow, saveRow := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Set as Default") && strings.Contains(line, "Account…") {
			utilityRow = i
		}
		if strings.Contains(line, "Save") && strings.Contains(line, "Cancel") {
			saveRow = i
		}
	}
	if utilityRow < 0 || saveRow < 0 || saveRow-utilityRow < 2 {
		t.Fatalf("utility and commit actions need separate rows:\n%s", plain)
	}
	for i, line := range strings.Split(plain, "\n") {
		if width := lipgloss.Width(line); width > 65 {
			t.Fatalf("rendered line %d is %d columns wide:\n%s", i, width, plain)
		}
	}
}

func TestCalendarAccountActionsMenuSeparatesDestructiveAction(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             11,
		AccountID:      7,
		Name:           "Personal",
		Color:          "#a6e3a1",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, Theme{})

	menu := m.AccountActionsMenu().SetSize(120, 40)
	labels := make([]string, 0, len(menu.actions))
	for _, action := range menu.actions {
		labels = append(labels, action.label)
	}
	if got, want := strings.Join(labels, ","), "Add calendars…,Re-authenticate…,Disconnect…,Cancel"; got != want {
		t.Fatalf("account actions = %q, want %q", got, want)
	}
	if menu.actions[2].variant != ButtonDanger {
		t.Fatal("Disconnect action is not destructive")
	}
	for i, action := range menu.actions {
		if i != 2 && action.variant == ButtonDanger {
			t.Fatalf("%q unexpectedly uses destructive styling", action.label)
		}
	}

	view := stripANSI(menu.View())
	var previous int
	for _, label := range labels {
		pos := strings.Index(view, label)
		if pos < previous {
			t.Fatalf("%q is not rendered after the previous action:\n%s", label, view)
		}
		previous = pos
	}
	if width, _ := menu.BoxSize(); width > calendarAccountActionsMaxWidth {
		t.Fatalf("Account menu width = %d, max = %d:\n%s", width, calendarAccountActionsMaxWidth, view)
	}

	msg, ok := menu.actions[0].onPress().(CalendarDiscoverAdditionalRequestedMsg)
	if !ok || msg.CalendarID != 11 || msg.AccountID != 7 {
		t.Fatalf("Add calendars action = %#v", msg)
	}
}

func TestCalendarAccountActionsMenuKeyboardNavigation(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:             11,
		AccountID:      7,
		Name:           "Personal",
		RemoteLinked:   true,
		RemoteAuthType: "oauth2",
	}, Theme{})
	menu := m.AccountActionsMenu().SetSize(120, 40)

	menu, _ = menu.Update(keyPressMsg("down"))
	if menu.selected != 1 {
		t.Fatalf("selected action = %d, want Re-authenticate at 1", menu.selected)
	}
	_, cmd := menu.Update(keyPressMsg("enter"))
	if cmd == nil {
		t.Fatal("Enter did not select the focused account action")
	}
	selected, ok := cmd().(CalendarAccountMenuSelectedMsg)
	if !ok {
		t.Fatalf("Enter emitted %T", cmd())
	}
	if _, ok := selected.Message.(CalendarReauthRequestedMsg); !ok {
		t.Fatalf("selected message = %T, want CalendarReauthRequestedMsg", selected.Message)
	}

	_, cmd = menu.Update(keyPressMsg("esc"))
	if cmd == nil {
		t.Fatal("Escape did not close the Account menu")
	}
	if _, ok := cmd().(CalendarAccountMenuClosedMsg); !ok {
		t.Fatalf("Escape emitted %T", cmd())
	}
}
