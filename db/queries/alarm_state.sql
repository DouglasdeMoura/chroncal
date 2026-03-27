-- name: GetAlarmState :one
SELECT * FROM alarm_state WHERE alarm_id = ? AND trigger_at = ?;

-- name: CreateAlarmState :one
INSERT INTO alarm_state (alarm_id, event_id, trigger_at, fired_at)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: AcknowledgeAlarmState :exec
UPDATE alarm_state SET acked_at = ? WHERE id = ?;

-- name: SnoozeAlarmState :exec
UPDATE alarm_state SET snoozed_to = ? WHERE id = ?;

-- name: ListPendingAlarmStates :many
SELECT * FROM alarm_state WHERE acked_at IS NULL AND fired_at IS NOT NULL ORDER BY trigger_at;

-- name: ListAlarmStatesByEventID :many
SELECT * FROM alarm_state WHERE event_id = ? ORDER BY trigger_at;

-- name: DeleteAlarmStatesByEventID :exec
DELETE FROM alarm_state WHERE event_id = ?;
