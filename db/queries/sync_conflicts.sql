-- name: CreateSyncConflict :exec
INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    owner_type = excluded.owner_type,
    owner_id = excluded.owner_id,
    local_ical = excluded.local_ical,
    server_ical = excluded.server_ical,
    server_etag = excluded.server_etag,
    detected_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: CountOpenSyncConflicts :one
SELECT COUNT(*) FROM sync_conflicts WHERE calendar_id = ? AND uid = ?;

-- name: ListSyncConflicts :many
SELECT * FROM sync_conflicts ORDER BY detected_at DESC;

-- name: ListSyncConflictsByCalendar :many
SELECT * FROM sync_conflicts WHERE calendar_id = ? ORDER BY detected_at DESC;

-- name: GetSyncConflict :one
SELECT * FROM sync_conflicts WHERE id = ?;

-- name: DeleteSyncConflict :exec
DELETE FROM sync_conflicts WHERE id = ?;

-- name: DeleteSyncConflictsByCalendar :exec
DELETE FROM sync_conflicts WHERE calendar_id = ?;
