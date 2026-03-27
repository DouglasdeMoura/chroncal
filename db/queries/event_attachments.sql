-- name: CreateEventAttachment :one
INSERT INTO event_attachments (event_id, uri, fmttype, data, filename) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListEventAttachmentsByEventID :many
SELECT * FROM event_attachments WHERE event_id = ? ORDER BY id;

-- name: DeleteEventAttachmentsByEventID :exec
DELETE FROM event_attachments WHERE event_id = ?;

-- name: GetEventAttachment :one
SELECT * FROM event_attachments WHERE id = ?;
