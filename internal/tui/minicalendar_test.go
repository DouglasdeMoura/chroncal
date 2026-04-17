package tui

import (
	"testing"
	"time"
)

func TestMiniMonth_ArrowAdvancesMonthAtBoundary(t *testing.T) {
	// Cursor on Jan 31. Pressing right should land on Feb 1 and advance displayMonth.
	m := NewMiniMonthModel(time.Date(2026, 1, 31, 0, 0, 0, 0, time.UTC))
	m, _ = m.moveCursor(1, 0) // right
	if got := m.cursor.Format("2006-01-02"); got != "2026-02-01" {
		t.Errorf("cursor: got %s want 2026-02-01", got)
	}
	if got := m.displayMonth.Format("2006-01"); got != "2026-02" {
		t.Errorf("displayMonth: got %s want 2026-02", got)
	}
}

func TestMiniMonth_ShiftMonthSnapsCursorToFirst(t *testing.T) {
	// After a chevron shift (or [ / ]), the cursor should land on the first
	// day of the newly displayed month so that Tab-ing into the day grid
	// picks a visible, sensible selection.
	m := NewMiniMonthModel(time.Date(2026, 4, 14, 0, 0, 0, 0, time.UTC))
	m, _ = m.shiftMonth(-1)
	if got := m.displayMonth.Format("2006-01"); got != "2026-03" {
		t.Errorf("displayMonth: got %s want 2026-03", got)
	}
	if got := m.cursor.Format("2006-01-02"); got != "2026-03-01" {
		t.Errorf("cursor should snap to first of new month: got %s", got)
	}
}

func TestMiniMonth_TodayKeySnapsBoth(t *testing.T) {
	m := NewMiniMonthModel(time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC))
	m.displayMonth = time.Date(1999, 12, 1, 0, 0, 0, 0, time.UTC)
	m, _ = m.snapToday()
	today := time.Now()
	if m.cursor.Year() != today.Year() || m.cursor.Month() != today.Month() || m.cursor.Day() != today.Day() {
		t.Errorf("cursor not today: got %s", m.cursor.Format("2006-01-02"))
	}
	if m.displayMonth.Year() != today.Year() || m.displayMonth.Month() != today.Month() {
		t.Errorf("displayMonth not today: got %s", m.displayMonth.Format("2006-01"))
	}
}
