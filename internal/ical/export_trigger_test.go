package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Import preserves an unparseable TRIGGER so the local alarm row survives the
// ReplaceAlarms merge, but that raw value must never reach the wire. Labelling
// it VALUE=DATE-TIME produces a VALARM strict CalDAV servers reject with 400,
// which fails the PUT for the whole resource and leaves it permanently dirty —
// far worse than omitting one alarm that could never fire anyway.
func TestExport_UnparseableTrigger_OmitsValarm(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "bad-trigger@example.com",
		Title:     "Event",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "not-a-time", Description: "Broken", Related: "START"},
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Good", Related: "START"},
		},
	}}
	data, err := ExportEvents(events, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if strings.Contains(out, "not-a-time") {
		t.Errorf("exported an unrepresentable TRIGGER value:\n%s", out)
	}
	if n := strings.Count(out, "BEGIN:VALARM"); n != 1 {
		t.Errorf("VALARM count = %d, want 1 (the valid alarm survives, the broken one is skipped)", n)
	}
	if !strings.Contains(out, "-PT15M") {
		t.Errorf("the valid alarm was dropped too:\n%s", out)
	}
}

func TestExport_UnparseableTodoTrigger_OmitsValarm(t *testing.T) {
	t.Parallel()
	todos := []todo.Todo{{
		UID:     "bad-trigger-todo@example.com",
		Summary: "Todo",
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "soon", Description: "Broken", Related: "START"},
		},
	}}
	data, err := ExportTodos(todos, "Work")
	if err != nil {
		t.Fatalf("ExportTodos: %v", err)
	}
	if out := string(data); strings.Contains(out, "BEGIN:VALARM") {
		t.Errorf("emitted a VALARM with an unrepresentable TRIGGER:\n%s", out)
	}
}

func TestExportableTrigger(t *testing.T) {
	t.Parallel()
	cases := map[string]bool{
		"-PT15M":               true,
		"PT0S":                 true,
		"+P1D":                 true,
		"20260401T100000Z":     true,
		"20260401T100000":      true,
		"2026-04-01T10:00:00Z": true,
		"":                     false,
		"not-a-time":           false,
		"soon":                 false,
		"tomorrow":             false,
		"PT15Q":                false,
		"2026-04-01":           false,
	}
	for value, want := range cases {
		if got := exportableTrigger(value); got != want {
			t.Errorf("exportableTrigger(%q) = %v, want %v", value, got, want)
		}
	}
}

// RFC 5545 §3.8.6.3: a TRIGGER given as a DATE-TIME MUST be UTC. parseAlarm
// stores a floating value when it cannot resolve a TZID (Exchange's
// "Customized Time Zone" and friends), so export has to normalize it. Emitting
// the floating form verbatim produces a VALARM strict servers reject with 400,
// wedging the whole resource — the failure exportableTrigger exists to stop.
func TestExport_FloatingDateTimeTrigger_NormalizedToUTC(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "floating-trigger@example.com",
		Title:     "Event",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{{
			Action: "DISPLAY", TriggerValue: "20260401T100000", Description: "Floating", Related: "START",
		}},
	}}
	data, err := ExportEvents(events, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	out := string(data)
	if !strings.Contains(out, "BEGIN:VALARM") {
		t.Fatalf("the alarm was dropped; a floating trigger is representable:\n%s", out)
	}
	if !strings.Contains(out, "20260401T100000Z") {
		t.Errorf("floating trigger not normalized to UTC:\n%s", out)
	}
	for _, line := range strings.Split(out, "\r\n") {
		if strings.HasPrefix(line, "TRIGGER") && strings.Contains(line, "DATE-TIME") &&
			!strings.HasSuffix(line, "Z") {
			t.Errorf("emitted a non-UTC DATE-TIME trigger, which RFC 5545 forbids: %q", line)
		}
	}
}
