-- name: CreateTodoAttachment :one
INSERT INTO todo_attachments (todo_id, uri, fmttype, data, filename) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListTodoAttachmentsByTodoID :many
SELECT * FROM todo_attachments WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoAttachmentsByTodoID :exec
DELETE FROM todo_attachments WHERE todo_id = ?;

-- name: GetTodoAttachment :one
SELECT * FROM todo_attachments WHERE id = ?;
