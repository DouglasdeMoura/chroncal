package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/testutil"
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
// brings back the rows of the pull, which carry its token. The orphan rows
// keep the tokens of their own deletes, so the log keeps them as an orphan
// log. A restore by UID then brings them back.
func TestOrphanDeletes_BesideUnloggedMasterSeedItsLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID, DeleteToken: nextToken(t, svc)}); err != nil {
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
	if len(overrides) != 2 {
		t.Errorf("live overrides after the restore of the master = %d, want the two rows of the pull", len(overrides))
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	if overrides, err = svc.ListSeriesOverrides(ctx, 1, master.UID); err != nil || len(overrides) != 4 {
		t.Errorf("live overrides after the restore by UID = %d, %v; want the two rows of the pull and the two orphans", len(overrides), err)
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
// ErrDeletedAgain, and the EXDATE of the new delete stays.
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
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain: the new delete owns the row", err)
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
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID, DeleteToken: nextToken(t, svc)}); err != nil {
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
// then refuses with ErrDeletedAgain: the occurrence log owns the row.
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
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain", err)
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

// TestRestoreByUID_OrphanTwinsSkipByRowNotByMarker: two orphan rows of one
// occurrence in two calendars. A delete by ID hid R1, and its log names R1.
// A pull prune hid R2 and wrote a pruned marker, which names no row. The
// restore by UID leaves R1 to its own restore and brings back R2.
func TestRestoreByUID_OrphanTwinsSkipByRowNotByMarker(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-twins"
	at := time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC)
	calB := insertCalendar(t, svc, "B")
	r1 := legacyAt(t, svc, uid, 1, at)
	r2 := legacyAt(t, svc, uid, calB, at)
	if err := svc.Delete(ctx, r1.ID); err != nil {
		t.Fatalf("Delete(R1): %v", err)
	}
	if err := svc.q.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: r2.ID, DeleteToken: nextToken(t, svc)}); err != nil {
		t.Fatalf("pull prune of R2: %v", err)
	}
	if err := svc.q.RecordEventExdatePrune(ctx, storage.RecordEventExdatePruneParams{CalendarID: calB, Uid: uid, RecurrenceID: at.Format(time.RFC3339)}); err != nil {
		t.Fatalf("pruned marker of R2: %v", err)
	}
	if err := svc.RestoreByUID(ctx, uid); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, true, r1.ID)
	wantTrashed(t, svc, false, r2.ID)
}

// TestPurgeByID_RefusesALiveRow: the purge by ID reads the row in its own
// transaction and refuses a live row with ErrNotDeleted.
func TestPurgeByID_RefusesALiveRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.PurgeByID(ctx, row.ID); !errors.Is(err, ErrNotDeleted) {
		t.Fatalf("PurgeByID(live row) = %v, want ErrNotDeleted", err)
	}
	wantTrashed(t, svc, false, row.ID)
}

// TestUndoOrphanSeriesDelete_RefusesARowDeletedAgain: a series delete of
// orphan rows hides O. A restore by ID brings O back, and a second series
// delete hides it again. The undo of the first delete refuses with
// ErrDeletedAgain: its series log no longer names O.
func TestUndoOrphanSeriesDelete_RefusesARowDeletedAgain(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-again"
	o := orphanRows(t, svc, uid, 1)[0]
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, uid)
	if err != nil || meta.SeriesLogID == 0 {
		t.Fatalf("DeleteSeriesWithUndo = %+v, %v; want a series log ID", meta, err)
	}
	if err := svc.RestoreByID(ctx, o.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, uid); err != nil {
		t.Fatalf("second DeleteSeries: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, o.ID)
}

// TestUndo_NoLogDeleteRefusesARowDeletedAgain: a delete by ID beside a
// trashed master writes no log. The series comes back, the row comes back,
// and an occurrence delete hides the row again. The undo of the first delete
// refuses with ErrDeletedAgain: the occurrence log of the newer delete owns
// the row.
func TestUndo_NoLogDeleteRefusesARowDeletedAgain(t *testing.T) {
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
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, row.ID)
}

// TestUndoOrphanDeleteByID_RefusesARowDeletedAgain: a delete by ID of an
// orphan row writes an entry in its orphan log. A restore by ID drops that
// log, and a second delete writes a new one. The undo of the first delete
// refuses with ErrDeletedAgain and keeps the log of the second. The undo of
// the second brings the row back.
func TestUndoOrphanDeleteByID_RefusesARowDeletedAgain(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "orphan-by-id-again"
	o := orphanRows(t, svc, uid, 1)[0]
	meta1, err := svc.DeleteWithUndo(ctx, o.ID)
	if err != nil || meta1.ExdateLogID == 0 {
		t.Fatalf("first DeleteWithUndo = %+v, %v; want an orphan log ID", meta1, err)
	}
	if err := svc.RestoreByID(ctx, o.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
	}
	meta2, err := svc.DeleteWithUndo(ctx, o.ID)
	if err != nil {
		t.Fatalf("second DeleteWithUndo: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta1); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo(first) = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, o.ID)
	if err := svc.RestoreUndo(ctx, meta2); err != nil {
		t.Fatalf("RestoreUndo(second): %v", err)
	}
	wantTrashed(t, svc, false, o.ID)
}

// TestPurgeTrashEntry_InstanceTakesItsRows: a purge of an occurrence entry
// from the trash takes the rows of the log with it. The EXDATE stays: the
// delete stands for good. A restore by ID of the row is then impossible.
func TestPurgeTrashEntry_InstanceTakesItsRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("DeleteInstance: %v", err)
	}
	entries, err := svc.ListTrash(ctx, 1)
	if err != nil {
		t.Fatalf("ListTrash: %v", err)
	}
	purged := false
	for _, e := range entries {
		if e.Kind == TrashKindInstance {
			if err := svc.PurgeTrashEntry(ctx, e); err != nil {
				t.Fatalf("PurgeTrashEntry: %v", err)
			}
			purged = true
		}
	}
	if !purged {
		t.Fatal("trash holds no occurrence entry")
	}
	if err := svc.RestoreByID(ctx, row.ID); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("RestoreByID of the purged row = %v, want ErrNotDeleted", err)
	}
	if n := exdateCount(t, svc, master.ID, second); n != 1 {
		t.Errorf("EXDATEs after the purge = %d, want 1", n)
	}
	if _, found := exdateLogRows(t, svc, master.UID, second); found {
		t.Error("log after the purge exists, want it gone")
	}
}

// TestTrashInstance_PrunedMarkerIsNoEntry: a pruned marker is no delete of
// the user. A restore or a purge of it as an occurrence entry gives
// ErrNotDeleted, and the marker stays.
func TestTrashInstance_PrunedMarkerIsNoEntry(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.q.RecordEventExdatePrune(ctx, storage.RecordEventExdatePruneParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)}); err != nil {
		t.Fatalf("pruned marker: %v", err)
	}
	var markerID int64
	if err := svc.db.QueryRowContext(ctx, `SELECT id FROM event_exdate_deletes WHERE pruned = 1`).Scan(&markerID); err != nil {
		t.Fatalf("read the marker: %v", err)
	}
	entry := TrashEntry{Kind: TrashKindInstance, ID: markerID, CalendarID: 1, UID: master.UID}
	if err := svc.RestoreTrash(ctx, entry); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("RestoreTrash(marker) = %v, want ErrNotDeleted", err)
	}
	if err := svc.PurgeTrashEntry(ctx, entry); !errors.Is(err, ErrNotDeleted) {
		t.Errorf("PurgeTrashEntry(marker) = %v, want ErrNotDeleted", err)
	}
	var n int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM event_exdate_deletes WHERE id = ?`, markerID).Scan(&n); err != nil || n != 1 {
		t.Errorf("markers after the refusals = %d, %v; want 1", n, err)
	}
}

// TestPurge_TwoOccurrencesOfOneMasterSettleInTurn: one purge takes rows of
// two occurrences of one master. Each occurrence has a live override beside
// a log that owns its EXDATE. The settle of the second occurrence reads the
// master again, so the strip of the first stays.
func TestPurge_TwoOccurrencesOfOneMasterSettleInTurn(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	x, y := start.Add(24*time.Hour), start.Add(48*time.Hour)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "two-settles", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=4",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	purge := make([]int64, 0, 2)
	for i, at := range []time.Time{x, y} {
		l1 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L1-"+strconv.Itoa(i)), at)
		l2 := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "L2-"+strconv.Itoa(i)), at)
		if err := svc.DeleteInstance(ctx, 1, master.UID, at); err != nil {
			t.Fatalf("DeleteInstance: %v", err)
		}
		// A raw revive keeps the EXDATE beside the live row L2: the state
		// that the settle of the purge repairs.
		if err := reviveRow(svc, l2.ID); err != nil {
			t.Fatalf("revive L2: %v", err)
		}
		purge = append(purge, l1.ID)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE id IN (?, ?)`, purge[0], purge[1]); err != nil {
		t.Fatalf("age the rows: %v", err)
	}
	if _, err := svc.PurgeDeletedReport(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("PurgeDeletedReport: %v", err)
	}
	for _, at := range []time.Time{x, y} {
		if n := exdateCount(t, svc, master.ID, at); n != 0 {
			t.Errorf("EXDATEs of %s after the purge = %d, want 0: a live override sits there", at, n)
		}
	}
}

