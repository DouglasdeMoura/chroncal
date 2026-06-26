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

	authpkg "github.com/douglasdemoura/chroncal/internal/auth"
	"github.com/douglasdemoura/chroncal/internal/caldav"
	"github.com/douglasdemoura/chroncal/internal/calendar"
	"github.com/douglasdemoura/chroncal/internal/event"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/journal"
	"github.com/douglasdemoura/chroncal/internal/storage"
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

// pushLockKey identifies a per-calendar push lock. It is keyed by the shared
// database handle (not the Engine) because each sync operation builds a fresh
// Engine over the app's shared *sql.DB — the TUI's save-time PushCalendar and a
// periodic SyncCalendar run on different Engine instances but the same DB (see
// internal/tui/app.go newSyncService). An Engine-scoped lock would not
// serialize them; a registry keyed by (db, calendar) does. The db pointer keeps
// independent databases (e.g. parallel tests) from sharing a lock.
type pushLockKey struct {
	db         *sql.DB
	calendarID int64
}

var (
	pushLocksMu gosync.Mutex
	pushLocks   = map[pushLockKey]*gosync.Mutex{}
)

// pushLock returns the per-calendar mutex that serializes the push phase for
// calendarID, creating it on first use. Concurrent push runs for the same
// calendar — e.g. an opportunistic save-time PushCalendar racing a periodic
// SyncCalendar — must not both read the same dirty, never-pushed sync_resource
// (RemoteUrl=""): each would mint a distinct random href and PUT it without an
// If-Match precondition, so the server would end up with two objects for one
// UID. CalDAV servers key dedup on href, not UID, so an If-None-Match:* guard
// would not catch this (the two hrefs differ). Serializing the phase lets the
// first run record the remote_url and clear the dirty flag before the second
// reads it. This guards only same-process concurrency; two CLI processes
// pushing the same calendar at once can still race. See issue #225.
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

// NewEngine creates a new sync engine.
func NewEngine(db *sql.DB, q *storage.Queries, credStore authpkg.CredentialStore, calendars *calendar.Service, events *event.Service, todos *todo.Service, journals *journal.Service, logger *slog.Logger) *Engine {
	if logger == nil {
		logger = slog.Default()
	}
	return &Engine{db: db, q: q, credStore: credStore, calendars: calendars, events: events, todos: todos, journals: journals, logger: logger}
}

// loadCalendarClient loads the calendar, its account, and a ready CalDAV client.
// Returns the calendar row and the remote calendar URL alongside the client.
func (e *Engine) loadCalendarClient(ctx context.Context, calendarID int64) (storage.Calendar, *caldav.Client, string, error) {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return storage.Calendar{}, nil, "", fmt.Errorf("get calendar: %w", err)
	}
	if cal.AccountID == nil || *cal.AccountID == 0 {
		return cal, nil, "", fmt.Errorf("calendar %d is not linked to an account", calendarID)
	}
	account, err := e.q.GetAccount(ctx, *cal.AccountID)
	if err != nil {
		return cal, nil, "", fmt.Errorf("get account: %w", err)
	}
	cred, err := e.credStore.Get(account.ID)
	if err != nil {
		return cal, nil, "", fmt.Errorf("get credentials: %w", err)
	}
	client, err := caldav.NewClientFromCredential(account.ServerUrl, cred, func(updated authpkg.Credential) error {
		return e.credStore.Set(updated)
	})
	if err != nil {
		return cal, nil, "", fmt.Errorf("create client: %w", err)
	}
	remoteURL := storage.NullableToString(cal.RemoteUrl)
	if remoteURL == "" {
		return cal, nil, "", fmt.Errorf("calendar %d has no remote URL", calendarID)
	}
	return cal, client, remoteURL, nil
}

