package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/trash"
)

func trashFixture() []trash.Entry {
	t1 := time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 4, 21, 9, 30, 0, 0, time.UTC)
	return []trash.Entry{
		{Kind: trash.KindEvent, ID: 1, CalendarID: 1, Title: "Lunch", DeletedAt: t1},
		{
			Kind:         trash.KindEventInstance,
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
	if !strings.Contains(out, "Nothing in the trash.") {
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
	if len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Fatalf("Entries = %+v, want single entry with ID=1", msg.Entries)
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
	if len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Fatalf("Entries = %+v, want single entry with ID=1", msg.Entries)
	}
}

func TestTrashModel_SpaceTogglesMark(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	// Mark row 0 (Event ID=1), move to row 1 and mark it too.
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})

	// Restore should now emit both entries.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if cmd == nil {
		t.Fatal("expected a command for 'r' after marking")
	}
	msg, ok := cmd().(TrashRestoreRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashRestoreRequestedMsg, got %T", cmd())
	}
	if len(msg.Entries) != 2 {
		t.Fatalf("Entries len = %d, want 2 (both marked rows)", len(msg.Entries))
	}
}

func TestTrashModel_SpaceUntoggles(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // mark row 0
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // unmark row 0
	// Back to single-row semantics: restore emits only the cursor entry.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	msg := cmd().(TrashRestoreRequestedMsg)
	if len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Fatalf("Entries = %+v, want single entry with ID=1", msg.Entries)
	}
}

func TestTrashModel_ClearMarksWipesSet(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "})
	m = m.ClearMarks()
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	msg := cmd().(TrashRestoreRequestedMsg)
	if len(msg.Entries) != 1 {
		t.Fatalf("after ClearMarks: Entries len = %d, want 1", len(msg.Entries))
	}
}

func TestTrashModel_ActionsAreRestoreAndPurgeOnly(t *testing.T) {
	// The trash dialog shows all necessary info inline; no secondary View
	// action is surfaced, regardless of entry kind.
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	if got := len(m.buildActions()); got != 2 {
		t.Fatalf("event-row actions = %d, want 2 (Restore + Purge)", got)
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	if got := len(m.buildActions()); got != 2 {
		t.Fatalf("instance-row actions = %d, want 2 (Restore + Purge)", got)
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
	next := []trash.Entry{trashFixture()[1]}
	m = m.SetEntries(next, map[int64]CalendarInfo{1: {Name: "Work", Color: "#a6e3a1"}})
	e, _ := m.selectedEntry()
	if e.Kind != trash.KindEventInstance || e.ID != 7 {
		t.Fatalf("selection not preserved: got %+v, want Instance ID=7", e)
	}
}
