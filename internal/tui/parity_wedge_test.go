package tui

import (
	"strings"
	"testing"
	"time"
)

// TestPaletteExposesParityWedge locks the tranche 1 wedge.
func TestPaletteExposesParityWedge(t *testing.T) {
	commands := buildPaletteCommands(Model{keys: defaultAppKeys()})
	for _, id := range []string{"nav.today", "nav.goto", "nav.next", "nav.prev"} {
		paletteCommandByID(t, commands, id)
	}
	gotoCmd := paletteCommandByID(t, commands, "nav.goto")
	if !gotoCmd.NeedsArg {
		t.Fatal("nav.goto must need a typed argument")
	}
}

func TestParseGotoDate(t *testing.T) {
	d, ok := parseGotoDate("goto 2026-04-22")
	if !ok || d.Format("2006-01-02") != "2026-04-22" {
		t.Fatalf("parse day = %v %v", d, ok)
	}
	if _, ok := parseGotoDate("goto nope"); ok {
		t.Fatal("invalid date must fail")
	}
	if _, ok := parseGotoDate(""); ok {
		t.Fatal("empty date must fail")
	}
}

func TestSlotStartFromCursor(t *testing.T) {
	day := time.Date(2026, 4, 22, 0, 0, 0, 0, time.Local)
	if got := slotStartFromCursor(day, viewDay); !got.IsZero() {
		t.Fatalf("midnight day must be zero Start, got %v", got)
	}
	timed := time.Date(2026, 4, 22, 14, 45, 0, 0, time.Local)
	got := slotStartFromCursor(timed, viewWeek)
	if got.Format("15:04") != "14:30" {
		t.Fatalf("week slot = %v, want 14:30", got)
	}
	if got := slotStartFromCursor(timed, viewMonth); !got.IsZero() {
		t.Fatalf("month must be zero Start, got %v", got)
	}
}

func TestNewEventFormModelWithStart(t *testing.T) {
	day := time.Date(2026, 4, 22, 0, 0, 0, 0, time.Local)
	start := time.Date(2026, 4, 22, 14, 0, 0, 0, time.Local)
	m, _ := NewEventFormModelWithStart(day, start, testEventFormCalendars(), Theme{})
	if m.timeField.StartValue() != "14:00" || m.timeField.EndValue() != "14:30" {
		t.Fatalf("slot fields = %s %s", m.timeField.StartValue(), m.timeField.EndValue())
	}
	zero, _ := NewEventFormModelWithStart(day, time.Time{}, testEventFormCalendars(), Theme{})
	if strings.TrimSpace(zero.timeField.StartValue()) == "" {
		t.Fatal("zero Start must keep default time")
	}
}
