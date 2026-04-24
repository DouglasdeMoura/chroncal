package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/douglasdemoura/chroncal/internal/event"
)

// scrollAgendaToTop expands the agenda window repeatedly by driving the
// top-edge preload trigger, flushing each AgendaReloadMsg by replaying
// the same event set through SetEvents. Used by tests to simulate rapid
// backward scrolling up to the max window.
func scrollAgendaToTop(t *testing.T, m AgendaModel, events []event.Event, maxPresses int) AgendaModel {
	t.Helper()
	for i := 0; i < maxPresses; i++ {
		m.scroll = 0
		m.selected = 0
		cmd := m.maybeExpandBackward()
		if cmd == nil {
			return m
		}
		m = m.SetEvents(events, nil)
	}
	return m
}

// TestMaybeExpandBackward_DoesNotSlideWindowEnd verifies the window grows
// to AgendaMaxWindow against a fixed far edge instead of sliding windowEnd
// backward. Sliding would drop events the user is actively looking at —
// the bug that made the agenda appear empty after several UP presses.
func TestMaybeExpandBackward_DoesNotSlideWindowEnd(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        1,
		Title:     "Anchor",
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24).SetEvents([]event.Event{ev}, nil)
	origEnd := m.WindowEnd()

	m = scrollAgendaToTop(t, m, []event.Event{ev}, 10)

	if !m.WindowEnd().Equal(origEnd) {
		t.Fatalf("windowEnd slid from %v to %v; backward expansion must hold the far edge",
			origEnd.Format("2006-01-02"), m.WindowEnd().Format("2006-01-02"))
	}
	if daysBetween(m.WindowStart(), m.WindowEnd()) > AgendaMaxWindow {
		t.Fatalf("window exceeded AgendaMaxWindow: %d", daysBetween(m.WindowStart(), m.WindowEnd()))
	}
	// Anchor event must still be in the window.
	if ev.StartTime.Before(m.WindowStart()) || !ev.StartTime.Before(m.WindowEnd()) {
		t.Fatalf("anchor event (%v) fell out of window [%v, %v)",
			ev.StartTime.Format("2006-01-02"),
			m.WindowStart().Format("2006-01-02"),
			m.WindowEnd().Format("2006-01-02"))
	}
}

// TestMaybeExpandBackward_RefusesAtMaxWindow verifies that once the window
// has grown to AgendaMaxWindow against the fixed far edge, further backward
// expansion is refused (no reload command emitted).
func TestMaybeExpandBackward_RefusesAtMaxWindow(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        1,
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24).SetEvents([]event.Event{ev}, nil)
	m = scrollAgendaToTop(t, m, []event.Event{ev}, 20)

	m.scroll = 0
	m.selected = 0
	if cmd := m.maybeExpandBackward(); cmd != nil {
		t.Fatalf("expected no further expansion at max window; got cmd = %T", cmd())
	}
}

// TestMaybeExpandForward_DoesNotSlideWindowStart mirrors the backward
// test: forward expansion must hold the near edge (windowStart) fixed.
func TestMaybeExpandForward_DoesNotSlideWindowStart(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        1,
		StartTime: time.Date(2026, 4, 23, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 10, 0, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24).SetEvents([]event.Event{ev}, nil)
	origStart := m.WindowStart()

	for i := 0; i < 10; i++ {
		m.scroll = len(m.rows) - 1
		if m.scroll < 0 {
			m.scroll = 0
		}
		m.selected = len(m.rows) - 1
		cmd := m.maybeExpandForward()
		if cmd == nil {
			break
		}
		m = m.SetEvents([]event.Event{ev}, nil)
	}

	if !m.WindowStart().Equal(origStart) {
		t.Fatalf("windowStart slid from %v to %v; forward expansion must hold the near edge",
			origStart.Format("2006-01-02"), m.WindowStart().Format("2006-01-02"))
	}
	if daysBetween(m.WindowStart(), m.WindowEnd()) > AgendaMaxWindow {
		t.Fatalf("window exceeded AgendaMaxWindow: %d", daysBetween(m.WindowStart(), m.WindowEnd()))
	}
}

// TestView_StickyTitleUsesFirstVisibleRowWhenNoMonthHeaderAbove verifies
// the bug from the screen recording: the user expanded the window back
// past today's real data, landing with windowStart in Dec 2025 but all
// events still in Apr 2026. The old code showed "December 2025" in the
// sticky title above April events. After the fix, the sticky should read
// the month of the first visible row.
func TestView_StickyTitleUsesFirstVisibleRowWhenNoMonthHeaderAbove(t *testing.T) {
	// Set up an agenda where windowStart has been pushed into a month
	// with no events (via repeated backward expansion), while the only
	// event still lives in the window's later portion.
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        7,
		Title:     "Pay the bills",
		StartTime: time.Date(2026, 4, 23, 13, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 13, 30, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24)
	theme := NewTheme(true)
	m = m.SetTheme(theme)
	m = m.SetEvents([]event.Event{ev}, nil)
	m = scrollAgendaToTop(t, m, []event.Event{ev}, 10)
	m.scroll = 0

	if m.WindowStart().Month() == time.April {
		t.Fatalf("test setup did not advance windowStart past April; got %v",
			m.WindowStart().Format("2006-01-02"))
	}

	out := m.View()
	// The title is the first non-empty line.
	var firstLine string
	for _, ln := range strings.Split(out, "\n") {
		if strings.TrimSpace(ln) != "" {
			firstLine = ln
			break
		}
	}
	if !strings.Contains(firstLine, "April 2026") {
		t.Fatalf("sticky title should reflect the month of visible content (April 2026), got %q",
			strings.TrimSpace(firstLine))
	}
}

