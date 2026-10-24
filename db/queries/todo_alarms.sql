-- name: CreateTodoAlarm :one
INSERT INTO todo_alarms (todo_id, uid, action, trigger_value, description, summary, repeat, duration, related, acknowledged, attach_uri, attach_fmttype, attach_binary)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListTodoAlarmsByTodoID :many
SELECT * FROM todo_alarms WHERE todo_id = ? ORDER BY id;

-- name: ListFireableTodoAlarmsByTodoIDs :many
-- The action list mirrors model.FireableAlarmAction. Keep the two in
-- lockstep. The alarm check loop is the only caller, and a preserved
-- sync-only action must not reach it. The loop reads every todo on each
-- tick, so it fetches the whole set in one query.
SELECT * FROM todo_alarms
WHERE todo_id IN (sqlc.slice(todo_ids))
  AND action IN ('AUDIO', 'DISPLAY', 'EMAIL')
ORDER BY id;

-- name: ListDistinctTodoAlarmTriggers :many
-- The action list mirrors model.FireableAlarmAction. Keep the two in
-- lockstep. A preserved sync-only action never fires, so its trigger
-- must not size the alarm check window.
SELECT DISTINCT trigger_value FROM todo_alarms
WHERE action IN ('AUDIO', 'DISPLAY', 'EMAIL');

-- name: DeleteTodoAlarmsByTodoID :exec
DELETE FROM todo_alarms WHERE todo_id = ?;

-- name: DeleteTodoAlarmByID :exec
DELETE FROM todo_alarms WHERE id = ?;

-- name: UpdateTodoAlarmContentByID :exec
UPDATE todo_alarms
SET action = ?, trigger_value = ?, description = ?, summary = ?, repeat = ?,
    duration = ?, related = ?, acknowledged = ?, attach_uri = ?, attach_fmttype = ?,
    attach_binary = ?
WHERE id = ? AND todo_id = ?;

-- name: ListTodoAlarmsWithEmptyUID :many
SELECT * FROM todo_alarms WHERE uid IS NULL;

-- name: UpdateTodoAlarmUID :exec
UPDATE todo_alarms SET uid = ? WHERE id = ?;

-- name: UpdateTodoAlarmAcknowledged :exec
UPDATE todo_alarms SET acknowledged = ? WHERE id = ?;
