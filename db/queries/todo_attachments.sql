-- name: CreateTodoAttachment :one
INSERT INTO todo_attachments (todo_id, uri, fmttype) VALUES (?, ?, ?) RETURNING *;

-- name: ListTodoAttachmentsByTodoID :many
SELECT * FROM todo_attachments WHERE todo_id = ? ORDER BY id;

-- name: DeleteTodoAttachmentsByTodoID :exec
DELETE FROM todo_attachments WHERE todo_id = ?;
