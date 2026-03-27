-- name: CreateTodoRelation :one
INSERT INTO todo_relations (todo_id, rel_type, rel_uid) VALUES (?, ?, ?) RETURNING *;

-- name: ListTodoRelationsByTodoID :many
SELECT * FROM todo_relations WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoRelationsByTodoID :exec
DELETE FROM todo_relations WHERE todo_id = ?;
