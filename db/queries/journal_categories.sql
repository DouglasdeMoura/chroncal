-- name: CreateJournalCategory :one
INSERT INTO journal_categories (journal_id, category) VALUES (?, ?) RETURNING *;

-- name: ListCategoriesByJournalID :many
SELECT * FROM journal_categories WHERE journal_id = ? ORDER BY category;

-- name: DeleteCategoriesByJournalID :exec
DELETE FROM journal_categories WHERE journal_id = ?;

-- name: ListAllJournalCategories :many
SELECT DISTINCT category FROM journal_categories ORDER BY category;
