package ical

import (
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestExportJournals_OverrideRIDTakesTheTypeOfTheSeries covers issue #929.
// The RECURRENCE-ID of an override takes the value type of the DTSTART of
// its series, not of the override.
func TestExportJournals_OverrideRIDTakesTheTypeOfTheSeries(t *testing.T) {
	tests := []struct {
		name          string
		master, over  journal.Journal
		want, notWant string
	}{
		{
			name:    "timed series, date-only override",
			master:  journal.Journal{UID: "t", Summary: "Log", StartDate: "2026-10-19T09:00:00Z", RecurrenceRule: "FREQ=WEEKLY"},
			over:    journal.Journal{UID: "t", Summary: "Log", StartDate: "2026-10-27", RecurrenceID: "2026-10-26T09:00:00Z"},
			want:    "RECURRENCE-ID:20261026T090000Z",
			notWant: "VALUE=DATE:20261026",
		},
		{
			name:    "date-only series, timed override",
			master:  journal.Journal{UID: "d", Summary: "Log", StartDate: "2026-10-19", RecurrenceRule: "FREQ=WEEKLY"},
			over:    journal.Journal{UID: "d", Summary: "Log", StartDate: "2026-10-26T15:00:00Z", RecurrenceID: "2026-10-26T00:00:00Z"},
			want:    "RECURRENCE-ID;VALUE=DATE:20261026",
			notWant: "RECURRENCE-ID:20261026T000000Z",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := ExportJournals([]journal.Journal{tc.master, tc.over}, "")
			if err != nil {
				t.Fatalf("ExportJournals: %v", err)
			}
			got := string(out)
			if !strings.Contains(got, tc.want) || strings.Contains(got, tc.notWant) {
				t.Errorf("export lacks %q or has %q:\n%s", tc.want, tc.notWant, got)
			}
		})
	}
}

// TestExportTodos_OverrideRIDTakesTheTypeOfTheSeries is the todo form of
// the rule. The anchor is DTSTART, else DUE.
func TestExportTodos_OverrideRIDTakesTheTypeOfTheSeries(t *testing.T) {
	master := todo.Todo{UID: "t", Summary: "Review", DueDate: "2026-10-19T09:00:00Z", RecurrenceRule: "FREQ=WEEKLY"}
	over := todo.Todo{UID: "t", Summary: "Review", DueDate: "2026-10-27", RecurrenceID: "2026-10-26T09:00:00Z"}
	out, err := ExportTodos([]todo.Todo{master, over}, "")
	if err != nil {
		t.Fatalf("ExportTodos: %v", err)
	}
	if got := string(out); !strings.Contains(got, "RECURRENCE-ID:20261026T090000Z") {
		t.Errorf("export lacks the DATE-TIME RECURRENCE-ID of the series:\n%s", got)
	}
}

// TestExportJournals_OverrideRIDWithNoMaster keeps the rule of the override
// when the export has no master of its UID.
func TestExportJournals_OverrideRIDWithNoMaster(t *testing.T) {
	over := journal.Journal{UID: "d", Summary: "Log", StartDate: "2026-10-26", RecurrenceID: "2026-10-26T00:00:00Z"}
	out, err := ExportJournals([]journal.Journal{over}, "")
	if err != nil {
		t.Fatalf("ExportJournals: %v", err)
	}
	if got := string(out); !strings.Contains(got, "RECURRENCE-ID;VALUE=DATE:20261026") {
		t.Errorf("export lacks the DATE RECURRENCE-ID of the override:\n%s", got)
	}
}
