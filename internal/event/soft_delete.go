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

// ErrDeletedAgain reports an undo or a restore of a delete whose row a newer
// delete owns now. The row came back, and a later delete hid it again. The
// undo of the older delete must not take the row from the newer one.
var ErrDeletedAgain = fmt.Errorf("%w: %s", ErrNotDeleted, MsgDeletedAgain)

// ErrChangedSince reports an undo of a delete whose row is in the trash with
// no log of that delete. A pull hid it, or a trigger dropped the log. The
// trash still restores the row.
var ErrChangedSince = fmt.Errorf("%w: %s", ErrNotDeleted, MsgChangedSince)

// MsgDeletedAgain and MsgChangedSince are the texts of ErrDeletedAgain and
// ErrChangedSince for the user. The TUI shows them, so the two places use
// one text.
const (
	MsgDeletedAgain = "the item was deleted again; restore that delete instead"
	MsgChangedSince = "the item changed after the delete; restore it from the trash if you want it back"
)

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

// ErrUndoNotPossible reports a permanent refusal of RestoreUndo. The undo
// data can never succeed: the rows are gone, live, or owned by a newer
// delete, the calendar is read-only, or the series changed. RestoreUndo wraps
// each such error, and the original error stays in the chain. A caller drops
// the undo entry for it. Any other error is transient, for example a busy
// database or a canceled context, and a retry can succeed.
var ErrUndoNotPossible = errors.New("event: the undo is not possible")

// ErrMasterAdvanced reports an undo of a truncation whose master changed after
// the truncation. The undo would overwrite that change.
var ErrMasterAdvanced = errors.New("event: the master changed after the delete")

// ErrNewerTruncation reports the restore of a truncation while a newer
// truncation of the same series is in the trash. The newer truncation
// recorded the rule that the older one wrote, so only the newest one restores
// cleanly. A *NewerTruncationError names the cutoff of the newer one.
var ErrNewerTruncation = errors.New("event: restore the newer truncation of this series first")

// NewerTruncationError is ErrNewerTruncation with the cutoff of the newer
// truncation. AllDay reports an all-day series. Its cutoff is a UTC
// midnight, so a caller shows the UTC date of the cutoff.
type NewerTruncationError struct {
	Cutoff time.Time
	AllDay bool
}

func (e *NewerTruncationError) Error() string {
	return fmt.Sprintf("restore the newer truncation of this series first (cutoff %s)", e.Cutoff.UTC().Format(time.RFC3339))
}

func (e *NewerTruncationError) Unwrap() error { return ErrNewerTruncation }

// undoRefusals are the permanent refusals of an undo (ErrUndoNotPossible).
var undoRefusals = []error{
	ErrNotDeleted, ErrMasterInTrash, ErrMasterPurged, ErrMasterAdvanced,
	ErrSeriesInTrash, ErrAmbiguousUID, ErrAmbiguousOverride, ErrOccurrenceOutsideSeries,
	calendaraccess.ErrReadOnly, calendaraccess.ErrUnsupportedComponent,
}

// undoRefusal is a permanent refusal of an undo. Its text is the text of the
// cause, and it matches ErrUndoNotPossible and the cause.
type undoRefusal struct{ cause error }

func (e undoRefusal) Error() string        { return e.cause.Error() }
func (e undoRefusal) Unwrap() error        { return e.cause }
func (e undoRefusal) Is(target error) bool { return target == ErrUndoNotPossible }

// ErrUndoNotYet reports an undo that cannot run now but can succeed later,
// after another restore. An example is the undo of a truncation while a newer
// truncation of the series is in the trash (ErrNewerTruncation). A caller
// keeps the undo entry and shows the message.
var ErrUndoNotYet = errors.New("event: the undo is not possible yet")

// undoNotYet are the refusals of an undo that a later restore can lift
// (ErrUndoNotYet).
var undoNotYet = []error{ErrNewerTruncation}

// undoLater is a refusal of an undo that a later restore can lift. Its text
// is the text of the cause, and it matches ErrUndoNotYet and the cause.
type undoLater struct{ cause error }

func (e undoLater) Error() string        { return e.cause.Error() }
func (e undoLater) Unwrap() error        { return e.cause }
func (e undoLater) Is(target error) bool { return target == ErrUndoNotYet }

