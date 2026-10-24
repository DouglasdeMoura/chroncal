package timeutil

import (
	"testing"
	"time"
)

// floatingZones holds a zone west of UTC, UTC, and a zone east of UTC
// (issue #875). testutil.LocalZones holds the same set. This package
// cannot import testutil, because testutil imports storage.
var floatingZones = []*time.Location{
	time.FixedZone("America/Sao_Paulo", -3*60*60),
	time.UTC,
	time.FixedZone("Asia/Tokyo", 9*60*60),
}

func forEachZone(t *testing.T, fn func(t *testing.T)) {
	t.Helper()
	for _, loc := range floatingZones {
		t.Run(loc.String(), func(t *testing.T) {
			prev := time.Local
			time.Local = loc
			t.Cleanup(func() { time.Local = prev })
			fn(t)
		})
	}
}

// TestFloatingLocal checks that a date-only value starts at local midnight
// of its own date in every zone (issue #875). A conversion with .Local()
// puts it on the day before, west of UTC.
func TestFloatingLocal(t *testing.T) {
	forEachZone(t, func(t *testing.T) {
		stored := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
		got := FloatingLocal(stored)
		want := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)
		if !got.Equal(want) || got.Location() != time.Local {
			t.Fatalf("FloatingLocal(%s) = %s, want %s", stored, got, want)
		}
		if back := FloatingBound(got); !back.Equal(stored) {
			t.Fatalf("FloatingBound(%s) = %s, want %s", got, back, stored)
		}
	})
}

// TestWindowFor checks the window of each kind of value. The local day of
// October 27 holds the date-only value of October 27, and not the one of
// October 28.
func TestWindowFor(t *testing.T) {
	forEachZone(t, func(t *testing.T) {
		from := time.Date(2026, 10, 27, 0, 0, 0, 0, time.Local)
		to := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)

		f, tt := WindowFor(false, from, to)
		if !f.Equal(from) || !tt.Equal(to) {
			t.Fatalf("timed window = [%s, %s), want [%s, %s)", f, tt, from, to)
		}

		f, tt = WindowFor(true, from, to)
		wantFrom := time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC)
		wantTo := time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC)
		if !f.Equal(wantFrom) || !tt.Equal(wantTo) {
			t.Fatalf("date-only window = [%s, %s), want [%s, %s)", f, tt, wantFrom, wantTo)
		}

		cf, ct := CoverWindow(from, to)
		if cf.After(from) || cf.After(wantFrom) || ct.Before(to) || ct.Before(wantTo) {
			t.Fatalf("CoverWindow = [%s, %s), does not hold both windows", cf, ct)
		}
	})
}

// TestCoverWindowKeepsZeroBounds checks that a zero bound stays open.
func TestCoverWindowKeepsZeroBounds(t *testing.T) {
	forEachZone(t, func(t *testing.T) {
		to := time.Date(2026, 10, 28, 0, 0, 0, 0, time.Local)
		f, tt := CoverWindow(time.Time{}, to)
		if !f.IsZero() {
			t.Fatalf("from = %s, want zero", f)
		}
		if tt.Before(to) {
			t.Fatalf("to = %s, before %s", tt, to)
		}
		if f, _ := WindowFor(true, time.Time{}, to); !f.IsZero() {
			t.Fatalf("date-only from = %s, want zero", f)
		}
	})
}
