package event

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/model"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

func newRecurringMaster(t *testing.T, svc *Service, uid string, rule string) Event {
	t.Helper()
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID:            uid,
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 5, 18, 9, 0, 0, 0, time.UTC), // Mon
		EndTime:        time.Date(2026, 5, 18, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: rule,
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	return master
}

func TestUpdateInstance_CreatesOverrideAndLeavesMasterIntact(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "scope-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC) // Wed instance
	_, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Standup (moved)",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	override, err := svc.GetByUIDAndRecurrenceID(ctx, master.UID, "2026-05-20T09:00:00Z")
	if err != nil {
		t.Fatalf("override not found: %v", err)
	}
	if override.Title != "Standup (moved)" {
		t.Errorf("override.Title = %q, want %q", override.Title, "Standup (moved)")
	}
	if !override.StartTime.Equal(time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC)) {
		t.Errorf("override.StartTime = %v, want 9:30", override.StartTime)
	}
	if override.RecurrenceRule != "" {
		t.Errorf("override.RecurrenceRule = %q, want empty", override.RecurrenceRule)
	}

	fresh, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if fresh.Title != master.Title {
		t.Errorf("master.Title changed: got %q, want %q", fresh.Title, master.Title)
	}
	if !fresh.StartTime.Equal(master.StartTime) {
		t.Errorf("master.StartTime changed: got %v, want %v", fresh.StartTime, master.StartTime)
	}
	if fresh.RecurrenceRule != master.RecurrenceRule {
		t.Errorf("master.RecurrenceRule changed: got %q, want %q", fresh.RecurrenceRule, master.RecurrenceRule)
	}
}

func TestUpdateInstance_UpdatesExistingOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "scope-uid-2", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)

	// First edit creates the override.
	if _, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "First edit",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("first UpdateInstance: %v", err)
	}

	// Second edit reuses the same override row.
	if _, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Second edit",
		StartTime:  time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 30, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("second UpdateInstance: %v", err)
	}

	overrides, err := svc.ListSeriesOverrides(ctx, master.CalendarID, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("expected 1 override, got %d", len(overrides))
	}
	if overrides[0].Title != "Second edit" {
		t.Errorf("override.Title = %q, want %q", overrides[0].Title, "Second edit")
	}
}

func TestUpdateFromInstance_TruncatesMasterAndCreatesSplit(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "split-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	cutoff := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC) // Wed of next week

	split, err := svc.UpdateFromInstance(ctx, master.CalendarID, master.UID, cutoff, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "Standup (new room)",
		StartTime:      cutoff,
		EndTime:        cutoff.Add(45 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	})
	if err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	// Original master is truncated to before the cutoff.
	old, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get old master: %v", err)
	}
	if !strings.Contains(strings.ToUpper(old.RecurrenceRule), "UNTIL=") {
		t.Errorf("expected UNTIL on truncated master, got %q", old.RecurrenceRule)
	}
	if strings.Contains(strings.ToUpper(old.RecurrenceRule), "COUNT=") {
		t.Errorf("expected COUNT to be stripped from truncated master, got %q", old.RecurrenceRule)
	}

	// Split row exists with the new title and a fresh UID.
	if split.UID == master.UID {
		t.Errorf("split UID should differ from old master UID")
	}
	if split.Title != "Standup (new room)" {
		t.Errorf("split.Title = %q", split.Title)
	}
	if split.RecurrenceRule != "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH" {
		t.Errorf("split rule lost: got %q", split.RecurrenceRule)
	}
	if !split.StartTime.Equal(cutoff) {
		t.Errorf("split.StartTime = %v, want %v", split.StartTime, cutoff)
	}
}

