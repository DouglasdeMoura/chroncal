package journal

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestUpsertByUID_NeverMovesARowToAnotherCalendar locks in issue #911. The
// key of a journal spans calendars, so an upsert into calendar B of a UID that
// calendar A holds would write the row of A. The upsert refuses it: for the
// row itself, and for a new override of a series in A. The rows of A do
// not change.
func TestUpsertByUID_NeverMovesARowToAnotherCalendar(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	other, err := q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	master, err := svc.UpsertByUID(ctx, UpsertParams{UID: "series", CalendarID: 1, Summary: "In A", RecurrenceRule: "FREQ=WEEKLY", StartDate: "2026-10-26"})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	for _, p := range []UpsertParams{
		{UID: "series", CalendarID: other.ID, Summary: "From B", RecurrenceRule: "FREQ=WEEKLY", StartDate: "2026-10-26"},
		{UID: "series", CalendarID: other.ID, Summary: "Override", RecurrenceID: "2026-11-02T00:00:00Z"},
	} {
		if _, err := svc.UpsertByUID(ctx, p); !errors.Is(err, storage.ErrUIDInOtherCalendar) {
			t.Errorf("UpsertByUID(%q) error = %v, want ErrUIDInOtherCalendar", p.RecurrenceID, err)
		}
	}
	got, err := svc.Get(ctx, master.ID)
	if err != nil || got.CalendarID != 1 || got.Summary != "In A" {
		t.Errorf("master = %+v, %v; want it unchanged in A", got, err)
	}
	if _, err := svc.GetByUIDAndRecurrenceID(ctx, "series", "2026-11-02T00:00:00Z"); err == nil {
		t.Error("the override landed")
	}
	// An upsert into the calendar of the series still works.
	if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "series", CalendarID: 1, Summary: "Edited", RecurrenceRule: "FREQ=WEEKLY", StartDate: "2026-10-26"}); err != nil {
		t.Errorf("upsert into A: %v", err)
	}
}
