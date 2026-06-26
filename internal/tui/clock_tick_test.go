package tui

import (
	"testing"
	"time"
)

func TestNextClockTickDelayAlignsToNextMinute(t *testing.T) {
	now := time.Date(2026, 6, 13, 14, 5, 37, 250*int(time.Millisecond), time.Local)
	want := 22*time.Second + 750*time.Millisecond

	if got := nextClockTickDelay(now); got != want {
		t.Fatalf("nextClockTickDelay() = %v, want %v", got, want)
	}
}

func TestNextClockTickDelayNeverReturnsZero(t *testing.T) {
	now := time.Date(2026, 6, 13, 14, 5, 0, 0, time.Local)
	want := time.Minute

	if got := nextClockTickDelay(now); got != want {
		t.Fatalf("nextClockTickDelay() = %v, want %v", got, want)
	}
}

func TestModelClockTickReschedules(t *testing.T) {
	_, cmd := Model{viewMode: viewDay}.Update(clockTickMsg{})
	if cmd == nil {
		t.Fatal("clockTickMsg did not schedule the next clock tick")
	}
}

// Every view (not just day/week) needs the clock tick so the "today" cell
// highlight follows the day rollover; otherwise month/agenda freeze on the
// startup date until the user navigates.
func TestModelClockTickReschedulesInEveryView(t *testing.T) {
	for _, mode := range []viewMode{viewMonth, viewWeek, viewDay, viewAgenda} {
		_, cmd := Model{viewMode: mode}.Update(clockTickMsg{})
		if cmd == nil {
			t.Fatalf("clockTickMsg did not reschedule in view mode %v", mode)
		}
	}
}

// Leaving the app open across midnight must roll the stored "today" forward in
// every view model, not just the active one, so a later view switch carries the
// current date.
func TestModelClockTickRefreshesTodayAcrossMidnight(t *testing.T) {
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	stale := today.AddDate(0, 0, -2)

	m := Model{viewMode: viewMonth}
	m.day.today = stale
	m.week.today = stale
	m.agenda.today = stale
	m.calendar.today = stale

	next, _ := m.Update(clockTickMsg{})
	got := next.(Model)

	for name, field := range map[string]time.Time{
		"day":      got.day.today,
		"week":     got.week.today,
		"agenda":   got.agenda.today,
		"calendar": got.calendar.today,
	} {
		if !sameDay(field, today) {
			t.Errorf("%s.today = %v after clock tick, want %v", name, field, today)
		}
	}
}

func TestSwitchToClockViewSchedulesClockTick(t *testing.T) {
	next, _ := Model{viewMode: viewMonth}.switchToView(viewDay)
	m := next.(Model)

	if !m.clockTickScheduled {
		t.Fatal("switching to day view did not schedule a clock tick")
	}
}

// The clock tick now runs in every view, so switching between views must keep
// the existing tick alive rather than tearing it down.
func TestSwitchBetweenViewsKeepsClockTick(t *testing.T) {
	next, _ := Model{
		viewMode:           viewDay,
		clockTickToken:     3,
		clockTickScheduled: true,
	}.switchToView(viewAgenda)
	m := next.(Model)

	if !m.clockTickScheduled {
		t.Fatal("switching between views tore down the clock tick")
	}
	if m.clockTickToken != 3 {
		t.Fatalf("clockTickToken = %d, want 3 (existing tick reused)", m.clockTickToken)
	}
}
