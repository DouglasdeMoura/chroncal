package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/journal"
)

// TestJournalOccurrence checks the occurrence form that a new journal
// override takes (issue #916).
func TestJournalOccurrence(t *testing.T) {
	master := journal.Journal{
		ID: 7, UID: "weekly", Summary: "Weekly log", StartDate: "2026-10-19",
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4", ExDates: "2026-11-02T00:00:00Z",
	}
	occ, ok := JournalOccurrence(master, time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC))
	if !ok {
		t.Fatal("JournalOccurrence = false, want the occurrence of 2026-10-26")
	}
	if occ.ID != 0 || occ.RecurrenceID != "2026-10-26T00:00:00Z" || occ.RecurrenceRule != "" || occ.ExDates != "" {
		t.Errorf("occurrence = %+v, want a new override form", occ)
	}
	if occ.StartDate != "2026-10-26" {
		t.Errorf("occurrence date = %q, want the date-only 2026-10-26", occ.StartDate)
	}

	for name, at := range map[string]time.Time{
		"between occurrences": time.Date(2026, 10, 27, 0, 0, 0, 0, time.UTC),
		"excluded":            time.Date(2026, 11, 2, 0, 0, 0, 0, time.UTC),
		"after the count":     time.Date(2026, 11, 16, 0, 0, 0, 0, time.UTC),
	} {
		if _, ok := JournalOccurrence(master, at); ok {
			t.Errorf("%s: JournalOccurrence = true, want false", name)
		}
	}

	single := master
	single.RecurrenceRule, single.ExDates = "", ""
	if _, ok := JournalOccurrence(single, time.Date(2026, 10, 19, 0, 0, 0, 0, time.UTC)); ok {
		t.Error("JournalOccurrence of a journal that does not recur = true, want false")
	}
}
