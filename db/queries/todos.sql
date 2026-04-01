-- name: ListTodos :many
SELECT * FROM todos WHERE status != 'COMPLETED' AND status != 'CANCELLED' ORDER BY due_date, summary;

-- name: ListTodosByCalendar :many
SELECT * FROM todos WHERE calendar_id = ? AND status != 'COMPLETED' AND status != 'CANCELLED' ORDER BY due_date, summary;

-- name: ListTodosByStatus :many
SELECT * FROM todos WHERE status = ? ORDER BY due_date, summary;

-- name: ListTodosByDueDateRange :many
SELECT * FROM todos WHERE due_date >= ? AND due_date < ? ORDER BY due_date, summary;

-- name: ListAllTodos :many
SELECT * FROM todos ORDER BY due_date, summary;

-- name: ListRecurringTodos :many
SELECT * FROM todos WHERE recurrence_rule IS NOT NULL AND recurrence_id = '';

-- name: ListRecurringTodosByCalendar :many
SELECT * FROM todos WHERE recurrence_rule IS NOT NULL AND recurrence_id = '' AND calendar_id = ?;


-- name: GetTodo :one
SELECT * FROM todos WHERE id = ?;

-- name: GetTodoByUID :one
SELECT * FROM todos WHERE uid = ? AND recurrence_id = '';

-- name: GetTodoByUIDAndRecurrenceID :one
SELECT * FROM todos WHERE uid = ? AND recurrence_id = ?;

-- name: ListTodoOverridesByUID :many
SELECT * FROM todos WHERE uid = ? AND recurrence_id != '' ORDER BY recurrence_id;


-- name: DeleteTodosByUID :exec
DELETE FROM todos WHERE uid = ?;

-- name: CreateTodo :one
INSERT INTO todos (
    uid, calendar_id, summary, description, location,
    due_date, start_date, duration, completed_at, percent_complete,
    status, priority, class, url,
    recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, geo, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateTodo :one
UPDATE todos SET
    summary = ?, description = ?, location = ?,
    due_date = ?, start_date = ?, duration = ?,
    completed_at = ?, percent_complete = ?,
    status = ?, calendar_id = ?, priority = ?,
    class = ?, url = ?,
    recurrence_rule = ?, timezone = ?,
    sequence = sequence + 1,
    exdates = ?, rdates = ?, geo = ?,
    dtstamp = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: CompleteTodo :one
UPDATE todos SET
    status = 'COMPLETED',
    completed_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    percent_complete = 100,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertTodoByUID :one
INSERT INTO todos (
    uid, calendar_id, summary, description, location,
    due_date, start_date, duration, completed_at, percent_complete,
    status, priority, class, url,
    recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, geo, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    calendar_id = excluded.calendar_id,
    summary = excluded.summary, description = excluded.description,
    location = excluded.location, due_date = excluded.due_date,
    start_date = excluded.start_date, duration = excluded.duration,
    completed_at = excluded.completed_at, percent_complete = excluded.percent_complete,
    status = excluded.status, priority = excluded.priority,
    class = excluded.class, url = excluded.url,
    recurrence_rule = excluded.recurrence_rule,
    timezone = excluded.timezone,
    sequence = MAX(excluded.sequence, todos.sequence + 1),
    exdates = excluded.exdates, rdates = excluded.rdates,
    geo = excluded.geo,
    dtstamp = excluded.dtstamp,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: UpdateTodoExdates :exec
UPDATE todos SET exdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: DeleteTodo :exec
DELETE FROM todos WHERE id = ?;

