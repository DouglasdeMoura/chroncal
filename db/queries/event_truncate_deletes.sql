-- name: RecordEventTruncateDelete :exec
-- Idempotent: truncating the same cutoff twice keeps one log row. Stores
-- only the FIRST previous_rrule, hidden_overrides, and removed_rdates seen so
-- re-truncating doesn't overwrite the original pre-truncation state with an
-- already-truncated rule or an empty override/rdate set.
INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, cutoff_time) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: ListEventTruncateDeletesByCalendar :many
SELECT * FROM event_truncate_deletes
WHERE calendar_id = ?
ORDER BY deleted_at DESC;

-- name: GetEventTruncateDelete :one
SELECT * FROM event_truncate_deletes WHERE id = ?;

-- name: GetEventTruncateDeleteByUIDAndCutoff :one
-- Looks up the truncation log row by its unique (uid, cutoff_time) key so the
-- TUI undo path can reverse a "this and following" delete through the same
-- provenance-aware reversal the trash view uses.
SELECT * FROM event_truncate_deletes WHERE uid = ? AND cutoff_time = ?;

-- name: DeleteEventTruncateDelete :exec
DELETE FROM event_truncate_deletes WHERE id = ?;

-- name: PurgeOldEventTruncateDeletes :execrows
DELETE FROM event_truncate_deletes WHERE deleted_at < ?;
