-- name: CreateAttendee :one
INSERT INTO event_attendees (event_id, email, name, rsvp_status, role, organizer)
VALUES (?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListAttendeesByEventID :many
SELECT * FROM event_attendees WHERE event_id = ? ORDER BY organizer DESC, name;

-- name: DeleteAttendeesByEventID :exec
DELETE FROM event_attendees WHERE event_id = ?;
