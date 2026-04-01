-- name: CreateTodoCategory :one
INSERT INTO todo_categories (todo_id, category) VALUES (?, ?) RETURNING *;

-- name: ListCategoriesByTodoID :many
SELECT * FROM todo_categories WHERE todo_id = ? ORDER BY category;

-- name: DeleteCategoriesByTodoID :exec
DELETE FROM todo_categories WHERE todo_id = ?;

-- name: ListAllTodoCategories :many
SELECT DISTINCT category FROM todo_categories ORDER BY category;

-- name: ListCategoriesByTodoIDs :many
SELECT todo_id, category FROM todo_categories WHERE todo_id IN sqlx.in(?) ORDER BY todo_id, category;
