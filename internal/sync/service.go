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

// PushMove runs the pushes after a write that can move the item uid from
// fromCalendarID to toCalendarID. push runs the opportunistic push of one
// calendar for the caller (the CLI and the TUI report the result in their
// own way). The CLI and the TUI share this order:
//
//  1. push the target calendar
//  2. push the source calendar, only when the write moved the item and
//     storage.MoveSourceDeleteSafe allows it
//
// A move leaves a tombstone in the source calendar
// (storage.RecordResourceMove). The push of the target alone leaves that
// delete for the next full sync, and a server that keeps a UID unique per
// collection shows a copy in both calendars until then. The target goes
// first: on a server that keeps a UID unique per account, its push sends
// the source delete itself (issue #828). After a failed target push the
// source object is the only server copy, so its delete waits with its
// tombstone. A failed check also skips the source push.
func PushMove(ctx context.Context, q *storage.Queries, fromCalendarID, toCalendarID int64, uid string, push func(calendarID int64)) {
	push(toCalendarID)
	if fromCalendarID == 0 || fromCalendarID == toCalendarID {
		return
	}
	if safe, err := storage.MoveSourceDeleteSafe(ctx, q, fromCalendarID, uid); err != nil || !safe {
		return
	}
	push(fromCalendarID)
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
func (s *Service) DoctorPush(ctx context.Context, calendarID int64, uid string, diagnosed []string) ([]string, error) {
	return s.engine.DoctorPush(ctx, calendarID, uid, diagnosed)
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
	// StuckHrefs lists the known hrefs whose retries the pull stopped.
	// Each one keeps its local row. See issue #816.
	StuckHrefs []StuckHref
	// Waiting lists the rows whose last push failure is a wait. See
	// WaitingResource.
	Waiting []WaitingResource
	// PushFailures lists the sync rows whose last push attempt failed.
	PushFailures []PushFailure
	// UnownedEdits lists the UIDs whose edit stays local, because no series
	// owns the edited row (storage.NoSeriesOwnerMsg).
	UnownedEdits []string
}

// PushFailure is a sync row whose last push attempt failed. Count is the
// count of consecutive failed attempts, and Error the last error.
type PushFailure struct {
	UID       string
	OwnerType string
	Count     int64
	Error     string
}

// PushFailures lists the sync rows of calendarID whose last push attempt
// failed. The sync status and the sync doctor show them.
func (s *Service) PushFailures(ctx context.Context, calendarID int64) ([]PushFailure, error) {
	rows, err := s.q.ListSyncResourcePushFailures(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list push failures: %w", err)
	}
	var out []PushFailure
	for _, r := range rows {
		out = append(out, PushFailure{UID: r.Uid, OwnerType: r.OwnerType, Count: r.PushFailCount, Error: r.LastPushError})
	}
	return out, nil
}

// UnownedEdits lists the UIDs of calendarID whose edit stays local, because
// no series owns the edited row (storage.NoSeriesOwnerMsg). The sync status
// and the sync doctor show them.
func (s *Service) UnownedEdits(ctx context.Context, calendarID int64) ([]string, error) {
	uids, err := s.q.ListUnownedEditUIDs(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list unowned edits: %w", err)
	}
	return uids, nil
}

// LegacyOverride names the live override rows of one UID in a calendar with
// no master of the UID (see storage.LegacyOverride). OwnerCalendarID names
// the calendar of the live series that owns them. ID is the lowest row ID.
// A move of that row to the owner calendar takes the other Rows with it.
type LegacyOverride struct {
	ID              int64
	UID             string
	OwnerCalendarID int64
	Rows            int64
}

// LegacyOverrides lists the UIDs of calendarID with a live legacy override
// that a live series owns. The startup heal moves the rows of a local
// calendar home, so the list holds the rows that it keeps: mostly the rows
// of a linked calendar. The sync doctor shows them.
func (s *Service) LegacyOverrides(ctx context.Context, calendarID int64) ([]LegacyOverride, error) {
	rows, err := s.q.ListLegacyEventOverrideUIDsInCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list legacy overrides: %w", err)
	}
	out := make([]LegacyOverride, 0, len(rows))
	for _, r := range rows {
		out = append(out, LegacyOverride{ID: r.ID, UID: r.Uid, OwnerCalendarID: r.OwnerCalendarID, Rows: r.RowCount})
	}
	return out, nil
}

