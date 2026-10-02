-- Operational state for default (virtual) alarms, issue #815. A default
-- alarm has no event_alarms row, so it cannot share alarm_state (its
-- alarm_id column references event_alarms). The
-- (event_id, action, trigger_value, trigger_at) index makes
-- CreateDefaultAlarmState an atomic claim, like
-- idx_alarm_state_unique does for alarm_state. action belongs in the key:
-- DISPLAY:-PT15M and AUDIO:-PT15M are two separate defaults with the same
-- trigger time, and each one fires and snoozes on its own state.

-- name: GetDefaultAlarmState :one
SELECT * FROM default_alarm_state WHERE event_id = ? AND action = ? AND trigger_value = ? AND trigger_at = ?;

-- Claim a default-alarm fire slot. The EXISTS arm reads the event and its
-- alarm rows in the same statement as the insert, so a sync pull that adds
-- an alarm (or the ACTION:NONE sentinel) between the check and the claim
-- cannot leave a fired default state behind. Zero rows means the claim
-- failed, and the caller reports sql.ErrNoRows. The
-- (event_id, action, trigger_value, trigger_at) UNIQUE index makes the
-- insert itself an atomic claim against overlapping checkers.
-- The inner NOT EXISTS correlates on events.id: sqlc resolves a named
-- parameter only one subquery deep.
-- name: CreateDefaultAlarmState :one
INSERT INTO default_alarm_state (event_id, action, trigger_value, trigger_at, fired_at)
SELECT sqlc.arg(alarm_event_id), sqlc.arg(alarm_action), sqlc.arg(alarm_trigger_value), sqlc.arg(alarm_trigger_at), sqlc.arg(alarm_fired_at)
WHERE EXISTS (
    SELECT 1 FROM events
    WHERE events.id = sqlc.arg(alarm_event_id)
      AND NOT EXISTS (
        SELECT 1 FROM event_alarms
        WHERE event_alarms.event_id = events.id
      )
)
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
