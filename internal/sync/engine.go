package sync

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	gosync "sync"
	"time"

	"github.com/emersion/go-ical"
	"github.com/google/uuid"
	"golang.org/x/sync/errgroup"

	authpkg "github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
	"github.com/douglasdemoura/chroncal/internal/synclock"
	"github.com/douglasdemoura/chroncal/internal/todo"
)

// SyncResult holds the outcome of a sync cycle for one calendar.
type SyncResult struct {
	CalendarID int64
	Pushed     int
	Pulled     int
	Deleted    int
	// Conflicts counts conflicts this cycle recorded and left open for
	// manual resolution. Every increment is backed by a sync_conflicts row.
	Conflicts int
	// AutoResolved counts conflicts this cycle settled without the user: a
	// server-wins pass that adopted the server body, and a tombstone delete
	// that yielded to a remote edit. The recorded rows stay recoverable.
	AutoResolved int
	// SkippedConflicts counts dirty resources a full prompt-mode pass left
	// unpushed because an open conflict already exists for them.
	SkippedConflicts int
	Errors           []error
	// Warnings lists what ImportFile could not represent faithfully while
	// absorbing server resources during this cycle. It rides on the result —
	// not just the engine logger — because most entry points run the engine
	// with a discarded logger (opportunistic pushes, TUI syncs, the first
	// pull after linking), and the fabricated substitute does not stay
	// local: the next local edit marks the resource dirty and pushes it back
	// over the value the server still holds correctly.
	Warnings []ImportWarning
}

// ImportWarning is one thing ImportFile could not represent faithfully
// during import of a server resource. Examples: a malformed DTEND replaced
// by a fabricated span, or an alarm dropped for an unusable trigger.
type ImportWarning struct {
	// Path is the remote resource path the payload came from; empty when the
	// payload was not fetched by path (conflict-resolution bodies).
	Path string
	// UID names the component only when every component in the payload
	// shares one nonempty UID (a single component, or a recurring master
	// plus its overrides). Payloads spanning several UIDs leave it empty
	// rather than blame the first component for a warning that may belong
	// to another.
	UID string
	// Message is the ImportFile warning text.
	Message string
}

// String renders the warning as "<location>: <message>" for one-line display.
func (w ImportWarning) String() string {
	switch {
	case w.Path != "" && w.UID != "":
		return w.Path + " (uid " + w.UID + "): " + w.Message
	case w.Path != "":
		return w.Path + ": " + w.Message
	case w.UID != "":
		return "uid " + w.UID + ": " + w.Message
	}
	return w.Message
}

// ConflictStrategy determines how to handle conflicts.
type ConflictStrategy string

const (
	ConflictServerWins ConflictStrategy = "server-wins"
	ConflictPrompt     ConflictStrategy = "prompt"
)

// ParseConflictStrategy maps a configured strategy name to its constant. An
// empty name means the default, server-wins. Any other name is an error.
func ParseConflictStrategy(s string) (ConflictStrategy, error) {
	switch ConflictStrategy(s) {
	case "", ConflictServerWins:
		return ConflictServerWins, nil
	case ConflictPrompt:
		return ConflictPrompt, nil
	default:
		return "", fmt.Errorf("invalid conflict strategy %q (use %q or %q)",
			s, ConflictServerWins, ConflictPrompt)
	}
}

// Resolution markers recorded on sync_conflicts.resolution. A resolved row
// keeps its local body so `sync resolve --pick local` can still restore it.
const (
	// ResolutionServer marks a conflict the user resolved for the server
	// version.
	ResolutionServer = "server"
	// ResolutionLocal marks a conflict the user resolved for the local
	// version.
	ResolutionLocal = "local"
	// ResolutionServerAuto marks a conflict a server-wins pass resolved by
	// adopting the server version.
	ResolutionServerAuto = "server-auto"
)

// resolvedAtFormat stamps every resolution with the same shape. One format
// keeps the timestamps comparable across engine and service paths.
const resolvedAtFormat = "2006-01-02T15:04:05Z"