// SyncCalendar runs a full sync cycle for one calendar.
func (e *Engine) SyncCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (result *SyncResult, err error) {
	cal, client, remoteURL, err := e.loadCalendarClient(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	result = &SyncResult{CalendarID: cal.ID}
	attemptedAt := time.Now().UTC().Format(time.RFC3339)
	defer func() {
		if updateErr := e.updateSyncHealth(ctx, cal.ID, attemptedAt, result, err); updateErr != nil {
			e.logger.Warn("update sync health failed", "calendar_id", cal.ID, "error", updateErr)
			result.Errors = append(result.Errors, fmt.Errorf("update sync health: %w", updateErr))
		}
	}()

	e.logger.Info("sync started", "calendar_id", calendarID, "remote_url", remoteURL)

	// Phase 0: Sync calendar metadata
	if err := e.syncCalendarMetadata(ctx, client, calendarID, remoteURL); err != nil {
		e.logger.Warn("calendar metadata sync failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("calendar metadata: %w", err))
	}

	// Phase 1: Push dirty resources
	pushResult, err := e.push(ctx, client, calendarID, remoteURL, strategy)
	if err != nil {
		e.logger.Error("push failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("push: %w", err))
	} else {
		result.Pushed = pushResult.pushed
		result.Conflicts = pushResult.conflicts
		result.Errors = append(result.Errors, pushResult.errors...)
	}

	// Phase 2: Pull changes from server
	pullResult, err := e.pull(ctx, client, calendarID, remoteURL)
	if err != nil {
		e.logger.Error("pull failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("pull: %w", err))
	} else {
		result.Pulled = pullResult.pulled
		result.Deleted = pullResult.deleted
	}

	// Phase 3: Process tombstones
	tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
	if err != nil {
		e.logger.Warn("tombstone processing failed", "calendar_id", calendarID, "error", err)
		result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
	} else {
		result.Deleted += tombstoneResult.deleted
		result.Errors = append(result.Errors, tombstoneResult.errors...)
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

// PushCalendar runs only the push + tombstone phases for one calendar.
// It is the write-only fast path used for opportunistic save-time sync:
// local mutations are flushed upstream without pulling or rewriting
// calendar metadata. Dirty resources that fail to push stay dirty, so the
// next full SyncCalendar will retry them. Safe to call concurrently with
// a full sync: the push phase holds a per-calendar lock (see pushLock), so
// two runs cannot both create a server object for the same never-pushed,
// etag-less resource. Existing resources are still arbitrated by the server
// via ETag preconditions.
func (e *Engine) PushCalendar(ctx context.Context, calendarID int64, strategy ConflictStrategy) (*SyncResult, error) {
	_, client, remoteURL, err := e.loadCalendarClient(ctx, calendarID)
	if err != nil {
		return nil, err
	}
	result := &SyncResult{CalendarID: calendarID}

	pushResult, err := e.push(ctx, client, calendarID, remoteURL, strategy)
	if err != nil {
		return result, fmt.Errorf("push: %w", err)
	}
	result.Pushed = pushResult.pushed
	result.Conflicts = pushResult.conflicts
	result.Errors = append(result.Errors, pushResult.errors...)

	tombstoneResult, err := e.processTombstones(ctx, client, calendarID, remoteURL)
	if err != nil {
		result.Errors = append(result.Errors, fmt.Errorf("tombstones: %w", err))
	} else {
		result.Deleted = tombstoneResult.deleted
		result.Errors = append(result.Errors, tombstoneResult.errors...)
	}
	return result, nil
}

// SyncAll syncs all connected calendars.
func (e *Engine) SyncAll(ctx context.Context, strategy ConflictStrategy) ([]*SyncResult, error) {
	cals, err := e.q.ListCalendars(ctx)
	if err != nil {
		return nil, fmt.Errorf("list calendars: %w", err)
	}

	var results []*SyncResult
	for _, cal := range cals {
		if cal.AccountID == nil || *cal.AccountID == 0 {
			continue
		}

		result, err := e.SyncCalendar(ctx, cal.ID, strategy)
		if err != nil {
			e.logger.Error("sync calendar failed", "calendar_id", cal.ID, "error", err)
			results = append(results, &SyncResult{
				CalendarID: cal.ID,
				Errors:     []error{err},
			})
			continue
		}
		results = append(results, result)
	}
	return results, nil
}

type pushResult struct {
	pushed    int
	conflicts int
	errors    []error
}

func (e *Engine) push(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, strategy ConflictStrategy) (*pushResult, error) {
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

	pushIdentity := e.resolvePushIdentity(ctx, calendarID)

	result := &pushResult{}
	for _, res := range dirty {
		// CalDAV's PUT contract (RFC 4791 §4.1) only lets the organizer
		// modify a meeting resource. Attendees are supposed to round-trip
		// RSVP changes via iTIP REPLY, not PUT — Google rejects attendee
		// PUTs with HTTP 400 / 500 and a vague <D:error/> body. Skipping
		// foreign-organized events here clears the dirty flag so we stop
		// retrying every sync; the local row is left untouched.
		if pushIdentity != "" && res.OwnerType == "event" && !e.userOrganizesEvent(ctx, res.Uid, pushIdentity) {
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
			result.errors = append(result.errors, fmt.Errorf("export %s: %w", res.Uid, err))
			continue
		}

		// Parse the iCal data for PUT
		cal, parseErr := parseICalData(icalData)
		if parseErr != nil {
			result.errors = append(result.errors, fmt.Errorf("parse ical for %s: %w", res.Uid, parseErr))
			continue
		}

		// Determine PUT path
		var putPath string
		if res.RemoteUrl != "" {
			putPath, err = client.CanonicalObjectRef(remoteURL, res.RemoteUrl)
			if err != nil {
				result.errors = append(result.errors, fmt.Errorf("validate remote href for %s: %w", res.Uid, err))
				continue
			}
		} else {
			putPath, err = client.CanonicalObjectRef(remoteURL, buildRemoteResourcePath(remoteURL, res.Uid))
			if err != nil {
				result.errors = append(result.errors, fmt.Errorf("build remote href for %s: %w", res.Uid, err))
				continue
			}
		}

		// PUT to server
		newEtag, putErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
			return client.PutResource(ctx, putPath, cal, res.Etag)
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
						if _, err := e.importICal(ctx, calendarID, buf.String()); err != nil {
							e.logger.Error("import server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("import server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						// Clear dirty and update ETag to accept server version
						if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
							CalendarID: calendarID,
							Uid:        res.Uid,
							Etag:       serverRes.ETag,
						}); err != nil {
							e.logger.Error("clear dirty after conflict", "uid", res.Uid, "error", err)
						}
					}
				} else {
					// ConflictPrompt: record conflict for manual resolution
					localIcal, exportErr := e.exportResource(ctx, res.OwnerType, res.Uid)
					if exportErr != nil {
						e.logger.Warn("export local resource for conflict record", "uid", res.Uid, "error", exportErr)
					}
					serverRes, fetchErr := client.GetResource(ctx, putPath)
					if fetchErr == nil {
						serverIcal, encodeErr := caldav.EncodeCalendar(serverRes.Data)
						if encodeErr != nil {
							e.logger.Warn("encode server resource for conflict record", "uid", res.Uid, "error", encodeErr)
						}
						ownerID := e.lookupOwnerID(ctx, res.OwnerType, res.Uid)
						_ = e.q.CreateSyncConflict(ctx, storage.CreateSyncConflictParams{
							CalendarID: calendarID,
							OwnerType:  res.OwnerType,
							OwnerID:    ownerID,
							Uid:        res.Uid,
							LocalIcal:  string(localIcal),
							ServerIcal: string(serverIcal),
							ServerEtag: serverRes.ETag,
						})
					}
				}
				result.conflicts++
				continue
			}
			e.logger.Error("PUT failed", "uid", res.Uid, "error", putErr)
			result.errors = append(result.errors, fmt.Errorf("put %s: %w", res.Uid, putErr))
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

type pullResult struct {
	pulled  int
	deleted int
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
// pages carry ~90 changes; 200 pages is far beyond any real calendar and
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
		if persistErr := e.persistImported(ctx, calendarID, importResult); persistErr != nil {
			e.logger.Error("persist imported resource", "uid", uid, "path", res.Path, "error", persistErr)
			continue
		}

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
		// clause preserves it. Force dirty=0 — the server's version is
		// now authoritative. See applySyncCollection for the full note.
		if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
			CalendarID: calendarID,
			Uid:        uid,
			Etag:       res.ETag,
		}); err != nil {
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
	result.deleted += deletions.apply(ctx, e, calendarID)

	return result, nil
}

// multigetBatchSize bounds how many hrefs go into a single calendar-multiget.
// Servers (notably Google) reject very large multigets; 50 is the conservative
// number used by other clients and keeps response sizes manageable.
const multigetBatchSize = 50

// pendingDeletions is the single chokepoint for the sync engine's most
// dangerous operation: removing local rows because the server no longer has
// them. Three production data-loss incidents — multiget 404s, href rewrites,
// and RFC 6578 §3.6 truncation — all share one root cause: a local row was
// deleted because it was ABSENT from a remote view that turned out to be
// incomplete. The two recorders below encode the only safe rule. Explicit
// deletions carry positive evidence (the server returned 404 for a specific
// href) and are always sound; absence-inferred deletions require a provably
// complete inventory and are withheld otherwise. Every deletion the pull
// performs goes through apply(), so a new "this looks deleted" code path
// cannot reach the executor without choosing one of these two doors.
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

// inferFromAbsence records a deletion for every local resource missing from
// the remote inventory (`seen`) — but ONLY when complete is true. When the
// inventory is partial it withholds the entire sweep (logging the count and
// reason) so a partial view can never drive deletions; the rows are
// re-evaluated on the next clean sync. complete MUST be computed by the
// caller as "every resource the server has was observed this pull." Local
// rows with no remote_url are skipped — they were never pushed.
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

// apply executes the accumulated deletions: soft-delete each local owner row
// and drop its sync_resource. Returns the count actually deleted.
func (p *pendingDeletions) apply(ctx context.Context, e *Engine, calendarID int64) int {
	deleted := 0
	for uid, ownerType := range p.owner {
		if err := e.deleteLocalResourceByUID(ctx, ownerType, uid); err != nil {
			e.logger.Error("delete local resource", "uid", uid, "owner_type", ownerType, "error", err)
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
	return deleted
}

// absenceWithholdReason describes why an absence sweep was (or wasn't) safe,
// for the withhold log line.
func absenceWithholdReason(truncated bool, multigetMisses, persistFailures int) string {
	var parts []string
	if truncated {
		parts = append(parts, "response truncated (RFC 6578 §3.6)")
	}
	if multigetMisses > 0 {
		parts = append(parts, fmt.Sprintf("%d multiget miss(es)", multigetMisses))
	}
	if persistFailures > 0 {
		parts = append(parts, fmt.Sprintf("%d persist failure(s)", persistFailures))
	}
	if len(parts) == 0 {
		return "complete"
	}
	return strings.Join(parts, " and ")
}

// applySyncCollection consumes the change list from a sync-collection REPORT,
// fetches bodies for changed resources via calendar-multiget, persists them,
// applies deletions, and stores the new sync-token. This is the fast path
// for steady-state syncs against RFC 6578-capable servers.
func (e *Engine) applySyncCollection(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, cal storage.Calendar, syncResult *caldav.SyncCollectionResult, initialSnapshot bool) (*pullResult, error) {
	result := &pullResult{}
	multigetMisses := 0
	persistFailures := 0

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
		// Per-resource 404s here are NOT treated as deletions. Some servers
		// (Google) hand back hrefs in sync-collection that 404 on multiget
		// for reasons that aren't actual deletions — race timing, path
		// encoding quirks, or server-side glitches. Soft-deleting on a 404
		// alone caused real data loss in production. Just log and skip;
		// the local row and sync_resource keep their previous etag, so the
		// next sync_collection call will list the path again and we get
		// another chance to fetch its body. A real server-side deletion
		// arrives as a sync-collection 404 on the response, not a multiget
		// 404, and is handled by the deletedPaths flow above.
		for _, miss := range multi.Missing {
			multigetMisses++
			e.logger.Warn("multiget href missing, skipping (will retry next sync)", "calendar_id", calendarID, "href", miss)
			// Treat the missing path's UID as "still seen" so the initial
			// snapshot deletion loop below doesn't conclude the resource
			// is gone from the server. Otherwise an empty token + a
			// transient multiget 404 would soft-delete the local event
			// even though we have no actual evidence of deletion.
			canonical, hrefErr := client.CanonicalObjectRef(remoteURL, miss)
			if hrefErr != nil {
				continue
			}
			if local, exists := localByPath[canonical]; exists {
				seenUIDs[local.Uid] = true
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
			uid := extractUID(importResult)
			if uid == "" {
				e.logger.Warn("no UID in fetched resource", "path", res.Path)
				continue
			}
			seenUIDs[uid] = true
			if tombstonedUIDs[uid] {
				continue
			}
			ownerType := detectOwnerType(importResult)
			if persistErr := e.persistImported(ctx, calendarID, importResult); persistErr != nil {
				// A changed body we fetched but couldn't store (transient
				// SQLite busy/lock, or a malformed component a Replace*
				// rejects). Leave the sync_resource on its old etag and count
				// the failure so the inventory is treated as incomplete: the
				// token is withheld and the next REPORT re-lists this change
				// for another attempt. Advancing the token here would skip the
				// change permanently until the server touches it again.
				persistFailures++
				e.logger.Error("persist imported resource", "uid", uid, "path", res.Path, "error", persistErr)
				continue
			}
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
			// push round-trips it back to the server. Force dirty=0 since the
			// server's version is now authoritative locally.
			if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
				CalendarID: calendarID,
				Uid:        uid,
				Etag:       res.ETag,
			}); err != nil {
				e.logger.Warn("clear post-import dirty", "uid", uid, "error", err)
			}
			result.pulled++
		}
	}

	// All deletions funnel through one chokepoint (see pendingDeletions).
	// The inventory is "complete" only when nothing limited our view of the
	// server's resources: the response wasn't truncated, every changed body
	// was fetched, and every fetched body persisted. A persist failure leaves
	// the local copy behind the server, so the token must be withheld too.
	inventoryComplete := !syncResult.Truncated && multigetMisses == 0 && persistFailures == 0
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
			inventoryComplete, absenceWithholdReason(syncResult.Truncated, multigetMisses, persistFailures))
	}

	result.deleted += deletions.apply(ctx, e, calendarID)

	if syncResult.SyncToken != "" && inventoryComplete {
		token := syncResult.SyncToken
		if err := e.q.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
			ID:        calendarID,
			Ctag:      cal.Ctag,
			SyncToken: &token,
		}); err != nil {
			e.logger.Warn("update sync token", "calendar_id", calendarID, "error", err)
		}
	} else if multigetMisses > 0 || persistFailures > 0 {
		// Pull made partial progress: some hrefs the server reported in
		// sync-collection 404'd on multiget, or a fetched body failed to
		// persist locally. We don't know if the 404s are real deletions or
		// transient errors, so we don't soft-delete them; and the persist
		// failures left those resources behind the server. We don't advance
		// the sync-token, so the next sync re-lists the same change set and
		// gets another shot at fetching and storing the bodies. Slow but safe.
		e.logger.Warn("not advancing sync-token: incomplete pull",
			"calendar_id", calendarID, "multiget_misses", multigetMisses,
			"persist_failures", persistFailures)
	}

	return result, nil
}

