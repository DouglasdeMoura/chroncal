-- name: CreateAlarmAttendee :one
INSERT INTO event_alarm_attendees (alarm_id, email, name) VALUES (?, ?, ?) RETURNING *;

-- name: ListAlarmAttendeesByAlarmID :many
SELECT * FROM event_alarm_attendees WHERE alarm_id = ? ORDER BY id;

-- name: DeleteAlarmAttendeesByAlarmID :exec
DELETE FROM event_alarm_attendees WHERE alarm_id = ?;
