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
	hydratepkg "github.com/douglasdemoura/chroncal/internal/hydrate"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/model"
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
	Conflicts  int
	Errors     []error
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
// save-time PushCalendar that races a periodic SyncCalendar. Each run would
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

func normalizeRemoteRef(ref string) string {
	if ref == "" {
		return ""
	}

	parsed, err := url.Parse(ref)
	if err != nil {
		return ref
	}

	if parsed.Path != "" {
		trailingSlash := strings.HasSuffix(parsed.Path, "/")
		cleaned := path.Clean(parsed.Path)
		switch {
		case cleaned == "." && trailingSlash:
			cleaned = "/"
		case trailingSlash && cleaned != "/":
			cleaned += "/"
		}
		parsed.Path = cleaned
	}

	return parsed.String()
}

func buildRemoteResourcePath(calendarRef, _ string) string {
	parsed, err := url.Parse(calendarRef)
	if err != nil {
		return normalizeRemoteRef(strings.TrimRight(calendarRef, "/") + "/" + newRemoteObjectName())
	}

	basePath := parsed.Path
	if basePath == "" {
		basePath = "/"
	}
	parsed.Path = path.Join(basePath, newRemoteObjectName())
	return normalizeRemoteRef(parsed.String())
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
		if updateErr := e.updateSyncHealth(ctx, calendarID, attemptedAt, result, err); updateErr != nil {
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
		pushResult, err := e.push(ctx, client, calendarID, remoteURL, resolvePushIdentity(cal, account), strategy)
		if err != nil {
			e.logger.Error("push failed", "calendar_id", calendarID, "error", err)
			result.Errors = append(result.Errors, fmt.Errorf("push: %w", err))
		} else {
			result.Pushed = pushResult.pushed
			result.Conflicts = pushResult.conflicts
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
			result.Conflicts += tombstoneResult.conflicts
			result.Errors = append(result.Errors, tombstoneResult.errors...)
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
		"errors", len(result.Errors),
	)

	return result, nil
}

// PushCalendar runs only the push and tombstone phases for one calendar.
// It is the write-only fast path used for opportunistic save-time sync.
// Local mutations are flushed upstream. There is no pull and no rewrite of
// calendar metadata. Dirty resources that fail to push stay dirty. The
// next full SyncCalendar will retry them. It shares a per-calendar lifecycle
// lock with SyncCalendar. The inner push lock continues to protect direct
// push calls used by focused engine tests.
func (e *Engine) PushCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (*SyncResult, error) {
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

	pushResult, err := e.push(ctx, client, calendarID, remoteURL, resolvePushIdentity(cal, account), strategy)
	if err != nil {
		return result, fmt.Errorf("push: %w", err)
	}
	result.Pushed = pushResult.pushed
	result.Conflicts = pushResult.conflicts
	result.Errors = append(result.Errors, pushResult.errors...)
	result.Warnings = append(result.Warnings, pushResult.warnings...)

	tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
	} else {
		result.Deleted = tombstoneResult.deleted
		result.Conflicts += tombstoneResult.conflicts
		result.Errors = append(result.Errors, tombstoneResult.errors...)
	}
	return result, nil
}

func remoteCalendarIsReadOnly(cal storage.Calendar) bool {
	return strings.EqualFold(strings.TrimSpace(cal.RemoteAccess), "read")
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

type pushResult struct {
	pushed    int
	conflicts int
	errors    []error
	warnings  []ImportWarning
}

func (e *Engine) push(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL, pushIdentity string, strategy ConflictStrategy) (*pushResult, error) {
	// Serialize the push phase per calendar so a concurrent run cannot read the
	// same dirty row and create a duplicate server object. See pushLock and
	// issue #225.
	lock := e.pushLock(calendarID)
	lock.Lock()
	defer lock.Unlock()

	dirty, err := e.q.ListDirtySyncResources(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list dirty: %w", err)
	}

	result := &pushResult{}
	for _, res := range dirty {
		// CalDAV's PUT contract (RFC 4791 §4.1) only lets the organizer
		// modify a meeting resource. Attendees are supposed to round-trip
		// RSVP changes via iTIP REPLY, not PUT — Google rejects attendee
		// PUTs with HTTP 400 / 500 and a vague <D:error/> body. Skipping
		// foreign-organized events here clears the dirty flag so we stop
		// retrying every sync; the local row is left untouched.
		if pushIdentity != "" && res.OwnerType == ownerTypeEvent && !e.userOrganizesEvent(ctx, res.Uid, pushIdentity) {
			e.logger.Info("skip push: not the organizer", "uid", res.Uid, "owner", pushIdentity)
			if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
				CalendarID: calendarID,
				Uid:        res.Uid,
				Etag:       res.Etag,
			}); err != nil {
				e.logger.Error("clear non-owned dirty", "uid", res.Uid, "error", err)
			}
			continue
		}

		// In prompt mode, skip resources that already have an open,
		// unresolved conflict. The local row is still dirty and carries the
		// ETag that already failed If-Match, so re-PUTing it just 412s again
		// and records another conflict every sync. Hold off until the user
		// resolves it via ResolveConflict, which clears the conflict and
		// refreshes the ETag. See issue #104. ServerWins is excluded: it
		// never records conflict rows and clears dirty on its own 412, so it
		// has no loop to break — and skipping it would strand a stale
		// conflict row left over from a prior prompt-mode run. The condition
		// mirrors the conflict-recording branch below, which treats every
		// non-ServerWins strategy as prompt mode.
		if strategy != ConflictServerWins {
			if open, cerr := e.q.CountOpenSyncConflicts(ctx, storage.CountOpenSyncConflictsParams{
				CalendarID: calendarID,
				Uid:        res.Uid,
			}); cerr != nil {
				e.logger.Error("check open conflict", "uid", res.Uid, "error", cerr)
			} else if open > 0 {
				e.logger.Debug("skip push: open conflict pending resolution", "uid", res.Uid)
				continue
			}
		}

		e.logger.Debug("pushing resource", "uid", res.Uid, "remote_url", res.RemoteUrl)

		// Export the local resource to iCal
		icalData, err := e.exportResource(ctx, res.OwnerType, res.Uid)
		if err != nil {
			if errors.Is(err, errResourceMissing) {
				// No live local row backs this dirty sync_resource (the user
				// purged it from trash, or the master/override pair got into
				// an inconsistent state). Retrying every sync just races the
				// same null lookup, so clear the flag and let processTombstones
				// handle any remote-side cleanup.
				e.logger.Info("clear dirty: local resource missing", "uid", res.Uid, "owner_type", res.OwnerType)
				if cerr := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
					CalendarID: calendarID,
					Uid:        res.Uid,
					Etag:       res.Etag,
				}); cerr != nil {
					e.logger.Error("clear missing-resource dirty", "uid", res.Uid, "error", cerr)
				}
				continue
			}
			e.logger.Error("export resource failed", "uid", res.Uid, "error", err)
			exportErr := fmt.Errorf("export %s: %w", res.Uid, err)
			result.errors = append(result.errors, exportErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, exportErr)
			continue
		}

		// Parse the iCal data for PUT
		cal, parseErr := parseICalData(icalData)
		if parseErr != nil {
			parseErr = fmt.Errorf("parse ical for %s: %w", res.Uid, parseErr)
			result.errors = append(result.errors, parseErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, parseErr)
			continue
		}

		// Determine PUT path
		var putPath string
		if res.RemoteUrl != "" {
			putPath, err = client.CanonicalObjectRef(remoteURL, res.RemoteUrl)
			if err != nil {
				err = fmt.Errorf("validate remote href for %s: %w", res.Uid, err)
				result.errors = append(result.errors, err)
				e.recordPushFailure(ctx, calendarID, res.Uid, err)
				continue
			}
		} else {
			putPath, err = client.CanonicalObjectRef(remoteURL, buildRemoteResourcePath(remoteURL, res.Uid))
			if err != nil {
				err = fmt.Errorf("build remote href for %s: %w", res.Uid, err)
				result.errors = append(result.errors, err)
				e.recordPushFailure(ctx, calendarID, res.Uid, err)
				continue
			}
		}

		// PUT to server. A PUT can reach the server and mutate the resource
		// even when its response is lost (e.g. connection reset while reading
		// the body), which Retry classifies as transient. The retried PUT then
		// re-sends the stale pre-PUT If-Match and the server — whose ETag has
		// already advanced — answers 412, masquerading as a conflict. When an
		// earlier attempt failed transiently, treat a 412 whose server body
		// equals what we PUT as the success that actually happened. See #294.
		priorAttemptMayHaveLanded := false
		newEtag, putErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
			etag, err := client.PutResource(ctx, putPath, cal, res.Etag)
			if err == nil {
				return etag, nil
			}
			// A 412 is never transient, so these branches are exclusive.
			if caldav.IsTransient(err) {
				priorAttemptMayHaveLanded = true
			} else if priorAttemptMayHaveLanded && caldav.IsConflict(err) {
				if landedEtag, ok := e.putAlreadyLanded(ctx, client, putPath, cal); ok {
					return landedEtag, nil
				}
			}
			return etag, err
		})
		if putErr != nil {
			// Check for 412 Precondition Failed (ETag conflict)
			if caldav.IsConflict(putErr) {
				e.logger.Warn("conflict detected during push", "uid", res.Uid)
				if strategy == ConflictServerWins {
					// Re-fetch server version, clear dirty flag, accept server state
					e.logger.Info("resolving conflict: server wins", "uid", res.Uid)
					serverRes, fetchErr := client.GetResource(ctx, putPath)
					if fetchErr != nil {
						e.logger.Error("re-fetch server resource failed", "uid", res.Uid, "error", fetchErr)
						result.errors = append(result.errors, fmt.Errorf("conflict re-fetch %s: %w", res.Uid, fetchErr))
					} else {
						var buf bytes.Buffer
						enc := ical.NewEncoder(&buf)
						if err := enc.Encode(serverRes.Data); err != nil {
							e.logger.Error("encode server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("encode server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						imported, revs, importWarnings, err := e.importICal(ctx, calendarID, buf.String())
						if err != nil {
							e.logger.Error("import server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("import server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						result.warnings = append(result.warnings, importWarnings...)
						if !imported {
							// The server's 412 body carried no importable
							// VEVENT/VTODO/VJOURNAL, so nothing was applied.
							// Clearing dirty and stamping the server ETag here
							// would drop the local edit behind a server version we
							// never adopted. Keep dirty so the next push retries;
							// the conflict is still counted below. Mirrors the
							// manual ResolveConflict guard. See issue #495.
							e.logger.Warn("server resource has no importable data; keeping local dirty", "uid", res.Uid)
						} else {
							// Clear dirty and update ETag to accept server version.
							// Guard the clear on the rev persistImported captured
							// inside its transaction so a local edit landing after
							// the import committed is not silently dropped (lost
							// update). See issues #92, #417 and #494.
							if err := e.clearDirtyAfterImport(ctx, calendarID, res.Uid, serverRes.ETag, revs[res.Uid]); err != nil {
								e.logger.Error("clear dirty after conflict", "uid", res.Uid, "error", err)
							}
						}
					}
				} else {
					// ConflictPrompt: record conflict for manual resolution.
					// Reuse icalData exported above — it is the exact body we
					// just tried to PUT and is unchanged here, so re-exporting
					// would needlessly repeat ~10 DB queries plus an iCal encode
					// per conflicting resource. See issue #264.
					serverRes, fetchErr := client.GetResource(ctx, putPath)
					if fetchErr == nil {
						serverIcal, encodeErr := caldav.EncodeCalendar(serverRes.Data)
						if encodeErr != nil {
							e.logger.Warn("encode server resource for conflict record", "uid", res.Uid, "error", encodeErr)
						}
						ownerID, lookupErr := e.lookupOwnerID(ctx, res.OwnerType, res.Uid)
						if lookupErr != nil {
							e.logger.Warn("lookup owner id for conflict record", "uid", res.Uid, "owner_type", res.OwnerType, "error", lookupErr)
						}
						_ = e.q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
							CalendarID: calendarID,
							OwnerType:  res.OwnerType,
							OwnerID:    ownerID,
							Uid:        res.Uid,
							LocalIcal:  string(icalData),
							ServerIcal: string(serverIcal),
							ServerEtag: serverRes.ETag,
						})
					}
				}
				result.conflicts++
				continue
			}
			e.logger.Error("PUT failed", "uid", res.Uid, "error", putErr)
			putErr = fmt.Errorf("put %s: %w", res.Uid, putErr)
			result.errors = append(result.errors, putErr)
			e.recordPushFailure(ctx, calendarID, res.Uid, putErr)
			continue
		}

		// Store the new server ETag and clear the dirty flag — but only clear
		// dirty if the resource has not been edited since we captured res.Rev
		// (before exporting the body we just PUT). A local edit landing during
		// the PUT round-trip bumps rev and keeps dirty=1; an unconditional
		// clear here would wipe that flag and silently drop the edit (lost
		// update). The ETag still advances so the next push's If-Match matches
		// the server. See issue #92.
		if err := e.q.FinalizePushedResource(ctx, storage.FinalizePushedResourceParams{
			CalendarID: calendarID,
			Uid:        res.Uid,
			Etag:       newEtag,
			Rev:        res.Rev,
		}); err != nil {
			e.logger.Error("finalize pushed resource failed", "uid", res.Uid, "error", err)
		}
		// The body reached the server, so this attempt succeeded. Reset the
		// failure bookkeeping even when the finalize write above failed.
		e.clearPushFailure(ctx, calendarID, res.Uid)

		// Update remote URL if it was newly assigned
		if res.RemoteUrl == "" {
			if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
				CalendarID:   calendarID,
				Uid:          res.Uid,
				OwnerType:    res.OwnerType,
				RemoteUrl:    normalizeRemoteRef(putPath),
				Etag:         newEtag,
				Dirty:        0,
				SyncStrategy: res.SyncStrategy,
			}); err != nil {
				e.logger.Error("update sync resource URL", "uid", res.Uid, "error", err)
			}
		}

		result.pushed++
		e.logger.Debug("pushed resource", "uid", res.Uid, "etag", newEtag)
	}

	return result, nil
}