// markConflictResolvedOn stamps the recorded conflict as resolved through
// q. The row stays in place so its local body remains recoverable through
// ResolveConflict. Pass e.q for the engine paths or a transaction-bound
// handle for the service path; the stamp must commit with the caller's
// other writes.
func markConflictResolvedOn(ctx context.Context, q *storage.Queries, calendarID int64, uid, resolution string) error {
	resolvedAt := time.Now().UTC().Format(resolvedAtFormat)
	return q.MarkSyncConflictResolved(ctx, storage.MarkSyncConflictResolvedParams{
		ResolvedAt: &resolvedAt,
		Resolution: &resolution,
		CalendarID: calendarID,
		Uid:        uid,
	})
}

// markConflictResolved stamps the recorded conflict as resolved on the
// engine's query handle.
func (e *Engine) markConflictResolved(ctx context.Context, calendarID int64, uid, resolution string) error {
	return markConflictResolvedOn(ctx, e.q, calendarID, uid, resolution)
}

func (e *Engine) hasTombstone(ctx context.Context, calendarID int64, uid string) (bool, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return false, fmt.Errorf("list tombstones: %w", err)
	}
	for _, ts := range tombstones {
		if ts.Uid == uid {
			return true, nil
		}
	}
	return false, nil
}

// hasOpenConflict reports whether uid still has an unresolved conflict row.
// A lookup error returns true so a pull does not import the server body
// over a local edit it cannot confirm is free of a conflict.
func (e *Engine) hasOpenConflict(ctx context.Context, calendarID int64, uid string) bool {
	open, err := e.q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{
		CalendarID: calendarID,
		Uid:        uid,
	})
	if err != nil {
		e.logger.Error("check open conflict", "uid", uid, "error", err)
		return true
	}
	return open > 0
}

// refreshConflictServerBody records the freshest server body on the open
// conflict row. Pull does not import over an open conflict, so the row
// would hold the server version from conflict time. A resolve must then
// pick current data, not stale data. The update touches only rows that
// are still open. See issue #610.
func (e *Engine) refreshConflictServerBody(ctx context.Context, calendarID int64, uid, serverIcal, serverEtag string) {
	n, err := e.q.UpdateSyncConflictServerBody(ctx, storage.UpdateSyncConflictServerBodyParams{
		ServerIcal: serverIcal,
		ServerEtag: serverEtag,
		CalendarID: calendarID,
		Uid:        uid,
	})
	if err != nil {
		e.logger.Warn("refresh conflict server body", "uid", uid, "error", err)
	} else if n > 0 {
		e.logger.Debug("refreshed conflict server body", "uid", uid)
	}
}

// Engine orchestrates push and pull of CalDAV resources.
type Engine struct {
	db        *sql.DB
	q         *storage.Queries
	credStore authpkg.CredentialStore
	calendars *calendar.Service
	events    *event.Service
	todos     *todo.Service
	journals  *journal.Service
	logger    *slog.Logger
}

type pushLockKey struct {
	db         *sql.DB
	calendarID int64
}

var (
	pushLocksMu gosync.Mutex
	pushLocks   = map[pushLockKey]*gosync.Mutex{}
)

// pushLock returns the per-calendar mutex that serializes the push phase for
// calendarID. It creates the mutex on first use.
//
// Concurrent push runs for the same calendar must not both read the same
// dirty, never-pushed sync_resource (RemoteUrl=""). One example is a
// save-time PushLocalEdits that races a periodic SyncCalendar. Each run would
// mint a distinct random href and PUT it without an If-Match precondition.
// The server would then hold two objects for one UID.
//
// CalDAV servers key dedup on href, not UID. An If-None-Match:* guard would
// not catch this: the two hrefs differ. Serialize the phase. The first run
// then records the remote_url and clears the dirty flag before the second
// reads it.
//
// This guards only same-process concurrency. Two CLI processes that push the
// same calendar at once can still race. See issue #225.
func (e *Engine) pushLock(calendarID int64) *gosync.Mutex {
	key := pushLockKey{db: e.db, calendarID: calendarID}
	pushLocksMu.Lock()
	defer pushLocksMu.Unlock()
	lock, ok := pushLocks[key]
	if !ok {
		lock = &gosync.Mutex{}
		pushLocks[key] = lock
	}
	return lock
}

var syncRetryOptions = caldav.RetryOptions{
	MaxAttempts: 3,
}

var newRemoteObjectName = func() string {
	return uuid.NewString() + ".ics"
}

