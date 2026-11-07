package tui

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/freebusy"
	"github.com/douglasdemoura/chroncal/internal/sharefreebusy"
)

func TestValidateShareExport(t *testing.T) {
	from := time.Date(2026, 4, 22, 9, 0, 0, 0, time.Local)
	to := time.Date(2026, 4, 22, 17, 0, 0, 0, time.Local)
	if got := ValidateShareExport(from, to, "/tmp/share.ics"); got != "" {
		t.Fatalf("valid = %q", got)
	}
	if got := ValidateShareExport(to, from, "/tmp/share.ics"); got == "" {
		t.Fatal("reverse range must fail")
	}
	if got := ValidateShareExport(from, to, "/tmp/share.txt"); got == "" {
		t.Fatal("non-ics path must fail")
	}
	if got := ValidateShareExport(time.Time{}, to, "/tmp/share.ics"); got == "" {
		t.Fatal("zero From must fail")
	}
}

func TestClampWorkHoursKeepsBusinessTime(t *testing.T) {
	free := []freebusy.Period{{Start: time.Date(2026, 4, 22, 0, 0, 0, 0, time.Local), End: time.Date(2026, 4, 22, 23, 0, 0, 0, time.Local)}}
	got := sharefreebusy.ClampWorkHours(free)
	if len(got) != 1 {
		t.Fatalf("clamped = %d periods, want 1", len(got))
	}
	start := got[0].Start.In(time.Local)
	end := got[0].End.In(time.Local)
	if start.Hour() != 9 || end.Hour() != 17 {
		t.Fatalf("clamp = %v to %v, want 9 to 17", start, end)
	}
}

func TestShareCalendarIDsFilter(t *testing.T) {
	m := Model{
		calendars: map[int64]CalendarInfo{
			1: {Name: "Work", OwnerEmail: "a@example.com"},
			2: {Name: "Life", OwnerEmail: "b@example.com"},
		},
		hiddenCalendars: map[int64]bool{},
	}
	ids, _, errText := shareCalendarIDsOwners(m, "Work")
	if errText != "" || len(ids) != 1 || ids[0] != 1 {
		t.Fatalf("filter = %v %q", ids, errText)
	}
	if _, _, errText := shareCalendarIDsOwners(m, "Missing"); errText == "" {
		t.Fatal("missing calendar must fail")
	}
}

func TestParseShareTime(t *testing.T) {
	got := parseShareTime("2026-04-22T09:00:00Z")
	if got.Format("2006-01-02") != "2026-04-22" {
		t.Fatalf("parse = %v", got)
	}
	if !parseShareTime("nope").IsZero() {
		t.Fatal("bad time must be zero")
	}
}

func TestPaletteExposesShareAvailability(t *testing.T) {
	commands := buildPaletteCommands(Model{keys: defaultAppKeys()})
	share := paletteCommandByID(t, commands, "share.availability")
	if msg := share.Action(); msg != nil {
		if _, ok := msg.(ShareAvailabilityMsg); !ok {
			t.Fatalf("action = %T, want ShareAvailabilityMsg", msg)
		}
	}
}
