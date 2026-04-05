package sync

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"

	"github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// Service provides the high-level sync interface used by CLI commands.
type Service struct {
	engine *Engine
	db     *sql.DB
	q      *storage.Queries
}

// NewService creates a new sync service.
func NewService(db *sql.DB, q *storage.Queries, credStore auth.CredentialStore, calendars *calendar.Service, events *event.Service, todos *todo.Service, journals *journal.Service, logger *slog.Logger) *Service {
	return &Service{
		engine: NewEngine(db, q, credStore, calendars, events, todos, journals, logger),
		db:     db,
		q:      q,
	}
}

// SyncCalendar runs a sync cycle for one calendar.
func (s *Service) SyncCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (*SyncResult, error) {
	return s.engine.SyncCalendar(ctx, calendarID, strategy)
}

// SyncAll syncs all calendars linked to accounts.
func (s *Service) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	return s.engine.SyncAll(ctx, strategy)
}

// SyncStatus returns the current sync status for a calendar.
type SyncStatus struct {
	CalendarID          int64
	CalendarName        string
	LastSyncToken       string
	LastSyncAt          string // RFC 3339 or empty
	LastSyncAttemptedAt string // RFC 3339 or empty
	LastSyncError       string
	PendingPush         int
	Conflicts           int
}

// Status returns sync status for all synced calendars.
func (s *Service) Status(ctx context.Context) ([]SyncStatus, error) {
	cals, err := s.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	var statuses []SyncStatus
	for _, cal := range cals {
		if cal.AccountID == nil || *cal.AccountID == 0 {
			continue
		}

		dirty, err := s.q.ListDirtySyncResources(ctx, cal.ID)
		if err != nil {
			dirty = nil
		}
		conflicts, err := s.q.ListSyncConflictsByCalendar(ctx, cal.ID)
		if err != nil {
			conflicts = nil
		}
		statuses = append(statuses, SyncStatus{
			CalendarID:          cal.ID,
			CalendarName:        cal.Name,
			LastSyncToken:       storage.NullableToString(cal.SyncToken),
			LastSyncAt:          storage.NullableToString(cal.LastSyncAt),
			LastSyncAttemptedAt: storage.NullableToString(cal.LastSyncAttemptedAt),
			LastSyncError:       storage.NullableToString(cal.LastSyncError),
			PendingPush:         len(dirty),
			Conflicts:           len(conflicts),
		})
	}
	return statuses, nil
}

// Conflict represents an unresolved sync conflict.
type Conflict struct {
	ID         int64
	CalendarID int64
	OwnerType  string
	UID        string
	LocalICal  string
	ServerICal string
	ServerETag string
	DetectedAt time.Time
}

// ListConflicts returns all unresolved sync conflicts.
func (s *Service) ListConflicts(ctx context.Context) ([]Conflict, error) {
	rows, err := s.q.ListSyncConflicts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, len(rows))
	for i, r := range rows {
		out[i] = Conflict{
			ID:         r.ID,
			CalendarID: r.CalendarID,
			OwnerType:  r.OwnerType,
			UID:        r.Uid,
			LocalICal:  r.LocalIcal,
			ServerICal: r.ServerIcal,
			ServerETag: r.ServerEtag,
			DetectedAt: parseTime(r.DetectedAt),
		}
	}
	return out, nil
}

// ResolveConflict resolves a conflict by picking local or server version.
func (s *Service) ResolveConflict(ctx context.Context, conflictID int64, pick string) error {
	conflict, err := s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return fmt.Errorf("get conflict: %w", err)
	}

	switch pick {
	case "server":
		// Server version is already the current local state after auto-resolve.
		// Accept it by clearing the pending local push and storing the server ETag.
		if err := s.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
			CalendarID: conflict.CalendarID,
			Uid:        conflict.Uid,
			Etag:       conflict.ServerEtag,
		}); err != nil {
			return fmt.Errorf("clear dirty: %w", err)
		}
	case "local":
		// Mark the resource as dirty so next sync pushes local version
		if err := s.q.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{
			CalendarID: conflict.CalendarID,
			Uid:        conflict.Uid,
		}); err != nil {
			return fmt.Errorf("mark dirty: %w", err)
		}
	default:
		return fmt.Errorf("invalid pick: %q (use 'local' or 'server')", pick)
	}

	return s.q.DeleteSyncConflict(ctx, conflictID)
}

// ResetCalendar clears all sync state for a calendar without deleting local data.
// The next sync will perform a full initial sync.
func (s *Service) ResetCalendar(ctx context.Context, calendarID int64) error {
	if err := s.q.DeleteSyncResourcesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete sync resources: %w", err)
	}
	if err := s.q.DeleteTombstonesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete tombstones: %w", err)
	}
	if err := s.q.DeleteSyncConflictsByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete conflicts: %w", err)
	}
	if err := s.q.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
		ID: calendarID,
	}); err != nil {
		return fmt.Errorf("clear sync state: %w", err)
	}
	return nil
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	if t.IsZero() {
		t, _ = time.Parse("2006-01-02 15:04:05", s)
	}
	return t
}