func (e *Engine) deleteLocalResourceByUID(ctx context.Context, ownerType, uid string) error {
	// Soft-delete across every owner type so a remote DELETE that races with
	// a user action doesn't nuke the local row — it stays in trash until the
	// retention window expires. The caller clears the sync_resource so a
	// later restore re-CREATEs a fresh one via MarkResourceDirty.
	switch ownerType {
	case "event":
		return e.q.SoftDeleteEventsByUID(ctx, uid)
	case "todo":
		return e.q.SoftDeleteTodosByUID(ctx, uid)
	case "journal":
		return e.q.SoftDeleteJournalsByUID(ctx, uid)
	default:
		return fmt.Errorf("unsupported owner type %q", ownerType)
	}
}

func (e *Engine) lookupOwnerID(ctx context.Context, ownerType, uid string) int64 {
	switch ownerType {
	case "event":
		row, err := e.q.GetEventByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	case "todo":
		row, err := e.q.GetTodoByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	case "journal":
		row, err := e.q.GetJournalByUID(ctx, uid)
		if err == nil {
			return row.ID
		}
	}
	return 0
}

type tombstoneResult struct {
	deleted int
	errors  []error
}

func (e *Engine) processTombstones(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string) (*tombstoneResult, error) {
	tombstones, err := e.q.ListTombstonesByCalendar(ctx, calendarID)
	if err != nil {
		return nil, fmt.Errorf("list tombstones: %w", err)
	}

	result := &tombstoneResult{}
	for _, ts := range tombstones {
		deletePath, hrefErr := client.CanonicalObjectRef(remoteURL, ts.RemoteUrl)
		if hrefErr != nil {
			result.errors = append(result.errors, fmt.Errorf("validate tombstone %s: %w", ts.Uid, hrefErr))
			continue
		}
		e.logger.Debug("deleting tombstone", "uid", ts.Uid, "remote_url", deletePath)
		if _, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (struct{}, error) {
			return struct{}{}, client.DeleteResource(ctx, deletePath)
		}); err != nil && !errors.Is(err, caldav.ErrResourceGone) {
			// A 404/410 means the resource is already absent server-side —
			// the desired end state — so fall through and clear the local
			// rows instead of re-issuing the DELETE on every sync.
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

	remoteColor, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (string, error) {
		return client.GetCalendarColor(ctx, remoteURL)
	})
	if err != nil {
		return fmt.Errorf("get remote calendar color: %w", err)
	}

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

	if remoteColor != storage.NullableToString(cal.RemoteColor) {
		if err := e.calendars.UpdateColorFromSync(ctx, calendarID, remoteColor, remoteColor); err != nil {
			return fmt.Errorf("update calendar color from sync: %w", err)
		}
	}

	return nil
}

