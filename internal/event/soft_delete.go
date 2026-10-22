package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/softdelete"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// ErrAlreadyRestored reports an undo or a restore whose target row is live
// again: another path restored the delete first. It wraps ErrNotDeleted, so
// a caller that tests for ErrNotDeleted still matches it.
var ErrAlreadyRestored = fmt.Errorf("%w: the delete is already restored", ErrNotDeleted)

// ErrMasterPurged reports a restore of rows whose master a purge took,
// while another calendar holds a live master of the UID. The view would
// give the rows to that unrelated series, so they stay in the trash.
var ErrMasterPurged = errors.New("event: the master of this series is purged; its overrides cannot come back")

// ErrMasterInTrash reports a restore that needs the live master of its
// series, while that master is in the trash. Restore the series first.
var ErrMasterInTrash = errors.New("event: the master of the series is in the trash; restore the series first")

// ErrNotDeleted is returned by Restore when the target row is not soft-deleted.
// The row was never deleted, or it has already been restored, or it was purged
// from the database. The CLI collapses this with ErrNotFound.
var ErrNotDeleted = errors.New("event: row not soft-deleted (may have been purged)")

// ErrSeriesInTrash reports a series delete in a calendar whose master of
// the UID is in the trash, while another calendar holds a live master.
// The rows of that calendar include no live series, so the delete has no
// target. Restore the series first, or name the other calendar.
var ErrSeriesInTrash = errors.New("event: the series is in the trash in this calendar")

// UndoKind discriminates the three reversible delete shapes.
type UndoKind int

const (
	// UndoKindSingle is a standalone or single-override soft-delete. Restore
	// clears deleted_at on exactly one row.
	UndoKindSingle UndoKind = iota
	// UndoKindSeries is a full series soft-delete (DeleteSeries) in one
	// calendar. Restore un-hides the master of that calendar and the
	// override rows that the series-delete log names (see
	// restoreSeriesLogTx). A row of the UID in another calendar stays
	// as it is.
	UndoKindSeries
	// UndoKindFromInstance is a truncation (DeleteFromInstance). Restore
	// rewrites the master's RRULE back to the pre-truncation value AND
	// clears deleted_at on all overrides that were soft-deleted by the
	// truncation.
	UndoKindFromInstance
)

// UndoMeta carries the data a TUI or CLI Restore caller needs to reverse a
// previously-soft-deleted operation. It is intentionally small (no blobs, no
// transient children). The per-Kind Restore method does the hard work. That
// method finds the actual rows by UID.
type UndoMeta struct {
	Kind      UndoKind
	UID       string
	Label     string
	DeletedAt time.Time

	// PushCalendars lists every calendar that the delete wrote, when the
	// delete wrote more than one: a series delete that hid legacy overrides
	// in other calendars. Empty means CalendarID only.
	PushCalendars []int64
	// CalendarID is the calendar of the series, or of the deleted row.
	// Two calendars can hold the same UID (issue #842), so Restore finds
	// the master in this calendar. 0 names no calendar: one master row of
	// the UID is then the only choice.
	CalendarID int64

	// UndoKindSingle only. DeleteWithUndo or DeleteInstanceWithUndo sets
	// this field when the delete targets an override instance. A standalone
	// master delete leaves this field empty.
	RecurrenceID string

	// RowID is the override row that the delete hid, for an occurrence
	// delete with a stored override. The undo un-hides exactly that row.
	// 0 means no override row went to the trash with the delete.
	RowID int64

	// MasterID is the master row of the series at delete time. The undo
	// reverses the delete on that row, with no second look at the series
	// rule. 0 with RowIDs is a series delete of orphan rows.
	MasterID int64

	// ExdateLogID and ExdateDeleteSeq name the delete in the occurrence log of
	// the master (UndoKindSingle with RecurrenceID). Each delete of an
	// occurrence has its own seq in the log, so the undo reverses that delete
	// only. A log with another ID, or with no entry of the seq, means that a
	// restore came first. The undo then refuses with ErrAlreadyRestored.
	// ExdateLogID 0 means that the delete wrote no log: the master was in the
	// trash. The undo then brings back the rows alone.
	ExdateLogID     int64
	ExdateDeleteSeq int64

	// RowIDs lists the rows that a series delete of orphan rows hid
	// (UndoKindSeries with MasterID 0). The undo un-hides exactly those rows and
	// drops them from the series-delete log. The rest of the log stays for the
	// restore of the master. For an occurrence delete (UndoKindSingle), it
	// lists every override row of the occurrence that the delete hid. RowID is
	// the first of them.
	RowIDs []int64

	// UndoKindFromInstance only. CutoffTime is the truncation cutoff. It finds
	// the event_truncate_deletes log row that Undo reverses. Then the RRULE,
	// trimmed RDATEs, and the exact overrides the truncation hid are all
	// restored. MasterUpdatedBefore is the stale-master guard baseline.
	CutoffTime          time.Time
	MasterUpdatedBefore time.Time
}

// DeleteWithUndo soft-deletes an event by ID and returns the metadata needed
// to reverse it. For an override, EXDATE mutation on the master is part of
// the Delete flow. The returned UndoMeta carries the override's
// recurrence_id. Undo then un-hides exactly that instance. Without it, undo
// falls back to a UID-wide restore that would also resurrect other
// soft-deleted overrides of the same series.
func (s *Service) DeleteWithUndo(ctx context.Context, id int64) (UndoMeta, error) {
	var deleted deletedRow
	if err := s.deleteRow(ctx, id, &deleted); err != nil {
		return UndoMeta{}, err
	}
	// The meta comes from the rows that the delete transaction read. A
	// change of the row between a read and the delete cannot reach it.
	evt, master := FromStorage(deleted.Row), deleted.Master
	rowID, masterID, calendarID := int64(0), evt.ID, evt.CalendarID
	if evt.RecurrenceID != "" {
		// No master (an orphan row) leaves 0, and the undo un-hides the row
		// alone. The undo key is the calendar of the master, the same key
		// that an occurrence delete of the row records. The row stays in
		// RowID.
		rowID, masterID = evt.ID, master.ID
		if master.ID != 0 {
			calendarID = master.CalendarID
		}
	}
	return UndoMeta{
		Kind:            UndoKindSingle,
		UID:             evt.UID,
		Label:           evt.Title,
		DeletedAt:       time.Now().UTC(),
		CalendarID:      calendarID,
		RecurrenceID:    evt.RecurrenceID,
		RowID:           rowID,
		MasterID:        masterID,
		ExdateLogID:     deleted.ExdateLogID,
		ExdateDeleteSeq: deleted.ExdateDeleteSeq,
	}, nil
}

// DeleteInstanceWithUndo excludes an occurrence and returns undo metadata.
// The overridden instance (if any) is soft-deleted; on Restore we un-hide it
// and remove the EXDATE we added. calendarID names the calendar of the
// series (see findSeriesMasterTx).
func (s *Service) DeleteInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
	master, d, err := s.deleteInstance(ctx, calendarID, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}
	var rowID int64
	if len(d.Rows) > 0 {
		rowID = d.Rows[0]
	}
	return UndoMeta{
		Kind:            UndoKindSingle,
		UID:             uid,
		Label:           master.Title,
		DeletedAt:       time.Now().UTC(),
		CalendarID:      master.CalendarID,
		RecurrenceID:    instanceTime.UTC().Format(time.RFC3339),
		RowID:           rowID,
		MasterID:        master.ID,
		RowIDs:          d.Rows,
		ExdateLogID:     d.LogID,
		ExdateDeleteSeq: d.Seq,
	}, nil
}

// DeleteFromInstanceWithUndo truncates the series at instanceTime and returns
// the cutoff plus master UpdatedAt. Restore can then reverse the truncation
// exactly via the event_truncate_deletes log row. calendarID names the
// calendar of the series (see findSeriesMasterTx).
func (s *Service) DeleteFromInstanceWithUndo(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (UndoMeta, error) {
	// deleteFromInstance bumps the master's updated_at (UpdateEventRecurrenceRule
	// sets updated_at=now) and returns that post-truncation value, read back
	// inside the truncation transaction. The stale-master guard in
	// restoreFromInstance must expect this value, not the pre-truncation one.
	// Otherwise the truncation's own write looks like a concurrent external edit
	// and Undo always fails.
	//
	// Capture it in-tx rather than via a separate read after commit. Then a
	// concurrent external edit that lands between commit and read is not
	// mistaken for the baseline. Only edits that advance updated_at *past*
	// this point trip the guard.
	master, postUpdated, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}

	return UndoMeta{
		Kind:                UndoKindFromInstance,
		UID:                 uid,
		Label:               master.Title,
		DeletedAt:           time.Now().UTC(),
		CalendarID:          master.CalendarID,
		CutoffTime:          instanceTime.UTC(),
		MasterUpdatedBefore: parseStorageTime(postUpdated),
	}, nil
}

// DeleteSeriesWithUndo soft-deletes a master + all overrides and returns undo
// metadata. Restore un-hides every row of the series in its calendar.
// calendarID names the calendar of the series (see findSeriesMasterTx).
func (s *Service) DeleteSeriesWithUndo(ctx context.Context, calendarID int64, uid string) (UndoMeta, error) {
	d, err := s.deleteSeries(ctx, calendarID, uid)
	if err != nil {
		return UndoMeta{}, err
	}
	return UndoMeta{
		Kind:          UndoKindSeries,
		UID:           uid,
		Label:         d.Label,
		DeletedAt:     time.Now().UTC(),
		CalendarID:    d.CalendarID,
		MasterID:      d.Master.ID,
		RowIDs:        d.Hidden,
		PushCalendars: d.Tombstoned,
	}, nil
}

// PushCalendar returns the calendar whose push sends the delete that meta
// records: the calendar of the series master, or of the deleted row. A
// legacy override lives in another calendar than its master, and the push
// of its own calendar would send nothing. fallback is the calendar of the
// row, for a record with no calendar. The CLI and the TUI push it.
func (m UndoMeta) PushCalendar(fallback int64) int64 {
	if m.CalendarID != 0 {
		return m.CalendarID
	}
	return fallback
}

// PushCalendarIDs returns every calendar whose push sends the delete that
// meta records: PushCalendars, or else PushCalendar(fallback).
func (m UndoMeta) PushCalendarIDs(fallback int64) []int64 {
	if len(m.PushCalendars) > 0 {
		return m.PushCalendars
	}
	return []int64{m.PushCalendar(fallback)}
}

// RestoreUndo reverses a soft-delete operation recorded in UndoMeta. It
// dispatches by Kind. For FromInstance kinds, it also rewrites the master's
// RRULE back to the pre-truncation value in the same transaction.
func (s *Service) RestoreUndo(ctx context.Context, meta UndoMeta) error {
	// The guard covers the calendars of the series only. A calendar
	// without a row of the series, for example a read-only copy of the
	// UID, does not block the restore (issue #842).
	if err := ensureSeriesWritableIn(ctx, s.q, meta.CalendarID, meta.UID); err != nil {
		return err
	}
	switch meta.Kind {
	case UndoKindSingle:
		return s.restoreSingle(ctx, meta)
	case UndoKindSeries:
		return s.restoreSeries(ctx, meta)
	case UndoKindFromInstance:
		return s.restoreFromInstance(ctx, meta)
	default:
		return fmt.Errorf("unknown undo kind %d", meta.Kind)
	}
}

