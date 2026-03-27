-- name: CreateTodoComment :one
INSERT INTO todo_comments (todo_id, text) VALUES (?, ?) RETURNING *;

-- name: ListTodoCommentsByTodoID :many
SELECT * FROM todo_comments WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoCommentsByTodoID :exec
DELETE FROM todo_comments WHERE todo_id = ?;
