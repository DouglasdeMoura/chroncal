-- name: CreateAlarm :one
INSERT INTO event_alarms (event_id, action, trigger_value, description)
VALUES (?, ?, ?, ?) RETURNING *;

-- name: ListAlarmsByEventID :many
SELECT * FROM event_alarms WHERE event_id = ? ORDER BY id;

-- name: DeleteAlarmsByEventID :exec
DELETE FROM event_alarms WHERE event_id = ?;
