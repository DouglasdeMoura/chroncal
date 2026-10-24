package tui

import (
	"errors"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/help"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

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
		return
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
		return
	}
	msg, ok := cmd().(TrashPurgeRequestedMsg)
	if !ok {
		t.Fatalf("expected TrashPurgeRequestedMsg, got %T", cmd())
	}
	if len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Fatalf("Entries = %+v, want single entry with ID=1", msg.Entries)
	}
}

func TestTrashModel_CheckboxesAreAlwaysVisible(t *testing.T) {
	// Empty checkbox should appear next to every row even when nothing
	// is marked. Teaches the affordance instead of hiding it behind
	// the Space keypress, matching Finder list-view's always-on
	// checkbox column and Mail's persistent edit-mode circles.
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	out := m.View()
	if !strings.Contains(out, Glyphs["checkbox.off"]) {
		t.Fatalf("empty checkbox %q not in view:\n%s", Glyphs["checkbox.off"], out)
	}
	if strings.Contains(out, Glyphs["checkbox.on"]) {
		t.Fatalf("filled checkbox %q rendered with nothing marked", Glyphs["checkbox.on"])
	}
}

func TestTrashModel_CheckboxFillsWhenMarked(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: ' ', Text: " "}) // mark row 0
	out := m.View()
	if !strings.Contains(out, Glyphs["checkbox.on"]) {
		t.Fatalf("filled checkbox %q not in view after marking:\n%s", Glyphs["checkbox.on"], out)
	}
	if !strings.Contains(out, Glyphs["checkbox.off"]) {
		t.Fatalf("empty checkbox %q missing — only the marked row should fill", Glyphs["checkbox.off"])
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
		return
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

func TestTrashModel_BottomActionsAreRestoreAndPurgeOnly(t *testing.T) {
	// Bottom action row stays focused on per-row scope: Restore and
	// Purge act on the cursor / marked set. Purge All is broader and
	// lives in the title bar so users don't conflate the two — see
	// TestTrashModel_PurgeAllLivesInTitleBar.
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	assertActionLabels := func(label string) {
		t.Helper()
		actions := m.buildActions()
		if got := len(actions); got != 2 {
			t.Fatalf("%s actions = %d, want 2 (Restore + Purge)", label, got)
		}
		if actions[0].Label != "Restore" {
			t.Errorf("%s actions[0] = %q, want Restore", label, actions[0].Label)
		}
		if actions[1].Label != "Purge" {
			t.Errorf("%s actions[1] = %q, want Purge", label, actions[1].Label)
		}
	}
	assertActionLabels("event-row")
	m, _ = m.Update(tea.KeyPressMsg{Code: 'j', Text: "j"})
	assertActionLabels("instance-row")
}

func TestTrashModel_PurgeAllLivesInTitleBar(t *testing.T) {
	// Purge All renders as the dialog's title-bar action — separated
	// from per-row Restore / Purge at the bottom, matching Photos.app
	// / Notes.app placement of "Delete All" in the trash header.
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	action := m.purgeAllTitleAction()
	if action == nil {
		t.Fatal("purgeAllTitleAction returned nil with entries present")
		return
	}
	if !strings.HasPrefix(action.Label, "Purge All (") {
		t.Errorf("title action label = %q, want prefix %q", action.Label, "Purge All (")
	}
	if !action.Danger {
		t.Errorf("title action must be Danger")
	}
}

func TestTrashModel_PurgeAllTargetsEveryEntry(t *testing.T) {
	// Purge All ignores the cursor / marked set and always operates on
	// the full visible list, even when the user has highlighted one
	// specific row. Locks the "broader scope" contract in.
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	if cmd == nil {
		t.Fatal("X (Purge All) produced no command")
		return
	}
	msg, ok := cmd().(TrashPurgeRequestedMsg)
	if !ok {
		t.Fatalf("X produced %T, want TrashPurgeRequestedMsg", cmd())
	}
	if len(msg.Entries) != 2 {
		t.Fatalf("Purge All Entries len = %d, want 2 (every trash entry)", len(msg.Entries))
	}
}

func TestTrashModel_MarkAllSelectsEveryEntry(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})

	// Restore should now act on every row, not just the cursor.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	msg := cmd().(TrashRestoreRequestedMsg)
	if len(msg.Entries) != 2 {
		t.Fatalf("after Mark All: Restore Entries len = %d, want 2", len(msg.Entries))
	}
}

func TestTrashModel_MarkAllTogglesOffOnSecondPress(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	m, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}) // select all
	m, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"}) // clear

	// Back to single-row semantics: Restore acts on the cursor only.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'r', Text: "r"})
	msg := cmd().(TrashRestoreRequestedMsg)
	if len(msg.Entries) != 1 || msg.Entries[0].ID != 1 {
		t.Fatalf("after toggle off: Entries = %+v, want single entry with ID=1", msg.Entries)
	}
}

func TestTrashModel_MarkAllNoOpOnEmptyTrash(t *testing.T) {
	m := newTrashForTest()
	m, _ = m.Update(tea.KeyPressMsg{Code: 'A', Text: "A"})
	// No entries → still nothing marked, no panic.
	if got := len(m.marked); got != 0 {
		t.Fatalf("marked = %d, want 0 (empty trash)", got)
	}
}