// RestoreByUID reverses a series delete of uid. Used by the CLI `events
// restore <uid>` path. A series delete leaves its master in the trash, or
// a series-delete log row (event_series_deletes), so those calendars are
// the candidates. A live series with rows in the trash for another reason
// is no candidate. An occurrence that the user deleted on its own, or a
// row that a truncation hid, keeps its own trash entry (issue #287).
//
// One candidate is the target. Two calendars can hold the same UID (issue
// #842), so more candidates give ErrAmbiguousUID: restore the series by
// the ID of its master instead. No candidate gives ErrNotDeleted. A UID
// with no master row and no log holds orphan rows only. The rows of the
// newest delete of one calendar come back (see restoreOrphanDeleteTx). A
// row that a delete by ID hid stays in the trash.
func (s *Service) RestoreByUID(ctx context.Context, uid string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	calendarID, err := restoreByUIDTx(ctx, qtx, uid)
	if err != nil {
		return err
	}
	if err := reconcileTx(ctx, qtx, calendarID, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreByUIDTx is RestoreByUID in one transaction: the reads of the
// candidates, the choice, the access guard, and the restore. It returns the
// calendar of the restore.
func restoreByUIDTx(ctx context.Context, qtx *storage.Queries, uid string) (int64, error) {
	masters, err := qtx.ListEventMastersByUIDIncludingDeleted(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list masters: %w", err)
	}
	allLogs, err := qtx.ListEventSeriesDeletesByUID(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list series delete logs: %w", err)
	}
	trashed, err := qtx.ListTrashedEventsByUID(ctx, uid)
	if err != nil {
		return 0, fmt.Errorf("list trashed rows: %w", err)
	}
	logs, err := liveSeriesLogs(masters, allLogs, trashed)
	if err != nil {
		return 0, err
	}
	if len(masters) == 0 && len(logs) == 0 {
		return restoreOrphanDeleteTx(ctx, qtx, uid, trashed)
	}
	calendarID, holdsMaster, err := restoreCandidate(masters, logs)
	if err != nil {
		return 0, err
	}
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return 0, err
	}
	n, ids, err := restoreSeriesLogTx(ctx, qtx, calendarID, uid, holdsMaster)
	if err != nil {
		return 0, err
	}
	if n == 0 {
		return 0, ErrNotDeleted
	}
	if err := clearOtherTombstonesTx(ctx, qtx, calendarID, uid, ids); err != nil {
		return 0, err
	}
	return calendarID, nil
}

// clearOtherTombstonesTx drops the tombstone of uid in each calendar other
// than seriesCalendarID that holds a row of ids. A series delete queues a
// tombstone in every calendar whose rows it hid. The restore of the series
// brings those rows back, so the delete of their server objects goes too.
// The caller reconciles seriesCalendarID itself (reconcileTx).
func clearOtherTombstonesTx(ctx context.Context, qtx *storage.Queries, seriesCalendarID int64, uid string, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	rows, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return err
	}
	for _, c := range rowCalendars(rows) {
		if c == seriesCalendarID {
			continue
		}
		if err := qtx.DeleteTombstonesByCalendarAndUID(ctx, storage.DeleteTombstonesByCalendarAndUIDParams{CalendarID: c, Uid: uid}); err != nil {
			return fmt.Errorf("clear tombstone after restore: %w", err)
		}
	}
	return nil
}

// restoreOrphanDeleteTx is RestoreByUID for a UID with no master row and no
// series-delete log. trashed holds the rows of the UID in the trash. A row
// that an occurrence log names had its own delete, and it stays in the trash.
// An example is a delete by ID of an orphan row. Of the other rows, one
// orphan delete hides the rows of one calendar with one deleted_at, to the
// second. The restore takes the calendar of those rows, and brings back its
// rows of the newest deleted_at. Two calendars give ErrAmbiguousUID. No row
// gives ErrNotDeleted.
func restoreOrphanDeleteTx(ctx context.Context, qtx *storage.Queries, uid string, trashed []storage.Event) (int64, error) {
	ownDelete := make(map[int64]map[string]bool)
	var candidates []storage.Event
	for _, r := range trashed {
		logged, ok := ownDelete[r.CalendarID]
		if !ok {
			logs, err := qtx.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{CalendarID: r.CalendarID, Uid: uid})
			if err != nil {
				return 0, fmt.Errorf("list exdate logs: %w", err)
			}
			logged = make(map[string]bool, len(logs))
			for _, l := range logs {
				logged[l.RecurrenceID] = true
			}
			ownDelete[r.CalendarID] = logged
		}
		if r.RecurrenceID != "" && logged[r.RecurrenceID] {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return 0, ErrNotDeleted
	}
	calendarID := candidates[0].CalendarID
	newest := ""
	for _, r := range candidates {
		if r.CalendarID != calendarID {
			return 0, ErrAmbiguousUID
		}
		if *r.DeletedAt > newest {
			newest = *r.DeletedAt
		}
	}
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return 0, err
	}
	ids := make([]int64, 0, len(candidates))
	for _, r := range candidates {
		if *r.DeletedAt == newest {
			ids = append(ids, r.ID)
		}
	}
	if _, err := qtx.RestoreEventsByIDs(ctx, ids); err != nil {
		return 0, fmt.Errorf("restore orphan rows: %w", err)
	}
	return calendarID, nil
}

// liveSeriesLogs keeps the series-delete logs that a restore can
// still reverse. A log counts when it names a row in the trash, or when its
// calendar holds a master in the trash. A log that names only purged rows,
// or no row, is not a candidate of RestoreByUID.
func liveSeriesLogs(masters []storage.Event, logs []storage.EventSeriesDelete, rows []storage.Event) ([]storage.EventSeriesDelete, error) {
	if len(logs) == 0 {
		return nil, nil
	}
	trashed := make(map[int64]bool, len(rows))
	for _, r := range rows {
		trashed[r.ID] = true
	}
	trashedMaster := make(map[int64]bool, len(masters))
	for _, m := range masters {
		if m.DeletedAt != nil {
			trashedMaster[m.CalendarID] = true
		}
	}
	kept := make([]storage.EventSeriesDelete, 0, len(logs))
	for _, l := range logs {
		named, err := parseRowIDs(l.HiddenIds)
		if err != nil {
			return nil, err
		}
		ok := trashedMaster[l.CalendarID]
		for _, id := range named {
			ok = ok || trashed[id]
		}
		if ok {
			kept = append(kept, l)
		}
	}
	return kept, nil
}

// restoreCandidate chooses the calendar whose series delete RestoreByUID
// reverses. holdsMaster reports whether that calendar holds a master row
// of the UID. Each of these calendars is a candidate, with equal rank:
//   - a calendar with a series-delete log of the UID that a restore can
//     still reverse (see liveSeriesLogs);
//   - a calendar with a master in the trash, with or without a log. An
//     example is an old copy whose log the purge dropped.
//
// One candidate is the result. Two or more give ErrAmbiguousUID: the UID
// alone does not name one series, and the ID of a master does. No candidate
// gives ErrNotDeleted.
func restoreCandidate(masters []storage.Event, logs []storage.EventSeriesDelete) (calendarID int64, holdsMaster bool, err error) {
	holds := make(map[int64]bool, len(masters))
	seen := make(map[int64]bool, len(masters)+len(logs))
	var candidates []int64
	add := func(cal int64) {
		// A calendar can hold more than one log row: one per delete.
		if !seen[cal] {
			seen[cal] = true
			candidates = append(candidates, cal)
		}
	}
	for _, l := range logs {
		add(l.CalendarID)
	}
	for _, m := range masters {
		holds[m.CalendarID] = true
		if m.DeletedAt != nil {
			add(m.CalendarID)
		}
	}
	switch len(candidates) {
	case 0:
		return 0, false, ErrNotDeleted
	case 1:
		return candidates[0], holds[candidates[0]], nil
	}
	return 0, false, ErrAmbiguousUID
}

// RestoreByID un-hides a soft-deleted row. Used by the CLI `events restore
// <id>` path. A master row reverses the series delete: it brings back the
// overrides that the series delete hid. An occurrence that the user deleted
// on its own before stays deleted, with its EXDATE (issue #287). Two
// calendars can hold the same UID (issue #842). The ID of the master is then
// the one reference that names one series in the trash. An override row comes
// back alone. Reconciles sync state so subsequent pushes re-CREATE the
// resource on the server if necessary.
func (s *Service) RestoreByID(ctx context.Context, id int64) error {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get event: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrAlreadyRestored
	}
	if r.RecurrenceID != "" {
		if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
			return err
		}
		return s.restoreOverrideByID(ctx, r)
	}
	return s.restoreMasterByID(ctx, id)
}

// restoreMasterByID is RestoreByID for a master row. The read of the row,
// the access guard, and the restore of the series run in one transaction.
// No row back gives ErrNotDeleted.
func (s *Service) restoreMasterByID(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	r, err := qtx.GetEventIncludingDeleted(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotDeleted
	}
	if err != nil {
		return fmt.Errorf("get event: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrAlreadyRestored
	}
	if err := ensureSeriesWritableIn(ctx, qtx, r.CalendarID, r.Uid); err != nil {
		return err
	}
	n, ids, err := restoreSeriesLogTx(ctx, qtx, r.CalendarID, r.Uid, true)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	if err := reconcileTx(ctx, qtx, r.CalendarID, r.Uid); err != nil {
		return err
	}
	if err := clearOtherTombstonesTx(ctx, qtx, r.CalendarID, r.Uid, ids); err != nil {
		return err
	}
	return tx.Commit()
}

// recordSeriesDeleteTx writes the series-delete log of (calendarID, uid) with
// hidden. Each delete gets its own log row. A non-nil into is a master in the
// trash. The rows then join the newest log row of that master, in the
// calendar of the master. Only a delete of orphan rows beside a master in the
// trash merges, because that master still needs its log. With no log, the
// merge writes the log of the master: the rows that went with the master
// (sameDeleteRowsTx), the orphan rows, and had_master 1. A restore of the
// master then brings back its rows too. A delete of orphan rows with no
// master writes a row of its own, so an earlier orphan delete keeps its
// record and stays restorable.
func recordSeriesDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, hidden []int64, into *storage.Event, hadMaster bool) error {
	if into != nil {
		calendarID = into.CalendarID
		log, err := qtx.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{
			CalendarID: calendarID, Uid: uid,
		})
		found, err := storage.Found(err)
		if err != nil {
			return fmt.Errorf("get series delete log: %w", err)
		}
		if found {
			// The orphan rows join the log of the master in the trash, and
			// the log keeps its had_master.
			if hidden, err = unionCommaList(hidden, log.HiddenIds, parseRowID); err != nil {
				return err
			}
			if err := qtx.UpdateEventSeriesDeleteHiddenIds(ctx, storage.UpdateEventSeriesDeleteHiddenIdsParams{HiddenIds: formatRowIDs(hidden), ID: log.ID}); err != nil {
				return fmt.Errorf("merge series delete: %w", err)
			}
			return nil
		}
		// No log: a delete from before migration 057, or the series
		// delete of a pull. The rows that went with the master join too.
		same, err := sameDeleteRowsTx(ctx, qtx, *into)
		if err != nil {
			return err
		}
		hidden = append(same, hidden...)
		hadMaster = true
	}
	flag := int64(0)
	if hadMaster {
		flag = 1
	}
	if err := qtx.RecordEventSeriesDelete(ctx, storage.RecordEventSeriesDeleteParams{
		CalendarID: calendarID, Uid: uid, HiddenIds: formatRowIDs(hidden), HadMaster: flag,
	}); err != nil {
		return fmt.Errorf("record series delete: %w", err)
	}
	return nil
}

// restoreSeriesLog runs restoreSeriesLogTx in one transaction.
func (s *Service) restoreSeriesLog(ctx context.Context, calendarID int64, uid string, requireMaster bool) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	n, ids, err := restoreSeriesLogTx(ctx, qtx, calendarID, uid, requireMaster)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := reconcileTx(ctx, qtx, calendarID, uid); err != nil {
			return 0, err
		}
		if err := clearOtherTombstonesTx(ctx, qtx, calendarID, uid, ids); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// restoreSeriesLogTx reverses a series delete in one calendar. It
