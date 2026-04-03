-- name: CreateSyncConflict :exec
INSERT INTO sync_conflicts (calendar_id, owner_type, owner_id, uid, local_ical, server_ical, server_etag)
VALUES (?, ?, ?, ?, ?, ?, ?);

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