// masterInsertedBy inserts a live master of uid in calendar 1, as a pull
// does. The insert trigger of migration 057 drops the series-delete log and
// the occurrence logs of the UID in that calendar.
func masterInsertedBy(t *testing.T, svc *Service, uid string) {
	t.Helper()
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if _, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: uid, CalendarID: 1, Title: "Pulled master",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=2",
	}); err != nil {
		t.Fatalf("pull upsert of a master: %v", err)
	}
}

// TestUndo_TriggerDroppedLogRestores: a pull inserts a master, and the insert
// trigger drops the log of the delete. No log names the row then, but the
// row is as the delete left it: its deleted_at is the time of the delete. So
// the undo brings it back. The case holds for a series delete of orphan rows
// and for a delete by ID of an orphan row.
func TestUndo_TriggerDroppedLogRestores(t *testing.T) {
	for _, kind := range []string{"series delete", "delete by ID"} {
		t.Run(kind, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			uid := "dropped-" + strings.ReplaceAll(kind, " ", "-")
			o := orphanRows(t, svc, uid, 1)[0]
			var meta UndoMeta
			var err error
			if kind == "series delete" {
				meta, err = svc.DeleteSeriesWithUndo(ctx, 1, uid)
			} else {
				meta, err = svc.DeleteWithUndo(ctx, o.ID)
			}
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			masterInsertedBy(t, svc, uid)
			if err := svc.RestoreUndo(ctx, meta); err != nil {
				t.Fatalf("RestoreUndo: %v", err)
			}
			wantTrashed(t, svc, false, o.ID)
		})
	}
}

// TestUndoOccurrence_PartlyEmptiedEntryRefuses: an occurrence delete hid L1
// and L2. A restore by ID brings L1 back, and a second delete hides L1 again.
// The undo of the first delete refuses with ErrDeletedAgain, and L2 stays in
// the trash: the undo never brings back a part of the rows.
func TestUndoOccurrence_PartlyEmptiedEntryRefuses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, l1, l2 := seriesWithTwoLegacyRows(t, svc, "partly-emptied")
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil || len(meta.RowIDs) != 2 {
		t.Fatalf("DeleteInstanceWithUndo = %+v, %v; want two rows", meta, err)
	}
	if err := svc.RestoreByID(ctx, l1.ID); err != nil {
		t.Fatalf("RestoreByID(L1): %v", err)
	}
	if err := svc.DeleteInstance(ctx, 1, master.UID, second); err != nil {
		t.Fatalf("second DeleteInstance: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, l1.ID, l2.ID)
}

// TestUndo_LiveRowsGiveAlreadyRestored: the undo of a delete whose rows are
// live again gives ErrAlreadyRestored, for each kind of identity.
func TestUndo_LiveRowsGiveAlreadyRestored(t *testing.T) {
	for _, kind := range []string{"series delete", "delete by ID", "occurrence delete"} {
		t.Run(kind, func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			var meta UndoMeta
			var row int64
			var err error
			switch kind {
			case "occurrence delete":
				master, second, _ := singleSeriesWithOverrides(t, svc)
				meta, err = svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
				if err == nil {
					row = meta.RowIDs[0]
				}
			default:
				uid := "live-" + strings.ReplaceAll(kind, " ", "-")
				o := orphanRows(t, svc, uid, 1)[0]
				row = o.ID
				if kind == "series delete" {
					meta, err = svc.DeleteSeriesWithUndo(ctx, 1, uid)
				} else {
					meta, err = svc.DeleteWithUndo(ctx, o.ID)
				}
			}
			if err != nil {
				t.Fatalf("delete: %v", err)
			}
			if err := svc.RestoreByID(ctx, row); err != nil {
				t.Fatalf("RestoreByID: %v", err)
			}
			if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrAlreadyRestored) {
				t.Fatalf("RestoreUndo = %v, want ErrAlreadyRestored", err)
			}
		})
	}
}

// TestUndoOrphanDeleteByID_GuardsTheCalendar: the undo of a delete by ID of
// an orphan row writes the calendar of the row. A read-only calendar refuses
// it.
func TestUndoOrphanDeleteByID_GuardsTheCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	o := orphanRows(t, svc, "orphan-guard", 1)[0]
	meta, err := svc.DeleteWithUndo(ctx, o.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = 1`); err != nil {
		t.Fatalf("make the calendar read-only: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreUndo = %v, want ErrReadOnly", err)
	}
	wantTrashed(t, svc, true, o.ID)
}

// TestPurgeTrashEntry_InstanceGuardsTheRowCalendars: a purge of an
// occurrence entry hard-deletes the legacy rows of the log in other

// TestUndo_PullDeletedRowRefuses: a pull hides the row of a delete again
// with no log of that delete, in the same second. The pull wrote a new
// delete token, so the undo refuses with ErrChangedSince.
// The cases are a series delete of a calendar by a pull, and a pull prune
// with its marker.
func TestUndo_PullDeletedRowRefuses(t *testing.T) {
	t.Run("series delete of a pull", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		const uid = "pull-hidden"
		o := orphanRows(t, svc, uid, 1)[0]
		meta, err := svc.DeleteSeriesWithUndo(ctx, 1, uid)
		if err != nil {
			t.Fatalf("DeleteSeriesWithUndo: %v", err)
		}
		if err := svc.RestoreByID(ctx, o.ID); err != nil {
			t.Fatalf("RestoreByID: %v", err)
		}
		if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: uid, DeleteToken: nextToken(t, svc)}); err != nil {
			t.Fatalf("pull hides the rows: %v", err)
		}
		if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrChangedSince) {
			t.Fatalf("RestoreUndo = %v, want ErrChangedSince", err)
		}
		wantTrashed(t, svc, true, o.ID)
	})
	t.Run("pull prune", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		master, second, _ := singleSeriesWithOverrides(t, svc)
		meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
		if err != nil {
			t.Fatalf("DeleteInstanceWithUndo: %v", err)
		}
		row := meta.RowIDs[0]
		if err := svc.RestoreByID(ctx, row); err != nil {
			t.Fatalf("RestoreByID: %v", err)
		}
		if err := svc.q.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: row, DeleteToken: nextToken(t, svc)}); err != nil {
			t.Fatalf("pull prune: %v", err)
		}
		if err := svc.q.RecordEventExdatePrune(ctx, storage.RecordEventExdatePruneParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)}); err != nil {
			t.Fatalf("pruned marker: %v", err)
		}
		if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrChangedSince) {
			t.Fatalf("RestoreUndo = %v, want ErrChangedSince", err)
		}
		wantTrashed(t, svc, true, row)
	})
}

// TestUndo_NewerOwnerWinsOverTheIdentity: the series log of the delete still
// names the row, and a newer occurrence entry names it too. The newer delete
// wins, so the undo refuses with ErrDeletedAgain.
func TestUndo_NewerOwnerWinsOverTheIdentity(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	const uid = "newer-wins"
	o := orphanRows(t, svc, uid, 1)[0]
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, uid)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		`INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id, hidden_ids, owns_exdate, delete_seq) VALUES (1, ?, ?, ?, 0, 1)`,
		uid, o.RecurrenceID, "1:"+strconv.FormatInt(o.ID, 10)); err != nil {
		t.Fatalf("seed the newer entry: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Fatalf("RestoreUndo = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, o.ID)
}

// TestRestoreByIDAndUndo_AgreeBesideTrashedMaster: beside a trashed master,
// the restore by ID and the undo of a delete with no log use one rule. A row
// that a delete log names refuses on both paths.
func TestRestoreByIDAndUndo_AgreeBesideTrashedMaster(t *testing.T) {
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
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	// A newer delete log names the row: the series delete of the master
	// takes it in its log here.
	if _, err := svc.db.ExecContext(ctx, `UPDATE event_series_deletes SET hidden_ids = ? WHERE calendar_id = 1 AND uid = ?`,
		strconv.FormatInt(row.ID, 10), master.UID); err != nil {
		t.Fatalf("seed the series log: %v", err)
	}
	if err := svc.RestoreByID(ctx, row.ID); !errors.Is(err, ErrMasterInTrash) {
		t.Errorf("RestoreByID = %v, want ErrMasterInTrash", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrDeletedAgain) {
		t.Errorf("RestoreUndo = %v, want ErrDeletedAgain", err)
	}
	wantTrashed(t, svc, true, row.ID)
}

// readOnly makes calendarID read-only for events.
func readOnly(t *testing.T, svc *Service, calendarID int64) {
	t.Helper()
	if _, err := svc.db.ExecContext(context.Background(), `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = ?`, calendarID); err != nil {
		t.Fatalf("make calendar %d read-only: %v", calendarID, err)
	}
}

// TestPurge_GuardsEveryCalendarOfItsRows: each purge guards the calendar of
// each row that it hard-deletes. Three purges refuse a read-only calendar:
// the purge by ID of a row, of a master with a legacy row, and of an

// TestPurgeDeletedReport_SkipsReadOnlyCalendars: the age purge runs in the
// background. It keeps the rows of a calendar that refuses a write, names

// TestPurge_ReadOnlyCalendarsStillEmptyTheTrash: a purge of the local trash
// makes no server write, so it needs no write guard. Four purges take their
// rows from a read-only calendar. These are the purge by ID of a row and of a
// master, the entry purge, and the age purge.
func TestPurge_ReadOnlyCalendarsStillEmptyTheTrash(t *testing.T) {
	gone := func(t *testing.T, svc *Service, ids ...int64) {
		t.Helper()
		for _, id := range ids {
			if _, err := svc.GetIncludingDeleted(context.Background(), id); err == nil {
				t.Errorf("row %d after the purge exists, want it gone", id)
			}
		}
	}
	t.Run("purge by ID of a row", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		o := orphanRows(t, svc, "purge-ro-row", 1)[0]
		if err := svc.Delete(ctx, o.ID); err != nil {
			t.Fatalf("Delete: %v", err)
		}
		readOnly(t, svc, 1)
		if err := svc.PurgeByID(ctx, o.ID); err != nil {
			t.Fatalf("PurgeByID: %v", err)
		}
		gone(t, svc, o.ID)
	})
	t.Run("purge by ID of a master", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		master, second, _ := singleSeriesWithOverrides(t, svc)
		calL := insertCalendar(t, svc, "Legacy")
		legacy := legacyAt(t, svc, master.UID, calL, second.Add(48*time.Hour))
		if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
			t.Fatalf("DeleteSeries: %v", err)
		}
		readOnly(t, svc, 1)
		readOnly(t, svc, calL)
		if err := svc.PurgeByID(ctx, master.ID); err != nil {
			t.Fatalf("PurgeByID(master): %v", err)
		}
		gone(t, svc, master.ID, legacy.ID)
	})
	t.Run("purge of an occurrence entry", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		master, second, _ := singleSeriesWithOverrides(t, svc)
		calL := insertCalendar(t, svc, "Legacy")
		legacy := legacyAt(t, svc, master.UID, calL, second)
		meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
		if err != nil {
			t.Fatalf("DeleteInstanceWithUndo: %v", err)
		}
		readOnly(t, svc, 1)
		readOnly(t, svc, calL)
		entries, err := svc.ListTrash(ctx, 1)
		if err != nil {
			t.Fatalf("ListTrash: %v", err)
		}
		for _, e := range entries {
			if e.Kind == TrashKindInstance {
				if err := svc.PurgeTrashEntry(ctx, e); err != nil {
					t.Fatalf("PurgeTrashEntry: %v", err)
				}
			}
		}
		gone(t, svc, append(meta.RowIDs, legacy.ID)...)
	})
	t.Run("age purge", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		calRO := insertCalendar(t, svc, "Read-only")
		row := legacyAt(t, svc, "purge-ro-age", calRO, time.Date(2026, 5, 5, 9, 0, 0, 0, time.UTC))
		if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE id = ?`, row.ID); err != nil {
			t.Fatalf("age the row: %v", err)
		}
		readOnly(t, svc, calRO)
		report, err := svc.PurgeDeletedReport(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil || report.Purged != 1 {
			t.Fatalf("PurgeDeletedReport = %+v, %v; want 1 row purged", report, err)
		}
		gone(t, svc, row.ID)
	})
}

