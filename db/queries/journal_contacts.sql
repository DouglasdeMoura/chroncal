-- name: CreateJournalContact :one
INSERT INTO journal_contacts (journal_id, text) VALUES (?, ?) RETURNING *;

-- name: ListJournalContactsByJournalID :many
SELECT * FROM journal_contacts WHERE journal_id = ? ORDER BY id;

-- name: DeleteJournalContactsByJournalID :exec
DELETE FROM journal_contacts WHERE journal_id = ?;