// recordPushFailure stores one more consecutive failed push attempt for a
// resource. The push loop and the sync doctor call it after a push attempt
// that fails on an export, a parse, or a PUT. The doctor reads the counter
// to show how long a resource stayed wedged. A failed bookkeeping write
// only logs: the push error itself stays the reported failure.
func (e *Engine) recordPushFailure(ctx context.Context, calendarID int64, uid string, err error) {
	if rerr := e.q.RecordSyncResourcePushFailure(ctx, storage.RecordSyncResourcePushFailureParams{
		LastPushError: err.Error(),
		CalendarID:    calendarID,
		Uid:           uid,
	}); rerr != nil {
		e.logger.Warn("record push failure", "uid", uid, "error", rerr)
	}
}

// clearPushFailure resets the push-failure bookkeeping after a successful
// push. A failed bookkeeping write only logs: the pushed body already
// reached the server.
func (e *Engine) clearPushFailure(ctx context.Context, calendarID int64, uid string) {
	if cerr := e.q.ClearSyncResourcePushFailure(ctx, storage.ClearSyncResourcePushFailureParams{
		CalendarID: calendarID,
		Uid:        uid,
	}); cerr != nil {
		e.logger.Warn("clear push failure", "uid", uid, "error", cerr)
	}
}

// putAlreadyLanded reports whether the server's current body for path equals
// the calendar we just PUT. It returns the server's ETag when it matches.
//
// It distinguishes a genuine 412 conflict from a retried PUT whose
// predecessor landed before its response was lost. If the server now
// holds exactly our payload, that earlier write won. We then adopt its ETag
// instead of a false conflict. A mismatch (a real concurrent edit)
// or any fetch/encode failure falls back to the 412. See #294.
func (e *Engine) putAlreadyLanded(ctx context.Context, client *caldav.Client, path string, sent *ical.Calendar) (string, bool) {
	serverRes, err := client.GetResource(ctx, path)
	if err != nil {
		return "", false
	}
	sentBody, err := caldav.EncodeCalendar(sent)
	if err != nil {
		return "", false
	}
	serverBody, err := caldav.EncodeCalendar(serverRes.Data)
	if err != nil {
		return "", false
	}
	// An empty ETag would disable the next push's If-Match precondition, so
	// fall back to the 412 rather than adopt it as a successful write.
	if serverRes.ETag == "" || !bytes.Equal(sentBody, serverBody) {
		return "", false
	}
	return serverRes.ETag, true
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

type pullResult struct {
	pulled   int
	deleted  int
	errors   []error
	warnings []ImportWarning
}

func (e *Engine) pull(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*pullResult, error) {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("get calendar: %w", err)
	}
	storedToken := storage.NullableToString(cal.SyncToken)

	// Fast path: RFC 6578 sync-collection. The server returns only the
	// resources that changed since storedToken — no token means initial
	// snapshot. We fetch bodies via multiget for just the changed paths,
	// so steady-state syncs cost a single REPORT instead of downloading
	// every resource on the calendar.
	//
	// Servers may TRUNCATE the result set (§3.6): a 507 marker on the
	// collection plus a continuation token. Google pages large initial
	// snapshots this way. We loop until the response is complete and only
	// then apply — diffing local state against a partial page once
	// soft-deleted every event beyond page one.
	token := storedToken
	merged := &caldav.SyncCollectionResult{}
	for page := 0; ; page++ {
		syncResult, syncErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (*caldav.SyncCollectionResult, error) {
			return client.SyncCollection(ctx, remoteURL, token)
		})
		if page == 0 {
			if errors.Is(syncErr, caldav.ErrSyncTokenInvalid) && token != "" {
				e.logger.Info("sync-token invalid, performing full resync", "calendar_id", calendarID)
				syncResult, syncErr = caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (*caldav.SyncCollectionResult, error) {
					return client.SyncCollection(ctx, remoteURL, "")
				})
				storedToken = ""
			}
			if errors.Is(syncErr, caldav.ErrSyncCollectionUnsupported) {
				e.logger.Info("server lacks sync-collection support, falling back to QueryAll", "calendar_id", calendarID)
				return e.pullFullSnapshot(ctx, client, calendarID, remoteURL)
			}
		}
		if syncErr != nil {
			return nil, fmt.Errorf("sync-collection: %w", syncErr)
		}

		merged.Changes = append(merged.Changes, syncResult.Changes...)
		merged.SyncToken = syncResult.SyncToken
		merged.Truncated = syncResult.Truncated
		if !syncResult.Truncated {
			break
		}
		if syncResult.SyncToken == "" {
			return nil, fmt.Errorf("sync-collection: truncated response without a continuation token")
		}
		if page+1 >= maxSyncCollectionPages {
			return nil, fmt.Errorf("sync-collection: still truncated after %d pages", maxSyncCollectionPages)
		}
		e.logger.Info("sync-collection truncated, fetching next page",
			"calendar_id", calendarID, "page", page+1, "changes_so_far", len(merged.Changes))
		token = syncResult.SyncToken
	}
	return e.applySyncCollection(ctx, client, calendarID, remoteURL, cal, merged, storedToken == "")
}

