-- name: RecordEventTruncateDelete :exec
-- Write the log row of one truncation. Each truncation gets its own row with
-- its own delete token, also a second truncation at the same cutoff. Each
-- calendar keeps its own rows (issue #842). The row keeps the rule before its
-- own truncation (previous_rrule), so the undo of each truncation restores
-- its own state and its own rows.
INSERT INTO event_truncate_deletes (calendar_id, uid, cutoff_time, previous_rrule, hidden_overrides, removed_rdates, hidden_ids, delete_token)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListEventTruncateDeletesByCalendar :many
SELECT * FROM event_truncate_deletes
WHERE calendar_id = ?
ORDER BY deleted_at DESC;

-- name: GetEventTruncateDelete :one
SELECT * FROM event_truncate_deletes WHERE id = ?;


-- name: DeleteEventTruncateDelete :exec
DELETE FROM event_truncate_deletes WHERE id = ?;

-- name: ListOldEventTruncateDeletes :many
-- The log rows before the cutoff. The age purge drops them by ID, except the
-- logs of the masters that the purge of the same run keeps.
SELECT id, calendar_id, uid FROM event_truncate_deletes WHERE deleted_at < ?;

-- name: DeleteEventTruncateDeletesByIDs :execrows
DELETE FROM event_truncate_deletes WHERE id IN (sqlc.slice(ids));

-- name: MoveEventTruncateDeletesUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE event_truncate_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: GetEventTruncateDeleteByCalendarUIDCutoffToken :one
-- The truncation log row of one truncation of the series in one calendar: the
-- cutoff and the delete token name it. Two calendars can hold the same UID
-- (issue #842).
SELECT * FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ? AND cutoff_time = ? AND delete_token = ?;

-- name: ListEventTruncateDeletesByCalendarUID :many
SELECT * FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ? ORDER BY cutoff_time, id;

-- name: DeleteEventTruncateDeletesByCalendarUID :exec
-- Drop the log rows of one series in one calendar (see
-- DeleteEventExdateDeletesByCalendarUID).
DELETE FROM event_truncate_deletes WHERE calendar_id = ? AND uid = ?;

-- name: DeleteEventTruncateDeletesCollidingWithMove :exec
-- Drop the log rows of the source calendar whose cutoff the target calendar
-- already holds (see DeleteEventExdateDeletesCollidingWithMove). The cutoff
-- alone is the key here: a row of the other calendar at that cutoff is stale.
DELETE FROM event_truncate_deletes
WHERE event_truncate_deletes.calendar_id = sqlc.arg(from_calendar_id) AND event_truncate_deletes.uid = sqlc.arg(uid)
  AND event_truncate_deletes.cutoff_time IN (
    SELECT t.cutoff_time FROM event_truncate_deletes AS t
    WHERE t.calendar_id = sqlc.arg(to_calendar_id) AND t.uid = sqlc.arg(uid));

-- name: UpdateEventTruncateDeleteHiddenIds :exec
-- Rewrite the hidden rows of one log row (see
-- UpdateEventSeriesDeleteHiddenIds).
UPDATE event_truncate_deletes SET hidden_ids = ? WHERE id = ?;
