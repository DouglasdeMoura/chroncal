-- name: CreateAlarm :one
INSERT INTO event_alarms (event_id, uid, action, trigger_value, description, summary, repeat, duration, related, acknowledged, attach_uri, attach_fmttype, attach_binary)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListAlarmsByEventID :many
SELECT * FROM event_alarms WHERE event_id = ? ORDER BY id;

-- name: ListAlarmSummariesByEventIDs :many
-- The TUI shows the alarms of the expanded rows (issue #912). It needs the
-- display fields only, not the attach data.
SELECT event_id, action, trigger_value, related FROM event_alarms
WHERE event_id IN (sqlc.slice(event_ids))
ORDER BY event_id, id;

-- name: ListDistinctAlarmTriggers :many
-- The action list mirrors model.FireableAlarmAction. Keep the two in
-- lockstep. A preserved sync-only action never fires, so its trigger
-- must not size the alarm check window.
SELECT DISTINCT trigger_value FROM event_alarms
WHERE action IN ('AUDIO', 'DISPLAY', 'EMAIL');

-- name: ListFireableAlarmsByEventIDs :many
-- The action list mirrors model.FireableAlarmAction. Keep the two in
-- lockstep. The alarm check loop is the only caller, and a preserved
-- sync-only action must not reach it.
SELECT * FROM event_alarms
WHERE event_id IN (sqlc.slice(event_ids))
  AND action IN ('AUDIO', 'DISPLAY', 'EMAIL')
ORDER BY event_id, id;

-- name: DeleteAlarmsByEventID :exec
DELETE FROM event_alarms WHERE event_id = ?;

-- name: DeleteAlarmByID :exec
DELETE FROM event_alarms WHERE id = ?;

-- name: UpdateAlarmUID :exec
UPDATE event_alarms SET uid = ? WHERE id = ?;

-- name: UpdateAlarmAcknowledged :exec
UPDATE event_alarms SET acknowledged = ? WHERE id = ? AND event_id = ?;

-- name: ListAlarmsWithEmptyUID :many
SELECT * FROM event_alarms WHERE uid IS NULL;

-- name: UpdateAlarmContentByID :exec
UPDATE event_alarms
SET action = ?, trigger_value = ?, description = ?, summary = ?, repeat = ?,
    duration = ?, related = ?, acknowledged = ?, attach_uri = ?, attach_fmttype = ?,
    attach_binary = ?
WHERE id = ? AND event_id = ?;

-- name: ListEventIDsWithAlarms :many
-- The alarm check loop uses this to find events that carry at least one
-- alarm row of any action, fireable or not. A sync-only sentinel such as
-- ACTION:NONE means the organiser turned the reminder off, so default
-- alarms must not apply (issue #815).
SELECT DISTINCT event_id FROM event_alarms
WHERE event_id IN (sqlc.slice(event_ids));

-- name: ListEventAlarmUIDsHeldByOtherEvents :many
-- The unique index on uid refuses a UID that an alarm of another event
-- holds. The alarm write and the import comparison then treat the alarm as
-- an alarm with no UID (model.WithoutRefusedAlarmUIDs, issue #915).
SELECT DISTINCT uid FROM event_alarms
WHERE event_id <> sqlc.arg(event_id) AND uid IN (sqlc.slice(uids));
