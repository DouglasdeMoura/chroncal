-- name: ListCalendars :many
SELECT * FROM calendars ORDER BY name;

-- name: GetCalendar :one
SELECT * FROM calendars WHERE id = ?;

-- name: CreateCalendar :one
INSERT INTO calendars (name, color, description) VALUES (?, ?, ?) RETURNING *;

-- name: UpdateCalendar :one
UPDATE calendars SET name = ?, color = ?, description = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? RETURNING *;

-- name: CountCalendars :one
SELECT COUNT(*) FROM calendars;

-- name: DeleteCalendar :exec
DELETE FROM calendars WHERE id = ?;

-- name: ListCalendarsByAccount :many
SELECT * FROM calendars WHERE account_id = ? ORDER BY name;

-- name: UpdateCalendarSyncState :exec
UPDATE calendars SET
    ctag = ?,
    sync_token = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: LinkCalendarToAccount :exec
UPDATE calendars SET
    account_id = ?,
    remote_url = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;
