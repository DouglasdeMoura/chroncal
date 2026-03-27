-- name: ListEventsByDateRange :many
SELECT * FROM events WHERE start_time >= ? AND start_time < ? ORDER BY start_time;

-- name: ListEventsByCalendarAndDateRange :many
SELECT * FROM events WHERE calendar_id = ? AND start_time >= ? AND start_time < ? ORDER BY start_time;

-- name: GetEvent :one
SELECT * FROM events WHERE id = ?;

-- name: GetEventByUID :one
SELECT * FROM events WHERE uid = ?;

-- name: CreateEvent :one
INSERT INTO events (uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: UpdateEvent :one
UPDATE events SET
    title = ?, description = ?, location = ?,
    start_time = ?, end_time = ?, all_day = ?,
    recurrence_rule = ?, calendar_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertEventByUID :one
INSERT INTO events (uid, calendar_id, title, description, location, start_time, end_time, all_day, recurrence_rule)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid) DO UPDATE SET
    title = excluded.title, description = excluded.description,
    location = excluded.location, start_time = excluded.start_time,
    end_time = excluded.end_time, all_day = excluded.all_day,
    recurrence_rule = excluded.recurrence_rule,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = ?;
