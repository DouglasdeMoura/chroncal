-- name: RecordEventSeriesDelete :exec
-- One log row per series in one calendar. The service writes the merged
-- list of an earlier unconsumed row and the new rows (see
-- recordSeriesDeleteTx), so the upsert replaces the column with it.
INSERT INTO event_series_deletes (calendar_id, uid, hidden_ids, had_master)
VALUES (?, ?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    hidden_ids = excluded.hidden_ids,
    had_master = excluded.had_master,
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: GetEventSeriesDeleteByCalendarUID :one
SELECT * FROM event_series_deletes WHERE calendar_id = ? AND uid = ?;

-- name: DeleteEventSeriesDeletesByCalendarUID :exec
-- Drop the log row of one series. A restore of the series consumes it. A
-- purge of the master drops it with the master. A calendar move clears a
-- stale row in the target calendar before the move.
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

-- name: UpdateEventSeriesDeleteHiddenIds :exec
-- Rewrite the hidden rows of one log row. A restore of one logged row by ID
-- or by undo drops the row from the list. A later series restore then does
-- not reverse an independent delete of that row.
UPDATE event_series_deletes SET hidden_ids = ? WHERE id = ?;