// normalizeRemoteRef normalizes a remote reference. It cleans the escaped
// path form. A percent-encoded separator ("%2F") then stays inside one
// object name. The decoded form would treat "%2F" as '/', collapse
// dot-segments inside one object name, and merge two distinct objects
// into one reference.
func normalizeRemoteRef(ref string) string {
	if ref == "" {
		return ""
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}

	escaped := parsed.EscapedPath()
	if escaped != "" {
		trailingSlash := strings.HasSuffix(escaped, "/")
		cleaned := path.Clean(escaped)
		switch {
		case cleaned == "." && trailingSlash:
			cleaned = "/"
		case trailingSlash && cleaned != "/":
			cleaned += "/"
		}
		if cleaned != escaped {
			if decoded, derr := url.PathUnescape(cleaned); derr == nil {
				parsed.Path = decoded
				parsed.RawPath = cleaned
			}
		}
	}

	return parsed.String()
}

// buildRemoteResourcePath derives the PUT href for a first-time push. The
// name comes from the UID, so a lost bookkeeping write cannot create a
// second object for the same UID on a later push. An empty UID uses a
// random name.
func buildRemoteResourcePath(calendarRef, uid string) string {
	name := ""
	if uid != "" {
		name = sanitizeRemoteObjectName(uid)
	} else {
		name = newRemoteObjectName()
	}

	parsed, err := url.Parse(calendarRef)
	if err != nil {
		return normalizeRemoteRef(strings.TrimRight(calendarRef, "/") + "/" + name)
	}

	basePath := parsed.Path
	if basePath == "" {
		basePath = "/"
	}
	parsed.Path = path.Join(basePath, name)
	return normalizeRemoteRef(parsed.String())
}

// sanitizeRemoteObjectName derives a single path segment from a UID. It
// percent-encodes the bytes that change the path structure or the escape
// pass: '/', '?', '#', and '%'. The mapping is injective, so two UIDs that
// differ only in those bytes cannot map to the same remote object. The URL
// encoder escapes the '%' of each escape sequence once more.
// CanonicalObjectRef removes that extra escape, so the PUT href keeps each
// escape exactly once.
func sanitizeRemoteObjectName(uid string) string {
	var b strings.Builder
	b.Grow(len(uid) + len(".ics"))
	for i := range len(uid) {
		switch uid[i] {
		case '/':
			b.WriteString("%2F")
		case '?':
			b.WriteString("%3F")
		case '#':
			b.WriteString("%23")
		case '%':
			b.WriteString("%25")
		default:
			b.WriteByte(uid[i])
		}
	}
	b.WriteString(".ics")
	return b.String()
}

// NewEngine creates a new sync engine. A nil logger disables logs.
// Several callers run while the TUI owns the terminal. A fall back to
// slog's stderr handler would print over the display. Callers that
// want sync logs must pass a logger explicitly.
func NewEngine(db *sql.DB, q *storage.Queries, credStore authpkg.CredentialStore, calendars *calendar.Service, events *event.Service, todos *todo.Service, journals *journal.Service, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Engine{db: db, q: q, credStore: credStore, calendars: calendars, events: events, todos: todos, journals: journals, logger: logger}
}

// loadCalendarClient loads the calendar, its account, and a ready CalDAV client.
// It returns the calendar row, its account, and the remote calendar URL with
// the client so callers can reuse them without a second query.
func (e *Engine) loadCalendarClient(ctx context.Context, calendarID int64) (storage.Calendar, storage.Account, *caldav.Client, string, error) {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return storage.Calendar{}, storage.Account{}, nil, "", fmt.Errorf("get calendar: %w", err)
	}
	if cal.AccountID == nil || *cal.AccountID == 0 {
		return cal, storage.Account{}, nil, "", fmt.Errorf("calendar %d is not linked to an account", calendarID)
	}
	account, err := e.q.GetAccount(ctx, *cal.AccountID)
	if err != nil {
		return cal, storage.Account{}, nil, "", fmt.Errorf("get account: %w", err)
	}
	fingerprint := authpkg.AccountFingerprint(account.ServerUrl, account.AuthType, account.Username)
	cred, err := e.credStore.Get(account.ID, fingerprint)
	if err != nil {
		return cal, account, nil, "", fmt.Errorf("get credentials: %w", err)
	}
	client, err := caldav.NewClientFromCredential(account.ServerUrl, cred, func(updated authpkg.Credential) error {
		updated.AccountID = account.ID
		updated.AccountFingerprint = fingerprint
		return e.credStore.Set(updated)
	})
	if err != nil {
		return cal, account, nil, "", fmt.Errorf("create client: %w", err)
	}
	remoteURL := storage.NullableToString(cal.RemoteUrl)
	if remoteURL == "" {
		return cal, account, nil, "", fmt.Errorf("calendar %d has no remote URL", calendarID)
	}
	return cal, account, client, remoteURL, nil
}

