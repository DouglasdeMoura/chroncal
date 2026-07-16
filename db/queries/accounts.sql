-- name: CreateAccount :one
INSERT INTO accounts (name, server_url, auth_type, username, display_order)
VALUES (?, ?, ?, ?, COALESCE((SELECT MAX(display_order) + 1 FROM accounts), 0))
RETURNING *;

-- name: AdvanceCurrentCredentialAccountWatermark :exec
UPDATE credential_locations
SET max_account_id = MAX(max_account_id, ?)
WHERE location = (
    SELECT current_location
    FROM credential_namespace
    WHERE id = 1
);

-- name: GetAccount :one
SELECT * FROM accounts WHERE id = ?;

-- name: GetAccountByName :one
SELECT * FROM accounts WHERE name = ? LIMIT 1;

-- name: ListAccounts :many
SELECT * FROM accounts ORDER BY display_order, id;

-- name: UpdateAccount :exec
UPDATE accounts SET
    name = ?,
    server_url = ?,
    auth_type = ?,
    username = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: SetAccountDisplayOrder :exec
UPDATE accounts SET display_order = ? WHERE id = ?;

-- name: DeleteAccount :exec
DELETE FROM accounts WHERE id = ?;
