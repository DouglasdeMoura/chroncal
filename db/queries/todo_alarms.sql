-- name: CreateTodoAlarm :one
INSERT INTO todo_alarms (todo_id, action, trigger_value, description, summary, repeat, duration, related)
VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListTodoAlarmsByTodoID :many
SELECT * FROM todo_alarms WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoAlarmsByTodoID :exec
DELETE FROM todo_alarms WHERE todo_id = ?;
