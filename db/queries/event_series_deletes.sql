-- name: RecordEventSeriesDelete :one
-- Write the log row of one series delete. Each delete gets its own row (the
-- id), so an earlier delete in the same calendar keeps its record. The merge
-- of orphan rows into the log of a master in the trash updates that row
-- (UpdateEventSeriesDeleteHiddenIds). It returns the ID, which the undo
-- keeps (UndoMeta.SeriesLogID).
INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master)
VALUES (?, ?, ?, ?)
RETURNING id;

-- name: GetEventSeriesDeleteByID :one
-- The log row of one series delete, by its ID. The undo of an orphan
-- delete reads it.
SELECT * FROM event_series_deletes WHERE id = ?;

-- name: GetEventSeriesDeleteByCalendarUID :one
-- The newest series-delete log row of one series in one calendar. A restore
-- reverses the newest delete first.
SELECT * FROM event_series_deletes WHERE calendar_id = ? AND uid = ? ORDER BY id DESC LIMIT 1;

-- name: DeleteEventSeriesDeleteByID :exec
-- Drop one series-delete log row. A restore of its delete consumes it.
DELETE FROM event_series_deletes WHERE id = ?;

-- name: DeleteEventSeriesDeletesByCalendarUID :exec
-- Drop the log rows of one series. A calendar move clears the stale rows in
-- the target calendar before the move.
DELETE FROM event_series_deletes WHERE calendar_id = ? AND uid = ?;

-- name: ListOldEventSeriesDeletes :many
-- The log rows before the cutoff. The age purge drops them by ID, except the
-- logs of the masters that the purge of the same run keeps.
SELECT id, calendar_id, uid FROM event_series_deletes WHERE deleted_at < ?;

-- name: DeleteEventSeriesDeletesByIDs :execrows
DELETE FROM event_series_deletes WHERE id IN (sqlc.slice(ids));

-- name: MoveEventSeriesDeletesUIDToCalendar :exec
-- Move the log row of one UID from one calendar to another. A local
-- calendar move of a resource calls it. See issue #828.
UPDATE event_series_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: ListEventSeriesDeletesByUID :many
SELECT * FROM event_series_deletes WHERE uid = ? ORDER BY calendar_id;

-- name: ListEventSeriesDeletesByCalendarUID :many
-- The series-delete logs of one UID in one calendar, one row per delete.
SELECT * FROM event_series_deletes WHERE calendar_id = ? AND uid = ? ORDER BY id;

-- name: UpdateEventSeriesDeleteHiddenIds :exec
-- Rewrite the hidden rows of one log row. A restore of one logged row by ID
-- or by undo drops the row from the list. A later series restore then does
-- not reverse an independent delete of that row.
UPDATE event_series_deletes SET hidden_ids = ? WHERE id = ?;
