package tui

import (
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestListDialog_ActionPutsTheFocusOnTheList checks the rule of the shell: a
// button that fires, by Enter, Space, or a click, puts the focus on the
// list. With no button to fire, the focus does not change (issue #909).
func TestListDialog_ActionPutsTheFocusOnTheList(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{{Code: tea.KeyEnter}, {Code: ' ', Text: " "}} {
		m := makeListDialogFixture().FocusAction(0)
		m, cmd, _ := m.HandleKey(k, func() tea.Msg { return nil })
		if cmd == nil {
			t.Fatalf("%v on a focused button: no command", k)
		}
		if got := m.FocusZone(); got != ListZoneList {
			t.Errorf("%v on a focused button: focus zone = %v, want the list", k, got)
		}
	}

	m, cmd := makeListDialogFixture().FocusAction(1).FireAction(0)
	if cmd == nil {
		t.Fatal("click on a button: no command")
	}
	if got := m.FocusZone(); got != ListZoneList {
		t.Errorf("click on a button: focus zone = %v, want the list", got)
	}

	m, cmd = makeListDialogFixture().SetFocusZone(ListZoneList).Activate()
	if cmd != nil || m.FocusZone() != ListZoneList {
		t.Errorf("Activate on the list = %v, %v, want no command and the list", cmd, m.FocusZone())
	}
}

// eventDialogForFocus returns a day dialog with two events. The first one is
// selected.
func eventDialogForFocus() EventDialogModel {
	day := time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC)
	evs := []event.Event{
		{ID: 1, CalendarID: 1, Title: "First", StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)},
		{ID: 2, CalendarID: 1, Title: "Second", StartTime: day.Add(11 * time.Hour), EndTime: day.Add(12 * time.Hour)},
	}
	cals := map[int64]CalendarInfo{1: {Name: "Work", Color: "#a6e3a1"}}
	return NewEventDialogModel(day, evs, cals, help.New()).SetSize(120, 40)
}

// TestEventDialog_ActionsPutTheFocusOnTheList checks the day dialog. Each
// action fires and puts the focus on the list: the Edit, Duplicate, and
// Delete keys, Enter and Space on a focused button, and a click on a
// button. After a canceled delete, Down then moves the selection, and
// Left changes the day (issue #909).
func TestEventDialog_ActionsPutTheFocusOnTheList(t *testing.T) {
	m0 := eventDialogForFocus()
	onDelete := m0.focusStop(tabStop{kind: zoneAction, idx: 2})
	click := func(idx int) tea.Msg {
		x, y := m0.shell.actionBarOrigin()
		for dx := 0; dx < 80; dx++ {
			if i, ok := m0.shell.ActionAtPosition(x+dx, y); ok && i == idx {
				return tea.MouseClickMsg{X: x + dx, Y: y, Button: tea.MouseLeft}
			}
		}
		t.Fatalf("no screen cell for action %d", idx)
		return nil
	}
	for _, tc := range []struct {
		name  string
		start EventDialogModel
		msg   tea.Msg
		want  string
	}{
		{"e key", m0, tea.KeyPressMsg{Code: 'e', Text: "e"}, "edit"},
		{"ctrl+d key", m0, tea.KeyPressMsg{Code: 'd', Mod: tea.ModCtrl}, "duplicate"},
		{"x key", m0, tea.KeyPressMsg{Code: 'x', Text: "x"}, "delete"},
		{"x key from the Delete button", onDelete, tea.KeyPressMsg{Code: 'x', Text: "x"}, "delete"},
		{"Enter on the Delete button", onDelete, tea.KeyPressMsg{Code: tea.KeyEnter}, "delete"},
		{"Space on the Delete button", onDelete, tea.KeyPressMsg{Code: ' ', Text: " "}, "delete"},
		{"click on the Delete button", m0, click(2), "delete"},
	} {
		m, cmd := tc.start.Update(tc.msg)
		if cmd == nil {
			t.Fatalf("%s: no command", tc.name)
		}
		got := ""
		switch msg := cmd().(type) {
		case EventEditMsg:
			got = "edit"
		case EventDuplicateMsg:
			got = "duplicate"
		case EventDeleteMsg:
			got = "delete"
		default:
			t.Fatalf("%s: command gave %T", tc.name, msg)
		}
		if got != tc.want {
			t.Errorf("%s: action = %s, want %s", tc.name, got, tc.want)
		}
		if zone := m.currentZone(); zone != zoneList {
			t.Errorf("%s: zone = %d, want the list", tc.name, zone)
		}
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		if ev, _ := next.selectedEvent(); ev.ID != 2 {
			t.Errorf("%s: Down after the action selected event %d, want 2", tc.name, ev.ID)
		}
		_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyLeft})
		if cmd == nil {
			t.Fatalf("%s: Left after the action gave no command", tc.name)
		}
		if _, ok := cmd().(DialogDayChangedMsg); !ok {
			t.Errorf("%s: Left after the action gave %T, want a day change", tc.name, cmd())
		}
	}
}

