-- name: BumpSyncKnownMiss :one
INSERT INTO sync_known_misses (calendar_id, href, uid, reason)
VALUES (?, ?, ?, ?)
ON CONFLICT(calendar_id, href) DO UPDATE SET
    uid = excluded.uid,
    reason = excluded.reason,
    miss_count = sync_known_misses.miss_count + 1
RETURNING *;

-- name: MarkSyncKnownMissGaveUp :exec
UPDATE sync_known_misses SET gave_up_at = ?
WHERE calendar_id = ? AND href = ? AND gave_up_at IS NULL;

-- name: ListSyncKnownMissesByCalendar :many
SELECT * FROM sync_known_misses WHERE calendar_id = ? ORDER BY id;

-- name: ListGaveUpSyncKnownMissesByCalendar :many
SELECT * FROM sync_known_misses
WHERE calendar_id = ? AND gave_up_at IS NOT NULL
ORDER BY id;

-- name: DeleteSyncKnownMiss :exec
DELETE FROM sync_known_misses WHERE calendar_id = ? AND href = ?;

-- name: DeleteSyncKnownMissesByCalendar :exec
DELETE FROM sync_known_misses WHERE calendar_id = ?;
