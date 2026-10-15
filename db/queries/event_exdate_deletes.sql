-- name: RecordEventExdateDelete :exec
-- Idempotent: deleting the same instance twice leaves exactly one log row.
INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id)
VALUES (?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: ListEventExdateDeletesByCalendar :many
SELECT * FROM event_exdate_deletes
WHERE calendar_id = ?
ORDER BY deleted_at DESC;

-- name: GetEventExdateDelete :one
SELECT * FROM event_exdate_deletes WHERE id = ?;

-- name: GetEventExdateDeleteByUIDRecurrence :one
SELECT * FROM event_exdate_deletes WHERE uid = ? AND recurrence_id = ?;

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
