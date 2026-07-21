package tui

import (
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/account"
)

// ---- blockReadOnlyCalendarMutation ----

func TestBlockReadOnlyCalendarMutation_ReadOnlyBlocked(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Shared", RemoteAccess: "read", RemoteComponents: ""},
		},
		toast: NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(1)
	if !blocked {
		t.Fatal("read-only calendar should block mutation")
	}
}

func TestBlockReadOnlyCalendarMutation_NonVEVENTBlocked(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Tasks", RemoteAccess: "write", RemoteComponents: "VTODO"},
		},
		toast: NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(1)
	if !blocked {
		t.Fatal("calendar without VEVENT support should block event mutation")
	}
}

func TestBlockReadOnlyCalendarMutation_JournalOnlyBlocked(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Journal", RemoteAccess: "owner", RemoteComponents: "VJOURNAL"},
		},
		toast: NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(1)
	if !blocked {
		t.Fatal("calendar advertising only VJOURNAL should block event mutation")
	}
}

func TestBlockReadOnlyCalendarMutation_SupportsVEVENTAllowed(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Work", RemoteAccess: "write", RemoteComponents: "VEVENT,VTODO"},
		},
		toast: NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(1)
	if blocked {
		t.Fatal("calendar with VEVENT support should not block mutation")
	}
}

func TestBlockReadOnlyCalendarMutation_EmptyComponentsAllowed(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Local", RemoteAccess: "owner", RemoteComponents: ""},
		},
		toast: NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(1)
	if blocked {
		t.Fatal("calendar with no advertised components should not block mutation (backward compat)")
	}
}

func TestBlockReadOnlyCalendarMutation_UnknownCalendarAllowed(t *testing.T) {
	t.Parallel()
	m := &Model{
		calendars: map[int64]CalendarInfo{},
		toast:     NewToastModel(NewTheme(true)),
	}
	_, blocked := m.blockReadOnlyCalendarMutation(999)
	if blocked {
		t.Fatal("unknown calendar ID should not block (defensive passthrough)")
	}
}

func TestEventFormCalendarsFiltersUnsupportedDestinations(t *testing.T) {
	t.Parallel()
	calendars := map[int64]CalendarInfo{
		1: {Name: "Local"},
		2: {Name: "Events", RemoteAccess: "write", RemoteComponents: "VEVENT,VTODO"},
		3: {Name: "Subscribed", RemoteAccess: "read", RemoteComponents: "VEVENT"},
		4: {Name: "Tasks", RemoteAccess: "owner", RemoteComponents: "VTODO"},
	}

	filtered := eventFormCalendars(calendars)
	if len(filtered) != 2 {
		t.Fatalf("filtered calendars = %#v, want only local and VEVENT-writable", filtered)
	}
	if _, ok := filtered[1]; !ok {
		t.Fatal("local calendar was filtered")
	}
	if _, ok := filtered[2]; !ok {
		t.Fatal("VEVENT-writable calendar was filtered")
	}
	if _, ok := filtered[3]; ok {
		t.Fatal("read-only calendar remained selectable")
	}
	if _, ok := filtered[4]; ok {
		t.Fatal("VTODO-only calendar remained selectable")
	}
}

// ---- discovery reload ----

// TestDiscoveryReadyReloadsCalendars proves that successful discovery, which
// reconciles remote metadata for already-imported calendars, immediately
// triggers a calendar reload while the integrated picker is shown.
func TestDiscoveryReadyReloadsCalendars(t *testing.T) {
	m := Model{
		theme:  NewTheme(true),
		width:  80,
		height: 24,
	}
	openCalendarManagerForTest(&m, CalendarDialogParams{})
	discovery := account.Discovery{Account: account.Account{ID: 1, Name: "Test"}}

	updated, cmd := m.Update(accountDiscoveryReadyMsg{discovery: discovery})
	if cmd == nil {
		t.Fatal("expected non-nil command (calendar reload) after successful discovery")
	}

	model := updated.(Model)
	if !model.calendarManagerOpen || model.calendarManager.DiscoveryPicker() == nil {
		t.Fatal("integrated picker should be open after discovery ready")
	}
}

// TestDiscoveryReadyErrorDoesNotReload proves that a failed discovery does not
// schedule a calendar reload (the error path returns a status-expire command,
// not loadCalendars).
func TestDiscoveryReadyErrorDoesNotOpenPicker(t *testing.T) {
	m := Model{
		theme:  NewTheme(true),
		width:  80,
		height: 24,
	}
	openCalendarManagerForTest(&m, CalendarDialogParams{})

	updated, _ := m.Update(accountDiscoveryReadyMsg{
		err: errors.New("boom"),
	})
	model := updated.(Model)
	if model.calendarManager.DiscoveryPicker() != nil {
		t.Fatal("integrated picker should not open on discovery failure")
	}
}
