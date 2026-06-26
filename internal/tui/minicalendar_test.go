package tui

import (
	"image/color"
	"strings"
	"testing"
	"time"

	lipgloss "charm.land/lipgloss/v2"
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

// TestMiniMonth_RangeColorAppliedToMiddleDays verifies that SetRangeColor is
// honoured by View: in-range (non-endpoint) days must render with the
// configured rangeColor background, not with the same cursor/endpoint style.
//
// Before the fix, View collapsed the isEndpoint and isInRange cases into one
// identical Background(activeTheme.Text) block and never consulted
// m.rangeColor, so the rangeColor ANSI escape code never appeared in the
// output. After the fix the two cases are separate and middle days use
// m.rangeColor.
func TestMiniMonth_RangeColorAppliedToMiddleDays(t *testing.T) {
	// April 2026, cursor at Apr 10 (outside the range so it does not
	// interfere with endpoint styling). Range: Apr 16 – Apr 20.
	// Middle days Apr 17–Apr 19 must use rangeColor; endpoints Apr 16
	// and Apr 20 use the accent style.
	rangeColor := color.RGBA{R: 0x80, G: 0x00, B: 0xff, A: 0xff} // #8000ff – distinctive purple
	mm := NewMiniMonthModel(time.Date(2026, 4, 10, 0, 0, 0, 0, time.UTC)).
		SetRangeColor(rangeColor).
		SetRange(true,
			time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC),
			time.Date(2026, 4, 20, 0, 0, 0, 0, time.UTC))

	out := mm.View()

	// Derive the expected ANSI background escape from a lipgloss dry-run so
	// the test stays independent of the exact SGR byte sequence.
	rangeStyled := lipgloss.NewStyle().Background(rangeColor).Render("x")
	bgEsc := strings.SplitN(rangeStyled, "x", 2)[0]

	if !strings.Contains(out, bgEsc) {
		t.Errorf("View() does not apply rangeColor to in-range days;\n"+
			"ANSI code %q not found in output:\n%q", bgEsc, out)
	}
}
