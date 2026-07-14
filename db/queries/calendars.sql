-- name: ListCalendars :many
SELECT * FROM calendars ORDER BY display_order, name;

-- name: GetCalendar :one
SELECT * FROM calendars WHERE id = ?;

-- name: CreateCalendar :one
-- display_order is computed as MAX+1 (0 for the first calendar) so new
-- calendars append to the bottom of the sidebar instead of all colliding at 0.
INSERT INTO calendars (name, color, description, display_order)
VALUES (?, ?, ?, (SELECT COALESCE(MAX(display_order), -1) + 1 FROM calendars))
RETURNING *;

-- name: UpdateCalendar :one
UPDATE calendars SET name = ?, color = ?, description = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ? RETURNING *;

-- name: CountCalendars :one
SELECT COUNT(*) FROM calendars;

-- name: DeleteCalendar :exec
DELETE FROM calendars WHERE id = ?;

-- name: ListCalendarsByAccount :many
SELECT * FROM calendars WHERE account_id = ? ORDER BY display_order, name;

-- name: SetCalendarDisplayOrder :exec
UPDATE calendars SET
    display_order = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: UpdateCalendarSyncState :exec
UPDATE calendars SET
    ctag = ?,
    sync_token = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: UpdateCalendarSyncHealth :exec
UPDATE calendars SET
    last_sync_attempted_at = ?,
    last_sync_at = ?,
    last_sync_error = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: LinkCalendarToAccount :exec
UPDATE calendars SET
    account_id = ?,
    remote_url = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: MarkCalendarColorDirty :exec
UPDATE calendars SET
    color_dirty = 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: UpdateCalendarColorFromSync :exec
UPDATE calendars SET
    color = ?,
    remote_color = ?,
    color_dirty = 0,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: ClearCalendarColorDirty :exec
UPDATE calendars SET
    remote_color = ?,
    color_dirty = 0,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: UpdateCalendarOwnerEmail :exec
UPDATE calendars SET
    owner_email = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: GetDefaultCalendar :one
SELECT * FROM calendars WHERE is_default = 1 LIMIT 1;

-- name: ClearDefaultCalendar :exec
UPDATE calendars SET
    is_default = 0,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE is_default = 1;

-- name: SetCalendarAsDefault :exec
UPDATE calendars SET
    is_default = 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ?;

-- name: CountDefaultCalendars :one
SELECT COUNT(*) FROM calendars WHERE is_default = 1;

-- name: CreateDiscoveredCalendar :one
INSERT INTO calendars (
    name, color, description, display_order,
    account_id, remote_url, remote_color,
    remote_name, remote_access, remote_components
)
VALUES (
    ?, ?, ?, (SELECT COALESCE(MAX(display_order), -1) + 1 FROM calendars),
    ?, ?, ?,
    ?, ?, ?
)
RETURNING *;

-- name: MarkAccountCalendarsMissing :exec
UPDATE calendars SET
    remote_missing = 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE account_id = ?;

-- name: UpdateCalendarDiscovery :one
UPDATE calendars SET
    name = CASE
        WHEN remote_name = '' OR name = remote_name THEN sqlc.arg(remote_name)
        ELSE name
    END,
    color = CASE
        WHEN color_dirty = 0 AND sqlc.arg(remote_color) <> '' THEN sqlc.arg(remote_color)
        ELSE color
    END,
    remote_color = CASE
        WHEN sqlc.arg(remote_color) <> '' THEN sqlc.arg(remote_color)
        ELSE remote_color
    END,
    remote_name = sqlc.arg(remote_name),
    remote_access = sqlc.arg(remote_access),
    remote_components = sqlc.arg(remote_components),
    remote_missing = 0,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = sqlc.arg(id)
RETURNING id;

-- name: ClearRemoteLinksByAccount :exec
UPDATE calendars SET
    account_id = NULL,
    remote_url = '',
    ctag = '',
    sync_token = '',
    last_sync_at = '',
    last_sync_attempted_at = '',
    last_sync_error = '',
    remote_color = '',
    color_dirty = 0,
    remote_name = '',
    remote_access = 'unknown',
    remote_components = '',
    remote_missing = 0,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE account_id = ?;
