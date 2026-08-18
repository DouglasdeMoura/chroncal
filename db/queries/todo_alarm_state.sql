-- name: GetTodoAlarmState :one
SELECT * FROM todo_alarm_state 
WHERE alarm_id = ? AND trigger_at = ?;

-- Claim a fire slot for one todo alarm. The EXISTS arm reads the action in
-- the same statement as the insert, so a sync pull that rewrites the alarm
-- to a sync-only action between the check and the claim cannot leave a
-- fired state for an alarm the user disabled (issue #579).
-- Keep the action list in lockstep with model.FireableAlarmAction.
-- name: InsertTodoAlarmState :one
INSERT INTO todo_alarm_state (alarm_id, todo_id, trigger_at, fired_at, acked_at, snoozed_to)
SELECT sqlc.arg(alarm_id), sqlc.arg(todo_id), sqlc.arg(trigger_at),
       sqlc.arg(fired_at), sqlc.arg(acked_at), sqlc.arg(snoozed_to)
WHERE EXISTS (
    SELECT 1 FROM todo_alarms
    WHERE id = sqlc.arg(alarm_id) AND action IN ('AUDIO','DISPLAY','EMAIL')
)
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

-- The same sync-only filter as ListPendingTodoAlarmStates, for the re-fire
-- path. Keep the action list in lockstep with model.FireableAlarmAction.
-- name: ListExpiredTodoSnoozed :many
SELECT s.* FROM todo_alarm_state s
JOIN todo_alarms a ON a.id = s.alarm_id
WHERE s.fired_at IS NOT NULL
  AND s.acked_at IS NULL
  AND s.snoozed_to IS NOT NULL
  AND s.snoozed_to <= ?
  AND a.action IN ('AUDIO','DISPLAY','EMAIL')
ORDER BY s.snoozed_to;

-- name: RefireTodoAlarmState :execrows
UPDATE todo_alarm_state SET fired_at = ?, snoozed_to = NULL
WHERE id = ? AND snoozed_to IS NOT NULL;

-- name: DeleteTodoAlarmState :exec
DELETE FROM todo_alarm_state WHERE id = ?;

-- name: CountTodoAlarmStates :one
SELECT COUNT(*) FROM todo_alarm_state WHERE todo_id = ?;

-- Hide the state of an alarm a sync pull rewrote to a sync-only action.
-- The filter reads the current action, so the row comes back when a later
-- pull restores a fireable action (issue #579).
-- Keep the action list in lockstep with model.FireableAlarmAction.
-- name: ListPendingTodoAlarmStates :many
SELECT s.* FROM todo_alarm_state s
JOIN todo_alarms a ON a.id = s.alarm_id
WHERE s.acked_at IS NULL AND (s.fired_at IS NOT NULL OR s.snoozed_to IS NOT NULL)
  AND a.action IN ('AUDIO','DISPLAY','EMAIL')
ORDER BY s.trigger_at;

-- name: GetTodoAlarmStateByID :one
SELECT * FROM todo_alarm_state WHERE id = ?;

-- name: PurgeAcknowledgedTodoAlarmStates :execrows
DELETE FROM todo_alarm_state WHERE acked_at IS NOT NULL AND trigger_at < ?;

-- name: PurgeStaleUnacknowledgedTodoAlarmStates :execrows
DELETE FROM todo_alarm_state
WHERE acked_at IS NULL
  AND trigger_at < ?
  AND (snoozed_to IS NULL OR snoozed_to < trigger_at);