// errResourceMissing reports that no live local row exists for a UID we were
// asked to export. Push uses errors.Is on this to distinguish a missing local
// row (clear the dirty flag, stop retrying) from a real export failure.
var errResourceMissing = errors.New("local resource missing for uid")

// exportResource exports a local resource to iCal bytes. CalDAV tracks one
// resource per UID; recurring resources are stored as a master row plus
// override rows sharing the UID. Normally we bundle master + overrides so
// instance edits round-trip to the server. Google sometimes serves a single
// orphan instance under a UID like `<master>_R<recurrence-id>@google.com`
// with a RECURRENCE-ID property and no master in the same iCal stream — we
// import those as override rows with no master sibling, so the master lookup
// fails. Fall back to listing every live row under the UID and exporting the
// non-empty result; only return errResourceMissing when nothing remains.
func (e *Engine) exportResource(ctx context.Context, ownerType string, uid string) ([]byte, error) {
	switch ownerType {
	case "event":
		var rows []event.Event
		if evt, err := e.events.GetByUID(ctx, uid); err == nil {
			rows = append(rows, evt)
		}
		overrides, _ := e.events.ListOverridesByUID(ctx, uid)
		rows = append(rows, overrides...)
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: event uid %s", errResourceMissing, uid)
		}
		for i := range rows {
			hydrateEvent(ctx, e, &rows[i])
		}
		return icalPkg.ExportEvents(rows, "")
	case "todo":
		var rows []todo.Todo
		if t, err := e.todos.GetByUID(ctx, uid); err == nil {
			rows = append(rows, t)
		}
		overrides, _ := e.todos.ListOverridesByUID(ctx, uid)
		rows = append(rows, overrides...)
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: todo uid %s", errResourceMissing, uid)
		}
		for i := range rows {
			hydrateTodo(ctx, e, &rows[i])
		}
		return icalPkg.ExportTodos(rows, "")
	case "journal":
		var rows []journal.Journal
		if j, err := e.journals.GetByUID(ctx, uid); err == nil {
			rows = append(rows, j)
		}
		overrides, _ := e.journals.ListOverridesByUID(ctx, uid)
		rows = append(rows, overrides...)
		if len(rows) == 0 {
			return nil, fmt.Errorf("%w: journal uid %s", errResourceMissing, uid)
		}
		for i := range rows {
			hydrateJournal(ctx, e, &rows[i])
		}
		return icalPkg.ExportJournals(rows, "")
	default:
		return nil, fmt.Errorf("unknown owner type: %s", ownerType)
	}
}