// nextToken takes a fresh delete token, as each soft-delete statement does.
func nextToken(t *testing.T, svc *Service) int64 {
	t.Helper()
	token, err := svc.q.NextEventDeleteToken(context.Background())
	if err != nil {
		t.Fatalf("delete token: %v", err)
	}
	return token
}

// rowToken reads the delete token of the row id.
func rowToken(t *testing.T, svc *Service, id int64) int64 {
	t.Helper()
	var token int64
	if err := svc.db.QueryRowContext(context.Background(), `SELECT delete_token FROM events WHERE id = ?`, id).Scan(&token); err != nil {
		t.Fatalf("read the delete token of row %d: %v", id, err)
	}
	return token
}

// TestUndoMeta_CarriesTheTokenOfEachDelete: each delete kind keeps the token
// that it wrote on its rows. The kinds are the delete by ID, the series
// delete, the occurrence delete, and the truncation. Each delete writes a new
// token.
func TestUndoMeta_CarriesTheTokenOfEachDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	seen := make(map[int64]bool)
	check := func(kind string, meta UndoMeta, err error, row int64) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", kind, err)
		}
		if meta.DeleteToken == 0 || meta.DeleteToken != rowToken(t, svc, row) || seen[meta.DeleteToken] {
			t.Errorf("%s: meta token %d, row token %d; want the new token of the row", kind, meta.DeleteToken, rowToken(t, svc, row))
		}
		seen[meta.DeleteToken] = true
	}
	single, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "token-single", CalendarID: 1, Title: "Single",
		StartTime: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("upsert single: %v", err)
	}
	meta, err := svc.DeleteWithUndo(ctx, single.ID)
	check("delete by ID of a master", meta, err, single.ID)

	master, second, third := singleSeriesWithOverrides(t, svc)
	meta, err = svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	check("occurrence delete", meta, err, meta.RowID)
	thirdRow, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	meta, err = svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, third)
	check("truncation", meta, err, thirdRow.ID)
	meta, err = svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	check("series delete of a master", meta, err, master.ID)

	o := orphanRows(t, svc, "token-orphan", 2)
	meta, err = svc.DeleteWithUndo(ctx, o[0].ID)
	check("delete by ID of an override", meta, err, o[0].ID)
	meta, err = svc.DeleteSeriesWithUndo(ctx, 1, "token-orphan")
	check("series delete of orphan rows", meta, err, o[1].ID)
}

// TestUndo_MasterAndSeriesRefuseANewerToken: the undo of a delete of a
// master restores the master only while it carries the token of that
// delete. A restore and a second delete give a newer token, so the undo of
// the first delete refuses with ErrChangedSince. The case holds for a delete
// by ID of a single master and for a series delete.
func TestUndo_MasterAndSeriesRefuseANewerToken(t *testing.T) {
	t.Run("delete by ID of a master", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		single, err := svc.UpsertByUID(ctx, UpsertParams{
			UID: "master-token", CalendarID: 1, Title: "Single",
			StartTime: time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC), EndTime: time.Date(2026, 5, 4, 10, 0, 0, 0, time.UTC),
		})
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		meta, err := svc.DeleteWithUndo(ctx, single.ID)
		if err != nil {
			t.Fatalf("DeleteWithUndo: %v", err)
		}
		if err := svc.RestoreByID(ctx, single.ID); err != nil {
			t.Fatalf("RestoreByID: %v", err)
		}
		if err := svc.Delete(ctx, single.ID); err != nil {
			t.Fatalf("second Delete: %v", err)
		}
		if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrChangedSince) {
			t.Fatalf("RestoreUndo = %v, want ErrChangedSince", err)
		}
		wantTrashed(t, svc, true, single.ID)
	})
	t.Run("series delete of a master", func(t *testing.T) {
		svc := newTestService(t)
		ctx := context.Background()
		master, _, _ := singleSeriesWithOverrides(t, svc)
		meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
		if err != nil {
			t.Fatalf("DeleteSeriesWithUndo: %v", err)
		}
		if err := svc.RestoreByID(ctx, master.ID); err != nil {
			t.Fatalf("RestoreByID: %v", err)
		}
		if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
			t.Fatalf("second DeleteSeries: %v", err)
		}
		if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrChangedSince) {
			t.Fatalf("RestoreUndo = %v, want ErrChangedSince", err)
		}
		wantTrashed(t, svc, true, master.ID)
	})
}

// TestUndoTruncation_RefusesARowOfANewerDelete: a row that the truncation log
// names carries another token than the truncation. A later delete wrote that
// token, so the undo of the truncation refuses with ErrChangedSince, and the
// rule of the master stays truncated.
func TestUndoTruncation_RefusesARowOfANewerDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, third := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
		CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, third)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET delete_token = ? WHERE id = ?`, nextToken(t, svc), row.ID); err != nil {
		t.Fatalf("seed the token of a later delete: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrChangedSince) {
		t.Fatalf("RestoreUndo = %v, want ErrChangedSince", err)
	}
	wantTrashed(t, svc, true, row.ID)
	m, err := svc.Get(ctx, master.ID)
	if err != nil || !strings.Contains(m.RecurrenceRule, "UNTIL") {
		t.Errorf("rule after the refused undo = %q, %v; want it still truncated", m.RecurrenceRule, err)
	}
}

// TestRestoreMaster_PairsTheRowsOfAPullByToken: a delete with no log hid row R,
// and a pull hid the master and its rows in the same second. R has the time
// of the master but another token. The restore of the master brings back the
// rows of the pull and leaves R in the trash. A row from before migration 057
// has token 0, and the time pairs it.
func TestRestoreMaster_PairsTheRowsOfAPullByToken(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy %v", legacy), func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			master, second, third := singleSeriesWithOverrides(t, svc)
			r, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
				CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339),
			})
			if err != nil {
				t.Fatalf("get R: %v", err)
			}
			other, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{
				CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339),
			})
			if err != nil {
				t.Fatalf("get the other row: %v", err)
			}
			if err := svc.q.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: r.ID, DeleteToken: nextToken(t, svc)}); err != nil {
				t.Fatalf("hide R: %v", err)
			}
			if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID, DeleteToken: nextToken(t, svc)}); err != nil {
				t.Fatalf("pull hides the series: %v", err)
			}
			// One second for every row: the time cannot tell them apart.
			if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE uid = ?`, master.UID); err != nil {
				t.Fatalf("align the times: %v", err)
			}
			if legacy {
				if _, err := svc.db.ExecContext(ctx, `UPDATE events SET delete_token = 0 WHERE uid = ?`, master.UID); err != nil {
					t.Fatalf("clear the tokens: %v", err)
				}
			}
			if err := svc.RestoreByID(ctx, master.ID); err != nil {
				t.Fatalf("RestoreByID(master): %v", err)
			}
			wantTrashed(t, svc, false, other.ID)
			wantTrashed(t, svc, !legacy, r.ID)
		})
	}
}

