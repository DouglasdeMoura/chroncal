-- name: CreateTodoAlarm :one
INSERT INTO todo_alarms (todo_id, uid, action, trigger_value, description, summary, repeat, duration, related, acknowledged, attach_uri, attach_fmttype)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListTodoAlarmsByTodoID :many
SELECT * FROM todo_alarms WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoAlarmsByTodoID :exec
DELETE FROM todo_alarms WHERE todo_id = ?;

-- name: ListTodoAlarmsWithEmptyUID :many
SELECT * FROM todo_alarms WHERE uid IS NULL;

-- name: UpdateTodoAlarmUID :exec
UPDATE todo_alarms SET uid = ? WHERE id = ?;

-- name: UpdateTodoAlarmAcknowledged :exec
UPDATE todo_alarms SET acknowledged = ? WHERE id = ?;
