package sync

import (
	"bytes"
	"context"
	"fmt"
	"strings"

	"github.com/emersion/go-ical"

	"github.com/douglasdemoura/chroncal/internal/caldav"
	icalPkg "github.com/douglasdemoura/chroncal/internal/ical"
	"github.com/douglasdemoura/chroncal/internal/storage"
)

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
			importResult, err := icalPkg.ImportFileRemote(strings.NewReader(buf.String()))
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
			if e.hasOpenConflict(ctx, calendarID, uid) {
				e.logger.Debug("skip pull: open conflict pending resolution", "uid", uid)
				// The fetched body is newer than the recorded one. Record it so
				// a later resolve picks current server data. The sync-token may
				// then advance: the row, not the token, carries the obligation.
				e.refreshConflictServerBody(ctx, calendarID, uid, buf.String(), res.ETag)
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
