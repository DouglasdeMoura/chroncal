-- name: RecordEventSeriesDelete :exec
-- One log row per series in one calendar. A second series delete replaces
-- the list: the first restore consumed the first row.
INSERT INTO event_series_deletes (calendar_id, uid, hidden_overrides)
VALUES (?, ?, ?)
ON CONFLICT(calendar_id, uid) DO UPDATE SET
    hidden_overrides = excluded.hidden_overrides,
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: GetEventSeriesDeleteByCalendarUID :one
SELECT * FROM event_series_deletes WHERE calendar_id = ? AND uid = ?;

-- name: DeleteEventSeriesDeletesByCalendarUID :exec
-- Drop the log row of one series. A restore of the series consumes it, a
-- purge of the master drops it, and a calendar move clears a stale row in
-- the target calendar before the move.
DELETE FROM event_series_deletes WHERE calendar_id = ? AND uid = ?;

-- name: PurgeOldEventSeriesDeletes :execrows
DELETE FROM event_series_deletes WHERE deleted_at < ?;

-- name: MoveEventSeriesDeletesUIDToCalendar :exec
-- Move the log row of one UID from one calendar to another. A local
-- calendar move of a resource calls it. See issue #828.
UPDATE event_series_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);