// TestMasterRevive_ConsumesOnlyItsSeriesLog: a calendar holds two series-delete
// logs of one UID. The revive of the master drops the log with the token of
// the master only. The other log stays.
func TestMasterRevive_ConsumesOnlyItsSeriesLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx,
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master, delete_token) VALUES (1, ?, '', 0, ?)`,
		master.UID, nextToken(t, svc)); err != nil {
		t.Fatalf("seed the other log: %v", err)
	}
	if err := reviveRow(svc, master.ID); err != nil {
		t.Fatalf("revive the master: %v", err)
	}
	if n := countLogsIn(t, svc, 1, master.UID); n != 1 {
		t.Errorf("series logs after the revive = %d, want 1: the other log stays", n)
	}
}

// TestRestoreByUID_OrphanRowsGroupByToken: with no master and no log, the
// restore by UID brings back the rows of the newest delete. Two deletes in
// one second have one time and two tokens: the token tells them apart. Rows
// from before migration 057 have token 0, and the time groups them.
func TestRestoreByUID_OrphanRowsGroupByToken(t *testing.T) {
	for _, legacy := range []bool{false, true} {
		t.Run(fmt.Sprintf("legacy %v", legacy), func(t *testing.T) {
			svc := newTestService(t)
			ctx := context.Background()
			uid := fmt.Sprintf("orphan-group-%v", legacy)
			rows := orphanRows(t, svc, uid, 3)
			first := nextToken(t, svc)
			second := nextToken(t, svc)
			for i, token := range []int64{first, second, second} {
				if err := svc.q.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: rows[i].ID, DeleteToken: token}); err != nil {
					t.Fatalf("hide row %d: %v", i, err)
				}
			}
			if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE uid = ?`, uid); err != nil {
				t.Fatalf("one second for the rows: %v", err)
			}
			if legacy {
				// The old rows: no token. The first row went one second
				// earlier.
				if _, err := svc.db.ExecContext(ctx, `UPDATE events SET delete_token = 0 WHERE uid = ?`, uid); err != nil {
					t.Fatalf("clear the tokens: %v", err)
				}
				if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-04-30T23:59:59Z' WHERE id = ?`, rows[0].ID); err != nil {
					t.Fatalf("move the first row: %v", err)
				}
			}
			if err := svc.RestoreByUID(ctx, uid); err != nil {
				t.Fatalf("RestoreByUID: %v", err)
			}
			wantTrashed(t, svc, true, rows[0].ID)
			wantTrashed(t, svc, false, rows[1].ID, rows[2].ID)
		})
	}
}

// TestUndoSeries_PurgedMasterRefusesAnotherLog is the five-step sequence:
//  1. A series delete of a master writes its log.
//  2. A purge of the master takes the log with it.
//  3. A pull imports an orphan row of the UID.
//  4. A series delete of the orphan row writes a new log.
//  5. The undo of the first delete names its own log, which is gone with
//     the master. It gives ErrMasterPurged and leaves the log of step 4
//     alone.
func TestUndoSeries_PurgedMasterRefusesAnotherLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil || meta.SeriesLogID == 0 {
		t.Fatalf("DeleteSeriesWithUndo = %+v, %v; want a series log ID", meta, err)
	}
	if err := svc.PurgeByID(ctx, master.ID); err != nil {
		t.Fatalf("PurgeByID(master): %v", err)
	}
	o := legacyAt(t, svc, master.UID, 1, second)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(orphan): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrMasterPurged) {
		t.Fatalf("RestoreUndo = %v, want ErrMasterPurged", err)
	}
	wantTrashed(t, svc, true, o.ID)
	if n := countLogsIn(t, svc, 1, master.UID); n != 1 {
		t.Errorf("series logs after the refused undo = %d, want the 1 of step 4", n)
	}
}

// TestUndoSeries_MasterLeavesTheMergedOrphanRows: a delete of orphan rows
// beside a trashed master merges its rows into the log of that master, with
// its own token. The undo of the series delete of the master brings back the
// master and the rows with its token. The merged row stays in the trash, and
// the log keeps it: a restore by UID then brings it back.
func TestUndoSeries_MasterLeavesTheMergedOrphanRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	// A pull brings back one row beside the trashed master, and a delete of
	// orphan rows hides it again into the log of the master.
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(orphan row): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	wantTrashed(t, svc, false, master.ID, own.ID)
	wantTrashed(t, svc, true, merged.ID)
	// The log keeps the merged row as an orphan log, so a restore by UID
	// brings it back.
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, merged.ID)
}

// TestRestoreSeries_LogAboveTheParameterLimit: a series-delete log names more
// rows than one SQLite statement takes as parameters (32766). The restore of
// the master reads and un-hides the rows in chunks, so every row comes back.
func TestRestoreSeries_LogAboveTheParameterLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("a large log")
	}
	svc := newTestService(t)
	ctx := context.Background()
	const n = 33000
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	master, err := svc.UpsertByUID(ctx, UpsertParams{
		UID: "big-log", CalendarID: 1, Title: "Series",
		StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=MINUTELY;COUNT=40000",
	})
	if err != nil {
		t.Fatalf("upsert master: %v", err)
	}
	token := nextToken(t, svc)
	for _, stmt := range []string{
		`WITH RECURSIVE k(i) AS (SELECT 1 UNION ALL SELECT i + 1 FROM k WHERE i < ` + strconv.Itoa(n) + `)
		 INSERT INTO events (uid, calendar_id, title, start_time, end_time, recurrence_id, deleted_at, delete_token)
		 SELECT 'big-log', 1, 'Row', '2026-05-04T09:00:00Z', '2026-05-04T10:00:00Z',
		        strftime('%Y-%m-%dT%H:%M:%SZ', '2026-05-04T09:00:00Z', '+' || i || ' minutes'), '2026-05-01T00:00:00Z', ` + strconv.FormatInt(token, 10) + ` FROM k`,
		`UPDATE events SET deleted_at = '2026-05-01T00:00:00Z', delete_token = ` + strconv.FormatInt(token, 10) + ` WHERE id = ` + strconv.FormatInt(master.ID, 10),
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master, delete_token)
		 SELECT 1, 'big-log', group_concat(id, ','), 1, ` + strconv.FormatInt(token, 10) + ` FROM events WHERE uid = 'big-log' AND recurrence_id != ''`,
	} {
		if _, err := svc.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed the large log: %v", err)
		}
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	var trashed int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM events WHERE uid = 'big-log' AND deleted_at IS NOT NULL`).Scan(&trashed); err != nil || trashed != 0 {
		t.Errorf("rows in the trash after the restore = %d, %v; want 0", trashed, err)
	}
}

// TestRestoreMasterByID_SkipsTheOrphanLogOfAnotherDelete: a master restore with
// no named log takes only the log that carries the token of the master. The
// sequence:
//
//  1. A series delete hides the master and its rows.
//  2. A pull brings back one row beside the trashed master.
//  3. A delete of orphan rows merges that row into the log of the master.
//  4. The undo of step 1 brings back the master. The log keeps the merged row
//     as an orphan log.
//  5. A pull hides the series again in one statement, with no log.
//
// The restore of the master by ID then pairs the rows of the pull of step 5.
// The merged row of step 3 stays in the trash, and its orphan log stays.
func TestRestoreMasterByID_SkipsTheOrphanLogOfAnotherDelete(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(orphan row): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID, DeleteToken: nextToken(t, svc)}); err != nil {
		t.Fatalf("pull hides the series: %v", err)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	wantTrashed(t, svc, false, master.ID, own.ID)
	wantTrashed(t, svc, true, merged.ID)
	if n := countLogsIn(t, svc, 1, master.UID); n != 1 {
		t.Errorf("series logs = %d, want the orphan log of step 3", n)
	}
}

