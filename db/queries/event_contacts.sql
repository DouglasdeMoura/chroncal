-- name: CreateEventContact :one
INSERT INTO event_contacts (event_id, text) VALUES (?, ?) RETURNING *;

-- name: ListEventContactsByEventID :many
SELECT * FROM event_contacts WHERE event_id = ? ORDER BY id;

-- name: DeleteEventContactsByEventID :exec
DELETE FROM event_contacts WHERE event_id = ?;
