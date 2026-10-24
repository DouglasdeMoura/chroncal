package icaltransfer_test

import (
	"bytes"
	"context"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/app"
	"github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/icaltransfer"
)

// importFixture parses data and imports it into calendarID.
func importFixture(t *testing.T, a *app.App, calendarID int64, data []byte) icaltransfer.Summary {
	t.Helper()
	result, err := ical.ImportFile(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("ImportFile: %v", err)
	}
	summary := icaltransfer.Import(context.Background(), a, calendarID, &result)
	if summary.Failed != 0 {
		t.Fatalf("import Failed = %d, warnings %v", summary.Failed, summary.Warnings)
	}
	return summary
}

// sequences returns the SEQUENCE of each stored row of the fixture.
func sequences(t *testing.T, a *app.App) []int64 {
	t.Helper()
	ctx := context.Background()
	series, err := a.Events.GetByUID(ctx, "imp-series@test")
	if err != nil {
		t.Fatalf("GetByUID series: %v", err)
	}
	override, err := a.Events.GetByUIDAndRecurrenceID(ctx, "imp-series@test", "2026-11-09T09:00:00Z")
	if err != nil {
		t.Fatalf("GetByUIDAndRecurrenceID override: %v", err)
	}
	allDay, err := a.Events.GetByUID(ctx, "imp-allday@test")
	if err != nil {
		t.Fatalf("GetByUID all-day: %v", err)
	}
	td, err := a.Todos.GetByUID(ctx, "imp-todo@test")
	if err != nil {
		t.Fatalf("GetByUID todo: %v", err)
	}
	jr, err := a.Journals.GetByUID(ctx, "imp-journal@test")
	if err != nil {
		t.Fatalf("GetByUID journal: %v", err)
	}
	return []int64{series.Sequence, override.Sequence, allDay.Sequence, td.Sequence, jr.Sequence}
}

func updatedCount(s icaltransfer.Summary) int {
	return s.UpdatedEvents + s.UpdatedTodos + s.UpdatedJournals
}

func unchangedCount(s icaltransfer.Summary) int {
	return s.UnchangedEvents + s.UnchangedTodos + s.UnchangedJournals
}

// TestImport_UnchangedItemKeepsItsSequence locks in issue #883. A second
// import of the same file changes nothing. The SEQUENCE of each row stays,
// and the summary counts each item as unchanged, not as updated.
func TestImport_UnchangedItemKeepsItsSequence(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Fast", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	data, err := os.ReadFile("testdata/unchanged.ics")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	first := importFixture(t, a, cal.ID, data)
	if got := first.NewEvents + first.NewTodos + first.NewJournals; got != 5 {
		t.Fatalf("first import new = %d, want 5", got)
	}
	want := sequences(t, a)

	for run := range 2 {
		s := importFixture(t, a, cal.ID, data)
		if got := updatedCount(s); got != 0 {
			t.Errorf("run %d: updated = %d, want 0", run, got)
		}
		if got := unchangedCount(s); got != 5 {
			t.Errorf("run %d: unchanged = %d, want 5", run, got)
		}
		if got := len(s.Events) + len(s.Todos) + len(s.Journals); got != 5 {
			t.Errorf("run %d: listed items = %d, want 5", run, got)
		}
		if got := sequences(t, a); !slices.Equal(got, want) {
			t.Errorf("run %d: sequences = %v, want %v", run, got, want)
		}
	}
}

// TestImport_ChangedItemStillUpdates checks the other half of issue #883.
// A change of a field of the row, or of a child row, still updates the
// item and adds 1 to its SEQUENCE.
func TestImport_ChangedItemStillUpdates(t *testing.T) {
	ctx := context.Background()
	data, err := os.ReadFile("testdata/unchanged.ics")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	tests := []struct {
		name, old, new string
	}{
		{"row field", "SUMMARY:Imported todo", "SUMMARY:Renamed todo"},
		{"child row", "DESCRIPTION:todo rem", "DESCRIPTION:todo reminder"},
		{"category", "CATEGORIES:import,test", "CATEGORIES:import"},
		{"alarm UID", "DESCRIPTION:todo rem", "UID:todo-alarm@test\nDESCRIPTION:todo rem"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newTestApp(t)
			cal, err := a.Calendars.Create(ctx, "Fast", "", "")
			if err != nil {
				t.Fatalf("create calendar: %v", err)
			}
			importFixture(t, a, cal.ID, data)
			before := sequences(t, a)

			changed := []byte(strings.Replace(string(data), tc.old, tc.new, 1))
			s := importFixture(t, a, cal.ID, changed)
			if got := updatedCount(s); got != 1 {
				t.Errorf("updated = %d, want 1", got)
			}
			if got := unchangedCount(s); got != 4 {
				t.Errorf("unchanged = %d, want 4", got)
			}
			after := sequences(t, a)
			bumped := 0
			for i := range before {
				if after[i] != before[i] {
					bumped++
				}
			}
			if bumped != 1 {
				t.Errorf("sequences %v -> %v, want one change", before, after)
			}
		})
	}
}