// WaitingResource is a sync row whose push waits for another step, for
// example the resolution of a move conflict. A wait is no failure. Reason
// names the step. The list derives it from the kind of the wait and the
// name of the holder calendar. See issue #841.
type WaitingResource struct {
	CalendarID int64
	UID        string
	OwnerType  string
	Reason     string
}

// WaitingResources lists the rows of every calendar whose push waits, in
// one query. A wait whose cause ended does not show.
func (s *Service) WaitingResources(ctx context.Context) ([]WaitingResource, error) {
	rows, err := s.q.ListWaitingSyncResources(ctx)
	if err != nil {
		return nil, fmt.Errorf("list waiting resources: %w", err)
	}
	out := make([]WaitingResource, 0, len(rows))
	for _, r := range rows {
		out = append(out, WaitingResource{
			CalendarID: r.CalendarID,
			UID:        r.Uid,
			OwnerType:  r.OwnerType,
			Reason:     waitReason(r.WaitKind, r.HolderName),
		})
	}
	return out, nil
}

// StuckHref is a known href whose server body the pull cannot import
// after the retry budget. Reason is "missing" (multiget 404) or
// "unparseable" (the ical decoder rejects the body).
type StuckHref struct {
	Href     string
	UID      string
	Reason   string
	GaveUpAt string // RFC 3339
}

// waitReason derives the reason text of a wait from its kind and the name
// of its holder calendar. The list query reads the name in the same row.
func waitReason(kind int64, holderName string) string {
	if kind == waitKindHeld {
		return heldWaitReason(holderName)
	}
	return sourceDeleteWaitReason
}

