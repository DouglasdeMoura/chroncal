package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
)

func TestToggleRangeMode_AutoPinsStart(t *testing.T) {
	day := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m.openDatePicker()

	m.toggleRangeMode()
	if !m.rangeMode {
		t.Fatal("rangeMode should be true after toggle")
	}
	if !sameDay(m.rangeStart, day) {
		t.Errorf("rangeStart = %v, want %v (cursor auto-pinned)", m.rangeStart, day)
	}
	if !m.rangeEnd.IsZero() {
		t.Errorf("rangeEnd = %v, want zero (end not yet pinned)", m.rangeEnd)
	}
	if !m.rangePickEnd {
		t.Error("rangePickEnd should be true (next Enter pins end)")
	}
}

func TestPinRangeEndpoint_Cycle(t *testing.T) {
	day := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m.openDatePicker()
	m.toggleRangeMode()

	// First pin: end
	jul10 := time.Date(2026, 7, 10, 0, 0, 0, 0, time.Local)
	m.pinRangeEndpoint(jul10)
	if !sameDay(m.rangeEnd, jul10) {
		t.Errorf("after pinning end: rangeEnd = %v, want %v", m.rangeEnd, jul10)
	}
	if m.rangePickEnd {
		t.Error("rangePickEnd should flip to false after pinning end")
	}

	// Third Enter re-pins start (cycle reset)
	jul8 := time.Date(2026, 7, 8, 0, 0, 0, 0, time.Local)
	m.pinRangeEndpoint(jul8)
	if !sameDay(m.rangeStart, jul8) {
		t.Errorf("after reset pin: rangeStart = %v, want %v", m.rangeStart, jul8)
	}
	if !m.rangeEnd.IsZero() {
		t.Error("rangeEnd should be cleared when starting a new cycle")
	}
	if !m.rangePickEnd {
		t.Error("rangePickEnd should be true after re-pinning start")
	}
}

func TestCommitDatePickerSelection_RangeNormalisesOrder(t *testing.T) {
	day := time.Date(2026, 7, 10, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m.openDatePicker()
	m.toggleRangeMode()
	// Pin end = Jul 5 (earlier than start=Jul 10). Commit should swap.
	m.pinRangeEndpoint(time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local))
	m.commitDatePickerSelection()

	wantLo := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	wantHi := time.Date(2026, 7, 10, 0, 0, 0, 0, time.Local)
	if !sameDay(m.day, wantLo) {
		t.Errorf("m.day = %v, want %v (earlier endpoint becomes start)", m.day, wantLo)
	}
	if !sameDay(m.rangeEndDate, wantHi) {
		t.Errorf("m.rangeEndDate = %v, want %v (later endpoint becomes end)", m.rangeEndDate, wantHi)
	}
	if !m.rangeHasEnd {
		t.Error("rangeHasEnd should be true when endpoints differ")
	}
}

func TestCommitDatePickerSelection_SingleDayCollapses(t *testing.T) {
	day := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m.openDatePicker()
	m.toggleRangeMode()
	// Pin both endpoints to the same day.
	m.pinRangeEndpoint(day)
	m.commitDatePickerSelection()

	if m.rangeHasEnd {
		t.Error("rangeHasEnd should be false when start == end (collapses to single day)")
	}
}

func TestToggleRangeMode_TogglingOffClearsEndPin(t *testing.T) {
	day := time.Date(2026, 7, 5, 0, 0, 0, 0, time.Local)
	m, _ := NewEventFormModel(day, map[int64]CalendarInfo{1: {}}, NewTheme(true))
	m.openDatePicker()
	m.toggleRangeMode()
	m.pinRangeEndpoint(time.Date(2026, 7, 10, 0, 0, 0, 0, time.Local))

	m.toggleRangeMode() // turn off
	if m.rangeMode {
		t.Error("rangeMode should be false after second toggle")
	}
	if !m.rangeEnd.IsZero() {
		t.Error("rangeEnd should be cleared when range mode turns off")
	}
}

