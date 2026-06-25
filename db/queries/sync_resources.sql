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
UPDATE sync_resources SET dirty = 1 WHERE calendar_id = ? AND uid = ?;

-- name: MarkSyncResourceDirtyWithEtag :exec
UPDATE sync_resources SET dirty = 1, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: ClearSyncResourceDirty :exec
UPDATE sync_resources SET dirty = 0, etag = ? WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResource :exec
DELETE FROM sync_resources WHERE calendar_id = ? AND uid = ?;

-- name: DeleteSyncResourcesByCalendar :exec
DELETE FROM sync_resources WHERE calendar_id = ?;

-- name: CreateTombstone :exec
INSERT INTO tombstones (calendar_id, uid, remote_url) VALUES (?, ?, ?);

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