// TestTrashModel_SpaceOnAFocusedButtonPressesIt checks that Space on a
// focused Restore button presses it and does not mark the row (issue #909).
func TestTrashModel_SpaceOnAFocusedButtonPressesIt(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m.shell = m.shell.FocusAction(0)
	m, cmd := m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	if cmd == nil {
		t.Fatal("Space on the Restore button: no command")
	}
	if msg, ok := cmd().(TrashRestoreRequestedMsg); !ok || len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Errorf("Space on the Restore button gave %#v, want the restore of the entry with ID 1", cmd())
	}
	if len(m.marked) != 0 {
		t.Errorf("Space on the Restore button marked %v, want no mark", m.marked)
	}
}

// TestEventDialog_RSVPAndTitleButtonPutTheFocusOnTheList checks the RSVP
// keys, Enter on a focused RSVP button, and a click on the title button.
// Each fires and puts the focus on the list (issue #909).
func TestEventDialog_RSVPAndTitleButtonPutTheFocusOnTheList(t *testing.T) {
	m0 := rsvpDialogModel()
	onRSVP := m0.focusStop(tabStop{kind: zoneRSVP, idx: 1})
	for _, tc := range []struct {
		name  string
		start EventDialogModel
		msg   tea.Msg
	}{
		{"y key", m0, tea.KeyPressMsg{Code: 'y', Text: "y"}},
		{"Enter on the No button", onRSVP, tea.KeyPressMsg{Code: tea.KeyEnter}},
	} {
		m, cmd := tc.start.Update(tc.msg)
		if cmd == nil {
			t.Fatalf("%s: no command", tc.name)
		}
		if _, ok := cmd().(EventRSVPMsg); !ok {
			t.Errorf("%s: command gave %T, want an RSVP", tc.name, cmd())
		}
		if zone := m.currentZone(); zone != zoneList {
			t.Errorf("%s: zone = %d, want the list", tc.name, zone)
		}
	}

	onDelete := eventDialogForFocus().focusStop(tabStop{kind: zoneAction, idx: 2})
	var click tea.Msg
	for y := 0; y < 40 && click == nil; y++ {
		for x := 0; x < 120; x++ {
			if _, ok := onDelete.shell.TitleActionAtPosition(x, y); ok {
				click = tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
				break
			}
		}
	}
	if click == nil {
		t.Fatal("no screen cell for the title button")
	}
	m, cmd := onDelete.Update(click)
	if cmd == nil {
		t.Fatal("click on the title button: no command")
	}
	if _, ok := cmd().(EventCreateMsg); !ok {
		t.Errorf("click on the title button gave %T, want a create", cmd())
	}
	if zone := m.currentZone(); zone != zoneList {
		t.Errorf("click on the title button: zone = %d, want the list", zone)
	}
}

// TestTrashModel_SpaceMarksAfterACanceledPurge checks Space after the purge
// key and on the focused Purge All button. Both mark the row: the purge key
// leaves the focus on the list, and Space never fires Purge All (issue
// #909).
func TestTrashModel_SpaceMarksAfterACanceledPurge(t *testing.T) {
	space := tea.KeyPressMsg{Code: ' ', Text: " "}
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	m, cmd := m.Update(space)
	if cmd != nil || !m.marked[entryKey(trashFixture()[0])] {
		t.Errorf("Space after the purge key: command %v, marks %v, want the mark of the first row", cmd, m.marked)
	}

	m = newTrashForTest().SetEntries(trashFixture(), nil)
	m.shell = m.shell.SetFocusZone(ListZoneTitleAction)
	m, cmd = m.Update(space)
	if cmd != nil || !m.marked[entryKey(trashFixture()[0])] {
		t.Errorf("Space on Purge All: command %v, marks %v, want the mark of the first row", cmd, m.marked)
	}
}
