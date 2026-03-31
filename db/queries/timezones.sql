-- name: UpsertTimezone :one
INSERT INTO timezones (tzid, vtimezone_data)
VALUES (?, ?)
ON CONFLICT(tzid) DO UPDATE SET
    vtimezone_data = excluded.vtimezone_data
RETURNING *;

-- name: GetTimezone :one
SELECT * FROM timezones WHERE tzid = ?;

-- name: ListTimezones :many
SELECT * FROM timezones ORDER BY tzid;

-- name: DeleteTimezone :exec
DELETE FROM timezones WHERE tzid = ?;
