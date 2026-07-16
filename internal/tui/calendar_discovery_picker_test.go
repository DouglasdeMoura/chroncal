package tui

import (
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
	"github.com/douglasdemoura/chroncal/internal/caldav"
)

func pickerDiscovery() account.Discovery {
	return account.Discovery{
		Account: account.Account{ID: 7, DisplayName: "Google", Username: "douglas@example.com"},
		Calendars: []account.DiscoveredCalendar{
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/primary/", Name: "Primary", Color: "#4285f4", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/holidays/", Name: "Holidays in Brazil", Description: "Public holidays in Brazil", Color: "#0f9d58", Access: caldav.CalendarAccessRead, SupportedComponentSet: []string{"VEVENT"}}, Importable: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/personal/", Name: "Personal", Color: "#a142f4", Access: caldav.CalendarAccessWrite, SupportedComponentSet: []string{"VEVENT"}}, Importable: true, Imported: true},
			{RemoteCalendar: caldav.RemoteCalendar{Path: "/tasks/", Name: "Tasks", SupportedComponentSet: []string{"VTODO"}}, Importable: false},
		},
	}
}

func pickerRowForPath(t *testing.T, m AccountCalendarPickerModel, path string) int {
	t.Helper()
	for row, calendarIndex := range m.rowCalendar {
		if calendarIndex >= 0 && m.discovery.Calendars[calendarIndex].Path == path {
			return row
		}
	}
	t.Fatalf("picker has no row for %q", path)
	return -1
}

func TestAccountCalendarPickerPresentsSectionedAccountInventory(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(160, 60)
	plain := stripANSI(m.View())

	for _, want := range []string{
		"Add Calendars",
		"Google · douglas@example.com",
		"Available",
		"Already Added",
		"Unavailable",
		"Primary",
		"Holidays in Brazil",
		"Personal",
		"Tasks",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("picker view missing %q:\n%s", want, plain)
		}
	}
	if available, added, unavailable := strings.Index(plain, "Available"), strings.Index(plain, "Already Added"), strings.Index(plain, "Unavailable"); available >= added || added >= unavailable {
		t.Errorf("section order = Available:%d Already Added:%d Unavailable:%d", available, added, unavailable)
	}
	for _, unwanted := range []string{"[read-only]", "[imported]", "[unsupported]", "URL:", "/primary/", "VEVENT", "VTODO"} {
		if strings.Contains(plain, unwanted) {
			t.Errorf("picker exposes implementation label %q:\n%s", unwanted, plain)
		}
	}

	first := m.shell.Selected()
	if got := m.discovery.Calendars[m.rowCalendar[first]].Path; got != "/primary/" {
		t.Fatalf("initial row path = %q, want first available calendar", got)
	}
	m.shell = m.shell.MoveDown().MoveDown()
	if got := m.discovery.Calendars[m.rowCalendar[m.shell.Selected()]].Path; got != "/personal/" {
		t.Errorf("navigation did not skip Already Added heading: got %q", got)
	}
}

func TestAccountCalendarPickerUsesHumanCalendarDetails(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(160, 60)
	m.shell = m.shell.SetSelected(pickerRowForPath(t, m, "/holidays/"))
	m = m.refresh()
	details := stripANSI(strings.Join(m.shell.detailLines, "\n"))

	for _, want := range []string{"Public holidays in Brazil", "Read only", "Events", "Changes made in Chroncal will not be uploaded"} {
		if !strings.Contains(details, want) {
			t.Errorf("details missing %q:\n%s", want, details)
		}
	}
	for _, unwanted := range []string{"URL:", "/holidays/", "VEVENT"} {
		if strings.Contains(details, unwanted) {
			t.Errorf("details expose implementation label %q:\n%s", unwanted, details)
		}
	}

	m.shell = m.shell.SetSelected(pickerRowForPath(t, m, "/tasks/"))
	m = m.refresh()
	details = stripANSI(strings.Join(m.shell.detailLines, "\n"))
	if !strings.Contains(details, "Can’t add") || !strings.Contains(details, "tasks") {
		t.Errorf("unsupported details do not explain the consequence:\n%s", details)
	}
}

