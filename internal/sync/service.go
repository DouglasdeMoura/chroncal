package sync

import (
	"context"
	"database/sql"
	"errors"
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

// PushLocalEdits pushes unpushed local changes for one calendar with no
// pull. Intended for opportunistic save-time sync from CLI/TUI mutations.
// A 412 records a conflict and keeps the local edit dirty; this path never
// adopts the server body over the edit the user just made. Failures leave
// the dirty flag intact so the periodic tick can retry.
func (s *Service) PushLocalEdits(ctx context.Context, calendarID int64) (*SyncResult, error) {
	return s.engine.PushLocalEdits(ctx, calendarID)
}

// SyncAccount syncs all calendars linked to one account.
func (s *Service) SyncAccount(ctx context.Context, accountID int64, strategy ConflictStrategy) ([]*SyncResult, error) {
	return s.engine.SyncAccount(ctx, accountID, strategy)
}

// SyncAll syncs all calendars linked to accounts.
func (s *Service) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	return s.engine.SyncAll(ctx, strategy)
}

// DiagnoseCalendar lists the wedged resources of one calendar. A wedged
// resource fails export on a deterministic hydration error, so every sync
// retries it and no edit under its UID reaches the server.
func (s *Service) DiagnoseCalendar(ctx context.Context, calendarID int64) ([]WedgedResource, error) {
	return s.engine.DiagnoseCalendar(ctx, calendarID)
}

// DoctorPush pushes one wedged resource with best-effort hydration. The PUT
// omits every relation in dropped. The caller must obtain explicit user
// confirmation first; this is the one path that knowingly pushes an
// incomplete record (issue #568).
func (s *Service) DoctorPush(ctx context.Context, calendarID int64, uid string) ([]string, error) {
	return s.engine.DoctorPush(ctx, calendarID, uid)
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

// Conflict represents a recorded sync conflict.
type Conflict struct {
	ID         int64
	CalendarID int64
	OwnerType  string
	UID        string
	LocalICal  string
	ServerICal string
	ServerETag string
	DetectedAt time.Time
	// Resolution and ResolvedAt are set on a resolved conflict. The row
	// survives resolution so `sync resolve --pick local` can still restore
	// the recorded local body.
	Resolution string
	ResolvedAt time.Time
}

// ListConflicts returns all unresolved sync conflicts.
func (s *Service) ListConflicts(ctx context.Context) ([]Conflict, error) {
	rows, err := s.q.ListSyncConflicts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, len(rows))
	for i, r := range rows {
		out[i] = conflictFromRow(r)
	}
	return out, nil
}

// ListResolvedConflicts returns the conflicts a pass or the user already
// resolved, newest resolution first.
func (s *Service) ListResolvedConflicts(ctx context.Context) ([]Conflict, error) {
	rows, err := s.q.ListResolvedSyncConflicts(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]Conflict, len(rows))
	for i, r := range rows {
		out[i] = conflictFromRow(r)
	}
	return out, nil
}

func conflictFromRow(r storage.SyncConflict) Conflict {
	c := Conflict{
		ID:         r.ID,
		CalendarID: r.CalendarID,
		OwnerType:  r.OwnerType,
		UID:        r.Uid,
		LocalICal:  r.LocalIcal,
		ServerICal: r.ServerIcal,
		ServerETag: r.ServerEtag,
		DetectedAt: parseTime(r.DetectedAt),
	}
	if r.Resolution != nil {
		c.Resolution = *r.Resolution
	}
	if r.ResolvedAt != nil {
		c.ResolvedAt = parseTime(*r.ResolvedAt)
	}
	return c
}

// resolveConflictAfterRevCapture, when non-nil, runs inside ResolveConflict's
// accept-server path between taking the import rev (from importICal's revs map)
// and the rev-guarded dirty clear. It is nil in production. Tests use it to
// simulate a concurrent local edit that lands in that window, to exercise
// the guard. The narrower persist-commit window is an edit that lands inside
// importICal right after persistImported commits. The engine's
// afterImportPersist hook covers that window. See the engine's
// afterImportRevCapture and issues #466 and #510.
var resolveConflictAfterRevCapture func()

