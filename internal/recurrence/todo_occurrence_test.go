package recurrence

import (
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/todo"
)

// TestTodoOccurrence checks the occurrence form that a new todo override
// takes (issue #887).
func TestTodoOccurrence(t *testing.T) {
	master := todo.Todo{
		ID: 7, UID: "weekly", Summary: "Weekly review",
		StartDate: "2026-10-23T09:00:00Z", DueDate: "2026-10-24T09:00:00Z",
		RecurrenceRule: "FREQ=WEEKLY;COUNT=4", ExDates: "2026-11-06T09:00:00Z",
	}
	at := time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC)
	occ, ok := TodoOccurrence(master, at)
	if !ok {
		t.Fatal("TodoOccurrence = false, want the occurrence of 2026-10-30")
	}
	if occ.ID != 0 || occ.RecurrenceID != "2026-10-30T09:00:00Z" || occ.RecurrenceRule != "" || occ.ExDates != "" {
		t.Errorf("occurrence = %+v, want a new override form", occ)
	}
	if occ.StartDate != "2026-10-30T09:00:00Z" || occ.DueDate != "2026-10-31T09:00:00Z" {
		t.Errorf("occurrence dates = %s / %s, want them moved by one week", occ.StartDate, occ.DueDate)
	}

	for name, at := range map[string]time.Time{
		"between occurrences": time.Date(2026, 10, 31, 9, 0, 0, 0, time.UTC),
		"excluded":            time.Date(2026, 11, 6, 9, 0, 0, 0, time.UTC),
		"after the count":     time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC),
	} {
		if _, ok := TodoOccurrence(master, at); ok {
			t.Errorf("%s: TodoOccurrence = true, want false", name)
		}
	}

	single := master
	single.RecurrenceRule, single.ExDates = "", ""
	if _, ok := TodoOccurrence(single, time.Date(2026, 10, 23, 9, 0, 0, 0, time.UTC)); ok {
		t.Error("TodoOccurrence of a todo that does not recur = true, want false")
	}
}
