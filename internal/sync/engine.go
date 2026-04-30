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
// a full sync — both funnel through the same dirty/tombstone rows, and
// the server arbitrates via ETag preconditions.
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

		e.logger.Debug("pushing resource", "uid", res.Uid, "remote_url", res.RemoteUrl)

		// Export the local resource to iCal
		icalData, err := e.exportResource(ctx, res.OwnerType, res.Uid)
		if err != nil {
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
						importResult, err := icalPkg.ImportFile(strings.NewReader(buf.String()))
						if err != nil {
							e.logger.Error("import server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("import server resource %s: %w", res.Uid, err))
							result.conflicts++
							continue
						}
						if err := e.persistImported(ctx, calendarID, importResult); err != nil {
							e.logger.Error("persist server resource failed", "uid", res.Uid, "error", err)
							result.errors = append(result.errors, fmt.Errorf("persist server resource %s: %w", res.Uid, err))
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

		// Clear dirty flag and update ETag
		if err := e.q.ClearSyncResourceDirty(ctx, storage.ClearSyncResourceDirtyParams{
			CalendarID: calendarID,
			Uid:        res.Uid,
			Etag:       newEtag,
		}); err != nil {
			e.logger.Error("clear dirty failed", "uid", res.Uid, "error", err)
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
	syncResult, syncErr := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (*caldav.SyncCollectionResult, error) {
		return client.SyncCollection(ctx, remoteURL, storedToken)
	})
	if errors.Is(syncErr, caldav.ErrSyncTokenInvalid) && storedToken != "" {
		e.logger.Info("sync-token invalid, performing full resync", "calendar_id", calendarID)
		syncResult, syncErr = caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) (*caldav.SyncCollectionResult, error) {
			return client.SyncCollection(ctx, remoteURL, "")
		})
		storedToken = ""
	}
	if syncErr == nil {
		return e.applySyncCollection(ctx, client, calendarID, remoteURL, cal, syncResult, storedToken == "")
	}
	if !errors.Is(syncErr, caldav.ErrSyncCollectionUnsupported) {
		return nil, fmt.Errorf("sync-collection: %w", syncErr)
	}
	e.logger.Info("server lacks sync-collection support, falling back to QueryAll", "calendar_id", calendarID)
	return e.pullFullSnapshot(ctx, client, calendarID, remoteURL)
}

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

		result.pulled++
		e.logger.Debug("pulled resource", "uid", uid, "path", res.Path, "etag", res.ETag)
	}

	// Detect deletions by UID. Local rows with no remote_url have never been
	// pushed and must be left alone — they're still pending upload.
	for _, local := range localResources {
		if local.RemoteUrl == "" {
			continue
		}
		if remoteUIDs[local.Uid] {
			continue
		}
		e.logger.Debug("resource deleted on server", "uid", local.Uid, "remote_url", local.RemoteUrl)
		if err := e.deleteLocalResourceByUID(ctx, local.OwnerType, local.Uid); err != nil {
			e.logger.Error("delete local resource", "uid", local.Uid, "owner_type", local.OwnerType, "error", err)
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        local.Uid,
		}); err != nil {
			e.logger.Error("delete sync resource", "uid", local.Uid, "error", err)
		}
		result.deleted++
	}

	return result, nil
}

// multigetBatchSize bounds how many hrefs go into a single calendar-multiget.
// Servers (notably Google) reject very large multigets; 50 is the conservative
// number used by other clients and keeps response sizes manageable.
const multigetBatchSize = 50

