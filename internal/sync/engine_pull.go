package sync

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

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

		importResult, err := icalPkg.ImportFileRemote(strings.NewReader(buf.String()))
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
		if e.hasOpenConflict(ctx, calendarID, uid) {
			e.logger.Debug("skip pull: open conflict pending resolution", "uid", uid)
			// The fetched body is newer than the recorded one. Record it so
			// a later resolve picks current server data.
			e.refreshConflictServerBody(ctx, calendarID, uid, buf.String(), res.ETag)
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