// maxSyncCollectionPages bounds the truncation-pagination loop. Google's
// pages carry ~90 changes. 200 pages is far beyond any real calendar. It
// turns a server paging bug into an error instead of an infinite loop.
const maxSyncCollectionPages = 200

// pullFullSnapshot is the legacy pull path: download every resource and
// compare etags locally. Only used when the server doesn't support
// sync-collection (RFC 6578).
func (e *Engine) pullFullSnapshot(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*pullResult, error) {
	result := &pullResult{}

	// Fetch all resources from server
	resources, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) ([]caldav.Resource, error) {
		return client.QueryAll(ctx, remoteURL)
	})
	if err != nil {
		return nil, fmt.Errorf("query all: %w", err)
	}

	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}
	tombstonedPaths := make(map[string]bool, len(tombstones))
	tombstonedUIDs := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
		if ts.RemoteUrl != "" {
			remotePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
			if hrefErr != nil {
				e.logger.Warn("ignore invalid tombstone href", "calendar_id", calendarID, "uid", ts.Uid, "remote_url", ts.RemoteUrl, "error", hrefErr)
				continue
			}
			tombstonedPaths[remotePath] = true
		}
		if ts.Uid != "" {
			tombstonedUIDs[ts.Uid] = true
		}
	}

	// Build map of known local resources
	localResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list local resources: %w", err)
	}
	localByPath := make(map[string]storage.SyncResource, len(localResources))
	for _, r := range localResources {
		if r.RemoteUrl != "" {
			remotePath, hrefErr := client.CanonicalObjectRef(remoteURL, r.RemoteUrl)
			if hrefErr != nil {
				e.logger.Warn("ignore invalid sync resource href", "calendar_id", calendarID, "uid", r.Uid, "remote_url", r.RemoteUrl, "error", hrefErr)
				continue
			}
			localByPath[remotePath] = r
		}
	}

	// Track which UIDs the server still reports. Deletion detection is keyed
	// by UID rather than path because some CalDAV servers (GMX/Cosmo) rewrite
	// object hrefs after PUT — the server-returned href can differ from the
	// one we stored, so path-based comparison produces false "deleted on
	// server" signals and nukes healthy local resources.
	remoteUIDs := make(map[string]bool, len(resources))
	for _, res := range resources {
		resPath, hrefErr := client.CanonicalObjectRef(remoteURL, res.Path)
		if hrefErr != nil {
			e.logger.Warn("skip out-of-scope remote href", "calendar_id", calendarID, "path", res.Path, "error", hrefErr)
			continue
		}
		if tombstonedPaths[resPath] {
			e.logger.Debug("skip tombstoned remote resource by path", "path", resPath)
			continue
		}

		if local, exists := localByPath[resPath]; exists {
			remoteUIDs[local.Uid] = true
			if local.Etag == res.ETag {
				continue
			}
		}

		// Import the resource
		if res.Data == nil {
			continue
		}
		var buf bytes.Buffer
		enc := ical.NewEncoder(&buf)
		if err := enc.Encode(res.Data); err != nil {
			e.logger.Warn("encode fetched resource failed", "path", res.Path, "error", err)
			continue
		}

		importResult, err := icalPkg.ImportFile(strings.NewReader(buf.String()))
		if err != nil {
			e.logger.Warn("import fetched resource failed", "path", res.Path, "error", err)
			continue
		}
		result.warnings = append(result.warnings, e.noteImportWarnings(res.Path, importResult)...)

		// Extract UID from imported data
		uid := extractUID(importResult)
		if uid == "" {
			e.logger.Warn("no UID in fetched resource", "path", res.Path)
			continue
		}
		remoteUIDs[uid] = true
		if tombstonedUIDs[uid] {
			e.logger.Debug("skip tombstoned remote resource by uid", "uid", uid, "path", resPath)
			continue
		}

		// Persist imported data to the database
		ownerType := detectOwnerType(importResult)
		revs, alarmWarnings, persistErr := e.persistImported(ctx, calendarID, importResult)
		if persistErr != nil {
			e.logger.Error("persist imported resource", "uid", uid, "path", res.Path, "error", persistErr)
			continue
		}
		result.warnings = append(result.warnings, e.notePersistWarnings(res.Path, uid, alarmWarnings)...)

		// Upsert sync resource tracking. UpsertSyncResource's ON CONFLICT is
		// keyed by (calendar_id, uid), so a stale remote_url from a prior
		// sync cycle (or from our PUT before the server rewrote the href)
		// gets replaced here with the authoritative server path.
		if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
			CalendarID:   calendarID,
			Uid:          uid,
			OwnerType:    ownerType,
			RemoteUrl:    resPath,
			Etag:         res.ETag,
			Dirty:        0,
			SyncStrategy: "sync-token",
		}); err != nil {
			e.logger.Error("upsert sync resource", "uid", uid, "error", err)
		}
		// persistImported flips dirty=1 via the Replace* services'
		// MarkResourceDirty side effect, and UpsertSyncResource's MAX
		// clause preserves it. Clear dirty — the server's version is now
		// authoritative — but guard the clear on the rev persistImported
		// captured inside its transaction so a concurrent local edit is not
		// silently dropped (issues #417 and #494). See applySyncCollection
		// for the full note.
		if err := e.clearDirtyAfterImport(ctx, calendarID, uid, res.ETag, revs[uid]); err != nil {
			e.logger.Warn("clear post-import dirty", "uid", uid, "error", err)
		}

		result.pulled++
		e.logger.Debug("pulled resource", "uid", uid, "path", res.Path, "etag", res.ETag)
	}

	// Deletions go through the same chokepoint as the sync-collection path.
	// QueryAll downloads the entire collection or returns an error (handled
	// above), so by construction the inventory is complete — but routing
	// through inferFromAbsence keeps the invariant uniform, so a future
	// partial-fetch optimization here cannot silently start deleting against
	// a partial view without flipping the complete flag.
	deletions := newPendingDeletions(e.logger)
	deletions.inferFromAbsence(calendarID, localResources, remoteUIDs, true, "complete (QueryAll)")
	// The full-snapshot path stores no sync-token, so a failed deletion here is
	// self-healing: the next snapshot re-infers the absence and retries.
	deleted, _ := deletions.apply(ctx, e, calendarID)
	result.deleted += deleted

	return result, nil
}

// multigetBatchSize bounds how many hrefs go into a single calendar-multiget.
// Servers (notably Google) reject very large multigets; 50 is the conservative
// number used by other clients and keeps response sizes manageable.
const multigetBatchSize = 50

// pendingDeletions is the single gate for the sync engine's most
// dangerous operation: a remove of local rows because the server no longer
// has them.
//
// Three production data-loss incidents share one root cause:
//   - multiget 404s
//   - href rewrites
//   - RFC 6578 §3.6 truncation
//
// A local row was deleted because it was ABSENT from a remote view that
// turned out to be incomplete.
//
// The two recorders below encode the only safe rule. Explicit deletions
// carry positive evidence (the server returned 404 for a specific href).
// Those are always sound. Absence-inferred deletions require a provably
// complete inventory. They are withheld otherwise.
//
// Every UID-level deletion the pull performs goes through apply(). A new
// "this looks deleted" code path cannot reach the executor unless it picks
// one of these two doors. The one sanctioned exception is override prune at
// row granularity inside a resource (pruneStaleOverrides). This type cannot
// host that prune. It still obeys the same completeness rule. See its
// comment for the gates.
type pendingDeletions struct {
	logger *slog.Logger
	owner  map[string]string // uid -> ownerType, deduped across both sources
}

func newPendingDeletions(logger *slog.Logger) *pendingDeletions {
	return &pendingDeletions{logger: logger, owner: make(map[string]string)}
}

// markExplicit records a deletion backed by positive evidence: the server
// returned 404 for this resource's specific href. Sound regardless of
// inventory completeness.
func (p *pendingDeletions) markExplicit(r storage.SyncResource) {
	if r.Uid != "" {
		p.owner[r.Uid] = r.OwnerType
	}
}