func TestUpdateFromInstance_SoftDeletesFutureOverrides(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "split-uid-2", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	// Override before cutoff — should survive.
	pastInstance := time.Date(2026, 5, 19, 9, 0, 0, 0, time.UTC) // Tue
	if _, err := svc.UpdateInstance(ctx, master.UID, pastInstance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Tue override (kept)",
		StartTime:  pastInstance,
		EndTime:    pastInstance.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("create past override: %v", err)
	}

	// Override at/after cutoff — should be soft-deleted.
	futureInstance := time.Date(2026, 5, 28, 9, 0, 0, 0, time.UTC) // Thu next week
	if _, err := svc.UpdateInstance(ctx, master.UID, futureInstance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Thu override (dropped)",
		StartTime:  futureInstance,
		EndTime:    futureInstance.Add(30 * time.Minute),
	}); err != nil {
		t.Fatalf("create future override: %v", err)
	}

	cutoff := time.Date(2026, 5, 27, 9, 0, 0, 0, time.UTC) // Wed next week
	if _, err := svc.UpdateFromInstance(ctx, master.CalendarID, master.UID, cutoff, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "split",
		StartTime:      cutoff,
		EndTime:        cutoff.Add(30 * time.Minute),
		RecurrenceRule: "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH",
	}); err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	overrides, err := svc.ListSeriesOverrides(ctx, master.CalendarID, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 1 {
		t.Fatalf("expected 1 surviving override, got %d", len(overrides))
	}
	if overrides[0].Title != "Tue override (kept)" {
		t.Errorf("wrong override survived: got %q", overrides[0].Title)
	}
}

// A truncate of an all-day recurring series must emit a DATE-valued UNTIL
// (YYYYMMDD). The RRULE's UNTIL value type then matches its DATE-valued DTSTART,
// as RFC 5545 requires. A DATE-TIME UNTIL on an all-day series is rejected or
// mis-handled by strict CalDAV servers.
func TestDeleteFromInstance_AllDayEmitsDateOnlyUntil(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "allday-trunc-1",
		CalendarID:     1,
		Title:          "Daily all-day",
		StartTime:      time.Date(2026, 5, 18, 0, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC),
		AllDay:         true,
		RecurrenceRule: "FREQ=DAILY",
	})
	if err != nil {
		t.Fatalf("upsert all-day master: %v", err)
	}

	// Drop the May 20 occurrence and everything after it; May 19 is the last
	// kept instance.
	cutoff := time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC)
	if err := svc.DeleteFromInstance(ctx, master.CalendarID, master.UID, cutoff); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}

	fresh, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get truncated master: %v", err)
	}

	until := rruleParamValue(fresh.RecurrenceRule, "UNTIL")
	if until == "" {
		t.Fatalf("expected UNTIL on truncated master, got %q", fresh.RecurrenceRule)
	}
	if strings.Contains(until, "T") || strings.Contains(until, "Z") {
		t.Errorf("all-day series must use DATE UNTIL, got %q in %q", until, fresh.RecurrenceRule)
	}
	if until != "20260519" {
		t.Errorf("UNTIL = %q, want last kept day 20260519", until)
	}
}

func rruleParamValue(rule, name string) string {
	for _, p := range strings.Split(rule, ";") {
		if strings.HasPrefix(strings.ToUpper(p), name+"=") {
			return p[len(name)+1:]
		}
	}
	return ""
}

// Caller is the source of truth for categories. Empty Categories on update
// means "no categories". UpdateInstance/UpdateFromInstance no longer inherit
// from master. Users can then clear tags via the form and have it persist.
func TestUpdateInstance_EmptyCategoriesClearsOverrideTags(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "cats-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")
	if err := svc.ReplaceCategories(ctx, master.ID, []string{"work", "team"}); err != nil {
		t.Fatalf("seed master categories: %v", err)
	}

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	override, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Standup (moved)",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		// Empty Categories means the user cleared tags on the override.
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	cats, err := svc.ListCategories(ctx, override.ID)
	if err != nil {
		t.Fatalf("list override categories: %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("expected 0 categories on override, got %d: %v", len(cats), cats)
	}

	// Master is untouched.
	mc, err := svc.ListCategories(ctx, master.ID)
	if err != nil {
		t.Fatalf("list master categories: %v", err)
	}
	if len(mc) != 2 {
		t.Errorf("master categories should be unchanged, got %v", mc)
	}
}

func TestUpdateInstance_NonEmptyCategoriesHonored(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "cats-uid-1b", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")
	if err := svc.ReplaceCategories(ctx, master.ID, []string{"work"}); err != nil {
		t.Fatalf("seed master categories: %v", err)
	}

	instance := time.Date(2026, 5, 20, 9, 0, 0, 0, time.UTC)
	override, err := svc.UpdateInstance(ctx, master.UID, instance, UpdateParams{
		CalendarID: master.CalendarID,
		Title:      "Standup (moved)",
		StartTime:  time.Date(2026, 5, 20, 9, 30, 0, 0, time.UTC),
		EndTime:    time.Date(2026, 5, 20, 10, 0, 0, 0, time.UTC),
		Categories: "personal,errand",
	})
	if err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}

	cats, err := svc.ListCategories(ctx, override.ID)
	if err != nil {
		t.Fatalf("list override categories: %v", err)
	}
	if len(cats) != 2 {
		t.Errorf("expected 2 caller-supplied categories, got %d: %v", len(cats), cats)
	}
}

