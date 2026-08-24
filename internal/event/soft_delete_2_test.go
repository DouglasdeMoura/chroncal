package event

import (
	"context"

	"testing"
	"time"
)

// TestSoftDelete_OverrideMasterLookupError is the regression test for issue
// #412. A delete of an override must not collapse a genuine DB error from the
// master lookup into the "no master" path. On a non-ErrNoRows error the old
// code soft-deleted the override. It skipped the EXDATE and provenance
// records in silence. Series expansion then resurrected the occurrence. Trash
// could not restore it. Same failure mode as #290. That was fixed there in
// the todo and journal services but never in the event service.
func TestSoftDelete_OverrideMasterLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "weekly-uid",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		RecurrenceID: time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Force the master lookup (GetEventByUID) to fail with a genuine, non-
	// ErrNoRows error by writing non-numeric text into the master's integer
	// sequence column so its row scan fails. The override row that the initial
	// Get(id) loads is untouched, and SoftDeleteEvent never scans, so the buggy
	// path would still soft-delete the override and return nil.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 'corrupt' WHERE id = ?", master.ID); err != nil {
		t.Fatalf("corrupt master sequence: %v", err)
	}

	if err := svc.Delete(ctx, override.ID); err == nil {
		t.Fatal("Delete should propagate a non-ErrNoRows master-lookup error, got nil")
	}

	// Repair the master so reads work, then confirm the override is still
	// live: the transaction must have rolled back.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 0 WHERE id = ?", master.ID); err != nil {
		t.Fatalf("repair master sequence: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override should still be live after failed delete: %v", err)
	}
}

// TestDeleteSeries_MasterLookupError mirrors the #290/#412 guard for the
// series path. DeleteSeries must not treat a genuine DB error from the master
// lookup as "no master". On a non-ErrNoRows error the old code soft-deleted
// the series locally with no tombstone. The next pull then resurrected the
// series. The todo and journal services already carry this guard.
func TestDeleteSeries_MasterLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "series-lookup-err",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// Force the master lookup (GetEventByUID) to fail with a genuine, non-
	// ErrNoRows error by writing non-numeric text into the master's integer
	// sequence column so its row scan fails. The writable check before the
	// transaction reads only calendar_id. SoftDeleteEventsByUID never scans,
	// so the buggy path would still soft-delete the series and return nil.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 'corrupt' WHERE id = ?", master.ID); err != nil {
		t.Fatalf("corrupt master sequence: %v", err)
	}

	if err := svc.DeleteSeries(ctx, master.UID); err == nil {
		t.Fatal("DeleteSeries should propagate a non-ErrNoRows master-lookup error, got nil")
	}

	// Repair the master so reads work, then confirm the series is still
	// live: the transaction must have rolled back.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 0 WHERE id = ?", master.ID); err != nil {
		t.Fatalf("repair master sequence: %v", err)
	}
	if _, err := svc.GetByUID(ctx, master.UID); err != nil {
		t.Fatalf("series should still be live after failed DeleteSeries: %v", err)
	}
}