// inferFromAbsence records a deletion for every local resource the remote
// inventory (`seen`) does not contain, but ONLY when complete is true. When
// the inventory is partial it withholds the entire sweep. It logs the count
// and reason. A partial view can then never drive deletions. The rows are
// re-evaluated on the next clean sync. complete MUST be computed by the
// caller as pullView.inventoryObserved. That flag is true when the REPORT
// was not truncated, every listed href that has a local row was fetched,
// and every fetched body persisted. An unknown multiget miss has no local
// row. It does not flip complete. Local rows with no remote_url
// are skipped. They were never pushed.
func (p *pendingDeletions) inferFromAbsence(calendarID int64, locals []storage.SyncResource, seen map[string]bool, complete bool, reason string) {
	var candidates []storage.SyncResource
	for _, local := range locals {
		if local.RemoteUrl == "" {
			continue
		}
		if seen[local.Uid] || p.owner[local.Uid] != "" {
			continue
		}
		candidates = append(candidates, local)
	}
	if len(candidates) == 0 {
		return
	}
	if !complete {
		p.logger.Warn("withholding absence-inferred deletions: incomplete inventory",
			"calendar_id", calendarID, "withheld", len(candidates), "reason", reason)
		return
	}
	for _, c := range candidates {
		p.owner[c.Uid] = c.OwnerType
	}
}

// apply executes the accumulated deletions. It soft-deletes each local owner
// row and drops its sync_resource. It returns the count actually deleted and
// the count that failed. A failed soft-delete (for example a transient
// SQLITE_BUSY) leaves the local row orphaned. The server dropped it, but we
// did not. The caller must treat failed > 0 as an incomplete pull and
// withhold the sync-token. Otherwise the server, now past the old token,
// never re-reports the deletion. The orphan then survives forever with no
// retry.
func (p *pendingDeletions) apply(ctx context.Context, e *Engine, calendarID int64) (deleted, failed int) {
	for uid, ownerType := range p.owner {
		if err := e.deleteLocalResourceByUID(ctx, ownerType, uid); err != nil {
			e.logger.Error("delete local resource", "uid", uid, "owner_type", ownerType, "error", err)
			failed++
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        uid,
		}); err != nil {
			e.logger.Error("delete sync resource", "uid", uid, "error", err)
		}
		deleted++
	}
	return deleted, failed
}

// applySyncCollection consumes the change list from a sync-collection REPORT.
// It fetches bodies for changed resources via calendar-multiget. It persists
// them, applies deletions, and stores the new sync-token. This is the fast
// path for steady-state syncs against RFC 6578-capable servers.
func (e *Engine) applySyncCollection(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, cal storage.Calendar, syncResult *caldav.SyncCollectionResult, initialSnapshot bool) (*pullResult, error) {
	result := &pullResult{}
	view := pullView{truncated: syncResult.Truncated}

	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}
	tombstonedPaths := make(map[string]bool, len(tombstones))
	tombstonedUIDs := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
		if ts.RemoteUrl != "" {
			if p, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl); hrefErr == nil {
				tombstonedPaths[p] = true
			}
		}
		if ts.Uid != "" {
			tombstonedUIDs[ts.Uid] = true
		}
	}

	localResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list local resources: %w", err)
	}
	localByPath := make(map[string]storage.SyncResource, len(localResources))
	for _, r := range localResources {
		if r.RemoteUrl == "" {
			continue
		}
		p, hrefErr := client.CanonicalObjectRef(remoteURL, r.RemoteUrl)
		if hrefErr != nil {
			continue
		}
		localByPath[p] = r
	}

	var fetchPaths []string
	var deletedPaths []string
	seenUIDs := make(map[string]bool, len(syncResult.Changes))
	for _, ch := range syncResult.Changes {
		canonical, hrefErr := client.CanonicalObjectRef(remoteURL, ch.Path)
		if hrefErr != nil {
			e.logger.Warn("skip out-of-scope change href", "calendar_id", calendarID, "path", ch.Path, "error", hrefErr)
			continue
		}
		if ch.Deleted {
			deletedPaths = append(deletedPaths, canonical)
			continue
		}
		if tombstonedPaths[canonical] {
			continue
		}
		if local, exists := localByPath[canonical]; exists && local.Etag == ch.ETag {
			seenUIDs[local.Uid] = true
			continue
		}
		fetchPaths = append(fetchPaths, canonical)
	}

	pending, err := loadPendingHrefs(ctx, e.q, e.logger, calendarID)
	if err != nil {
		return nil, err
	}
	deletedSet := make(map[string]bool, len(deletedPaths))
	for _, pth := range deletedPaths {
		deletedSet[pth] = true
	}
	pending.forgetSet(ctx, deletedSet)
	pending.forgetSet(ctx, tombstonedPaths)
	fetchPaths = pending.appendUnseen(fetchPaths)

	for start := 0; start < len(fetchPaths); start += multigetBatchSize {
		end := start + multigetBatchSize
		if end > len(fetchPaths) {
			end = len(fetchPaths)
		}
		batch := fetchPaths[start:end]
		multi, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (*caldav.MultiGetResult, error) {
			return client.MultiGetTolerant(ctx, remoteURL, batch)
		})
		if err != nil {
			return nil, fmt.Errorf("multiget batch %d: %w", start, err)
		}
		// Per-resource 404s here are NOT deletions. Google can list an href
		// that 404s on multiget for a reason other than a real delete.
		// classifyMultigetMiss splits a known miss (local row: incomplete)
		// from an unknown miss (no local row: record and retry). An
		// uncanonical href carries neither risk: skip it. See pullView.
		for _, miss := range multi.Missing {
			canonical, hrefErr := client.CanonicalObjectRef(remoteURL, miss)
			kind, local := classifyMultigetMiss(canonical, hrefErr, localByPath)
			e.logger.Warn("multiget href missing", "kind", string(kind), "calendar_id", calendarID, "href", miss)
			switch kind {
			case multigetMissKnown:
				view.knownMisses++
				// Treat the missing path's UID as "still seen" so the initial
				// snapshot deletion loop below does not conclude the resource
				// is gone from the server. Otherwise an empty token + a
				// transient multiget 404 would soft-delete the local event
				// even though we have no actual evidence of deletion.
				seenUIDs[local.Uid] = true
			case multigetMissUncanonical:
				// CanonicalObjectRef rejected this href (query or fragment,
				// another origin, a collection path). localByPath holds
				// canonical paths only, so no local row maps to this miss
				// and there is no data to lose. A retry obligation cannot
				// converge either: the resource loop below discards any body
				// served under an uncanonical path. Log and skip the miss so
				// a broken or hostile server cannot hold back the sync
				// token forever. See issue #625.
			case multigetMissUnknown:
				if recErr := pending.noteMiss(ctx, canonical); recErr != nil {
					e.logger.Warn("record unknown multiget miss", "calendar_id", calendarID, "href", miss, "error", recErr)
					view.pendingRecordFails++
				}
			}
		}
		for _, res := range multi.Resources {
			resPath, hrefErr := client.CanonicalObjectRef(remoteURL, res.Path)
			if hrefErr != nil {
				e.logger.Warn("skip out-of-scope multiget href", "path", res.Path, "error", hrefErr)
				continue
			}
			if res.Data == nil {
				continue
			}
			var buf bytes.Buffer
			enc := ical.NewEncoder(&buf)
			if err := enc.Encode(res.Data); err != nil {
				e.logger.Warn("encode fetched resource failed", "path", res.Path, "error", err)
				continue
			}
			importResult, err := icalPkg.ImportFile(strings.NewReader(buf.String()))
			if err != nil {
				e.logger.Warn("import fetched resource failed", "path", res.Path, "error", err)
				continue
			}
			result.warnings = append(result.warnings, e.noteImportWarnings(res.Path, importResult)...)
			uid := extractUID(importResult)
			if uid == "" {
				e.logger.Warn("no UID in fetched resource", "path", res.Path)
				continue
			}
			seenUIDs[uid] = true
			if tombstonedUIDs[uid] {
				pending.forget(ctx, resPath)
				continue
			}
			ownerType := detectOwnerType(importResult)
			revs, alarmWarnings, persistErr := e.persistImported(ctx, calendarID, importResult)
			if persistErr != nil {
				// A changed body we fetched but couldn't store (transient
				// SQLite busy/lock, or a malformed component a Replace*
				// rejects). Leave the sync_resource on its old etag and count
				// the failure so the inventory is treated as incomplete: the
				// token is withheld and the next REPORT re-lists this change
				// for another attempt. Advancing the token here would skip the
				// change permanently until the server touches it again.
				view.persistFailures++
				e.logger.Error("persist imported resource", "uid", uid, "path", res.Path, "error", persistErr)
				continue
			}
			result.warnings = append(result.warnings, e.notePersistWarnings(res.Path, uid, alarmWarnings)...)
			if err := e.q.UpsertSyncResource(ctx, storage.UpsertSyncResourceParams{
				CalendarID:   calendarID,
				Uid:          uid,
				OwnerType:    ownerType,
				RemoteUrl:    resPath,
				Etag:         res.ETag,
				Dirty:        0,
				SyncStrategy: "sync-token",
			}); err != nil {
				e.logger.Error("upsert sync resource", "uid", uid, "error", err)
			}
			// persistImported goes through the event/todo/journal services,
			// whose Replace* methods all flip dirty=1 via MarkResourceDirty
			// as a side effect (correct for user-initiated edits, wrong for
			// sync-driven imports). UpsertSyncResource's `dirty = MAX(...)`
			// clause then preserves that 1, so without an explicit clear here
			// every pull re-dirties everything it just absorbed and the next
			// push round-trips it back to the server. Clear dirty since the
			// server's version is now authoritative locally, but guard the
			// clear on the rev persistImported captured inside its transaction
			// so a concurrent local edit is not silently dropped (issues #417
			// and #494).
			if err := e.clearDirtyAfterImport(ctx, calendarID, uid, res.ETag, revs[uid]); err != nil {
				e.logger.Warn("clear post-import dirty", "uid", uid, "error", err)
			}
			pending.forget(ctx, resPath)
			result.pulled++
		}
	}

	// All deletions funnel through one chokepoint (see pendingDeletions).
	// pullView names the two questions: inventoryObserved (absence may
	// infer) and localRowsSafe (token may advance). An unknown miss has
	// no local row to lose. A persist failure leaves the local copy
	// behind the server, so the token must be withheld too.
	deletions := newPendingDeletions(e.logger)

	// Explicit deletions: the server returned a top-level 404 for these
	// hrefs. Positive evidence — sound even if the inventory is incomplete.
	// Exception: an href rewrite (Cosmo/GMX) shows the same UID 404'd at the
	// old path but present at a new one within the same response; the fetch
	// loop already re-upserted it, so a seen UID is not a deletion.
	for _, pth := range deletedPaths {
		local, exists := localByPath[pth]
		if !exists || seenUIDs[local.Uid] {
			continue
		}
		deletions.markExplicit(local)
	}

	// Absence-inferred deletions: an initial snapshot lists only additions,
	// so a locally-tracked UID missing from it is gone on the server — but
	// only when the inventory is complete. Incremental pulls carry deletions
	// explicitly (above), so absence inference applies to initial snapshots
	// only. The gate withholds the sweep on a partial inventory; pull()
	// paginates so the common path is complete, but the invariant is
	// enforced here, where the deletion happens, not only where data is
	// fetched.
	if initialSnapshot {
		deletions.inferFromAbsence(calendarID, localResources, seenUIDs,
			view.inventoryObserved(), view.absenceWithholdReason())
	}

	deleted, deleteFailures := deletions.apply(ctx, e, calendarID)
	result.deleted += deleted

	if syncResult.SyncToken != "" && view.localRowsSafe() && deleteFailures == 0 {
		token := syncResult.SyncToken
		if err := e.q.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
			ID:        calendarID,
			Ctag:      cal.Ctag,
			SyncToken: &token,
		}); err != nil {
			e.logger.Warn("update sync token", "calendar_id", calendarID, "error", err)
		}
	} else if view.incomplete() || deleteFailures > 0 {
		// Pull made partial progress: a known miss, a persist failure, a
		// failed pending-href record, or a failed deletion. We do not
		// advance the sync-token, so the next sync re-lists the same
		// change set. Slow but safe.
		e.logger.Warn("not advancing sync-token: incomplete pull",
			"calendar_id", calendarID, "known_misses", view.knownMisses,
			"persist_failures", view.persistFailures,
			"pending_record_fails", view.pendingRecordFails,
			"delete_failures", deleteFailures)
		// Surface the incompleteness so the calendar is recorded unhealthy
		// (LastSyncError) rather than healthy. A pull that can never converge
		// — a permanent persist failure, an href that always 404s on
		// multiget, or a server-reported deletion that won't apply locally —
		// otherwise only logs, leaving LastSyncError clear and the ambient ⚠
		// sidebar glyph dark while sync stays silently stuck.
		result.errors = append(result.errors, fmt.Errorf(
			"incomplete pull: not advancing sync-token (%d known miss(es), %d persist failure(s), %d pending-record failure(s), %d delete failure(s))",
			view.knownMisses, view.persistFailures, view.pendingRecordFails, deleteFailures))
	}

	return result, nil
}

