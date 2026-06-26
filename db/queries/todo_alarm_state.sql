-- name: GetTodoAlarmState :one
SELECT * FROM todo_alarm_state 
WHERE alarm_id = ? AND trigger_at = ?;

-- name: InsertTodoAlarmState :one
INSERT INTO todo_alarm_state (alarm_id, todo_id, trigger_at, fired_at, acked_at, snoozed_to)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: AcknowledgeTodoAlarmState :exec
UPDATE todo_alarm_state SET acked_at = ? WHERE id = ?;

-- name: SnoozeTodoAlarmState :exec
UPDATE todo_alarm_state SET snoozed_to = ? WHERE id = ?;

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

-- name: RefireTodoAlarmState :execrows
UPDATE todo_alarm_state SET fired_at = ?, snoozed_to = NULL
WHERE id = ? AND snoozed_to IS NOT NULL;

-- name: DeleteTodoAlarmState :exec
DELETE FROM todo_alarm_state WHERE id = ?;

-- name: CountTodoAlarmStates :one
SELECT COUNT(*) FROM todo_alarm_state WHERE todo_id = ?;

-- name: ListPendingTodoAlarmStates :many
SELECT * FROM todo_alarm_state
WHERE acked_at IS NULL AND (fired_at IS NOT NULL OR snoozed_to IS NOT NULL)
ORDER BY trigger_at;

-- name: GetTodoAlarmStateByID :one
SELECT * FROM todo_alarm_state WHERE id = ?;

-- name: PurgeAcknowledgedTodoAlarmStates :execrows
DELETE FROM todo_alarm_state WHERE acked_at IS NOT NULL AND trigger_at < ?;

-- name: PurgeStaleUnacknowledgedTodoAlarmStates :execrows
DELETE FROM todo_alarm_state
WHERE acked_at IS NULL
  AND trigger_at < ?
  AND (snoozed_to IS NULL OR snoozed_to < trigger_at);
