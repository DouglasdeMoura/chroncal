-- name: InsertXProperty :exec
INSERT INTO x_properties (owner_type, owner_id, name, value, params)
VALUES (?, ?, ?, ?, ?);

-- name: ListXPropertiesByOwner :many
SELECT id, owner_type, owner_id, name, value, params
FROM x_properties
WHERE owner_type = ? AND owner_id = ?
ORDER BY id;

-- name: DeleteXPropertiesByOwner :exec
DELETE FROM x_properties
WHERE owner_type = ? AND owner_id = ?;

-- name: ListXPropertiesByOwnerIDs :many
SELECT id, owner_type, owner_id, name, value, params
FROM x_properties
WHERE owner_type = ? AND owner_id IN (sqlc.slice(owner_ids))
ORDER BY id;