// ResolveConflict resolves a conflict by a pick of the local or server
// version. The local pick first imports the recorded local body, then marks
// the resource dirty with the server ETag so the next push sends it. The
// server pick imports the recorded server body. The returned warnings list
// what an import could not represent faithfully. The CLI builds this
// service with a nil (silent) engine logger. The return value is then the
// only channel those warnings have. A resolved conflict can be resolved
// again: the row survives resolution so the recorded local body stays
// recoverable.
func (s *Service) ResolveConflict(ctx context.Context, conflictID int64, pick string) ([]ImportWarning, error) {
	if pick != "server" && pick != "local" {
		return nil, fmt.Errorf("invalid pick: %q (use 'local' or 'server')", pick)
	}

	conflict, err := s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return nil, fmt.Errorf("get conflict: %w", err)
	}
	release, err := s.engine.lockCalendarLifecycle(ctx, conflict.CalendarID)
	if err != nil {
		return nil, fmt.Errorf("lock conflict calendar lifecycle: %w", err)
	}
	defer release()
	conflict, err = s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return nil, fmt.Errorf("revalidate conflict: %w", err)
	}

	// serverRev captures the sync_resources rev right after the accept-server
	// import so the dirty clear below can be made conditional on it (see the
	// "server" case in the transaction). Unused for the "local" pick.
	var serverRev int64
	var warnings []ImportWarning
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
		imported, revs, importWarnings, err := s.engine.importICal(ctx, conflict.CalendarID, conflict.ServerIcal)
		if err != nil {
			return nil, fmt.Errorf("import server version: %w", err)
		}
		if !imported {
			// ImportFile returns no error for empty or component-less iCal, so a
			// blank ServerIcal would otherwise clear dirty and stamp the server
			// ETag onto the unchanged local row — the silent-overwrite bug this
			// branch exists to prevent. Refuse instead of resolving falsely.
			return nil, fmt.Errorf("server version has no importable data for %q", conflict.Uid)
		}
		warnings = importWarnings
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
	} else {
		// Restore the recorded local body before the transaction below marks
		// the resource dirty. The current row may no longer hold that body: a
		// server-wins pass (or any later import) can have replaced it after
		// the conflict was recorded. Without the restore, the dirty mark
		// faithfully re-pushes the server content and the local edit stays
		// lost. See issue #610.
		//
		// Skip the import when the resource is still dirty. The live row is
		// then the unpushed local edit, which can be newer than LocalIcal.
		// A tombstoned UID is a pending delete. Refuse it. importICal reports
		// imported=true for a payload that still has a component, then drops
		// the tombstoned UID, so the imported flag cannot detect this.
		tombstoned, err := s.engine.hasTombstone(ctx, conflict.CalendarID, conflict.Uid)
		if err != nil {
			return nil, err
		}
		if tombstoned {
			return nil, fmt.Errorf("local version cannot be restored for %q: the item is pending delete", conflict.Uid)
		}
		sr, srErr := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{
			CalendarID: conflict.CalendarID,
			Uid:        conflict.Uid,
		})
		if srErr != nil && !errors.Is(srErr, sql.ErrNoRows) {
			return nil, fmt.Errorf("get sync resource: %w", srErr)
		}
		if errors.Is(srErr, sql.ErrNoRows) || sr.Dirty == 0 {
			// The import runs before the transaction below because it flows
			// through the event/todo/journal services, which use their own
			// connection. UpsertByUID is idempotent, so if the transaction
			// fails to commit the conflict survives and the whole resolution
			// replays cleanly.
			imported, revs, importWarnings, err := s.engine.importICal(ctx, conflict.CalendarID, conflict.LocalIcal)
			if err != nil {
				return nil, fmt.Errorf("import local version: %w", err)
			}
			if !imported {
				return nil, fmt.Errorf("local version has no importable data for %q", conflict.Uid)
			}
			if _, ok := revs[conflict.Uid]; !ok {
				return nil, fmt.Errorf("local version has no importable data for %q", conflict.Uid)
			}
			warnings = importWarnings
		}
	}

	// Wrap the dirty/etag mutation and the resolution stamp in one
	// transaction so a failure can't half-resolve the conflict — e.g. dirty
	// cleared with a stale ETag but the conflict still open, which would
	// re-trigger an HTTP 412 loop on the next push. Issue #89 gap #3.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
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
			return nil, fmt.Errorf("clear dirty: %w", err)
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
			return nil, fmt.Errorf("mark dirty: %w", err)
		}
	}

	// Mark the conflict resolved instead of deleting the row. The row keeps
	// the local body, so a later `sync resolve --pick local` can still
	// restore the edit an auto-resolution discarded. See issue #610.
	resolution := ResolutionServer
	if pick == "local" {
		resolution = ResolutionLocal
	}
	if err := markConflictResolvedOn(ctx, qtx, conflict.CalendarID, conflict.Uid, resolution); err != nil {
		return nil, fmt.Errorf("mark conflict resolved: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return warnings, nil
}

// ResetCalendar clears all sync state for a calendar. Local data stays.
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
	if err := qtx.DeleteSyncPendingHrefsByCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("delete pending hrefs: %w", err)
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