// lockCalendarLifecycle acquires locks in account-then-calendar order and
// revalidates the association after the wait. A concurrent relink can change
// the account between the initial lookup and lock acquisition. A retry makes
// sure the returned lock always covers the account loadCalendarClient uses.
func (e *Engine) lockCalendarLifecycle(ctx context.Context, calendarID int64) (func(), error) {
	for {
		before, err := e.q.GetCalendar(ctx, calendarID)
		if err != nil {
			return nil, fmt.Errorf("get calendar for lifecycle lock: %w", err)
		}
		lockID := nullableID(before.AccountID)
		if lockID == 0 {
			lockID = -calendarID
		}
		releaseAccount, err := synclock.Account(ctx, e.db, lockID)
		if err != nil {
			return nil, fmt.Errorf("lock sync account: %w", err)
		}

		calendarLock := synclock.Calendar(e.db, calendarID)
		calendarLock.Lock()
		after, err := e.q.GetCalendar(ctx, calendarID)
		if err != nil {
			calendarLock.Unlock()
			releaseAccount()
			return nil, fmt.Errorf("revalidate calendar lifecycle lock: %w", err)
		}
		afterLockID := nullableID(after.AccountID)
		if afterLockID == 0 {
			afterLockID = -calendarID
		}
		if afterLockID != lockID {
			calendarLock.Unlock()
			releaseAccount()
			continue
		}
		return func() {
			calendarLock.Unlock()
			releaseAccount()
		}, nil
	}
}
func nullableID(id *int64) int64 {
	if id == nil {
		return 0
	}
	return *id
}

