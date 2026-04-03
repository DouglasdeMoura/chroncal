-- name: CreateJournalComment :one
INSERT INTO journal_comments (journal_id, text) VALUES (?, ?) RETURNING *;

-- name: ListJournalCommentsByJournalID :many
SELECT * FROM journal_comments WHERE journal_id = ? ORDER BY id;

-- name: DeleteJournalCommentsByJournalID :exec
DELETE FROM journal_comments WHERE journal_id = ?;