// truncateAgainAtSameCutoff truncates the series of master at cutoff, imports
// a late override after the cutoff, and truncates again at the same cutoff.
// It returns the undo data of both truncations and the late row.
func truncateAgainAtSameCutoff(t *testing.T, svc *Service, master Event, cutoff, after time.Time) (first, second UndoMeta, late Event) {
	t.Helper()
	ctx := context.Background()
	first, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, cutoff)
	if err != nil {
		t.Fatalf("first truncation: %v", err)
	}
	late, err = svc.UpsertByUID(ctx, UpsertParams{
		UID: master.UID, CalendarID: 1, Title: "Late import",
		StartTime: after, EndTime: after.Add(time.Hour), RecurrenceID: after.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert late override: %v", err)
	}
	second, err = svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, cutoff)
	if err != nil {
		t.Fatalf("second truncation: %v", err)
	}
	return first, second, late
}

// masterRule returns the recurrence rule of the master row id.
func masterRule(t *testing.T, svc *Service, id int64) string {
	t.Helper()
	r, err := svc.q.GetEventIncludingDeleted(context.Background(), id)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	return storage.NullableToString(r.RecurrenceRule)
}

// TestUndoTruncation_SecondAtSameCutoffRestoresItsOwnRows: two truncations at
// the same cutoff have one log row each. The undo of the second brings back
// its own row and the rule after the first truncation. The rows of the first
// stay in the trash with their log. The undo of the first then brings them
// back with the rule before both truncations.
func TestUndoTruncation_SecondAtSameCutoffRestoresItsOwnRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	firstRows := trashedOverrideIDs(t, svc, master.UID)
	if len(firstRows) != 0 {
		t.Fatalf("trashed rows before the truncation = %v", firstRows)
	}
	firstMeta, secondMeta, late := truncateAgainAtSameCutoff(t, svc, master, second, third.Add(24*time.Hour))
	firstRows = slices.DeleteFunc(trashedOverrideIDs(t, svc, master.UID), func(id int64) bool { return id == late.ID })
	if len(firstRows) != 2 {
		t.Fatalf("rows of the first truncation = %v, want 2", firstRows)
	}
	truncatedRule := masterRule(t, svc, master.ID)
	if err := svc.RestoreUndo(ctx, secondMeta); err != nil {
		t.Fatalf("RestoreUndo(second): %v", err)
	}
	wantTrashed(t, svc, false, late.ID)
	wantTrashed(t, svc, true, firstRows...)
	if got := masterRule(t, svc, master.ID); got != truncatedRule {
		t.Errorf("rule after the undo of the second = %q, want the rule of the first truncation %q", got, truncatedRule)
	}
	if err := svc.restoreTruncationByLogID(ctx, truncationLogID(t, svc, master.UID, firstMeta.DeleteToken), firstMeta.DeleteToken); err != nil {
		t.Fatalf("undo of the first: %v", err)
	}
	wantTrashed(t, svc, false, firstRows...)
	if got := masterRule(t, svc, master.ID); got != master.RecurrenceRule {
		t.Errorf("rule after both undos = %q, want %q", got, master.RecurrenceRule)
	}
}

// TestRestoreTruncation_OlderFirstRefuses: the restore of the older of two
// truncations at the same cutoff refuses while the newer one is in the trash.
// The newer one recorded the rule that the older one wrote. After the restore
// of the newer one, the older one restores the full rule.
func TestRestoreTruncation_OlderFirstRefuses(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	firstMeta, secondMeta, late := truncateAgainAtSameCutoff(t, svc, master, second, third.Add(24*time.Hour))
	firstLog := truncationLogID(t, svc, master.UID, firstMeta.DeleteToken)
	var newer *NewerTruncationError
	if err := svc.restoreTruncationByLogID(ctx, firstLog, 0); !errors.As(err, &newer) || !newer.Cutoff.Equal(second) {
		t.Fatalf("restore the older first = %v, want NewerTruncationError at %s", err, second)
	}
	wantTrashed(t, svc, true, late.ID)
	if err := svc.restoreTruncationByLogID(ctx, truncationLogID(t, svc, master.UID, secondMeta.DeleteToken), 0); err != nil {
		t.Fatalf("restore the newer: %v", err)
	}
	if err := svc.restoreTruncationByLogID(ctx, firstLog, 0); err != nil {
		t.Fatalf("restore the older: %v", err)
	}
	wantTrashed(t, svc, false, late.ID)
	if got := masterRule(t, svc, master.ID); got != master.RecurrenceRule {
		t.Errorf("rule after both restores = %q, want %q", got, master.RecurrenceRule)
	}
}

// TestRestoreTruncation_NewerAtAnEarlierCutoffComesFirst: a truncation at the
// later cutoff (the "Mar 10" case), then a newer one at an earlier cutoff (the
// "Mar 1" case). The restore of the first refuses and names the cutoff of the
// newer one. The restore of the newer one, then of the first, gives back the
// full rule and every row.
func TestRestoreTruncation_NewerAtAnEarlierCutoffComesFirst(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	later, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, third)
	if err != nil {
		t.Fatalf("truncate at the later cutoff: %v", err)
	}
	earlier, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("truncate at the earlier cutoff: %v", err)
	}
	laterLog := truncationLogID(t, svc, master.UID, later.DeleteToken)
	err = svc.restoreTruncationByLogID(ctx, laterLog, 0)
	var newer *NewerTruncationError
	if !errors.As(err, &newer) || !errors.Is(err, ErrNewerTruncation) || !newer.Cutoff.Equal(second) {
		t.Fatalf("restore the later cutoff first = %v, want NewerTruncationError at %s", err, second)
	}
	if !strings.Contains(err.Error(), "restore the newer truncation of this series first (cutoff "+second.Format(time.RFC3339)+")") {
		t.Errorf("text = %q, want the cutoff of the newer truncation", err.Error())
	}
	// The undo of the older truncation can succeed after the restore of the
	// newer one. It is a refusal for now (ErrUndoNotYet), not a permanent
	// one.
	if err := svc.RestoreUndo(ctx, later); !errors.Is(err, ErrUndoNotYet) || errors.Is(err, ErrUndoNotPossible) || !errors.Is(err, ErrNewerTruncation) {
		t.Errorf("RestoreUndo(later) = %v, want ErrUndoNotYet with ErrNewerTruncation", err)
	}
	if err := svc.restoreTruncationByLogID(ctx, truncationLogID(t, svc, master.UID, earlier.DeleteToken), 0); err != nil {
		t.Fatalf("restore the earlier cutoff: %v", err)
	}
	if err := svc.restoreTruncationByLogID(ctx, laterLog, 0); err != nil {
		t.Fatalf("restore the later cutoff: %v", err)
	}
	if got := masterRule(t, svc, master.ID); got != master.RecurrenceRule {
		t.Errorf("rule after both restores = %q, want %q", got, master.RecurrenceRule)
	}
	if rows := trashedOverrideIDs(t, svc, master.UID); len(rows) != 0 {
		t.Errorf("trashed rows after both restores = %v, want none", rows)
	}
}

// TestUndoTruncation_StaleGuardInTheTransaction: an edit of the master after
// the truncation refuses the undo with ErrMasterAdvanced, and the log stays.
// The guard reads the master in the transaction of the restore: no master
// read runs outside it.
func TestUndoTruncation_StaleGuardInTheTransaction(t *testing.T) {
	svc, spy := spiedService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET updated_at = '2099-01-01T00:00:00Z' WHERE id = ?`, master.ID); err != nil {
		t.Fatalf("advance the master: %v", err)
	}
	spy.names = nil
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrMasterAdvanced) {
		t.Fatalf("RestoreUndo = %v, want ErrMasterAdvanced", err)
	}
	if spy.ran("GetEvent") || spy.ran("GetSeriesMaster") {
		t.Errorf("queries outside the transaction = %v, want no read of the master", spy.names)
	}
	truncationLogID(t, svc, master.UID, meta.DeleteToken)
}

// TestUndoTruncation_StaleGuardAllowsNoDrift: an edit of the master one
// second after the truncation refuses the undo. The baseline of the guard
// is the updated_at that the truncation read back, so the guard allows no
// drift (issue #862).
func TestUndoTruncation_StaleGuardAllowsNoDrift(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}
	later := meta.MasterUpdatedBefore.Add(time.Second).UTC().Format(time.RFC3339)
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET updated_at = ? WHERE id = ?`, later, master.ID); err != nil {
		t.Fatalf("advance the master: %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); !errors.Is(err, ErrMasterAdvanced) {
		t.Fatalf("RestoreUndo = %v, want ErrMasterAdvanced", err)
	}
	truncationLogID(t, svc, master.UID, meta.DeleteToken)
}

