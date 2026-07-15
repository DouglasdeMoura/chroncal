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

// PushCalendar pushes pending local changes for one calendar without pulling.
// Intended for opportunistic save-time sync from CLI/TUI mutations. Failures
// leave the dirty flag intact so the periodic tick can retry.
func (s *Service) PushCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (*SyncResult, error) {
	return s.engine.PushCalendar(ctx, calendarID, strategy)
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

// resolveConflictAfterRevCapture, when non-nil, runs inside ResolveConflict's
// accept-server path between taking the import rev (from importICal's revs map)
// and the rev-guarded dirty clear. It is nil in production and exists only so
// tests can simulate a concurrent local edit landing in that window to exercise
// the guard. The narrower persist-commit window — an edit landing inside
// importICal right after persistImported commits — is covered by the engine's
// afterImportPersist hook. See the engine's afterImportRevCapture and issues
// #466 and #510.
var resolveConflictAfterRevCapture func()

// ResolveConflict resolves a conflict by picking local or server version.
func (s *Service) ResolveConflict(ctx context.Context, conflictID int64, pick string) error {
	if pick != "server" && pick != "local" {
		return fmt.Errorf("invalid pick: %q (use 'local' or 'server')", pick)
	}

	conflict, err := s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return fmt.Errorf("get conflict: %w", err)
	}
	release, err := s.engine.lockCalendarLifecycle(ctx, conflict.CalendarID)
	if err != nil {
		return fmt.Errorf("lock conflict calendar lifecycle: %w", err)
	}
	defer release()
	conflict, err = s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return fmt.Errorf("revalidate conflict: %w", err)
	}

	// serverRev captures the sync_resources rev right after the accept-server
	// import so the dirty clear below can be made conditional on it (see the
	// "server" case in the transaction). Unused for the "local" pick.
	var serverRev int64
	if pick == "server" {
		// Accept the server version: import the recorded server iCal into the
		// local row so it reflects the server state. Without the import the
		// local row keeps its divergent local copy while the ETag (cleared
		// below) claims it matches the server, so a later local edit silently
		// overwrites the server. importICal is tombstone-aware, so a UID the
		// user has locally deleted is not resurrected here (issue #89 gap #2).
		//
		// The import runs before the transaction below because it flows through
		// the event/todo/journal services, which use their own connection.
		// UpsertByUID is idempotent, so if the transaction fails to commit the
		// conflict survives and the whole resolution replays cleanly.
		imported, revs, err := s.engine.importICal(ctx, conflict.CalendarID, conflict.ServerIcal)
		if err != nil {
			return fmt.Errorf("import server version: %w", err)
		}
		if !imported {
			// ImportFile returns no error for empty or component-less iCal, so a
			// blank ServerIcal would otherwise clear dirty and stamp the server
			// ETag onto the unchanged local row — the silent-overwrite bug this
			// branch exists to prevent. Refuse instead of resolving falsely.
			return fmt.Errorf("server version has no importable data for %q", conflict.Uid)
		}
		// Use the rev importICal captured inside persistImported's transaction so
		// the dirty clear can be rev-guarded like the auto accept-server paths
		// (engine.clearDirtyAfterImport). importICal bumps rev and re-sets dirty=1
		// via MarkResourceDirty; a concurrent local edit committing after that
		// capture bumps rev again, and the conditional clear below then leaves
		// dirty=1 so the edit is still pushed instead of being silently dropped —
		// the lost-update race of issues #92/#417/#466/#494. Re-reading the rev
		// after commit (as this path did before #510) reopened that window: an
		// edit landing in the persist-commit→read gap was read back and matched by
		// the guard, wiping its dirty flag.
		serverRev = revs[conflict.Uid]
		if resolveConflictAfterRevCapture != nil {
			resolveConflictAfterRevCapture()
		}
	}

	// Wrap the dirty/etag mutation and the conflict deletion in one transaction
	// so a failure can't half-resolve the conflict — e.g. dirty cleared with a
	// stale ETag but the conflict still recorded, which would re-trigger an HTTP
	// 412 loop on the next push. Issue #89 gap #3.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	switch pick {
	case "server":
		// Adopt the server ETag and clear the pending local push now that the
		// local row reflects the server version — but only when no local edit
		// has landed since serverRev was captured. FinalizePushedResource always
		// advances the ETag yet clears dirty only on rev == serverRev, so a
		// concurrent edit (which bumped rev) keeps dirty=1 and survives to the
		// next push. The unconditional ClearSyncResourceDirty used here before
		// wiped that edit — issues #92/#417/#466.
		if err := qtx.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
			CalendarID: conflict.CalendarID,
			Uid:        conflict.Uid,
			Etag:       conflict.ServerEtag,
			Rev:        serverRev,
		}); err != nil {
			return fmt.Errorf("clear dirty: %w", err)
		}
	case "local":
		// Mark the resource as dirty so the next sync pushes the local
		// version, and replace the stored etag with the server etag recorded
		// at conflict-detection time. The previously stored etag may be stale
		// (it could be the value that already failed If-Match); reusing it
		// would re-trigger HTTP 412 forever. Using the conflict's ServerEtag
		// keeps the concurrency check intact: the next push sends
		// If-Match: <ServerEtag>, which succeeds if the server is unchanged
		// (fixing the loop) but 412s and surfaces a fresh conflict if the
		// server was edited again after this conflict was recorded.
		if err := qtx.MarkSyncResourceDirtyWithEtag(ctx, storage.MarkSyncResourceDirtyWithEtagParams{
			CalendarID: conflict.CalendarID,
			Uid:        conflict.Uid,
			Etag:       conflict.ServerEtag,
		}); err != nil {
			return fmt.Errorf("mark dirty: %w", err)
		}
	}

	if err := qtx.DeleteSyncConflict(ctx, conflictID); err != nil {
		return fmt.Errorf("delete conflict: %w", err)
	}
	return tx.Commit()
}

// ResetCalendar clears all sync state for a calendar without deleting local data.
// The next sync will perform a full initial sync.
func (s *Service) ResetCalendar(ctx context.Context, calendarID int64) error {
	release, err := s.engine.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("lock calendar reset lifecycle: %w", err)
	}
	defer release()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin calendar reset: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	if err := qtx.DeleteSyncResourcesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete sync resources: %w", err)
	}
	if err := qtx.DeleteTombstonesByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete tombstones: %w", err)
	}
	if err := qtx.DeleteSyncConflictsByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete conflicts: %w", err)
	}
	if err := qtx.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
		ID: calendarID,
	}); err != nil {
		return fmt.Errorf("clear sync state: %w", err)
	}
	return tx.Commit()
}

func parseTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	if t.IsZero() {
		t, _ = time.Parse("2006-01-02 15:04:05", s)
	}
	return t
}
