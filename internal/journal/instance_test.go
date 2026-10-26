package journal

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// seedWeekly writes a weekly journal series with three occurrences, on
// 2026-10-19, 2026-10-26, and 2026-11-02.
func seedWeekly(t *testing.T, svc *Service) Journal {
	t.Helper()
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: "weekly", CalendarID: 1, Summary: "Weekly log",
		StartDate: "2026-10-19", RecurrenceRule: "FREQ=WEEKLY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	return master
}

// occurrenceParams is the update form of the occurrence at 2026-10-26.
func occurrenceParams() UpdateParams {
	return UpdateParams{Summary: "Weekly log", StartDate: "2026-10-26", CalendarID: 1}
}

// TestCreateOverride_CreatesTheOverride locks in issue #916. The override
// of one occurrence is a new row with the UID of the series and the
// RECURRENCE-ID of the occurrence. The master does not change.
func TestCreateOverride_CreatesTheOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)

	p := occurrenceParams()
	p.Summary = "Short week"
	got, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", p)
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	if got.ID == master.ID || got.UID != "weekly" || got.RecurrenceID != "2026-10-26T00:00:00Z" {
		t.Fatalf("override = %+v, want a new row of the series", got)
	}
	if got.Summary != "Short week" || got.Status != defaultStatus || got.Class != defaultClass {
		t.Errorf("override summary %q, status %q, class %q", got.Summary, got.Status, got.Class)
	}
	if got.RecurrenceRule != "" || got.Sequence != master.Sequence+1 || got.CalendarID != master.CalendarID {
		t.Errorf("override rule %q, sequence %d, calendar %d", got.RecurrenceRule, got.Sequence, got.CalendarID)
	}
	after, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if after.Summary != master.Summary || after.RecurrenceRule != master.RecurrenceRule || after.ExDates != "" {
		t.Errorf("master changed: %+v", after)
	}

	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", p); !errors.Is(err, ErrOverrideExists) {
		t.Errorf("second CreateOverride error = %v, want ErrOverrideExists", err)
	}
}

