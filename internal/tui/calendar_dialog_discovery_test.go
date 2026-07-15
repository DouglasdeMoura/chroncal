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