// Status returns sync status for all synced calendars.
func (s *Service) Status(ctx context.Context) ([]SyncStatus, error) {
	cals, err := s.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	// Like the dirty and conflict counts, a read failure shows no stuck
	// hrefs. It does not fail the status.
	stuckRows, _ := s.q.ListGaveUpSyncKnownMisses(ctx)
	stuckByCalendar := make(map[int64][]StuckHref)
	for _, r := range stuckRows {
		stuckByCalendar[r.CalendarID] = append(stuckByCalendar[r.CalendarID], StuckHref{
			Href:     r.Href,
			UID:      r.Uid,
			Reason:   r.Reason,
			GaveUpAt: storage.NullableToString(r.GaveUpAt),
		})
	}

	// Like the stuck hrefs, a read failure shows no waits.
	waits, _ := s.WaitingResources(ctx)
	waitsByCalendar := make(map[int64][]WaitingResource)
	for _, w := range waits {
		waitsByCalendar[w.CalendarID] = append(waitsByCalendar[w.CalendarID], w)
	}

	var statuses []SyncStatus
	for _, cal := range cals {
		if !storage.CalendarLinked(cal) {
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

		// Like the other counts, a read failure shows no unowned edits and
		// no push failures.
		unowned, _ := s.UnownedEdits(ctx, cal.ID)
		failures, _ := s.PushFailures(ctx, cal.ID)
		statuses = append(statuses, SyncStatus{
			CalendarID:          cal.ID,
			CalendarName:        cal.Name,
			LastSyncToken:       storage.NullableToString(cal.SyncToken),
			LastSyncAt:          storage.NullableToString(cal.LastSyncAt),
			LastSyncAttemptedAt: storage.NullableToString(cal.LastSyncAttemptedAt),
			LastSyncError:       storage.NullableToString(cal.LastSyncError),
			PendingPush:         len(dirty),
			Conflicts:           len(conflicts),
			StuckHrefs:          stuckByCalendar[cal.ID],
			Waiting:             waitsByCalendar[cal.ID],
			PushFailures:        failures,
			UnownedEdits:        unowned,
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
	// MoveSourceCalendarID names the source calendar of a move conflict. The
	// user moved the item to CalendarID, and the source object changed on the
	// server before the source DELETE (issue #841). It is 0 for an edit
	// conflict.
	MoveSourceCalendarID int64
	// MoveState is the state of the move of a move conflict (see
	// MoveStateOpen and the other constants). It is empty for an edit
	// conflict.
	MoveState string
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
	if r.MoveSourceCalendarID != nil {
		c.MoveSourceCalendarID = *r.MoveSourceCalendarID
	}
	c.MoveState = r.MoveState
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

// ResolveResult is the outcome of a conflict pick. MoveFinished reports a
// move that the pick finished because its source object is gone (issue
// #841). The pick did not apply the picked side then. Note says what
// remains, for a finished move and for a pick that ran with a note.
type ResolveResult struct {
	Warnings     []ImportWarning
	MoveFinished bool
	UID          string
	Note         string
}

// ResolveConflictOutcome is ResolveConflict with the outcome of the pick.
func (s *Service) ResolveConflictOutcome(ctx context.Context, conflictID int64, pick string) (ResolveResult, error) {
	res, err := s.resolveConflict(ctx, conflictID, pick)
	return ResolveResult{Warnings: res.Warnings, MoveFinished: res.Finished, UID: res.UID, Note: res.Note}, err
}

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
	res, err := s.ResolveConflictOutcome(ctx, conflictID, pick)
	if res.Note != "" {
		// The note of a pick goes to this caller as one more warning.
		return append(res.Warnings, ImportWarning{UID: res.UID, Message: res.Note}), err
	}
	return res.Warnings, err
}

// resolveConflict runs the pick. See ResolveConflict.
func (s *Service) resolveConflict(ctx context.Context, conflictID int64, pick string) (moveResolution, error) {
	if pick != "server" && pick != "local" {
		return moveResolution{}, fmt.Errorf("invalid pick: %q (use 'local' or 'server')", pick)
	}

	conflict, err := s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return moveResolution{}, fmt.Errorf("get conflict: %w", err)
	}
	if conflict.MoveSourceCalendarID != nil {
		// A move conflict touches two calendars. Take both lifecycle locks
		// up front, in one order, so two resolves in opposite directions
		// cannot block each other (issue #841). A move back also rewrites
		// the tombstones of third calendars that name the target, so their
		// locks come with it (storage.MoveResolveLockSet).
		lockedSource, lockedTarget := *conflict.MoveSourceCalendarID, conflict.CalendarID
		release, err := s.engine.lockMoveResolve(ctx, lockedSource, lockedTarget, conflict.Uid)
		if err != nil {
			return moveResolution{}, fmt.Errorf("lock move calendars lifecycle: %w", err)
		}
		defer release()
		if s.engine.testHooks != nil && s.engine.testHooks.afterConflictLock != nil {
			s.engine.testHooks.afterConflictLock()
		}
		conflict, err = s.q.GetSyncConflict(ctx, conflictID)
		if err != nil {
			return moveResolution{}, fmt.Errorf("revalidate conflict: %w", err)
		}
		// The locks cover the two calendars read before the wait. A row
		// that names other calendars now needs other locks.
		if conflict.MoveSourceCalendarID == nil || *conflict.MoveSourceCalendarID != lockedSource || conflict.CalendarID != lockedTarget {
			return moveResolution{}, fmt.Errorf("conflict %d changed while it waited for the lock; run chroncal sync conflicts again", conflictID)
		}
		// An account delete keeps the move conflicts of its calendars, so a
		// re-link resumes them. Until then no move pick can reach a server.
		// The check runs after the locks and the re-read, so an account
		// delete during the wait counts. An edit-conflict pick is local
		// only, so it has no such check (issue #841).
		for _, id := range []int64{lockedTarget, lockedSource} {
			if err := s.refuseUnlinkedCalendar(ctx, id); err != nil {
				return moveResolution{}, err
			}
		}
		return s.resolveMoveConflict(ctx, conflict, pick)
	}
	lockedCalendar := conflict.CalendarID
	release, err := s.engine.lockCalendars(ctx, lockedCalendar)
	if err != nil {
		return moveResolution{}, fmt.Errorf("lock conflict calendar lifecycle: %w", err)
	}
	defer release()
	if s.engine.testHooks != nil && s.engine.testHooks.afterConflictLock != nil {
		s.engine.testHooks.afterConflictLock()
	}
	conflict, err = s.q.GetSyncConflict(ctx, conflictID)
	if err != nil {
		return moveResolution{}, fmt.Errorf("revalidate conflict: %w", err)
	}
	if conflict.MoveSourceCalendarID != nil || conflict.CalendarID != lockedCalendar {
		return moveResolution{}, fmt.Errorf("conflict %d changed while it waited for the lock; run chroncal sync conflicts again", conflictID)
	}

	// serverRev captures the sync_resources rev right after the accept-server
	// import so the dirty clear below can be made conditional on it (see the
	// "server" case in the transaction). Unused for the "local" pick.
	var serverRev int64
	var warnings []ImportWarning
	// An edit conflict on a create (the row has no server object, and the
	// record names an object) adopts the object at the planned href.
	var createConflictHref string
	if pick == "server" && conflict.ServerEtag != "" {
		sr, err := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: conflict.CalendarID, Uid: conflict.Uid})
		found, err := storage.Found(err)
		if err != nil {
			return moveResolution{}, fmt.Errorf("get sync resource: %w", err)
		}
		if found && sr.RemoteUrl == "" {
			if createConflictHref, err = s.engine.plannedHref(ctx, conflict.CalendarID, conflict.Uid); err != nil {
				return moveResolution{}, fmt.Errorf("planned href of %q: %w", conflict.Uid, err)
			}
		}
	}
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
			return moveResolution{}, fmt.Errorf("import server version: %w", err)
		}
		if !imported {
			// ImportFile returns no error for empty or component-less iCal, so a
			// blank ServerIcal would otherwise clear dirty and stamp the server
			// ETag onto the unchanged local row — the silent-overwrite bug this
			// branch exists to prevent. Refuse instead of resolving falsely.
			return moveResolution{}, fmt.Errorf("server version has no importable data for %q", conflict.Uid)
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
		return resolutionOf(s.restoreLocalVersion(ctx, conflict.CalendarID, conflict.CalendarID, conflict.Uid, conflict.LocalIcal, conflict.ServerEtag, nil))
	}

	// Wrap the dirty/etag mutation and the resolution stamp in one
	// transaction so a failure can't half-resolve the conflict — e.g. dirty
	// cleared with a stale ETag but the conflict still open, which would
	// re-trigger an HTTP 412 loop on the next push. Issue #89 gap #3.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return moveResolution{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)

	// Adopt the server ETag and clear the pending local push now that the
	// local row reflects the server version. Do this only when no local edit
	// landed after the capture of serverRev. FinalizePushedResource always
	// advances the ETag, but it clears dirty only on rev == serverRev. A
	// concurrent edit (which bumped rev) then keeps dirty=1 and survives to
	// the next push. The unconditional ClearSyncResourceDirty used here before
	// wiped that edit — issues #92/#417/#466.
	//
	// An edit conflict on a create: the planned href held the object that the
	// pick adopts. The same statement records that href, as the server-wins
	// pass does. A move tombstone then does not wait on a row that never
	// pushes (issue #841). An empty href keeps the row's href.
	if _, err := qtx.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
		CalendarID: conflict.CalendarID,
		Uid:        conflict.Uid,
		Etag:       conflict.ServerEtag,
		Rev:        serverRev,
		RemoteUrl:  createConflictHref,
	}); err != nil {
		return moveResolution{}, fmt.Errorf("clear dirty: %w", err)
	}

	// Mark the conflict resolved instead of deleting the row. The row keeps
	// the local body, so a later `sync resolve --pick local` can still
	// restore the edit an auto-resolution discarded. See issue #610.
	if err := markConflictResolvedOn(ctx, qtx, conflict.CalendarID, conflict.Uid, ResolutionServer); err != nil {
		return moveResolution{}, fmt.Errorf("mark conflict resolved: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return moveResolution{}, err
	}
	return moveResolution{Warnings: warnings}, nil
}

