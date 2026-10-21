-- name: RecordEventExdateDelete :exec
-- Idempotent: a second delete of the same occurrence in one calendar keeps
-- one log row. Each calendar keeps its own row (issue #842).
INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id)
VALUES (?, ?, ?)
ON CONFLICT(calendar_id, uid, recurrence_id) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: ListEventExdateDeletesByCalendar :many
SELECT * FROM event_exdate_deletes
WHERE calendar_id = ?
ORDER BY deleted_at DESC;

-- name: GetEventExdateDelete :one
SELECT * FROM event_exdate_deletes WHERE id = ?;


-- name: GetEventExdateDeleteByCalendarUIDRecurrence :one
-- The delete log of one occurrence of the series in one calendar. Two
-- calendars can hold the same UID.
SELECT * FROM event_exdate_deletes WHERE calendar_id = ? AND uid = ? AND recurrence_id = ?;

-- name: DeleteEventExdateDelete :exec
DELETE FROM event_exdate_deletes WHERE id = ?;

-- name: PurgeOldEventExdateDeletes :execrows
DELETE FROM event_exdate_deletes WHERE deleted_at < ?;

-- name: MoveEventExdateDeletesUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE event_exdate_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: DeleteEventExdateDeletesByCalendarUID :exec
-- Drop the log rows of one series in one calendar. A purge of the master
-- drops them. A calendar move clears stale rows in the target calendar before
-- the move, because the keys are per calendar.
DELETE FROM event_exdate_deletes WHERE calendar_id = ? AND uid = ?;

-- name: DeleteEventExdateDeletesCollidingWithMove :exec
-- Drop the log rows of the source calendar whose key the target calendar
-- already holds. A move of the UID into a calendar with a master of the UID
-- is a legacy override that goes back to its master. That move keeps the logs
-- of the target.
DELETE FROM event_exdate_deletes
WHERE event_exdate_deletes.calendar_id = sqlc.arg(from_calendar_id) AND event_exdate_deletes.uid = sqlc.arg(uid)
  AND event_exdate_deletes.recurrence_id IN (
    SELECT t.recurrence_id FROM event_exdate_deletes AS t
    WHERE t.calendar_id = sqlc.arg(to_calendar_id) AND t.uid = sqlc.arg(uid));
