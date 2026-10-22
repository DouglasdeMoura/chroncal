package event

import (
	"context"
	"errors"
	"strings"
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
	if err := svc.RestoreByID(ctx, override.ID); err != nil {
		t.Fatalf("RestoreByID: %v", err)
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
	if err := svc.RestoreByID(ctx, rowsB[1]); err != nil {
		t.Fatalf("RestoreByID(live override of B): %v", err)
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

// TestRestoreInstance_TrashedMasterUnhidesTheOverride: the restore of an
// occurrence delete from the trash un-hides the override row that the
// delete hid. That holds when the master is in the trash too.
func TestRestoreInstance_TrashedMasterUnhidesTheOverride(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	master, override, entry := occurrenceDeletedBeforeSeries(t, svc)
	if err := svc.RestoreTrash(ctx, entry); err != nil {
		t.Fatalf("RestoreTrash(occurrence): %v", err)
	}
	if o, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || o.DeletedAt != nil {
		t.Errorf("override after the restore = trashed %v, %v; want it live", o.DeletedAt != nil, err)
	}
	m, err := svc.q.GetEventIncludingDeleted(ctx, master.ID)
	if err != nil {
		t.Fatalf("get master: %v", err)
	}
	if m.DeletedAt == nil {
		t.Error("master after the restore of the occurrence is live, want it in the trash")
	}
	if got := storage.NullableToString(m.Exdates); strings.Contains(got, override.RecurrenceID) {
		t.Errorf("exdates of the master = %q, want the occurrence back", got)
	}
}

// TestRestoreInstance_TrashedMasterGuardsTheCalendar: the restore of an
// occurrence delete writes the master. A read-only calendar refuses it, also
// when the master is in the trash.
func TestRestoreInstance_TrashedMasterGuardsTheCalendar(t *testing.T) {
	svc := newTestService(t)
	ctx := context.Background()
	_, override, entry := occurrenceDeletedBeforeSeries(t, svc)
	if _, err := svc.db.ExecContext(ctx, `UPDATE calendars SET remote_access = 'read', remote_components = 'VEVENT' WHERE id = 1`); err != nil {
		t.Fatalf("make the calendar read-only: %v", err)
	}
	if err := svc.RestoreTrash(ctx, entry); !errors.Is(err, calendaraccess.ErrReadOnly) {
		t.Fatalf("RestoreTrash(occurrence) = %v, want ErrReadOnly", err)
	}
	if o, err := svc.GetIncludingDeleted(ctx, override.ID); err != nil || o.DeletedAt == nil {
		t.Errorf("override after the refused restore = trashed %v, %v; want it in the trash", o.DeletedAt != nil, err)
	}
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
	ids, err := parseRowIDs(log.HiddenIds)
	if err != nil || len(ids) != 2 {
		t.Fatalf("hidden rows of the log = %q, %v; want L1 and L2", log.HiddenIds, err)
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
// paths are the restore from the trash, the undo beside a live master, and
// the undo beside a master in the trash. A master of the UID in another
// calendar comes after the delete. The series of calendar 1 then no longer
// owns the legacy row, so the guard of the series does not cover its
// calendar.
func TestRestoreOccurrence_GuardsTheCalendarOfEachRow(t *testing.T) {
	for _, path := range []string{"trash", "undo live master", "undo trashed master"} {
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
			if path == "undo trashed master" {
				if err := svc.DeleteSeries(ctx, 1, master.UID); err != nil {
					t.Fatalf("DeleteSeries: %v", err)
				}
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
