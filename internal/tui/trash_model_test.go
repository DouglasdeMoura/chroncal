package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func trashFixture() []event.TrashEntry {
	t1 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)
	return []event.TrashEntry{
		{Kind: event.TrashKindEvent, ID: 1, CalendarID: 1, Title: "Lunch", DeletedAt: t1},
		{
			Kind:         event.TrashKindInstance,
			ID:           7,
			CalendarID:   1,
			UID:          "standup",
			Title:        "Standup",
			InstanceTime: time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC),
			DeletedAt:    t2,
		},
	}
}

func TestTrashModel_SetEntriesSelectsFirst(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	m = m.SetEntries(trashFixture(), nil)
	e, ok := m.Selected()
	if !ok || e.ID != 1 || e.Kind != event.TrashKindEvent {
		t.Fatalf("Selected = %+v ok=%v, want Event ID=1", e, ok)
	}
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
}

func TestTrashModel_EmptyRendersPlaceholder(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	out := m.View()
	if !strings.Contains(out, "No deleted events.") {
		t.Fatalf("View = %q, want contains placeholder", out)
	}
}

func TestTrashModel_InstanceRowShowsOccurrenceTime(t *testing.T) {
	m := NewTrashModel().SetSize(80, 20).SetEntries(trashFixture(), nil)
	// Move to the instance row (index 1).
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	out := m.View()
	// The instance's local time formatted YYYY-MM-DD HH:MM should appear
	// somewhere in the rendered row.
	inst := time.Date(2026, 4, 21, 9, 0, 0, 0, time.UTC).Local().Format("2006-01-02 15:04")
	if !strings.Contains(out, inst) {
		t.Fatalf("View = %q, want contains %q", out, inst)
	}
}

func TestTrashModel_DownMovesSelection(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEntries(trashFixture(), nil)
	m2, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	e, ok := m2.Selected()
	if !ok || e.ID != 7 {
		t.Fatalf("after Down: Selected = %+v ok=%v, want ID=7", e, ok)
	}
}

func TestTrashModel_RestoreEmitsEntry(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("expected a command for 'r'")
	}
	msg, ok := cmd().(TrashRestoreRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashRestoreRequestedMsg, got %T", cmd())
	}
	if msg.Entry.Kind != event.TrashKindEvent || msg.Entry.ID != 1 {
		t.Fatalf("Entry = %+v, want Event ID=1", msg.Entry)
	}
}

func TestTrashModel_PurgeEmitsEntry(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // now on instance row
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("expected a command for 'x'")
	}
	msg, ok := cmd().(TrashPurgeRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashPurgeRequestedMsg, got %T", cmd())
	}
	if msg.Entry.Kind != event.TrashKindInstance || msg.Entry.ID != 7 {
		t.Fatalf("Entry = %+v, want Instance ID=7", msg.Entry)
	}
}

func TestTrashModel_EnterViewOnlyForEventKind(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEntries(trashFixture(), nil)
	// On event row → emits
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected a command for Enter on event row")
	}
	if _, ok := cmd().(TrashViewRequestedMsg); !ok {
		t.Fatalf("expected TrashViewRequestedMsg, got %T", cmd())
	}
	// Move to instance row → Enter is a no-op
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatalf("expected nil command for Enter on instance row, got %T", cmd())
	}
}

func TestTrashModel_NoActionsWhenEmpty(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd != nil {
		t.Fatalf("expected nil command when list is empty, got %T", cmd())
	}
}

func TestTrashModel_SetEntriesPreservesSelection(t *testing.T) {
	m := NewTrashModel().SetSize(60, 20).SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // on instance ID=7
	// Drop the event row: cursor should land on the still-present instance row.
	next := []event.TrashEntry{trashFixture()[1]}
	m = m.SetEntries(next, nil)
	e, _ := m.Selected()
	if e.Kind != event.TrashKindInstance || e.ID != 7 {
		t.Fatalf("selection not preserved: got %+v, want Instance ID=7", e)
	}
}
