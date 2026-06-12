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

-- name: GetAlarmStateByID :one
SELECT * FROM alarm_state WHERE id = ?;

-- name: ListExpiredSnoozedAlarmStates :many
SELECT * FROM alarm_state
WHERE fired_at IS NOT NULL
  AND acked_at IS NULL
  AND snoozed_to IS NOT NULL
  AND snoozed_to <= ?
ORDER BY snoozed_to;

-- name: RefireAlarmState :exec
UPDATE alarm_state SET fired_at = ?, snoozed_to = NULL WHERE id = ?;

-- name: ListAlarmStatesByEventID :many
SELECT * FROM alarm_state WHERE event_id = ? ORDER BY trigger_at;

-- name: DeleteAlarmStatesByEventID :exec
DELETE FROM alarm_state WHERE event_id = ?;

-- name: PurgeAcknowledgedAlarmStates :execrows
DELETE FROM alarm_state WHERE acked_at IS NOT NULL AND trigger_at < ?;