// trashedOverrideIDs returns the IDs of the trashed override rows of uid.
func trashedOverrideIDs(t *testing.T, svc *Service, uid string) []int64 {
	t.Helper()
	rows, err := svc.q.ListTrashedEventsByUID(context.Background(), uid)
	if err != nil {
		t.Fatalf("list trashed rows: %v", err)
	}
	var ids []int64
	for _, r := range rows {
		if r.RecurrenceID != "" {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

// truncationLogID returns the ID of the truncation log of uid with token.
func truncationLogID(t *testing.T, svc *Service, uid string, token int64) int64 {
	t.Helper()
	var id int64
	if err := svc.db.QueryRowContext(context.Background(),
		`SELECT id FROM event_truncate_deletes WHERE uid = ? AND delete_token = ?`, uid, token).Scan(&id); err != nil {
		t.Fatalf("truncation log of token %d: %v", token, err)
	}
	return id
}

// outsideTxSpy wraps a DBTX and records the name of each query that runs
// outside a transaction. A query in a transaction runs on the *sql.Tx of
// Queries.WithTx, not on this wrapper.
type outsideTxSpy struct {
	storage.DBTX
	names []string
}

func (d *outsideTxSpy) record(query string) {
	if _, rest, ok := strings.Cut(query, "-- name: "); ok {
		name, _, _ := strings.Cut(rest, " ")
		d.names = append(d.names, name)
	}
}

func (d *outsideTxSpy) QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row {
	d.record(query)
	return d.DBTX.QueryRowContext(ctx, query, args...)
}

func (d *outsideTxSpy) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	d.record(query)
	return d.DBTX.QueryContext(ctx, query, args...)
}

// ran reports whether the spy saw a query whose name starts with prefix.
func (d *outsideTxSpy) ran(prefix string) bool {
	return slices.ContainsFunc(d.names, func(n string) bool { return strings.HasPrefix(n, prefix) })
}

// spiedService returns a service whose queries outside a transaction go
// through an outsideTxSpy.
func spiedService(t *testing.T) (*Service, *outsideTxSpy) {
	t.Helper()
	db, q := testutil.NewTestDB(t)
	spy := &outsideTxSpy{DBTX: db}
	svc := NewService(db, q)
	svc.q = storage.New(spy)
	return svc, spy
}

// TestRestoreTruncation_ReadsTheLogInTheTransaction: the restore of a
// truncation reads its log row in its transaction. A read outside it lets a
// concurrent restore consume the row between the read and the writes.
func TestRestoreTruncation_ReadsTheLogInTheTransaction(t *testing.T) {
	svc, spy := spiedService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo: %v", err)
	}
	spy.names = nil
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	meta, err = svc.DeleteFromInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteFromInstanceWithUndo(again): %v", err)
	}
	if err := svc.restoreTruncationByLogID(ctx, truncationLogID(t, svc, master.UID, meta.DeleteToken), 0); err != nil {
		t.Fatalf("restoreTruncationByLogID: %v", err)
	}
	if spy.ran("GetEventTruncateDelete") {
		t.Errorf("queries outside the transaction = %v, want no read of a truncation log", spy.names)
	}
}

// TestUndoSingle_ReadsTheMasterInTheTransaction: the undo of a single delete
// reads the master in its transaction and chooses its path there. A series
// delete between a read outside it and the restore would send the undo down
// the path of a live master.
func TestUndoSingle_ReadsTheMasterInTheTransaction(t *testing.T) {
	svc, spy := spiedService(t)
	ctx := context.Background()
	master, second, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteInstanceWithUndo(ctx, 1, master.UID, second)
	if err != nil {
		t.Fatalf("DeleteInstanceWithUndo: %v", err)
	}
	spy.names = nil
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if spy.ran("GetEventIncludingDeleted") {
		t.Errorf("queries outside the transaction = %v, want no read of the master", spy.names)
	}
}

