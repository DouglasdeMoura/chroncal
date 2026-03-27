-- name: CreateTodoContact :one
INSERT INTO todo_contacts (todo_id, text) VALUES (?, ?) RETURNING *;

-- name: ListTodoContactsByTodoID :many
SELECT * FROM todo_contacts WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoContactsByTodoID :exec
DELETE FROM todo_contacts WHERE todo_id = ?;
