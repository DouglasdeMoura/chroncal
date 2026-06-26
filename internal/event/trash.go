package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/timeutil"
)

// TrashKind discriminates the three shapes a trash row can have: a full
// soft-deleted events row, a recurring-instance delete captured in the
// event_exdate_deletes log (no row was deleted, an EXDATE was added), or
// an RRULE truncation captured in event_truncate_deletes (the master's
// recurrence was shortened and any overrides past the cutoff were
// soft-deleted).
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
// event_truncate_deletes row ID and CutoffTime/PreviousRRule are populated.
//
// Display-ready event fields (StartTime/EndTime/AllDay/Location/Description/
// Status/Categories) are populated so the trash dialog can render full
// details inline without opening a second view. For TrashKindInstance and
// TrashKindTruncation these come from the master (the series shape), with
// StartTime/EndTime shifted to the instance or cutoff window when relevant.
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
// is responsible for visibility filtering (hidden calendars, etc.).
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

	entries := make([]TrashEntry, 0, len(evts)+len(instLogs)+len(truncLogs))
	for _, r := range evts {
		ev := fromStorage(r)
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

	// For log rows, look up the master (including soft-deleted) once per
	// UID so the trash dialog can render full context — title, location,
	// description, status — without the user having to open a second view.
	masters := make(map[string]*Event, len(instLogs)+len(truncLogs))
	lookupMaster := func(uid string) *Event {
		if m, ok := masters[uid]; ok {
			return m
		}
		if r, err := s.q.GetEventByUIDIncludingDeleted(ctx, uid); err == nil {
			ev := fromStorage(r)
			masters[uid] = &ev
			return &ev
		}
		masters[uid] = nil
		return nil
	}

	for _, l := range instLogs {
		inst, _ := time.Parse(time.RFC3339, l.RecurrenceID)
		entry := TrashEntry{
			Kind:         TrashKindInstance,
			ID:           l.ID,
			CalendarID:   l.CalendarID,
			UID:          l.Uid,
			InstanceTime: inst,
			DeletedAt:    parseStorageTime(l.DeletedAt),
		}
		if m := lookupMaster(l.Uid); m != nil {
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
		if m := lookupMaster(l.Uid); m != nil {
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
// Returns the number of rows purged. The corresponding EXDATEs on the master
// stay in place — the user intended those instances to be gone.
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

// PurgeTrashEntry hard-removes entry from the trash. For TrashKindEvent it
// drops the events row; for TrashKindInstance and TrashKindTruncation it
// drops the log row only — the EXDATE (or truncated RRULE + deleted
// overrides) stays on the master, matching the "deleted forever" semantic
// the user expressed at delete time.
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
	// snapshot (issue #116).
	master, err := qtx.GetEventByUID(ctx, log.Uid)
	if err != nil {
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
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}

// restoreTruncationByLogID rewrites the master's RRULE back to the
// pre-truncation value and un-hides every override soft-deleted at/after
// the cutoff, then drops the log row. The master is marked dirty so the
// next push propagates the restored RRULE.
func (s *Service) restoreTruncationByLogID(ctx context.Context, logID int64) error {
	log, err := s.q.GetEventTruncateDelete(ctx, logID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotDeleted
		}
		return fmt.Errorf("get truncate log: %w", err)
	}
	master, err := s.q.GetEventByUID(ctx, log.Uid)
	if err != nil {
		return fmt.Errorf("get master: %w", err)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	if err := qtx.UpdateEventRecurrenceRule(ctx, storage.UpdateEventRecurrenceRuleParams{
		RecurrenceRule: storage.StringToNullable(log.PreviousRrule),
		ID:             master.ID,
	}); err != nil {
		return fmt.Errorf("restore rrule: %w", err)
	}
	if err := qtx.RestoreOverridesAtOrAfter(ctx, storage.RestoreOverridesAtOrAfterParams{
		Uid:          log.Uid,
		RecurrenceID: log.CutoffTime,
	}); err != nil {
		return fmt.Errorf("restore overrides: %w", err)
	}
	if err := qtx.DeleteEventTruncateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete log row: %w", err)
	}
	if err := storage.MarkResourceDirty(ctx, tx, master.CalendarID, log.Uid, "event"); err != nil {
		return fmt.Errorf("mark resource dirty: %w", err)
	}
	return tx.Commit()
}
