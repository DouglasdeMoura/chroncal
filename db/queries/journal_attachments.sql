-- name: CreateJournalAttachment :one
INSERT INTO journal_attachments (journal_id, uri, fmttype, data, filename) VALUES (?, ?, ?, ?, ?) RETURNING *;

-- name: ListJournalAttachmentsByJournalID :many
SELECT * FROM journal_attachments WHERE journal_id = ? ORDER BY id;

-- name: DeleteJournalAttachmentsByJournalID :exec
DELETE FROM journal_attachments WHERE journal_id = ?;

-- name: GetJournalAttachment :one
SELECT * FROM journal_attachments WHERE id = ?;
