-- name: CreateEventCategory :one
INSERT INTO event_categories (event_id, category) VALUES (?, ?) RETURNING *;

-- name: ListCategoriesByEventID :many
SELECT * FROM event_categories WHERE event_id = ? ORDER BY category;

-- name: DeleteCategoriesByEventID :exec
DELETE FROM event_categories WHERE event_id = ?;

-- name: ListAllEventCategories :many
SELECT DISTINCT category FROM event_categories ORDER BY category;

-- name: ListAllEventCategoriesWithIDs :many
SELECT event_id, category FROM event_categories ORDER BY event_id, category;

-- name: ListCategoriesByEventIDs :many
SELECT event_id, category FROM event_categories WHERE event_id IN sqlx.in(?) ORDER BY event_id, category;
