-- name: CreateAlarm :one
INSERT INTO event_alarms (event_id, uid, action, trigger_value, description, summary, repeat, duration, related, acknowledged, attach_uri, attach_fmttype)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListAlarmsByEventID :many
SELECT * FROM event_alarms WHERE event_id = ? ORDER BY id;

-- name: ListAlarmsByEventIDs :many
SELECT * FROM event_alarms WHERE event_id IN (sqlc.slice(event_ids)) ORDER BY event_id, id;

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