// Owner-type strings stamped on every sync_resource row and CalDAV change
// record. CalDAV tracks one resource per UID, partitioned by component type.
const (
	ownerTypeEvent   = "event"
	ownerTypeTodo    = "todo"
	ownerTypeJournal = "journal"
)

// errUnknownOwnerType reports a sync_resource OwnerType the engine does not
// recognize. Every owner-type dispatch surfaces it instead of a guess. A
// new (or misspelled) component type then fails loudly rather than a silent
// mis-resolve. Notably, lookupOwnerID no longer reports ID 0 without error.
var errUnknownOwnerType = errors.New("unknown owner type")

// ownerOps bundles the per-component-type operations the sync engine
// dispatches on a sync_resource's OwnerType. Each component type is enumerated
// exactly once in ownerOpsByType. A fourth type is then a single map
// entry rather than synchronized edits to parallel switches. A missed
// type cannot compile cleanly into a silent mis-dispatch.
type ownerOps struct {
	softDeleteByUID func(ctx context.Context, e *Engine, uid string) error
	lookupID        func(ctx context.Context, e *Engine, uid string) (int64, error)
	export          func(ctx context.Context, e *Engine, uid string) ([]byte, error)
}

var ownerOpsByType = map[string]ownerOps{
	ownerTypeEvent: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteEventsByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetEventByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeEvent,
				e.events.GetByUID, e.events.ListOverridesByUID, hydrateEvent, icalPkg.ExportEvents)
		},
	},
	ownerTypeTodo: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteTodosByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetTodoByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeTodo,
				e.todos.GetByUID, e.todos.ListOverridesByUID, hydrateTodo, icalPkg.ExportTodos)
		},
	},
	ownerTypeJournal: {
		softDeleteByUID: func(ctx context.Context, e *Engine, uid string) error {
			return e.q.SoftDeleteJournalsByUID(ctx, uid)
		},
		lookupID: func(ctx context.Context, e *Engine, uid string) (int64, error) {
			row, err := e.q.GetJournalByUID(ctx, uid)
			if err != nil {
				return 0, err
			}
			return row.ID, nil
		},
		export: func(ctx context.Context, e *Engine, uid string) ([]byte, error) {
			return exportResourceFor(ctx, e, uid, ownerTypeJournal,
				e.journals.GetByUID, e.journals.ListOverridesByUID, hydrateJournal, icalPkg.ExportJournals)
		},
	},
}

// exportResourceFor bundles a UID's master row plus its override rows into an
// iCal payload. CalDAV tracks one resource per UID. Recurring resources are
// stored as a master row plus override rows that share the UID. We normally
// bundle master plus overrides so instance edits round-trip to the server.
//
// Google sometimes serves a single orphan instance under a UID like
// `<master>_R<recurrence-id>@google.com` with a RECURRENCE-ID property and no
// master in the same iCal stream. We import those as override rows with no
// master sibling. The master lookup then fails. We therefore append every
// live row under the UID and export the non-empty result. We return
// errResourceMissing only when nothing remains.
func exportResourceFor[T any](
	ctx context.Context,
	e *Engine,
	uid, kind string,
	get func(context.Context, string) (T, error),
	listOverrides func(context.Context, string) ([]T, error),
	hydrate func(context.Context, *Engine, *T) error,
	export func([]T, string) ([]byte, error),
) ([]byte, error) {
	var rows []T
	switch row, err := get(ctx, uid); {
	case err == nil:
		rows = append(rows, row)
	case !errors.Is(err, sql.ErrNoRows):
		// Only "no master row" is an expected outcome here (the orphan-instance
		// case described above). Treating a transient failure — SQLITE_BUSY, an
		// I/O error — the same way would export the overrides alone, PUT a
		// resource with the master VEVENT and its recurrence rule amputated,
		// then clear the dirty flag so nothing ever retries.
		return nil, fmt.Errorf("get %s master uid %s: %w", kind, uid, err)
	}
	overrides, err := listOverrides(ctx, uid)
	if err != nil {
		return nil, fmt.Errorf("list overrides for %s uid %s: %w", kind, uid, err)
	}
	rows = append(rows, overrides...)
	if len(rows) == 0 {
		return nil, fmt.Errorf("%w: %s uid %s", errResourceMissing, kind, uid)
	}
	for i := range rows {
		if err := hydrate(ctx, e, &rows[i]); err != nil {
			// A hydration failure means the exported iCal would silently omit
			// alarms/attendees/attachments/... and the PUT would strip them
			// from the server copy. Abort instead: the dirty flag stays set, so
			// the resource is retried whole on the next sync.
			//
			// A deterministic failure (a corrupt row a retry can never fix)
			// wedges the resource: every sync fails identically and no edit
			// under this UID reaches the server. Name the broken relations and
			// point at the escape hatch so the user learns what is stuck and
			// why (issue #568).
			var hErr *hydratepkg.HydrationError
			if errors.As(err, &hErr) && len(hErr.Failures) > 0 {
				rels := make([]string, 0, len(hErr.Failures))
				for _, f := range hErr.Failures {
					rels = append(rels, f.Relation)
				}
				return nil, fmt.Errorf(
					"hydrate %s uid %s: unreadable relation(s) %s; the resource is stuck and no edit reaches the server until it is resolved (see chroncal sync doctor): %w",
					kind, uid, strings.Join(rels, ", "), err)
			}
			return nil, fmt.Errorf("hydrate %s uid %s: %w", kind, uid, err)
		}
	}
	return export(rows, "")
}

// ownerOpsFor resolves the dispatch table for an owner type. It returns
// errUnknownOwnerType for any type not registered above.
func ownerOpsFor(ownerType string) (ownerOps, error) {
	ops, ok := ownerOpsByType[ownerType]
	if !ok {
		return ownerOps{}, fmt.Errorf("%w: %q", errUnknownOwnerType, ownerType)
	}
	return ops, nil
}

