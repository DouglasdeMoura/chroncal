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

	// For log rows, look up each master's title (including soft-deleted
	// masters) so the trash view has something recognizable to display.
	titles := make(map[string]string, len(instLogs)+len(truncLogs))
	lookupTitle := func(uid string) string {
		if t, ok := titles[uid]; ok {
			return t
		}
		if m, err := s.q.GetEventByUIDIncludingDeleted(ctx, uid); err == nil {
			titles[uid] = m.Title
			return m.Title
		}
		titles[uid] = ""
		return ""
	}

	for _, l := range instLogs {
		inst, _ := time.Parse(time.RFC3339, l.RecurrenceID)
		entries = append(entries, TrashEntry{
			Kind:         TrashKindInstance,
			ID:           l.ID,
			CalendarID:   l.CalendarID,
			UID:          l.Uid,
			Title:        lookupTitle(l.Uid),
			InstanceTime: inst,
			DeletedAt:    parseStorageTime(l.DeletedAt),
		})
	}
	for _, l := range truncLogs {
		cutoff, _ := time.Parse(time.RFC3339, l.CutoffTime)
		entries = append(entries, TrashEntry{
			Kind:          TrashKindTruncation,
			ID:            l.ID,
			CalendarID:    l.CalendarID,
			UID:           l.Uid,
			Title:         lookupTitle(l.Uid),
			CutoffTime:    cutoff,
			PreviousRRule: l.PreviousRrule,
			DeletedAt:     parseStorageTime(l.DeletedAt),
		})
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
	cutoff := olderThan.UTC().Format(storageTimeFormat)
	n, err := s.q.PurgeOldEventExdateDeletes(ctx, cutoff)
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