// applySyncCollection consumes the change list from a sync-collection REPORT,
// fetches bodies for changed resources via calendar-multiget, persists them,
// applies deletions, and stores the new sync-token. This is the fast path
// for steady-state syncs against RFC 6578-capable servers.
func (e *Engine) applySyncCollection(ctx context.Context, client *caldav.Client, calendarID int64, remoteURL string, cal storage.Calendar, syncResult *caldav.SyncCollectionResult, initialSnapshot bool) (*pullResult, error) {
	result := &pullResult{}

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
		resources, err := caldav.Retry(ctx, syncRetryOptions, func(ctx context.Context) ([]caldav.Resource, error) {
			return client.GetResources(ctx, remoteURL, batch)
		})
		if err != nil {
			return nil, fmt.Errorf("multiget batch %d: %w", start, err)
		}
		for _, res := range resources {
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
			result.pulled++
		}
	}

	deletedUIDs := make(map[string]bool)
	for _, p := range deletedPaths {
		local, exists := localByPath[p]
		if !exists {
			continue
		}
		if seenUIDs[local.Uid] {
			// Server reported a 404 on the old path but the same UID showed
			// up at a new path within the same response — this is an href
			// rewrite (Cosmo/GMX), not a real deletion. The fetch loop
			// already upserted the new path; skip.
			continue
		}
		deletedUIDs[local.Uid] = true
	}

	// Initial-snapshot pulls (empty stored token) only see ADDITIONS in the
	// change list, never deletions. So any locally-tracked UID missing from
	// the snapshot is gone on the server. This also handles upgrades from
	// QueryAll-based deployments where calendars carry sync resources but
	// no sync-token yet.
	if initialSnapshot {
		for _, local := range localResources {
			if local.RemoteUrl == "" {
				continue
			}
			if seenUIDs[local.Uid] || deletedUIDs[local.Uid] {
				continue
			}
			deletedUIDs[local.Uid] = true
		}
	}

	for _, local := range localResources {
		if !deletedUIDs[local.Uid] {
			continue
		}
		if err := e.deleteLocalResourceByUID(ctx, local.OwnerType, local.Uid); err != nil {
			e.logger.Error("delete local resource", "uid", local.Uid, "owner_type", local.OwnerType, "error", err)
			continue
		}
		if err := e.q.DeleteSyncResource(ctx, storage.DeleteSyncResourceParams{
			CalendarID: calendarID,
			Uid:        local.Uid,
		}); err != nil {
			e.logger.Error("delete sync resource", "uid", local.Uid, "error", err)
		}
		result.deleted++
	}

	if syncResult.SyncToken != "" {
		token := syncResult.SyncToken
		if err := e.q.UpdateCalendarSyncState(ctx, storage.UpdateCalendarSyncStateParams{
			ID:        calendarID,
			Ctag:      cal.Ctag,
			SyncToken: &token,
		}); err != nil {
			e.logger.Warn("update sync token", "calendar_id", calendarID, "error", err)
		}
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
		}); err != nil {
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

// exportResource exports a local resource to iCal bytes. CalDAV tracks one
// resource per UID, but recurring resources are stored as a master row plus
// override rows sharing the UID. Export must bundle master + overrides so
// instance edits round-trip to the server.
func (e *Engine) exportResource(ctx context.Context, ownerType string, uid string) ([]byte, error) {
	switch ownerType {
	case "event":
		evt, err := e.events.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get event by uid %s: %w", uid, err)
		}
		hydrateEvent(ctx, e, &evt)
		overrides, _ := e.events.ListOverridesByUID(ctx, uid)
		for i := range overrides {
			hydrateEvent(ctx, e, &overrides[i])
		}
		return icalPkg.ExportEvents(append([]event.Event{evt}, overrides...), "")
	case "todo":
		t, err := e.todos.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get todo by uid %s: %w", uid, err)
		}
		hydrateTodo(ctx, e, &t)
		overrides, _ := e.todos.ListOverridesByUID(ctx, uid)
		for i := range overrides {
			hydrateTodo(ctx, e, &overrides[i])
		}
		return icalPkg.ExportTodos(append([]todo.Todo{t}, overrides...), "")
	case "journal":
		j, err := e.journals.GetByUID(ctx, uid)
		if err != nil {
			return nil, fmt.Errorf("get journal by uid %s: %w", uid, err)
		}
		hydrateJournal(ctx, e, &j)
		overrides, _ := e.journals.ListOverridesByUID(ctx, uid)
		for i := range overrides {
			hydrateJournal(ctx, e, &overrides[i])
		}
		return icalPkg.ExportJournals(append([]journal.Journal{j}, overrides...), "")
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

	// Import events
	for _, ev := range result.Events {
		saved, err := e.events.UpsertByUID(ctx, event.UpsertParams{
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
		if len(ev.Alarms) > 0 {
			_ = e.events.ReplaceAlarms(ctx, saved.ID, ev.Alarms)
		}
		if len(ev.Attendees) > 0 {
			_ = e.events.ReplaceAttendees(ctx, saved.ID, ev.Attendees)
		}
		if len(ev.Attachments) > 0 {
			_ = e.events.ReplaceAttachments(ctx, saved.ID, ev.Attachments)
		}
		if len(ev.Comments) > 0 {
			_ = e.events.ReplaceComments(ctx, saved.ID, ev.Comments)
		}
		if len(ev.Contacts) > 0 {
			_ = e.events.ReplaceContacts(ctx, saved.ID, ev.Contacts)
		}
		if len(ev.Resources) > 0 {
			_ = e.events.ReplaceResources(ctx, saved.ID, ev.Resources)
		}
		if len(ev.Relations) > 0 {
			_ = e.events.ReplaceRelations(ctx, saved.ID, ev.Relations)
		}
		if len(ev.XProperties) > 0 {
			_ = e.events.ReplaceXProperties(ctx, saved.ID, ev.XProperties)
		}
	}

	// Import todos
	for _, t := range result.Todos {
		saved, err := e.todos.UpsertByUID(ctx, todo.UpsertParams{
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
		if len(t.Alarms) > 0 {
			_ = e.todos.ReplaceAlarms(ctx, saved.ID, t.Alarms)
		}
		if len(t.Attendees) > 0 {
			_ = e.todos.ReplaceAttendees(ctx, saved.ID, t.Attendees)
		}
		if len(t.Attachments) > 0 {
			_ = e.todos.ReplaceAttachments(ctx, saved.ID, t.Attachments)
		}
		if len(t.Comments) > 0 {
			_ = e.todos.ReplaceComments(ctx, saved.ID, t.Comments)
		}
		if len(t.Contacts) > 0 {
			_ = e.todos.ReplaceContacts(ctx, saved.ID, t.Contacts)
		}
		if len(t.Resources) > 0 {
			_ = e.todos.ReplaceResources(ctx, saved.ID, t.Resources)
		}
		if len(t.Relations) > 0 {
			_ = e.todos.ReplaceRelations(ctx, saved.ID, t.Relations)
		}
		if len(t.XProperties) > 0 {
			_ = e.todos.ReplaceXProperties(ctx, saved.ID, t.XProperties)
		}
	}

	// Import journals
	for _, j := range result.Journals {
		saved, err := e.journals.UpsertByUID(ctx, journal.UpsertParams{
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
		if len(j.Attendees) > 0 {
			_ = e.journals.ReplaceAttendees(ctx, saved.ID, j.Attendees)
		}
		if len(j.Attachments) > 0 {
			_ = e.journals.ReplaceAttachments(ctx, saved.ID, j.Attachments)
		}
		if len(j.Comments) > 0 {
			_ = e.journals.ReplaceComments(ctx, saved.ID, j.Comments)
		}
		if len(j.Contacts) > 0 {
			_ = e.journals.ReplaceContacts(ctx, saved.ID, j.Contacts)
		}
		if len(j.Relations) > 0 {
			_ = e.journals.ReplaceRelations(ctx, saved.ID, j.Relations)
		}
		if len(j.XProperties) > 0 {
			_ = e.journals.ReplaceXProperties(ctx, saved.ID, j.XProperties)
		}
	}

	return nil
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