// TestDeleteInstance_OverrideLookupError mirrors the #290/#412 guard for the
// instance path. DeleteInstance must not treat a genuine DB error from the
// override lookup as "no override". On a non-ErrNoRows error the old code
// committed the EXDATE and its provenance log while the live override row
// stayed. The deleted occurrence then stayed visible through the override,
// and the trash entry could not un-hide it.
func TestDeleteInstance_OverrideLookupError(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "instance-lookup-err",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	instanceAt := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Weekly Review (moved)",
		StartTime:    time.Date(2026, 4, 15, 14, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 15, 15, 0, 0, 0, time.UTC),
		RecurrenceID: instanceAt.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// Force the override lookup (GetEventByUIDAndRecurrenceID) to fail with
	// a genuine, non-ErrNoRows error by writing non-numeric text into the
	// override's integer sequence column so its row scan fails. The master
	// read and UpdateEventExdates never scan the override row, so the buggy
	// path still committed the EXDATE and returned nil.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 'corrupt' WHERE id = ?", override.ID); err != nil {
		t.Fatalf("corrupt override sequence: %v", err)
	}

	err = svc.DeleteInstance(ctx, master.UID, instanceAt)
	if err == nil {
		t.Fatal("DeleteInstance should propagate a non-ErrNoRows override-lookup error, got nil")
	}

	// Repair the override so reads work, then confirm nothing committed: the
	// master carries no EXDATE and the override is still live.
	if _, err := svc.db.ExecContext(ctx,
		"UPDATE events SET sequence = 0 WHERE id = ?", override.ID); err != nil {
		t.Fatalf("repair override sequence: %v", err)
	}
	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after failed DeleteInstance: %v", err)
	}
	if n := len(got.ParseExDates()); n != 0 {
		t.Fatalf("master EXDATE count = %d, want 0 (%q)", n, got.ExDates)
	}
	if _, err := svc.Get(ctx, override.ID); err != nil {
		t.Fatalf("override should still be live after failed DeleteInstance: %v", err)
	}
}

// TestSoftDelete_UndoOverrideDeleteRestoresOnlyThatInstance verifies the
// undo metadata DeleteWithUndo returns for an override. The metadata must
// carry the override's recurrence_id. Undo then un-hides exactly that
// instance and clears only its EXDATE. Before the fix, the metadata dropped
// the recurrence_id. Undo fell back to a UID-wide restore. That resurrected
// another override of the same series the user had deleted before.
func TestSoftDelete_UndoOverrideDeleteRestoresOnlyThatInstance(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "undo-override-uid",
		CalendarID:     1,
		Title:          "Weekly Review",
		StartTime:      time.Date(2026, 4, 1, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 11, 0, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=5",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}
	week2 := time.Date(2026, 4, 8, 10, 0, 0, 0, time.UTC)
	week3 := time.Date(2026, 4, 15, 10, 0, 0, 0, time.UTC)
	override2, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Week 2 (moved)",
		StartTime:    week2.Add(2 * time.Hour),
		EndTime:      week2.Add(3 * time.Hour),
		RecurrenceID: week2.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override 2: %v", err)
	}
	override3, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Week 3 (moved)",
		StartTime:    week3.Add(2 * time.Hour),
		EndTime:      week3.Add(3 * time.Hour),
		RecurrenceID: week3.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override 3: %v", err)
	}

	// Delete week 2's override first (an earlier, unrelated delete).
	if _, err := svc.DeleteWithUndo(ctx, override2.ID); err != nil {
		t.Fatalf("delete override 2: %v", err)
	}
	// Then delete week 3's override and undo just that one.
	meta, err := svc.DeleteWithUndo(ctx, override3.ID)
	if err != nil {
		t.Fatalf("delete override 3: %v", err)
	}
	if meta.RecurrenceID != week3.UTC().Format(time.RFC3339) {
		t.Fatalf("meta.RecurrenceID = %q, want %q", meta.RecurrenceID, week3.UTC().Format(time.RFC3339))
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}

	// Week 3 must be live again: override un-hidden, its EXDATE cleared.
	if _, err := svc.Get(ctx, override3.ID); err != nil {
		t.Fatalf("override 3 should be live after undo: %v", err)
	}
	got, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("get master after undo: %v", err)
	}
	exdates := got.ParseExDates()
	if len(exdates) != 1 || !exdates[0].Equal(week2) {
		t.Fatalf("master EXDATEs after undo = %v, want exactly week 2 (%v)", exdates, week2)
	}

	// Week 2's earlier delete must stand: its override stays soft-deleted.
	still, err := svc.GetIncludingDeleted(ctx, override2.ID)
	if err != nil {
		t.Fatalf("get override 2 including deleted: %v", err)
	}
	if still.DeletedAt == nil {
		t.Fatal("override 2 was resurrected by the undo of override 3")
	}
}

