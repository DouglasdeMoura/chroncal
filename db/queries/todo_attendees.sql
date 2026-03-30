-- name: CreateTodoAttendee :one
INSERT INTO todo_attendees (todo_id, email, name, rsvp_status, role, organizer, cutype, rsvp, sent_by, delegated_to, delegated_from, member, dir, language)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListTodoAttendeesByTodoID :many
SELECT * FROM todo_attendees WHERE todo_id = ? ORDER BY organizer DESC, name;

-- name: DeleteTodoAttendeesByTodoID :exec
DELETE FROM todo_attendees WHERE todo_id = ?;
