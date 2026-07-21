package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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
	}, Theme{}).SetSize(65, 26)

	// This is a legacy app-wired dialog (not manager-embedded), so Export —
	// whose host handler does not exist yet — is gated out; only Set as
	// Default and Delete remain. The "Manage Account…" button is gone;
	// drilling into the owning account is an inline "Account: <name> ›"
	// opener that emits the same canonical AccountSettingsRequestedMsg the
	// host already routes.
	labels := make([]string, 0, len(m.form.actionButtons))
	for _, button := range m.form.actionButtons {
		labels = append(labels, button.Label)
	}
	if got, want := strings.Join(labels, ","), "Set as Default,Delete Calendar…"; got != want {
		t.Fatalf("edit actions = %q, want %q", got, want)
	}

	openerIdx := -1
	for i := range m.form.ItemCount() {
		if _, ok := m.form.Field(i).(*OpenerField); ok {
			openerIdx = i
			break
		}
	}
	if openerIdx < 0 {
		t.Fatal("linked calendar detail has no Account opener field")
	}
	m.form, _ = m.form.focusIndex(openerIdx)
	var cmd tea.Cmd
	m.form, cmd = m.form.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	msg, ok := cmd().(AccountSettingsRequestedMsg)
	if !ok || msg.AccountID != 7 {
		t.Fatalf("Account opener = %#v, want account 7 settings", msg)
	}

	plain := stripANSI(m.View())
	for _, want := range []string{"Account: Personal Google ›", "Last synced:"} {
		if !strings.Contains(plain, want) {
			t.Errorf("linked calendar context missing %q:\n%s", want, plain)
		}
	}
	for _, unwanted := range []string{"Manage Account…", "Remote:", "Re-authenticate", "Disconnect"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("Edit Calendar still contains account maintenance %q:\n%s", unwanted, plain)
		}
	}

	// Utility actions stack vertically (one per line) above the Save/Cancel
	// commit row; confirm the first utility line precedes the commit row.
	utilityRow, saveRow := -1, -1
	for i, line := range strings.Split(plain, "\n") {
		if utilityRow < 0 && strings.Contains(line, "Set as Default") {
			utilityRow = i
		}
		if saveRow < 0 && strings.Contains(line, "Save") && strings.Contains(line, "Cancel") {
			saveRow = i
		}
		if width := lipgloss.Width(line); width > 65 {
			t.Fatalf("rendered line %d is %d columns wide:\n%s", i, width, plain)
		}
	}
	if utilityRow < 0 || saveRow < 0 || saveRow <= utilityRow {
		t.Fatalf("utility actions must precede the Save/Cancel row:\n%s", plain)
	}
}