func TestAccountCalendarPickerSelectsOnlyAvailableCalendars(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(160, 60)
	if !m.selected["/primary/"] || !m.selected["/holidays/"] || m.selected["/personal/"] || m.selected["/tasks/"] {
		t.Fatalf("initial selections = %v", m.selected)
	}
	if got := m.shell.actions[0].Label; got != "Add 2 Calendars" {
		t.Fatalf("primary action = %q, want %q", got, "Add 2 Calendars")
	}

	m.shell = m.shell.SetSelected(pickerRowForPath(t, m, "/holidays/"))
	m = m.toggleCurrent()
	if m.selected["/holidays/"] {
		t.Fatal("space toggle should deselect the current available calendar")
	}
	if got := m.shell.actions[0].Label; got != "Add Calendar" {
		t.Errorf("single-selection action = %q, want %q", got, "Add Calendar")
	}
	msg := m.importSelected()().(AccountCalendarsImportRequestedMsg)
	if len(msg.Paths) != 1 || msg.Paths[0] != "/primary/" || msg.AccountID != 7 {
		t.Fatalf("add message = %+v", msg)
	}
}

func TestAccountCalendarPickerDisablesAddWithNoSelection(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{})
	m = m.toggleAll()
	if !m.shell.actions[0].Disabled {
		t.Fatal("Add action remains enabled with no selected calendars")
	}
	if got := m.shell.actions[0].Label; got != "Add Calendars" {
		t.Errorf("empty-selection action = %q, want %q", got, "Add Calendars")
	}
	if cmd := m.importSelected(); cmd != nil {
		t.Fatal("importSelected returned a command with no selected calendars")
	}
}

func TestAccountCalendarPickerCannotSelectUnavailableOrExistingCalendar(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{})
	for _, path := range []string{"/personal/", "/tasks/"} {
		m.shell = m.shell.SetSelected(pickerRowForPath(t, m, path))
		m = m.toggleCurrent()
		if m.selected[path] {
			t.Errorf("%s must remain unselected", path)
		}
	}
}

func TestAccountCalendarPickerKeyboardLifecycle(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(120, 40)

	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter with selected calendars did not invoke Add")
	}
	added, ok := cmd().(AccountCalendarsImportRequestedMsg)
	if !ok || len(added.Paths) != 2 {
		t.Fatalf("Enter message = %#v, want two-calendar add request", added)
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: 'a', Text: "a"})
	if m.selectedCount() != 0 {
		t.Fatalf("select-all toggle left %d calendars selected, want 0", m.selectedCount())
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("Enter invoked disabled Add action")
	}

	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
	if m.shell.FocusZone() != ListZoneActions || m.shell.FocusedAction() != 1 {
		t.Fatalf("Tab focus = (%v, %d), want enabled Cancel action", m.shell.FocusZone(), m.shell.FocusedAction())
	}
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("Enter on Cancel did not close the picker")
	}
	if _, ok := cmd().(AccountCalendarPickerClosedMsg); !ok {
		t.Fatalf("Cancel message = %T, want AccountCalendarPickerClosedMsg", cmd())
	}
}

func TestAccountCalendarPickerNarrowLayoutFitsTerminal(t *testing.T) {
	const width = 65
	m := NewAccountCalendarPickerModel(pickerDiscovery(), Theme{}).SetSize(width, 20)
	view := m.View()
	for lineNumber, line := range strings.Split(view, "\n") {
		if got := lipgloss.Width(line); got > width {
			t.Errorf("rendered line %d is %d columns wide, max %d:\n%s", lineNumber+1, got, width, stripANSI(view))
		}
	}
	plain := stripANSI(view)
	for _, want := range []string{"Add Calendars", "Google · douglas@example.com", "Available", "Add 2 Calendars", "Cancel"} {
		if !strings.Contains(plain, want) {
			t.Errorf("narrow picker missing %q:\n%s", want, plain)
		}
	}
}
