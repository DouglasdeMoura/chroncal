package ical

import (
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Import DROPS a VALARM whose TRIGGER cannot be parsed, with a warning (see
// trigger_contract_test.go and the decision record in issue #570), so an
// unparseable value should normally never reach export. The exportableTrigger
// gate exercised here is a backstop, not the primary defense: it catches alarm
// rows written during the window in which import preserved the raw value
// verbatim, and anything a future caller stores directly. Without it,
// buildValarm would label the value VALUE=DATE-TIME, producing a VALARM strict
// CalDAV servers reject with 400, which fails the PUT for the whole resource
// and leaves it permanently dirty — far worse than omitting one alarm that
// could never fire anyway. Do not "fix" parseAlarm to preserve raw trigger
// values again; that contract was deliberately removed.
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

// RFC 5545 §3.8.6.3: the trigabs production permits only VALUE=DATE-TIME as a
// parameter on an absolute TRIGGER. Emitting RELATED=END alongside it produces
// a VALARM strict CalDAV servers (Google, Fastmail) reject with HTTP 400,
// failing the PUT for the whole resource. Reachable because parseAlarm stores
// RELATED unconditionally and the TUI alarm editor preserves Related across
// edits, so an absolute trigger can carry Related == "END" locally. The stored
// model keeps the field (inert locally); only the wire format suppresses it.
func TestExport_AbsoluteTriggerWithRelatedEnd_OmitsRelated(t *testing.T) {
	t.Parallel()
	events := []event.Event{{
		UID:       "abs-trigger-related@example.com",
		Title:     "Event",
		StartTime: time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:   time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		Alarms: []model.Alarm{
			{Action: "DISPLAY", TriggerValue: "20260401T094500Z", Description: "Absolute", Related: "END"},
			{Action: "DISPLAY", TriggerValue: "-PT15M", Description: "Duration", Related: "END"},
		},
	}}
	data, err := ExportEvents(events, "Work")
	if err != nil {
		t.Fatalf("ExportEvents: %v", err)
	}
	var absolute, durational string
	for _, line := range strings.Split(string(data), "\r\n") {
		if !strings.HasPrefix(line, "TRIGGER") {
			continue
		}
		switch {
		case strings.HasSuffix(line, "20260401T094500Z"):
			absolute = line
		case strings.HasSuffix(line, "-PT15M"):
			durational = line
		}
	}
	if absolute == "" || durational == "" {
		t.Fatalf("missing TRIGGER lines in export:\n%s", data)
	}
	if strings.Contains(absolute, "RELATED") {
		t.Errorf("absolute trigger carries a RELATED param (invalid per trigabs): %s", absolute)
	}
	if !strings.Contains(durational, "RELATED=END") {
		t.Errorf("duration trigger lost its RELATED=END param: %s", durational)
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
