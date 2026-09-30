-- Operational state for default (virtual) alarms, issue #815. A default
-- alarm has no event_alarms row, so it cannot share alarm_state (its
-- alarm_id column references event_alarms). The
-- (event_id, trigger_value, trigger_at) index makes CreateDefaultAlarmState
-- an atomic claim, like idx_alarm_state_unique does for alarm_state.

-- name: GetDefaultAlarmState :one
SELECT * FROM default_alarm_state WHERE event_id = ? AND trigger_value = ? AND trigger_at = ?;

-- name: CreateDefaultAlarmState :one
INSERT INTO default_alarm_state (event_id, action, trigger_value, trigger_at, fired_at)
VALUES (sqlc.arg(event_id), sqlc.arg(action), sqlc.arg(trigger_value), sqlc.arg(trigger_at), sqlc.arg(fired_at))
RETURNING *;

-- name: GetDefaultAlarmStateByID :one
SELECT * FROM default_alarm_state WHERE id = ?;

-- name: AcknowledgeDefaultAlarmState :exec
UPDATE default_alarm_state SET acked_at = ? WHERE id = ?;

-- name: SnoozeDefaultAlarmState :exec
UPDATE default_alarm_state SET snoozed_to = ? WHERE id = ?;

-- name: ListPendingDefaultAlarmStates :many
SELECT * FROM default_alarm_state
WHERE acked_at IS NULL AND fired_at IS NOT NULL
ORDER BY trigger_at;

-- The same expired-snooze filter as ListExpiredSnoozedAlarmStates, for the
-- re-fire path.
-- name: ListExpiredSnoozedDefaultAlarmStates :many
SELECT * FROM default_alarm_state
WHERE fired_at IS NOT NULL
  AND acked_at IS NULL
  AND snoozed_to IS NOT NULL
  AND snoozed_to <= ?
ORDER BY snoozed_to;

-- The same atomic refire claim as RefireAlarmState.
-- name: RefireDefaultAlarmState :execrows
UPDATE default_alarm_state SET fired_at = ?, snoozed_to = NULL
WHERE id = ? AND snoozed_to IS NOT NULL;

-- name: PurgeAcknowledgedDefaultAlarmStates :execrows
DELETE FROM default_alarm_state WHERE acked_at IS NOT NULL AND trigger_at < ?;

-- The same stale-row filter as PurgeStaleUnacknowledgedAlarmStates: a
-- rescheduled event leaves unfired trigger rows behind. Delete the ones
-- that no checker will ever fire again.
-- name: PurgeStaleUnacknowledgedDefaultAlarmStates :execrows
DELETE FROM default_alarm_state
WHERE acked_at IS NULL
  AND trigger_at < ?
  AND (snoozed_to IS NULL OR snoozed_to < trigger_at);
