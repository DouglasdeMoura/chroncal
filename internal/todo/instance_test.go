package todo

import (
	"context"
	"errors"
	"testing"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
)

// seedWeekly writes a weekly todo series with four occurrences, due on
// 2026-10-24, 2026-10-31, 2026-11-07, and 2026-11-14.
func seedWeekly(t *testing.T, svc *Service) Todo {
	t.Helper()
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: "weekly", CalendarID: 1, Summary: "Weekly review",
		DueDate: "2026-10-24", RecurrenceRule: "FREQ=WEEKLY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	return master
}

// occurrenceParams is the update form of the occurrence at 2026-10-31.
func occurrenceParams() UpdateParams {
	return UpdateParams{Summary: "Weekly review", DueDate: "2026-10-31", CalendarID: 1}
}

// TestCreateOverride_CreatesTheOverride locks in issue #887. The override
// of one occurrence is a new row with the UID of the series and the
// RECURRENCE-ID of the occurrence. The master does not change.
func TestCreateOverride_CreatesTheOverride(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)

	p := occurrenceParams()
	p.Status = "COMPLETED"
	got, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", p)
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	if got.ID == master.ID || got.UID != "weekly" || got.RecurrenceID != "2026-10-31T00:00:00Z" {
		t.Fatalf("override = %+v, want a new row of the series", got)
	}
	if got.Status != "COMPLETED" || got.PercentComplete != 100 || got.CompletedAt == "" {
		t.Errorf("override status = %q, progress %d, completed %q; want it completed", got.Status, got.PercentComplete, got.CompletedAt)
	}
	if got.RecurrenceRule != "" || got.Sequence != master.Sequence+1 || got.CalendarID != master.CalendarID {
		t.Errorf("override rule %q, sequence %d, calendar %d", got.RecurrenceRule, got.Sequence, got.CalendarID)
	}
	after, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if after.Status != master.Status || after.RecurrenceRule != master.RecurrenceRule || after.ExDates != "" {
		t.Errorf("master changed: %+v", after)
	}

	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", p); !errors.Is(err, ErrOverrideExists) {
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
	if _, err := svc.CreateOverride(context.Background(), "weekly", "2026-10-31T00:00:00Z", p); !errors.Is(err, ErrInstanceCalendarMove) {
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

	gone, err := svc.DeleteInstance(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams())
	if err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	row, err := svc.GetIncludingDeleted(ctx, gone.ID)
	if err != nil || row.DeletedAt == nil || row.RecurrenceID != "2026-10-31T00:00:00Z" {
		t.Fatalf("deleted override = %+v, %v; want it in the trash", row, err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil || m.ExDates == "" {
		t.Fatalf("master = %+v, %v; want an EXDATE", m, err)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-31T00:00:00Z")
	if err != nil || !found || !trashed || id != gone.ID {
		t.Errorf("OverrideRow = %d, %v, %v, %v; want the row in the trash", id, trashed, found, err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams()); !errors.Is(err, ErrOccurrenceInTrash) {
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
	created, err := svc.CreateOverride(ctx, "weekly", "2026-10-30T21:00:00-03:00", occurrenceParams())
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	if created.RecurrenceID != "2026-10-31T00:00:00Z" {
		t.Errorf("stored RECURRENCE-ID = %q, want the UTC form", created.RecurrenceID)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-30T21:00:00-03:00")
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
	stored, err := svc.UpsertByUID(ctx, UpsertParams{UID: "weekly", CalendarID: 1, Summary: "Imported", DueDate: "2026-10-31", RecurrenceID: "2026-10-31"})
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}
	id, trashed, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-31T00:00:00Z")
	if err != nil || !found || trashed || id != stored.ID {
		t.Errorf("OverrideRow = %d, %v, %v, %v; want the date-only override", id, trashed, found, err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams()); !errors.Is(err, ErrOverrideExists) {
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
		CalendarID: 1, Uid: "weekly", OwnerType: "todo", RemoteUrl: "/cal/weekly.ics", Etag: "e1", SyncStrategy: "sync-token",
	}); err != nil {
		t.Fatalf("UpsertSyncResource: %v", err)
	}
	if _, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams()); err != nil {
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
	if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "once", CalendarID: 1, Summary: "Once", DueDate: "2026-10-24"}); err != nil {
		t.Fatalf("seed single todo: %v", err)
	}
	for _, tc := range []struct{ uid, rid string }{
		{"weekly", "2026-10-25T00:00:00Z"}, // off the rule
		{"weekly", "2026-11-21T00:00:00Z"}, // past the COUNT
		{"once", "2026-10-24T00:00:00Z"},   // no rule
	} {
		if _, err := svc.CreateOverride(ctx, tc.uid, tc.rid, occurrenceParams()); !errors.Is(err, ErrNotOccurrence) {
			t.Errorf("CreateOverride(%s, %s) error = %v, want ErrNotOccurrence", tc.uid, tc.rid, err)
		}
	}
}

// TestCreateOverride_CopiesTheChildRows gives the override the alarms and
// the attendees of the series, so the occurrence keeps its reminder.
func TestCreateOverride_CopiesTheChildRows(t *testing.T) {
	db, q := testutil.NewTestDB(t)
	svc := NewService(db, q)
	ctx := context.Background()
	master := seedWeekly(t, svc)
	if err := svc.ReplaceAlarms(ctx, master.ID, []model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT15M", Related: "START", Description: "Review"}}); err != nil {
		t.Fatalf("ReplaceAlarms: %v", err)
	}
	if err := svc.ReplaceAttendees(ctx, master.ID, []model.Attendee{{Email: "amy@example.com", Name: "Amy", RSVPStatus: "NEEDS-ACTION", Role: "REQ-PARTICIPANT"}}); err != nil {
		t.Fatalf("ReplaceAttendees: %v", err)
	}
	masterAlarms, err := svc.ListAlarms(ctx, master.ID)
	if err != nil || len(masterAlarms) != 1 {
		t.Fatalf("master alarms = %v, %v", masterAlarms, err)
	}

	ov, err := svc.CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams())
	if err != nil {
		t.Fatalf("CreateOverride: %v", err)
	}
	alarms, err := svc.ListAlarms(ctx, ov.ID)
	if err != nil || len(alarms) != 1 || alarms[0].TriggerValue != "-PT15M" {
		t.Fatalf("override alarms = %v, %v; want the alarm of the series", alarms, err)
	}
	if alarms[0].UID == "" || alarms[0].UID == masterAlarms[0].UID {
		t.Errorf("override alarm UID = %q, master %q; want a new UID", alarms[0].UID, masterAlarms[0].UID)
	}
	atts, err := svc.ListAttendees(ctx, ov.ID)
	if err != nil || len(atts) != 1 || atts[0].Email != "amy@example.com" {
		t.Errorf("override attendees = %v, %v; want the attendee of the series", atts, err)
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
	if _, err := svc.WithTx(tx).CreateOverride(ctx, "weekly", "2026-10-31T00:00:00Z", occurrenceParams()); err != nil {
		t.Fatalf("CreateOverride in tx: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if _, _, found, err := svc.OverrideRow(ctx, "weekly", "2026-10-31T00:00:00Z"); err != nil || found {
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
		UID: "weekly", CalendarID: 1, Summary: "Weekly review",
		DueDate: "2026-10-24", RecurrenceRule: "FREQ=WEEKLY;COUNT=4", ExDates: "2026-10-31T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("seed series: %v", err)
	}
	ov, err := svc.UpsertByUID(ctx, UpsertParams{UID: "weekly", CalendarID: 1, Summary: "Override", DueDate: "2026-10-31", RecurrenceID: "2026-10-31T00:00:00Z"})
	if err != nil {
		t.Fatalf("seed override: %v", err)
	}
	if err := svc.Delete(ctx, ov.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	m, err := svc.Get(ctx, master.ID)
	if err != nil || m.ExDates != "2026-10-31T00:00:00Z" {
		t.Fatalf("master EXDATEs = %q, %v; want the one imported EXDATE", m.ExDates, err)
	}
	if _, err := q.GetTodoExdateDeleteByUIDRecurrence(ctx, storage.GetTodoExdateDeleteByUIDRecurrenceParams{Uid: "weekly", RecurrenceID: "2026-10-31T00:00:00Z"}); err == nil {
		t.Error("the delete recorded an EXDATE that it did not add")
	}
	if err := svc.RestoreByID(ctx, ov.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if m, err = svc.Get(ctx, master.ID); err != nil || m.ExDates != "2026-10-31T00:00:00Z" {
		t.Errorf("master EXDATEs after restore = %q, %v; want the imported EXDATE kept", m.ExDates, err)
	}
}
