-- name: CreateAccount :one
INSERT INTO accounts (name, server_url, auth_type, username)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: GetAccountByName :one
SELECT * FROM accounts WHERE name = ? LIMIT 1;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY name;

-- name: UpdateAccount :exec
UPDATE accounts SET
    name = ?,
    server_url = ?,
    auth_type = ?,
    username = ?,
    updated_at = datetime('now')
WHERE id = ?;

-- name: DeleteAccount :exec
DELETE FROM accounts WHERE id = ?;
