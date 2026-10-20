-- name: RecordEventTruncateDelete :exec
-- Idempotent: a second truncation at the same cutoff in one calendar keeps
-- one log row. Each calendar keeps its own row (issue #842). The row keeps
-- the FIRST previous_rrule, hidden_overrides, and removed_rdates, so a
-- second truncation does not overwrite the pre-truncation state with an
-- already-truncated rule or an empty override/rdate set.
INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(calendar_id, uid, cutoff_time) DO UPDATE SET
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

-- name: MoveEventTruncateDeletesUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE event_truncate_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: GetEventTruncateDeleteByCalendarUIDAndCutoff :one
-- The truncation log row of one cutoff of the series in one calendar. Two
-- calendars can hold the same UID (issue #842).
SELECT * FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ? AND cutoff_time = ?;

-- name: ListEventTruncateDeletesByCalendarUID :many
SELECT * FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ? ORDER BY cutoff_time;

-- name: DeleteEventTruncateDeletesByCalendarUID :exec
-- Drop the log rows of one series in one calendar (see
-- DeleteEventExdateDeletesByCalendarUID).
DELETE FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ?;

-- name: DeleteEventTruncateDeletesCollidingWithMove :exec
-- Drop the log rows of the source calendar whose key the target calendar
-- already holds (see DeleteEventExdateDeletesCollidingWithMove).
DELETE FROM event_truncate_deletes
WHERE event_truncate_deletes.calendar_id = sqlc.arg(from_calendar_id) AND event_truncate_deletes.uid = sqlc.arg(uid)
  AND event_truncate_deletes.cutoff_time IN (
    SELECT t.cutoff_time FROM event_truncate_deletes AS t
    WHERE t.calendar_id = sqlc.arg(to_calendar_id) AND t.uid = sqlc.arg(uid));
