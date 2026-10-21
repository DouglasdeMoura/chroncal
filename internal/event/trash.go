package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/douglasdemoura/chroncal/internal/calendaraccess"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// TrashKind discriminates the three shapes a trash row can have:
//
//   - a full soft-deleted events row
//   - a recurring-instance delete captured in the event_exdate_deletes log
//     (no row was deleted; an EXDATE was added)
//   - an RRULE truncation captured in event_truncate_deletes. The master's
//     recurrence was shortened. Any overrides past the cutoff were
//     soft-deleted.
type TrashKind int

const (
	// TrashKindEvent is a soft-deleted events row. Restore clears deleted_at.
	TrashKindEvent TrashKind = iota
	// TrashKindInstance is an EXDATE-based instance delete. Restore removes
	// the EXDATE from the master and deletes the log row.
	TrashKindInstance
	// TrashKindTruncation is an RRULE truncation ("This and following").
	// Restore rewrites the master's RRULE back to its pre-truncation value
	// AND un-hides any overrides soft-deleted by the truncation.
	TrashKindTruncation
)

// TrashEntry is a unified row for the "Recently deleted" view. For
// TrashKindEvent, ID is the events row ID. For TrashKindInstance, ID is
// the event_exdate_deletes row ID. For TrashKindTruncation, ID is the
// event_truncate_deletes row ID and CutoffTime/PreviousRRule have values.
//
// Display-ready event fields (StartTime/EndTime/AllDay/Location/Description/
// Status/Categories) have values so the trash dialog can show full
// details inline. A second view is not needed. For TrashKindInstance and
// TrashKindTruncation these come from the master (the series shape).
// StartTime/EndTime shift to the instance or cutoff window when relevant.
type TrashEntry struct {
	Kind          TrashKind
	ID            int64
	CalendarID    int64
	UID           string
	Title         string
	InstanceTime  time.Time // populated for TrashKindInstance
	CutoffTime    time.Time // populated for TrashKindTruncation
	PreviousRRule string    // populated for TrashKindTruncation
	DeletedAt     time.Time

	StartTime   time.Time
	EndTime     time.Time
	AllDay      bool
	Location    string
	Description string
	Status      string
	Categories  string
}

