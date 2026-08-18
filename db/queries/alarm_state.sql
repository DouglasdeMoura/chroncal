-- name: GetAlarmState :one
SELECT * FROM alarm_state WHERE alarm_id = ? AND trigger_at = ?;

-- Claim a fire slot for one alarm. The EXISTS arm reads the action in the
-- same statement as the insert, so a sync pull that rewrites the alarm to
-- a sync-only action between the check and the claim cannot leave a fired
-- state for an alarm the user disabled (issue #579). Zero rows means the
-- claim failed, and the caller reports sql.ErrNoRows.
-- Keep the action list in lockstep with model.FireableAlarmAction.
-- name: CreateAlarmState :one
INSERT INTO alarm_state (alarm_id, event_id, trigger_at, fired_at)
SELECT sqlc.arg(alarm_id), sqlc.arg(event_id), sqlc.arg(trigger_at), sqlc.arg(fired_at)
WHERE EXISTS (
    SELECT 1 FROM event_alarms
    WHERE id = sqlc.arg(alarm_id) AND action IN ('AUDIO','DISPLAY','EMAIL')
)
RETURNING *;

-- name: AcknowledgeAlarmState :exec
UPDATE alarm_state SET acked_at = ? WHERE id = ?;

-- name: SnoozeAlarmState :exec
UPDATE alarm_state SET snoozed_to = ? WHERE id = ?;

-- Hide the state of an alarm a sync pull rewrote to a sync-only action.
-- The filter reads the current action, so the row comes back when a later
-- pull restores a fireable action (issue #579). A retirement that wrote
-- acked_at instead would consume the snooze of the user for good.
-- Keep the action list in lockstep with model.FireableAlarmAction.
-- name: ListPendingAlarmStates :many
SELECT s.* FROM alarm_state s
JOIN event_alarms a ON a.id = s.alarm_id
WHERE s.acked_at IS NULL AND s.fired_at IS NOT NULL
  AND a.action IN ('AUDIO','DISPLAY','EMAIL')
ORDER BY s.trigger_at;

-- name: GetAlarmStateByID :one
SELECT * FROM alarm_state WHERE id = ?;

-- The same sync-only filter as ListPendingAlarmStates, for the re-fire path.
-- Keep the action list in lockstep with model.FireableAlarmAction.
-- name: ListExpiredSnoozedAlarmStates :many
SELECT s.* FROM alarm_state s
JOIN event_alarms a ON a.id = s.alarm_id
WHERE s.fired_at IS NOT NULL
  AND s.acked_at IS NULL
  AND s.snoozed_to IS NOT NULL
  AND s.snoozed_to <= ?
  AND a.action IN ('AUDIO','DISPLAY','EMAIL')
ORDER BY s.snoozed_to;

-- name: RefireAlarmState :execrows
UPDATE alarm_state SET fired_at = ?, snoozed_to = NULL
WHERE id = ? AND snoozed_to IS NOT NULL;

-- name: ListAlarmStatesByEventID :many
SELECT * FROM alarm_state WHERE event_id = ? ORDER BY trigger_at;

-- name: DeleteAlarmStatesByEventID :exec
DELETE FROM alarm_state WHERE event_id = ?;

-- name: PurgeAcknowledgedAlarmStates :execrows
DELETE FROM alarm_state WHERE acked_at IS NOT NULL AND trigger_at < ?;

-- name: PurgeStaleUnacknowledgedAlarmStates :execrows
DELETE FROM alarm_state
WHERE acked_at IS NULL
  AND trigger_at < ?
  AND (snoozed_to IS NULL OR snoozed_to < trigger_at);