func TestUpdate_AllEventsRewritesMaster(t *testing.T) {
	// Sanity guard that the existing Update path (used by the "All events"
	// scope) still rewrites the master row in place rather than creating an
	// override.
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "all-uid-1", "FREQ=WEEKLY;BYDAY=MO,TU,WE,TH")

	if _, err := svc.Update(ctx, master.ID, UpdateParams{
		CalendarID:     master.CalendarID,
		Title:          "Standup (renamed)",
		StartTime:      master.StartTime,
		EndTime:        master.EndTime,
		RecurrenceRule: master.RecurrenceRule,
	}); err != nil {
		t.Fatalf("Update: %v", err)
	}

	overrides, err := svc.ListSeriesOverrides(ctx, master.CalendarID, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 0 {
		t.Errorf("expected no overrides after Update on master, got %d", len(overrides))
	}

	fresh, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID: %v", err)
	}
	if fresh.Title != "Standup (renamed)" {
		t.Errorf("master.Title = %q, want %q", fresh.Title, "Standup (renamed)")
	}
}

// TestUpdateInstance_RevivesDeletedOverride edits one occurrence, deletes
// that occurrence, and edits it again. The delete soft-deletes the
// override row, and the row keeps its UNIQUE key. The second edit must
// revive and update that row, not fail on the key. The delete-recorded
// EXDATE goes away, so the occurrence shows again. An imported EXDATE of
// another occurrence stays (issue #86).
func TestUpdateInstance_RevivesDeletedOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "revive-uid-1", "FREQ=DAILY;COUNT=5")
	inst := master.StartTime.Add(24 * time.Hour)
	imported := master.StartTime.Add(72 * time.Hour)
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET exdates = ? WHERE id = ?`, imported.Format(time.RFC3339), master.ID); err != nil {
		t.Fatalf("seed imported EXDATE: %v", err)
	}

	edit := func(title string) (Event, error) {
		return svc.UpdateInstance(ctx, master.UID, inst, UpdateParams{
			CalendarID: master.CalendarID, Title: title, StartTime: inst, EndTime: inst.Add(time.Hour),
		})
	}
	first, err := edit("First edit")
	if err != nil {
		t.Fatalf("first UpdateInstance: %v", err)
	}
	if err := svc.DeleteInstance(ctx, master.CalendarID, master.UID, inst); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	second, err := edit("Second edit")
	if err != nil {
		t.Fatalf("UpdateInstance of a deleted occurrence = %v, want nil", err)
	}
	if second.ID != first.ID || second.Title != "Second edit" {
		t.Fatalf("second edit = %+v, want row %d revived with the new title", second, first.ID)
	}
	fresh, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if strings.Contains(fresh.ExDates, inst.Format("2006-01-02T15")) {
		t.Fatalf("master EXDATEs = %q, want the delete-recorded EXDATE gone", fresh.ExDates)
	}
	if !strings.Contains(fresh.ExDates, imported.Format("2006-01-02T15")) {
		t.Fatalf("master EXDATEs = %q, want the imported EXDATE kept", fresh.ExDates)
	}
}

// TestUpdateInstance_RevivedOverrideStartsWithoutOldRelations edits one
// occurrence and gives the override an alarm, an attendee, and an
// attachment. A delete of the occurrence and a new plain edit revive the
// row. The new edit is a fresh override, so the old relations must not
// come back.
func TestUpdateInstance_RevivedOverrideStartsWithoutOldRelations(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "revive-uid-2", "FREQ=DAILY;COUNT=5")
	inst := master.StartTime.Add(24 * time.Hour)
	params := UpdateParams{CalendarID: master.CalendarID, Title: "Edit", StartTime: inst, EndTime: inst.Add(time.Hour)}

	first, err := svc.UpdateInstanceWithRelations(ctx, master.UID, inst, params,
		[]model.Attendee{{Email: "a@example.com"}},
		[]model.Alarm{{Action: "DISPLAY", TriggerValue: "-PT5M", Description: "soon"}})
	if err != nil {
		t.Fatalf("first UpdateInstanceWithRelations: %v", err)
	}
	if err := svc.ReplaceAttachments(ctx, first.ID, []model.Attachment{{URI: "https://example.com/a.pdf"}}); err != nil {
		t.Fatalf("ReplaceAttachments: %v", err)
	}
	if err := svc.DeleteInstance(ctx, master.CalendarID, master.UID, inst); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	second, err := svc.UpdateInstance(ctx, master.UID, inst, params)
	if err != nil {
		t.Fatalf("second UpdateInstance: %v", err)
	}
	if alarms, err := svc.ListAlarms(ctx, second.ID); err != nil || len(alarms) != 0 {
		t.Fatalf("alarms of the revived override = %+v, %v; want none", alarms, err)
	}
	if att, err := svc.ListAttendees(ctx, second.ID); err != nil || len(att) != 0 {
		t.Fatalf("attendees of the revived override = %+v, %v; want none", att, err)
	}
	if files, err := svc.ListAttachments(ctx, second.ID); err != nil || len(files) != 0 {
		t.Fatalf("attachments of the revived override = %+v, %v; want none", files, err)
	}
}

// TestUpdateInstance_RevivesOverrideOfItsOwnSeries holds one UID in two
// calendars, Work and Home. Each master has the same deleted occurrence.
// A new edit of that occurrence in Home revives the Home override. Only
// the EXDATE of the Home master may go; the Work master keeps its EXDATE.
func TestUpdateInstance_RevivesOverrideOfItsOwnSeries(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	home, err := svc.q.CreateCalendar(ctx, storage.CreateCalendarParams{Name: "Home"})
	if err != nil {
		t.Fatalf("CreateCalendar: %v", err)
	}
	work := newRecurringMaster(t, svc, "revive-uid-3", "FREQ=DAILY;COUNT=5")
	inst := work.StartTime.Add(24 * time.Hour)
	homeMaster, err := svc.Create(ctx, CreateParams{CalendarID: home.ID, Title: "Standup", StartTime: work.StartTime, EndTime: work.EndTime, RecurrenceRule: "FREQ=DAILY;COUNT=5"})
	if err != nil {
		t.Fatalf("Create Home master: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET uid = ? WHERE id = ?`, work.UID, homeMaster.ID); err != nil {
		t.Fatalf("seed the Home copy: %v", err)
	}
	exdate := inst.Format(time.RFC3339)
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET exdates = ? WHERE id IN (?, ?)`, exdate, work.ID, homeMaster.ID); err != nil {
		t.Fatalf("seed EXDATEs: %v", err)
	}
	// The deleted override of Home and the delete log of that EXDATE.
	if _, err := svc.db.ExecContext(ctx,
		`INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id, deleted_at) VALUES (?, ?, 'Old', ?, ?, ?, '2026-05-01T00:00:00Z')`,
		work.UID, home.ID, exdate, inst.Add(time.Hour).Format(time.RFC3339), exdate); err != nil {
		t.Fatalf("seed deleted override: %v", err)
	}
	if err := svc.q.RecordEventExdateDelete(ctx, storage.RecordEventExdateDeleteParams{CalendarID: home.ID, Uid: work.UID, RecurrenceID: exdate}); err != nil {
		t.Fatalf("seed delete log: %v", err)
	}

	if _, err := svc.UpdateInstance(ctx, work.UID, inst, UpdateParams{
		CalendarID: home.ID, Title: "Back", StartTime: inst, EndTime: inst.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateInstance in Home: %v", err)
	}
	for _, tc := range []struct {
		name    string
		id      int64
		wantHas bool
	}{{"Work", work.ID, true}, {"Home", homeMaster.ID, false}} {
		m, err := svc.Get(ctx, tc.id)
		if err != nil {
			t.Fatalf("Get %s master: %v", tc.name, err)
		}
		if got := strings.Contains(m.ExDates, inst.Format("2006-01-02T15")); got != tc.wantHas {
			t.Fatalf("%s master EXDATEs = %q, want the EXDATE kept = %v", tc.name, m.ExDates, tc.wantHas)
		}
	}
}

// TestUpdateInstance_DoesNotReviveOverrideHiddenBySplit edits an occurrence
// after a "this and following" split. The split soft-deleted the override
// of that occurrence, because the occurrence is past the UNTIL of the
// master. An edit of it must not revive that row: the result would be a
// live override outside the series. The edit must fail with a clear error.
func TestUpdateInstance_DoesNotReviveOverrideHiddenBySplit(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "revive-uid-4", "FREQ=WEEKLY")
	cut := master.StartTime.Add(7 * 24 * time.Hour)
	later := cut.Add(7 * 24 * time.Hour)
	params := func(at time.Time) UpdateParams {
		return UpdateParams{CalendarID: master.CalendarID, Title: "Edit", StartTime: at, EndTime: at.Add(time.Hour)}
	}
	if _, err := svc.UpdateInstance(ctx, master.UID, later, params(later)); err != nil {
		t.Fatalf("UpdateInstance before the split: %v", err)
	}
	split := params(cut)
	split.RecurrenceRule = "FREQ=WEEKLY"
	if _, err := svc.UpdateFromInstance(ctx, master.CalendarID, master.UID, cut, split); err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}

	_, err := svc.UpdateInstance(ctx, master.UID, later, params(later))
	if !errors.Is(err, ErrOccurrenceOutsideSeries) {
		t.Fatalf("UpdateInstance past the split = %v, want ErrOccurrenceOutsideSeries", err)
	}
	overrides, err := svc.ListSeriesOverrides(ctx, master.CalendarID, master.UID)
	if err != nil {
		t.Fatalf("ListOverridesByUID: %v", err)
	}
	if len(overrides) != 0 {
		t.Fatalf("live overrides after the refused edit = %+v, want none", overrides)
	}
}

// TestUpdateInstance_RevivesOverrideAfterMasterRestore deletes a series
// with an override, and then restores the master only. The override stays
// in the trash, with no occurrence-delete log row. Its occurrence is still
// part of the series, so a new edit of it must revive the row.
func TestUpdateInstance_RevivesOverrideAfterMasterRestore(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "revive-uid-5", "FREQ=DAILY;COUNT=5")
	inst := master.StartTime.Add(24 * time.Hour)
	params := UpdateParams{CalendarID: master.CalendarID, Title: "Edit", StartTime: inst, EndTime: inst.Add(time.Hour)}
	first, err := svc.UpdateInstance(ctx, master.UID, inst, params)
	if err != nil {
		t.Fatalf("first UpdateInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, master.CalendarID, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID master: %v", err)
	}

	params.Title = "Again"
	second, err := svc.UpdateInstance(ctx, master.UID, inst, params)
	if err != nil {
		t.Fatalf("UpdateInstance after the master restore = %v, want nil", err)
	}
	if second.ID != first.ID || second.Title != "Again" {
		t.Fatalf("second edit = %+v, want row %d revived", second, first.ID)
	}
}

// TestUpdateInstance_RevivesOverrideAfterUntilRemoved splits a series at a
// cut, which hides the override of a later occurrence. Then the user
// removes the UNTIL of the master. The later occurrence is part of the
// series again, so a new edit of it must revive the row.
func TestUpdateInstance_RevivesOverrideAfterUntilRemoved(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master := newRecurringMaster(t, svc, "revive-uid-6", "FREQ=WEEKLY")
	cut := master.StartTime.Add(7 * 24 * time.Hour)
	later := cut.Add(7 * 24 * time.Hour)
	params := func(at time.Time) UpdateParams {
		return UpdateParams{CalendarID: master.CalendarID, Title: "Edit", StartTime: at, EndTime: at.Add(time.Hour)}
	}
	first, err := svc.UpdateInstance(ctx, master.UID, later, params(later))
	if err != nil {
		t.Fatalf("UpdateInstance before the split: %v", err)
	}
	split := params(cut)
	split.RecurrenceRule = "FREQ=WEEKLY"
	if _, err := svc.UpdateFromInstance(ctx, master.CalendarID, master.UID, cut, split); err != nil {
		t.Fatalf("UpdateFromInstance: %v", err)
	}
	fresh, err := svc.Get(ctx, master.ID)
	if err != nil {
		t.Fatalf("Get master: %v", err)
	}
	if _, err := svc.Update(ctx, master.ID, UpdateParams{
		CalendarID: fresh.CalendarID, Title: fresh.Title, StartTime: fresh.StartTime, EndTime: fresh.EndTime,
		RecurrenceRule: "FREQ=WEEKLY", Status: fresh.Status, Transp: fresh.Transp, Class: fresh.Class,
	}); err != nil {
		t.Fatalf("remove UNTIL: %v", err)
	}

	second, err := svc.UpdateInstance(ctx, master.UID, later, params(later))
	if err != nil {
		t.Fatalf("UpdateInstance after the UNTIL removal = %v, want nil", err)
	}
	if second.ID != first.ID {
		t.Fatalf("second edit = %+v, want row %d revived", second, first.ID)
	}
}
