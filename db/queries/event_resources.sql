-- name: CreateEventResource :one
INSERT INTO event_resources (event_id, text) VALUES (?, ?) RETURNING *;

-- name: ListEventResourcesByEventID :many
SELECT * FROM event_resources WHERE event_id = ? ORDER BY id;

-- name: DeleteEventResourcesByEventID :exec
DELETE FROM event_resources WHERE event_id = ?;
