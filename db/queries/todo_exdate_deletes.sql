-- name: RecordTodoExdateDelete :exec
-- Idempotent: deleting the same instance twice leaves exactly one log row.
INSERT INTO todo_exdate_deletes (calendar_id, uid, recurrence_id)
VALUES (?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: GetTodoExdateDeleteByUIDRecurrence :one
SELECT * FROM todo_exdate_deletes WHERE uid = ? AND recurrence_id = ?;

-- name: DeleteTodoExdateDelete :exec
DELETE FROM todo_exdate_deletes WHERE id = ?;

-- name: PurgeOldTodoExdateDeletes :execrows
DELETE FROM todo_exdate_deletes WHERE deleted_at < ?;
