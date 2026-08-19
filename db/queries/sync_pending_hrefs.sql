-- name: BumpSyncPendingHref :one
INSERT INTO sync_pending_hrefs (calendar_id, href)
VALUES (?, ?)
ON CONFLICT(calendar_id, href) DO UPDATE SET
    miss_count = sync_pending_hrefs.miss_count + 1
RETURNING *;

-- name: ListSyncPendingHrefsByCalendar :many
SELECT * FROM sync_pending_hrefs WHERE calendar_id = ? ORDER BY id;

-- name: DeleteSyncPendingHref :exec
DELETE FROM sync_pending_hrefs WHERE calendar_id = ? AND href = ?;

-- name: DeleteSyncPendingHrefsByCalendar :exec
DELETE FROM sync_pending_hrefs WHERE calendar_id = ?;