// classifyUndo wraps a permanent refusal of an undo with ErrUndoNotPossible,
// and a refusal that a later restore can lift with ErrUndoNotYet. It returns a
// transient error and nil as they are.
func classifyUndo(err error) error {
	if err == nil || errors.Is(err, ErrUndoNotPossible) || errors.Is(err, ErrUndoNotYet) {
		return err
	}
	for _, later := range undoNotYet {
		if errors.Is(err, later) {
			return undoLater{cause: err}
		}
	}
	for _, refusal := range undoRefusals {
		if errors.Is(err, refusal) {
			return undoRefusal{cause: err}
		}
	}
	return err
}

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
	Kind  UndoKind
	UID   string
	Label string
	// DeleteToken is the delete token that the delete wrote on its rows
	// (events.delete_token, migration 057). A row in the trash that no log
	// names is still this delete's while its token matches. A later delete,
	// a pull included, writes a newer token (see identityRefusalTx).
	DeleteToken int64

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

	// SeriesLogID is the series-delete log of a series delete
	// (UndoKindSeries). It is the identity of the delete for the undo. A
	// delete of orphan rows checks it through identityRefusalTx. A delete of
	// a master restores that log only, and the rows with its token.
	SeriesLogID int64

	// ExdateLogID and ExdateDeleteSeq name the entry of the delete in its
	// log. For an occurrence delete (UndoKindSingle with RecurrenceID), the
	// log is the occurrence log of the master. For a delete by ID of an
	// orphan row, the log is the orphan log of the row. The entry is the
	// identity of the delete for the undo (see identityRefusalTx).
	// ExdateLogID 0 means that the delete wrote no log: the master was in
	// the trash. The undo then brings back the rows alone.
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
		DeleteToken:     deleted.DeleteToken,
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
		DeleteToken:     d.Token,
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
	master, postUpdated, token, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
	if err != nil {
		return UndoMeta{}, err
	}

	return UndoMeta{
		Kind:                UndoKindFromInstance,
		UID:                 uid,
		Label:               master.Title,
		DeleteToken:         token,
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
		DeleteToken:   d.Token,
		CalendarID:    d.CalendarID,
		MasterID:      d.Master.ID,
		RowIDs:        d.Hidden,
		SeriesLogID:   d.LogID,
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
// RRULE back to the pre-truncation value in the same transaction. A
// permanent refusal matches ErrUndoNotPossible (classifyUndo). Any other
// error is transient.
func (s *Service) RestoreUndo(ctx context.Context, meta UndoMeta) error {
	return classifyUndo(s.restoreUndo(ctx, meta))
}

func (s *Service) restoreUndo(ctx context.Context, meta UndoMeta) error {
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
		return undoRefusal{cause: fmt.Errorf("unknown undo kind %d", meta.Kind)}
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
	calendarID, holdsMaster, logID, err := restoreCandidate(masters, logs)
	if err != nil {
		return 0, err
	}
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return 0, err
	}
	n, ids, err := restoreSeriesLogTx(ctx, qtx, calendarID, uid, holdsMaster, 0, logID)
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
// that an occurrence log names by ID had its own delete, and it stays in the
// trash. A pruned marker names no row.
// An example is a delete by ID of an orphan row. Of the other rows, one
// orphan delete hides the rows of one calendar with one delete token. The
// restore takes the calendar of the newest delete (by time, then by token),
// and brings back the rows of that delete (sameStatement). Only a tie of two calendars gives
// ErrAmbiguousUID. No row gives ErrNotDeleted.
func restoreOrphanDeleteTx(ctx context.Context, qtx *storage.Queries, uid string, trashed []storage.Event) (int64, error) {
	// The occurrence logs name the rows of their deletes by ID. A pruned
	// marker is no delete of the user, so it names no row here.
	logged := make(map[int64]loggedRows)
	var candidates []storage.Event
	for _, r := range trashed {
		l, read := logged[r.CalendarID]
		if !read {
			var err error
			if l, err = loggedRowIDsTx(ctx, qtx, r.CalendarID, uid); err != nil {
				return 0, err
			}
			logged[r.CalendarID] = l
		}
		if l.ids[r.ID] {
			continue
		}
		candidates = append(candidates, r)
	}
	if len(candidates) == 0 {
		return 0, ErrNotDeleted
	}
	// The newest delete names one calendar. The order is the time of the
	// trash, then the token. The token counter grows with each delete, so it
	// orders the deletes of one second. The order is total, so the result
	// does not depend on the order of the rows.
	newer := func(a, b storage.Event) bool {
		if *a.DeletedAt != *b.DeletedAt {
			return *a.DeletedAt > *b.DeletedAt
		}
		return a.DeleteToken > b.DeleteToken
	}
	newest := candidates[0]
	for _, r := range candidates {
		if newer(r, newest) {
			newest = r
		}
	}
	calendarID := newest.CalendarID
	// Only a tie names two calendars: a row of another calendar from the
	// same statement as the newest delete, by the rule of sameStatement.
	for _, r := range candidates {
		if r.CalendarID != calendarID && sameStatement(r, newest) {
			return 0, ErrAmbiguousUID
		}
	}
	if err := ensureSeriesWritableIn(ctx, qtx, calendarID, uid); err != nil {
		return 0, err
	}
	// The delete token groups the rows of that delete: one statement hid
	// them. A row from before migration 057 has token 0, and the trash time
	// groups it, to the second.
	ids := make([]int64, 0, len(candidates))
	for _, r := range candidates {
		if sameStatement(r, newest) {
			ids = append(ids, r.ID)
		}
	}
	if _, err := restoreEventsByIDsTx(ctx, qtx, ids); err != nil {
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
//
// logID names the log that the restore reverses. A calendar with a master in
// the trash gives 0: the restore takes the log of that master
// (masterSeriesLogTx). Else it is the newest of the logs that a restore can
// still reverse. An empty log never shadows an older one.
func restoreCandidate(masters []storage.Event, logs []storage.EventSeriesDelete) (calendarID int64, holdsMaster bool, logID int64, err error) {
	holds := make(map[int64]bool, len(masters))
	trashedMaster := make(map[int64]bool, len(masters))
	newestLog := make(map[int64]int64, len(logs))
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
		if l.ID > newestLog[l.CalendarID] {
			newestLog[l.CalendarID] = l.ID
		}
	}
	for _, m := range masters {
		holds[m.CalendarID] = true
		if m.DeletedAt != nil {
			trashedMaster[m.CalendarID] = true
			add(m.CalendarID)
		}
	}
	switch len(candidates) {
	case 0:
		return 0, false, 0, ErrNotDeleted
	case 1:
		cal := candidates[0]
		if trashedMaster[cal] {
			return cal, holds[cal], 0, nil
		}
		return cal, holds[cal], newestLog[cal], nil
	}
	return 0, false, 0, ErrAmbiguousUID
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
	n, ids, err := restoreSeriesLogTx(ctx, qtx, r.CalendarID, r.Uid, true, 0, 0)
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
// trash. The rows then join the log row with the token of that master, in the
// calendar of the master. Only a delete of orphan rows beside a master in the
// trash merges, because that master still needs its log. With no log, the
// merge writes the log of the master: the rows that went with the master
// (sameDeleteRowsTx), the orphan rows, and had_master 1. A restore of the
// master brings back the rows with its token. The orphan rows keep the token
// of their own delete, and they stay as an orphan log. A delete of orphan rows with no
// master writes a row of its own, so an earlier orphan delete keeps its
// record and stays restorable. token is the delete token of the master that
// the delete hides; the log keeps it, so the revive of that master consumes
// the log. It returns the ID of the log row.
func recordSeriesDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, hidden []int64, into *storage.Event, hadMaster bool, token int64) (int64, error) {
	if into != nil {
		calendarID = into.CalendarID
		// The log of a master in the trash carries the token of that master,
		// so the revive of the master consumes it.
		token = into.DeleteToken
		// The rows join the log of the master (masterSeriesLogTx), not the
		// newest log of the calendar: that can be the orphan log of another
		// delete.
		log, err := masterSeriesLogTx(ctx, qtx, *into)
		found, err := storage.Found(err)
		if err != nil {
			return 0, fmt.Errorf("get series delete log: %w", err)
		}
		if found {
			// The orphan rows join the log of the master in the trash, and
			// the log keeps its had_master.
			if hidden, err = unionCommaList(hidden, log.HiddenIds, parseRowID); err != nil {
				return 0, err
			}
			if err := qtx.UpdateEventSeriesDeleteHiddenIds(ctx, storage.UpdateEventSeriesDeleteHiddenIdsParams{HiddenIds: formatRowIDs(hidden), ID: log.ID}); err != nil {
				return 0, fmt.Errorf("merge series delete: %w", err)
			}
			return log.ID, nil
		}
		// No log: a delete from before migration 057, or the series
		// delete of a pull. The rows that went with the master join too.
		same, err := sameDeleteRowsTx(ctx, qtx, *into)
		if err != nil {
			return 0, err
		}
		hidden = append(same, hidden...)
		hadMaster = true
	}
	flag := int64(0)
	if hadMaster {
		flag = 1
	}
	id, err := qtx.RecordEventSeriesDelete(ctx, storage.RecordEventSeriesDeleteParams{
		CalendarID: calendarID, Uid: uid, HiddenIds: formatRowIDs(hidden), HadMaster: flag, DeleteToken: token,
	})
	if err != nil {
		return 0, fmt.Errorf("record series delete: %w", err)
	}
	return id, nil
}

// restoreSeriesLogAndSyncTx is restoreSeriesLogTx plus the sync state of the
// rows that came back, in the transaction of qtx.
func restoreSeriesLogAndSyncTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, requireMaster bool, token, logID int64) (int64, error) {
	n, ids, err := restoreSeriesLogTx(ctx, qtx, calendarID, uid, requireMaster, token, logID)
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
func restoreSeriesLogTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string, requireMaster bool, token, logID int64) (int64, []int64, error) {
	var master storage.Event
	if requireMaster {
		var err error
		master, err = qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: calendarID, Uid: uid,
		})
		if err != nil {
			return 0, nil, fmt.Errorf("get master: %w", err)
		}
		// The undo of a series delete restores the master only while the
		// master carries the token of that delete (tokenSplitTx).
		split, err := tokenSplitTx(ctx, qtx, []int64{master.ID}, token)
		if err != nil {
			return 0, nil, err
		}
		if _, err := split.refuseOther(); err != nil {
			return 0, nil, err
		}
	}
	// The log comes first: the revival of the master fires the trigger
	// that drops the log of its calendar (migration 057). The log row
	// goes before the restore, so the drop of the restored rows from the
	// other logs does not rewrite it either.
	// An undo names its log (logID), and so does RestoreByUID: the log of
	// its candidate choice (restoreCandidate). A restore of a master in the
	// trash takes the log of the delete that hid the master
	// (masterSeriesLogTx). With neither, no log applies: the restore never
	// takes the newest log of the calendar, which can belong to another
	// delete.
	var log storage.EventSeriesDelete
	var err error
	switch {
	case logID != 0:
		log, err = qtx.GetEventSeriesDeleteByID(ctx, logID)
	case requireMaster && master.DeletedAt != nil:
		log, err = masterSeriesLogTx(ctx, qtx, master)
	default:
		err = sql.ErrNoRows
	}
	logged, err := storage.Found(err)
	if err != nil {
		return 0, nil, fmt.Errorf("get series delete log: %w", err)
	}
	if logged && (log.CalendarID != calendarID || log.Uid != uid) {
		logged = false
	}
	if logID != 0 && !logged && !requireMaster {
		// The master is gone, and the purge of the master took the log of
		// the undo with it. No other log of the calendar belongs to this
		// delete.
		return 0, nil, ErrMasterPurged
	}
	var ids []int64
	switch {
	case logged:
		ids, err = parseRowIDs(log.HiddenIds)
		if err != nil {
			return 0, nil, err
		}
		// The undo brings back the rows of its own delete only: the rows
		// with its token. A delete of orphan rows can merge its rows into
		// this log with another token. Those rows stay in the log, which
		// becomes an orphan log, so a restore by UID or their own undo
		// brings them back.
		// A user restore of a master in the trash (no token of an undo)
		// takes the rows with the token of that master. The rows that a
		// delete of orphan rows merged in keep their own token. They stay
		// in the log as an orphan log, as for the undo.
		rowToken := token
		if rowToken == 0 && requireMaster && master.DeletedAt != nil {
			rowToken = master.DeleteToken
		}
		// A master from before the token (token 0) has rows with token 0.
		// A merged row came from a later delete, with a token: it stays.
		legacyMaster := rowToken == 0 && requireMaster && master.DeletedAt != nil
		var rest []int64
		if rowToken != 0 || legacyMaster {
			all := ids
			if legacyMaster {
				if ids, err = legacyRowsTx(ctx, qtx, all); err != nil {
					return 0, nil, err
				}
			} else {
				split, err := tokenSplitTx(ctx, qtx, all, rowToken)
				if err != nil {
					return 0, nil, err
				}
				ids = split.matchIDs()
			}
			own := make(map[int64]struct{}, len(ids))
			for _, id := range ids {
				own[id] = struct{}{}
			}
			for _, id := range all {
				if _, ok := own[id]; !ok {
					rest = append(rest, id)
				}
			}
		}
		if len(rest) > 0 {
			if err := qtx.UpdateEventSeriesDeleteAsOrphanLog(ctx, storage.UpdateEventSeriesDeleteAsOrphanLogParams{HiddenIds: formatRowIDs(rest), ID: log.ID}); err != nil {
				return 0, nil, fmt.Errorf("keep the merged rows in the log: %w", err)
			}
		} else if err := qtx.DeleteEventSeriesDeleteByID(ctx, log.ID); err != nil {
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

// legacyRowsTx returns the rows of ids in the trash with no delete token: the
// rows of a delete from before the token (migration 057).
func legacyRowsTx(ctx context.Context, qtx *storage.Queries, ids []int64) ([]int64, error) {
	states, err := rowStatesTx(ctx, qtx, ids)
	if err != nil {
		return nil, err
	}
	var out []int64
	for _, st := range states {
		if st.DeletedAt != nil && st.DeleteToken == 0 {
			out = append(out, st.ID)
		}
	}
	return out, nil
}

// masterSeriesLogTx returns the series-delete log of the delete that hid
// master. That log carries the delete token of the master. A master from
// before the token (token 0) takes the newest log with a master. An orphan
// log of another delete never matches, so the restore of the master does not
// bring back its rows. With no match, it returns sql.ErrNoRows, and the
// caller pairs the rows of the master statement (sameDeleteRowsTx).
func masterSeriesLogTx(ctx context.Context, qtx *storage.Queries, master storage.Event) (storage.EventSeriesDelete, error) {
	logs, err := qtx.ListEventSeriesDeletesByCalendarUID(ctx, storage.ListEventSeriesDeletesByCalendarUIDParams{
		CalendarID: master.CalendarID, Uid: master.Uid,
	})
	if err != nil {
		return storage.EventSeriesDelete{}, err
	}
	for _, l := range slices.Backward(logs) {
		if l.DeleteToken == master.DeleteToken && (master.DeleteToken != 0 || l.HadMaster == 1) {
			return l, nil
		}
	}
	return storage.EventSeriesDelete{}, sql.ErrNoRows
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
	// The logs of a kept master stay with it.
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
	rows, err := qtx.ListTrashedMastersBefore(ctx, &cutoff)
	if err != nil {
		return PurgeReport{}, fmt.Errorf("list trashed masters: %w", err)
	}
	masters := make([]seriesRef, 0, len(rows))
	for _, m := range rows {
		masters = append(masters, seriesRef{ID: m.ID, CalendarID: m.CalendarID, UID: m.Uid})
	}
	overrides, err := seriesOverridesTx(ctx, qtx, masters)
	if err != nil {
		return PurgeReport{}, err
	}
	var report PurgeReport
	var purge, blocked []seriesRef
	for _, m := range masters {
		if overrides.hasLive(m) {
			blocked = append(blocked, m)
			report.Skipped = append(report.Skipped, SkippedMaster(m))
			continue
		}
		purge = append(purge, m)
	}
	// One read of the series-delete logs serves the purge set, the kept
	// series, and the young merged rows.
	logs, err := seriesLogsOfMastersTx(ctx, qtx, append(slices.Clone(purge), blocked...))
	if err != nil {
		return PurgeReport{}, err
	}
	ids, err := seriesPurgeSetTx(ctx, qtx, purge, overrides, logs)
	if err != nil {
		return PurgeReport{}, err
	}
	// The rows of a kept series stay, whatever their age.
	keep := make(map[int64]bool, len(blocked))
	for _, m := range blocked {
		keep[m.ID] = true
	}
	if len(blocked) > 0 {
		// The kept series keeps the rows of the view and the rows of its log.
		kept, err := seriesPurgeSetTx(ctx, qtx, blocked, overrides, logs)
		if err != nil {
			return PurgeReport{}, err
		}
		for _, id := range kept {
			keep[id] = true
		}
	}
	// A delete of orphan rows can merge its rows into the log of a purged
	// master. Such a row keeps the time and the token of its own delete. A
	// row younger than the cutoff stays in the trash, and its log stays as
	// an orphan log.
	young, err := youngLoggedRowsTx(ctx, qtx, purge, logs, cutoff)
	if err != nil {
		return PurgeReport{}, err
	}
	for _, y := range young {
		for _, id := range y.rows {
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
	purgeIDs := refIDs(purge)
	// The occurrence and truncation logs of a purged master go. Its
	// series-delete logs go too, except a log with young merged rows
	// (settleSeriesLogsOfPurgeTx).
	if err := dropMasterLogsTx(ctx, qtx, purgeIDs, false); err != nil {
		return PurgeReport{}, err
	}
	if err := settleSeriesLogsOfPurgeTx(ctx, qtx, purge, logs, young); err != nil {
		return PurgeReport{}, err
	}
	n, err := purgeIDsTx(ctx, qtx, append(ids, purgeIDs...))
	if err != nil {
		return PurgeReport{}, err
	}
	report.Purged = int(n)
	return report, nil
}

// seriesLogsOfMastersTx reads the series-delete logs of the calendars of
// masters once, each with the ID of its master.
func seriesLogsOfMastersTx(ctx context.Context, qtx *storage.Queries, masters []seriesRef) ([]storage.ListSeriesLogsOfMastersRow, error) {
	var logs []storage.ListSeriesLogsOfMastersRow
	for _, chunk := range storage.ChunkSlice(refIDs(masters), idChunk) {
		rows, err := qtx.ListSeriesLogsOfMasters(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("list series logs: %w", err)
		}
		logs = append(logs, rows...)
	}
	return logs, nil
}

// youngRows are the young merged rows of one series-delete log, and the
// earliest time of those rows.
type youngRows struct {
	rows     []int64
	earliest string
}

// settleSeriesLogsOfPurgeTx settles the series-delete logs of the purged
// masters. A log with young merged rows (young, by log ID) stays in place as
// an orphan log with only those rows. Its ID stays, so an undo that names it
// still finds it. Its time becomes the earliest time of the rows. Every
// other log of a purged master goes.
func settleSeriesLogsOfPurgeTx(ctx context.Context, qtx *storage.Queries, purge []seriesRef, logs []storage.ListSeriesLogsOfMastersRow, young map[int64]youngRows) error {
	purged := make(map[int64]bool, len(purge))
	for _, m := range purge {
		purged[m.ID] = true
	}
	var drop []int64
	for _, l := range logs {
		if !purged[l.MasterID] {
			continue
		}
		y, ok := young[l.ID]
		if !ok {
			drop = append(drop, l.ID)
			continue
		}
		if err := qtx.UpdateEventSeriesDeleteAsAgedOrphanLog(ctx, storage.UpdateEventSeriesDeleteAsAgedOrphanLogParams{
			HiddenIds: formatRowIDs(y.rows), DeletedAt: y.earliest, ID: l.ID,
		}); err != nil {
			return fmt.Errorf("keep the young rows in an orphan log: %w", err)
		}
	}
	for _, chunk := range storage.ChunkSlice(drop, idChunk) {
		if _, err := qtx.DeleteEventSeriesDeletesByIDs(ctx, chunk); err != nil {
			return fmt.Errorf("drop series logs: %w", err)
		}
	}
	return nil
}

// youngLoggedRowsTx returns the young merged rows of the series-delete logs
// of masters, by log ID. Such a row is in the trash, and the log of a master
// names it. It went to the trash at or after cutoff. A delete of orphan rows
// added it to the log, so its delete token differs from the token of the
// master. The rows of the series delete itself go with the master, whatever
// their time. logs are the logs of masters (seriesLogsOfMastersTx).
func youngLoggedRowsTx(ctx context.Context, qtx *storage.Queries, masters []seriesRef, logs []storage.ListSeriesLogsOfMastersRow, cutoff string) (map[int64]youngRows, error) {
	of := make(map[int64]bool, len(masters))
	for _, m := range masters {
		of[m.ID] = true
	}
	named := make(map[int64][]int64)
	logMaster := make(map[int64]int64)
	var all []int64
	for _, l := range logs {
		if !of[l.MasterID] {
			continue
		}
		rows, err := parseRowIDs(l.HiddenIds)
		if err != nil {
			return nil, err
		}
		named[l.ID] = rows
		logMaster[l.ID] = l.MasterID
		all = append(all, rows...)
	}
	if len(all) == 0 {
		return nil, nil
	}
	states, err := rowStatesTx(ctx, qtx, append(all, refIDs(masters)...))
	if err != nil {
		return nil, err
	}
	byID := make(map[int64]storage.ListEventRowStatesByIDsRow, len(states))
	for _, st := range states {
		byID[st.ID] = st
	}
	out := make(map[int64]youngRows)
	for logID, rows := range named {
		token := byID[logMaster[logID]].DeleteToken
		var y youngRows
		for _, id := range rows {
			st, ok := byID[id]
			if !ok || st.DeletedAt == nil || *st.DeletedAt < cutoff || st.DeleteToken == token {
				continue
			}
			y.rows = append(y.rows, id)
			if y.earliest == "" || *st.DeletedAt < y.earliest {
				y.earliest = *st.DeletedAt
			}
		}
		if len(y.rows) > 0 {
			out[logID] = y
		}
	}
	return out, nil
}

// seriesRef names the master row of one series.
type seriesRef struct {
	ID         int64
	CalendarID int64
	UID        string
}

// refIDs returns the master IDs of refs.
func refIDs(refs []seriesRef) []int64 {
	ids := make([]int64, 0, len(refs))
	for _, r := range refs {
		ids = append(ids, r.ID)
	}
	return ids
}

// seriesOverrides holds the override rows of a set of UIDs, by the series
// that owns each row (the view event_series_rows).
type seriesOverrides map[string][]storage.ListSeriesOverridesOfUIDsRow

// hasLive reports whether the series of m owns a live override row.
func (o seriesOverrides) hasLive(m seriesRef) bool {
	return slices.ContainsFunc(o[seriesKeyOf(m.CalendarID, m.UID)], func(r storage.ListSeriesOverridesOfUIDsRow) bool { return r.DeletedAt == nil })
}

// liveIDs returns the IDs of the live override rows that the series of m
// owns, in ID order.
func (o seriesOverrides) liveIDs(m seriesRef) []int64 {
	var ids []int64
	for _, r := range o[seriesKeyOf(m.CalendarID, m.UID)] {
		if r.DeletedAt == nil {
			ids = append(ids, r.ID)
		}
	}
	slices.Sort(ids)
	return ids
}

// seriesOverridesTx reads the override rows of the UIDs of masters from the
// view, in chunks of bound UIDs. A bound UID list lets SQLite filter each arm
// of the view by the index on uid. A filter that joins the view to the
// masters scans the events table instead.
func seriesOverridesTx(ctx context.Context, qtx *storage.Queries, masters []seriesRef) (seriesOverrides, error) {
	uids := make([]string, 0, len(masters))
	seen := make(map[string]bool, len(masters))
	for _, m := range masters {
		if !seen[m.UID] {
			seen[m.UID] = true
			uids = append(uids, m.UID)
		}
	}
	out := make(seriesOverrides)
	for _, chunk := range storage.ChunkSlice(uids, idChunk) {
		rows, err := qtx.ListSeriesOverridesOfUIDs(ctx, chunk)
		if err != nil {
			return nil, fmt.Errorf("list series overrides: %w", err)
		}
		for _, r := range rows {
			key := seriesKeyOf(r.OwnerCalendarID, r.Uid)
			out[key] = append(out[key], r)
		}
	}
	return out, nil
}

// seriesPurgeSetTx lists the override rows in the trash that the purge of
// masters takes along:
//   - the rows that each series owns in the view (overrides), in its
//     calendar and in other calendars;
//   - the rows in the trash that the series-delete log of each master names.
//     Another series can own such a row in the view now. A log names a row
//     only while the row stays in the trash from that delete. The revive
//     trigger of migration 057 removes a row that comes back by any path. A
//     later delete of the row is then not in the log.
//
// PurgeByID and PurgeDeletedReport share it. overrides holds the rows of the
// view for the UIDs of masters (seriesOverridesTx), and all the series-delete
// logs of their calendars (seriesLogsOfMastersTx), read once.
func seriesPurgeSetTx(ctx context.Context, qtx *storage.Queries, masters []seriesRef, overrides seriesOverrides, all []storage.ListSeriesLogsOfMastersRow) ([]int64, error) {
	if len(masters) == 0 {
		return nil, nil
	}
	of := make(map[int64]bool, len(masters))
	for _, m := range masters {
		of[m.ID] = true
	}
	var logs []storage.ListSeriesLogsOfMastersRow
	for _, l := range all {
		if of[l.MasterID] {
			logs = append(logs, l)
		}
	}
	var ids []int64
	seen := make(map[int64]bool)
	for _, m := range masters {
		for _, r := range overrides[seriesKeyOf(m.CalendarID, m.UID)] {
			if r.DeletedAt != nil && !seen[r.ID] {
				seen[r.ID] = true
				ids = append(ids, r.ID)
			}
		}
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
	for _, chunk := range storage.ChunkSlice(named, idChunk) {
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

// dropMasterLogsTx drops the occurrence and truncation logs of the series of
// the masters of masterIDs. With includeSeries, it drops their series-delete
// logs too. The age purge settles those itself (settleSeriesLogsOfPurgeTx).
func dropMasterLogsTx(ctx context.Context, qtx *storage.Queries, masterIDs []int64, includeSeries bool) error {
	if len(masterIDs) == 0 {
		return nil
	}
	drops := []func(context.Context, []int64) error{qtx.DeleteExdateLogsOfMasters, qtx.DeleteTruncateLogsOfMasters}
	if includeSeries {
		drops = append(drops, qtx.DeleteSeriesLogsOfMasters)
	}
	for _, chunk := range storage.ChunkSlice(masterIDs, idChunk) {
		for _, drop := range drops {
			if err := drop(ctx, chunk); err != nil {
				return fmt.Errorf("drop master logs: %w", err)
			}
		}
	}
	return nil
}

// purgeIDsTx hard-deletes the rows in the trash of ids, in chunks under the
// parameter limit of SQLite. The purge trigger removes each row from the
// delete logs, and the occurrence logs settle (settlePurgedTx). It returns
// the count of purged rows.
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
	for _, chunk := range storage.ChunkSlice(unique, idChunk) {
		// The occurrences of the rows, read before the purge, so their
		// logs settle after it.
		rows, err := rowStatesTx(ctx, qtx, chunk)
		if err != nil {
			return 0, err
		}
		touched, err := purgedOccurrencesTx(ctx, qtx, rows)
		if err != nil {
			return 0, err
		}
		n, err := qtx.PurgeEventsByIDs(ctx, chunk)
		if err != nil {
			return 0, fmt.Errorf("purge rows: %w", err)
		}
		total += n
		if err := settlePurgedTx(ctx, qtx, touched); err != nil {
			return 0, err
		}
	}
	return total, nil
}

// idChunk is the size of one IN list of row IDs, under the parameter limit
// of SQLite.
const idChunk = 500

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
	// One read of the view gives the live rows and the purge set.
	ref := []seriesRef{{ID: master.ID, CalendarID: master.CalendarID, UID: master.Uid}}
	overrides, err := seriesOverridesTx(ctx, qtx, ref)
	if err != nil {
		return 0, err
	}
	if live := overrides.liveIDs(ref[0]); len(live) > 0 {
		return 0, &SeriesHasLiveOverridesError{IDs: live}
	}
	logs, err := seriesLogsOfMastersTx(ctx, qtx, ref)
	if err != nil {
		return 0, err
	}
	ids, err := seriesPurgeSetTx(ctx, qtx, ref, overrides, logs)
	if err != nil {
		return 0, err
	}
	if err := dropMasterLogsTx(ctx, qtx, []int64{master.ID}, true); err != nil {
		return 0, err
	}
	return purgeIDsTx(ctx, qtx, ids)
}

// PurgeByID hard-deletes a single soft-deleted row. Returns ErrNotDeleted if
// the row is live (or absent). Callers then cannot purge a live event with
// the wrong ID.
func (s *Service) PurgeByID(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// The read of the row and the purge share the transaction, so a restore
	// in between cannot make the purge take a live row. PurgeEventByID
	// refuses a live row too.
	r, err := qtx.GetEventIncludingDeleted(ctx, id)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get event: %w", err)
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		return ErrNotDeleted
	}
	// A purge of the local trash makes no server write, so it needs no write
	// guard: a read-only calendar can empty its trash too.
	// The rows of a master go before the master: the view names them only
	// while the master row exists.
	if r.RecurrenceID == "" {
		if _, err := purgeMasterRowsTx(ctx, qtx, r); err != nil {
			return err
		}
	}
	// The live master of an override, resolved before the purge: its
	// occurrence log settles after the purge.
	var touched []purgedOccurrence
	if r.RecurrenceID != "" {
		state := storage.ListEventRowStatesByIDsRow{ID: r.ID, CalendarID: r.CalendarID, Uid: r.Uid, RecurrenceID: r.RecurrenceID, DeletedAt: r.DeletedAt}
		if touched, err = purgedOccurrencesTx(ctx, qtx, []storage.ListEventRowStatesByIDsRow{state}); err != nil {
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
		// The purge trigger removed the row from the logs. The occurrence
		// log then settles: a log that hides nothing goes.
		if err := settlePurgedTx(ctx, qtx, touched); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// restoreSingle reverses one single-row delete from its record: the
// master that meta.MasterID names, and the override row that meta.RowID
// names. Undo data with no master and no row names no delete, so it gives
// ErrNotDeleted. One transaction holds the read of the master, the choice of
// the path, and the restore. A series delete between a read and the restore
// then cannot send the undo down the path of a live master.
func (s *Service) restoreSingle(ctx context.Context, meta UndoMeta) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := restoreSingleTx(ctx, s.q.WithTx(tx), meta); err != nil {
		return err
	}
	return tx.Commit()
}

// restoreSingleTx is the body of restoreSingle in the transaction of qtx.
func restoreSingleTx(ctx context.Context, qtx *storage.Queries, meta UndoMeta) error {
	calendarID, uid, recurrenceID, rowID := meta.CalendarID, meta.UID, meta.RecurrenceID, meta.RowID
	// An occurrence delete can hide more than one row: the row beside the
	// master and the legacy overrides of the occurrence. Older undo data
	// names one row in RowID.
	rowIDs := meta.RowIDs
	if len(rowIDs) == 0 && rowID != 0 {
		rowIDs = []int64{rowID}
	}
	r, err := undoMasterTx(ctx, qtx, meta)
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
		return restoreOverrideRowTx(ctx, qtx, calendarID, uid, recurrenceID, rowID, deleteIdentity{exdateLogID: meta.ExdateLogID, seq: meta.ExdateDeleteSeq, token: meta.DeleteToken})
	}
	if err != nil {
		return err
	}
	// The restore of a legacy override writes the EXDATE of its master,
	// so the calendar of the master takes the write too.
	if r.CalendarID != calendarID {
		if err := calendaraccess.EnsureWritable(ctx, qtx, r.CalendarID, "VEVENT"); err != nil {
			return err
		}
	}
	if r.DeletedAt != nil && recurrenceID != "" {
		// The master is in the trash. A delete by ID of a row beside the
		// trashed master wrote no log (ExdateLogID 0). The row was live a
		// moment before, so the undo brings back exactly that row.
		if meta.ExdateLogID == 0 {
			return restoreRowsBesideTrashedMasterTx(ctx, qtx, r, rowIDs, meta.DeleteToken)
		}
		// An occurrence delete of the live series came before the series
		// delete. Live rows beside a trashed master would block its purge.
		// The series restore brings back the series first.
		return ErrMasterInTrash
	}
	if r.DeletedAt == nil || *r.DeletedAt == "" {
		if recurrenceID != "" {
			return restoreEXDATEOnlyTx(ctx, qtx, r, uid, recurrenceID, rowIDs, deleteIdentity{exdateLogID: meta.ExdateLogID, seq: meta.ExdateDeleteSeq, token: meta.DeleteToken})
		}
		// The undo reverses a delete of the master row, and the master
		// is live: a restore from the trash came first. The undo has
		// nothing to do. It must not un-hide the other rows of the
		// series: an occurrence deleted on its own stays deleted
		// (issue #287).
		return ErrAlreadyRestored
	}
	// The token rule decides.
	split, err := tokenSplitTx(ctx, qtx, []int64{r.ID}, meta.DeleteToken)
	if err != nil {
		return err
	}
	trashed, err := split.refuseOther()
	if err != nil {
		return err
	}
	if len(trashed) == 0 {
		return ErrAlreadyRestored
	}
	if err := qtx.RestoreEvent(ctx, r.ID); err != nil {
		return err
	}
	if err := reconcileTx(ctx, qtx, r.CalendarID, r.Uid); err != nil {
		return err
	}
	return nil
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
	n, err := restoreEventsByIDsTx(ctx, qtx, ids)
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

// restoreOverrideRowTx un-hides the override row of an undo with no master,
// by its ID. logID and seq name the entry of the orphan log of the delete.
// identityRefusalTx decides whether the row comes back. The undo guards the
// calendar of the row, and then drops its own entry only: the entry of a
// newer delete of the occurrence stays.
func restoreOverrideRowTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string, rowID int64, id deleteIdentity) error {
	logID, seq := id.exdateLogID, id.seq
	restore, err := identityRefusalTx(ctx, qtx, uid, []int64{rowID}, id)
	if err != nil {
		return err
	}
	if _, err := restoreStatesTx(ctx, qtx, 0, restore); err != nil {
		return err
	}
	switch {
	case logID != 0:
		// The revive trigger rewrote the log, so the read comes after the
		// restore. The undo drops its own entry. With no entry left, the
		// log goes. A log that a trigger dropped leaves nothing to drop.
		log, found, err := occurrenceLogTx(ctx, qtx, calendarID, uid, recurrenceID)
		if err != nil {
			return err
		}
		if found && log.ID == logID {
			log = log.WithoutEntry(seq)
			if len(log.Entries) == 0 {
				if err := qtx.DeleteEventExdateDelete(ctx, log.ID); err != nil {
					return fmt.Errorf("delete orphan log: %w", err)
				}
			} else if _, err := occurrenceStoreTx(qtx, calendarID, uid, recurrenceID, false).SaveLog(ctx, log); err != nil {
				return fmt.Errorf("update orphan log: %w", err)
			}
		}
	default:
		holds, err := calendarHoldsMasterTx(ctx, qtx, calendarID, uid)
		if err != nil {
			return err
		}
		if err := dropOrphanDeleteLogTx(ctx, qtx, calendarID, uid, recurrenceID, holds); err != nil {
			return err
		}
	}
	if err := reconcileTx(ctx, qtx, calendarID, uid); err != nil {
		return err
	}
	return nil
}

// restoreRowByIDTx un-hides one row by ID: restoreHiddenRowsTx with one ID.
// The caller guards the calendar of the row. The revive trigger drops the
// row from the delete logs of its UID. A live row gives ErrAlreadyRestored,
// and no row gives ErrNotDeleted.
func restoreRowByIDTx(ctx context.Context, qtx *storage.Queries, r storage.Event) error {
	notBack, err := restoreHiddenRowsTx(ctx, qtx, r.CalendarID, []int64{r.ID})
	if err != nil {
		return err
	}
	return notBack
}

// undoMasterTx reads the master row that meta.MasterID names, live or in the
// trash. No master in the undo data, or a purged master, gives
// ErrNotDeleted. The series rule never picks another master.
func undoMasterTx(ctx context.Context, qtx *storage.Queries, meta UndoMeta) (storage.Event, error) {
	if meta.MasterID == 0 {
		return storage.Event{}, ErrNotDeleted
	}
	r, err := qtx.GetEventIncludingDeleted(ctx, meta.MasterID)
	if errors.Is(err, sql.ErrNoRows) {
		return storage.Event{}, ErrNotDeleted
	}
	return r, err
}

// restoreSeries reverses a series delete from its own record. With MasterID,
// it un-hides that master and the rows of its own series-delete log
// (meta.SeriesLogID) that carry its delete token. With RowIDs (a delete of
// orphan rows), it un-hides exactly those rows and drops them from the log.
// The rest of the log stays for the restore of the master. A purged master
// gives ErrMasterPurged. A master that a restore from the trash brought back
// first, or rows that are live again, give ErrNotDeleted. Undo
// data with neither field names no delete. An occurrence that the user
// deleted on its own before the series delete stays deleted (issue #287, see
// restoreSeriesLogTx).
func (s *Service) restoreSeries(ctx context.Context, meta UndoMeta) error {
	uid := meta.UID
	switch {
	case meta.MasterID != 0:
		// One transaction holds the read of the master, the choice of the
		// path, and the restore.
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin tx: %w", err)
		}
		defer tx.Rollback()
		if err := restoreSeriesOfMasterTx(ctx, s.q.WithTx(tx), meta); err != nil {
			return err
		}
		return tx.Commit()
	case len(meta.RowIDs) != 0:
		n, err := s.restoreRows(ctx, meta.CalendarID, uid, meta.RowIDs, deleteIdentity{seriesLogID: meta.SeriesLogID, token: meta.DeleteToken})
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

// restoreSeriesOfMasterTx is restoreSeries for undo data with a master, in
// the transaction of qtx. A master that a purge took gives ErrMasterPurged.
func restoreSeriesOfMasterTx(ctx context.Context, qtx *storage.Queries, meta UndoMeta) error {
	uid := meta.UID
	master, err := qtx.GetEventIncludingDeleted(ctx, meta.MasterID)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// The master row is gone. A purge drops the log with the master,
		// so only an older purge leaves a log that names the hidden rows
		// (an orphan tail). The undo then reverses that record, with the
		// refusal of restoreSeriesLogTx. The series rule stays out. With
		// nothing to reverse, the purge took the delete.
		n, err := restoreSeriesLogAndSyncTx(ctx, qtx, meta.CalendarID, uid, false, meta.DeleteToken, meta.SeriesLogID)
		if err != nil {
			return err
		}
		if n == 0 {
			return ErrMasterPurged
		}
		return nil
	case err != nil:
		return fmt.Errorf("get master: %w", err)
	case master.DeletedAt == nil:
		return ErrAlreadyRestored
	}
	_, err = restoreSeriesLogAndSyncTx(ctx, qtx, master.CalendarID, uid, true, meta.DeleteToken, meta.SeriesLogID)
	return err
}

// restoreRows is the undo of a series delete of orphan rows. ids are the rows
// of that delete, which belong to uid in the series of calendarID. id names
// the delete: its series-delete log and its delete token.
// identityRefusalTx decides which rows come back, and restoreStatesTx
// un-hides them in one guarded statement. The revive trigger drops them from
// the delete logs of the UID. It returns the number of rows that came back.
func (s *Service) restoreRows(ctx context.Context, calendarID int64, uid string, ids []int64, id deleteIdentity) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	restore, err := identityRefusalTx(ctx, qtx, uid, ids, id)
	if err != nil {
		return 0, err
	}
	if ids, err = restoreStatesTx(ctx, qtx, calendarID, restore); err != nil {
		return 0, err
	}
	n := int64(len(ids))
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
		// The rule of the undo decides, with the zero identity: a delete
		// beside a trashed master wrote no log. A log that names the row
		// means that the series restore brings it back.
		if _, err := identityRefusalTx(ctx, qtx, r.Uid, []int64{r.ID}, deleteIdentity{}); err != nil {
			if errors.Is(err, ErrDeletedAgain) {
				return ErrMasterInTrash
			}
			return err
		}
		if err := restoreRowByIDTx(ctx, qtx, r); err != nil {
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
	if err := restoreRowByIDTx(ctx, qtx, r); err != nil {
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

// restoreRowsBesideTrashedMasterTx is the undo of a delete by ID of a row that
// was live beside the trashed master. That delete wrote no log, so
// identityRefusalTx decides with the zero identity. The undo writes no
// EXDATE, no log, and no sync state: the rows stay local until the master
// comes back.
func restoreRowsBesideTrashedMasterTx(ctx context.Context, qtx *storage.Queries, master storage.Event, rowIDs []int64, token int64) error {
	restore, err := identityRefusalTx(ctx, qtx, master.Uid, rowIDs, deleteIdentity{token: token})
	if err != nil {
		return err
	}
	if _, err := restoreStatesTx(ctx, qtx, master.CalendarID, restore); err != nil {
		return err
	}
	return nil
}

// restoreEXDATEOnlyTx reverses an occurrence delete of the live series of
// master for the undo. The caller read master in the transaction of qtx. rowIDs are the rows that the delete hid. logID and
// seq name the entry of the delete in the occurrence log
// (UndoMeta.ExdateLogID and UndoMeta.ExdateDeleteSeq). The steps:
//  1. A master in the trash gives ErrMasterInTrash.
//  2. A delete that hid rows: identityRefusalTx decides which rows come
//     back. logID 0 is the zero identity: the delete wrote no log, because
//     the master was in the trash then.
//  3. A delete that hid no row: its entry must still be in the log. Another
//     log, or no entry, gives ErrAlreadyRestored.
//  4. The entry goes, and the log settles (softdelete.SettleOccurrence). A
//     live override at the occurrence takes the EXDATE away. The entries of
//     other deletes keep their rows hidden.
func restoreEXDATEOnlyTx(ctx context.Context, qtx *storage.Queries, master storage.Event, uid, recurrenceID string, rowIDs []int64, id deleteIdentity) error {
	logID, seq := id.exdateLogID, id.seq
	// Live rows beside a master in the trash would block its purge. The
	// caller read master in the same transaction.
	if master.DeletedAt != nil {
		return ErrMasterInTrash
	}
	if len(rowIDs) == 0 {
		log, found, err := occurrenceLogTx(ctx, qtx, master.CalendarID, uid, recurrenceID)
		if err != nil {
			return err
		}
		if _, ok := log.Entry(seq); !found || log.ID != logID || !ok {
			return ErrAlreadyRestored
		}
	}
	restore, err := identityRefusalTx(ctx, qtx, uid, rowIDs, id)
	if err != nil {
		return err
	}
	restored, err := restoreStatesTx(ctx, qtx, master.CalendarID, restore)
	if err != nil {
		return err
	}
	if err := settleOccurrenceTx(ctx, qtx, master, recurrenceID, func(l softdelete.OccurrenceLog) softdelete.OccurrenceLog {
		if logID != 0 && l.ID == logID {
			l = l.WithoutEntry(seq)
		}
		return l.WithoutRows(restored)
	}); err != nil {
		return err
	}
	if err := reconcileTx(ctx, qtx, master.CalendarID, uid); err != nil {
		return err
	}
	return nil
}

// sameStatement reports whether the soft-delete statement that hid master hid
// r too. The delete token pairs the rows. A row from before migration 057
// has token 0, and the time of the trash pairs it, to the second.
func sameStatement(r, master storage.Event) bool {
	if master.DeleteToken != 0 {
		return r.DeleteToken == master.DeleteToken
	}
	return r.DeleteToken == 0 && *r.DeletedAt == *master.DeletedAt
}

// sameDeleteRowsTx lists the override rows of the series of master that
// went to the trash in the same statement as master. Two deletes leave no
// log: a series delete from before the series-delete log (migration 057),
// and the series delete of a pull. Each hides the master and its
// overrides in one statement, so the rows share deleted_at to the second. A
// row that an occurrence log or a truncation log names went in a delete of
// its own, and it stays in the trash. The occurrence log names its rows by
// ID, so another row of the same occurrence still comes back.
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
	// An occurrence log names the rows of its deletes by ID. A pruned marker
	// names no row, so it keeps the match by recurrence ID: the pull hid
	// that row.
	logged, err := loggedRowIDsTx(ctx, qtx, master.CalendarID, master.Uid)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range trashed {
		if r.RecurrenceID == "" || r.DeletedAt == nil || !sameStatement(r, master) || truncated(r) || logged.ids[r.ID] || logged.pruned[r.RecurrenceID] {
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
// undo path. It routes the reversal through restoreTruncation, the same
// provenance-aware path the trash view uses. The transaction of the restore
// does three things. It finds the master of the series. It applies the
// stale-master guard: an external edit after the truncation refuses the
// undo. It reads the log row by the cutoff and the token, because a second
// truncation at the same cutoff has its own row.
//
// That restores the RRULE, the trimmed RDATEs (issue #490), and only the
// overrides the truncation itself hid (issue #491, the #287 class on undo).
// The truncate-log row is consumed. No phantom "Series tail" trash entry
// then lingers for the now-live series.
func (s *Service) restoreFromInstance(ctx context.Context, meta UndoMeta) error {
	return s.restoreTruncation(ctx, truncationKey{
		calendarID:          meta.CalendarID,
		uid:                 meta.UID,
		cutoff:              meta.CutoffTime.UTC().Format(time.RFC3339),
		token:               meta.DeleteToken,
		masterUpdatedBefore: meta.MasterUpdatedBefore,
	})
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
	// DeleteToken is the delete token that the delete wrote on the row.
	DeleteToken int64
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
		token, err := qtx.NextEventDeleteToken(ctx)
		if err != nil {
			return fmt.Errorf("delete token: %w", err)
		}
		if err := qtx.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: id, DeleteToken: token}); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		if out != nil {
			out.DeleteToken = token
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
			logID, seq, err := recordOrphanDeleteTx(ctx, qtx, evt.CalendarID, evt.UID, evt.RecurrenceID, evt.ID)
			if err != nil {
				return err
			}
			if out != nil {
				out.ExdateLogID, out.ExdateDeleteSeq = logID, seq
			}
		}
		token, tErr := qtx.NextEventDeleteToken(ctx)
		if tErr != nil {
			return fmt.Errorf("delete token: %w", tErr)
		}
		if err := qtx.SoftDeleteEvent(ctx, storage.SoftDeleteEventParams{ID: id, DeleteToken: token}); err != nil {
			return fmt.Errorf("soft-delete event: %w", err)
		}
		if out != nil {
			out.DeleteToken = token
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
		// committing a change that is never pushed (issue #107). The mark
		// names the deleted row. When no series owns the row, the mark then
		// flags no other live override of the UID (issue #862).
		if err := storage.MarkEventRowDirty(ctx, tx, evt.CalendarID, evt.UID, evt.ID); err != nil {
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
// orphan row: the UID has no master row, live or in the trash. The entry
// names the row, so a restore by UID leaves the row to its own restore. The
// log owns no EXDATE. A master would hold the record of an occurrence delete.
func recordOrphanDeleteTx(ctx context.Context, qtx *storage.Queries, calendarID int64, uid, recurrenceID string, rowID int64) (logID, seq int64, err error) {
	log, _, err := occurrenceLogTx(ctx, qtx, calendarID, uid, recurrenceID)
	if err != nil {
		return 0, 0, err
	}
	log.LastSeq++
	log.Entries = append(log.Entries, softdelete.OccurrenceEntry{Seq: log.LastSeq, Rows: []int64{rowID}})
	log.OwnsExdate = false
	id, err := occurrenceStoreTx(qtx, calendarID, uid, recurrenceID, true).SaveLog(ctx, log)
	if err != nil {
		return 0, 0, fmt.Errorf("record orphan delete: %w", err)
	}
	return id, log.LastSeq, nil
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
	// Token is the delete token of the rows, or 0 with no row.
	Token int64
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
		hidden = append(hidden, o.ID)
	}
	// One statement hides the rows with one delete token. The undo keeps it.
	var token int64
	if len(hidden) > 0 {
		if token, err = qtx.NextEventDeleteToken(ctx); err != nil {
			return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("delete token: %w", err)
		}
		if err := softDeleteEventsByIDsTx(ctx, qtx, hidden, token); err != nil {
			return storage.Event{}, occurrenceDeletion{}, fmt.Errorf("soft-delete overrides: %w", err)
		}
	}

	// The log makes the delete visible in the trash. It names the hidden
	// rows, so a restore of the occurrence from the trash un-hides them. No
	// live override remains, so the delete adds the EXDATE, unless the log
	// owns one already (softdelete.RecordOccurrenceDelete).
	d := occurrenceDeletion{Rows: hidden, Token: token}
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
	_, _, _, err := s.deleteFromInstance(ctx, calendarID, uid, instanceTime)
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
func softDeleteOverridesAndRecordTruncation(ctx context.Context, qtx *storage.Queries, master storage.Event, instanceTime time.Time, prevRRule string) (int64, error) {
	uid := master.Uid
	cutoff := instanceTime.UTC().Format(time.RFC3339)

	removedRDates, err := trimMasterRDatesAtOrAfter(ctx, qtx, master, instanceTime)
	if err != nil {
		return 0, err
	}

	// The overrides of the series in the calendar of the master. The rows
	// of a master in another calendar stay (issue #842).
	hidden, err := qtx.ListLiveSeriesOverridesAtOrAfter(ctx, storage.ListLiveSeriesOverridesAtOrAfterParams{
		CalendarID:   master.CalendarID,
		Uid:          uid,
		RecurrenceID: cutoff,
	})
	if err != nil {
		return 0, fmt.Errorf("list future overrides: %w", err)
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
		return 0, err
	}
	// Each truncation gets a token, also with no row to hide. The token
	// names the log row of the truncation: one row per truncation. A second
	// truncation at the same cutoff then keeps its own rule and its rows.
	token, err := qtx.NextEventDeleteToken(ctx)
	if err != nil {
		return 0, fmt.Errorf("delete token: %w", err)
	}
	if len(rowIDs) > 0 {
		if err := softDeleteEventsByIDsTx(ctx, qtx, rowIDs, token); err != nil {
			return 0, fmt.Errorf("soft-delete future overrides: %w", err)
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
		DeleteToken:     token,
	}); err != nil {
		return 0, fmt.Errorf("record truncate: %w", err)
	}
	return token, nil
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
func (s *Service) deleteFromInstance(ctx context.Context, calendarID int64, uid string, instanceTime time.Time) (storage.Event, string, int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return storage.Event{}, "", 0, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the RRULE we truncate reflects
	// a concurrent writer's edits rather than a pre-transaction snapshot
	// (issue #116).
	master, err := findSeriesMasterTx(ctx, qtx, calendarID, uid)
	if err != nil {
		return storage.Event{}, "", 0, wrapMasterLookup(err)
	}
	if err := calendaraccess.EnsureWritable(ctx, qtx, master.CalendarID, "VEVENT"); err != nil {
		return storage.Event{}, "", 0, err
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
			return storage.Event{}, "", 0, fmt.Errorf("update rrule: %w", err)
		}
	}

	token, err := softDeleteOverridesAndRecordTruncation(ctx, qtx, master, instanceTime, prevRRule)
	if err != nil {
		return storage.Event{}, "", 0, err
	}

	// Read the master's updated_at back inside the transaction so the value we
	// return reflects exactly this truncation's write, with no chance of an
	// interleaved external edit in between.
	truncated, err := qtx.GetEvent(ctx, master.ID)
	if err != nil {
		return storage.Event{}, "", 0, fmt.Errorf("read back master: %w", err)
	}
	postUpdated := truncated.UpdatedAt

	// Mark the master dirty — its RRULE and RDATEs were modified — inside
	// the same transaction so a failed mark rolls the truncation back rather
	// than committing a change that is never pushed (issue #107).
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, uid, "event"); err != nil {
		return storage.Event{}, "", 0, fmt.Errorf("mark resource dirty: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return storage.Event{}, "", 0, err
	}
	return master, postUpdated, token, nil
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
	// LogID is the series-delete log of the delete.
	LogID int64
	// Token is the delete token that the delete wrote on its rows.
	Token int64
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
		token, err := qtx.NextEventDeleteToken(ctx)
		if err != nil {
			return seriesDeletion{}, fmt.Errorf("delete token: %w", err)
		}
		logID, err := recordSeriesDeleteTx(ctx, qtx, t.calendarID, uid, ids, into, false, token)
		if err != nil {
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
			CalendarID: t.calendarID, Uid: uid, DeleteToken: token,
		}); err != nil {
			return seriesDeletion{}, fmt.Errorf("soft-delete orphan rows: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return seriesDeletion{}, err
		}
		return seriesDeletion{Label: t.label(), CalendarID: t.calendarID, Hidden: ids, LogID: logID, Token: token, Tombstoned: calendars}, nil
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
	token, err := qtx.NextEventDeleteToken(ctx)
	if err != nil {
		return seriesDeletion{}, fmt.Errorf("delete token: %w", err)
	}
	logID, err := recordSeriesDeleteTx(ctx, qtx, t.master.CalendarID, uid, hidden, nil, true, token)
	if err != nil {
		return seriesDeletion{}, err
	}
	if err := qtx.SoftDeleteSeriesRowsInCalendar(ctx, storage.SoftDeleteSeriesRowsInCalendarParams{
		CalendarID: t.calendarID, Uid: uid, DeleteToken: token,
	}); err != nil {
		return seriesDeletion{}, fmt.Errorf("soft-delete series: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return seriesDeletion{}, err
	}
	return seriesDeletion{Master: t.master, Label: t.label(), CalendarID: t.master.CalendarID, LogID: logID, Token: token, Tombstoned: calendars}, nil
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
