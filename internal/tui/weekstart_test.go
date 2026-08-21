package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/config"
)

func TestCalendarGridAnchor_WeekStart(t *testing.T) {
	// April 2026 starts on Wednesday.
	month := time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)

	sun := calendarGridAnchor(month, time.Sunday)
	if got := sun.Format("2006-01-02"); got != "2026-03-29" {
		t.Errorf("Sunday start anchor = %s, want 2026-03-29", got)
	}
	if sun.Weekday() != time.Sunday {
		t.Errorf("Sunday start weekday = %s, want Sunday", sun.Weekday())
	}

	mon := calendarGridAnchor(month, time.Monday)
	if got := mon.Format("2006-01-02"); got != "2026-03-30" {
		t.Errorf("Monday start anchor = %s, want 2026-03-30", got)
	}
	if mon.Weekday() != time.Monday {
		t.Errorf("Monday start weekday = %s, want Monday", mon.Weekday())
	}
}

func TestMiniWeekdayHeader(t *testing.T) {
	if got := miniWeekdayHeader(time.Sunday); got != "Su Mo Tu We Th Fr Sa" {
		t.Errorf("Sunday header = %q", got)
	}
	if got := miniWeekdayHeader(time.Monday); got != "Mo Tu We Th Fr Sa Su" {
		t.Errorf("Monday header = %q", got)
	}
	if w := len("Su Mo Tu We Th Fr Sa"); w != miniMonthHeaderWidth {
		t.Fatalf("fixture width %d != miniMonthHeaderWidth %d", w, miniMonthHeaderWidth)
	}
	if got := len(miniWeekdayHeader(time.Monday)); got != miniMonthHeaderWidth {
		t.Errorf("Monday header width = %d, want %d", got, miniMonthHeaderWidth)
	}
}

func TestMiniMonth_MondayWeekStartHeaderAndClick(t *testing.T) {
	// April 2026 starts on Wednesday. Monday-first layout of week row 0:
	//   Mo Tu We Th Fr Sa Su
	//         1  2  3  4  5
	day := time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)
	m := NewMiniMonthModel(day).SetWeekStart(time.Monday)
	out := stripANSI(m.View())
	if !strings.Contains(out, "Mo Tu We Th Fr Sa Su") {
		t.Fatalf("View missing Monday-first header:\n%s", out)
	}
	if strings.Contains(out, "Su Mo Tu We Th Fr Sa") {
		t.Fatalf("View still has Sunday-first header:\n%s", out)
	}

	// Click column 2 (Wednesday) of the first grid row (y=2) → April 1.
	got, cmd := m.HandleClick(2*3, 2)
	if cmd == nil {
		t.Fatal("click on April 1 returned no command")
	}
	if got.cursor.Format("2006-01-02") != "2026-04-01" {
		t.Errorf("cursor = %s, want 2026-04-01", got.cursor.Format("2006-01-02"))
	}
}

func TestWeekModel_SetWeekStartShiftsAnchor(t *testing.T) {
	// Sunday 5 April 2026.
	cursor := time.Date(2026, 4, 5, 12, 0, 0, 0, time.Local)
	m := NewWeekModel(cursor)
	m.cursor = cursor
	if got := m.WeekStartDate().Format("2006-01-02"); got != "2026-04-05" {
		t.Errorf("Sunday-start week = %s, want 2026-04-05", got)
	}
	m = m.SetWeekStart(time.Monday)
	if got := m.WeekStartDate().Format("2006-01-02"); got != "2026-03-30" {
		t.Errorf("Monday-start week = %s, want 2026-03-30", got)
	}
}