func TestTrashModel_PurgeAllHiddenWhenTrashEmpty(t *testing.T) {
	// No entries → no title action and no bottom-row actions. The
	// key binding is also a no-op so it never fires on an empty list.
	m := newTrashForTest()
	if got := len(m.buildActions()); got != 0 {
		t.Fatalf("empty trash actions = %d, want 0", got)
	}
	if m.purgeAllTitleAction() != nil {
		t.Errorf("empty trash should not install a title-bar Purge All button")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: 'X', Text: "X"})
	if cmd != nil {
		t.Fatalf("X with empty trash should be a no-op, got %T", cmd())
	}
}

func TestTrashModel_EscEmitsCloseMsg(t *testing.T) {
	m := newTrashForTest().SetEntries(trashFixture(), nil)
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	if cmd == nil {
		t.Fatal("expected a command for Esc")
		return
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

// TestTrashDetailLines_NamesTheRowsThatKeepAMaster: the detail pane of a
// master that the purge keeps names the live override rows that keep it. The
// user can then act on them. An entry that the purge takes has no such line.
func TestTrashDetailLines_NamesTheRowsThatKeepAMaster(t *testing.T) {
	kept := trash.Entry{Kind: trash.KindEvent, ID: 1, Title: "Series", KeptOverrideIDs: []int64{5, 6}}
	text := ansi.Strip(strings.Join(trashDetailLines(kept, CalendarInfo{}, 120, 10, nil), "\n"))
	if !strings.Contains(text, "kept: the series still has live overrides, rows 5, 6") {
		t.Errorf("detail of the kept master = %q, want the note with rows 5, 6", text)
	}
	plain := trash.Entry{Kind: trash.KindEvent, ID: 2, Title: "Single"}
	if text := ansi.Strip(strings.Join(trashDetailLines(plain, CalendarInfo{}, 120, 10, nil), "\n")); strings.Contains(text, "kept:") {
		t.Errorf("detail of an entry that the purge takes = %q, want no note", text)
	}
}

// TestTrashDetailLines_NamesTheTruncationToRestoreFirst: the detail pane of a
// truncation that a newer truncation of the series blocks names the cutoff of
// the newer one. A truncation that restores now has no such line.
func TestTrashDetailLines_NamesTheTruncationToRestoreFirst(t *testing.T) {
	cutoff := time.Date(2026, 3, 10, 9, 0, 0, 0, time.UTC)
	newer := time.Date(2026, 3, 1, 9, 0, 0, 0, time.UTC)
	blocked := trash.Entry{
		Kind: trash.KindEventSeriesTail, ID: 1, Title: "Series", CutoffTime: cutoff,
		NewerTruncationID: 2, NewerTruncationCutoff: newer,
	}
	text := ansi.Strip(strings.Join(trashDetailLines(blocked, CalendarInfo{}, 120, 10, nil), "\n"))
	want := "restore the truncation with cutoff " + newer.Local().Format("Mon, Jan 2 15:04") + " first"
	if !strings.Contains(text, want) {
		t.Errorf("detail of the blocked truncation = %q, want %q", text, want)
	}

	same := blocked
	same.NewerTruncationCutoff = cutoff
	text = ansi.Strip(strings.Join(trashDetailLines(same, CalendarInfo{}, 120, 10, nil), "\n"))
	if !strings.Contains(text, "restore the newer truncation at the same cutoff first") {
		t.Errorf("detail of a truncation blocked at the same cutoff = %q, want the same-cutoff note", text)
	}

	free := trash.Entry{Kind: trash.KindEventSeriesTail, ID: 2, Title: "Series", CutoffTime: newer}
	if text := ansi.Strip(strings.Join(trashDetailLines(free, CalendarInfo{}, 120, 10, nil), "\n")); strings.Contains(text, " first") {
		t.Errorf("detail of a truncation that restores = %q, want no restore note", text)
	}
}

// detailNoteValue returns the value of the detail line with the label, and
// the continuation lines below it. It also fails the test when any line is
// wider than w.
func detailNoteValue(t *testing.T, lines []string, label string, w, lw int) string {
	t.Helper()
	indent := strings.Repeat(" ", labelColWidth(label, lw))
	for _, line := range lines {
		plain := ansi.Strip(line)
		if got := ansi.StringWidth(plain); got > w {
			t.Errorf("w=%d lw=%d: line %q is %d cells wide", w, lw, plain, got)
		}
	}
	var values []string
	for i, line := range lines {
		plain := ansi.Strip(line)
		if !strings.HasPrefix(strings.TrimSpace(plain), label+" ") {
			continue
		}
		values = append(values, strings.TrimPrefix(strings.TrimSpace(plain), label))
		for _, cont := range lines[i+1:] {
			c := ansi.Strip(cont)
			if len(c) <= len(indent) || !strings.HasPrefix(c, indent) || c[len(indent)] == ' ' {
				break
			}
			values = append(values, c)
		}
		break
	}
	return strings.Join(strings.Fields(strings.Join(values, " ")), " ")
}

// TestTrashDetailLines_NotesWrapInANarrowPane checks that the restore-order
// note and the purge note show in full at the width of the detail pane. The
// notes wrap, so the cutoff and the row IDs at their end do not go (issue
// #871). The test covers the label width of the wide and the narrow layout.
func TestTrashDetailLines_NotesWrapInANarrowPane(t *testing.T) {
	newer := time.Date(2026, 11, 3, 13, 0, 0, 0, time.UTC)
	blocked := trash.Entry{
		Kind: trash.KindEventSeriesTail, ID: 1, Title: "Series",
		CutoffTime:        time.Date(2026, 11, 10, 13, 0, 0, 0, time.UTC),
		NewerTruncationID: 2, NewerTruncationCutoff: newer,
	}
	kept := trash.Entry{Kind: trash.KindEvent, ID: 3, Title: "Series", KeptOverrideIDs: []int64{12, 13}}
	restoreNote := "restore the truncation with cutoff " + newer.Local().Format("Mon, Jan 2 15:04") + " first"
	for _, lw := range []int{9, 11} {
		for _, w := range []int{40, 60} {
			lines := trashDetailLines(blocked, CalendarInfo{}, w, lw, nil)
			if got := detailNoteValue(t, lines, "Restore", w, lw); got != restoreNote {
				t.Errorf("w=%d lw=%d: restore note = %q, want %q", w, lw, got, restoreNote)
			}
			lines = trashDetailLines(kept, CalendarInfo{}, w, lw, nil)
			if got := detailNoteValue(t, lines, "Purge", w, lw); got != kept.KeptNote() {
				t.Errorf("w=%d lw=%d: purge note = %q, want %q", w, lw, got, kept.KeptNote())
			}
		}
	}
}

// TestDetailWrappedLines_TooNarrowForTheLabel checks a pane too narrow for
// the label column. The label shows on its own line, and the value wraps at
// the full width, so the note still shows in full (issue #871).
func TestDetailWrappedLines_TooNarrowForTheLabel(t *testing.T) {
	note := "restore the truncation with cutoff Tue, Nov 3 10:00 first"
	lines := detailWrappedLines(lipgloss.NewStyle(), "Restore", note, 11, 10)
	if got := ansi.Strip(lines[0]); got != "Restore" {
		t.Errorf("first line = %q, want the label", got)
	}
	values := make([]string, 0, len(lines)-1)
	for _, line := range lines[1:] {
		plain := ansi.Strip(line)
		if ansi.StringWidth(plain) > 10 {
			t.Errorf("line %q is wider than 10 cells", plain)
		}
		values = append(values, plain)
	}
	if got := strings.Join(values, " "); got != note {
		t.Errorf("value = %q, want %q", got, note)
	}
}

// TestTrashDetailLines_AllDayCutoffShowsTheUTCDate checks that an all-day
// series shows the date of its cutoff and of the cutoff to restore first.
// The series stores them at midnight UTC, so in local time west of UTC the
// date is the day before.
func TestTrashDetailLines_AllDayCutoffShowsTheUTCDate(t *testing.T) {
	old := time.Local
	time.Local = time.FixedZone("America/New_York", -5*60*60)
	t.Cleanup(func() { time.Local = old })

	e := trash.Entry{
		Kind: trash.KindEventSeriesTail, ID: 1, Title: "Holiday", AllDay: true,
		CutoffTime:        time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC),
		NewerTruncationID: 2, NewerTruncationCutoff: time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC),
	}
	text := ansi.Strip(strings.Join(trashDetailLines(e, CalendarInfo{}, 120, 10, nil), "\n"))
	for _, want := range []string{"Tue, Mar 10", "restore the truncation with cutoff Thu, Mar 5 first"} {
		if !strings.Contains(text, want) {
			t.Errorf("detail = %q, want %q", text, want)
		}
	}
	if strings.Contains(text, "Mar 9") || strings.Contains(text, "Mar 4") {
		t.Errorf("detail = %q names the local day before", text)
	}
}

// TestTrashActionDone_PartialRestoreReloadsAndCounts checks a bulk restore
// that fails after it restores some entries. The toast gives the count, and
// the model reloads the trash and the events, so the view shows the entries
// that remain.
func TestTrashActionDone_PartialRestoreReloadsAndCounts(t *testing.T) {
	m := NewModel(nil, "")
	next, cmd := m.handleTrashActionDone(trashActionDoneMsg{
		action: "restored", title: "2 items", err: errors.New("calendar is read-only"), done: 1, total: 2,
	})
	m = next.(Model)
	if m.toast.State() != ToastFailed || m.toast.reason != "restored 1 of 2; calendar is read-only" {
		t.Errorf("toast = %v %q, want the failed toast with the count", m.toast.State(), m.toast.reason)
	}
	if cmd == nil {
		t.Fatal("no command after a failed bulk restore")
	}
	batch, ok := cmd().(tea.BatchMsg)
	if !ok || len(batch) != 3 {
		t.Errorf("command = %T with %d commands, want a batch of the trash reload, the events reload, and the toast", cmd(), len(batch))
	}
}