func hydrateEvent(ctx context.Context, e *Engine, evt *event.Event) {
	evt.Alarms, _ = e.events.ListAlarms(ctx, evt.ID)
	evt.Attendees, _ = e.events.ListAttendees(ctx, evt.ID)
	evt.Attachments, _ = e.events.ListAttachments(ctx, evt.ID)
	evt.Comments, _ = e.events.ListComments(ctx, evt.ID)
	evt.Contacts, _ = e.events.ListContacts(ctx, evt.ID)
	evt.Resources, _ = e.events.ListResources(ctx, evt.ID)
	evt.Relations, _ = e.events.ListRelations(ctx, evt.ID)
	evt.XProperties, _ = e.events.ListXProperties(ctx, evt.ID)
}

func hydrateTodo(ctx context.Context, e *Engine, t *todo.Todo) {
	t.Alarms, _ = e.todos.ListAlarms(ctx, t.ID)
	t.Attendees, _ = e.todos.ListAttendees(ctx, t.ID)
	t.Attachments, _ = e.todos.ListAttachments(ctx, t.ID)
	t.Comments, _ = e.todos.ListComments(ctx, t.ID)
	t.Contacts, _ = e.todos.ListContacts(ctx, t.ID)
	t.Resources, _ = e.todos.ListResources(ctx, t.ID)
	t.Relations, _ = e.todos.ListRelations(ctx, t.ID)
	t.XProperties, _ = e.todos.ListXProperties(ctx, t.ID)
}