// TestView_StickyTitleSkipsDuplicateInlineMonthHeader verifies that when
// the sticky title labels a month, the leading separator/monthHeader/
// separator run for that same month is skipped inline so the user
// doesn't see "April 2026" twice back-to-back.
func TestView_StickyTitleSkipsDuplicateInlineMonthHeader(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        7,
		Title:     "Pay the bills",
		StartTime: time.Date(2026, 4, 23, 13, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 13, 30, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24)
	m = m.SetTheme(NewTheme(true))
	m = m.SetEvents([]event.Event{ev}, nil)
	m = scrollAgendaToTop(t, m, []event.Event{ev}, 10)
	m.scroll = 0

	out := m.View()
	if strings.Count(out, "April 2026") > 1 {
		t.Fatalf("April 2026 rendered twice; sticky and inline should not duplicate:\n%s", out)
	}
}

// TestMaybeFillViewport_TriggersForwardExpansionWhenUnderfilled verifies
// that after a `[`/`]` jump lands on a sparse month, the agenda
// auto-expands the window forward so the next month flows in instead of
// leaving blank rows below.
func TestMaybeFillViewport_TriggersForwardExpansionWhenUnderfilled(t *testing.T) {
	jumpDay := time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local)
	// Just one event in the 30-day window — rows will be far below the
	// viewport height (40), so the agenda should want to grow forward.
	ev := event.Event{
		ID:        1,
		Title:     "Lonely event",
		StartTime: time.Date(2026, 4, 5, 9, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 5, 10, 0, 0, 0, time.Local),
	}
	m := NewAgendaModel(jumpDay).SetSize(80, 40).SetTheme(NewTheme(true))
	m = m.ResetWindow(jumpDay)
	m = m.SetEvents([]event.Event{ev}, nil)

	origEnd := m.WindowEnd()
	cmd := m.MaybeFillViewport()
	if cmd == nil {
		t.Fatal("expected MaybeFillViewport to return a reload command when rows don't fill viewport")
	}
	if _, ok := cmd().(AgendaReloadMsg); !ok {
		t.Fatalf("expected AgendaReloadMsg, got %T", cmd())
	}
	if !m.WindowEnd().After(origEnd) {
		t.Fatalf("windowEnd did not grow: was %v, now %v",
			origEnd.Format("2006-01-02"), m.WindowEnd().Format("2006-01-02"))
	}
}

// TestMaybeFillViewport_NoopWhenFull verifies the auto-fill doesn't fire
// when rows already fill the viewport — otherwise every load would spawn
// a pointless reload round-trip.
func TestMaybeFillViewport_NoopWhenFull(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	events := make([]event.Event, 30)
	for i := range events {
		day := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local).AddDate(0, 0, i)
		events[i] = event.Event{
			ID:        int64(i + 1),
			Title:     "Event",
			StartTime: day.Add(9 * time.Hour),
			EndTime:   day.Add(10 * time.Hour),
		}
	}
	m := NewAgendaModel(today).SetSize(80, 10).SetTheme(NewTheme(true))
	m = m.SetEvents(events, nil)
	if len(m.rows) <= m.viewportH() {
		t.Skipf("test precondition not met: rows=%d viewport=%d", len(m.rows), m.viewportH())
	}
	if cmd := m.MaybeFillViewport(); cmd != nil {
		t.Fatalf("expected no reload when viewport is full; got %T", cmd())
	}
}

// TestAgenda_UpPressAtTopDoesNotEmptyAgenda simulates the user's reported
// flow: press UP repeatedly from the first row; the agenda must never
// lose the only event in the dataset.
func TestAgenda_UpPressAtTopDoesNotEmptyAgenda(t *testing.T) {
	today := time.Date(2026, 4, 23, 0, 0, 0, 0, time.Local)
	ev := event.Event{
		ID:        9,
		Title:     "Weekly Yoga",
		StartTime: time.Date(2026, 4, 23, 7, 30, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 23, 8, 30, 0, 0, time.Local),
	}
	m := NewAgendaModel(today).SetSize(80, 24).SetTheme(NewTheme(true))
	m = m.SetEvents([]event.Event{ev}, nil)

	for i := 0; i < 12; i++ {
		var cmd tea.Cmd
		m, cmd = m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
		if cmd != nil {
			if _, ok := cmd().(AgendaReloadMsg); ok {
				m = m.SetEvents([]event.Event{ev}, nil)
			}
		}
	}

	if len(m.rows) == 0 {
		t.Fatal("agenda lost all rows after repeated UP presses at top")
	}
	if _, ok := m.SelectedEvent(); !ok {
		t.Fatal("selection no longer points at the anchor event after scroll-to-top loop")
	}
}