// ListTrash merges soft-deleted events, EXDATE-based instance deletes,
// and RRULE truncation deletes for a calendar, newest first. The caller
// is responsible for visibility filters (hidden calendars, and others).
func (s *Service) ListTrash(ctx context.Context, calendarID int64) ([]TrashEntry, error) {
	evts, err := s.q.ListDeletedEventsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list deleted events: %w", err)
	}
	instLogs, err := s.q.ListEventExdateDeletesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list exdate deletes: %w", err)
	}
	truncLogs, err := s.q.ListEventTruncateDeletesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list truncate deletes: %w", err)
	}

	deletedEvents := make([]Event, len(evts))
	for i, r := range evts {
		deletedEvents[i] = FromStorage(r)
	}
	s.populateCategories(ctx, deletedEvents)

	entries := make([]TrashEntry, 0, len(evts)+len(instLogs)+len(truncLogs))
	for _, ev := range deletedEvents {
		deletedAt := time.Time{}
		if ev.DeletedAt != nil {
			deletedAt = *ev.DeletedAt
		}
		entries = append(entries, TrashEntry{
			Kind:        TrashKindEvent,
			ID:          ev.ID,
			CalendarID:  ev.CalendarID,
			UID:         ev.UID,
			Title:       ev.Title,
			DeletedAt:   deletedAt,
			StartTime:   ev.StartTime,
			EndTime:     ev.EndTime,
			AllDay:      ev.AllDay,
			Location:    ev.Location,
			Description: ev.Description,
			Status:      ev.Status,
			Categories:  ev.Categories,
		})
	}

	// For log rows, look up the master (soft-deleted included) once per
	// UID so the trash dialog can show full context: title, location,
	// description, status. The user does not need a second view.
	// Fetch each distinct master once, then batch category population into a
	// single query rather than one per master. A log row names the
	// calendar of its master: two calendars can hold the same UID.
	type masterKey struct {
		calendarID int64
		uid        string
	}
	masters := make(map[masterKey]*Event, len(instLogs)+len(truncLogs))
	for _, l := range instLogs {
		masters[masterKey{l.CalendarID, l.Uid}] = nil
	}
	for _, l := range truncLogs {
		masters[masterKey{l.CalendarID, l.Uid}] = nil
	}
	masterList := make([]Event, 0, len(masters))
	for key := range masters {
		r, err := s.q.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
			CalendarID: key.calendarID, Uid: key.uid,
		})
		if err == nil {
			masterList = append(masterList, FromStorage(r))
		}
	}
	s.populateCategories(ctx, masterList)
	for i := range masterList {
		masters[masterKey{masterList[i].CalendarID, masterList[i].UID}] = &masterList[i]
	}
	lookupMaster := func(calendarID int64, uid string) *Event { return masters[masterKey{calendarID, uid}] }

	for _, l := range instLogs {
		// An occurrence log with no master in its calendar is no entry. The
		// log of a delete by ID of an orphan row names a row that the trash
		// lists already, and a stale log names nothing to restore.
		if lookupMaster(l.CalendarID, l.Uid) == nil {
			continue
		}
		inst, _ := time.Parse(time.RFC3339, l.RecurrenceID)
		entry := TrashEntry{
			Kind:         TrashKindInstance,
			ID:           l.ID,
			CalendarID:   l.CalendarID,
			UID:          l.Uid,
			InstanceTime: inst,
			DeletedAt:    parseStorageTime(l.DeletedAt),
		}
		if m := lookupMaster(l.CalendarID, l.Uid); m != nil {
			entry.Title = m.Title
			entry.AllDay = m.AllDay
			entry.Location = m.Location
			entry.Description = m.Description
			entry.Status = m.Status
			entry.Categories = m.Categories
			// Project the master's duration onto this occurrence so
			// "When" reads as "Fri, Apr 3 09:00 – 09:30" rather than
			// the master's original time.
			entry.StartTime = inst
			if !m.EndTime.IsZero() && !m.StartTime.IsZero() {
				entry.EndTime = inst.Add(m.EndTime.Sub(m.StartTime))
			}
		}
		entries = append(entries, entry)
	}
	for _, l := range truncLogs {
		cutoff, _ := time.Parse(time.RFC3339, l.CutoffTime)
		entry := TrashEntry{
			Kind:          TrashKindTruncation,
			ID:            l.ID,
			CalendarID:    l.CalendarID,
			UID:           l.Uid,
			CutoffTime:    cutoff,
			PreviousRRule: l.PreviousRrule,
			DeletedAt:     parseStorageTime(l.DeletedAt),
		}
		if m := lookupMaster(l.CalendarID, l.Uid); m != nil {
			entry.Title = m.Title
			entry.AllDay = m.AllDay
			entry.Location = m.Location
			entry.Description = m.Description
			entry.Status = m.Status
			entry.Categories = m.Categories
			entry.StartTime = m.StartTime
			entry.EndTime = m.EndTime
		}
		entries = append(entries, entry)
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].DeletedAt.After(entries[j].DeletedAt)
	})
	return entries, nil
}

// RestoreTrash reverses the delete recorded in entry. Dispatches by kind.
// For TrashKindEvent it un-hides the row and bumps sequence. For
// TrashKindInstance it removes the EXDATE from the master and deletes the
// log row. For TrashKindTruncation it rewrites the master's RRULE back
// and un-hides any overrides that were soft-deleted by the truncation.
func (s *Service) RestoreTrash(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.RestoreByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.restoreInstanceByLogID(ctx, entry.ID)
	case TrashKindTruncation:
		return s.restoreTruncationByLogID(ctx, entry.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", entry.Kind)
	}
}

