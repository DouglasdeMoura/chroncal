-- name: GetTodoAlarmState :one
SELECT * FROM todo_alarm_state 
WHERE alarm_id = ? AND trigger_at = ?;

-- name: InsertTodoAlarmState :one
INSERT INTO todo_alarm_state (alarm_id, todo_id, trigger_at, fired_at, acked_at, snoozed_to)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateTodoAlarmState :exec
UPDATE todo_alarm_state 
SET fired_at = ?, acked_at = ?, snoozed_to = ?
WHERE id = ?;

-- name: ListTodoAlarmStates :many
SELECT * FROM todo_alarm_state 
WHERE todo_id = ? 
ORDER BY trigger_at DESC;

-- name: ListFiredTodoAlarmStates :many
SELECT * FROM todo_alarm_state 
WHERE todo_id = ? AND fired_at IS NOT NULL AND acked_at IS NULL AND snoozed_to IS NULL
ORDER BY fired_at DESC;

-- name: ListExpiredTodoSnoozed :many
SELECT * FROM todo_alarm_state 
WHERE snoozed_to IS NOT NULL AND snoozed_to <= ?
ORDER BY snoozed_to;

-- name: RefireTodoAlarmState :exec
UPDATE todo_alarm_state SET fired_at = ?, snoozed_to = NULL WHERE id = ?;

-- name: DeleteTodoAlarmState :exec
DELETE FROM todo_alarm_state WHERE id = ?;

-- name: CountTodoAlarmStates :one
SELECT COUNT(*) FROM todo_alarm_state WHERE todo_id = ?;
