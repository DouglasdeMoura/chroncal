-- name: InsertRecurrenceInstance :one
INSERT INTO recurrence_instances (event_id, original_id, instance_at, is_override)
VALUES (?, ?, ?, ?)
ON CONFLICT(event_id, instance_at) DO NOTHING
RETURNING *;

-- name: ListRecurrenceInstances :many
SELECT * FROM recurrence_instances 
WHERE event_id = ? AND instance_at >= ? AND instance_at < ?
ORDER BY instance_at;

-- name: DeleteRecurrenceInstances :exec
DELETE FROM recurrence_instances WHERE event_id = ? AND instance_at >= ?;

-- name: CountRecurrenceInstances :one
SELECT COUNT(*) FROM recurrence_instances WHERE event_id = ?;

-- Same for todos
-- name: InsertTodoRecurrenceInstance :one
INSERT INTO todo_recurrence_instances (todo_id, original_id, instance_at, is_override)
VALUES (?, ?, ?, ?)
ON CONFLICT(todo_id, instance_at) DO NOTHING
RETURNING *;

-- name: ListTodoRecurrenceInstances :many
SELECT * FROM todo_recurrence_instances 
WHERE todo_id = ? AND instance_at >= ? AND instance_at < ?
ORDER BY instance_at;

-- name: DeleteTodoRecurrenceInstances :exec
DELETE FROM todo_recurrence_instances WHERE todo_id = ? AND instance_at >= ?;
