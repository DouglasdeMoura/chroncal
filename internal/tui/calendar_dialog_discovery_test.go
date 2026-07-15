package tui

import (
	"strings"
	"testing"
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

func TestCalendarDialogLinkedCalendarOffersAdditionalCalendarDiscovery(t *testing.T) {
	m := NewCalendarDialogModel(CalendarDialogParams{
		ID:           11,
		AccountID:    7,
		Name:         "Personal",
		Color:        "#a6e3a1",
		RemoteLinked: true,
	}, Theme{}).SetSize(120, 40)

	for _, button := range m.form.actionButtons {
		if button.Label != "Add calendars" {
			continue
		}
		msg, ok := button.OnPress().(CalendarDiscoverAdditionalRequestedMsg)
		if !ok {
			t.Fatalf("Add calendars message = %T", button.OnPress())
		}
		if msg.CalendarID != 11 || msg.AccountID != 7 {
			t.Fatalf("Add calendars message = %+v", msg)
		}
		return
	}
	t.Fatal("linked calendar dialog does not offer Add calendars")
}