// un-hides the master and the override rows that the series delete hid.
// The series-delete log row (event_series_deletes) lists those rows by
// ID. The restore touches exactly that set, also when the owner of a
// legacy override changed after the delete. The calendar of each hidden
// row takes the write. The restore consumes the log row.
//
// A master with no log row comes back with the override rows that went to
// the trash in the same statement (sameDeleteRowsTx). Three deletes leave
// no log row:
//
//   - a plain delete of a master,
//   - a delete that a sync pull made,
//   - a series delete from before the log (migration 057).
//
// An override that a sync prune, an occurrence delete, or a truncation
// hid stays in the trash with its EXDATE and its log row. The trash view
// still lists it on its own.
//
// requireMaster (the reverse of a series delete with a master) also
// un-hides the master of calendarID. A calendar with no master row is
// then an error. Without it (the undo of a delete of orphan rows), it never
// touches a master: that delete hid none. The count is the number of rows
// that came back, and ids the override rows of the log.
func restoreSeriesLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, requireMaster bool) (int64, []int64, error) {
	var master storage.Event
	if requireMaster {
		var err error
		master, err = qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("get master: %w", err)
		}
	}
	// The log comes first: the revival of the master fires the trigger
	// that drops the log of its calendar (migration 057). The log row
	// goes before the restore, so the drop of the restored rows from the
	// other logs does not rewrite it either.
	log, err := qtx.GetEventSeriesDeleteByCalendarUID(ctx, storage.GetEventSeriesDeleteByCalendarUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	logged, err := storage.Found(err)
	if err != nil {
		return 0, nil, fmt.Errorf("get series delete log: %w", err)
	}
	var ids []int64
	switch {
	case logged:
		ids, err = parseRowIDs(log.HiddenIds)
		if err != nil {
			return 0, nil, err
		}
		if err := qtx.DeleteEventSeriesDeleteByID(ctx, log.ID); err != nil {
			return 0, nil, fmt.Errorf("delete series delete log: %w", err)
		}
	case requireMaster && master.DeletedAt != nil:
		ids, err = sameDeleteRowsTx(ctx, qtx, master)
		if err != nil {
			return 0, nil, err
		}
	}
	if !requireMaster && len(ids) > 0 && logged && log.HadMaster == 1 {
		// The rows of a series delete with a master come back into a
		// calendar whose master row is gone. Another calendar with a live
		// master would take them over as legacy overrides. The log of a
		// delete of orphan rows (had_master 0) restores as it is.
		holds, err := calendarHoldsMasterTx(ctx, qtx, calendarID, uid)
		if err != nil {
			return 0, nil, err
		}
		if !holds {
			live, err := qtx.EventUIDMasterCalendars(ctx, uid)
			if err != nil {
				return 0, nil, fmt.Errorf("find master calendars: %w", err)
			}
			if live.Calendars > 0 {
				return 0, nil, ErrMasterPurged
			}
		}
	}
	var n int64
	if requireMaster && master.DeletedAt != nil {
		k, err := qtx.RestoreEventsByIDs(ctx, []int64{master.ID})
		if err != nil {
			return 0, nil, fmt.Errorf("restore master: %w", err)
		}
		n += k
	}
	k, err := restoreLoggedIDsTx(ctx, qtx, calendarID, ids)
	if err != nil {
		return 0, nil, err
	}
	return n + k, ids, nil
}

// ListDeleted returns soft-deleted events for a calendar, newest first.
func (s *Service) ListDeleted(ctx context.Context, calendarID int64) ([]Event, error) {
	rows, err := s.q.ListDeletedEventsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	return fromStorageSlice(rows), nil
}

// GetIncludingDeleted returns a row by ID even if it has been soft-deleted.
// Used by the trash view's detail popup where the user wants to inspect
// what was deleted before restore.
func (s *Service) GetIncludingDeleted(ctx context.Context, id int64) (Event, error) {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		return Event{}, err
	}
	return FromStorage(r), nil
}

// PurgeDeleted hard-deletes rows soft-deleted before olderThan. Returns the
// number of rows purged. Children cascade via the FK ON DELETE CASCADE.
func (s *Service) PurgeDeleted(ctx context.Context, olderThan time.Time) (int, error) {
	r, err := s.PurgeDeletedReport(ctx, olderThan)
	return r.Purged, err
}

// SkippedMaster names a master that the purge by age kept, because its
// series still owns a live override.
type SkippedMaster struct {
	ID         int64
	CalendarID int64
	UID        string
}

// PurgeReport is the result of PurgeDeletedReport.
type PurgeReport struct {
	// Purged is the count of hard-deleted rows.
	Purged int
	// Skipped lists the masters that the purge kept.
	Skipped []SkippedMaster
}