// TestSoftDelete_FromInstanceUndo_ReAddsRDates reproduces issue #490. The TUI
// truncation-undo path (RestoreUndo of an UndoKindFromInstance) must re-add the
// post-cutoff RDATEs the truncation trimmed. That matches the trash-restore
// path (issue #463). Before the fix, RestoreUndo rewrote only the RRULE. It
// dropped the trimmed RDATEs in silence.
func TestSoftDelete_FromInstanceUndo_ReAddsRDates(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	rdate1 := time.Date(2026, 4, 15, 9, 0, 0, 0, time.UTC)
	rdate2 := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	master := newRDateOnlyMaster(t, svc, "rdate-undo-readds", []time.Time{rdate1, rdate2})

	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, rdate2)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// The truncation trimmed the post-cutoff RDATE.
	trimmed, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after truncate: %v", err)
	}
	if got := trimmed.ParseRDates(); len(got) != 1 || !got[0].Equal(rdate1) {
		t.Fatalf("RDates after truncate = %v, want only %s", got, rdate1.Format(time.RFC3339))
	}

	// Undo must put the dropped RDATE back.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	restored, err := svc.GetByUID(ctx, master.UID)
	if err != nil {
		t.Fatalf("GetByUID after undo: %v", err)
	}
	rdates := restored.ParseRDates()
	foundR2 := false
	for _, rd := range rdates {
		if rd.Equal(rdate2) {
			foundR2 = true
		}
	}
	if len(rdates) != 2 || !foundR2 {
		t.Fatalf("issue #490: RDates after undo = %v, want both restored (incl. %s)",
			rdates, rdate2.Format(time.RFC3339))
	}
}

// TestSoftDelete_FromInstanceUndo_KeepsIndependentlyDeletedOverride reproduces
// issue #491 (the #287 class on the undo path). Delete a single override. Then
// truncate "this and following" from an earlier cutoff. Then Undo must NOT
// resurrect the independently-deleted override. Before the fix, RestoreUndo
// called RestoreEventsByUID. That un-hid every soft-deleted row that shares
// the UID.
func TestSoftDelete_FromInstanceUndo_KeepsIndependentlyDeletedOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()

	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:            "standup-undo",
		CalendarID:     1,
		Title:          "Standup",
		StartTime:      time.Date(2026, 4, 1, 9, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 4, 1, 9, 30, 0, 0, time.UTC),
		RecurrenceRule: "FREQ=WEEKLY;COUNT=10",
	})
	if err != nil {
		t.Fatalf("create master: %v", err)
	}

	// User customizes the Apr 22 instance (creates an override).
	overrideTime := time.Date(2026, 4, 22, 9, 0, 0, 0, time.UTC)
	override, err := svc.UpsertByUID(ctx, UpsertParams{
		UID:          master.UID,
		CalendarID:   1,
		Title:        "Standup (moved)",
		StartTime:    time.Date(2026, 4, 22, 10, 0, 0, 0, time.UTC),
		EndTime:      time.Date(2026, 4, 22, 10, 30, 0, 0, time.UTC),
		RecurrenceID: overrideTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("create override: %v", err)
	}

	// User deletes that single customized instance on its own.
	if err := svc.DeleteInstance(ctx, master.UID, overrideTime); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err == nil {
		t.Fatalf("override should be soft-deleted after DeleteInstance")
	}

	// Later, user truncates "this and following" from an earlier cutoff (Apr 8).
	cutoff := time.Date(2026, 4, 8, 9, 0, 0, 0, time.UTC)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, master.UID, cutoff)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}

	// Undo the truncation. This must NOT resurrect the override the user
	// independently deleted before the truncation.
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if _, err := svc.Get(ctx, override.ID); err == nil {
		t.Fatalf("issue #491: independently-deleted override resurrected by truncation undo")
	}
}
