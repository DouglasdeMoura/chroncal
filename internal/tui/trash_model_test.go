package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func ptrTime(t time.Time) *time.Time { return &t }

func deletedFixture() []event.Event {
	t1 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)
	return []event.Event{
		{ID: 1, CalendarID: 1, Title: "Lunch", DeletedAt: ptrTime(t1)},
		{ID: 2, CalendarID: 1, Title: "Standup", DeletedAt: ptrTime(t2)},
	}
}

func TestTrashModel_SetEventsSelectsFirst(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	m = m.SetEvents(deletedFixture(), nil)
	ev, ok := m.SelectedEvent()
	if !ok || ev.ID != 1 {
		t.Fatalf("SelectedEvent = %+v ok=%v, want ID=1", ev, ok)
	}
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
}

func TestTrashModel_EmptyRendersPlaceholder(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	out := m.View()
	if out == "" {
		t.Fatal("View should render header+placeholder when empty")
	}
	if !strings.Contains(out, "No deleted events.") {
		t.Fatalf("View = %q, want contains 'No deleted events.'", out)
	}
}

func TestTrashModel_DownMovesSelection(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	m2, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	ev, ok := m2.SelectedEvent()
	if !ok || ev.ID != 2 {
		t.Fatalf("after Down: SelectedEvent = %+v ok=%v, want ID=2", ev, ok)
	}
}

func TestTrashModel_DownClampsAtBottom(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // no-op, clamps
	ev, _ := m.SelectedEvent()
	if ev.ID != 2 {
		t.Fatalf("clamped selection = %d, want 2", ev.ID)
	}
}

func TestTrashModel_RestoreEmitsMsg(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("expected a command for 'r'")
	}
	msg, ok := cmd().(TrashRestoreRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashRestoreRequestedMsg, got %T", cmd())
	}
	if msg.ID != 1 {
		t.Fatalf("TrashRestoreRequestedMsg.ID = %d, want 1", msg.ID)
	}
}

func TestTrashModel_PurgeEmitsMsg(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("expected a command for 'x'")
	}
	msg, ok := cmd().(TrashPurgeRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashPurgeRequestedMsg, got %T", cmd())
	}
	if msg.ID != 1 {
		t.Fatalf("TrashPurgeRequestedMsg.ID = %d, want 1", msg.ID)
	}
}

func TestTrashModel_EnterEmitsViewMsg(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command for Enter")
	}
	msg, ok := cmd().(TrashViewRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashViewRequestedMsg, got %T", cmd())
	}
	if msg.Event.ID != 1 {
		t.Fatalf("TrashViewRequestedMsg.Event.ID = %d, want 1", msg.Event.ID)
	}
}

func TestTrashModel_NoActionsWhenEmpty(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Fatalf("expected nil command when list is empty, got %T", cmd())
	}
}

func TestTrashModel_SetEventsPreservesSelectionByID(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEvents(deletedFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // now on ID=2
	// Reload with ID=1 gone (as after a restore): cursor should land on ID=2.
	next := []event.Event{{ID: 2, CalendarID: 1, Title: "Standup", DeletedAt: ptrTime(time.Now())}}
	m = m.SetEvents(next, nil)
	ev, _ := m.SelectedEvent()
	if ev.ID != 2 {
		t.Fatalf("selection not preserved: got %d, want 2", ev.ID)
	}
}