// PurgeDeletedReport is PurgeDeleted with the masters that it skipped. It
// reads the masters in the trash before the cutoff once, and then works by
// ID:
//  1. A master whose series still owns a live override stays, and the
//     report names it. The rows and the logs of its series stay too.
//  2. Each other master takes along the rows of its series (see
//     seriesPurgeSetTx) and its logs, whatever their deleted_at.
//  3. The other rows in the trash before the cutoff go.
func (s *Service) PurgeDeletedReport(ctx context.Context, olderThan time.Time) (PurgeReport, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PurgeReport{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	report, err := purgeDeletedTx(ctx, s.q.WithTx(tx), cutoff)
	if err != nil {
		return PurgeReport{}, err
	}
	if err := tx.Commit(); err != nil {
		return PurgeReport{}, err
	}
	return report, nil
}

// PurgeOldReport is the result of PurgeOld.
type PurgeOldReport struct {
	PurgeReport
	// InstanceLogs is the count of purged event_exdate_deletes rows.
	InstanceLogs int
	// TruncateLogs is the count of purged event_truncate_deletes rows.
	TruncateLogs int
	// SeriesLogs is the count of purged event_series_deletes rows.
	SeriesLogs int
}

// PurgeOld is the retention purge of the events. One transaction holds
// the purge of PurgeDeletedReport and the purge of the delete logs before
// the cutoff. The logs of the series of a kept master stay. Thus a crash
// cannot keep a master and lose its logs.
func (s *Service) PurgeOld(ctx context.Context, olderThan time.Time) (PurgeOldReport, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return PurgeOldReport{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	var report PurgeOldReport
	if report.PurgeReport, err = purgeDeletedTx(ctx, qtx, cutoff); err != nil {
		return PurgeOldReport{}, err
	}
	keep := report.Skipped
	if report.InstanceLogs, err = purgeOldLogsTx(ctx, cutoff, keep, qtx.ListOldEventExdateDeletes, qtx.DeleteEventExdateDeletesByIDs); err != nil {
		return PurgeOldReport{}, fmt.Errorf("purge event instance logs: %w", err)
	}
	if report.TruncateLogs, err = purgeOldLogsTx(ctx, cutoff, keep, qtx.ListOldEventTruncateDeletes, qtx.DeleteEventTruncateDeletesByIDs); err != nil {
		return PurgeOldReport{}, fmt.Errorf("purge event truncation logs: %w", err)
	}
	if report.SeriesLogs, err = purgeOldLogsTx(ctx, cutoff, keep, qtx.ListOldEventSeriesDeletes, qtx.DeleteEventSeriesDeletesByIDs); err != nil {
		return PurgeOldReport{}, fmt.Errorf("purge event series logs: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return PurgeOldReport{}, err
	}
	return report, nil
}

// purgeDeletedTx is the body of PurgeDeletedReport inside the transaction
// of qtx.
func purgeDeletedTx(ctx context.Context, qtx *storage.Queries, cutoff string) (PurgeReport, error) {
	masters, err := qtx.ListTrashedMastersForPurge(ctx, &cutoff)
	if err != nil {
		return PurgeReport{}, fmt.Errorf("list trashed masters: %w", err)
	}
	var report PurgeReport
	var purge, blocked []int64
	for _, m := range masters {
		if m.Blocked {
			blocked = append(blocked, m.ID)
			report.Skipped = append(report.Skipped, SkippedMaster{ID: m.ID, CalendarID: m.CalendarID, UID: m.Uid})
			continue
		}
		purge = append(purge, m.ID)
	}
	ids, err := seriesPurgeSetTx(ctx, qtx, purge)
	if err != nil {
		return PurgeReport{}, err
	}
	// The rows of a kept series stay, whatever their age.
	keep := make(map[int64]bool, len(blocked))
	for _, id := range blocked {
		keep[id] = true
	}
	if len(blocked) > 0 {
		// The kept series keeps the rows of the view and the rows of its log.
		kept, err := seriesPurgeSetTx(ctx, qtx, blocked)
		if err != nil {
			return PurgeReport{}, err
		}
		for _, id := range kept {
			keep[id] = true
		}
	}
	aged, err := qtx.ListTrashedEventIDsBefore(ctx, &cutoff)
	if err != nil {
		return PurgeReport{}, fmt.Errorf("list aged rows: %w", err)
	}
	// A row in the log of a purged master can belong to a kept series too.
	// The kept series wins: the row stays.
	ids = slices.DeleteFunc(append(ids, aged...), func(id int64) bool { return keep[id] })
	if err := dropMasterLogsTx(ctx, qtx, purge); err != nil {
		return PurgeReport{}, err
	}
	n, err := purgeIDsTx(ctx, qtx, append(ids, purge...))
	if err != nil {
		return PurgeReport{}, err
	}
	report.Purged = int(n)
	return report, nil
}

// seriesPurgeSetTx lists the override rows in the trash that the purge of
// the masters of masterIDs takes along:
//   - the rows that each series owns in the view, in its calendar and in
//     other calendars;
//   - the rows in the trash that the series-delete log of each master names.
//     Another series can own such a row in the view now. A log names a row
//     only while the row stays in the trash from that delete. The revive
//     trigger of migration 057 removes a row that comes back by any path. A
//     later delete of the row is then not in the log.
//
// PurgeByID and PurgeDeletedReport share it. It reads the view once.
func seriesPurgeSetTx(ctx context.Context, qtx *storage.Queries, masterIDs []int64) ([]int64, error) {
	if len(masterIDs) == 0 {
		return nil, nil
	}
	owned, err := qtx.ListTrashedRowsOwnedByMasters(ctx, masterIDs)
	if err != nil {
		return nil, fmt.Errorf("list owned rows: %w", err)
	}
	ids := make([]int64, 0, len(owned))
	seen := make(map[int64]bool, len(owned))
	for _, r := range owned {
		if !seen[r.ID] {
			seen[r.ID] = true
			ids = append(ids, r.ID)
		}
	}
	logs, err := qtx.ListSeriesLogsOfMasters(ctx, masterIDs)
	if err != nil {
		return nil, fmt.Errorf("list series logs: %w", err)
	}
	var named []int64
	for _, l := range logs {
		rows, err := parseRowIDs(l.HiddenIds)
		if err != nil {
			return nil, err
		}
		for _, id := range rows {
			if !seen[id] {
				seen[id] = true
				named = append(named, id)
			}
		}
	}
	for _, chunk := range chunkIDs(named) {
		trashed, err := qtx.ListTrashedEventsByIDs(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("list logged rows: %w", err)
		}
		for _, r := range trashed {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// dropMasterLogsTx drops the occurrence, truncation, and series-delete logs
// of the series of the masters of masterIDs.
func dropMasterLogsTx(ctx context.Context, qtx *storage.Queries, masterIDs []int64) error {
	if len(masterIDs) == 0 {
		return nil
	}
	for _, drop := range []func(context.Context, []int64) error{
		qtx.DeleteExdateLogsOfMasters, qtx.DeleteTruncateLogsOfMasters, qtx.DeleteSeriesLogsOfMasters,
	} {
		if err := drop(ctx, masterIDs); err != nil {
			return fmt.Errorf("drop master logs: %w", err)
		}
	}
	return nil
}

// purgeIDsTx hard-deletes the rows in the trash of ids, in chunks under the
// parameter limit of SQLite. It returns the count of purged rows.
func purgeIDsTx(ctx context.Context, qtx *storage.Queries, ids []int64) (int64, error) {
	seen := make(map[int64]bool, len(ids))
	unique := make([]int64, 0, len(ids))
	for _, id := range ids {
		if !seen[id] {
			seen[id] = true
			unique = append(unique, id)
		}
	}
	var total int64
	for _, chunk := range chunkIDs(unique) {
		n, err := qtx.PurgeEventsByIDs(ctx, chunk)
		if err != nil {
			return 0, fmt.Errorf("purge rows: %w", err)
		}
		total += n
	}
	return total, nil
}

// idChunk is the size of one IN list of row IDs, under the parameter limit
// of SQLite.
const idChunk = 500

// chunkIDs splits ids into slices of at most idChunk IDs.
func chunkIDs(ids []int64) [][]int64 {
	size := idChunk
	var out [][]int64
	for len(ids) > size {
		out = append(out, ids[:size])
		ids = ids[size:]
	}
	if len(ids) > 0 {
		out = append(out, ids)
	}
	return out
}

// ErrSeriesHasLiveOverrides reports the purge of a master whose series
// still owns a live override. After the purge, the view would hand the live
// rows to another series with the same UID. A *SeriesHasLiveOverridesError
// names the rows.
var ErrSeriesHasLiveOverrides = errors.New("event: the series still has live overrides; delete them first")

// SeriesHasLiveOverridesError is ErrSeriesHasLiveOverrides with the IDs of
// the live override rows.
type SeriesHasLiveOverridesError struct {
	IDs []int64
}

func (e *SeriesHasLiveOverridesError) Error() string {
	return fmt.Sprintf("event: the series still has live overrides (rows %s); delete them first", strings.ReplaceAll(formatRowIDs(e.IDs), ",", ", "))
}

func (e *SeriesHasLiveOverridesError) Unwrap() error { return ErrSeriesHasLiveOverrides }

// purgeMasterRowsTx removes what the purge of master takes along, before
// the delete of the master row. A series that still owns a live override
// refuses with a *SeriesHasLiveOverridesError. Else the rows of
// seriesPurgeSetTx and the logs of the series go, on the shared path of
// PurgeDeletedReport. It returns the count of purged rows.
func purgeMasterRowsTx(ctx context.Context, qtx *storage.Queries, master storage.Event) (int64, error) {
	live, err := qtx.ListLiveSeriesOverrideIDs(ctx, storage.ListLiveSeriesOverrideIDsParams{Uid: master.Uid, CalendarID: master.CalendarID})
	if err != nil {
		return 0, fmt.Errorf("list live overrides: %w", err)
	}
	if len(live) > 0 {
		return 0, &SeriesHasLiveOverridesError{IDs: live}
	}
	ids, err := seriesPurgeSetTx(ctx, qtx, []int64{master.ID})
	if err != nil {
		return 0, err
	}
	if err := dropMasterLogsTx(ctx, qtx, []int64{master.ID}); err != nil {
		return 0, err
	}
	return purgeIDsTx(ctx, qtx, ids)
}

// PurgeByID hard-deletes a single soft-deleted row. Returns ErrNotDeleted if
// the row is live (or absent). Callers then cannot purge a live event with
// the wrong ID.
func (s *Service) PurgeByID(ctx context.Context, id int64) error {
	r, err := s.q.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get event: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrNotDeleted
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// The rows of a master go before the master: the view names them only
	// while the master row exists.
	if r.RecurrenceID == "" {
		if _, err := purgeMasterRowsTx(ctx, qtx, r); err != nil {
			return err
		}
	}
	n, err := qtx.PurgeEventByID(ctx, id)
	if err != nil {
		return err
	}
	if n == 0 {
		return ErrNotDeleted
	}
	if r.RecurrenceID != "" {
		holds, err := calendarHoldsMasterTx(ctx, qtx, r.CalendarID, r.Uid)
		if err != nil {
			return err
		}
		if err := dropOrphanDeleteLogTx(ctx, qtx, r.CalendarID, r.Uid, r.RecurrenceID, holds); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// restoreSingle reverses one single-row delete from its record: the
// master that meta.MasterID names, and the override row that meta.RowID
// names. Undo data with no master and no row names no delete, so it gives
// ErrNotDeleted.
func (s *Service) restoreSingle(ctx context.Context, meta UndoMeta) error {
	calendarID, uid, recurrenceID, rowID := meta.CalendarID, meta.UID, meta.RecurrenceID, meta.RowID
	// An occurrence delete can hide more than one row: the row beside the
	// master and the legacy overrides of the occurrence. Older undo data
	// names one row in RowID.
	rowIDs := meta.RowIDs
	if len(rowIDs) == 0 && rowID != 0 {
		rowIDs = []int64{rowID}
	}
	r, err := s.undoMaster(ctx, meta)
	if errors.Is(err, ErrNotDeleted) {
		// No master: an orphan row, or a master that a purge took. The
		// delete of an orphan override has its own record, the row. A
		// purged master leaves a dead series: its row has nothing to come
		// back to, so the undo has nothing to restore.
		if rowID == 0 {
			return err
		}
		if meta.MasterID != 0 {
			return fmt.Errorf("%w: the master of the series is purged", ErrNotDeleted)
		}
		return s.restoreOverrideRow(ctx, calendarID, uid, recurrenceID, rowID)
	}
	if err != nil {
		return err
	}
	// The restore of a legacy override writes the EXDATE of its master,
	// so the calendar of the master takes the write too.
	if r.CalendarID != calendarID {
		if err := calendaraccess.EnsureWritable(ctx, s.q, r.CalendarID, "VEVENT"); err != nil {
			return err
		}
	}
	if r.DeletedAt != nil && recurrenceID != "" {
		// The master is in the trash. A delete by ID of a row beside the
		// trashed master wrote no log (ExdateLogID 0). The row was live a
		// moment before, so the undo brings back exactly that row.
		if meta.ExdateLogID == 0 {
			return s.restoreRowsBesideTrashedMaster(ctx, r, rowIDs)
		}
		// An occurrence delete of the live series came before the series
		// delete. Live rows beside a trashed master would block its purge.
		// The series restore brings back the series first.
		return ErrMasterInTrash
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		if recurrenceID != "" {
			return s.restoreEXDATEOnly(ctx, r.ID, uid, recurrenceID, rowIDs, meta.ExdateLogID, meta.ExdateDeleteSeq)
		}
		// The undo reverses a delete of the master row, and the master
		// is live: a restore from the trash came first. The undo has
		// nothing to do. It must not un-hide the other rows of the
		// series: an occurrence deleted on its own stays deleted
		// (issue #287).
		return ErrAlreadyRestored
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.RestoreEvent(ctx, r.ID); err != nil {
		return err
	}
	if err := reconcileTx(ctx, qtx, r.CalendarID, r.Uid); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreLoggedIDsTx un-hides the rows that a delete log names: the
// series-delete log and the truncation log use it. It guards the
// calendar of each row, un-hides the rows in one statement, and drops
// them from every other log of the UID. It returns the number of rows
// that came back.
func restoreLoggedIDsTx(ctx context.Context, qtx *storage.Queries, calendarID int64, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if err := ensureRowsWritableTx(ctx, qtx, calendarID, ids); err != nil {
		return 0, err
	}
	// The revive trigger of migration 057 removes the rows from every other
	// log of the UID.
	n, err := qtx.RestoreEventsByIDs(ctx, ids)
	if err != nil {
		return 0, fmt.Errorf("restore logged rows: %w", err)
	}
	return n, nil
}

// parseCommaList parses a comma-separated list with parse. An empty list
// and an empty field give no item. The delete logs store row IDs and
// recurrence IDs in this form.
func parseCommaList[T any](list string, parse func(string) (T, error)) ([]T, error) {
	if list == "" {
		return nil, nil
	}
	fields := strings.Split(list, ",")
	out := make([]T, 0, len(fields))
	for _, field := range fields {
		if field == "" {
			continue
		}
		v, err := parse(field)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

// formatCommaList is the inverse of parseCommaList.
func formatCommaList[T any](items []T, format func(T) string) string {
	fields := make([]string, len(items))
	for i, v := range items {
		fields[i] = format(v)
	}
	return strings.Join(fields, ",")
}

// unionCommaList adds the items of the list earlier to items, with no
// duplicate. A delete log that a second delete extends uses it.
func unionCommaList[T comparable](items []T, earlier string, parse func(string) (T, error)) ([]T, error) {
	old, err := parseCommaList(earlier, parse)
	if err != nil {
		return nil, err
	}
	seen := make(map[T]bool, len(items)+len(old))
	for _, v := range items {
		seen[v] = true
	}
	for _, v := range old {
		if !seen[v] {
			seen[v] = true
			items = append(items, v)
		}
	}
	return items, nil
}

// parseRowID parses one field of a list of row IDs.
func parseRowID(field string) (int64, error) {
	id, err := strconv.ParseInt(field, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse hidden row id %q: %w", field, err)
	}
	return id, nil
}

// parseRecurrenceID parses one field of a list of recurrence IDs.
func parseRecurrenceID(field string) (string, error) { return field, nil }

// parseRowIDs parses a comma-separated list of row IDs.
func parseRowIDs(list string) ([]int64, error) { return parseCommaList(list, parseRowID) }

// formatRowIDs is the inverse of parseRowIDs.
func formatRowIDs(ids []int64) string {
	return formatCommaList(ids, func(id int64) string { return strconv.FormatInt(id, 10) })
}

// ensureRowsWritableTx enforces the remote access policy on the calendar of
// each row of ids. It skips calendarID, the calendar of the series, which the
// caller guards. A legacy override lives in another calendar than its master,
// and its restore writes that calendar.
func ensureRowsWritableTx(ctx context.Context, qtx *storage.Queries, calendarID int64, ids []int64) error {
	// One query reads the calendars of the rows. A purged row is not in
	// the result, so it needs no guard.
	rows, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return err
	}
	return guardRowCalendarsTx(ctx, qtx, calendarID, rows)
}

// restoreOverrideRow un-hides the override row of an undo with no master,
// by its ID. It drops the row from the delete logs of the UID. No row, or
// a live row, gives ErrNotDeleted.
func (s *Service) restoreOverrideRow(ctx context.Context, calendarID int64, uid, recurrenceID string, rowID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := restoreRowByIDTx(ctx, qtx, rowID); err != nil {
		return err
	}
	holds, err := calendarHoldsMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return err
	}
	if err := dropOrphanDeleteLogTx(ctx, qtx, calendarID, uid, recurrenceID, holds); err != nil {
		return err
	}
	if err := reconcileTx(ctx, qtx, calendarID, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreRowByIDTx un-hides one row by ID. The revive trigger drops it from
// the delete logs of its UID. No row, or a live row, gives ErrNotDeleted.
func restoreRowByIDTx(ctx context.Context, qtx *storage.Queries, rowID int64) error {
	n, err := qtx.RestoreEventsByIDs(ctx, []int64{rowID})
	if err != nil {
		return fmt.Errorf("restore row %d: %w", rowID, err)
	}
	if n == 0 {
		// A live row is a restore that came first. An absent row is a
		// purge: nothing to restore.
		if _, err := qtx.GetEvent(ctx, rowID); err == nil {
			return ErrAlreadyRestored
		}
		return ErrNotDeleted
	}
	// The revive trigger of migration 057 removes the row from the delete
	// logs of its UID.
	return nil
}

// undoMaster reads the master row that meta.MasterID names, live or in the
// trash. No master in the undo data, or a purged master, gives
// ErrNotDeleted. The series rule never picks another master.
func (s *Service) undoMaster(ctx context.Context, meta UndoMeta) (storage.Event, error) {
	if meta.MasterID == 0 {
		return storage.Event{}, ErrNotDeleted
	}
	r, err := s.q.GetEventIncludingDeleted(ctx, meta.MasterID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Event{}, ErrNotDeleted
	}
	return r, err
}

// restoreSeries reverses a series delete from its own record. With MasterID,
// it un-hides that master and the rows of the series-delete log of its
// calendar. With RowIDs (a delete of orphan rows), it un-hides exactly those
// rows and drops them from the log. The rest of the log stays for the restore
// of the master. A purged master, a master that a restore from the trash
// brought back first, or rows that are live again give ErrNotDeleted. Undo
// data with neither field names no delete. An occurrence that the user
// deleted on its own before the series delete stays deleted (issue #287, see
// restoreSeriesLogTx).
func (s *Service) restoreSeries(ctx context.Context, meta UndoMeta) error {
	uid := meta.UID
	switch {
	case meta.MasterID != 0:
		master, err := s.q.GetEventIncludingDeleted(ctx, meta.MasterID)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			// The master row is gone. A purge drops the log with the
			// master, so only an older purge leaves a log that names the
			// hidden rows (an orphan tail). The undo then reverses that
			// record, with the refusal of restoreSeriesLogTx. The series
			// rule stays out.
			n, err := s.restoreSeriesLog(ctx, meta.CalendarID, uid, false)
			if err != nil {
				return err
			}
			if n == 0 {
				return ErrNotDeleted
			}
			return nil
		case err != nil:
			return fmt.Errorf("get master: %w", err)
		case master.DeletedAt == nil:
			return ErrAlreadyRestored
		}
		_, err = s.restoreSeriesLog(ctx, master.CalendarID, uid, true)
		return err
	case len(meta.RowIDs) != 0:
		n, err := s.restoreRows(ctx, meta.CalendarID, uid, meta.RowIDs)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrNotDeleted
		}
		return nil
	default:
		return fmt.Errorf("%w: the undo data names no master and no rows", ErrNotDeleted)
	}
}

// restoreRows un-hides the rows of ids, which belong to uid in the series
// of calendarID, in one guarded statement (restoreLoggedIDsTx). It drops
// them from the delete logs of the UID and returns the number of rows
// that came back.
func (s *Service) restoreRows(ctx context.Context, calendarID int64, uid string, ids []int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	n, err := restoreLoggedIDsTx(ctx, qtx, calendarID, ids)
	if err != nil {
		return 0, err
	}
	if n > 0 {
		if err := reconcileTx(ctx, qtx, calendarID, uid); err != nil {
			return 0, err
		}
		if err := clearOtherTombstonesTx(ctx, qtx, calendarID, uid, ids); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return n, nil
}

// restoreOverrideByID is RestoreByID for an override row. The restore takes
// the row from the occurrence log of the master, and the log settles
// (softdelete.SettleOccurrence). The row is live again, so the log gives up
// its EXDATE. The other rows of the log stay hidden.
//
// A master of the series in the trash has two cases. A delete of the row
// while the master was in the trash left no log that names the row. The
// row was live beside the trashed master, so it comes back alone, with no
// sync state. Else ErrMasterInTrash: live rows beside a trashed master would
// block its purge, and the series restore brings back the row.
func (s *Service) restoreOverrideByID(ctx context.Context, r storage.Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// The master of the override is in the calendar of the row. Else the row is
	// a legacy override of the one master of the UID. No master leaves no EXDATE
	// to clear. A legacy override of a UID with more than one master belongs to
	// no series. Delete recorded no EXDATE for it, so there is nothing to clear
	// either.
	master, mErr := findSeriesMasterIncludingDeletedTx(ctx, qtx, r.CalendarID, r.Uid)
	if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) && !errors.Is(mErr, ErrAmbiguousUID) {
		return fmt.Errorf("get master: %w", mErr)
	}
	hasMaster := mErr == nil
	if hasMaster && master.DeletedAt != nil {
		named, err := logsNameRowTx(ctx, qtx, master, r)
		if err != nil {
			return err
		}
		if named {
			return ErrMasterInTrash
		}
		if err := restoreRowByIDTx(ctx, qtx, r.ID); err != nil {
			return err
		}
		if err := dropOrphanDeleteLogTx(ctx, qtx, r.CalendarID, r.Uid, r.RecurrenceID, master.CalendarID == r.CalendarID); err != nil {
			return err
		}
		// The row stays local until the master comes back: no sync state.
		return tx.Commit()
	}
	if hasMaster && master.CalendarID != r.CalendarID {
		// The EXDATE lives on the master, so its calendar takes the
		// write too. A legacy override lives in another calendar.
		if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
			return err
		}
	}
	if err := restoreRowByIDTx(ctx, qtx, r.ID); err != nil {
		return err
	}
	// With no master, the row is an orphan instance of its own calendar.
	seriesCalendarID := r.CalendarID
	// The orphan log of the row lives in the calendar of the row. It goes
	// unless that calendar holds the master of the row.
	if err := dropOrphanDeleteLogTx(ctx, qtx, r.CalendarID, r.Uid, r.RecurrenceID, hasMaster && master.CalendarID == r.CalendarID); err != nil {
		return err
	}
	if hasMaster {
		if err := settleOccurrenceTx(ctx, qtx, master, r.RecurrenceID, withoutRows(r.ID)); err != nil {
			return err
		}
		seriesCalendarID = master.CalendarID
	}
	// A legacy override in another calendar leaves the tombstone of that
	// calendar: the old server object there still needs its delete.
	if err := reconcileTx(ctx, qtx, seriesCalendarID, r.Uid); err != nil {
		return err
	}
	return tx.Commit()
}

// logsNameRowTx reports whether a delete log of the series of master names
// the row r. The logs are the occurrence log of r and the series-delete logs
// of the master calendar.
func logsNameRowTx(ctx context.Context, qtx *storage.Queries, master, r storage.Event) (bool, error) {
	log, found, err := occurrenceLogTx(ctx, qtx, master.CalendarID, master.Uid, r.RecurrenceID)
	if err != nil {
		return false, err
	}
	if found {
		for _, e := range log.Entries {
			if slices.Contains(e.Rows, r.ID) {
				return true, nil
			}
		}
	}
	logs, err := qtx.ListEventSeriesDeletesByUID(ctx, master.Uid)
	if err != nil {
		return false, fmt.Errorf("list series delete logs: %w", err)
	}
	for _, l := range logs {
		if l.CalendarID != master.CalendarID {
			continue
		}
		ids, err := parseRowIDs(l.HiddenIds)
		if err != nil {
			return false, err
		}
		if slices.Contains(ids, r.ID) {
			return true, nil
		}
	}
	return false, nil
}

// restoreRowsBesideTrashedMaster is the undo of a delete by ID of a row that
// was live beside the trashed master. It brings back exactly the rows of
// rowIDs. It writes no EXDATE and no log, and no sync state: the rows stay
// local until the master comes back. No row back gives the error of the
// rows.
func (s *Service) restoreRowsBesideTrashedMaster(ctx context.Context, master storage.Event, rowIDs []int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	_, notBack, err := restoreHiddenRowsTx(ctx, s.q.WithTx(tx), master.CalendarID, rowIDs)
	if err != nil {
		return err
	}
	if notBack != nil {
		return notBack
	}
	return tx.Commit()
}

// restoreEXDATEOnly reverses an occurrence delete of the live series of
// masterID for the undo. logID and seq name the delete in the occurrence log
// (UndoMeta.ExdateLogID and UndoMeta.ExdateDeleteSeq). The steps:
//  1. With logID 0, the delete wrote no log: the master was in the trash
//     then, and a restore brought it back. The undo brings back the rows of
//     rowIDs, and the log of the occurrence settles.
//  2. Else the log of the occurrence must hold the entry of the delete.
//     Another log, no entry, or an entry that lost every row of the delete
//     gives ErrAlreadyRestored. A restore came first, and a later delete can
//     own the occurrence now.
//  3. The undo brings back the rows that the entry still names. With rows
//     and no row back, it gives the error of the rows and writes nothing.
//  4. The entry goes, and the log settles (softdelete.SettleOccurrence). A
//     live override at the occurrence takes the EXDATE away. The entries of
//     other deletes keep their rows hidden.
func (s *Service) restoreEXDATEOnly(ctx context.Context, masterID int64, uid, recurrenceID string, rowIDs []int64, logID, seq int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	master, err := qtx.GetEventIncludingDeleted(ctx, masterID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotDeleted
	}
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}
	if logID == 0 {
		restored, notBack, err := restoreHiddenRowsTx(ctx, qtx, master.CalendarID, rowIDs)
		if err != nil {
			return err
		}
		if notBack != nil {
			return notBack
		}
		if err := settleOccurrenceTx(ctx, qtx, master, recurrenceID, withoutRows(restored...)); err != nil {
			return err
		}
	} else {
		log, found, err := occurrenceLogTx(ctx, qtx, master.CalendarID, uid, recurrenceID)
		if err != nil {
			return err
		}
		if !found || log.ID != logID {
			return ErrAlreadyRestored
		}
		entry, ok := log.Entry(seq)
		// The entry of a delete that hid rows names none of them now. The revive
		// trigger took them out: the rows came back by another path.
		if !ok || (len(entry.Rows) == 0 && len(rowIDs) > 0) {
			return ErrAlreadyRestored
		}
		restored, notBack, err := restoreHiddenRowsTx(ctx, qtx, master.CalendarID, entry.Rows)
		if err != nil {
			return err
		}
		if notBack != nil {
			return notBack
		}
		if err := settleOccurrenceTx(ctx, qtx, master, recurrenceID, func(l softdelete.OccurrenceLog) softdelete.OccurrenceLog {
			return l.WithoutEntry(seq).WithoutRows(restored)
		}); err != nil {
			return err
		}
	}
	if err := reconcileTx(ctx, qtx, master.CalendarID, uid); err != nil {
		return err
	}
	return tx.Commit()
}

// sameDeleteRowsTx lists the override rows of the series of master that
// went to the trash in the same statement as master. Two deletes leave no
// log: a series delete from before the series-delete log (migration 057),
// and the series delete of a pull. Each hides the master and its
// overrides in one statement, so the rows share deleted_at to the second. A row that an
// occurrence log or a truncation log names went in a delete of its own,
// and it stays in the trash.
func sameDeleteRowsTx(ctx context.Context, qtx *storage.Queries, master storage.Event) ([]int64, error) {
	trashed, err := qtx.ListTrashedSeriesRowsInCalendar(ctx, storage.ListTrashedSeriesRowsInCalendarParams{
		CalendarID: master.CalendarID, Uid: master.Uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list trashed series rows: %w", err)
	}
	truncated, err := truncationHidden(ctx, qtx, master.CalendarID, master.Uid)
	if err != nil {
		return nil, err
	}
	exdateLogs, err := qtx.ListEventExdateDeletesByCalendarUID(ctx, storage.ListEventExdateDeletesByCalendarUIDParams{
		CalendarID: master.CalendarID, Uid: master.Uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list exdate logs: %w", err)
	}
	ownDeletes := make(map[string]bool, len(exdateLogs))
	for _, l := range exdateLogs {
		ownDeletes[l.RecurrenceID] = true
	}
	var ids []int64
	for _, r := range trashed {
		if r.RecurrenceID == "" || r.DeletedAt == nil || *r.DeletedAt != *master.DeletedAt || truncated(r) || ownDeletes[r.RecurrenceID] {
			continue
		}
		ids = append(ids, r.ID)
	}
	return ids, nil
}

// truncationHidden returns a test for the rows that the unconsumed
// truncation logs of the series in calendarID hid. A log row with row IDs
// names its rows. A log row from before the IDs (migration 057) names
// recurrence IDs in hidden_overrides. A log row from before issue #287
// names no rows: the rows at or after its cutoff count.
func truncationHidden(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (func(storage.Event) bool, error) {
	logs, err := qtx.ListEventTruncateDeletesByCalendarUID(ctx, storage.ListEventTruncateDeletesByCalendarUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return nil, fmt.Errorf("list truncate logs: %w", err)
	}
	ids := make(map[int64]bool)
	recIDs := make(map[string]bool)
	var cutoffs []string
	for _, log := range logs {
		switch {
		case log.HiddenIds != nil:
			parsed, err := parseRowIDs(*log.HiddenIds)
			if err != nil {
				return nil, err
			}
			for _, id := range parsed {
				ids[id] = true
			}
		case log.HiddenOverrides != nil:
			hidden, err := parseCommaList(*log.HiddenOverrides, parseRecurrenceID)
			if err != nil {
				return nil, err
			}
			for _, recID := range hidden {
				recIDs[recID] = true
			}
		default:
			cutoffs = append(cutoffs, log.CutoffTime)
		}
	}
	return func(r storage.Event) bool {
		if ids[r.ID] {
			return true
		}
		if r.RecurrenceID == "" {
			return false
		}
		if recIDs[r.RecurrenceID] {
			return true
		}
		for _, cutoff := range cutoffs {
			if r.RecurrenceID >= cutoff {
				return true
			}
		}
		return false
	}, nil
}

// restoreFromInstance reverses a "this and following" truncation for the TUI/CLI
// undo path. It applies the stale-master guard. Undo is rejected if an external
// edit advanced the master past the truncation. It then routes the reversal
// through restoreTruncationByLogID, the same provenance-aware path the trash
// view uses.
//
// That restores the RRULE, the trimmed RDATEs (issue #490), and only the
// overrides the truncation itself hid (issue #491, the #287 class on undo).
// The truncate-log row is consumed. No phantom "Series tail" trash entry
// then lingers for the now-live series.
func (s *Service) restoreFromInstance(ctx context.Context, meta UndoMeta) error {
	master, err := findSeriesMasterIncludingDeletedTx(ctx, s.q, meta.CalendarID, meta.UID)
	if err != nil {
		return wrapMasterLookup(err)
	}
	prevUpdated := parseStorageTime(master.UpdatedAt)
	if prevUpdated.After(meta.MasterUpdatedBefore.Add(time.Second)) {
		return fmt.Errorf("master advanced since delete (expected updated_at <= %s, got %s)",
			meta.MasterUpdatedBefore.Format(time.RFC3339), prevUpdated.Format(time.RFC3339))
	}

	log, err := s.q.GetEventTruncateDeleteByCalendarUIDAndCutoff(ctx, storage.GetEventTruncateDeleteByCalendarUIDAndCutoffParams{
		CalendarID: master.CalendarID,
		Uid:        meta.UID,
		CutoffTime: meta.CutoffTime.UTC().Format(time.RFC3339),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get truncate log: %w", err)
	}
	return s.restoreTruncationByLogID(ctx, log.ID)
}

// reconcileTx mirrors the 3-case state machine from
// snapshot.go's Restore. For a freshly un-hidden row:
//   - Case A (local-only, no sync_resource): no-ops.
//   - Case B (tombstone present): clear the tombstone.
//   - Case C (tombstone + sync_resource both gone): MarkResourceDirty creates
//     a fresh sync_resource with remote_url=” so the next push allocates a
//     new href.
//
// Override rows don't own a sync_resource; callers pass the master's UID so
// the master is marked dirty instead, which covers the override on push.
//
// It runs in the transaction of the restore, so the restore, the tombstone
// delete, and the dirty mark commit together or not at all.
func reconcileTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) error {
	if err := qtx.ReconcileAfterRestore(ctx, calendarID, uid, "event"); err != nil {
		return fmt.Errorf("reconcile sync after restore: %w", err)
	}
	return nil
}

func parseStorageTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(timeutil.StorageTimeFormat, s)
	if err != nil {
		t, _ = time.Parse(time.RFC3339, s)
	}
	return t
}

// ErrHasOverrides is returned when a delete targets a recurring master
// event that has override instances. Use DeleteSeries instead.
var ErrHasOverrides = fmt.Errorf("event has overrides: use DeleteSeries to delete the entire series")

func (s *Service) Delete(ctx context.Context, id int64) error {
	return s.deleteRow(ctx, id, nil)
}

// deletedRow is the result of deleteRow for the undo. The delete
// transaction reads both rows.
type deletedRow struct {
	// Row is the deleted row, as the transaction read it before the delete.
	Row storage.Event
	// Master is the master of an override row, live or in the trash. The ID
	// is 0 when the row has no master.
	Master storage.Event
	// ExdateLogID and ExdateDeleteSeq name the delete in the occurrence log
	// of the master. 0 means no log (see UndoMeta.ExdateLogID).
	ExdateLogID, ExdateDeleteSeq int64
}

// deleteRow is Delete. One transaction reads the row, checks it, and
// deletes it. A non-nil out receives the row and, for an override row, its
// master. A failed read fails the delete.
func (s *Service) deleteRow(ctx context.Context, id int64, out *deletedRow) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	r, err := qtx.GetEvent(ctx, id)
	if err != nil {
		return err
	}
	evt := FromStorage(r)
	if out != nil {
		out.Row = r
	}

	if err := calendaraccess.EnsureWritable(ctx, qtx, evt.CalendarID, "VEVENT"); err != nil {
		return err
	}

	// If this is a recurring master, check for overrides. RDATE-only masters
	// (no RRULE) are recurring too, so guard on either rule or RDATEs (#415).
	if (evt.RecurrenceRule != "" || evt.RDates != "") && evt.RecurrenceID == "" {
		overrides, err := qtx.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
			CalendarID: evt.CalendarID, Uid: evt.UID,
		})
		if err != nil {
			return fmt.Errorf("check overrides: %w", err)
		}
		if len(overrides) > 0 {
			return ErrHasOverrides
		}
	}

	// If this is a standalone event (no recurrence or a solo master), create
	// a tombstone so the sync engine can send a DELETE to the server. The
	// tombstone and the soft-delete commit together: if the tombstone write
	// fails the soft-delete rolls back, so the next sync can never DELETE a
	// still-live event from the server (issue #107).
	if evt.RecurrenceID == "" {
		if _, err := storage.CreateTombstoneIfSynced(ctx, tx, evt.CalendarID, evt.UID); err != nil {
			return fmt.Errorf("create tombstone: %w", err)
		}
		if err := qtx.SoftDeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		return tx.Commit()
	}

	// If this is an override, add EXDATE to the master so the instance
	// doesn't reappear on next expansion. The master's sync resource
	// becomes dirty (modified EXDATE), not the override.
	if evt.RecurrenceID != "" {
		// The master is in the calendar of the override, or the override is a
		// legacy override of the one live master of the UID. A legacy override of a
		// UID with more than one live master belongs to no series. The expansion
		// keys the overrides by calendar, so no master shows it. The row goes
		// with no EXDATE record, like a row with no master.
		master, err := findSeriesMasterTx(ctx, qtx, evt.CalendarID, evt.UID)
		// A genuine lookup error (e.g. SQLITE_BUSY) must not collapse into the
		// "no master" path: that would soft-delete the override while skipping
		// its EXDATE/provenance bookkeeping, resurrecting the occurrence via
		// series expansion (issue #412). Only a missing master (ErrNoRows)
		// legitimately skips the bookkeeping.
		if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
			return fmt.Errorf("get master: %w", err)
		}
		if err == nil {
			// The EXDATE and the delete log go onto the master, so its
			// calendar takes the write too. A legacy override lives in
			// another calendar than its master.
			if master.CalendarID != evt.CalendarID {
				if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
					return err
				}
			}
			// Fail on a malformed recurrence_id. The master cannot exclude it,
			// so a delete of the override would bring back the occurrence
			// through the series expansion. The restore path treats the same
			// parse failure as fatal.
			if _, parseErr := timeutil.ParseRecurrenceID(evt.RecurrenceID); parseErr != nil {
				return fmt.Errorf("parse recurrence_id %q: %w", evt.RecurrenceID, parseErr)
			}
		}

		// The master of the row, live or in the trash, for the undo and for
		// the orphan rule. ErrNoRows here means no master row anywhere.
		anyMaster, anyErr := master, err
		if err != nil {
			anyMaster, anyErr = findSeriesMasterIncludingDeletedTx(ctx, qtx, evt.CalendarID, evt.UID)
		}
		if anyErr != nil && !errors.Is(anyErr, sql.ErrNoRows) && !errors.Is(anyErr, ErrAmbiguousUID) {
			return fmt.Errorf("get master: %w", anyErr)
		}
		if errors.Is(anyErr, sql.ErrNoRows) {
			// An orphan row gets an occurrence log of its own delete. A
			// restore by UID then leaves the row to its own restore, as for
			// an occurrence of a series.
			if err := recordOrphanDeleteTx(ctx, qtx, evt.CalendarID, evt.UID, evt.RecurrenceID); err != nil {
				return err
			}
		}
		if err := qtx.SoftDeleteEvent(ctx, id); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		if err == nil {
			// The log of the master records the delete. It adds the EXDATE
			// only when no live override of the series remains at the
			// occurrence (softdelete.RecordOccurrenceDelete). A master in the
			// trash gets no log: the undo then brings back the row alone.
			logID, seq, err := recordOccurrenceDeleteTx(ctx, qtx, master, evt.RecurrenceID, []int64{evt.ID})
			if err != nil {
				return err
			}
			if out != nil {
				out.ExdateLogID, out.ExdateDeleteSeq = logID, seq
			}
		}
		// Mark the master dirty — its EXDATE was modified — inside the same
		// transaction so a failed mark rolls the EXDATE change back rather than
		// committing a change that is never pushed (issue #107).
		if err := storage.MarkResourceDirty(ctx, tx, evt.CalendarID, evt.UID, "event"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
		if out != nil && anyErr == nil {
			out.Master = anyMaster
		}
		return tx.Commit()
	}

	// Unreachable: RecurrenceID is either "" (handled above) or non-empty.
	return tx.Commit()
}

// calendarMasterTx returns the master row of uid in calendarID, live or in
// the trash. found is false with no master row. One lookup on the unique
// key. Every rule that asks whether a calendar holds a master reads it.
func calendarMasterTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (master storage.Event, found bool, err error) {
	m, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{CalendarID: calendarID, Uid: uid})
	found, err = storage.Found(err)
	if err != nil {
		return storage.Event{}, false, fmt.Errorf("get master: %w", err)
	}
	return m, found, nil
}

// calendarHoldsMasterTx reports whether calendarID holds a master row of
// uid, live or in the trash (see calendarMasterTx).
func calendarHoldsMasterTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (bool, error) {
	_, found, err := calendarMasterTx(ctx, qtx, calendarID, uid)
	return found, err
}

// recordOrphanDeleteTx writes the occurrence log of a delete by ID of an
// orphan row: the UID has no master row, live or in the trash. The log owns
// no EXDATE. A master would hold the record of an occurrence delete.
func recordOrphanDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string) error {
	log, _, err := occurrenceLogTx(ctx, qtx, calendarID, uid, recurrenceID)
	if err != nil {
		return err
	}
	log.LastSeq++
	log.Entries = append(log.Entries, softdelete.OccurrenceEntry{Seq: log.LastSeq})
	log.OwnsExdate = false
	if _, err := occurrenceStoreTx(qtx, calendarID, uid, recurrenceID, true).SaveLog(ctx, log); err != nil {
		return fmt.Errorf("record orphan delete: %w", err)
	}
	return nil
}

// dropOrphanDeleteLogTx drops the occurrence log that recordOrphanDeleteTx
// wrote for an orphan row in calendarID. The restore and the purge of the row
// call it. calendarHoldsMaster reports a master row of the UID in calendarID,
// live or in the trash. That calendar keeps its logs, because the EXDATE
// contract owns them. A master in another calendar owns no log of calendarID,
// so the log goes.
func dropOrphanDeleteLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string, calendarHoldsMaster bool) error {
	if recurrenceID == "" || calendarHoldsMaster {
		return nil
	}
	log, err := qtx.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
		CalendarID: calendarID, Uid: uid, RecurrenceID: recurrenceID,
	})
	found, err := storage.Found(err)
	if err != nil || !found {
		return err
	}
	return qtx.DeleteEventExdateDelete(ctx, log.ID)
}

// DeleteInstance excludes a single occurrence of a recurring event by adding
// an EXDATE to the master. instanceTime is the StartTime of the occurrence.
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842). With 0, one live master is the only choice, and
// more give ErrAmbiguousUID. See findSeriesMasterTx.
func (s *Service) DeleteInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) error {
	_, _, err := s.deleteInstance(ctx, calendarID, uid, instanceTime)
	return err
}