func hydrateJournal(ctx context.Context, e *Engine, j *journal.Journal) {
	j.Attendees, _ = e.journals.ListAttendees(ctx, j.ID)
	j.Attachments, _ = e.journals.ListAttachments(ctx, j.ID)
	j.Comments, _ = e.journals.ListComments(ctx, j.ID)
	j.Contacts, _ = e.journals.ListContacts(ctx, j.ID)
	j.Relations, _ = e.journals.ListRelations(ctx, j.ID)
	j.XProperties, _ = e.journals.ListXProperties(ctx, j.ID)
}

// persistImported saves parsed iCal data to the local database using the same
// upsert pattern as the CLI import command.
func (e *Engine) persistImported(ctx context.Context, calendarID int64, result icalPkg.ImportResult) error {
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
			if err := events.ReplaceAlarms(ctx, saved.ID, ev.Alarms); err != nil {
				return fmt.Errorf("replace alarms for event %q: %w", ev.UID, err)
			}
			if err := events.ReplaceAttendees(ctx, saved.ID, ev.Attendees); err != nil {
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
			return nil
		}); err != nil {
			return err
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
			if err := todos.ReplaceAlarms(ctx, saved.ID, t.Alarms); err != nil {
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
			return nil
		}); err != nil {
			return err
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
			return nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// inTx runs fn inside a single transaction, committing on success and rolling
// back on any error. It is the atomicity boundary for persistImported: a failed
// Replace* part-way through a resource unwinds the whole resource so the local
// row never reflects a partial server component.
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
// auto-resolve (ConflictServerWins) and manual conflict resolution, so the local
// row actually reflects the server data instead of the divergent local copy.
//
// imported reports whether the payload carried at least one VEVENT/VTODO/
// VJOURNAL component. ImportFile returns no error for empty or component-less
// input, so callers that must not accept the server version without applying it
// (e.g. clearing the dirty flag) check imported to avoid silently stamping the
// server ETag onto an unchanged local row.
func (e *Engine) importICal(ctx context.Context, calendarID int64, data string) (imported bool, err error) {
	importResult, err := icalPkg.ImportFile(strings.NewReader(data))
	if err != nil {
		return false, fmt.Errorf("import ical: %w", err)
	}
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
		return false, err
	}
	if err := e.persistImported(ctx, calendarID, importResult); err != nil {
		return false, err
	}
	return imported, nil
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

	result.Events = filterTombstoned(e.logger, result.Events, tombstoned, "event", func(ev event.Event) string { return ev.UID })
	result.Todos = filterTombstoned(e.logger, result.Todos, tombstoned, "todo", func(t todo.Todo) string { return t.UID })
	result.Journals = filterTombstoned(e.logger, result.Journals, tombstoned, "journal", func(j journal.Journal) string { return j.UID })

	return result, nil
}

// filterTombstoned returns items whose UID (via uidOf) is not tombstoned,
// logging each one it drops. The result reuses a zero-capacity head of the
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
		return "event"
	}
	if len(result.Todos) > 0 {
		return "todo"
	}
	if len(result.Journals) > 0 {
		return "journal"
	}
	return "event"
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
// PUT meeting resources. Prefers the calendar's stored owner_email and
// falls back to the linked account's username (which is the user's email
// for both basic-auth and OAuth providers we support). Returns empty when
// neither is known — the caller should then skip the organizer gate
// rather than guess.
func (e *Engine) resolvePushIdentity(ctx context.Context, calendarID int64) string {
	cal, err := e.q.GetCalendar(ctx, calendarID)
	if err != nil {
		return ""
	}
	if email := strings.TrimSpace(cal.OwnerEmail); email != "" {
		return email
	}
	if cal.AccountID != nil && *cal.AccountID != 0 {
		acc, err := e.q.GetAccount(ctx, *cal.AccountID)
		if err == nil {
			return acc.Username
		}
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
