package tui

import (
	"strings"
	"testing"

	lipgloss "charm.land/lipgloss/v2"
)

func TestNewCalendarAndAddAccountAreSeparateFlows(t *testing.T) {
	calendarDialog := NewCalendarDialogModel(CalendarDialogParams{Color: "#a6e3a1"}, Theme{}).SetSize(120, 40)
	calendarView := stripANSI(calendarDialog.View())
	for _, want := range []string{"New local calendar", "Name", "Color", "Save"} {
		if !strings.Contains(calendarView, want) {
			t.Errorf("new-calendar view missing %q:\n%s", want, calendarView)
		}
	}
	for _, unwanted := range []string{"Enable CalDAV sync", "Server URL", "Add Account"} {
		if strings.Contains(calendarView, unwanted) {
			t.Errorf("new-calendar view contains account action %q:\n%s", unwanted, calendarView)
		}
	}

	accountDialog := NewAccountDialogModel(Theme{}).SetSize(120, 40)
	accountView := stripANSI(accountDialog.View())
	for _, want := range []string{"Add Account", "Server URL", "Username", "Auth", "Sign In", "Cancel"} {
		if !strings.Contains(accountView, want) {
			t.Errorf("add-account view missing %q:\n%s", want, accountView)
		}
	}
	for _, unwanted := range []string{"New local calendar", "Enable CalDAV sync", "Back"} {
		if strings.Contains(accountView, unwanted) {
			t.Errorf("add-account view contains calendar flow %q:\n%s", unwanted, accountView)
		}
	}
}

func TestCalendarDialogRoutesLinkedMaintenanceToAccountSettings(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		AccountName:  "Personal Google",
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
		LastSyncAt:   "2026-07-14T09:00:00Z",
	}, Theme{}).SetSize(65, 20)

	labels := make([]string, 0, len(m.form.actionButtons))
	for _, button := range m.form.actionButtons {
		labels = append(labels, button.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Manage Account…"; got != want {
		t.Fatalf("edit actions = %q, want %q", got, want)
	}
	msg, ok := m.form.actionButtons[1].OnPress().(AccountSettingsRequestedMsg)
	if !ok || msg.AccountID != 7 {
		t.Fatalf("Manage Account action = %#v, want account 7 settings", msg)
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"Account: Personal Google", "Last synced:", "Manage Account…"} {
		if !strings.Contains(plain, want) {
			t.Errorf("linked calendar context missing %q:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Remote:", "Re-authenticate", "Disconnect"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("Edit Calendar still contains account maintenance %q:\n%s", unwanted, plain)
		}
	}

	utilityRow, saveRow := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "Set as Default") && strings.Contains(line, "Manage Account…") {
			utilityRow = i
		}
		if strings.Contains(line, "Save") && strings.Contains(line, "Cancel") {
			saveRow = i
		}
		if width := lipgloss.Width(line); width > 65 {
			t.Fatalf("rendered line %d is %d columns wide:\n%s", i, width, plain)
		}
	}
	if utilityRow < 0 || saveRow < 0 || saveRow-utilityRow < 2 {
		t.Fatalf("utility and commit actions need separate rows:\n%s", plain)
	}
}
