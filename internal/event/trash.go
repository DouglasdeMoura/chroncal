package event

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/douglasdemoura/chroncal/internal/storage"
)

// TrashKind discriminates the two shapes a trash row can have: a full
// soft-deleted events row, or a recurring-instance delete captured in the
// event_exdate_deletes log (no row was deleted, an EXDATE was added).
type TrashKind int

const (
	// TrashKindEvent is a soft-deleted events row. Restore clears deleted_at.
	TrashKindEvent TrashKind = iota
	// TrashKindInstance is an EXDATE-based instance delete. Restore removes
	// the EXDATE from the master and deletes the log row.
	TrashKindInstance
)

// TrashEntry is a unified row for the "Recently deleted" view. For
// TrashKindEvent, ID is the events row ID. For TrashKindInstance, ID is
// the event_exdate_deletes row ID.
type TrashEntry struct {
	Kind         TrashKind
	ID           int64
	CalendarID   int64
	UID          string
	Title        string
	InstanceTime time.Time // only populated for TrashKindInstance
	DeletedAt    time.Time
}

// ListTrash merges soft-deleted events and EXDATE-based instance deletes
// for a calendar, newest first. The caller is responsible for visibility
// filtering (hidden calendars, etc.).
func (s *Service) ListTrash(ctx context.Context, calendarID int64) ([]TrashEntry, error) {
	evts, err := s.q.ListDeletedEventsByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list deleted events: %w", err)
	}

	logs, err := s.q.ListEventExdateDeletesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list exdate deletes: %w", err)
	}

	entries := make([]TrashEntry, 0, len(evts)+len(logs))
	for _, e := range evts {
		deletedAt := time.Time{}
		if e.DeletedAt != nil {
			deletedAt = parseStorageTime(*e.DeletedAt)
		}
		entries = append(entries, TrashEntry{
			Kind:       TrashKindEvent,
			ID:         e.ID,
			CalendarID: e.CalendarID,
			UID:        e.Uid,
			Title:      e.Title,
			DeletedAt:  deletedAt,
		})
	}

	// For instance logs, look up the master's title so the trash view can
	// show something recognizable. Masters may themselves be soft-deleted
	// — we still want to show the log row so the user can restore it.
	titles := make(map[string]string, len(logs))
	for _, l := range logs {
		if _, ok := titles[l.Uid]; ok {
			continue
		}
		if m, err := s.q.GetEventByUIDIncludingDeleted(ctx, l.Uid); err == nil {
			titles[l.Uid] = m.Title
		}
	}
	for _, l := range logs {
		inst, _ := time.Parse(time.RFC3339, l.RecurrenceID)
		entries = append(entries, TrashEntry{
			Kind:         TrashKindInstance,
			ID:           l.ID,
			CalendarID:   l.CalendarID,
			UID:          l.Uid,
			Title:        titles[l.Uid],
			InstanceTime: inst,
			DeletedAt:    parseStorageTime(l.DeletedAt),
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].DeletedAt.After(entries[j].DeletedAt)
	})
	return entries, nil
}

// RestoreTrash reverses the delete recorded in entry. Dispatches by kind.
// For TrashKindInstance it removes the EXDATE from the master and deletes
// the log row; for TrashKindEvent it un-hides the row and bumps sequence.
func (s *Service) RestoreTrash(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.RestoreByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.restoreInstanceByLogID(ctx, entry.ID)
	default:
		return fmt.Errorf("unknown trash kind %d", entry.Kind)
	}
}

// PurgeOldInstanceDeletes drops event_exdate_deletes rows older than olderThan.
// Returns the number of rows purged. The corresponding EXDATEs on the master
// stay in place — the user intended those instances to be gone.
func (s *Service) PurgeOldInstanceDeletes(ctx context.Context, olderThan time.Time) (int, error) {
	cutoff := olderThan.UTC().Format(storageTimeFormat)
	n, err := s.q.PurgeOldEventExdateDeletes(ctx, cutoff)
	if err != nil {
		return 0, err
	}
	return int(n), nil
}

// PurgeTrashEntry hard-removes entry from the trash. For TrashKindEvent it
// drops the events row; for TrashKindInstance it drops the log row (the
// EXDATE stays on the master — that's the "deleted forever" semantic).
func (s *Service) PurgeTrashEntry(ctx context.Context, entry TrashEntry) error {
	switch entry.Kind {
	case TrashKindEvent:
		return s.PurgeByID(ctx, entry.ID)
	case TrashKindInstance:
		return s.q.DeleteEventExdateDelete(ctx, entry.ID)
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

	existing := ParseTimeList(storage.NullableToString(master.Exdates))
	target, err := time.Parse(time.RFC3339, log.RecurrenceID)
	if err != nil {
		return fmt.Errorf("parse recurrence_id %q: %w", log.RecurrenceID, err)
	}
	filtered := removeTimeFromList(existing, target)
	if err := qtx.UpdateEventExdates(ctx, storage.UpdateEventExdatesParams{
		Exdates: storage.StringToNullable(SerializeTimeList(filtered)),
		ID:      master.ID,
	}); err != nil {
		return fmt.Errorf("update exdates: %w", err)
	}
	if err := qtx.DeleteEventExdateDelete(ctx, logID); err != nil {
		return fmt.Errorf("delete log row: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = storage.MarkResourceDirty(ctx, s.db, master.CalendarID, log.Uid, "event")
	return nil
}

// removeTimeFromList returns list with every element equal to target (after
// UTC normalization) removed. Used to reverse an EXDATE insertion.
func removeTimeFromList(list []time.Time, target time.Time) []time.Time {
	out := make([]time.Time, 0, len(list))
	targetKey := target.UTC().Format(time.RFC3339)
	for _, t := range list {
		if strings.EqualFold(t.UTC().Format(time.RFC3339), targetKey) {
			continue
		}
		out = append(out, t)
	}
	return out
}