// TestCreateOverride_RefusesAMoveToAnotherCalendar keeps the occurrence in
// the calendar of its series.
func TestCreateOverride_RefusesAMoveToAnotherCalendar(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	seedWeekly(t, svc)
	other, err := q.CreateCalendar(context.Background(), storage.CreateCalendarParams{Name: "Other"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}

	p := occurrenceParams()
	p.CalendarID = other.ID
	if _, err := svc.CreateOverride(context.Background(), "weekly", "2026-10-26T00:00:00Z", p); !errors.Is(err, ErrInstanceCalendarMove) {
		t.Errorf("error = %v, want ErrInstanceCalendarMove", err)
	}
}

// TestDeleteInstance_HidesTheOccurrenceAndRestores deletes one occurrence
// with no override row. The occurrence goes to the trash with an EXDATE on
// the master, and RestoreByID brings it back.
func TestDeleteInstance_HidesTheOccurrenceAndRestores(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)

	gone, err := svc.DeleteInstance(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams())
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	row, err := svc.GetIncludingDeleted(ctx, gone.ID)
	if err != nil || row.DeletedAt == nil || row.RecurrenceID != "2026-10-26T00:00:00Z" {
		t.Fatalf("deleted override = %+v, %v; want it in the trash", row, err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil || m.ExDates == "" {
		t.Fatalf("master = %+v, %v; want an EXDATE", m, err)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-26T00:00:00Z")
	if err != nil || !found || !trashed || id != gone.ID {
		t.Errorf("OverrideRow = %d, %v, %v, %v; want the row in the trash", id, trashed, found, err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams()); !errors.Is(err, ErrOccurrenceInTrash) {
		t.Errorf("CreateOverride of a deleted occurrence = %v, want ErrOccurrenceInTrash", err)
	}

	if err := svc.RestoreByID(ctx, gone.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	m, err = svc.Get(ctx, master.ID)
	if err != nil || m.ExDates != "" {
		t.Errorf("master after restore = %+v, %v; want no EXDATE", m, err)
	}
}

// TestOverrideRow_TakesTheUTCForm finds an override by another spelling of
// its RECURRENCE-ID.
func TestOverrideRow_TakesTheUTCForm(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	seedWeekly(t, svc)
	created, err := svc.CreateOverride(ctx, "weekly", "2026-10-25T21:00:00-03:00", occurrenceParams())
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	if created.RecurrenceID != "2026-10-26T00:00:00Z" {
		t.Errorf("stored RECURRENCE-ID = %q, want the UTC form", created.RecurrenceID)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-25T21:00:00-03:00")
	if err != nil || !found || trashed || id != created.ID {
		t.Errorf("OverrideRow = %d, %v, %v, %v; want the live override", id, trashed, found, err)
	}
}

// TestOverrideRow_TakesTheDateOnlyForm finds an override whose stored
// RECURRENCE-ID is date-only, so a second override of the occurrence is
// never written.
func TestOverrideRow_TakesTheDateOnlyForm(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	seedWeekly(t, svc)
	stored, err := svc.UpsertByUID(ctx, UpsertParams{UID: "weekly", CalendarID: 1, Summary: "Imported", StartDate: "2026-10-26", RecurrenceID: "2026-10-26"})
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-26T00:00:00Z")
	if err != nil || !found || trashed || id != stored.ID {
		t.Errorf("OverrideRow = %d, %v, %v, %v; want the date-only override", id, trashed, found, err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams()); !errors.Is(err, ErrOverrideExists) {
		t.Errorf("CreateOverride error = %v, want ErrOverrideExists", err)
	}
}

// TestCreateOverride_MarksTheSeriesDirty checks that the new override of a
// synced series reaches the push.
func TestCreateOverride_MarksTheSeriesDirty(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	testutil.LinkCalendarToAccount(t, db)
	seedWeekly(t, svc)
	if err := q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
		CalendarID: 1, Uid: "weekly", OwnerType: "journal", RemoteUrl: "/cal/weekly.ics", Etag: "e1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams()); err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	var dirty int
	if err := db.QueryRowContext(ctx, `SELECT dirty FROM sync_resources WHERE calendar_id = 1 AND uid = 'weekly'`).Scan(&dirty); err != nil {
		t.Fatalf("read dirty: %v", err)
	}
	if dirty != 1 {
		t.Errorf("dirty = %d, want 1", dirty)
	}
}

// TestCreateOverride_RefusesATimeThatIsNotAnOccurrence checks the rule in
// the transaction: the caller cannot write an orphan override.
func TestCreateOverride_RefusesATimeThatIsNotAnOccurrence(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	seedWeekly(t, svc)
	if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "once", CalendarID: 1, Summary: "Once", StartDate: "2026-10-19"}); err != nil {
		t.Fatalf("seed single journal: %v", err)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "cancelled", CalendarID: 1, Summary: "Cancelled", StartDate: "2026-10-19", RecurrenceRule: "FREQ=WEEKLY", Status: "CANCELLED"}); err != nil {
		t.Fatalf("seed cancelled series: %v", err)
	}
	for _, tc := range []struct{ uid, rid string }{
		{"weekly", "2026-10-20T00:00:00Z"},    // off the rule
		{"weekly", "2026-11-09T00:00:00Z"},    // past the COUNT
		{"once", "2026-10-19T00:00:00Z"},      // no rule
		{"cancelled", "2026-10-26T00:00:00Z"}, // a cancelled series
	} {
		if _, err := svc.CreateOverride(ctx, tc.uid, tc.rid, occurrenceParams()); !errors.Is(err, ErrNotOccurrence) {
			t.Errorf("CreateOverride(%s, %s) error = %v, want ErrNotOccurrence", tc.uid, tc.rid, err)
		}
	}
}

// TestCreateOverride_CopiesTheChildRows gives the override the attendees,
// the comments, and the other child rows of the series.
func TestCreateOverride_CopiesTheChildRows(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)
	if err := svc.ReplaceAttendees(ctx, master.ID, []model.Attendee{{Email: "amy@example.com", Name: "Amy", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"}}); err != nil {
		t.Fatalf("ReplaceAttendees: %v", err)
	}
	if err := svc.ReplaceComments(ctx, master.ID, []string{"Keep it short"}); err != nil {
		t.Fatalf("ReplaceComments: %v", err)
	}

	ov, err := svc.CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams())
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	atts, err := svc.ListAttendees(ctx, ov.ID)
	if err != nil || len(atts) != 1 || atts[0].Email != "amy@example.com" {
		t.Errorf("override attendees = %v, %v; want the attendee of the series", atts, err)
	}
	comments, err := svc.ListComments(ctx, ov.ID)
	if err != nil || len(comments) != 1 || comments[0] != "Keep it short" {
		t.Errorf("override comments = %v, %v; want the comment of the series", comments, err)
	}
}

// TestCreateOverride_JoinsTheOuterTransaction writes nothing when the outer
// transaction of a WithTx service rolls back.
func TestCreateOverride_JoinsTheOuterTransaction(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	seedWeekly(t, svc)
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("BeginTx: %v", err)
	}
	if _, err := svc.WithTx(tx).CreateOverride(ctx, "weekly", "2026-10-26T00:00:00Z", occurrenceParams()); err != nil {
		t.Fatalf("CreateOverride in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, _, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-26T00:00:00Z"); err != nil || found {
		t.Errorf("OverrideRow after rollback = found %v, %v; want no row", found, err)
	}
}

// TestDelete_KeepsAnEXDATEThatTheMasterHeld deletes an override whose
// occurrence the master already excludes. The delete records nothing, so a
// restore keeps that EXDATE (issue #86).
func TestDelete_KeepsAnEXDATEThatTheMasterHeld(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "weekly", CalendarID: 1, Summary: "Weekly log",
		StartDate: "2026-10-19", RecurrenceRule: "FREQ=WEEKLY;COUNT=3", ExDates: "2026-10-26T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	ov, err := svc.UpsertByUID(ctx, UpsertParams{UID: "weekly", CalendarID: 1, Summary: "Override", StartDate: "2026-10-26", RecurrenceID: "2026-10-26T00:00:00Z"})
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}
	if err := svc.Delete(ctx, ov.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil || m.ExDates != "2026-10-26T00:00:00Z" {
		t.Fatalf("master EXDATEs = %q, %v; want the one imported EXDATE", m.ExDates, err)
	}
	if _, err := q.GetJournalExdateDeleteByUIDRecurrence(ctx, storage.GetJournalExdateDeleteByUIDRecurrenceParams{Uid: "weekly", RecurrenceID: "2026-10-26T00:00:00Z"}); err == nil {
		t.Error("the delete recorded an EXDATE that it did not add")
	}
	if err := svc.RestoreByID(ctx, ov.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if m, err = svc.Get(ctx, master.ID); err != nil || m.ExDates != "2026-10-26T00:00:00Z" {
		t.Errorf("master EXDATEs after restore = %q, %v; want the imported EXDATE kept", m.ExDates, err)
	}
}