// SyncCalendar runs a full sync cycle for one calendar.
func (e *Engine) SyncCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (result *SyncResult, err error) {
	release, err := e.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	defer release()
	// Register the health-update defer before loading the client so that an
	// early return from loadCalendarClient (missing credentials, no linked
	// account, empty RemoteUrl) still records the failed attempt — otherwise
	// LastSyncError stays stale and the ambient ⚠ glyph never lights up for a
	// permanently failing calendar (issue #416).
	attemptedAt := time.Now().UTC().Format(time.RFC3339)
	defer func() {
		// The sync context can be expired when this defer runs. One example
		// is the per-calendar deadline that SyncAll enforces. The health
		// write must still record the failed attempt, so it uses a context
		// with the cancellation removed.
		if updateErr := e.updateSyncHealth(context.WithoutCancel(ctx), calendarID, attemptedAt, result, err); updateErr != nil {
			e.logger.Warn("update sync health failed", "calendar_id", calendarID, "error", updateErr)
			if result != nil {
				result.Errors = append(result.Errors, fmt.Errorf("update sync health: %w", updateErr))
			}
		}
	}()

	cal, account, client, remoteURL, err := e.loadCalendarClient(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	result = &SyncResult{CalendarID: cal.ID}

	e.logger.Info("sync started", "calendar_id", calendarID, "remote_url", remoteURL)

	if !remoteCalendarIsReadOnly(cal) {
		// Phase 0: Sync writable calendar metadata.
		if err := e.syncCalendarMetadata(ctx, client, calendarID, remoteURL); err != nil {
			e.logger.Warn("calendar metadata sync failed", "calendar_id", calendarID, "error", err)
			result.Errors = append(result.Errors, fmt.Errorf("calendar metadata: %w", err))
		}

		// Phase 1: Push dirty resources.
		pushResult, err := e.push(ctx, client, calendarID, remoteURL, resolvePushIdentity(cal, account), strategy, false)
		if err != nil {
			e.logger.Error("push failed", "calendar_id", calendarID, "error", err)
			result.Errors = append(result.Errors, fmt.Errorf("push: %w", err))
		} else {
			result.Pushed = pushResult.pushed
			result.Conflicts = pushResult.conflicts
			result.AutoResolved = pushResult.autoResolved
			result.SkippedConflicts = pushResult.skippedConflicts
			result.Errors = append(result.Errors, pushResult.errors...)
			result.Warnings = append(result.Warnings, pushResult.warnings...)
		}
	}

	// Phase 2: Pull changes from server
	pullResult, err := e.pull(ctx, client, calendarID, remoteURL)
	if err != nil {
		e.logger.Error("pull failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("pull: %w", err))
	} else {
		result.Pulled = pullResult.pulled
		result.Deleted = pullResult.deleted
		result.Errors = append(result.Errors, pullResult.errors...)
		result.Warnings = append(result.Warnings, pullResult.warnings...)
	}

	if !remoteCalendarIsReadOnly(cal) {
		// Phase 3: Process tombstones only when the server permits writes.
		tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
		if err != nil {
			e.logger.Warn("tombstone processing failed", "calendar_id", calendarID, "error", err)
			result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
		} else {
			result.Deleted += tombstoneResult.deleted
			result.AutoResolved += tombstoneResult.autoResolved
			result.Errors = append(result.Errors, tombstoneResult.errors...)
		}

		// Phase 4: A server-wins pass also closes open conflicts whose
		// resource is no longer dirty. Such a conflict outlived its
		// substance — most often a pre-#610 server-wins pass adopted the
		// server body without touching the row. Marking it resolved keeps
		// the recorded local body recoverable instead of stranding the row
		// in `sync conflicts` forever.
		if strategy == ConflictServerWins {
			resolved, err := e.resolveMootConflicts(ctx, calendarID)
			if err != nil {
				e.logger.Warn("resolve moot conflicts failed", "calendar_id", calendarID, "error", err)
				result.Errors = append(result.Errors, fmt.Errorf("resolve moot conflicts: %w", err))
			} else {
				result.AutoResolved += resolved
			}
		}
	}

	// Cleanup stale tombstones
	if err := e.q.DeleteStaleTombstones(ctx); err != nil {
		e.logger.Warn("stale tombstone cleanup failed", "error", err)
	}

	e.logger.Info("sync completed",
		"calendar_id", calendarID,
		"pushed", result.Pushed,
		"pulled", result.Pulled,
		"deleted", result.Deleted,
		"conflicts", result.Conflicts,
		"auto_resolved", result.AutoResolved,
		"errors", len(result.Errors),
	)

	return result, nil
}

// PushLocalEdits runs only the push and tombstone phases for one calendar.
// It is the write-only fast path used for opportunistic save-time sync.
// Local mutations are flushed upstream. There is no pull and no rewrite of
// calendar metadata. A 412 conflict records a sync_conflicts row and keeps
// the local row dirty: this path never adopts the server body over an edit
// the user just made, whatever the configured strategy. Dirty resources
// that fail to push stay dirty. The next full SyncCalendar will retry them
// under the configured strategy. It shares a per-calendar lifecycle lock
// with SyncCalendar. The inner push lock continues to protect direct push
// calls used by focused engine tests.
func (e *Engine) PushLocalEdits(ctx context.Context, calendarID int64) (*SyncResult, error) {
	release, err := e.lockCalendarLifecycle(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	defer release()
	cal, account, client, remoteURL, err := e.loadCalendarClient(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	result := &SyncResult{CalendarID: calendarID}
	if remoteCalendarIsReadOnly(cal) {
		return result, nil
	}

	pushResult, err := e.push(ctx, client, calendarID, remoteURL, resolvePushIdentity(cal, account), ConflictPrompt, true)
	if err != nil {
		return result, fmt.Errorf("push: %w", err)
	}
	result.Pushed = pushResult.pushed
	result.Conflicts = pushResult.conflicts
	result.AutoResolved = pushResult.autoResolved
	result.SkippedConflicts = pushResult.skippedConflicts
	result.Errors = append(result.Errors, pushResult.errors...)
	result.Warnings = append(result.Warnings, pushResult.warnings...)

	tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
	} else {
		result.Deleted = tombstoneResult.deleted
		result.AutoResolved += tombstoneResult.autoResolved
		result.Errors = append(result.Errors, tombstoneResult.errors...)
	}
	return result, nil
}

func remoteCalendarIsReadOnly(cal storage.Calendar) bool {
	return strings.EqualFold(strings.TrimSpace(cal.RemoteAccess), "read")
}

// resolveMootConflicts marks open conflicts resolved when their resource is
// no longer dirty. A non-dirty resource carries no unpushed local edit, so
// the recorded conflict has nothing left to decide. A missing
// sync_resource row is moot the same way.
func (e *Engine) resolveMootConflicts(ctx context.Context, calendarID int64) (int, error) {
	open, err := e.q.ListSyncConflictsByCalendar(ctx, calendarID)
	if err != nil {
		return 0, fmt.Errorf("list conflicts: %w", err)
	}
	resolved := 0
	for _, c := range open {
		sr, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{
			CalendarID: calendarID,
			Uid:        c.Uid,
		})
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return resolved, fmt.Errorf("get sync resource %s: %w", c.Uid, err)
		}
		if err == nil && sr.Dirty != 0 {
			continue
		}
		if err := e.markConflictResolved(ctx, calendarID, c.Uid, ResolutionServerAuto); err != nil {
			return resolved, fmt.Errorf("mark conflict resolved %s: %w", c.Uid, err)
		}
		resolved++
	}
	return resolved, nil
}

const accountCalendarSyncTimeout = 5 * time.Minute

// SyncAccount syncs every calendar linked to one account serially. Calendars
// that share a credential must not refresh or persist that credential
// concurrently.
func (e *Engine) SyncAccount(ctx context.Context, accountID int64, strategy ConflictStrategy) ([]*SyncResult, error) {
	cals, err := e.q.ListCalendarsByAccount(ctx, &accountID)
	if err != nil {
		return nil, fmt.Errorf("list account calendars: %w", err)
	}
	results := make([]*SyncResult, 0, len(cals))
	for _, cal := range cals {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		calendarCtx, cancel := context.WithTimeout(ctx, accountCalendarSyncTimeout)
		result, err := e.SyncCalendar(calendarCtx, cal.ID, strategy)
		cancel()
		if err != nil {
			e.logger.Error("sync calendar failed", "calendar_id", cal.ID, "error", err)
			result = &SyncResult{CalendarID: cal.ID, Errors: []error{err}}
		}
		results = append(results, result)
	}
	return results, nil
}

// maxSyncAllConcurrency bounds how many accounts SyncAll syncs at once. Each
// account talks to an independent server. Concurrency then cuts wall-clock
// time toward the slowest single account instead of the sum of all of them.
// The cap keeps a user with many accounts from an unbounded number of
// simultaneous server connections.
const maxSyncAllConcurrency = 8

// SyncAll syncs all connected calendars. Calendars are grouped by account.
// Distinct accounts sync concurrently (independent servers and credentials).
// Calendars that share an account sync serially within one worker. A
// single OAuth credential refresh then cannot race itself. Results are
// returned in ListCalendars order regardless of completion order. A
// per-calendar failure is captured in its own SyncResult. The other
// calendars still run.
func (e *Engine) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	cals, err := e.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	// Assign each connected calendar a stable output slot (ListCalendars order)
	// and bucket it under its account so same-account calendars stay serial.
	type calJob struct {
		id   int64
		slot int
	}
	byAccount := make(map[int64][]calJob)
	connected := 0
	for _, cal := range cals {
		if cal.AccountID == nil || *cal.AccountID == 0 {
			continue
		}
		acct := *cal.AccountID
		byAccount[acct] = append(byAccount[acct], calJob{id: cal.ID, slot: connected})
		connected++
	}

	// Each worker writes only its own pre-assigned slots, so the shared slice
	// needs no locking and the output order is independent of who finishes first.
	results := make([]*SyncResult, connected)
	var g errgroup.Group
	g.SetLimit(maxSyncAllConcurrency)
	for _, jobs := range byAccount {
		g.Go(func() error {
			for _, j := range jobs {
				result, err := e.SyncCalendar(ctx, j.id, strategy)
				if err != nil {
					e.logger.Error("sync calendar failed", "calendar_id", j.id, "error", err)
					result = &SyncResult{CalendarID: j.id, Errors: []error{err}}
				}
				results[j.slot] = result
			}
			return nil
		})
	}
	// Workers never return an error — per-calendar failures live in results — so
	// Wait only blocks until every account finishes.
	_ = g.Wait()
	return results, nil
}