func TestMultiDayEndDate_AllDayMultiDay(t *testing.T) {
	ev := event.Event{
		AllDay:    true,
		StartTime: time.Date(2026, 5, 9, 0, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 5, 12, 0, 0, 0, 0, time.UTC), // exclusive end
	}
	got, ok := multiDayEndDate(ev)
	if !ok {
		t.Fatal("expected multiDay detection for all-day 5/9–5/11")
	}
	want := time.Date(2026, 5, 11, 0, 0, 0, 0, time.UTC)
	if !sameDay(got, want) {
		t.Errorf("got last day %v, want %v", got, want)
	}
}

func TestMultiDayEndDate_TimedCrossMidnight(t *testing.T) {
	loc := time.Local
	ev := event.Event{
		AllDay:    false,
		StartTime: time.Date(2026, 6, 13, 18, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 6, 14, 12, 0, 0, 0, loc),
	}
	got, ok := multiDayEndDate(ev)
	if !ok {
		t.Fatal("expected multiDay detection for timed cross-midnight")
	}
	want := time.Date(2026, 6, 14, 0, 0, 0, 0, loc)
	if !sameDay(got, want) {
		t.Errorf("got last day %v, want %v", got, want)
	}
}

func TestMultiDayEndDate_SingleDayReturnsFalse(t *testing.T) {
	ev := event.Event{
		StartTime: time.Date(2026, 4, 20, 12, 0, 0, 0, time.Local),
		EndTime:   time.Date(2026, 4, 20, 13, 0, 0, 0, time.Local),
	}
	if _, ok := multiDayEndDate(ev); ok {
		t.Error("single-day event should not be detected as multi-day")
	}
}

func TestMultiDayEndDate_EndAtMidnightDoesNotCountNextDay(t *testing.T) {
	// Timed event ending exactly at midnight of next day has exclusive
	// semantics: it does not touch that next day, so it's single-day.
	loc := time.Local
	ev := event.Event{
		StartTime: time.Date(2026, 6, 13, 18, 0, 0, 0, loc),
		EndTime:   time.Date(2026, 6, 14, 0, 0, 0, 0, loc),
	}
	if _, ok := multiDayEndDate(ev); ok {
		t.Error("event ending at midnight of next day should be single-day")
	}
}

func TestMiniMonth_InRangeInclusive(t *testing.T) {
	mm := NewMiniMonthModel(time.Date(2026, 4, 1, 0, 0, 0, 0, time.Local))
	start := time.Date(2026, 4, 16, 0, 0, 0, 0, time.Local)
	end := time.Date(2026, 4, 20, 0, 0, 0, 0, time.Local)
	mm = mm.SetRange(true, start, end)

	cases := []struct {
		day  time.Time
		want bool
	}{
		{time.Date(2026, 4, 15, 0, 0, 0, 0, time.Local), false},
		{start, true},
		{time.Date(2026, 4, 18, 0, 0, 0, 0, time.Local), true},
		{end, true},
		{time.Date(2026, 4, 21, 0, 0, 0, 0, time.Local), false},
	}
	for _, tc := range cases {
		if got := mm.inRangeInclusive(tc.day); got != tc.want {
			t.Errorf("inRangeInclusive(%s) = %v, want %v",
				tc.day.Format("2006-01-02"), got, tc.want)
		}
	}
}

func TestDatePickerField_FormatRange(t *testing.T) {
	f := NewDatePickerField(time.Date(2026, 4, 16, 0, 0, 0, 0, time.UTC))
	f.SetRangeEnd(time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC))
	got := f.Value()
	want := "Apr 16 → 24, 2026"
	if got != want {
		t.Errorf("same-month range: got %q, want %q", got, want)
	}

	f.SetRangeEnd(time.Date(2026, 5, 2, 0, 0, 0, 0, time.UTC))
	got = f.Value()
	want = "Apr 16 → May 2, 2026"
	if got != want {
		t.Errorf("cross-month same-year range: got %q, want %q", got, want)
	}

	f.SetRangeEnd(time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	got = f.Value()
	want = "Apr 16, 2026 → Jan 2, 2027"
	if got != want {
		t.Errorf("cross-year range: got %q, want %q", got, want)
	}

	f.ClearRangeEnd()
	got = f.Value()
	want = "Thu, Apr 16, 2026"
	if got != want {
		t.Errorf("single date after clear: got %q, want %q", got, want)
	}
}
