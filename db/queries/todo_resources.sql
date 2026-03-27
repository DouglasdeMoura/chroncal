-- name: CreateTodoResource :one
INSERT INTO todo_resources (todo_id, text) VALUES (?, ?) RETURNING *;

-- name: ListTodoResourcesByTodoID :many
SELECT * FROM todo_resources WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoResourcesByTodoID :exec
DELETE FROM todo_resources WHERE todo_id = ?;