// afterImportRevCapture, when non-nil, runs inside clearDirtyAfterImport just
// before the conditional clear. It is nil in production. Tests use it to
// simulate a concurrent local edit that lands between the import and the
// clear, to exercise the rev guard. See issue #417.
var afterImportRevCapture func()

// afterImportPersist, when non-nil, runs inside importICal right after
// persistImported commits and before the caller's clearDirtyAfterImport. It is
// nil in production. Tests use it to simulate a concurrent local edit that
// lands in the persist-commit to clear window. That exercises the rev guard
// now that the persist transaction captures the rev. See issue #494.
var afterImportPersist func()

// clearDirtyAfterImport adopts the server ETag and clears the dirty flag.
// The resource's local row was overwritten with the server's version
// (accept-server conflict resolution or a pull). It does so only when no
// local edit landed after the import.
//
// importICal and persistImported route through the event, todo, and journal
// services. Those services flip dirty=1 and bump rev via MarkResourceDirty as
// a side effect. persistImported captures that post-import rev inside the
// same transaction. The caller feeds it here as rev.
//
// Pass it to FinalizePushedResource. The clear then becomes a no-op when a
// concurrent user edit bumped rev again after the import committed. An
// unconditional clear would wipe that edit's dirty flag and drop it in
// silence. That is the same lost-update race FinalizePushedResource guards
// on the push path.
//
// Capture rev inside the persist transaction. Do not re-read it after
// commit. That also closes the narrow window between persist-commit and the
// read. See issues #92, #417 and #494.
func (e *Engine) clearDirtyAfterImport(ctx context.Context, calendarID int64, uid, etag string, rev int64) error {
	if afterImportRevCapture != nil {
		afterImportRevCapture()
	}
	return e.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
		CalendarID: calendarID,
		Uid:        uid,
		Etag:       etag,
		Rev:        rev,
	})
}