// TestRestoreUndo_ClassifiesPermanentRefusals: a read-only calendar is a
// permanent refusal of the undo. It matches ErrUndoNotPossible and keeps the
// cause and its text. A canceled context is transient and does not match.
func TestRestoreUndo_ClassifiesPermanentRefusals(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	ev := createEvent(t, svc)
	meta, err := svc.DeleteWithUndo(ctx, ev.ID)
	if err != nil {
		t.Fatalf("DeleteWithUndo: %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := svc.RestoreUndo(canceled, meta); err == nil || errors.Is(err, ErrUndoNotPossible) {
		t.Errorf("RestoreUndo(canceled) = %v, want a transient error", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = 1`); err != nil {
		t.Fatalf("make the calendar read-only: %v", err)
	}
	err = svc.RestoreUndo(ctx, meta)
	if !errors.Is(err, ErrUndoNotPossible) || !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreUndo(read-only) = %v, want ErrUndoNotPossible with ErrReadOnly", err)
	}
	if !strings.Contains(err.Error(), "read-only") {
		t.Errorf("text = %q, want the text of the cause", err.Error())
	}
	if err := svc.RestoreUndo(ctx, UndoMeta{Kind: UndoKind(99), UID: "x"}); !errors.Is(err, ErrUndoNotPossible) {
		t.Errorf("RestoreUndo(unknown kind) = %v, want ErrUndoNotPossible", err)
	}
}

// TestOrphanDelete_JoinsTheLogOfTheTrashedMaster: a delete of orphan rows
// beside a trashed master joins the log with the token of that master. It
// does not join the newest log of the calendar. The sequence:
//
//  1. A series delete hides the master and its rows.
//  2. A pull brings back one row (merged) beside the trashed master.
//  3. A delete of orphan rows merges that row into the log of the master.
//  4. The undo of step 1 brings back the master. The log keeps the merged row
//     as an orphan log.
//  5. A pull hides the series again in one statement, with no log.
//  6. A pull brings back the other row (own) beside the trashed master.
//  7. A delete of orphan rows hides it again.
//
// Step 7 writes a new log with the token of the master. The orphan log of
// step 3 keeps only its row. The restore of the master brings back the
// master. The row of step 7 keeps the token of its own delete, so the log of
// step 7 keeps it as an orphan log. A restore by UID then reverses the newest
// delete, step 7, and the row of step 3 stays in the trash.
func TestOrphanDelete_JoinsTheLogOfTheTrashedMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the merged row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(merged row): %v", err)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if err := svc.q.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{CalendarID: 1, Uid: master.UID, DeleteToken: nextToken(t, svc)}); err != nil {
		t.Fatalf("pull hides the series: %v", err)
	}
	if err := reviveRow(svc, own.ID); err != nil {
		t.Fatalf("revive the own row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(own row): %v", err)
	}
	trashed, err := svc.q.GetEventIncludingDeleted(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	var hidden string
	if err := svc.db.QueryRowContext(ctx, `SELECT hidden_ids FROM event_series_deletes WHERE uid = ? AND delete_token = ? AND had_master = 1`, master.UID, trashed.DeleteToken).Scan(&hidden); err != nil {
		t.Fatalf("log with the token of the master: %v", err)
	}
	if hidden != strconv.FormatInt(own.ID, 10) {
		t.Errorf("log of the master = %q, want the row of step 7 (%d)", hidden, own.ID)
	}
	if n := countLogsIn(t, svc, 1, master.UID); n != 2 {
		t.Errorf("series logs = %d, want the orphan log and the log of the master", n)
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatalf("RestoreByID(master): %v", err)
	}
	wantTrashed(t, svc, false, master.ID)
	wantTrashed(t, svc, true, own.ID, merged.ID)
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, own.ID)
	wantTrashed(t, svc, true, merged.ID)
}

// TestUndoSeries_ReadsTheMasterInTheTransaction: the undo of a series delete
// reads the master in its transaction and chooses its path there. No master
// read runs outside it.
func TestUndoSeries_ReadsTheMasterInTheTransaction(t *testing.T) {
	svc, spy := spiedService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	spy.names = nil
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo: %v", err)
	}
	if spy.ran("GetEvent") {
		t.Errorf("queries outside the transaction = %v, want no read of the master", spy.names)
	}
}

// TestUndoSeries_VanishedMasterGivesMasterPurged: a purge took the master and
// its log. Undo data that names no log (from before the log ID) then has
// nothing to reverse. The undo gives ErrMasterPurged, not ErrNotDeleted or a
// raw sql.ErrNoRows.
func TestUndoSeries_VanishedMasterGivesMasterPurged(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil {
		t.Fatalf("DeleteSeriesWithUndo: %v", err)
	}
	if err := svc.PurgeByID(ctx, master.ID); err != nil {
		t.Fatalf("PurgeByID(master): %v", err)
	}
	meta.SeriesLogID = 0
	err = svc.RestoreUndo(ctx, meta)
	if !errors.Is(err, ErrMasterPurged) || errors.Is(err, sql.ErrNoRows) || errors.Is(err, ErrNotDeleted) {
		t.Fatalf("RestoreUndo = %v, want ErrMasterPurged", err)
	}
}

// TestPurgeMaster_RefusalNamesOwnAndLegacyRows: the refusal of the purge of a
// master names every live override row that its series owns, from the one
// read of the view. A legacy override in another calendar counts, and the
// IDs come in ID order.
func TestPurgeMaster_RefusalNamesOwnAndLegacyRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, third := singleSeriesWithOverrides(t, svc)
	own, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, own.ID); err != nil {
		t.Fatalf("revive the own row: %v", err)
	}
	legacy := legacyAt(t, svc, master.UID, insertCalendar(t, svc, "Legacy"), third.Add(24*time.Hour))
	err = svc.PurgeByID(ctx, master.ID)
	var refusal *SeriesHasLiveOverridesError
	if !errors.As(err, &refusal) || !slices.Equal(refusal.IDs, []int64{own.ID, legacy.ID}) {
		t.Fatalf("PurgeByID err = %v, want a refusal that names rows %d and %d", err, own.ID, legacy.ID)
	}
}

// orphanRowAt writes a live override row of uid in calendar 1 at at, with no
// master of the UID.
func orphanRowAt(t *testing.T, svc *Service, uid string, at time.Time) Event {
	t.Helper()
	e, err := svc.UpsertByUID(context.Background(), UpsertParams{
		UID: uid, CalendarID: 1, Title: "Orphan",
		StartTime: at, EndTime: at.Add(time.Hour), RecurrenceID: at.Format(time.RFC3339),
	})
	if err != nil {
		t.Fatalf("upsert orphan row: %v", err)
	}
	return e
}

// TestRestoreByUID_PurgedLogDoesNotShadowAnOlderLog: the sequence:
//
//  1. A delete of orphan rows (A) hides row a and writes its log.
//  2. A second delete of orphan rows (B) hides row b and writes its log.
//  3. A purge of row b empties the log of B.
//
// The purge trigger drops the empty orphan log of B. A restore by UID then
// reverses A and brings back row a.
func TestRestoreByUID_PurgedLogDoesNotShadowAnOlderLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	at := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	a := orphanRowAt(t, svc, "orphan-ab", at)
	if err := svc.DeleteSeries(ctx, 1, a.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	b := orphanRowAt(t, svc, a.UID, at.Add(24*time.Hour))
	if err := svc.DeleteSeries(ctx, 1, a.UID); err != nil {
		t.Fatalf("DeleteSeries(B): %v", err)
	}
	if n := countLogsIn(t, svc, 1, a.UID); n != 2 {
		t.Fatalf("series logs before the purge = %d, want 2", n)
	}
	if err := svc.PurgeByID(ctx, b.ID); err != nil {
		t.Fatalf("PurgeByID(b): %v", err)
	}
	if n := countLogsIn(t, svc, 1, a.UID); n != 1 {
		t.Errorf("series logs after the purge of b = %d, want the log of A only", n)
	}
	if err := svc.RestoreByUID(ctx, a.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, a.ID)
}

// TestRestoreByUID_TakesTheLogOfItsCandidate: an empty orphan log from before
// the purge trigger drops it is the newest log of the calendar. The restore
// by UID takes the log of its candidate choice, never the newest log, so it
// still reverses the older delete.
func TestRestoreByUID_TakesTheLogOfItsCandidate(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	at := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	a := orphanRowAt(t, svc, "orphan-empty", at)
	if err := svc.DeleteSeries(ctx, 1, a.UID); err != nil {
		t.Fatalf("DeleteSeries(A): %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master) VALUES (1, ?, '', 0)`, a.UID); err != nil {
		t.Fatalf("insert an empty orphan log: %v", err)
	}
	if err := svc.RestoreByUID(ctx, a.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, a.ID)
}

// TestMasterRevive_KeepsTheMergedOrphanRows: a pull brings back a master whose
// log holds rows that a delete of orphan rows merged in. The revive trigger
// keeps those rows as an orphan log, and drops the rows of the master from
// it. A restore by UID then brings back the merged row.
func TestMasterRevive_KeepsTheMergedOrphanRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the merged row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(merged row): %v", err)
	}
	if err := reviveRow(svc, master.ID); err != nil {
		t.Fatalf("pull revives the master: %v", err)
	}
	var hidden string
	var hadMaster, token int64
	if err := svc.db.QueryRowContext(ctx, `SELECT hidden_ids, had_master, delete_token FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&hidden, &hadMaster, &token); err != nil {
		t.Fatalf("log after the revive: %v", err)
	}
	if hidden != strconv.FormatInt(merged.ID, 10) || hadMaster != 0 || token != 0 {
		t.Errorf("log after the revive = %q, had_master %d, token %d; want the merged row %d as an orphan log", hidden, hadMaster, token, merged.ID)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, merged.ID)
	wantTrashed(t, svc, true, own.ID)
}

// TestPurgeOld_KeepsAYoungMergedRow: a later delete of orphan rows merges a
// row into the log of an old master. The age purge of the master keeps that
// row while it is younger than the cutoff. The rows of the series delete go
// with the master. The log row stays in place as an orphan log with the
// young row. Its ID stays, and its time is the time of the row. The undo of
// the orphan delete names that log, and it still brings the row back.
func TestPurgeOld_KeepsAYoungMergedRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the merged row: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = ? AND deleted_at IS NOT NULL`, master.UID); err != nil {
		t.Fatalf("age the series delete: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE event_series_deletes SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = ?`, master.UID); err != nil {
		t.Fatalf("age the log: %v", err)
	}
	meta, err := svc.DeleteSeriesWithUndo(ctx, 1, master.UID)
	if err != nil || meta.SeriesLogID == 0 {
		t.Fatalf("DeleteSeriesWithUndo(merged row) = %+v, %v; want a series log ID", meta, err)
	}
	if _, err := svc.PurgeOld(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("PurgeOld: %v", err)
	}
	for _, id := range []int64{master.ID, own.ID} {
		if _, err := svc.GetIncludingDeleted(ctx, id); !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("row %d after the purge: err = %v, want it purged", id, err)
		}
	}
	wantTrashed(t, svc, true, merged.ID)
	row, err := svc.q.GetEventIncludingDeleted(ctx, merged.ID)
	if err != nil {
		t.Fatalf("get the merged row: %v", err)
	}
	var logID, hadMaster, token int64
	var hidden, deletedAt string
	if err := svc.db.QueryRowContext(ctx, `SELECT id, hidden_ids, had_master, delete_token, deleted_at FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&logID, &hidden, &hadMaster, &token, &deletedAt); err != nil {
		t.Fatalf("orphan log after the purge: %v", err)
	}
	if logID != meta.SeriesLogID || hidden != strconv.FormatInt(merged.ID, 10) || hadMaster != 0 || token != 0 {
		t.Errorf("log after the purge = id %d, %q, had_master %d, token %d; want log %d as an orphan log of row %d", logID, hidden, hadMaster, token, meta.SeriesLogID, merged.ID)
	}
	if deletedAt != *row.DeletedAt {
		t.Errorf("time of the log = %s, want the time of the young row %s", deletedAt, *row.DeletedAt)
	}
	if err := svc.RestoreUndo(ctx, meta); err != nil {
		t.Fatalf("RestoreUndo(orphan delete): %v", err)
	}
	wantTrashed(t, svc, false, merged.ID)
}

// TestMasterRevive_LegacyMasterKeepsAnOrphanLog: a master from before the
// token (token 0) comes back. Its revive consumes the log with a master
// only. An orphan log of the calendar has token 0 too, and it stays.
func TestMasterRevive_LegacyMasterKeepsAnOrphanLog(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, third := singleSeriesWithOverrides(t, svc)
	merged, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	for _, stmt := range []string{
		`UPDATE events SET deleted_at = '2026-05-01T00:00:00Z', delete_token = 0 WHERE id = ` + strconv.FormatInt(master.ID, 10),
		`UPDATE events SET deleted_at = '2026-05-02T00:00:00Z', delete_token = 7 WHERE id = ` + strconv.FormatInt(merged.ID, 10),
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master, delete_token) VALUES (1, '` + master.UID + `', '', 1, 0)`,
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master, delete_token) VALUES (1, '` + master.UID + `', '` + strconv.FormatInt(merged.ID, 10) + `', 0, 0)`,
	} {
		if _, err := svc.db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	if err := reviveRow(svc, master.ID); err != nil {
		t.Fatalf("revive the master: %v", err)
	}
	var hadMaster int64
	var hidden string
	if err := svc.db.QueryRowContext(ctx, `SELECT had_master, hidden_ids FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&hadMaster, &hidden); err != nil {
		t.Fatalf("logs after the revive: %v (want only the orphan log)", err)
	}
	if hadMaster != 0 || hidden != strconv.FormatInt(merged.ID, 10) {
		t.Errorf("log after the revive = had_master %d, %q; want the orphan log of row %d", hadMaster, hidden, merged.ID)
	}
}