// occurrenceDeletion is the record of one occurrence delete, for the undo.
type occurrenceDeletion struct {
	// Rows are the override rows that the delete hid.
	Rows []int64
	// LogID and Seq name the delete in its occurrence log.
	LogID, Seq int64
}

// deleteInstance is DeleteInstance. It returns the master it read in the
// transaction, and the record of the delete. It also returns the IDs of the override rows that the delete
// hid. These are the row beside the master and each legacy override of the
// occurrence that the series owns. No stored override gives none.
func (s *Service) deleteInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (storage.Event, occurrenceDeletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the EXDATE list we recompute
	// reflects a concurrent writer's changes (issue #116). Reading outside the
	// tx let a second instance-delete clobber the first one's EXDATE.
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return storage.Event{}, occurrenceDeletion{}, wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return storage.Event{}, occurrenceDeletion{}, err
	}

	recID := instanceTime.UTC().Format(time.RFC3339)

	// The occurrence goes as a whole: the delete hides the row beside the
	// master and each legacy override of the occurrence that the series
	// owns. A row that stays live would show the occurrence again.
	rows, err := qtx.ListSeriesOverridesAtRecurrenceID(ctx, storage.ListSeriesOverridesAtRecurrenceIDParams{
		CalendarID: master.CalendarID, Uid: master.Uid, RecurrenceID: recID,
	})
	if err != nil {
		return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("list overrides: %w", err)
	}
	hidden := make([]int64, 0, len(rows))
	for _, o := range rows {
		// A legacy override lives in another calendar than its master,
		// and the delete writes that calendar too.
		if o.CalendarID != master.CalendarID {
			if err := calendaraccess.EnsureWritable(ctx, qtx, o.CalendarID, "VEVENT"); err != nil {
				return storage.Event{}, occurrenceDeletion{}, err
			}
		}
		if err := qtx.SoftDeleteEvent(ctx, o.ID); err != nil {
			return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("soft-delete override: %w", err)
		}
		hidden = append(hidden, o.ID)
	}

	// The log makes the delete visible in the trash. It names the hidden
	// rows, so a restore of the occurrence from the trash un-hides them. No
	// live override remains, so the delete adds the EXDATE, unless the log
	// owns one already (softdelete.RecordOccurrenceDelete).
	d := occurrenceDeletion{Rows: hidden}
	if d.LogID, d.Seq, err = recordOccurrenceDeleteTx(ctx, qtx, master, recID, hidden); err != nil {
		return storage.Event{}, occurrenceDeletion{}, err
	}

	// Mark the master dirty — its EXDATE was modified — inside the same
	// transaction so a failed mark rolls the EXDATE change back rather than
	// committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Event{}, occurrenceDeletion{}, err
	}
	return master, d, nil
}