func parseICalData(data []byte) (*ical.Calendar, error) {
	dec := ical.NewDecoder(bytes.NewReader(data))
	return dec.Decode()
}

// collectImportWarnings converts ImportFile's warning strings into
// ImportWarnings labeled with the resource path. A UID label is attached only
// when every component in the payload shares one nonempty UID. That covers
// the common recurring case, where a resource imports as master plus overrides
// under a single UID. A payload that spans several UIDs (412 server-wins
// bodies, manual conflict resolution) may carry a warning from ANY of its
// components. If we blame the first component's UID, the user goes to an
// event with nothing wrong in it.
func collectImportWarnings(path string, result icalPkg.ImportResult) []ImportWarning {
	if len(result.Warnings) == 0 {
		return nil
	}
	uid := soleUID(result)
	warnings := make([]ImportWarning, 0, len(result.Warnings))
	for _, msg := range result.Warnings {
		warnings = append(warnings, ImportWarning{Path: path, UID: uid, Message: msg})
	}
	return warnings
}

// soleUID returns the one UID shared by every component of the payload.
// It returns "" when the payload is empty, mixes UIDs, or any component
// lacks one.
func soleUID(result icalPkg.ImportResult) string {
	uid := ""
	sole := func(u string) bool {
		if u == "" {
			return false
		}
		if uid == "" {
			uid = u
			return true
		}
		return u == uid
	}
	for _, ev := range result.Events {
		if !sole(ev.UID) {
			return ""
		}
	}
	for _, td := range result.Todos {
		if !sole(td.UID) {
			return ""
		}
	}
	for _, jr := range result.Journals {
		if !sole(jr.UID) {
			return ""
		}
	}
	return uid
}

// noteImportWarnings collects the import warnings for one imported payload
// and logs them. Callers append the returned slice to their result so the
// warnings also travel as data for entry points that discard the logger.
func (e *Engine) noteImportWarnings(path string, result icalPkg.ImportResult) []ImportWarning {
	warnings := collectImportWarnings(path, result)
	logImportWarnings(e.logger, warnings)
	return warnings
}

// notePersistWarnings turns the alarms persistImported dropped into
// ImportWarnings and logs them. A dropped alarm is a silent local change
// otherwise: the resource persists without it, and the next push writes
// the shorter alarm set over the server copy. The caller appends the
// returned slice to its result, so an entry point that discards the
// logger still reports the drop (issue #585).
func (e *Engine) notePersistWarnings(path, uid string, messages []string) []ImportWarning {
	if len(messages) == 0 {
		return nil
	}
	warnings := make([]ImportWarning, 0, len(messages))
	for _, msg := range messages {
		warnings = append(warnings, ImportWarning{Path: path, UID: uid, Message: msg})
	}
	logImportWarnings(e.logger, warnings)
	return warnings
}

