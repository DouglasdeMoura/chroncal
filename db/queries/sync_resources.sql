-- name: UpsertSyncResource :exec
INSERT INTO sync_resources (calendar_id, uid, owner_type, remote_url, etag, dirty, sync_strategy)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    remote_url = excluded.remote_url,
    etag = excluded.etag,
    dirty = MAX(sync_resources.dirty, excluded.dirty),
    sync_strategy = excluded.sync_strategy;

-- name: GetSyncResource :one
SELECT * FROM sync_resources WHERE calendar_id = ? AND uid = ?;

-- name: ListSyncResourcesByCalendar :many
SELECT * FROM sync_resources WHERE calendar_id = ? ORDER BY id;

-- name: ListDirtySyncResources :many
SELECT * FROM sync_resources WHERE calendar_id = ? AND dirty = 1 ORDER BY id;

-- name: MarkSyncResourceDirty :exec
UPDATE sync_resources SET dirty = 1, rev = rev + 1 WHERE calendar_id = ? AND uid = ?;

-- name: MarkSyncResourceDirtyWithEtag :exec
UPDATE sync_resources SET dirty = 1, rev = rev + 1, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: ClearSyncResourceDirty :exec
UPDATE sync_resources SET dirty = 0, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: FinalizePushedResource :exec
-- Records the new server ETag after a successful PUT and optimistically clears
-- the dirty flag. The ETag always advances to the server's current version so
-- the next push's If-Match does not 412 against a change we just made. Dirty
-- is cleared only when rev still matches the value captured before the body was
-- exported and PUT: a local edit that landed during the PUT round-trip bumps
-- rev (via MarkSyncResourceDirty / MarkResourceDirty), so dirty stays 1 and the
-- edit survives to the next push instead of being silently dropped. See #92.
UPDATE sync_resources
SET etag = ?,
    dirty = CASE WHEN rev = ? THEN 0 ELSE dirty END
WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResource :exec
DELETE FROM sync_resources WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResourcesByCalendar :exec
DELETE FROM sync_resources WHERE calendar_id = ?;

-- name: DetachSyncResourcesByCalendar :exec
-- Preserve local resources when an account is removed, but erase every
-- server-specific identity so a later link cannot reuse stale hrefs or ETags.
-- Dirty=1 makes the retained local objects first-time creates on a new server.
UPDATE sync_resources
SET remote_url = '', etag = '', dirty = 1, rev = rev + 1
WHERE calendar_id = ?;

-- name: CreateTombstone :exec
INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    remote_url = excluded.remote_url,
    deleted_at = excluded.deleted_at;

-- name: ListTombstonesByCalendar :many
SELECT * FROM tombstones WHERE calendar_id = ? ORDER BY deleted_at;

-- name: DeleteTombstone :exec
DELETE FROM tombstones WHERE id = ?;

-- name: DeleteTombstonesByCalendar :exec
DELETE FROM tombstones WHERE calendar_id = ?;

-- name: DeleteTombstonesByCalendarAndUID :exec
DELETE FROM tombstones WHERE calendar_id = ? AND uid = ?;

-- name: DeleteStaleTombstones :exec
DELETE FROM tombstones WHERE deleted_at < strftime('%Y-%m-%dT%H:%M:%SZ', 'now', '-30 days');