// exdateLogTx reads the occurrence log of recID in calendarID. found is
// false with no log. A pruned marker is no log.
func exdateLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recID string) (storage.EventExdateDelete, bool, error) {
	log, err := qtx.GetEventExdateDeleteByCalendarUIDRecurrence(ctx, storage.GetEventExdateDeleteByCalendarUIDRecurrenceParams{
		CalendarID: calendarID, Uid: uid, RecurrenceID: recID,
	})
	found, err := storage.Found(err)
	if err != nil {
		return storage.EventExdateDelete{}, false, fmt.Errorf("get exdate log: %w", err)
	}
	return log, found, nil
}

// DeleteFromInstance truncates a recurring series so that instances at or
// after instanceTime are removed. It sets UNTIL on the RRULE. It soft-deletes
// any overrides at or after the cutoff. It records the pre-truncation
// RRULE in event_truncate_deletes so the trash view can restore it atomically.
// calendarID names the calendar of the series (see DeleteInstance).
func (s *Service) DeleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) error {
	_, _, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
	return err
}

// HasLiveOverrideFrom reports whether any live override of the series of uid
// in calendarID has a RECURRENCE-ID at or after from. The --following scope
// guard uses it. A truncation removes such overrides even when an imported
// EXDATE hides every generated slot, so their presence keeps the scope
// meaningful. The overrides of a master in another calendar stay out (issue
// #842). The truncation does not hide them, so they do not make the scope
// meaningful.
func (s *Service) HasLiveOverrideFrom(ctx context.Context, calendarID int64, uid string, from time.Time) (bool, error) {
	return s.q.HasLiveSeriesOverrideAtOrAfter(ctx, storage.HasLiveSeriesOverrideAtOrAfterParams{
		CalendarID:   calendarID,
		Uid:          uid,
		RecurrenceID: from.UTC().Format(time.RFC3339),
	})
}