func (e *Engine) deleteLocalResourceByUID(ctx context.Context, ownerType, uid string) error {
	// Soft-delete across every owner type so a remote DELETE that races with
	// a user action doesn't nuke the local row — it stays in trash until the
	// retention window expires. The caller clears the sync_resource so a
	// later restore re-CREATEs a fresh one via MarkResourceDirty.
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return err
	}
	return ops.softDeleteByUID(ctx, e, uid)
}

// lookupOwnerID resolves the local row ID that backs a UID for the given owner
// type. It returns errUnknownOwnerType for an unrecognized type and the
// lookup error when no live row exists. Callers then never silently
// attribute a record to ID 0.
func (e *Engine) lookupOwnerID(ctx context.Context, ownerType, uid string) (int64, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return 0, err
	}
	return ops.lookupID(ctx, e, uid)
}

type tombstoneResult struct {
	deleted   int
	conflicts int
	errors    []error
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*tombstoneResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}

	// Index the last-seen ETags so each DELETE can be made conditional. A real
	// lookup failure must abort rather than silently degrade to unconditional
	// DELETEs that could clobber a concurrent remote edit, so we surface it.
	syncResources, err := e.q.ListSyncResourcesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list sync resources: %w", err)
	}
	etagByUID := make(map[string]string, len(syncResources))
	for _, sr := range syncResources {
		etagByUID[sr.Uid] = sr.Etag
	}

	result := &tombstoneResult{}
	for _, ts := range tombstones {
		deletePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
		if hrefErr != nil {
			result.errors = append(result.errors, fmt.Errorf("validate tombstone %s: %w", ts.Uid, hrefErr))
			continue
		}
		// Make the DELETE conditional on the ETag we last saw so the server
		// rejects it (412) if the resource was edited remotely after our last
		// sync. Without this, the tombstone push would silently destroy that
		// concurrent edit. An untracked resource (never synced, or already
		// cleaned up) has no ETag, which falls back to an unconditional DELETE.
		etag := etagByUID[ts.Uid]
		e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", deletePath)
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.DeleteResource(ctx, deletePath, etag)
		}); err != nil && !errors.Is(err, caldav.ErrResourceGone) {
			// A 404/410 means the resource is already absent server-side —
			// the desired end state — so fall through and clear the local
			// rows instead of re-issuing the DELETE on every sync.
			if caldav.IsConflict(err) {
				// 412: the resource was edited remotely after we last saw it.
				// Honoring the delete would discard that edit, so abandon it.
				// Drop the tombstone (stop re-issuing the DELETE every sync)
				// but keep the sync_resource so the next pull re-imports the
				// remote version, resurrecting the item with its remote edit.
				// A delete-vs-edit conflict always preserves the remote edit
				// (the non-destructive choice), independent of ConflictStrategy.
				e.logger.Warn("tombstone delete conflict: remote edited, preserving remote version", "uid", ts.Uid)
				if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
					e.logger.Warn("delete tombstone row after conflict failed", "uid", ts.Uid, "error", err)
				}
				result.conflicts++
				continue
			}
			e.logger.Warn("delete remote resource failed", "uid", ts.Uid, "error", err)
			result.errors = append(result.errors, fmt.Errorf("delete tombstone %s: %w", ts.Uid, err))
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        ts.Uid,
		}); err != nil {
			e.logger.Warn("delete sync resource after tombstone", "uid", ts.Uid, "error", err)
		}
		if err := e.q.DeleteTombstone(ctx, ts.ID); err != nil {
			e.logger.Warn("delete tombstone row failed", "uid", ts.Uid, "error", err)
		}
		result.deleted++
	}
	return result, nil
}

func (e *Engine) syncCalendarMetadata(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) error {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return fmt.Errorf("get calendar for metadata sync: %w", err)
	}

	// Google CalendarList already supplies the color at discovery. Apple
	// calendar-color is not a Google CalDAV property, so never PROPFIND
	// it. A dirty local color still has to be written: CalendarList PATCH
	// is the Google equivalent of Apple calendar-color PROPPATCH. Clearing
	// ColorDirty after a successful write lets later Discover rounds adopt
	// CalendarList again. A failed write must not fail event sync
	// (issue #628); the dirty latch then keeps the local override.
	if caldav.IsGoogleCalendarEndpoint(remoteURL) {
		if cal.ColorDirty == 0 {
			return nil
		}
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetGoogleCalendarListColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			e.logger.Warn("set google calendar color failed", "calendar_id", calendarID, "error", err)
			return nil
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	// A dirty local color wins: push it and clear the flag. Skip the remote
	// fetch entirely — its value would be discarded, and a failed fetch must
	// not block the pending push or strand ColorDirty (issue #419).
	if cal.ColorDirty != 0 {
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.SetCalendarColor(ctx, remoteURL, cal.Color)
		}); err != nil {
			return fmt.Errorf("set remote calendar color: %w", err)
		}
		if err := e.calendars.ClearColorDirty(ctx, calendarID, cal.Color); err != nil {
			return fmt.Errorf("clear calendar color dirty: %w", err)
		}
		return nil
	}

	remoteColor, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		return client.GetCalendarColor(ctx, remoteURL)
	})
	if err != nil {
		// Color is decorative. A server that refuses calendar-color must
		// not fail the rest of the calendar sync (issue #628).
		e.logger.Warn("get remote calendar color failed", "calendar_id", calendarID, "error", err)
		return nil
	}
	if remoteColor == "" {
		// The server does not advertise a color. Keep the color that
		// discovery or the user already set.
		return nil
	}

	if remoteColor != storage.NullableToString(cal.RemoteColor) {
		if err := e.calendars.UpdateColorFromSync(ctx, calendarID, remoteColor, remoteColor); err != nil {
			return fmt.Errorf("update calendar color from sync: %w", err)
		}
	}

	return nil
}

// errResourceMissing reports that no live local row exists for a UID we were
// asked to export. Push uses errors.Is on this to distinguish a local row
// that is gone (clear the dirty flag, stop the retry) from a real export
// failure.
var errResourceMissing = errors.New("local resource missing for uid")

// exportResource exports a local resource to iCal bytes. It dispatches on
// owner type. See exportResourceFor for the master/override bundle behavior.
func (e *Engine) exportResource(ctx context.Context, ownerType string, uid string) ([]byte, error) {
	ops, err := ownerOpsFor(ownerType)
	if err != nil {
		return nil, err
	}
	return ops.export(ctx, e, uid)
}

// hydrateEvent/hydrateTodo/hydrateJournal adapt the domain services' Hydrate
// methods to the ownerOps export signature. The relation list itself lives in
// the services (event.Service.Hydrate and friends). Sync, file export, and
// the CLI then cannot drift apart on what a complete record contains.
func hydrateEvent(ctx context.Context, e *Engine, evt *event.Event) error {
	return e.events.Hydrate(ctx, evt)
}

func hydrateTodo(ctx context.Context, e *Engine, t *todo.Todo) error {
	return e.todos.Hydrate(ctx, t)
}

func hydrateJournal(ctx context.Context, e *Engine, j *journal.Journal) error {
	return e.journals.Hydrate(ctx, j)
}

