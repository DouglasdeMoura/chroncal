-- name: CreateEventRelation :one
INSERT INTO event_relations (event_id, rel_type, rel_uid) VALUES (?, ?, ?) RETURNING *;

-- name: ListEventRelationsByEventID :many
SELECT * FROM event_relations WHERE event_id = ? ORDER BY id;

-- name: DeleteEventRelationsByEventID :exec
DELETE FROM event_relations WHERE event_id = ?;