// restoreMove holds the move fields of a local pick on a move conflict.
type restoreMove struct {
	// newState, when not empty, sets the state of the move conflict in the
	// transaction of the resolution.
	newState string
	// source is the source calendar of the move conflict.
	source *int64
	// cancelRev is the rev of the source row after the cancel.
	cancelRev int64
	// cancelRowID is the id of that source row, or 0 when the cancel
	// recorded no id. A re-created row has a new id.
	cancelRowID int64
	// strictRev refuses any source rev other than cancelRev, clean or
	// dirty (a cancelled move).
	strictRev bool
	// probeGone, when not nil, reports whether the source object is gone
	// from the server. The restore calls it after its local refusals and
	// before the import, and stores the answer in sourceGone. An error of
	// the probe refuses the restore, which then writes nothing.
	probeGone func(ctx context.Context) (gone bool, err error)
	// sourceGone marks a source object that is gone from the server. The
	// row then drops its href and its etag in the transaction of the
	// resolution, and the next push creates the item.
	sourceGone bool
}

// restoreLocalVersion is the local pick. It writes the recorded local body
// localIcal into calendarID and marks the resource dirty with etag, so the
// next push sends it. move is nil for an edit conflict. For a move conflict,
// the restore imports the recorded body even into a dirty row from the
// cancel. It refuses a dirty row that the user edited after the cancel. The
// conflict row lives on conflictCalendarID and gets the resolution stamp. An
// edit conflict restores into its own calendar. The local pick on a
// cancelled move restores into the source calendar, where the rows are then
// (issue #841).
//
// The current row may no longer hold the body: a server-wins pass (or any
// later import) replaces it after the record. Without the restore, the dirty
// mark pushes the server content again and the local edit stays lost. See
// issue #610. The function skips the import when the resource is still
// dirty. The live row is then the unpushed local edit, which can be newer
// than localIcal. A tombstoned UID is a pending delete, and the pick refuses
// it. importICal reports imported=true for a payload that still has a
// component and then drops the tombstoned UID, so the imported flag cannot
// detect this.
//
// The push sends If-Match with etag. The etag of the record keeps the
// concurrency check intact. The push succeeds when the server is unchanged.
// It gets 412 and records a fresh conflict when the server changed again
// after the record. The stored etag of the row can be the value that already
// failed If-Match, and that value gets 412 without end. A row without a
// server object keeps an empty etag, with one exception. The row of an edit
// conflict on a create, or of an edit conflict whose row went away, takes
// the planned href and the etag.
func (s *Service) restoreLocalVersion(ctx context.Context, conflictCalendarID, calendarID int64, uid, localIcal, etag string, move *restoreMove) ([]ImportWarning, error) {
	_, err := s.q.GetTombstoneByCalendarAndUID(ctx, storage.GetTombstoneByCalendarAndUIDParams{CalendarID: calendarID, Uid: uid})
	tombstoned, err := storage.Found(err)
	if err != nil {
		return nil, fmt.Errorf("get tombstone: %w", err)
	}
	if tombstoned {
		return nil, fmt.Errorf("local version cannot be restored for %q: the item is pending delete", uid)
	}
	sr, srErr := s.q.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if srErr != nil && !errors.Is(srErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("get sync resource: %w", srErr)
	}
	// An edit conflict on a create: the row existed before the pick, and it
	// has no server object. The record holds the etag of the object at its
	// planned href. An edit conflict whose row went away takes the same
	// href: a create with If-None-Match gets 412 from the object that the
	// record names, so an empty etag would record the conflict again. A
	// conditional update of a gone object gets 412 too, and the push then
	// creates the object again (issue #866).
	var createConflictHref string
	rowGone := errors.Is(srErr, sql.ErrNoRows) && move == nil
	if (rowGone || (srErr == nil && sr.RemoteUrl == "")) && etag != "" && conflictCalendarID == calendarID {
		href, err := s.engine.plannedHref(ctx, calendarID, uid)
		switch {
		case err == nil:
			createConflictHref = href
		case !rowGone:
			return nil, fmt.Errorf("planned href of %q: %w", uid, err)
		}
		// A gone row of a calendar without a remote has no planned href. It
		// keeps the rule of an empty etag: no push sends it.
	}
	var warnings []ImportWarning
	// A restore of a move conflict imports the recorded body even into a dirty
	// source row when the dirt comes from the cancel itself. The user picked
	// it. A source without a server object stays dirty after the cancel. A
	// dirty row with another rev holds an edit after the cancel, and the
	// restore refuses to overwrite it. With strictRev (a cancelled move), any
	// other rev refuses, clean or dirty: a pushed edit or a pull after the
	// cancel changed the source.
	changed := move != nil && srErr == nil && (sr.Rev != move.cancelRev || (move.cancelRowID != 0 && sr.ID != move.cancelRowID))
	if changed && (sr.Dirty != 0 || move.strictRev) {
		return nil, fmt.Errorf("the item %q changed after the cancel; the recorded version stays in the conflict", uid)
	}
	// Every local refusal ran above. Only now does a move restore probe the
	// source object, so a refused pick sends no request.
	if move != nil && move.probeGone != nil {
		gone, err := move.probeGone(ctx)
		if err != nil {
			return nil, err
		}
		move.sourceGone = gone
	}
	if errors.Is(srErr, sql.ErrNoRows) || sr.Dirty == 0 || move != nil {
		// The import runs before the transaction below because it flows
		// through the event/todo/journal services, which use their own
		// connection. UpsertByUID is idempotent, so if the transaction
		// fails to commit the conflict survives and the whole resolution
		// replays cleanly.
		imported, revs, importWarnings, err := s.engine.importICal(ctx, calendarID, localIcal)
		if err != nil {
			return nil, fmt.Errorf("import local version: %w", err)
		}
		if _, ok := revs[uid]; !imported || !ok {
			return nil, fmt.Errorf("local version has no importable data for %q", uid)
		}
		warnings = importWarnings
	}

	// The dirty mark and the resolution stamp commit together, so a failure
	// cannot half-resolve the conflict (issue #89 gap #3).
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	qtx := s.q.WithTx(tx)
	// A row without a server object keeps an empty etag: a create PUT
	// with If-Match fails. Only a row with a server object takes the etag
	// of the record. The import above can create the row, so the check
	// reads the row again.
	after, afterErr := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{CalendarID: calendarID, Uid: uid})
	if afterErr != nil && !errors.Is(afterErr, sql.ErrNoRows) {
		return nil, fmt.Errorf("get sync resource after the import: %w", afterErr)
	}
	switch {
	case move != nil && move.sourceGone && afterErr == nil:
		if err := qtx.ClearSyncResourceRemote(ctx, storage.ClearSyncResourceRemoteParams{CalendarID: calendarID, Uid: uid}); err != nil {
			return nil, fmt.Errorf("drop the href of the gone source: %w", err)
		}
	case createConflictHref != "" && afterErr == nil && after.RemoteUrl == "":
		// An edit conflict on a create: the planned href held an object.
		// The row names that object with the recorded etag, so the push is
		// a conditional update and a newer remote edit gets 412.
		if err := qtx.SetSyncResourceCreateConflictRef(ctx, storage.SetSyncResourceCreateConflictRefParams{
			RemoteUrl: createConflictHref, Etag: etag, CalendarID: calendarID, Uid: uid,
		}); err != nil {
			return nil, fmt.Errorf("mark dirty: %w", err)
		}
	case afterErr != nil || after.RemoteUrl == "":
		if err := qtx.MarkSyncResourceDirty(ctx, storage.MarkSyncResourceDirtyParams{CalendarID: calendarID, Uid: uid}); err != nil {
			return nil, fmt.Errorf("mark dirty: %w", err)
		}
	default:
		if err := qtx.MarkSyncResourceDirtyWithEtag(ctx, storage.MarkSyncResourceDirtyWithEtagParams{
			CalendarID: calendarID,
			Uid:        uid,
			Etag:       etag,
		}); err != nil {
			return nil, fmt.Errorf("mark dirty: %w", err)
		}
	}
	if err := markConflictResolvedOn(ctx, qtx, conflictCalendarID, uid, ResolutionLocal); err != nil {
		return nil, fmt.Errorf("mark conflict resolved: %w", err)
	}
	// The state of a move conflict commits with the resolution, so a
	// failure cannot leave a resolved row in its old state.
	if move != nil && move.newState != "" {
		row := storage.SyncConflict{CalendarID: conflictCalendarID, Uid: uid, MoveSourceCalendarID: move.source}
		if err := setMoveState(ctx, qtx, row, move.newState); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return warnings, nil
}