// PurgeOldInstanceDeletes drops event_exdate_deletes rows older than olderThan.
// Returns the number of rows purged. The related EXDATEs on the master
// stay in place. The user intended those instances to be gone.
func (s *Service) PurgeOldInstanceDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeOldEventExdateDeletes(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeOldTruncationDeletes drops event_truncate_deletes rows older than
// olderThan. Returns the number of rows purged. The truncated RRULE and
// soft-deleted overrides on the master stay in place.
func (s *Service) PurgeOldTruncationDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeOldEventTruncateDeletes(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeOldSeriesDeletes drops event_series_deletes rows older than
// olderThan. Returns the number of rows purged. A restore of the series
// then brings the master back alone.
func (s *Service) PurgeOldSeriesDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(timeutil.StorageTimeFormat)
	n, err := s.q.PurgeOldEventSeriesDeletes(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeTrashEntry hard-removes entry from the trash. For TrashKindEvent it
// drops the events row. For TrashKindInstance and TrashKindTruncation it
// drops the log row only. The EXDATE (or truncated RRULE plus deleted
// overrides) stays on the master. That matches the "deleted forever"
// semantic the user expressed at delete time.
func (s *Service) PurgeTrashEntry(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.PurgeByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.q.DeleteEventExdateDelete(ctx, entry.ID)
	case TrashKindTruncation:
		return s.q.DeleteEventTruncateDelete(ctx, entry.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", entry.Kind)
	}
}

// restoreInstanceByLogID removes the EXDATE for (uid, recurrence_id) from
// the master and deletes the log row. The master is marked dirty so the
// next push propagates the EXDATE removal.
func (s *Service) restoreInstanceByLogID(ctx context.Context, logID int64) error {
	log, err := s.q.GetEventExdateDelete(ctx, logID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get exdate log: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Read the master inside the transaction so the EXDATE list we rewrite
	// reflects a concurrent writer's changes rather than a pre-transaction
	// snapshot (issue #116). The log row names the calendar of the
	// master: two calendars can hold the same UID (issue #842).
	master, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
		CalendarID: log.CalendarID, Uid: log.Uid,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get master: %w", ErrNotDeleted)
		}
		return fmt.Errorf("get master: %w", err)
	}

	existing := ParseTimeList(storage.NullableToString(master.Exdates))
	target, err := time.Parse(time.RFC3339, log.RecurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", log.RecurrenceID, err)
	}
	filtered := timeutil.RemoveTimeFromList(existing, target)
	if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
		Exdates: storage.StringToNullable(SerializeTimeList(filtered)),
		ID:      master.ID,
	}); err != nil {
		return fmt.Errorf("update exdates: %w", err)
	}
	if err := qtx.DeleteEventExdateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete log row: %w", err)
	}
	// A master in the trash takes the EXDATE change as it is: its own
	// restore shows the occurrence and marks the series dirty.
	if master.DeletedAt == nil {
		if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
			return fmt.Errorf("mark resource dirty: %w", err)
		}
	}
	return tx.Commit()
}