// logImportWarnings surfaces what ImportFile could not represent faithfully.
// Examples: a malformed DTEND replaced by a fabricated span, or an alarm
// dropped for an unusable trigger. Sync used to discard these. That mattered
// because the substitute value does not stay local. The next local edit marks
// the resource dirty and pushes our fabrication back over the value the
// server still holds correctly. The log serves `sync run`. Entry points that
// discard the logger read the same warnings off SyncResult.Warnings instead.
func logImportWarnings(logger *slog.Logger, warnings []ImportWarning) {
	for _, w := range warnings {
		if w.UID != "" {
			logger.Warn("import warning", "path", w.Path, "uid", w.UID, "warning", w.Message)
			continue
		}
		logger.Warn("import warning", "path", w.Path, "warning", w.Message)
	}
}

func extractUID(result icalPkg.ImportResult) string {
	if len(result.Events) > 0 {
		return result.Events[0].UID
	}
	if len(result.Todos) > 0 {
		return result.Todos[0].UID
	}
	if len(result.Journals) > 0 {
		return result.Journals[0].UID
	}
	return ""
}

func detectOwnerType(result icalPkg.ImportResult) string {
	if len(result.Events) > 0 {
		return ownerTypeEvent
	}
	if len(result.Todos) > 0 {
		return ownerTypeTodo
	}
	if len(result.Journals) > 0 {
		return ownerTypeJournal
	}
	return ownerTypeEvent
}

func (e *Engine) updateSyncHealth(ctx context.Context, calendarID int64, attemptedAt string, result *SyncResult, runErr error) error {
	lastSyncAt := ""
	lastSyncError := summarizeSyncError(result, runErr)
	if runErr == nil && len(result.Errors) == 0 {
		lastSyncAt = attemptedAt
		lastSyncError = ""
	}

	return e.q.UpdateCalendarSyncHealth(ctx, storage.UpdateCalendarSyncHealthParams{
		ID:                  calendarID,
		LastSyncAttemptedAt: storage.StringToNullable(attemptedAt),
		LastSyncAt:          storage.StringToNullable(lastSyncAt),
		LastSyncError:       storage.StringToNullable(lastSyncError),
	})
}

// resolvePushIdentity returns the email address the calendar owner uses to
// PUT meeting resources. It prefers the calendar's stored owner_email and
// falls back to the linked account's username. That username is the user's
// email for both basic-auth and OAuth providers we support. It returns empty
// when neither is known. The caller should then skip the organizer gate
// rather than guess. The caller passes the cal and account rows (already
// loaded by loadCalendarClient), so this performs no queries.
func resolvePushIdentity(cal storage.Calendar, account storage.Account) string {
	if email := strings.TrimSpace(cal.OwnerEmail); email != "" {
		return email
	}
	if cal.AccountID != nil && *cal.AccountID != 0 {
		return account.Username
	}
	return ""
}

// userOrganizesEvent reports whether the calendar owner can legitimately
// PUT this event. Returns true when the event has no organizer attendee
// (locally-created event) or when the organizer's email matches identity
// (case-insensitive, mailto: prefix tolerated). Returns false only when
// we can prove the user is just an attendee.
func (e *Engine) userOrganizesEvent(ctx context.Context, uid, identity string) bool {
	row, err := e.q.GetEventByUID(ctx, uid)
	if err != nil {
		return true
	}
	attendees, err := e.q.ListAttendeesByEventID(ctx, row.ID)
	if err != nil {
		return true
	}
	for _, a := range attendees {
		if a.Organizer == 1 {
			return strings.EqualFold(stripMailtoPrefix(a.Email), stripMailtoPrefix(identity))
		}
	}
	return true
}

func stripMailtoPrefix(s string) string {
	s = strings.TrimSpace(s)
	if len(s) >= 7 && strings.EqualFold(s[:7], "mailto:") {
		return s[7:]
	}
	return s
}

func summarizeSyncError(result *SyncResult, runErr error) string {
	if runErr != nil {
		return runErr.Error()
	}
	if len(result.Errors) == 0 {
		return ""
	}
	if len(result.Errors) == 1 {
		return result.Errors[0].Error()
	}
	return fmt.Sprintf("%s (+%d more)", result.Errors[0], len(result.Errors)-1)
}
