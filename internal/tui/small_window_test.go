package tui

import (
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	lipgloss "charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// TestEventDialogFitsAt100x30 guards issue #881. At 100x30 the help line
// of the day dialog wrapped and broke the frame, and the action row cut
// the Delete button.
func TestEventDialogFitsAt100x30(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)
	evs := []event.Event{{ID: 1, Title: "Team Standup", CalendarID: 1,
		StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)}}
	m := NewEventDialogModel(day, evs, map[int64]CalendarInfo{1: {Name: "Personal"}},
		newThemedHelp(ActiveTheme())).SetSize(100, 30)

	boxW, boxH := m.BoxSize()
	lines := strings.Split(m.shell.View(), "\n")
	if len(lines) != boxH {
		t.Fatalf("dialog has %d lines, want %d", len(lines), boxH)
	}
	for i, l := range lines {
		if w := lipgloss.Width(l); w != boxW {
			t.Errorf("line %d width = %d, want %d: %q", i, w, boxW, l)
		}
	}
	view := ansi.Strip(m.shell.View())
	if !strings.Contains(view, "Delete  ") || strings.Contains(view, "Delete …") || strings.Contains(view, "Duplicate …") {
		t.Errorf("the action row cuts the Delete button:\n%s", view)
	}
}

// TestActionAtPositionFollowsGap checks that the hit test of the action
// row uses the same join space as renderActions.
func TestActionAtPositionFollowsGap(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)
	evs := []event.Event{{ID: 1, Title: "Team Standup", CalendarID: 1,
		StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)}}
	m := NewEventDialogModel(day, evs, map[int64]CalendarInfo{1: {Name: "Personal"}},
		newThemedHelp(ActiveTheme())).SetSize(100, 30)
	gap := m.shell.actionGap(m.shell.actionsWidth())
	ox, oy := m.shell.actionBarOrigin()
	bs := DefaultButtonStyles()
	x := ox
	for i, a := range m.shell.actions {
		if idx, ok := m.shell.ActionAtPosition(x, oy); !ok || idx != i {
			t.Fatalf("ActionAtPosition(%d) = %d, %v, want %d", x, idx, ok, i)
		}
		x += actionButtonWidth(bs, a) + gap
	}
}

// TestEventDialogActionRowFitsAtEverySize guards issue #881. A short window
// gives a narrow golden box, so the box widens until the action row fits.
func TestEventDialogActionRowFitsAtEverySize(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)
	evs := []event.Event{{ID: 1, Title: "Team Standup", CalendarID: 1,
		StartTime: day.Add(9 * time.Hour), EndTime: day.Add(10 * time.Hour)}}
	for h := minWindowHeight; h <= 30; h++ {
		for w := minWindowWidth; w <= 160; w += 4 {
			m := NewEventDialogModel(day, evs, map[int64]CalendarInfo{1: {Name: "Personal"}},
				newThemedHelp(ActiveTheme())).SetSize(w, h)
			boxW, boxH := m.BoxSize()
			view := m.shell.View()
			lines := strings.Split(view, "\n")
			if len(lines) != boxH || boxW > w {
				t.Fatalf("%dx%d: box %dx%d, %d lines", w, h, boxW, boxH, len(lines))
			}
			for _, l := range lines {
				if lipgloss.Width(l) != boxW {
					t.Fatalf("%dx%d: line width %d, want %d: %q", w, h, lipgloss.Width(l), boxW, l)
				}
			}
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "Delete") || strings.Contains(plain, "Duplicate …") {
				t.Fatalf("%dx%d: the action row is cut:\n%s", w, h, plain)
			}
		}
	}
}

func TestShortHelpLineFitsWidth(t *testing.T) {
	h := newThemedHelp(ActiveTheme())
	bindings := []key.Binding{
		key.NewBinding(key.WithKeys("up"), key.WithHelp("↑↓", "navigate")),
		key.NewBinding(key.WithKeys("left"), key.WithHelp("←→", "days")),
		key.NewBinding(key.WithKeys("tab"), key.WithHelp("tab", "sections")),
		key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "create")),
		key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "delete")),
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close")),
	}
	for w := 10; w <= 80; w++ {
		if got := lipgloss.Width(shortHelpLine(h, bindings, w)); got > w {
			t.Errorf("width %d: line is %d cells", w, got)
		}
	}
}

// TestMonthCellOverflowLabelFits checks that a narrow month cell keeps the
// overflow label on one line.
func TestMonthCellOverflowLabelFits(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)
	evs := make([]CalendarEvent, 4)
	for i := range evs {
		evs[i] = CalendarEvent{ID: int64(i + 1), Title: "Standup", Day: day,
			StartTime: day.Add(time.Duration(9+i) * time.Hour), EndTime: day.Add(time.Duration(10+i) * time.Hour)}
	}
	cell := buildCalendarCell(day, false, false, true, evs, 6, 3, "")
	if n := len(strings.Split(cell, "\n")); n != 3 {
		t.Fatalf("cell has %d lines, want 3:\n%s", n, cell)
	}
	if !strings.Contains(cell, "+3") {
		t.Fatalf("cell lacks the overflow count:\n%s", cell)
	}
}