// softDeleteOverridesAndRecordTruncation trims the master's post-cutoff RDATEs.
// It hides every live override at or after cutoff. It records the truncation
// so the trash view can restore it. It captures the recurrence_ids it hides
// and the RDATEs it drops BEFORE it removes them. Restore then re-shows
// exactly those overrides and re-adds exactly those RDATEs.
//
// It does not restore an override the user deleted on its own (issue #287).
// It does not leave a post-cutoff RDATE to reappear on the next expansion
// (issue #463, since rrule-go expands RDATEs independently of the RRULE UNTIL
// bound). Pairs with restoreTruncatedOverrides / restoreTruncatedRDates.
//
// prevRRule is the master's pre-truncation RRULE. The caller passes it because
// it overwrote the master's recurrence_rule in the DB before this runs.
func softDeleteOverridesAndRecordTruncation(ctx context.Context, qtx *storage.Queries, master storage.Event, instanceTime time.Time, prevRRule string) error {
	uid := master.Uid
	cutoff := instanceTime.UTC().Format(time.RFC3339)

	removedRDates, err := trimMasterRDatesAtOrAfter(ctx, qtx, master, instanceTime)
	if err != nil {
		return err
	}

	// The overrides of the series in the calendar of the master. The rows
	// of a master in another calendar stay (issue #842).
	hidden, err := qtx.ListLiveSeriesOverridesAtOrAfter(ctx, storage.ListLiveSeriesOverridesAtOrAfterParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: cutoff,
	})
	if err != nil {
		return fmt.Errorf("list future overrides: %w", err)
	}
	// The log keeps the recurrence IDs for the trash view and the row IDs for
	// the restore (see restoreTruncatedOverrides). A legacy override lives in
	// another calendar than its master, and the hide writes that calendar. Its
	// access policy applies now, as it applies to the restore later.
	recIDs := make([]string, 0, len(hidden))
	rowIDs := make([]int64, 0, len(hidden))
	for _, o := range hidden {
		recIDs = append(recIDs, o.RecurrenceID)
		rowIDs = append(rowIDs, o.ID)
	}
	if err := ensureRowsWritableTx(ctx, qtx, master.CalendarID, rowIDs); err != nil {
		return err
	}
	if len(rowIDs) > 0 {
		if err := qtx.SoftDeleteEventsByIDs(ctx, rowIDs); err != nil {
			return fmt.Errorf("soft-delete future overrides: %w", err)
		}
	}
	// A second truncation at the same cutoff extends the log of the first.
	// The log then names the rows of both truncations.
	earlier, err := qtx.GetEventTruncateDeleteByCalendarUIDAndCutoff(ctx, storage.GetEventTruncateDeleteByCalendarUIDAndCutoffParams{
		CalendarID: master.CalendarID, Uid: uid, CutoffTime: cutoff,
	})
	found, err := storage.Found(err)
	if err != nil {
		return fmt.Errorf("get truncation log: %w", err)
	}
	if found {
		earlierIDs := storage.NullableToString(earlier.HiddenIds)
		if earlier.HiddenIds == nil {
			// A log from before migration 057 names its rows by recurrence
			// ID only. Its rows are the trashed rows of the series at those
			// recurrence IDs.
			if earlierIDs, err = trashedRowsAtTx(ctx, qtx, master, storage.NullableToString(earlier.HiddenOverrides)); err != nil {
				return err
			}
		}
		if recIDs, err = unionCommaList(recIDs, storage.NullableToString(earlier.HiddenOverrides), parseRecurrenceID); err != nil {
			return err
		}
		if rowIDs, err = unionCommaList(rowIDs, earlierIDs, parseRowID); err != nil {
			return err
		}
	}
	hiddenList := strings.Join(recIDs, ",")
	hiddenIDs := formatRowIDs(rowIDs)

	if err := qtx.RecordEventTruncateDelete(ctx, storage.RecordEventTruncateDeleteParams{
		CalendarID:      master.CalendarID,
		Uid:             uid,
		CutoffTime:      cutoff,
		PreviousRrule:   prevRRule,
		HiddenOverrides: &hiddenList,
		RemovedRdates:   &removedRDates,
		HiddenIds:       &hiddenIDs,
	}); err != nil {
		return fmt.Errorf("record truncate: %w", err)
	}
	return nil
}

// trashedRowsAtTx returns a list of the IDs of the trashed rows of the series
// of master. The rows are at the recurrence IDs of the comma-separated list
// recIDs.
func trashedRowsAtTx(ctx context.Context, qtx *storage.Queries, master storage.Event, recIDs string) (string, error) {
	want, err := parseCommaList(recIDs, parseRecurrenceID)
	if err != nil || len(want) == 0 {
		return "", err
	}
	at := make(map[string]bool, len(want))
	for _, r := range want {
		at[r] = true
	}
	rows, err := qtx.ListTrashedSeriesRowsInCalendar(ctx, storage.ListTrashedSeriesRowsInCalendarParams{CalendarID: master.CalendarID, Uid: master.Uid})
	if err != nil {
		return "", fmt.Errorf("list trashed series rows: %w", err)
	}
	ids := make([]int64, 0, len(rows))
	for _, r := range rows {
		if r.RecurrenceID != "" && at[r.RecurrenceID] {
			ids = append(ids, r.ID)
		}
	}
	return formatRowIDs(ids), nil
}

// trimMasterRDatesAtOrAfter rewrites the master's rdates column to keep only the
// occurrences strictly before instanceTime, and returns the dropped ones
// serialized for the truncation log. It only issues an UPDATE when something
// changes, so a master with no post-cutoff RDATEs is left untouched.
func trimMasterRDatesAtOrAfter(ctx context.Context, qtx *storage.Queries, master storage.Event, instanceTime time.Time) (string, error) {
	rdates := ParseTimeList(storage.NullableToString(master.Rdates))
	if len(rdates) == 0 {
		return "", nil
	}
	kept := make([]time.Time, 0, len(rdates))
	var removed []time.Time
	for _, rd := range rdates {
		if rd.Before(instanceTime) {
			kept = append(kept, rd)
		} else {
			removed = append(removed, rd)
		}
	}
	if len(removed) == 0 {
		return "", nil
	}
	if err := qtx.UpdateEventRdates(ctx, storage.UpdateEventRdatesParams{
		Rdates: storage.StringToNullable(SerializeTimeList(kept)),
		ID:     master.ID,
	}); err != nil {
		return "", fmt.Errorf("trim rdates: %w", err)
	}
	return SerializeTimeList(removed), nil
}

