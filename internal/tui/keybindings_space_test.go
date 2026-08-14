package tui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// These tests guard the issue #551 regression. bubbletea v2 delivers the
// space bar as the keystroke "space" (a KeyPressMsg whose String() returns
// "space", never " "). A key binding that lists a bare " " alternative can
// therefore never match a real space press. Only Enter then activates the
// control. The three bindings this covers are agenda Select, list-dialog
// Enter, and event-view Enter. They were the stragglers left after the
// account-settings fix in PR #540 (72dd0a7).

// spacePress and enterPress build the exact tea.KeyPressMsg bubbletea v2
// hands to a model.Update for those keys.
func spacePress() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeySpace, Text: " "}
}

func enterPress() tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: tea.KeyEnter}
}

// assertBindingAccepts fails when b does not match the given press. It
// exercises the real match path the dispatch code uses (key.Matches over
// the binding's keys). It then fails loudly the moment a binding's space
// alternative drifts back to the dead bare " ".
func assertBindingAccepts(t *testing.T, b key.Binding, msg tea.KeyPressMsg, label string) {
	t.Helper()
	if !key.Matches(msg, b) {
		t.Fatalf("%s binding does not match %q press; a bare \" \" alternative "+
			"is dead under bubbletea v2 — use \"space\"", label, msg.String())
	}
}

// assertNoBareSpace fails when b still carries the bare " " alternative
// bubbletea v2 never emits.
func assertNoBareSpace(t *testing.T, b key.Binding, label string) {
	t.Helper()
	for _, k := range b.Keys() {
		if k == " " {
			t.Fatalf("%s binding keeps the dead bare-space \" \" alternative; "+
				"bubbletea v2 emits space as \"space\", never \" \"", label)
		}
	}
}

func TestAgendaSelectKey_SpaceAndEnterOpenEventView(t *testing.T) {
	day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        7,
		Title:     "Standup",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 9, 15, 0, 0, time.Local),
	}
	m := NewAgendaModel(day).SetEvents([]event.Event{ev}, nil)

	sel := defaultAgendaKeys().Select
	assertBindingAccepts(t, sel, spacePress(), "agenda Select")
	assertBindingAccepts(t, sel, enterPress(), "agenda Select")

	for _, press := range []tea.KeyPressMsg{spacePress(), enterPress()} {
		_, cmd := m.Update(press)
		if cmd == nil {
			t.Fatalf("agenda Update(%q) returned no command; want EventViewRequestedMsg", press.String())
		}
		got := cmd()
		msg, ok := got.(EventViewRequestedMsg)
		if !ok {
			t.Fatalf("agenda Update(%q) produced %T; want EventViewRequestedMsg", press.String(), got)
		}
		if msg.Event.ID != ev.ID {
			t.Fatalf("EventViewRequestedMsg.Event.ID = %d, want %d", msg.Event.ID, ev.ID)
		}
	}
}

func TestListDialogEnterKey_SpaceAndEnterActivateFocusedAction(t *testing.T) {
	// Focus the first action ("New") so Enter/Space activate it rather than
	// the list.
	m := makeListDialogFixture().FocusAction(0)

	ent := defaultListDialogKeys().Enter
	assertBindingAccepts(t, ent, spacePress(), "list dialog Enter")
	assertBindingAccepts(t, ent, enterPress(), "list dialog Enter")

	for _, press := range []tea.KeyPressMsg{spacePress(), enterPress()} {
		_, cmd, handled := m.HandleKey(press, func() tea.Msg { return nil })
		if !handled {
			t.Fatalf("list dialog HandleKey(%q) not handled; want focused action activation", press.String())
		}
		if cmd == nil {
			t.Fatalf("list dialog HandleKey(%q) returned no command; want action msg", press.String())
		}
		if got := cmd().(string); got != "new" {
			t.Fatalf("list dialog HandleKey(%q) fired %q; want the focused \"New\" action msg", press.String(), got)
		}
	}
}

func TestEventViewEnterKey_SpaceAndEnterActivateFocusedAction(t *testing.T) {
	// Default focus is the action bar with "Edit" focused (action 0).
	m := NewEventViewDialogModel(testViewEvent(), CalendarInfo{Name: "Work"}, Theme{}).SetSize(120, 40)

	ent := defaultEventViewKeys().Enter
	assertBindingAccepts(t, ent, spacePress(), "event view Enter")
	assertBindingAccepts(t, ent, enterPress(), "event view Enter")

	for _, press := range []tea.KeyPressMsg{spacePress(), enterPress()} {
		_, cmd := m.Update(press)
		if cmd == nil {
			t.Fatalf("event view Update(%q) returned no command; want EventEditMsg", press.String())
		}
		got := cmd()
		if _, ok := got.(EventEditMsg); !ok {
			t.Fatalf("event view Update(%q) produced %T; want EventEditMsg (default focused action)", press.String(), got)
		}
	}
}

// TestKeyMaps_HaveNoBareSpaceAlternative is the smallest guard against the
// regression. It walks every binding in the three affected key
// maps and rejects the dead bare-space alternative outright.
func TestKeyMaps_HaveNoBareSpaceAlternative(t *testing.T) {
	ag := defaultAgendaKeys()
	ld := defaultListDialogKeys()
	ev := defaultEventViewKeys()

	bindings := []struct {
		name string
		b    key.Binding
	}{
		{"agenda.Up", ag.Up}, {"agenda.Down", ag.Down},
		{"agenda.PrevDay", ag.PrevDay}, {"agenda.NextDay", ag.NextDay},
		{"agenda.PrevMonth", ag.PrevMonth}, {"agenda.NextMonth", ag.NextMonth},
		{"agenda.Today", ag.Today}, {"agenda.Select", ag.Select},
		{"agenda.Create", ag.Create}, {"agenda.Edit", ag.Edit},
		{"agenda.Duplicate", ag.Duplicate}, {"agenda.Delete", ag.Delete},
		{"agenda.ToggleEmpty", ag.ToggleEmpty},

		{"list.Up", ld.Up}, {"list.Down", ld.Down},
		{"list.Tab", ld.Tab}, {"list.ShiftTab", ld.ShiftTab},
		{"list.Enter", ld.Enter}, {"list.Close", ld.Close},
		{"list.PageUp", ld.PageUp}, {"list.PageDown", ld.PageDown},
		{"list.Home", ld.Home}, {"list.End", ld.End},

		{"event.Edit", ev.Edit}, {"event.Duplicate", ev.Duplicate},
		{"event.Delete", ev.Delete}, {"event.Close", ev.Close},
		{"event.Tab", ev.Tab}, {"event.ShiftTab", ev.ShiftTab},
		{"event.Left", ev.Left}, {"event.Right", ev.Right},
		{"event.Enter", ev.Enter},
		{"event.RSVPYes", ev.RSVPYes}, {"event.RSVPNo", ev.RSVPNo},
		{"event.RSVPMaybe", ev.RSVPMaybe},
		{"event.ScrollUp", ev.ScrollUp}, {"event.ScrollDown", ev.ScrollDown},
		{"event.PageUp", ev.PageUp}, {"event.PageDown", ev.PageDown},
		{"event.Home", ev.Home}, {"event.End", ev.End},
	}
	for _, b := range bindings {
		assertNoBareSpace(t, b.b, b.name)
	}
}