// persistImported saves parsed iCal data to the local database using the same
// upsert pattern as the CLI import command.
//
// It returns the post-import sync_resources.rev for each persisted UID. The
// value is read inside the same transaction that bumped it via
// MarkResourceDirty. Accept-server callers feed that rev to
// FinalizePushedResource. The dirty clear is then guarded on a rev captured
// atomically with the import. It is not re-read after commit. A concurrent
// local edit could then slip in and have its dirty flag wiped. That is the
// lost-update window of issue #494. A UID with no sync_resources row yet (a
// first pull) reports rev 0.
//
// The second return value lists the alarms this function dropped. An alarm
// the write rule rejects must not fail its whole resource: the record and
// its valid alarms still persist, and the caller reports each drop as a
// warning (issue #585). Every other error still fails the resource, so the
// caller keeps it dirty and retries.
func (e *Engine) persistImported(ctx context.Context, calendarID int64, result icalPkg.ImportResult) (map[string]int64, []string, error) {
	revs := make(map[string]int64)
	var alarmWarnings []string

	// Build the prune inputs up front: per-UID keep-sets of the components
	// the server sent, plus each prunable UID's dirty flag — which must be
	// read before the upserts below flip it via MarkResourceDirty, because
	// the override pruning at the end needs the pre-import value to
	// distinguish "the server dropped this override" from "a local override
	// the server has never seen" (an unpushed local edit). A component the
	// parser dropped (SkippedComponents != 0) makes the keep-sets an
	// incomplete inventory, so pruning is disabled wholesale: the nil maps
	// below make pruneStaleOverrides a no-op.
	var eventKeep, todoKeep, journalKeep map[string]map[string]bool
	var dirtyBefore map[string]bool
	if result.SkippedComponents == 0 {
		eventKeep = keepSets(result.Events, func(v event.Event) (string, string) { return v.UID, v.RecurrenceID })
		todoKeep = keepSets(result.Todos, func(v todo.Todo) (string, string) { return v.UID, v.RecurrenceID })
		journalKeep = keepSets(result.Journals, func(v journal.Journal) (string, string) { return v.UID, v.RecurrenceID })
		var err error
		dirtyBefore, err = e.preImportDirty(ctx, calendarID, eventKeep, todoKeep, journalKeep)
		if err != nil {
			return nil, nil, err
		}
	}

	// Store timezones
	for _, tz := range result.Timezones {
		if _, err := e.q.UpsertTimezone(ctx, storage.UpsertTimezoneParams{
			Tzid:          tz.TZID,
			VtimezoneData: tz.Data,
		}); err != nil {
			e.logger.Warn("store VTIMEZONE", "tzid", tz.TZID, "error", err)
		}
	}

	// Import events. Each resource's upsert plus its child-collection replaces
	// run in one transaction (inTx) so a mid-sequence failure rolls back to the
	// prior consistent state rather than leaving a half-updated row (e.g. new
	// alarms but stale attendees). The resource stays dirty and is retried.
	for _, ev := range result.Events {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			events := e.events.WithTx(tx)
			saved, err := events.UpsertByUID(ctx, event.UpsertParams{
				UID: ev.UID, CalendarID: calendarID,
				Title: ev.Title, Description: ev.Description, Location: ev.Location,
				StartTime: ev.StartTime, EndTime: ev.EndTime, AllDay: ev.AllDay,
				RecurrenceRule: ev.RecurrenceRule, Timezone: ev.Timezone,
				Status: ev.Status, Transp: ev.Transp, Sequence: ev.Sequence,
				Priority: ev.Priority, Class: ev.Class, URL: ev.URL,
				ConferenceURI: ev.ConferenceURI,
				Categories:    ev.Categories, ExDates: ev.ExDates, RDates: ev.RDates,
				RecurrenceID: ev.RecurrenceID, Geo: ev.Geo,
				DurationValue: ev.DurationValue, DtStamp: ev.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert event %q: %w", ev.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated, mirroring how Categories are handled
			// via UpsertByUID. A full CalDAV pull sends the complete component, so
			// the absence of a property means "cleared", not "unknown". Propagate
			// any replace error so the caller keeps the resource dirty and retries.
			// An alarm the write rule rejects is the one exception. The partition
			// drops it, and the record still persists (issue #585).
			okAlarms, badAlarms := model.PartitionAlarmsForWrite(ev.Alarms)
			for _, bad := range badAlarms {
				alarmWarnings = append(alarmWarnings,
					fmt.Sprintf("event %q: alarm %d dropped: %v", ev.UID, bad.Index, bad.Err))
			}
			if err := events.ReplaceAlarmsForSync(ctx, saved.ID, okAlarms); err != nil {
				return fmt.Errorf("replace alarms for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceAttendeesForSync(ctx, saved.ID, ev.Attendees); err != nil {
				return fmt.Errorf("replace attendees for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceAttachments(ctx, saved.ID, ev.Attachments); err != nil {
				return fmt.Errorf("replace attachments for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceComments(ctx, saved.ID, ev.Comments); err != nil {
				return fmt.Errorf("replace comments for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceContacts(ctx, saved.ID, ev.Contacts); err != nil {
				return fmt.Errorf("replace contacts for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceResources(ctx, saved.ID, ev.Resources); err != nil {
				return fmt.Errorf("replace resources for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceRelations(ctx, saved.ID, ev.Relations); err != nil {
				return fmt.Errorf("replace relations for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceXProperties(ctx, saved.ID, ev.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for event %q: %w", ev.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, ev.UID)
			if err != nil {
				return fmt.Errorf("capture rev for event %q: %w", ev.UID, err)
			}
			revs[ev.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Import todos. One transaction per resource; see the event loop above.
	for _, t := range result.Todos {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			todos := e.todos.WithTx(tx)
			saved, err := todos.UpsertByUID(ctx, todo.UpsertParams{
				UID: t.UID, CalendarID: calendarID,
				Summary: t.Summary, Description: t.Description, Location: t.Location,
				DueDate: t.DueDate, StartDate: t.StartDate, Duration: t.Duration,
				CompletedAt: t.CompletedAt, PercentComplete: t.PercentComplete,
				Status: t.Status, Priority: t.Priority, Class: t.Class,
				URL: t.URL, Categories: t.Categories,
				RecurrenceRule: t.RecurrenceRule, Timezone: t.Timezone,
				Sequence: t.Sequence, ExDates: t.ExDates, RDates: t.RDates,
				RecurrenceID: t.RecurrenceID, Geo: t.Geo,
				DtStamp: t.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert todo %q: %w", t.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated. See the event loop above.
			okAlarms, badAlarms := model.PartitionAlarmsForWrite(t.Alarms)
			for _, bad := range badAlarms {
				alarmWarnings = append(alarmWarnings,
					fmt.Sprintf("todo %q: alarm %d dropped: %v", t.UID, bad.Index, bad.Err))
			}
			if err := todos.ReplaceAlarmsForSync(ctx, saved.ID, okAlarms); err != nil {
				return fmt.Errorf("replace alarms for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceAttendees(ctx, saved.ID, t.Attendees); err != nil {
				return fmt.Errorf("replace attendees for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceAttachments(ctx, saved.ID, t.Attachments); err != nil {
				return fmt.Errorf("replace attachments for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceComments(ctx, saved.ID, t.Comments); err != nil {
				return fmt.Errorf("replace comments for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceContacts(ctx, saved.ID, t.Contacts); err != nil {
				return fmt.Errorf("replace contacts for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceResources(ctx, saved.ID, t.Resources); err != nil {
				return fmt.Errorf("replace resources for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceRelations(ctx, saved.ID, t.Relations); err != nil {
				return fmt.Errorf("replace relations for todo %q: %w", t.UID, err)
			}
			if err := todos.ReplaceXProperties(ctx, saved.ID, t.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for todo %q: %w", t.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, t.UID)
			if err != nil {
				return fmt.Errorf("capture rev for todo %q: %w", t.UID, err)
			}
			revs[t.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Import journals. One transaction per resource; see the event loop above.
	for _, j := range result.Journals {
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			journals := e.journals.WithTx(tx)
			saved, err := journals.UpsertByUID(ctx, journal.UpsertParams{
				UID: j.UID, CalendarID: calendarID,
				Summary: j.Summary, Description: j.Description,
				StartDate: j.StartDate, Status: j.Status, Class: j.Class,
				URL: j.URL, Categories: j.Categories,
				RecurrenceRule: j.RecurrenceRule, Timezone: j.Timezone,
				Sequence: j.Sequence, ExDates: j.ExDates, RDates: j.RDates,
				RecurrenceID: j.RecurrenceID,
				DtStamp:      j.DtStamp,
			})
			if err != nil {
				return fmt.Errorf("upsert journal %q: %w", j.UID, err)
			}
			// Replace child collections unconditionally so server-side removals
			// (an empty list) are propagated. See the event loop above.
			if err := journals.ReplaceAttendees(ctx, saved.ID, j.Attendees); err != nil {
				return fmt.Errorf("replace attendees for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceAttachments(ctx, saved.ID, j.Attachments); err != nil {
				return fmt.Errorf("replace attachments for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceComments(ctx, saved.ID, j.Comments); err != nil {
				return fmt.Errorf("replace comments for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceContacts(ctx, saved.ID, j.Contacts); err != nil {
				return fmt.Errorf("replace contacts for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceRelations(ctx, saved.ID, j.Relations); err != nil {
				return fmt.Errorf("replace relations for journal %q: %w", j.UID, err)
			}
			if err := journals.ReplaceXProperties(ctx, saved.ID, j.XProperties); err != nil {
				return fmt.Errorf("replace xproperties for journal %q: %w", j.UID, err)
			}
			rev, err := captureImportRev(ctx, e.q.WithTx(tx), calendarID, j.UID)
			if err != nil {
				return fmt.Errorf("capture rev for journal %q: %w", j.UID, err)
			}
			revs[j.UID] = rev
			return nil
		}); err != nil {
			return nil, nil, err
		}
	}

	// Prune overrides the server dropped (e.g. a deleted instance that became
	// an EXDATE on the master); see pruneStaleOverrides for the safety gates.
	if err := pruneStaleOverrides(ctx, e, calendarID, eventKeep, dirtyBefore, revs,
		e.q.ListOverridesByUID,
		func(v storage.Event) int64 { return v.ID },
		func(v storage.Event) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteEvent,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale event overrides: %w", err)
	}
	if err := pruneStaleOverrides(ctx, e, calendarID, todoKeep, dirtyBefore, revs,
		e.q.ListTodoOverridesByUID,
		func(v storage.Todo) int64 { return v.ID },
		func(v storage.Todo) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteTodo,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale todo overrides: %w", err)
	}
	if err := pruneStaleOverrides(ctx, e, calendarID, journalKeep, dirtyBefore, revs,
		e.q.ListJournalOverridesByUID,
		func(v storage.Journal) int64 { return v.ID },
		func(v storage.Journal) string { return v.RecurrenceID },
		(*storage.Queries).SoftDeleteJournal,
	); err != nil {
		return nil, nil, fmt.Errorf("prune stale journal overrides: %w", err)
	}

	return revs, alarmWarnings, nil
}

// keepSets groups imported components into per-UID keep-sets of their
// RECURRENCE-IDs, the inventory pruneStaleOverrides reconciles against.
// Returns nil for an empty slice so empty domains cost nothing.
func keepSets[C any](items []C, key func(C) (uid, rid string)) map[string]map[string]bool {
	if len(items) == 0 {
		return nil
	}
	keeps := make(map[string]map[string]bool)
	for _, item := range items {
		uid, rid := key(item)
		keep := keeps[uid]
		if keep == nil {
			keep = make(map[string]bool)
			keeps[uid] = keep
		}
		keep[rid] = true
	}
	return keeps
}

// preImportDirty reads the sync dirty flag for every UID that override
// prune may reconcile. Those are UIDs whose master (recurrence_id == "") is
// in their keep-set. It must run before persistImported's upserts, which flip
// the flag via MarkResourceDirty. A UID with no sync_resources row (a first
// pull) reads as clean.
func (e *Engine) preImportDirty(ctx context.Context, calendarID int64, keeps ...map[string]map[string]bool) (map[string]bool, error) {
	dirty := make(map[string]bool)
	for _, keepByUID := range keeps {
		for uid, keep := range keepByUID {
			if !keep[""] {
				continue
			}
			if _, seen := dirty[uid]; seen {
				continue
			}
			res, err := e.q.GetSyncResource(ctx, storage.GetSyncResourceParams{
				CalendarID: calendarID,
				Uid:        uid,
			})
			if errors.Is(err, sql.ErrNoRows) {
				dirty[uid] = false
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read pre-import sync state for %q: %w", uid, err)
			}
			dirty[uid] = res.Dirty != 0
		}
	}
	return dirty, nil
}

// pruneStaleOverrides soft-deletes local override rows whose recurrence_id the
// server no longer has. When a CalDAV server deletes a recurring instance it
// drops the override component from the resource and adds the slot to the
// master's EXDATE. The master upsert carries the EXDATE. The stale
// override row must be pruned separately. Otherwise expansion restores the
// deleted instance. The orphan checker ignores EXDATEs so a legitimate
// override is never mistaken for an orphan.
//
// This is the sanctioned row-granularity counterpart of pendingDeletions'
// absence-inferred deletions (see that type's comment). It obeys the same
// rule: absence only counts against a provably complete inventory. The caller
// passes nil keep-sets when the parser dropped a component (an incomplete
// inventory). Each UID is reconciled only when:
//   - its own master (recurrence_id == "") is in its keep-set. An
//     overrides-only resource is unusual. Another UID's master says
//     nothing about this UID's inventory.
//   - its resource was not dirty before this import. A dirty resource has
//     unpushed local changes. A locally created override is absent from
//     the server body because the server did not see it, not because it
//     was deleted.
//   - its rev is unchanged inside the delete transaction. A local edit that
//     landed after this import bumped rev. The rows listed here may no
//     longer reflect it.
//
// A skipped prune is safe. The rows stay live. The dirty records that
// blocked it push or reconcile them on a later cycle.
func pruneStaleOverrides[R any](
	ctx context.Context,
	e *Engine,
	calendarID int64,
	keepByUID map[string]map[string]bool,
	dirtyBefore map[string]bool,
	revs map[string]int64,
	list func(context.Context, string) ([]R, error),
	idOf func(R) int64,
	ridOf func(R) string,
	del func(*storage.Queries, context.Context, int64) error,
) error {
	for uid, keep := range keepByUID {
		if !keep[""] || dirtyBefore[uid] {
			continue
		}
		existing, err := list(ctx, uid)
		if err != nil {
			return fmt.Errorf("list overrides %q: %w", uid, err)
		}
		var stale []int64
		for _, o := range existing {
			if !keep[ridOf(o)] {
				stale = append(stale, idOf(o))
			}
		}
		// The common case — a resource with no stale overrides — ends here,
		// without paying for a transaction.
		if len(stale) == 0 {
			continue
		}
		if err := e.inTx(ctx, func(tx *sql.Tx) error {
			qtx := e.q.WithTx(tx)
			rev, err := captureImportRev(ctx, qtx, calendarID, uid)
			if err != nil {
				return err
			}
			if rev != revs[uid] {
				return nil
			}
			for _, id := range stale {
				if err := del(qtx, ctx, id); err != nil {
					return err
				}
			}
			return nil
		}); err != nil {
			return fmt.Errorf("prune overrides %q: %w", uid, err)
		}
	}
	return nil
}

// captureImportRev reads the sync_resources.rev for uid using qtx (a Queries
// bound to the import's transaction). The value then reflects the rev as
// bumped by this import and nothing committed after it. A UID with no
// sync_resources row yet (a first pull, before UpsertSyncResource creates it)
// reports rev 0. See #494.
func captureImportRev(ctx context.Context, qtx *storage.Queries, calendarID int64, uid string) (int64, error) {
	res, err := qtx.GetSyncResource(ctx, storage.GetSyncResourceParams{
		CalendarID: calendarID,
		Uid:        uid,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return res.Rev, nil
}

// inTx runs fn inside a single transaction. It commits on success and rolls
// back on any error. It is the atomicity boundary for persistImported. A
// failed Replace* part-way through a resource unwinds the whole resource.
// The local row then never reflects a partial server component.
func (e *Engine) inTx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := e.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

// importICal parses raw iCal data and persists it to the local database via
// persistImported. It is the shared accept-the-server-version path used both by
// auto-resolve (ConflictServerWins) and manual conflict resolution. The local
// row then reflects the server data instead of the divergent local copy.
//
// imported reports whether the payload carried at least one VEVENT, VTODO,
// or VJOURNAL component. ImportFile returns no error for empty or
// component-less input. Callers that must not accept the server version
// without an apply (for example a clear of the dirty flag) check imported.
// They then do not stamp the server ETag onto an unchanged local row in
// silence.
func (e *Engine) importICal(ctx context.Context, calendarID int64, data string) (imported bool, revs map[string]int64, warnings []ImportWarning, err error) {
	importResult, err := icalPkg.ImportFile(strings.NewReader(data))
	if err != nil {
		return false, nil, nil, fmt.Errorf("import ical: %w", err)
	}
	// Collect warnings before the tombstone filter below: dropping a
	// component must not change which component a warning is attributed to.
	warnings = e.noteImportWarnings("", importResult)
	// imported reflects whether the SERVER payload carried any component. It is
	// computed before tombstone filtering so the empty-iCal guard in callers
	// still fires for a genuinely empty server version, and never falsely fires
	// just because the only component was tombstoned away.
	imported = len(importResult.Events) > 0 || len(importResult.Todos) > 0 || len(importResult.Journals) > 0

	// Tombstone-aware import: drop any UID the user has locally deleted
	// (tombstoned, pending propagation to the server). UpsertByUID clears
	// deleted_at, so persisting a tombstoned UID would resurrect a row the user
	// just deleted. The pull path filters tombstoned UIDs inline (see the NOTE
	// in db/queries/events.sql); doing the same here keeps the accept-server
	// conflict paths — manual `sync resolve <id> server` and auto
	// ConflictServerWins — consistent with it. Issue #89 gap #2.
	importResult, err = e.dropTombstonedFromImport(ctx, calendarID, importResult)
	if err != nil {
		return false, nil, nil, err
	}
	revs, alarmWarnings, err := e.persistImported(ctx, calendarID, importResult)
	if err != nil {
		return false, nil, nil, err
	}
	warnings = append(warnings, e.notePersistWarnings("", "", alarmWarnings)...)
	if afterImportPersist != nil {
		afterImportPersist()
	}
	return imported, revs, warnings, nil
}

// dropTombstonedFromImport removes events/todos/journals whose UID is
// tombstoned for the calendar, so an accept-server import never resurrects a
// row the user has locally deleted. Returns the result unchanged when nothing
// is tombstoned.
func (e *Engine) dropTombstonedFromImport(ctx context.Context, calendarID int64, result icalPkg.ImportResult) (icalPkg.ImportResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return result, fmt.Errorf("list tombstones: %w", err)
	}
	if len(tombstones) == 0 {
		return result, nil
	}
	tombstoned := make(map[string]bool, len(tombstones))
	for _, ts := range tombstones {
		if ts.Uid != "" {
			tombstoned[ts.Uid] = true
		}
	}

	result.Events = filterTombstoned(e.logger, result.Events, tombstoned, ownerTypeEvent, func(ev event.Event) string { return ev.UID })
	result.Todos = filterTombstoned(e.logger, result.Todos, tombstoned, ownerTypeTodo, func(t todo.Todo) string { return t.UID })
	result.Journals = filterTombstoned(e.logger, result.Journals, tombstoned, ownerTypeJournal, func(j journal.Journal) string { return j.UID })

	return result, nil
}

// filterTombstoned returns items whose UID (via uidOf) is not tombstoned.
// It logs each one it drops. The result reuses a zero-capacity head of the
// input so append always allocates fresh and never clobbers the caller's slice.
func filterTombstoned[T any](logger *slog.Logger, items []T, tombstoned map[string]bool, ownerType string, uidOf func(T) string) []T {
	kept := items[:0:0]
	for _, it := range items {
		if uid := uidOf(it); tombstoned[uid] {
			logger.Info("skip accept-server import: UID tombstoned locally", "uid", uid, "owner_type", ownerType)
			continue
		}
		kept = append(kept, it)
	}
	return kept
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