// deleteFromInstance performs the truncation and returns the master's
// updated_at as written by this operation, read back inside the same
// transaction. The in-tx value (rather than a post-commit read) closes a
// TOCTOU window. A concurrent writer that edits the master after our commit
// but before a separate read would otherwise have its updated_at captured as
// the undo baseline. RestoreUndo would then clobber that edit.
func (s *Service) deleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (storage.Event, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Event{}, "", fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the RRULE we truncate reflects
	// a concurrent writer's edits rather than a pre-transaction snapshot
	// (issue #116).
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return storage.Event{}, "", wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return storage.Event{}, "", err
	}

	// An RDATE-only master (no RRULE) has no recurrence rule to truncate.
	// Synthesizing an "UNTIL=..." here would be an invalid bare RRULE that fails
	// to parse, collapsing the whole series to its DTSTART (issue #414). Leave
	// the rule NULL; the future-override soft-delete and truncation record below
	// still apply.
	prevRRule := storage.NullableToString(master.RecurrenceRule)
	if prevRRule != "" {
		until := instanceTime.UTC().Add(-time.Second)
		rule := setRRuleUntil(prevRRule, until, master.AllDay == 1)
		if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
			RecurrenceRule: storage.StringToNullable(rule),
			ID:             master.ID,
		}); err != nil {
			return storage.Event{}, "", fmt.Errorf("update rrule: %w", err)
		}
	}

	if err := softDeleteOverridesAndRecordTruncation(ctx, qtx, master, instanceTime, prevRRule); err != nil {
		return storage.Event{}, "", err
	}

	// Read the master's updated_at back inside the transaction so the value we
	// return reflects exactly this truncation's write, with no chance of an
	// interleaved external edit in between.
	truncated, err := qtx.GetEvent(ctx, master.ID)
	if err != nil {
		return storage.Event{}, "", fmt.Errorf("read back master: %w", err)
	}
	postUpdated := truncated.UpdatedAt

	// Mark the master dirty — its RRULE and RDATEs were modified — inside
	// the same transaction so a failed mark rolls the truncation back rather
	// than committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return storage.Event{}, "", fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Event{}, "", err
	}
	return master, postUpdated, nil
}

// DeleteSeries deletes a recurring master event and all its overrides.
// calendarID names the calendar of the series. Two calendars can hold the
// same UID (issue #842), so the rows of the other calendar stay. With 0,
// one live master is the only choice, and more give ErrAmbiguousUID. See
// findSeriesMasterTx.
func (s *Service) DeleteSeries(ctx context.Context, calendarID int64, uid string) error {
	_, err := s.deleteSeries(ctx, calendarID, uid)
	return err
}

// seriesDeletion is the record of one series delete.
type seriesDeletion struct {
	// Master is the master the delete read in the transaction. ID 0 means
	// no live master: the delete hid orphan rows.
	Master storage.Event
	// Label names the delete for the undo: the title of the master, or of
	// the first hidden row.
	Label string
	// CalendarID is the calendar of the delete.
	CalendarID int64
	// Hidden lists the orphan rows the delete hid, by ID. Empty for a
	// delete with a master: the series-delete log holds its rows.
	Hidden []int64
	// Tombstoned lists every calendar that got a tombstone, the calendar
	// of the delete first.
	Tombstoned []int64
}

// deleteSeries is DeleteSeries. It returns the record of the delete.
func (s *Service) deleteSeries(ctx context.Context, calendarID int64, uid string) (seriesDeletion, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return seriesDeletion{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	t, err := resolveSeriesDeleteTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return seriesDeletion{}, err
	}
	// The tombstone and the soft-delete commit together (issue #107). A failed
	// tombstone write then cannot leave a tombstone for a live series, which the
	// next sync would DELETE from the server.
	if t.master.ID == 0 {
		// No live master anywhere: the live rows of the calendar are orphans.
		// Beside a master of their series in the trash, the log of the series
		// delete of that master still names rows. The new rows join that list. With
		// no such master, an earlier log names another delete, and the delete
		// writes a fresh log.
		ids := make([]int64, 0, len(t.orphans))
		for _, o := range t.orphans {
			ids = append(ids, o.ID)
		}
		var into *storage.Event
		if t.owner.ID != 0 {
			into = &t.owner
		}
		if err := recordSeriesDeleteTx(ctx, qtx, t.calendarID, uid, ids, into, false); err != nil {
			return seriesDeletion{}, err
		}
		// The rows of a synced calendar have a server object. A tombstone
		// queues its delete, as for a series with a master, or the next
		// pull brings the rows back.
		tombstoned, err := storage.CreateTombstoneIfSynced(ctx, tx, t.calendarID, uid)
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("create tombstone: %w", err)
		}
		var calendars []int64
		if tombstoned {
			calendars = []int64{t.calendarID}
		}
		if err := qtx.SoftDeleteEventsByCalendarUID(ctx, storage.SoftDeleteEventsByCalendarUIDParams{
			CalendarID: t.calendarID, Uid: uid,
		}); err != nil {
			return seriesDeletion{}, fmt.Errorf("soft-delete orphan rows: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return seriesDeletion{}, err
		}
		return seriesDeletion{Label: t.label(), CalendarID: t.calendarID, Hidden: ids, Tombstoned: calendars}, nil
	}
	if _, err := storage.CreateTombstoneIfSynced(ctx, tx, t.master.CalendarID, uid); err != nil {
		return seriesDeletion{}, fmt.Errorf("create tombstone: %w", err)
	}
	// Record the live override rows that this delete hides, by ID, so a
	// restore of the series brings back exactly those rows.
	live, err := qtx.ListSeriesOverridesInCalendar(ctx, storage.ListSeriesOverridesInCalendarParams{
		CalendarID: t.master.CalendarID, Uid: uid,
	})
	if err != nil {
		return seriesDeletion{}, fmt.Errorf("list overrides: %w", err)
	}
	hidden := make([]int64, 0, len(live))
	tombstoned := map[int64]bool{t.master.CalendarID: true}
	calendars := []int64{t.master.CalendarID}
	for _, o := range live {
		hidden = append(hidden, o.ID)
		// A legacy override lives in another calendar. Its server object
		// there needs a delete too, or the next pull brings it back.
		if !tombstoned[o.CalendarID] {
			tombstoned[o.CalendarID] = true
			calendars = append(calendars, o.CalendarID)
			if _, err := storage.CreateTombstoneIfSynced(ctx, tx, o.CalendarID, uid); err != nil {
				return seriesDeletion{}, fmt.Errorf("create tombstone: %w", err)
			}
		}
	}
	if err := recordSeriesDeleteTx(ctx, qtx, t.master.CalendarID, uid, hidden, nil, true); err != nil {
		return seriesDeletion{}, err
	}
	if err := qtx.SoftDeleteSeriesRowsInCalendar(ctx, storage.SoftDeleteSeriesRowsInCalendarParams{
		CalendarID: t.calendarID, Uid: uid,
	}); err != nil {
		return seriesDeletion{}, fmt.Errorf("soft-delete series: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return seriesDeletion{}, err
	}
	return seriesDeletion{Master: t.master, Label: t.label(), CalendarID: t.master.CalendarID, Tombstoned: calendars}, nil
}

// seriesDeleteTarget is what a series delete of a UID in a calendar takes.
type seriesDeleteTarget struct {
	// master is the live master of the series. ID 0 means orphan rows.
	master storage.Event
	// calendarID is the calendar of the delete.
	calendarID int64
	// orphans holds the live orphan rows of the calendar when master has
	// ID 0.
	orphans []storage.Event
	// owner is the master in the trash of the series of the orphan rows (see
	// findSeriesMasterIncludingDeletedTx). The delete then extends the
	// series-delete log of that master, in the calendar of the master. ID 0
	// means no such master: the delete writes a fresh log.
	owner storage.Event
}

// label names the delete: the title of the master, or of the first orphan
// row.
func (t seriesDeleteTarget) label() string {
	if t.master.ID != 0 {
		return t.master.Title
	}
	return t.orphans[0].Title
}

// resolveSeriesDeleteTx resolves the target of a series delete of uid in
// calendarID, with no write. It returns the exact error of the delete: a
// series in the trash, an ambiguous UID, a refused calendar, or nothing to
// delete. deleteSeries and SeriesDeleteTarget share it.
func resolveSeriesDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (seriesDeleteTarget, error) {
	master, mErr := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	// Only ErrNoRows means "no master to track". A genuine lookup error must
	// abort. Otherwise the series is soft-deleted locally with no tombstone.
	// The next pull would then resurrect the series (issue #290).
	if mErr != nil && !errors.Is(mErr, sql.ErrNoRows) {
		return seriesDeleteTarget{}, wrapMasterLookup(mErr)
	}
	if mErr == nil {
		calendarID = master.CalendarID
	} else if calendarID != 0 {
		// The named calendar holds no live series of the UID: its master is in the
		// trash, or it holds no row. When another calendar holds a live master, a
		// delete of the rows of this calendar would also take the legacy overrides
		// of that master. The delete then has no target. With no live master
		// anywhere, the rows are orphans (a purged master), and the delete takes
		// them.
		n, err := qtx.EventUIDMasterCalendars(ctx, uid)
		if err != nil {
			return seriesDeleteTarget{}, fmt.Errorf("find master calendars: %w", err)
		}
		if n.Calendars > 0 {
			found, err := calendarHoldsMasterTx(ctx, qtx, calendarID, uid)
			if err != nil {
				return seriesDeleteTarget{}, err
			}
			if found {
				return seriesDeleteTarget{}, ErrSeriesInTrash
			}
			return seriesDeleteTarget{}, wrapMasterLookup(mErr)
		}
	}
	if calendarID == 0 {
		// No calendar and no live master: the UID holds orphan rows only.
		// One calendar with live rows is the target.
		calendars, err := qtx.ListLiveEventCalendarIDsByUID(ctx, uid)
		if err != nil {
			return seriesDeleteTarget{}, fmt.Errorf("list calendars: %w", err)
		}
		switch len(calendars) {
		case 0:
			return seriesDeleteTarget{}, ErrNotDeleted
		case 1:
			calendarID = calendars[0]
		default:
			return seriesDeleteTarget{}, ErrAmbiguousUID
		}
	}
	// The access guard covers the calendars of the series only, after the
	// resolve of the target calendar. A read-only copy of the UID in
	// another calendar does not block the delete (issue #842).
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return seriesDeleteTarget{}, err
	}
	t := seriesDeleteTarget{master: master, calendarID: calendarID}
	if mErr == nil {
		return t, nil
	}
	// No live master anywhere: the live rows of the calendar are orphans.
	// The series rule assigns them to a master in the trash, if any. No row
	// means nothing to delete.
	live, err := qtx.ListLiveOverridesInCalendarByUID(ctx, storage.ListLiveOverridesInCalendarByUIDParams{
		CalendarID: calendarID, Uid: uid,
	})
	if err != nil {
		return seriesDeleteTarget{}, fmt.Errorf("list orphan rows: %w", err)
	}
	if len(live) == 0 {
		return seriesDeleteTarget{}, ErrNotDeleted
	}
	t.master, t.orphans = storage.Event{}, live
	// The live rows can belong to a series with its master in the trash.
	// With a calendar named or not, the rule is the same: the rows go as
	// orphans, and they join the log of that master.
	owner, err := findSeriesMasterIncludingDeletedTx(ctx, qtx, calendarID, uid)
	if err != nil && !errors.Is(err, sql.ErrNoRows) && !errors.Is(err, ErrAmbiguousUID) {
		return seriesDeleteTarget{}, wrapMasterLookup(err)
	}
	if err == nil && owner.DeletedAt != nil {
		t.owner = owner
	}
	return t, nil
}

// SeriesDeleteTarget reports what DeleteSeries of uid in calendarID would
// delete, with no write. It returns the label of the delete and the
// calendar of the series, or the exact error that DeleteSeries would
// return. The CLI reads it before the confirm prompt, and passes the
// calendar to the delete: with the calendar named, the delete never picks
// another series.
func (s *Service) SeriesDeleteTarget(ctx context.Context, calendarID int64, uid string) (label string, seriesCalendarID int64, err error) {
	t, err := resolveSeriesDeleteTx(ctx, s.q, calendarID, uid)
	if err != nil {
		return "", 0, err
	}
	return t.label(), t.calendarID, nil
}