// TestSmallWindowShowsNotice checks that a window below the minimum size
// shows a notice instead of a broken layout.
func TestSmallWindowShowsNotice(t *testing.T) {
	m, _ := newDBBackedModel(t)
	for _, size := range [][2]int{{60, 15}, {30, 8}, {79, 40}, {120, 23}} {
		updated, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		got := updated.(Model).View().Content
		if !strings.Contains(got, "Window too small") {
			t.Errorf("%dx%d: view lacks the notice:\n%s", size[0], size[1], got)
		}
		lines := strings.Split(got, "\n")
		if len(lines) > size[1] {
			t.Errorf("%dx%d: notice has %d lines", size[0], size[1], len(lines))
		}
		for _, l := range lines {
			if lipgloss.Width(l) > size[0] {
				t.Errorf("%dx%d: line %q is too wide", size[0], size[1], l)
			}
		}
	}
	updated, _ := m.Update(tea.WindowSizeMsg{Width: minWindowWidth, Height: minWindowHeight})
	if got := updated.(Model).View().Content; strings.Contains(got, "Window too small") {
		t.Errorf("%dx%d shows the notice", minWindowWidth, minWindowHeight)
	}
}

// TestPickerInspectorClickFollowsNarrowGap checks that a click on the
// action row of the picker inspector hits each button when the row drops
// its join space.
func TestPickerInspectorClickFollowsNarrowGap(t *testing.T) {
	m := NewAccountCalendarPickerModel(pickerDiscovery(), ActiveTheme())
	bs := DefaultButtonStyles()
	total := 0
	for _, a := range m.shell.actions {
		total += actionButtonWidth(bs, a)
	}
	w, h := total, 12
	if m.shell.actionGap(w) != 0 {
		t.Fatalf("actionGap(%d) = %d, want 0", w, m.shell.actionGap(w))
	}
	y := 3 + max(h-4, 1)
	x := 0
	for i, a := range m.shell.actions {
		next, _ := m.HandleInspectorClick(x, y, w, h)
		if a.Disabled {
			x += actionButtonWidth(bs, a)
			continue
		}
		if got := next.shell.FocusedAction(); got != i || next.shell.FocusZone() != ListZoneActions {
			t.Fatalf("click at x=%d focused action %d (zone %v), want %d", x, got, next.shell.FocusZone(), i)
		}
		x += actionButtonWidth(bs, a)
	}
}

// TestSmallWindowIgnoresInput checks that a key or a click does nothing
// while the window shows the small-window notice.
func TestSmallWindowIgnoresInput(t *testing.T) {
	m, _ := newDBBackedModel(t)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 60, Height: 15})
	m = updated.(Model)
	mode := m.viewMode
	updated, cmd := m.Update(keyPressMsg("c"))
	if got := updated.(Model); got.formOpen || cmd != nil {
		t.Fatalf("c in a small window opened the form (formOpen=%v, cmd=%v)", got.formOpen, cmd != nil)
	}
	updated, cmd = m.Update(tea.MouseClickMsg{X: 70, Y: 20, Button: tea.MouseLeft})
	if updated.(Model).viewMode != mode || cmd != nil {
		t.Fatal("a click in a small window changed the view")
	}
	if _, cmd = m.Update(tea.PasteMsg{Content: "text"}); cmd != nil {
		t.Fatal("a paste in a small window produced a command")
	}
	// ctrl+c keeps the quit confirm, and the notice shows it.
	ctrlC := tea.KeyPressMsg{Code: 'c', Mod: tea.ModCtrl}
	updated, _ = m.Update(ctrlC)
	q := updated.(Model)
	if !q.confirmOpen || q.pending.kind != pendingActionQuit {
		t.Fatal("ctrl+c in a small window did not open the quit confirm")
	}
	if !strings.Contains(q.View().Content, "Quit chroncal?") {
		t.Fatalf("the notice does not show the quit confirm:\n%s", q.View().Content)
	}
	updated, cmd = q.Update(keyPressMsg("esc"))
	if cmd != nil {
		updated, _ = updated.(Model).Update(cmd())
	}
	if updated.(Model).confirmOpen {
		t.Fatal("esc did not close the quit confirm in a small window")
	}
	_, cmd = q.Update(ctrlC)
	if cmd == nil {
		t.Fatal("a second ctrl+c produced no command")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("a second ctrl+c in a small window did not quit")
	}

	// esc stops a running operation.
	cancelled := false
	m.syncing = true
	m.opCancel = func() { cancelled = true }
	updated, _ = m.Update(keyPressMsg("esc"))
	if !cancelled || !updated.(Model).opCancelled {
		t.Fatal("esc in a small window did not stop the running operation")
	}
}

func TestMonthCellOverflowKeepsCountInTinyCell(t *testing.T) {
	day := time.Date(2026, 10, 24, 0, 0, 0, 0, time.Local)
	evs := make([]CalendarEvent, 14)
	for i := range evs {
		evs[i] = CalendarEvent{ID: int64(i + 1), Title: "Standup", Day: day,
			StartTime: day.Add(time.Hour), EndTime: day.Add(2 * time.Hour)}
	}
	cell := ansi.Strip(buildCalendarCell(day, false, false, true, evs, 3, 3, ""))
	if !strings.Contains(cell, "+13") {
		t.Fatalf("cell lacks the overflow count:\n%s", cell)
	}
	cell = ansi.Strip(buildCalendarCell(day, false, false, true, evs, 2, 3, ""))
	if !strings.Contains(cell, "+…") {
		t.Fatalf("a two-cell overflow label is not cut with an ellipsis:\n%s", cell)
	}
}
