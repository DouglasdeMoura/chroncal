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

func TestModelClockTickIgnoredOutsideClockViews(t *testing.T) {
	_, cmd := Model{viewMode: viewMonth}.Update(clockTickMsg{})
	if cmd != nil {
		t.Fatal("clockTickMsg scheduled another tick outside day/week views")
	}

	_, cmd = Model{viewMode: viewAgenda}.Update(clockTickMsg{})
	if cmd != nil {
		t.Fatal("clockTickMsg scheduled another tick outside day/week views")
	}
}

func TestSwitchToClockViewSchedulesClockTick(t *testing.T) {
	next, _ := Model{viewMode: viewMonth}.switchToView(viewDay)
	m := next.(Model)

	if !m.clockTickScheduled {
		t.Fatal("switching to day view did not schedule a clock tick")
	}
}

func TestSwitchAwayFromClockViewDisablesClockTick(t *testing.T) {
	next, _ := Model{
		viewMode:           viewDay,
		clockTickToken:     3,
		clockTickScheduled: true,
	}.switchToView(viewAgenda)
	m := next.(Model)

	if m.clockTickScheduled {
		t.Fatal("switching away from day/week left the clock tick scheduled")
	}
	if m.clockTickToken != 4 {
		t.Fatalf("clockTickToken = %d, want 4", m.clockTickToken)
	}
}
