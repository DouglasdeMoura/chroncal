-- name: BumpSyncKnownMiss :one
-- An empty etag names no ETag, so the update keeps the known one.
INSERT INTO sync_known_misses (calendar_id, href, uid, reason, etag, first_miss_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, href) DO UPDATE SET
    uid = excluded.uid,
    reason = excluded.reason,
    etag = COALESCE(NULLIF(excluded.etag, ''), sync_known_misses.etag),
    miss_count = sync_known_misses.miss_count + 1
RETURNING *;

-- name: MarkSyncKnownMissGaveUp :exec
UPDATE sync_known_misses SET gave_up_at = ?
WHERE calendar_id = ? AND href = ? AND gave_up_at IS NULL;

-- name: ListSyncKnownMissesByCalendar :many
SELECT * FROM sync_known_misses WHERE calendar_id = ? ORDER BY id;

-- name: ListGaveUpSyncKnownMisses :many
SELECT * FROM sync_known_misses
WHERE gave_up_at IS NOT NULL
ORDER BY calendar_id, id;

-- name: DeleteSyncKnownMiss :exec
DELETE FROM sync_known_misses WHERE calendar_id = ? AND href = ?;

-- name: DeleteSyncKnownMissesByCalendar :exec
DELETE FROM sync_known_misses WHERE calendar_id = ?;
