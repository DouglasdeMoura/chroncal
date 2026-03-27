-- name: CreateTodoAlarmAttendee :one
INSERT INTO todo_alarm_attendees (alarm_id, email, name) VALUES (?, ?, ?) RETURNING *;

-- name: ListTodoAlarmAttendeesByAlarmID :many
SELECT * FROM todo_alarm_attendees WHERE alarm_id = ? ORDER BY id;

-- name: DeleteTodoAlarmAttendeesByAlarmID :exec
DELETE FROM todo_alarm_attendees WHERE alarm_id = ?;
