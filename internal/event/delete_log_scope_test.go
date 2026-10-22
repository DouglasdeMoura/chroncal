package event

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

// countLogsIn returns the count of series-delete logs of uid in calendarID.
func countLogsIn(t *testing.T, svc *Service, calendarID int64, uid string) int {
	t.Helper()
	var n int
	if err := svc.db.QueryRowContext(context.Background(),
		`SELECT COUNT(*) FROM event_series_deletes WHERE calendar_id = ? AND uid = ?`, calendarID, uid).Scan(&n); err != nil {
		t.Fatalf("count series logs: %v", err)
	}
	return n
}

// TestOverrideRevive_KeepsTheEmptyLogOfAnotherCalendar: the revive of an
// override in calendar A drops only the logs that named the row. The empty
// log of the UID in calendar B stays, because the row does not belong to
// that delete.
func TestOverrideRevive_KeepsTheEmptyLogOfAnotherCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	calB := insertCalendar(t, svc, "B")
	if _, err := svc.db.ExecContext(ctx,
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master) VALUES (?, ?, '', 0)`,
		calB, master.UID); err != nil {
		t.Fatalf("seed the log of B: %v", err)
	}
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, override.ID); err != nil {
		t.Fatalf("revive the override: %v", err)
	}
	if n := countLogsIn(t, svc, calB, master.UID); n != 1 {
		t.Errorf("logs of B after the revive in A = %d, want 1", n)
	}
	if n := countLogsIn(t, svc, 1, master.UID); n != 1 {
		t.Errorf("logs of A after the revive = %d, want 1 (the master stays in the trash)", n)
	}
}

// TestPurgeDeleted_PurgedLogSparesTheRowsOfAKeptSeries: the series-delete
// log of a purged master can name a row that a kept series owns. The kept
// series wins, so the row stays in the trash.
func TestPurgeDeleted_PurgedLogSparesTheRowsOfAKeptSeries(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "spare-uid"
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	calB := insertCalendar(t, svc, "B")
	rowsB := make([]int64, 0, 2)
	for _, cal := range []int64{1, calB} {
		if _, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: uid, CalendarID: cal, Title: "Series",
			StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
		}); err != nil {
			t.Fatalf("upsert master: %v", err)
		}
	}
	for _, days := range []int{1, 2} {
		at := start.AddDate(0, 0, days)
		o, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: uid, CalendarID: calB, Title: "Late",
			StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert override: %v", err)
		}
		rowsB = append(rowsB, o.ID)
	}
	if err := svc.DeleteSeries(ctx, calB, uid); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if err := reviveRow(svc, rowsB[1]); err != nil {
		t.Fatalf("revive the live override of B: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("DeleteSeries(1): %v", err)
	}
	// A stale log of calendar 1 names the trashed row of the kept series.
	for _, stmt := range []string{
		`UPDATE event_series_deletes SET hidden_ids = CAST(? AS TEXT) WHERE calendar_id = 1 AND uid = 'spare-uid'`,
		`UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = 'spare-uid' AND deleted_at IS NOT NULL AND ? > 0`,
	} {
		if _, err := svc.db.ExecContext(ctx, stmt, rowsB[0]); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	report, err := svc.PurgeDeletedReport(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("PurgeDeletedReport: %v", err)
	}
	if len(report.Skipped) != 1 || report.Skipped[0].CalendarID != calB {
		t.Fatalf("skipped = %+v, want the master of B", report.Skipped)
	}
	if _, err := svc.GetIncludingDeleted(ctx, rowsB[0]); err != nil {
		t.Errorf("trashed row of the kept series after the purge: %v, want it kept", err)
	}
}

// occurrenceDeletedBeforeSeries deletes the occurrence second of a series
// with a stored override, then the whole series. It returns the master, the
// override row, and the trash entry of the occurrence delete.
func occurrenceDeletedBeforeSeries(t *testing.T, svc *Service) (Event, storage.Event, TrashEntry) {
	t.Helper()
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	override, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			return master, override, e
		}
	}
	t.Fatalf("trash holds no occurrence entry: %+v", entries)
	return Event{}, storage.Event{}, TrashEntry{}
}

// TestRestoreInstance_TrashedMasterRefuses: the restore of an occurrence
// delete from the trash refuses while the master is in the trash. The hidden
// override stays in the trash, so it cannot block the purge of the master.
// After the series restore, the entry brings back the override.
func TestRestoreInstance_TrashedMasterRefuses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, override, entry := occurrenceDeletedBeforeSeries(t, svc)
	if err := svc.RestoreTrash(ctx, entry); !errors.Is(err, ErrMasterInTrash) {
		t.Fatalf("RestoreTrash(occurrence) = %v, want ErrMasterInTrash", err)
	}
	wantTrashed(t, svc, true, override.ID, master.ID)
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	wantTrashed(t, svc, true, override.ID)
	if err := svc.RestoreTrash(ctx, entry); err != nil {
		t.Fatalf("RestoreTrash(occurrence) after the series restore: %v", err)
	}
	wantTrashed(t, svc, false, override.ID)
}

// TestOrphanDeletes_BesideUnloggedMasterSeedItsLog: a pull hides a master and
// its overrides with no log. A delete of orphan row O1, then of orphan row O2,
// writes and then extends a log of that master with had_master 1. The log
// names the rows that went with the master too. The restore of the master
// then brings back every row of the series.
func TestOrphanDeletes_BesideUnloggedMasterSeedItsLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); err != nil {
		t.Fatalf("hide the series: %v", err)
	}
	if n := countLogsIn(t, svc, 1, master.UID); n != 0 {
		t.Fatalf("logs after the pull = %d, want 0", n)
	}
	for _, at := range []time.Time{third.Add(24 * time.Hour), master.StartTime} {
		o, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: master.UID, CalendarID: 1, Title: "Orphan",
			StartTime: at.Add(time.Hour), EndTime: at.Add(2 * time.Hour), RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("upsert orphan row: %v", err)
		}
		if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
			t.Fatalf("DeleteSeries(orphan %d): %v", o.ID, err)
		}
		log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
		if err != nil || log.HadMaster != 1 {
			t.Fatalf("log after the delete of orphan %d = %+v, %v; want had_master 1", o.ID, log, err)
		}
	}
	if n := countLogsIn(t, svc, 1, master.UID); n != 1 {
		t.Errorf("logs after the two deletes = %d, want 1", n)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	overrides, err := svc.ListSeriesOverrides(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("ListSeriesOverrides: %v", err)
	}
	if len(overrides) != 4 {
		t.Errorf("live overrides after the restore = %d, want the two rows of the pull and the two orphans", len(overrides))
	}
	for _, at := range []time.Time{second, third} {
		if _, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		}); err != nil {
			t.Errorf("override %s after the restore: %v, want it live", at, err)
		}
	}
}

// legacyAt stores a legacy override of uid at the occurrence at in calendarID.
func legacyAt(t *testing.T, svc *Service, uid string, calendarID int64, at time.Time) storage.Event {
	t.Helper()
	e, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: uid, CalendarID: calendarID, Title: "Legacy",
		StartTime: at.Add(30 * time.Minute), EndTime: at.Add(90 * time.Minute), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert legacy override: %v", err)
	}
	return storage.Event{ID: e.ID, CalendarID: calendarID, Uid: uid, RecurrenceID: at.Format(time.RFC3339)}
}

// wantTrashed fails unless each row of ids has the trash state want.
func wantTrashed(t *testing.T, svc *Service, want bool, ids ...int64) {
	t.Helper()
	for _, id := range ids {
		r, err := svc.GetIncludingDeleted(context.Background(), id)
		if err != nil || (r.DeletedAt != nil) != want {
			t.Errorf("row %d: trashed %v, %v; want trashed %v", id, r.DeletedAt != nil, err, want)
		}
	}
}

// exdateCount returns the count of the EXDATEs of master at at.
func exdateCount(t *testing.T, svc *Service, masterID int64, at time.Time) int {
	t.Helper()
	m, err := svc.q.GetEventIncludingDeleted(context.Background(), masterID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	n := 0
	for _, x := range ParseTimeList(storage.NullableToString(m.Exdates)) {
		if x.Equal(at) {
			n++
		}
	}
	return n
}

// TestDeleteInstance_HidesTheLegacyRowBesideTheOwnRow: an occurrence with a
// row beside the master and a legacy override goes as a whole. The delete
// hides both rows, and the undo brings both back.
func TestDeleteInstance_HidesTheLegacyRowBesideTheOwnRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get own override: %v", err)
	}
	legacy := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "Legacy"), second)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	wantTrashed(t, svc, true, own.ID, legacy.ID)
	if _, err := svc.SeriesOverride(ctx, 1, master.UID, second.Format(time.RFC3339)); err == nil {
		t.Error("SeriesOverride after the delete found a row, want none")
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	wantTrashed(t, svc, false, own.ID, legacy.ID)
}

// TestDeleteInstance_SecondDeleteKeepsTheRowsOfTheFirst is the L1/L2
// sequence. A delete hides legacy row L1. A later import stores legacy row L2
// of the same occurrence, and a second delete hides it. The log names both
// rows, and the master holds one EXDATE of the occurrence. The restore from
// the trash brings back both rows and the occurrence.
func TestDeleteInstance_SecondDeleteKeepsTheRowsOfTheFirst(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "l1-l2", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	l1 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L1"), second)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("first DeleteInstance: %v", err)
	}
	l2 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L2"), second)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("second DeleteInstance: %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs of the occurrence = %d, want 1", n)
	}
	log, err := svc.q.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get exdate log: %v", err)
	}
	parsed, err := parseOccurrenceLog(log)
	if err != nil || len(parsed.Entries) != 2 || !parsed.HasRows() {
		t.Fatalf("entries of the log = %q, %v; want the entries of L1 and L2", log.HiddenIds, err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Fatalf("RestoreTrash(occurrence): %v", err)
			}
		}
	}
	wantTrashed(t, svc, false, l1.ID, l2.ID)
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs of the occurrence after the restore = %d, want 0", n)
	}
}

// TestRestoreOccurrence_GuardsTheCalendarOfEachRow: each restore of an
// occurrence delete guards the calendar of each row that it un-hides. The
// paths are the restore from the trash and the undo. Both refuse beside a
// master in the trash (ErrMasterInTrash). A master of the UID in another
// calendar comes after the delete. The series of calendar 1 then no longer
// owns the legacy row, so the guard of the series does not cover its
// calendar.
func TestRestoreOccurrence_GuardsTheCalendarOfEachRow(t *testing.T) {
	for _, path := range []string{"trash", "undo live master"} {
		t.Run(path, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			master, second, _ := singleSeriesWithOverrides(t, svc)
			calL := insertCalendar(t, svc, "Legacy")
			legacy := legacyAt(t, svc, master.UID, calL, second)
			meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
			if err != nil {
				t.Fatalf("DeleteInstanceWithUndo: %v", err)
			}
			if _, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: master.UID, CalendarID: insertCalendar(t, svc, "B"), Title: "Other series",
				StartTime: master.StartTime, EndTime: master.EndTime, RecurrenceRule: master.RecurrenceRule,
			}); err != nil {
				t.Fatalf("upsert master B: %v", err)
			}
			if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = ?`, calL); err != nil {
				t.Fatalf("make the legacy calendar read-only: %v", err)
			}
			switch path {
			case "trash":
				entries, err := svc.ListTrash(ctx, 1)
				if err != nil {
					t.Fatalf("ListTrash: %v", err)
				}
				for _, e := range entries {
					if e.Kind == TrashKindInstance {
						err = svc.RestoreTrash(ctx, e)
					}
				}
				if !errors.Is(err, calendaraccess.ErrReadOnly) {
					t.Fatalf("RestoreTrash(occurrence) = %v, want ErrReadOnly", err)
				}
			default:
				if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, calendaraccess.ErrReadOnly) {
					t.Fatalf("RestoreUndo = %v, want ErrReadOnly", err)
				}
			}
			wantTrashed(t, svc, true, legacy.ID)
		})
	}
}