func TestToggleWeekStart_PersistsMonday(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	m := NewModel(nil, "")
	if m.weekStart != time.Sunday {
		t.Fatalf("default weekStart = %v, want Sunday", m.weekStart)
	}

	next, cmd := m.toggleWeekStart()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("month view toggle must reload events for the shifted grid")
	}
	if m.weekStart != time.Monday {
		t.Fatalf("toggled weekStart = %v, want Monday", m.weekStart)
	}
	if m.calendar.WeekStart() != time.Monday {
		t.Fatal("calendar weekStart was not applied")
	}
	if m.week.WeekStart() != time.Monday {
		t.Fatal("week view weekStart was not applied")
	}

	state := config.LoadUIState()
	if state.WeekStart != config.WeekStartMonday {
		t.Fatalf("persisted week_start = %q, want monday", state.WeekStart)
	}

	reloaded := NewModel(nil, "")
	if reloaded.weekStart != time.Monday {
		t.Fatalf("reloaded weekStart = %v, want Monday", reloaded.weekStart)
	}
}

func TestToggleWeekStart_MonthViewReloadsShiftedRange(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	m := NewModel(nil, "")
	m.viewMode = viewMonth
	// January 2023 starts on Sunday. Sunday-start grid is 1 Jan;
	// Monday-start grid is 26 Dec.
	m.calendar.month = time.Date(2023, 1, 1, 0, 0, 0, 0, time.Local)
	m.calendar.cursor = time.Date(2023, 1, 15, 0, 0, 0, 0, time.Local)

	beforeFrom, beforeTo := m.expectedEventRange()
	next, cmd := m.toggleWeekStart()
	m = next.(Model)
	if cmd == nil {
		t.Fatal("month view toggle must reload events for the shifted grid")
	}
	afterFrom, afterTo := m.expectedEventRange()
	if afterFrom.Equal(beforeFrom) && afterTo.Equal(beforeTo) {
		t.Fatalf("expectedEventRange did not change: %v–%v", afterFrom, afterTo)
	}
	wantFrom, wantTo := localSpanQueryRange(
		time.Date(2022, 12, 26, 0, 0, 0, 0, time.Local),
		time.Date(2023, 2, 6, 0, 0, 0, 0, time.Local),
	)
	if !afterFrom.Equal(wantFrom) || !afterTo.Equal(wantTo) {
		t.Fatalf("expectedEventRange = %v–%v, want %v–%v", afterFrom, afterTo, wantFrom, wantTo)
	}
}

func TestNewModel_ConfigWeekStartAppliesWhenStateEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)

	m := newModel(nil, "", time.Monday)
	if m.weekStart != time.Monday {
		t.Fatalf("weekStart = %v, want Monday from config", m.weekStart)
	}
	m.saveUIState()
	if got := config.LoadUIState().WeekStart; got != config.WeekStartMonday {
		t.Fatalf("saved week_start = %q, want monday", got)
	}
}

func TestNewModel_UIStateOverridesConfigWeekStart(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	if err := config.SaveUIState(config.UIState{ShowSidebar: true, WeekStart: "sunday"}); err != nil {
		t.Fatalf("SaveUIState: %v", err)
	}

	m := newModel(nil, "", time.Monday)
	if m.weekStart != time.Sunday {
		t.Fatalf("weekStart = %v, want Sunday from UI state", m.weekStart)
	}
}

func TestPaletteWeekStartTitleFollowsState(t *testing.T) {
	if got := weekStartPaletteTitle(time.Sunday); got != "Start Week on Monday" {
		t.Errorf("Sunday title = %q", got)
	}
	if got := weekStartPaletteTitle(time.Monday); got != "Start Week on Sunday" {
		t.Errorf("Monday title = %q", got)
	}

	cmd := paletteCommandByID(t, buildPaletteCommands(Model{weekStart: time.Monday}), "ui.week_start")
	if cmd.Title != "Start Week on Sunday" || cmd.Shortcut != "W" {
		t.Fatalf("palette command = %+v", cmd)
	}
	if _, ok := cmd.Action().(ToggleWeekStartMsg); !ok {
		t.Fatalf("action = %T, want ToggleWeekStartMsg", cmd.Action())
	}
}

func TestHelpDialog_WindowsSectionDocumentsWeekStart(t *testing.T) {
	if got := findHelpEntry(t, "Windows", "toggle week start"); got != "W" {
		t.Fatalf("week-start key = %q, want W", got)
	}
}
