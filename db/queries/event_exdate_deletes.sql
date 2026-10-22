-- name: RecordEventExdateDelete :one
-- Idempotent: a second delete of the same occurrence in one calendar keeps
-- one log row. Each calendar keeps its own row (issue #842). A delete of
-- the user turns a pruned marker of the occurrence into a delete log.
-- hidden_ids lists the override rows that the deletes hid. The caller
-- passes the union with the list of an earlier delete of the occurrence
-- (see recordExdateDeleteTx). It returns the ID of the log, which the undo
-- keeps (UndoMeta.ExdateLogID).
INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id, hidden_ids)
VALUES (?, ?, ?, ?)
ON CONFLICT(calendar_id, uid, recurrence_id) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    pruned = 0,
    hidden_ids = excluded.hidden_ids
RETURNING id;

-- name: RecordEventExdatePrune :exec
-- Write the pruned marker of an override row that a pull prune hid. A
-- delete log of the same occurrence stays as it is.
INSERT INTO event_exdate_deletes (calendar_id, uid, recurrence_id, pruned)
VALUES (?, ?, ?, 1)
ON CONFLICT(calendar_id, uid, recurrence_id) DO NOTHING;

-- name: ListEventExdateDeletesByCalendar :many
-- The occurrence delete logs of one calendar for the trash. A pruned marker
-- is no delete of the user, so it stays out.
SELECT * FROM event_exdate_deletes
WHERE calendar_id = ? AND pruned = 0
ORDER BY deleted_at DESC;

-- name: GetEventExdateDelete :one
SELECT * FROM event_exdate_deletes WHERE id = ?;


-- name: GetEventExdateDeleteByCalendarUIDRecurrence :one
-- The delete log of one occurrence of the series in one calendar. Two
-- calendars can hold the same UID. A pruned marker is no delete log: the
-- EXDATE contract must not strip an EXDATE of the server for it.
SELECT * FROM event_exdate_deletes WHERE calendar_id = ? AND uid = ? AND recurrence_id = ? AND pruned = 0;

-- name: DeleteEventExdateDelete :exec
DELETE FROM event_exdate_deletes WHERE id = ?;

-- name: ListOldEventExdateDeletes :many
-- The log rows before the cutoff. The age purge drops them by ID, except the
-- logs of the masters that the purge of the same run keeps.
SELECT id, calendar_id, uid FROM event_exdate_deletes WHERE deleted_at < ?;

-- name: DeleteEventExdateDeletesByIDs :execrows
DELETE FROM event_exdate_deletes WHERE id IN (sqlc.slice(ids));

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

-- name: ListEventExdateDeletesByCalendarUID :many
-- The occurrence logs of one series in one calendar, the pruned markers
-- too. The restore of a master with no series-delete log reads them in one
-- query, and leaves the rows they name in the trash.
SELECT * FROM event_exdate_deletes WHERE calendar_id = ? AND uid = ? ORDER BY recurrence_id;

-- name: UpdateEventExdateDeleteHiddenIds :exec
-- The undo of one occurrence delete removes its rows from the list. The
-- rows of an earlier delete of the occurrence stay in the log.
UPDATE event_exdate_deletes SET hidden_ids = ? WHERE id = ?;
