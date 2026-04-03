-- name: CreateJournalRelation :one
INSERT INTO journal_relations (journal_id, rel_type, rel_uid) VALUES (?, ?, ?) RETURNING *;

-- name: ListJournalRelationsByJournalID :many
SELECT * FROM journal_relations WHERE journal_id = ? ORDER BY id;

-- name: DeleteJournalRelationsByJournalID :exec
DELETE FROM journal_relations WHERE journal_id = ?;
