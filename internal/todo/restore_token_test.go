package todo

import (
	"context"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// TestRestoreByUID_KeepsAnOccurrenceThatAnEarlierDeleteHid covers issue
// #928. A restore of the series reverses the series delete only. The
// occurrence that a delete before the series delete hid stays in the
// trash, and its EXDATE stays on the master.
func TestRestoreByUID_KeepsAnOccurrenceThatAnEarlierDeleteHid(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)
	kept, err := svc.CreateOverride(ctx, "weekly", "2026-11-07T00:00:00Z", UpdateParams{Summary: "Moved", DueDate: "2026-11-08", CalendarID: 1})
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	gone, err := svc.DeleteInstance(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams())
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, "weekly"); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}

	if err := svc.RestoreByUID(ctx, "weekly"); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("the master is not live: %v", err)
	}
	if m.ExDates != "2026-10-31T00:00:00Z" {
		t.Errorf("master EXDATEs = %q, want the EXDATE of the deleted occurrence", m.ExDates)
	}
	if _, err := svc.Get(ctx, kept.ID); err != nil {
		t.Errorf("the override that the series delete hid is not live: %v", err)
	}
	row, err := svc.GetIncludingDeleted(ctx, gone.ID)
	if err != nil || row.DeletedAt == nil {
		t.Errorf("deleted occurrence = %+v, %v; want it in the trash", row, err)
	}

	// The occurrence still comes back by its ID.
	if err := svc.RestoreByID(ctx, gone.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if m, err = svc.Get(ctx, master.ID); err != nil || m.ExDates != "" {
		t.Errorf("master after the restore of the occurrence = %q, %v; want no EXDATE", m.ExDates, err)
	}
}

// TestRestoreByUID_WithALiveMasterRestoresEachOverride keeps the rule for a
// live master: a restore by UID brings back each override in the trash.
func TestRestoreByUID_WithALiveMasterRestoresEachOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)
	for _, rid := range []string{"2026-10-31T00:00:00Z", "2026-11-07T00:00:00Z"} {
		p := occurrenceParams()
		if _, err := svc.DeleteInstance(ctx, "weekly", rid, p); err != nil {
			t.Fatalf("DeleteInstance %s: %v", rid, err)
		}
	}
	if err := svc.RestoreByUID(ctx, "weekly"); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil || m.ExDates != "" {
		t.Errorf("master = %q, %v; want no EXDATE", m.ExDates, err)
	}
	overrides, err := svc.ListOverridesByUID(ctx, "weekly")
	if err != nil || len(overrides) != 2 {
		t.Errorf("live overrides = %d, %v; want 2", len(overrides), err)
	}
}