// ResetCalendar clears all sync state for a calendar. Local data stays.
// The next sync will perform a full initial sync. Two kinds of sync state
// stay (issue #841):
//   - the move tombstones of live moves from the calendar, with their sync
//     rows, so their source DELETE still goes out
//   - the sync rows of the moved copies in the calendar that a move
//     tombstone names. An unpushed copy keeps its create, and a landed copy
//     keeps its href and etag.
//
// A calendar with an open move conflict, as the target or as the source,
// refuses the reset.
//
// The reset runs under the locks of the calendars that a move links to
// it.
func (s *Service) ResetCalendar(ctx context.Context, calendarID int64) error {
	release, err := s.engine.lockCalendarWithMoves(ctx, calendarID)
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
	// The reset deletes the conflicts of the calendar. An open move
	// conflict on it, or one whose source it is, holds a move that the user
	// must settle first. So the reset refuses (issue #841).
	open, err := qtx.CountOpenMoveConflictsTouchingCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("count the open move conflicts: %w", err)
	}
	if open > 0 {
		return fmt.Errorf("calendar %q has open move conflicts; resolve them first (chroncal sync conflicts)", storage.CalendarLabel(ctx, qtx, calendarID))
	}
	if err := qtx.DeleteTombstonesForReset(ctx, calendarID); err != nil {
		return fmt.Errorf("delete tombstones: %w", err)
	}
	// A moved copy without a sync row gets one first. The delete below then
	// keeps the rows of the copies that a move tombstone names. So a landed
	// copy keeps its href and etag, and no second create goes out (issue
	// #841).
	if err := qtx.MarkMovedCopiesDirtyInCalendar(ctx, calendarID); err != nil {
		return fmt.Errorf("mark the moved copies dirty: %w", err)
	}
	if err := qtx.DeleteSyncResourcesForReset(ctx, calendarID); err != nil {
		return fmt.Errorf("delete sync resources: %w", err)
	}
	if err := qtx.DeleteSyncHrefRetriesByCalendar(ctx, calendarID); err != nil {
		return err
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

// refuseUnlinkedCalendar refuses a pick when calendarID lost its account to
// an account delete. Such a calendar keeps its remote_url for a re-link,
// so it differs from a local calendar, which has none.
func (s *Service) refuseUnlinkedCalendar(ctx context.Context, calendarID int64) error {
	cal, err := s.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar %d: %w", calendarID, err)
	}
	if storage.CalendarUnlinkedByAccountDelete(cal) {
		return fmt.Errorf("calendar %q is not linked; add the account again or unlink the calendar", cal.Name)
	}
	return nil
}
