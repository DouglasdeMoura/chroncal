package tui

import (
	"testing"
	"time"
)

// TestExpectedEventRange_DayCoversLocalEvening guards against the day view
// that hides a late-evening event in a UTC-negative zone. The database
// stores a 22:30 local event (UTC-3) as 01:30 UTC on the next date. A
// query range built from UTC midnights excludes that instant. The range
// must extend to the local midnight converted to UTC.
func TestExpectedEventRange_DayCoversLocalEvening(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("America/Sao_Paulo", -3*60*60)
	defer func() { time.Local = orig }()

	cursor := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	m := Model{viewMode: viewDay, day: DayModel{cursor: cursor}}

	from, to := m.expectedEventRange()

	// All-day events keep their 00:00 UTC datestamp coverage.
	wantFrom := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	// Timed events run until the local midnight, which is 03:00 UTC.
	wantTo := time.Date(2026, 8, 15, 3, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) {
		t.Fatalf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Fatalf("to = %v, want %v", to, wantTo)
	}

	// The reported bug: an event at 22:30 local is 01:30 UTC the next date.
	evStart := time.Date(2026, 8, 15, 1, 30, 0, 0, time.UTC)
	if !evStart.Before(to) || evStart.Before(from) {
		t.Fatalf("range [%v, %v) does not cover the 22:30 local event at %v", from, to, evStart)
	}
}

// TestExpectedEventRange_DayCoversLocalEarlyMorning guards the mirror case
// for a UTC-positive zone. An early-morning local event maps to the
// previous UTC date. The range must start at the local midnight converted
// to UTC.
func TestExpectedEventRange_DayCoversLocalEarlyMorning(t *testing.T) {
	orig := time.Local
	time.Local = time.FixedZone("Europe/Helsinki", 3*60*60)
	defer func() { time.Local = orig }()

	cursor := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	m := Model{viewMode: viewDay, day: DayModel{cursor: cursor}}

	from, to := m.expectedEventRange()

	// Timed events start at the local midnight, which is 21:00 UTC the
	// previous date.
	wantFrom := time.Date(2026, 8, 13, 21, 0, 0, 0, time.UTC)
	// All-day events keep their 00:00 UTC datestamp coverage on the far edge.
	wantTo := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) {
		t.Fatalf("from = %v, want %v", from, wantFrom)
	}
	if !to.Equal(wantTo) {
		t.Fatalf("to = %v, want %v", to, wantTo)
	}

	// An event at 00:30 local is 21:30 UTC the previous date.
	evStart := time.Date(2026, 8, 13, 21, 30, 0, 0, time.UTC)
	if !evStart.Before(to) || evStart.Before(from) {
		t.Fatalf("range [%v, %v) does not cover the 00:30 local event at %v", from, to, evStart)
	}
}

// TestLocalSpanQueryRange_UTCStaysExact verifies the union collapses to the
// plain UTC-midnight span when the local zone is UTC.
func TestLocalSpanQueryRange_UTCStaysExact(t *testing.T) {
	orig := time.Local
	time.Local = time.UTC
	defer func() { time.Local = orig }()

	day := time.Date(2026, 8, 14, 0, 0, 0, 0, time.Local)
	from, to := localSpanQueryRange(day, day.AddDate(0, 0, 1))

	wantFrom := time.Date(2026, 8, 14, 0, 0, 0, 0, time.UTC)
	wantTo := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	if !from.Equal(wantFrom) || !to.Equal(wantTo) {
		t.Fatalf("range = [%v, %v), want [%v, %v)", from, to, wantFrom, wantTo)
	}
}
