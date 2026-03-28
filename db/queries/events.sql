-- name: ListEventsByDateRange :many
SELECT * FROM events WHERE start_time >= ? AND start_time < ? ORDER BY start_time;

-- name: ListEventsByCalendarAndDateRange :many
SELECT * FROM events WHERE calendar_id = ? AND start_time >= ? AND start_time < ? ORDER BY start_time;

-- name: ListOverridesByUID :many
SELECT * FROM events WHERE uid = ? AND recurrence_id != '' ORDER BY recurrence_id;

-- name: ListEventsByStatusAndDateRange :many
SELECT * FROM events WHERE status = ? AND start_time >= ? AND start_time < ? ORDER BY start_time;

-- name: GetEvent :one
SELECT * FROM events WHERE id = ?;

-- name: GetEventByUID :one
SELECT * FROM events WHERE uid = ? AND recurrence_id = '';

-- name: GetEventByUIDAndRecurrenceID :one
SELECT * FROM events WHERE uid = ? AND recurrence_id = ?;

-- name: CreateEvent :one
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, categories, exdates, rdates, recurrence_id, geo
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEvent :one
UPDATE events SET
    title = ?, description = ?, location = ?,
    start_time = ?, end_time = ?, all_day = ?,
    recurrence_rule = ?, calendar_id = ?,
    timezone = ?, status = ?, transp = ?,
    sequence = sequence + 1, priority = ?,
    class = ?, url = ?, categories = ?,
    exdates = ?, rdates = ?, geo = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertEventByUID :one
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, categories, exdates, rdates, recurrence_id, geo
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    title = excluded.title, description = excluded.description,
    location = excluded.location, start_time = excluded.start_time,
    end_time = excluded.end_time, all_day = excluded.all_day,
    recurrence_rule = excluded.recurrence_rule,
    timezone = excluded.timezone, status = excluded.status,
    transp = excluded.transp,
    sequence = MAX(excluded.sequence, events.sequence + 1),
    priority = excluded.priority, class = excluded.class,
    url = excluded.url, categories = excluded.categories,
    exdates = excluded.exdates, rdates = excluded.rdates,
    geo = excluded.geo,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = ?;
