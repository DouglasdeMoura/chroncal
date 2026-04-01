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
    class, url, exdates, rdates, recurrence_id, geo, duration, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateEvent :one
UPDATE events SET
    title = ?, description = ?, location = ?,
    start_time = ?, end_time = ?, all_day = ?,
    recurrence_rule = ?, calendar_id = ?,
    timezone = ?, status = ?, transp = ?,
    sequence = sequence + 1, priority = ?,
    class = ?, url = ?,
    exdates = ?, rdates = ?, geo = ?,
    duration = ?, dtstamp = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertEventByUID :one
INSERT INTO events (
    uid, calendar_id, title, description, location,
    start_time, end_time, all_day, recurrence_rule,
    timezone, status, transp, sequence, priority,
    class, url, exdates, rdates, recurrence_id, geo, duration, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    title = excluded.title, description = excluded.description,
    location = excluded.location, start_time = excluded.start_time,
    end_time = excluded.end_time, all_day = excluded.all_day,
    recurrence_rule = excluded.recurrence_rule,
    timezone = excluded.timezone, status = excluded.status,
    transp = excluded.transp,
    sequence = MAX(excluded.sequence, events.sequence + 1),
    priority = excluded.priority, class = excluded.class,
    url = excluded.url,
    exdates = excluded.exdates, rdates = excluded.rdates,
    geo = excluded.geo,
    duration = excluded.duration,
    dtstamp = excluded.dtstamp,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: DeleteEvent :exec
DELETE FROM events WHERE id = ?;

-- name: DeleteEventsByUID :exec
DELETE FROM events WHERE uid = ?;

-- name: ListAllEvents :many
SELECT * FROM events;

-- name: ListRecurringEvents :many
SELECT * FROM events WHERE recurrence_rule != '' AND recurrence_id = '';

-- name: ListRecurringEventsByCalendar :many
SELECT * FROM events WHERE recurrence_rule != '' AND recurrence_id = '' AND calendar_id = ?;

-- name: ListRecurringEventsByStatus :many
SELECT * FROM events WHERE recurrence_rule != '' AND recurrence_id = '' AND status = ?;

-- name: ListEventsFiltered :many
SELECT * FROM events
WHERE recurrence_rule = '' AND recurrence_id = ''
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
AND (sqlc.arg(category) = '' OR EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND ec.category = sqlc.arg(category)))
AND (sqlc.arg(from_time) = '' OR start_time >= sqlc.arg(from_time))
AND (sqlc.arg(to_time) = '' OR start_time < sqlc.arg(to_time))
ORDER BY start_time ASC;

-- name: ListRecurringEventsFiltered :many
SELECT * FROM events
WHERE recurrence_rule != '' AND recurrence_id = ''
AND (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
AND (sqlc.arg(category) = '' OR EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND ec.category = sqlc.arg(category)))
ORDER BY start_time ASC;

-- name: ListEventsForExport :many
SELECT * FROM events
WHERE (sqlc.arg(calendar_id) = 0 OR calendar_id = sqlc.arg(calendar_id))
AND (sqlc.arg(from_time) = '' OR start_time >= sqlc.arg(from_time))
AND (sqlc.arg(to_time) = '' OR start_time < sqlc.arg(to_time))
AND (sqlc.arg(category) = '' OR EXISTS (SELECT 1 FROM event_categories ec WHERE ec.event_id = events.id AND ec.category = sqlc.arg(category)))
AND (sqlc.arg(filter_status) = '' OR status = sqlc.arg(filter_status))
ORDER BY start_time ASC;
