package tui

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

func pickerDiscovery() account.Discovery {
	return account.Discovery{
		Account: account.Account{ID: 7, DisplayName: "Google"},
		Calendars: []account.DiscoveredCalendar{
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/primary/", Name: "Primary", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/holidays/", Name: "Holidays in Brazil", Access: caldav.CalendarAccessRead, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/tasks/", Name: "Tasks", SupportedComponentSet: []string{"VTODO"}}, Importable: false},
		},
	}
}

func TestAccountCalendarPickerSelectsImportableCollections(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(160, 60)
	if !m.selected["/primary/"] || !m.selected["/holidays/"] || m.selected["/tasks/"] {
		t.Fatalf("initial selections = %v", m.selected)
	}
	if out := m.View(); !strings.Contains(out, "Holidays in Brazil") || !strings.Contains(out, "read-only") || !strings.Contains(out, "unsupported") {
		t.Fatalf("picker view missing collection metadata: %q", out)
	}

	m.shell = m.shell.SetSelected(1)
	m = m.toggleCurrent()
	if m.selected["/holidays/"] {
		t.Fatal("space toggle should deselect the current importable calendar")
	}
	cmd := m.importSelected()
	msg := cmd().(AccountCalendarsImportRequestedMsg)
	if len(msg.Paths) != 1 || msg.Paths[0] != "/primary/" || msg.AccountID != 7 {
		t.Fatalf("import message = %+v", msg)
	}
}

func TestAccountCalendarPickerCannotSelectUnsupportedCollection(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{})
	m.shell = m.shell.SetSelected(2)
	m = m.toggleCurrent()
	if m.selected["/tasks/"] {
		t.Fatal("unsupported VTODO collection must remain unselected")
	}
}

func TestAccountCalendarPickerDoesNotReimportExistingCalendar(t *testing.T) {
	discovery := pickerDiscovery()
	discovery.Calendars[0].Imported = true
	m := NewAccountCalendarPickerModel(discovery, Theme{})
	if m.selected["/primary/"] {
		t.Fatal("already-imported calendar must not be selected")
	}
	msg := m.importSelected()().(AccountCalendarsImportRequestedMsg)
	if len(msg.Paths) != 1 || msg.Paths[0] != "/holidays/" {
		t.Fatalf("import paths = %v, want only new calendar", msg.Paths)
	}
}
