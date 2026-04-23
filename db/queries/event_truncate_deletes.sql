-- name: RecordEventTruncateDelete :exec
-- Idempotent: truncating the same cutoff twice keeps one log row. Stores
-- only the FIRST previous_rrule seen so re-truncating doesn't overwrite
-- the original pre-truncation state with an already-truncated rule.
INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule)
VALUES (?, ?, ?, ?)
ON CONFLICT(uid, cutoff_time) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: ListEventTruncateDeletesByCalendar :many
SELECT * FROM event_truncate_deletes
WHERE calendar_id = ?
ORDER BY deleted_at DESC;

-- name: GetEventTruncateDelete :one
SELECT * FROM event_truncate_deletes WHERE id = ?;

-- name: DeleteEventTruncateDelete :exec
DELETE FROM event_truncate_deletes WHERE id = ?;

-- name: PurgeOldEventTruncateDeletes :execrows
DELETE FROM event_truncate_deletes WHERE deleted_at < ?;