// TestRestoreByUID_TrashedMasterLeavesTheMergedRows: a restore by UID of a
// master in the trash brings back the rows with the token of the master. A
// row that a delete of orphan rows merged into its log stays in the trash,
// and the log keeps it as an orphan log. A second restore by UID then brings
// it back.
func TestRestoreByUID_TrashedMasterLeavesTheMergedRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	get := func(at time.Time) storage.Event {
		t.Helper()
		r, err := svc.q.GetEventByCalendarUIDAndRecurrenceIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDIncludingDeletedParams{
			CalendarID: 1, Uid: master.UID, RecurrenceID: at.Format(time.RFC3339),
		})
		if err != nil {
			t.Fatalf("get override %s: %v", at, err)
		}
		return r
	}
	own, merged := get(second), get(third)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if err := reviveRow(svc, merged.ID); err != nil {
		t.Fatalf("revive the merged row: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries(merged row): %v", err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, master.ID, own.ID)
	wantTrashed(t, svc, true, merged.ID)
	var hadMaster, token int64
	if err := svc.db.QueryRowContext(ctx, `SELECT had_master, delete_token FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&hadMaster, &token); err != nil || hadMaster != 0 || token != 0 {
		t.Errorf("log after the restore = had_master %d, token %d, %v; want an orphan log", hadMaster, token, err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID(again): %v", err)
	}
	wantTrashed(t, svc, false, merged.ID)
}

// TestOverrideRevive_DropsTheEmptyOrphanLogBesideATrashedMaster: an orphan
// log names one row, and the master of its calendar is in the trash. The
// revive of the row empties the log. The trigger drops it: an empty orphan
// log records nothing. The log of the master stays.
func TestOverrideRevive_DropsTheEmptyOrphanLogBesideATrashedMaster(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, third := singleSeriesWithOverrides(t, svc)
	row, err := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
	if err != nil {
		t.Fatalf("get override: %v", err)
	}
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatalf("DeleteSeries: %v", err)
	}
	if _, err := svc.db.ExecContext(ctx, `INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master) VALUES (1, ?, ?, 0)`, master.UID, strconv.FormatInt(row.ID, 10)); err != nil {
		t.Fatalf("seed the orphan log: %v", err)
	}
	if err := reviveRow(svc, row.ID); err != nil {
		t.Fatalf("revive the row: %v", err)
	}
	var orphans, masters int
	if err := svc.db.QueryRowContext(ctx, `SELECT COUNT(*) FILTER (WHERE had_master = 0), COUNT(*) FILTER (WHERE had_master = 1) FROM event_series_deletes WHERE uid = ?`, master.UID).Scan(&orphans, &masters); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	if orphans != 0 || masters != 1 {
		t.Errorf("logs after the revive = %d orphan, %d with a master; want 0 and 1", orphans, masters)
	}
}

// TestRestoreMasterByID_LegacyMasterLeavesTheMergedRows: a master from before
// the token (token 0) has a log with two rows. Its own row has token 0. A
// later delete of orphan rows merged the other row in, with token 9. The restore of the master
// brings back the rows with token 0. The merged row stays in the trash.
func TestRestoreMasterByID_LegacyMasterLeavesTheMergedRows(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, second, third := singleSeriesWithOverrides(t, svc)
	own, _ := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: second.Format(time.RFC3339)})
	merged, _ := svc.q.GetEventByCalendarUIDAndRecurrenceID(ctx, storage.GetEventByCalendarUIDAndRecurrenceIDParams{CalendarID: 1, Uid: master.UID, RecurrenceID: third.Format(time.RFC3339)})
	ids := strconv.FormatInt(own.ID, 10) + "," + strconv.FormatInt(merged.ID, 10)
	for _, stmt := range []string{
		`UPDATE events SET deleted_at = '2026-05-01T00:00:00Z', delete_token = 0 WHERE id IN (` + strconv.FormatInt(master.ID, 10) + `,` + strconv.FormatInt(own.ID, 10) + `)`,
		`UPDATE events SET deleted_at = '2026-05-02T00:00:00Z', delete_token = 9 WHERE id = ` + strconv.FormatInt(merged.ID, 10),
		`INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master, delete_token) VALUES (1, '` + master.UID + `', '` + ids + `', 1, 0)`,
	} {
		if _, err := svc.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.RestoreByID(ctx, master.ID); err != nil {
		t.Fatal(err)
	}
	wantTrashed(t, svc, false, master.ID, own.ID)
	wantTrashed(t, svc, true, merged.ID)
}

// TestRestoreByUID_OrphanNewestDeleteIsATotalOrder: three orphan deletes, in
// the order of the row IDs. Y has 10:00:05 with token 3. Calendar 1 has
// 10:00:01 with token 5, and 10:00:03 with token 0. A comparator that mixes the
// token and the time has a cycle here, so the result depends on the order of
// the rows. By time, then by token, the newest delete is the one of Y.
func TestRestoreByUID_OrphanNewestDeleteIsATotalOrder(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calY := insertCalendar(t, svc, "Y")
	base := time.Date(2026, 6, 1, 9, 0, 0, 0, time.UTC)
	mk := func(cal int64, i int, at string, tok int64) int64 {
		r, err := svc.UpsertByUID(ctx, UpsertParams{UID: "zz4", CalendarID: cal, Title: "O", StartTime: base.Add(time.Duration(i) * time.Hour), EndTime: base.Add(time.Duration(i)*time.Hour + time.Minute), RecurrenceID: base.Add(time.Duration(i) * time.Hour).Format(time.RFC3339)})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = ?, delete_token = ? WHERE id = ?`, at, tok, r.ID); err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	c := mk(calY, 0, "2026-06-01T10:00:05Z", 3)
	mk(1, 1, "2026-06-01T10:00:01Z", 5)
	mk(1, 2, "2026-06-01T10:00:03Z", 0)
	if err := svc.RestoreByUID(ctx, "zz4"); err != nil {
		t.Fatal(err)
	}
	wantTrashed(t, svc, false, c)
}

// TestRestoreByID_UnownedRestoreMarksNoOtherRow: calendars 1 and B hold a live master of
// one UID, and C holds two legacy overrides x and y. No series owns them. The
// restore of x is no edit of y, so it does not mark y (edit_unowned).
func TestRestoreByID_UnownedRestoreMarksNoOtherRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB, calC := insertCalendar(t, svc, "B"), insertCalendar(t, svc, "C")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	for _, c := range []int64{1, calB} {
		if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "zz5", CalendarID: c, Title: "M", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3"}); err != nil {
			t.Fatal(err)
		}
	}
	x := legacyAt(t, svc, "zz5", calC, start.Add(24*time.Hour))
	y := legacyAt(t, svc, "zz5", calC, start.Add(48*time.Hour))
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2026-05-01T00:00:00Z' WHERE id = ?`, x.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET edit_unowned = 0`); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreByID(ctx, x.ID); err != nil {
		t.Fatalf("RestoreByID(x): %v", err)
	}
	var my int64
	if err := svc.db.QueryRowContext(ctx, `SELECT edit_unowned FROM events WHERE id = ?`, y.ID).Scan(&my); err != nil {
		t.Fatalf("read the mark of y: %v", err)
	}
	if my != 0 {
		t.Errorf("row y flagged by the restore of x")
	}
}

// TestDelete_UnownedDeleteMarksNoOtherRow: calendars 1 and B hold a live
// master of one UID, and C holds two legacy overrides x and y. No series
// owns them. The delete of x is no edit of y, so it does not mark y
// (edit_unowned, issue #862).
func TestDelete_UnownedDeleteMarksNoOtherRow(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	calB, calC := insertCalendar(t, svc, "B"), insertCalendar(t, svc, "C")
	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	for _, c := range []int64{1, calB} {
		if _, err := svc.UpsertByUID(ctx, UpsertParams{UID: "zz6", CalendarID: c, Title: "M", StartTime: start, EndTime: start.Add(time.Hour), RecurrenceRule: "FREQ=DAILY;COUNT=3"}); err != nil {
			t.Fatal(err)
		}
	}
	x := legacyAt(t, svc, "zz6", calC, start.Add(24*time.Hour))
	y := legacyAt(t, svc, "zz6", calC, start.Add(48*time.Hour))
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET edit_unowned = 0`); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, x.ID); err != nil {
		t.Fatalf("Delete(x): %v", err)
	}
	wantTrashed(t, svc, true, x.ID)
	var my int64
	if err := svc.db.QueryRowContext(ctx, `SELECT edit_unowned FROM events WHERE id = ?`, y.ID).Scan(&my); err != nil {
		t.Fatalf("read the mark of y: %v", err)
	}
	if my != 0 {
		t.Errorf("row y flagged by the delete of x")
	}
}

// TestPurgeOld_YoungLegacyRowOfAnotherCalendarRestores: a delete of orphan
// rows merges a legacy override of calendar L into the log of an old master
// of calendar 1. The age purge keeps the young row, and the log stays in
// calendar 1 as an orphan log. A restore by UID still brings back the row of
// L: the restore guards and un-hides each row in its own calendar.
func TestPurgeOld_YoungLegacyRowOfAnotherCalendarRestores(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, _, _ := singleSeriesWithOverrides(t, svc)
	calL := insertCalendar(t, svc, "L")
	at := time.Date(2026, 5, 10, 9, 0, 0, 0, time.UTC)
	legacy := legacyAt(t, svc, master.UID, calL, at)
	if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
		t.Fatal(err)
	}
	if err := reviveRow(svc, legacy.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.db.ExecContext(ctx, `UPDATE events SET deleted_at = '2020-01-01T00:00:00Z' WHERE uid = ? AND deleted_at IS NOT NULL`, master.UID); err != nil {
		t.Fatal(err)
	}
	if err := svc.DeleteSeries(ctx, calL, master.UID); err != nil {
		t.Fatalf("DeleteSeries(L): %v", err)
	}
	wantTrashed(t, svc, true, legacy.ID)
	if _, err := svc.PurgeOld(ctx, time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := svc.RestoreByUID(ctx, master.UID); err != nil {
		t.Fatalf("RestoreByUID: %v", err)
	}
	wantTrashed(t, svc, false, legacy.ID)
}
