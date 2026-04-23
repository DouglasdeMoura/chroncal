package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
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

func newTrashForTest() TrashModel {
	return NewTrashModel(map[int64]CalendarInfo{1: {Name: "Work", Color: "#a6e3a1"}}, help.New()).
		SetSize(120, 30)
}

func TestTrashModel_SetEntriesPopulates(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	if m.Len() != 2 {
		t.Fatalf("Len = %d, want 2", m.Len())
	}
	e, ok := m.selectedEntry()
	if !ok || e.ID != 1 {
		t.Fatalf("selectedEntry = %+v ok=%v, want ID=1", e, ok)
	}
}

func TestTrashModel_EmptyRendersPlaceholder(t *testing.T) {
	m := newTrashForTest()
	out := m.View()
	if !strings.Contains(out, "No deleted events.") {
		t.Fatalf("View = %q, want contains placeholder", out)
	}
}

func TestTrashModel_RestoreKeyEmitsEntry(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("expected a command for 'r'")
	}
	msg, ok := cmd().(TrashRestoreRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashRestoreRequestedMsg, got %T", cmd())
	}
	if msg.Entry.ID != 1 {
		t.Fatalf("Entry.ID = %d, want 1", msg.Entry.ID)
	}
}

func TestTrashModel_PurgeKeyEmitsEntry(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'x', Text: "x"})
	if cmd == nil {
		t.Fatal("expected a command for 'x'")
	}
	msg, ok := cmd().(TrashPurgeRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashPurgeRequestedMsg, got %T", cmd())
	}
	if msg.Entry.ID != 1 {
		t.Fatalf("Entry.ID = %d, want 1", msg.Entry.ID)
	}
}

func TestTrashModel_EscEmitsCloseMsg(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a command for Esc")
	}
	if _, ok := cmd().(TrashDialogClosedMsg); !ok {
		t.Fatalf("expected TrashDialogClosedMsg, got %T", cmd())
	}
}

func TestTrashModel_DownKeyMovesSelection(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m2, _ := m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	e, _ := m2.selectedEntry()
	if e.ID != 7 {
		t.Fatalf("selected.ID = %d, want 7", e.ID)
	}
}

func TestTrashModel_DetailLinesContainKindAndInstance(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // on instance row
	out := m.View()
	if !strings.Contains(out, "Instance") {
		t.Fatalf("View = %q, want contains 'Instance'", out)
	}
}

func TestTrashModel_SetEntriesPreservesSelection(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"}) // on instance ID=7
	next := []event.TrashEntry{trashFixture()[1]}
	m = m.SetEntries(next, map[int64]CalendarInfo{1: {Name: "Work", Color: "#a6e3a1"}})
	e, _ := m.selectedEntry()
	if e.Kind != event.TrashKindInstance || e.ID != 7 {
		t.Fatalf("selection not preserved: got %+v, want Instance ID=7", e)
	}
}