// exdateLogRows returns the hidden rows of the occurrence log of uid at at in
// calendar 1. found is false with no log.
func exdateLogRows(t *testing.T, svc *Service, uid string, at time.Time) (ids []int64, found bool) {
	t.Helper()
	log, ok, err := exdateLogTx(context.Background(), svc.q, 1, uid, at.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("get exdate log: %v", err)
	}
	if !ok {
		return nil, false
	}
	parsed, err := parseOccurrenceLog(log)
	if err != nil {
		t.Fatalf("parse hidden rows: %v", err)
	}
	for _, e := range parsed.Entries {
		ids = append(ids, e.Rows...)
	}
	return ids, true
}

// TestUndoOccurrence_AfterRedeleteWithLiveRowsWritesNothing is the TUI undo
// after a CLI redelete. The TUI deletes the occurrence. The CLI restores the
// row and deletes the occurrence again, and a pull brings the row back. The
// TUI undo then finds its row live. It gives ErrAlreadyRestored and keeps the
// EXDATE and the log of the CLI delete.
func TestUndoOccurrence_AfterRedeleteWithLiveRowsWritesNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil || len(meta.RowIDs) != 1 {
		t.Fatalf("DeleteInstanceWithUndo = %+v, %v; want one hidden row", meta, err)
	}
	row := meta.RowIDs[0]
	if err := svc.RestoreByID(ctx, row); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("second DeleteInstance: %v", err)
	}
	// A pull import clears deleted_at of the row.
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = NULL WHERE id = ?`, row); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("RestoreUndo = %v, want ErrAlreadyRestored", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs of the occurrence after the undo = %d, want the 1 of the CLI delete", n)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); !found {
		t.Error("log of the CLI delete after the undo is gone, want it kept")
	}
}

// TestUndoOccurrence_SecondDeleteLeavesTheFirst: the undo of the second
// delete of an occurrence brings back the rows of that delete only. The
// first delete keeps its rows in the log. L2 is live again, so the log gives
// up the EXDATE. The undo of the first delete then drops the log.
func TestUndoOccurrence_SecondDeleteLeavesTheFirst(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "undo-l1-l2", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	l1 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L1"), second)
	meta1, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("first delete: %v", err)
	}
	l2 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L2"), second)
	meta2, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta2); err != nil {
		t.Fatalf("RestoreUndo(second): %v", err)
	}
	wantTrashed(t, svc, false, l2.ID)
	wantTrashed(t, svc, true, l1.ID)
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after the undo of the second delete = %d, want 0: L2 is live", n)
	}
	if ids, found := exdateLogRows(t, svc, master.UID, second); !found || len(ids) != 1 || ids[0] != l1.ID {
		t.Errorf("log rows after the undo of the second delete = %v, %v; want L1 %d", ids, found, l1.ID)
	}
	if err := svc.RestoreUndo(ctx, meta1); err != nil {
		t.Fatalf("RestoreUndo(first): %v", err)
	}
	wantTrashed(t, svc, false, l1.ID)
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after both undos = %d, want 0", n)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); found {
		t.Error("log after both undos exists, want it gone")
	}
}

// TestDeleteByID_LegacyRowBesideAnotherLiveRow: a delete by ID of one of two
// legacy rows of an occurrence hides that row. The other row stays live, so
// the master gets no EXDATE. The log of the master records the delete. The
// restore by ID and the undo bring the row back, and the log goes.
func TestDeleteByID_LegacyRowBesideAnotherLiveRow(t *testing.T) {
	for _, path := range []string{"restore by ID", "undo"} {
		t.Run(path, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
			second := start.Add(24 * time.Hour)
			master, err := svc.UpsertByUID(ctx, UpsertParams{
				UID: "two-legacy", CalendarID: 1, Title: "Series",
				StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
			})
			if err != nil {
				t.Fatalf("upsert master: %v", err)
			}
			calA := insertCalendar(t, svc, "A")
			l1 := legacyAt(t, svc, master.UID, calA, second)
			l2 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "B"), second)
			meta, err := svc.DeleteWithUndo(ctx, l1.ID)
			if err != nil {
				t.Fatalf("DeleteWithUndo(L1): %v", err)
			}
			wantTrashed(t, svc, true, l1.ID)
			wantTrashed(t, svc, false, l2.ID)
			if n := exdateCount(t, svc, master.ID, second); n != 0 {
				t.Errorf("EXDATEs after the delete of L1 = %d, want 0", n)
			}
			if ids, found := exdateLogRows(t, svc, master.UID, second); !found || len(ids) != 1 || ids[0] != l1.ID {
				t.Errorf("log rows of the master after the delete of L1 = %v, %v; want L1 with no EXDATE", ids, found)
			}
			if path == "undo" {
				err = svc.RestoreUndo(ctx, meta)
			} else {
				err = svc.RestoreByID(ctx, l1.ID)
			}
			if err != nil {
				t.Fatalf("%s: %v", path, err)
			}
			wantTrashed(t, svc, false, l1.ID, l2.ID)
			if _, found := exdateLogRows(t, svc, master.UID, second); found {
				t.Errorf("log of the master after the %s exists, want it gone", path)
			}
		})
	}
}

// reviveRow clears deleted_at of the row id, as a pull import does. A restore
// by ID beside a master in the trash refuses (ErrMasterInTrash), so a test
// builds a live row beside a trashed master this way. The revive triggers of
// migration 057 run.
func reviveRow(svc *Service, id int64) error {
	_, err := svc.db.ExecContext(context.Background(), `UPDATE events SET deleted_at = NULL WHERE id = ?`, id)
	return err
}

// seriesWithTwoLegacyRows stores a master in calendar 1 and two legacy rows of
// the occurrence second in two other calendars. It returns the master, the
// occurrence, and the two rows.
func seriesWithTwoLegacyRows(t *testing.T, svc *Service, uid string) (Event, time.Time, storage.Event, storage.Event) {
	t.Helper()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: uid, CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	l1 := legacyAt(t, svc, uid, insertCalendar(t, svc, "L1"), second)
	l2 := legacyAt(t, svc, uid, insertCalendar(t, svc, "L2"), second)
	return master, second, l1, l2
}

// TestDeleteByID_RowBesideMasterKeepsNoEXDATE: a delete by ID of the row A
// beside the master, with a live legacy row B of the occurrence, adds no
// EXDATE. The log records the delete of A. An EXDATE beside the live B would
// make Google and iCloud drop B.
func TestDeleteByID_RowBesideMasterKeepsNoEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get own override: %v", err)
	}
	legacy := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "Legacy"), second)
	if err := svc.Delete(ctx, own.ID); err != nil {
		t.Fatalf("Delete(own row): %v", err)
	}
	wantTrashed(t, svc, false, legacy.ID)
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after the delete of the own row = %d, want 0: B is live", n)
	}
	if ids, found := exdateLogRows(t, svc, master.UID, second); !found || len(ids) != 1 || ids[0] != own.ID {
		t.Errorf("log rows after the delete = %v, %v; want the own row %d", ids, found, own.ID)
	}
}

// TestUndoOccurrence_WithNoRowKeepsTheLogOfALaterDelete: the undo of a delete
// that hid no row keeps the ID of its log. A restore from the trash and a new
// delete of the occurrence give a new log. The undo then refuses with
// ErrAlreadyRestored and keeps the EXDATE and the log of the new delete.
func TestUndoOccurrence_WithNoRowKeepsTheLogOfALaterDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "no-row-undo", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil || len(meta.RowIDs) != 0 || meta.ExdateLogID == 0 {
		t.Fatalf("DeleteInstanceWithUndo = %+v, %v; want no row and a log ID", meta, err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			if err := svc.RestoreTrash(ctx, e); err != nil {
				t.Fatalf("RestoreTrash: %v", err)
			}
		}
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("second DeleteInstance: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("RestoreUndo = %v, want ErrAlreadyRestored", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the undo = %d, want the 1 of the new delete", n)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); !found {
		t.Error("log of the new delete after the undo is gone, want it kept")
	}
}

// TestUndoOccurrence_TwoDeletesWithNoRowInOneLog: two deletes of an
// occurrence with no stored override share one log. Each has its own entry.
// The undo of the second removes its entry only, and the first delete keeps
// the EXDATE. The undo of the first then strips it.
func TestUndoOccurrence_TwoDeletesWithNoRowInOneLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "two-empty", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	meta1, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("first delete: %v", err)
	}
	meta2, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if meta1.ExdateLogID != meta2.ExdateLogID || meta1.ExdateDeleteSeq == meta2.ExdateDeleteSeq {
		t.Fatalf("metas = %+v and %+v; want one log and two seqs", meta1, meta2)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Fatalf("EXDATEs after two deletes = %d, want 1", n)
	}
	if err := svc.RestoreUndo(ctx, meta2); err != nil {
		t.Fatalf("RestoreUndo(second): %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the undo of the second delete = %d, want the 1 of the first", n)
	}
	if err := svc.RestoreUndo(ctx, meta2); !errors.Is(err, ErrAlreadyRestored) {
		t.Errorf("second RestoreUndo(second) = %v, want ErrAlreadyRestored", err)
	}
	if err := svc.RestoreUndo(ctx, meta1); err != nil {
		t.Fatalf("RestoreUndo(first): %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after both undos = %d, want 0", n)
	}
}

// TestUndoOccurrence_PullEmptiedLogStripsNothing: a delete hides R. A pull
// brings R back, so the log gives up its EXDATE, and the revive trigger
// takes R out of the log. A new delete hides R again and adds the EXDATE.
// The undo of the first delete then strips nothing: it refuses with
// ErrAlreadyRestored, and the EXDATE of the new delete stays.
func TestUndoOccurrence_PullEmptiedLogStripsNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if _, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Pulled",
		StartTime: second.Add(time.Hour), EndTime: second.Add(2 * time.Hour), RecurrenceID: second.Format(time.RFC3339),
	}); err != nil {
		t.Fatalf("pull upsert: %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after the pull = %d, want 0: the override is live", n)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("second DeleteInstance: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("RestoreUndo = %v, want ErrAlreadyRestored", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the undo = %d, want the 1 of the new delete", n)
	}
	wantTrashed(t, svc, true, meta.RowIDs...)
}

// TestRestoreByID_ReleasesOnlyItsRow: a restore by ID of A, one of two rows
// of an occurrence delete, makes A live at the occurrence. The log gives up
// the EXDATE and keeps the entry of B, which stays hidden. The restore of B
// then drops the log.
func TestRestoreByID_ReleasesOnlyItsRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, l1, l2 := seriesWithTwoLegacyRows(t, svc, "release-one")
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.RestoreByID(ctx, l1.ID); err != nil {
		t.Fatalf("RestoreByID(L1): %v", err)
	}
	wantTrashed(t, svc, true, l2.ID)
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after the restore of L1 = %d, want 0: L1 is live", n)
	}
	if ids, found := exdateLogRows(t, svc, master.UID, second); !found || len(ids) != 1 || ids[0] != l2.ID {
		t.Errorf("log rows after the restore of L1 = %v, %v; want L2 %d", ids, found, l2.ID)
	}
	if err := svc.RestoreByID(ctx, l2.ID); err != nil {
		t.Fatalf("RestoreByID(L2): %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after both restores = %d, want 0", n)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); found {
		t.Error("log after both restores exists, want it gone")
	}
}

// TestUndoOccurrence_LiveRowGivesAlreadyRestored: a pull brings back the row
// of an occurrence delete. The undo then finds the row live and its own log.
// It gives ErrAlreadyRestored and writes nothing: the EXDATE stays.
func TestUndoOccurrence_LiveRowGivesAlreadyRestored(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil || len(meta.RowIDs) != 1 {
		t.Fatalf("DeleteInstanceWithUndo = %+v, %v; want one row", meta, err)
	}
	if err := reviveRow(svc, meta.RowIDs[0]); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrAlreadyRestored) {
		t.Fatalf("RestoreUndo = %v, want ErrAlreadyRestored", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the refused undo = %d, want 1", n)
	}
}

// ownsExdate reports the EXDATE flag of the occurrence log of uid at at in
// calendar 1. found is false with no log.
func ownsExdate(t *testing.T, svc *Service, uid string, at time.Time) (owns, found bool) {
	t.Helper()
	log, ok, err := occurrenceLogTx(context.Background(), svc.q, 1, uid, at.Format(time.RFC3339))
	if err != nil {
		t.Fatalf("get exdate log: %v", err)
	}
	return log.OwnsExdate, ok
}

// restoreOccurrenceFromTrash restores the occurrence entry of calendar 1.
func restoreOccurrenceFromTrash(t *testing.T, svc *Service) error {
	t.Helper()
	entries, err := svc.ListTrash(context.Background(), 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			return svc.RestoreTrash(context.Background(), e)
		}
	}
	t.Fatal("trash holds no occurrence entry")
	return nil
}

// TestDeleteInstance_ImportedEXDATEStaysItsSource: a delete of an occurrence
// that an imported EXDATE excludes already appends no copy. The log owns no
// EXDATE, so the undo keeps the imported one (issue #86).
func TestDeleteInstance_ImportedEXDATEStaysItsSource(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "imported-x", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
		ExDates: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the delete = %d, want the 1 import", n)
	}
	if owns, found := ownsExdate(t, svc, master.UID, second); !found || owns {
		t.Errorf("log after the delete = owns %v, found %v; want a log that owns no EXDATE", owns, found)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the undo = %d, want the 1 import", n)
	}
}

// TestPullRewriteOfEXDATEs_ClearsTheOwnership: a pull that rewrites the
// EXDATE list of the master takes the ownership from the occurrence logs. A
// restore then keeps the EXDATE of the server (issue #86). A pull that echoes
// the same instants keeps the ownership, so the restore strips the EXDATE.
func TestPullRewriteOfEXDATEs_ClearsTheOwnership(t *testing.T) {
	for _, tc := range []struct {
		name      string
		extra     bool
		wantCount int
	}{
		{"rewritten list", true, 2},
		{"echoed list", false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
			second, third := start.Add(24*time.Hour), start.Add(48*time.Hour)
			params := UpsertParams{
				UID: "pull-x", CalendarID: 1, Title: "Series",
				StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
			}
			master, err := svc.UpsertByUID(ctx, params)
			if err != nil {
				t.Fatalf("upsert master: %v", err)
			}
			if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
				t.Fatalf("DeleteInstance: %v", err)
			}
			params.ExDates = second.Format(time.RFC3339)
			if tc.extra {
				params.ExDates = third.Format(time.RFC3339) + "," + second.Format(time.RFC3339)
			}
			if _, err := svc.UpsertByUID(ctx, params); err != nil {
				t.Fatalf("pull upsert of the master: %v", err)
			}
			if owns, _ := ownsExdate(t, svc, master.UID, second); owns == tc.extra {
				t.Errorf("log owns the EXDATE after the pull = %v, want %v", owns, !tc.extra)
			}
			if err := restoreOccurrenceFromTrash(t, svc); err != nil {
				t.Fatalf("RestoreTrash: %v", err)
			}
			m, err := svc.q.GetEventIncludingDeleted(ctx, master.ID)
			if err != nil {
				t.Fatalf("get master: %v", err)
			}
			if got := len(ParseTimeList(storage.NullableToString(m.Exdates))); got != tc.wantCount {
				t.Errorf("EXDATEs after the restore = %q, want %d", storage.NullableToString(m.Exdates), tc.wantCount)
			}
		})
	}
}

// TestRestoreByID_TruncatedRowBesideTrashedMasterRefuses: a truncation hid
// the row, and then the master went to the trash. The truncation log names
// the row, so the restore by ID refuses with ErrMasterInTrash.
func TestRestoreByID_TruncatedRowBesideTrashedMasterRefuses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteFromInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteFromInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.RestoreByID(ctx, row.ID); !errors.Is(err, ErrMasterInTrash) {
		t.Fatalf("RestoreByID(truncated row) = %v, want ErrMasterInTrash", err)
	}
	wantTrashed(t, svc, true, row.ID)
}

// TestUpdateInstance_NewOverrideStripsTheEXDATE: an edit that creates an
// override at a deleted occurrence makes an override live there. The log
// gives up its EXDATE.
func TestUpdateInstance_NewOverrideStripsTheEXDATE(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "edit-x", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if _, err := svc.UpdateInstance(ctx, 1, master.UID, second, UpdateParams{
		CalendarID: 1, Title: "Back", StartTime: second, EndTime: second.Add(time.Hour),
	}); err != nil {
		t.Fatalf("UpdateInstance: %v", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 0 {
		t.Errorf("EXDATEs after the new override = %d, want 0", n)
	}
}

// TestRestoreMaster_UnloggedSeriesKeepsTheRowOfALoggedOccurrence: a pull
// hides the master and its row O with no log. An occurrence log of the same
// occurrence names another row L only. The restore of the master brings O
// back with the series, and L stays in the trash.
func TestRestoreMaster_UnloggedSeriesKeepsTheRowOfALoggedOccurrence(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	second := start.Add(24 * time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "unlogged-o", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	l := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "Legacy"), second)
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	o := legacyAt(t, svc, master.UID, 1, second)
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID}); err != nil {
		t.Fatalf("pull hides the series: %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	wantTrashed(t, svc, false, o.ID)
	wantTrashed(t, svc, true, l.ID)
}

// TestUndo_DeleteBesideTrashedMasterRefusesALoggedRow: a row went alone
// beside a trashed master. The series came back, the row came back, and an
// occurrence delete hid it again before the next series delete. The old undo
// then refuses with ErrMasterInTrash: the occurrence log owns the row.
func TestUndo_DeleteBesideTrashedMasterRefusesALoggedRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, row.ID); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, row.ID)
	if err != nil || meta.ExdateLogID != 0 {
		t.Fatalf("DeleteWithUndo = %+v, %v; want no log", meta, err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	if err := svc.RestoreByID(ctx, row.ID); err != nil {
		t.Fatalf("RestoreByID(row): %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("second DeleteSeries: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrMasterInTrash) {
		t.Fatalf("RestoreUndo = %v, want ErrMasterInTrash", err)
	}
	wantTrashed(t, svc, true, row.ID)
}

// TestPurgeByID_RemovesTheRowFromTheLogs: a purge of a logged row removes its
// ID from the occurrence log and from the series-delete log. The occurrence
// keeps its EXDATE for the row that stays hidden.
func TestPurgeByID_RemovesTheRowFromTheLogs(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, l1, l2 := seriesWithTwoLegacyRows(t, svc, "purge-logged")
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	if err := svc.PurgeByID(ctx, l1.ID); err != nil {
		t.Fatalf("PurgeByID(L1): %v", err)
	}
	if ids, found := exdateLogRows(t, svc, master.UID, second); !found || len(ids) != 1 || ids[0] != l2.ID {
		t.Errorf("log rows after the purge of L1 = %v, %v; want L2 %d", ids, found, l2.ID)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the purge of L1 = %d, want 1", n)
	}
	// A series-delete log loses a purged row too.
	own, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Own",
		StartTime: master.StartTime.Add(time.Hour), EndTime: master.StartTime.Add(2 * time.Hour), RecurrenceID: master.StartTime.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert own override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := svc.PurgeByID(ctx, own.ID); err != nil {
		t.Fatalf("PurgeByID(own): %v", err)
	}
	log, err := svc.q.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{CalendarID: 1, Uid: master.UID})
	if err != nil {
		t.Fatalf("get series log: %v", err)
	}
	if ids, _ := parseRowIDs(log.HiddenIds); slices.Contains(ids, own.ID) {
		t.Errorf("series log after the purge = %q, want no row %d", log.HiddenIds, own.ID)
	}
}

// TestPurgeByID_DropsALogThatHidesNothing: a delete by ID of L1 beside a live
// L2 writes a log with no EXDATE. The purge of L1 leaves that log with no
// row and no EXDATE, so the log goes.
func TestPurgeByID_DropsALogThatHidesNothing(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, l1, _ := seriesWithTwoLegacyRows(t, svc, "purge-drop")
	if err := svc.Delete(ctx, l1.ID); err != nil {
		t.Fatalf("Delete(L1): %v", err)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); !found {
		t.Fatal("log after the delete of L1 is missing")
	}
	if err := svc.PurgeByID(ctx, l1.ID); err != nil {
		t.Fatalf("PurgeByID(L1): %v", err)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); found {
		t.Error("log after the purge of L1 exists, want it gone")
	}
}