// restoreTruncationByLogID rewrites the master's RRULE back to the
// pre-truncation value. It re-adds the RDATEs the truncation trimmed. It
// un-hides every override soft-deleted at or after the cutoff. It then
// drops the log row. The master is marked dirty so the next push
// propagates the restored series.
func (s *Service) restoreTruncationByLogID(ctx context.Context, logID int64) error {
	log, err := s.q.GetEventTruncateDelete(ctx, logID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get truncate log: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// The restore writes the master in the calendar of the log. A log
	// with row IDs guards the calendars of its rows in restoreLoggedIDsTx.
	// An older log restores by recurrence ID through the series, so the
	// guard covers the calendars of the series.
	if log.HiddenIds != nil {
		if err := calendaraccess.EnsureWritable(ctx, qtx, log.CalendarID, "VEVENT"); err != nil {
			return err
		}
	} else if err := ensureSeriesWritableIn(ctx, qtx, log.CalendarID, log.Uid); err != nil {
		return err
	}

	// Read the master inside the transaction. Then a concurrent purge or
	// soft-delete between the read and the in-tx UPDATE cannot make
	// UpdateEventRecurrenceRule match 0 rows while we still consume the log
	// row. That would drop the RRULE restore in silence (issue #413, mirrors
	// restoreInstanceByLogID / #116). The log row names the calendar of
	// the master: two calendars can hold the same UID (issue #842).
	master, err := qtx.GetEventByCalendarUIDIncludingDeleted(ctx, storage.GetEventByCalendarUIDIncludingDeletedParams{
		CalendarID: log.CalendarID, Uid: log.Uid,
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("get master: %w", ErrNotDeleted)
		}
		return fmt.Errorf("get master: %w", err)
	}
	if master.DeletedAt != nil {
		// The truncation rewrites the rule of a live master. The entry
		// stays, and the restore of the series comes first.
		return ErrMasterInTrash
	}

	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(log.PreviousRrule),
		ID:             master.ID,
	}); err != nil {
		return fmt.Errorf("restore rrule: %w", err)
	}
	if err := restoreTruncatedRDates(ctx, qtx, master, log); err != nil {
		return fmt.Errorf("restore rdates: %w", err)
	}
	// The restore consumes the log row first. The drop of the restored rows
	// from the other logs then does not rewrite this row.
	if err := qtx.DeleteEventTruncateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete log row: %w", err)
	}
	if err := restoreTruncatedOverrides(ctx, qtx, log); err != nil {
		return fmt.Errorf("restore overrides: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}

// restoreTruncatedRDates re-adds to the master the RDATEs a truncation trimmed.
// removed_rdates is non-NULL only for #463-era log rows. Pre-#463 rows store
// NULL (the truncation left RDATEs untouched). An empty string means the
// truncation dropped none. Both no-op. The kept RDATEs (already on the
// master after a concurrent edit) are merged with the recorded removed ones.
func restoreTruncatedRDates(ctx context.Context, qtx *storage.Queries, master storage.Event, log storage.EventTruncateDelete) error {
	if log.RemovedRdates == nil || *log.RemovedRdates == "" {
		return nil
	}
	merged := append(ParseTimeList(storage.NullableToString(master.Rdates)), ParseTimeList(*log.RemovedRdates)...)
	return qtx.UpdateEventRdates(ctx, storage.UpdateEventRdatesParams{
		Rdates: storage.StringToNullable(SerializeTimeList(merged)),
		ID:     master.ID,
	})
}

// restoreTruncatedOverrides un-hides the overrides a truncation soft-deleted.
// When the log recorded provenance (hidden_overrides is non-NULL), it restores
// only those specific overrides. An override the user deleted on its own
// before the truncation then stays deleted (issue #287). Pre-#287 log rows
// store NULL provenance. For those it falls back to un-hiding every
// soft-deleted override at or after the cutoff. The overrides are the
// rows of the series in the calendar of the log row (issue #842).
func restoreTruncatedOverrides(ctx context.Context, qtx *storage.Queries, log storage.EventTruncateDelete) error {
	if log.HiddenIds != nil {
		// A log row of migration 057 and later names the hidden rows by ID. The
		// restore then does not depend on the series owner of today. The calendar
		// of each row takes the write.
		ids, err := parseRowIDs(*log.HiddenIds)
		if err != nil {
			return err
		}
		_, err = restoreLoggedIDsTx(ctx, qtx, log.CalendarID, log.Uid, ids)
		return err
	}
	if log.HiddenOverrides == nil {
		return qtx.RestoreSeriesOverridesAtOrAfter(ctx, storage.RestoreSeriesOverridesAtOrAfterParams{
			CalendarID:   log.CalendarID,
			Uid:          log.Uid,
			RecurrenceID: log.CutoffTime,
		})
	}
	if *log.HiddenOverrides == "" {
		return nil
	}
	recIDs, err := parseCommaList(*log.HiddenOverrides, parseRecurrenceID)
	if err != nil {
		return err
	}
	for _, recID := range recIDs {
		if _, err := qtx.RestoreSeriesOverrideByRecurrenceID(ctx, storage.RestoreSeriesOverrideByRecurrenceIDParams{
			CalendarID:   log.CalendarID,
			Uid:          log.Uid,
			RecurrenceID: recID,
		}); err != nil {
			return err
		}
	}
	return nil
}
