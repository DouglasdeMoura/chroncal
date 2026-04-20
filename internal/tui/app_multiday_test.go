package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestEventsToCalendar_AllDayMultiDay(t *testing.T) {
	cals := map[int64]CalendarInfo{1: {Name: "Personal", Color: "#7C3AED"}}
	e := event.Event{
		ID:         42,
		CalendarID: 1,
		Title:      "Parents visiting",
		AllDay:     true,
		StartTime:  time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC),
	}

	out := eventsToCalendar([]event.Event{e}, cals, nil)
	if len(out) != 3 {
		t.Fatalf("expected 3 entries (5/9, 5/10, 5/11), got %d", len(out))
	}
	wantDays := []string{"2026-05-09", "2026-05-10", "2026-05-11"}
	for i, ce := range out {
		if got := ce.Day.Format("2006-01-02"); got != wantDays[i] {
			t.Errorf("entry %d: Day = %s, want %s", i, got, wantDays[i])
		}
		if ce.ID != 42 {
			t.Errorf("entry %d: ID = %d, want 42 (all entries share event ID)", i, ce.ID)
		}
		if !ce.AllDay {
			t.Errorf("entry %d: AllDay = false, want true", i)
		}
	}
}

func TestEventsToCalendar_TimedCrossMidnight(t *testing.T) {
	cals := map[int64]CalendarInfo{1: {Name: "Work", Color: "#a6e3a1"}}
	loc := time.Local
	e := event.Event{
		ID:         7,
		CalendarID: 1,
		Title:      "Overnight hackathon",
		AllDay:     false,
		StartTime:  time.Date(2026, 6, 13, 18, 0, 0, 0, loc),
		EndTime:    time.Date(2026, 6, 14, 12, 0, 0, 0, loc),
	}

	out := eventsToCalendar([]event.Event{e}, cals, nil)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries (6/13, 6/14), got %d", len(out))
	}

	day1 := out[0]
	if got := day1.Day.Format("2006-01-02"); got != "2026-06-13" {
		t.Errorf("day1 Day = %s, want 2026-06-13", got)
	}
	if day1.StartTime.Hour() != 18 {
		t.Errorf("day1 StartTime hour = %d, want 18 (original start preserved)", day1.StartTime.Hour())
	}
	if day1.EndTime.Hour() != 23 || day1.EndTime.Minute() != 59 {
		t.Errorf("day1 EndTime = %02d:%02d, want 23:59 (clipped to end-of-day)",
			day1.EndTime.Hour(), day1.EndTime.Minute())
	}

	day2 := out[1]
	if got := day2.Day.Format("2006-01-02"); got != "2026-06-14" {
		t.Errorf("day2 Day = %s, want 2026-06-14", got)
	}
	if day2.StartTime.Hour() != 0 || day2.StartTime.Minute() != 0 {
		t.Errorf("day2 StartTime = %02d:%02d, want 00:00 (clipped to midnight)",
			day2.StartTime.Hour(), day2.StartTime.Minute())
	}
	if day2.EndTime.Hour() != 12 {
		t.Errorf("day2 EndTime hour = %d, want 12 (original end preserved)", day2.EndTime.Hour())
	}
}

func TestEventsToCalendar_SingleDayUnchanged(t *testing.T) {
	cals := map[int64]CalendarInfo{1: {}}
	loc := time.Local
	e := event.Event{
		ID: 1, CalendarID: 1, Title: "Lunch",
		StartTime: time.Date(2026, 4, 20, 12, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 4, 20, 13, 0, 0, 0, loc),
	}
	out := eventsToCalendar([]event.Event{e}, cals, nil)
	if len(out) != 1 {
		t.Fatalf("single-day event should produce 1 entry, got %d", len(out))
	}
	if out[0].StartTime != e.StartTime || out[0].EndTime != e.EndTime {
		t.Errorf("single-day event start/end should be unchanged")
	}
}
