-- name: CreateEventComment :one
INSERT INTO event_comments (event_id, text) VALUES (?, ?) RETURNING *;

-- name: ListEventCommentsByEventID :many
SELECT * FROM event_comments WHERE event_id = ? ORDER BY id;

-- name: DeleteEventCommentsByEventID :exec
DELETE FROM event_comments WHERE event_id = ?;