// TestImport_ExportOfTheCalendarIsUnchanged imports the export of a
// calendar into the same calendar. The export carries new DTSTAMP,
// CREATED, and LAST-MODIFIED values and the UIDs of the alarms. The items
// are still unchanged.
func TestImport_ExportOfTheCalendarIsUnchanged(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Fast", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	data, err := os.ReadFile("testdata/unchanged.ics")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	completed := strings.Replace(string(data), "STATUS:NEEDS-ACTION", "STATUS:COMPLETED", 1)
	importFixture(t, a, cal.ID, []byte(completed))
	// The todo has no COMPLETED property, so the import stored the time of
	// the run. An older time proves that a later run keeps the stored time.
	if _, err := a.DB.ExecContext(ctx, `UPDATE todos SET completed_at = '2026-01-02T03:04:05Z'`); err != nil {
		t.Fatalf("age completed_at: %v", err)
	}
	want := sequences(t, a)

	exported, err := icaltransfer.ExportCalendar(ctx, a, cal.ID, cal.Name)
	if err != nil {
		t.Fatalf("ExportCalendar: %v", err)
	}
	for _, input := range [][]byte{exported.Data, []byte(completed)} {
		s := importFixture(t, a, cal.ID, input)
		if got := updatedCount(s); got != 0 {
			t.Errorf("updated = %d, want 0", got)
		}
		if got := unchangedCount(s); got != 5 {
			t.Errorf("unchanged = %d, want 5", got)
		}
		if got := sequences(t, a); !slices.Equal(got, want) {
			t.Errorf("sequences = %v, want %v", got, want)
		}
	}
}

// TestImport_UnchangedItemOfASyncedCalendarStaysClean imports the same file
// twice into a synced calendar. The second import writes nothing, so no
// item gets a dirty mark and the push sends nothing.
func TestImport_UnchangedItemOfASyncedCalendarStaysClean(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, _ := syncedPair(t, a)
	data, err := os.ReadFile("testdata/unchanged.ics")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	importFixture(t, a, cal.ID, data)
	if _, err := a.DB.ExecContext(ctx, `UPDATE sync_resources SET dirty = 0`); err != nil {
		t.Fatalf("clear dirty marks: %v", err)
	}

	s := importFixture(t, a, cal.ID, data)
	if got := unchangedCount(s); got != 5 {
		t.Errorf("unchanged = %d, want 5", got)
	}
	var dirty int
	if err := a.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM sync_resources WHERE dirty = 1`).Scan(&dirty); err != nil {
		t.Fatalf("count dirty marks: %v", err)
	}
	if dirty != 0 {
		t.Errorf("dirty resources = %d, want 0", dirty)
	}
}

// TestImport_UnchangedItemIgnoresTheOrderOfSets imports a file whose
// categories and attendees are not in the stored order. The store reads
// both in a sorted order, so the order alone is no change.
func TestImport_UnchangedItemIgnoresTheOrderOfSets(t *testing.T) {
	ctx := context.Background()
	a := newTestApp(t)
	cal, err := a.Calendars.Create(ctx, "Fast", "", "")
	if err != nil {
		t.Fatalf("create calendar: %v", err)
	}
	data, err := os.ReadFile("testdata/unchanged.ics")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	unsorted := strings.Replace(string(data), "CATEGORIES:import,test", "CATEGORIES:work,alpha", 1)
	unsorted = strings.Replace(unsorted, "ATTENDEE;CN=Carol;PARTSTAT=ACCEPTED:mailto:carol@example.com",
		"ATTENDEE;CN=Zed:mailto:zed@example.com\nATTENDEE;CN=Amy:mailto:amy@example.com\nATTENDEE;CN=Carol;PARTSTAT=ACCEPTED:mailto:carol@example.com", 1)
	if unsorted == string(data) {
		t.Fatal("fixture edit did not apply")
	}
	importFixture(t, a, cal.ID, []byte(unsorted))
	want := sequences(t, a)

	s := importFixture(t, a, cal.ID, []byte(unsorted))
	if got := unchangedCount(s); got != 5 {
		t.Errorf("unchanged = %d, want 5", got)
	}
	if got := sequences(t, a); !slices.Equal(got, want) {
		t.Errorf("sequences = %v, want %v", got, want)
	}
}
