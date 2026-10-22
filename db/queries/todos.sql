-- name: ListTodos :many
SELECT * FROM todos
WHERE status != 'COMPLETED' AND status != 'CANCELLED' AND deleted_at IS NULL
ORDER BY due_date, summary;

-- name: ListTodosByCalendar :many
SELECT * FROM todos
WHERE calendar_id = ? AND status != 'COMPLETED' AND status != 'CANCELLED' AND deleted_at IS NULL
ORDER BY due_date, summary;

-- name: ListTodosByStatus :many
SELECT * FROM todos WHERE status = ? AND deleted_at IS NULL ORDER BY due_date, summary;

-- name: ListTodosByDueDateRange :many
SELECT * FROM todos WHERE due_date >= ? AND due_date < ? AND deleted_at IS NULL ORDER BY due_date, summary;

-- name: ListAllTodos :many
SELECT * FROM todos WHERE deleted_at IS NULL ORDER BY due_date, summary;

-- name: ListRecurringTodos :many
SELECT * FROM todos WHERE (recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = '' AND deleted_at IS NULL;

-- name: ListRecurringTodosByCalendar :many
SELECT * FROM todos WHERE recurrence_rule IS NOT NULL AND recurrence_id = '' AND calendar_id = ? AND deleted_at IS NULL;


-- name: GetTodo :one
SELECT * FROM todos WHERE id = ? AND deleted_at IS NULL;

-- name: GetTodoIncludingDeleted :one
SELECT * FROM todos WHERE id = ?;

-- name: GetTodoByUID :one
SELECT * FROM todos WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: GetTodoByUIDIncludingDeleted :one
SELECT * FROM todos WHERE uid = ? AND recurrence_id = '';

-- name: GetTodoByUIDAndRecurrenceID :one
SELECT * FROM todos WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NULL;

-- name: ListTodoOverridesByUID :many
SELECT * FROM todos WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NULL ORDER BY recurrence_id;

-- name: ListDeletedTodoOverrideRecurrenceIDs :many
SELECT recurrence_id FROM todos
WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NOT NULL
ORDER BY recurrence_id;


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

-- NOTE: ON CONFLICT UPDATE clears deleted_at. This query resurrects
-- soft-deleted rows. Callers outside the pull path in the sync engine
-- must know this. The pull path is safe. The engine excludes
-- tombstoned UIDs before this query runs (engine.go loads tombstones
-- first and skips them during pull).
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
    deleted_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: UpdateTodoExdates :exec
UPDATE todos SET exdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: DeleteTodo :exec
DELETE FROM todos WHERE id = ?;

-- name: SoftDeleteTodo :exec
UPDATE todos SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NULL;

-- name: SoftDeleteTodosByCalendarUID :exec
-- Soft-delete every live row of one UID in one calendar. A pull that sees
-- the server object of the UID gone in that calendar uses it. Another
-- calendar can hold the same UID (a move, or a copy), so the delete stays
-- in the pulled calendar.
UPDATE todos SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE calendar_id = ? AND uid = ? AND deleted_at IS NULL;

-- name: SoftDeleteTodosByUID :exec
UPDATE todos SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NULL;

-- name: RestoreTodo :exec
UPDATE todos SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NOT NULL;

-- name: RestoreTodosByUID :execrows
UPDATE todos SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NOT NULL;

-- name: PurgeSoftDeletedTodos :execrows
DELETE FROM todos WHERE deleted_at IS NOT NULL AND deleted_at < ?;

-- name: PurgeTodoByID :execrows
DELETE FROM todos WHERE id = ? AND deleted_at IS NOT NULL;

-- name: ListDeletedTodosByCalendar :many
SELECT * FROM todos
WHERE calendar_id = ? AND deleted_at IS NOT NULL
ORDER BY deleted_at DESC;

-- name: ReassignTodosCalendar :execrows
-- Bulk-move every todo (live or soft-deleted) from one calendar to another
-- during calendar migration. See ReassignEventsCalendar.
UPDATE todos SET
    calendar_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE calendar_id = ?;

-- name: CountTodoIdentitiesByCalendar :one
-- See CountEventIdentitiesByCalendar.
SELECT COUNT(DISTINCT uid) FROM todos WHERE calendar_id = ? AND uid != '' AND deleted_at IS NULL;

-- name: MarkTodoIdentitiesDirtyForMigration :exec
-- See MarkEventIdentitiesDirtyForMigration.
INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
SELECT ?, uid, 'todo', 1, 'sync-token'
FROM todos AS src
WHERE src.calendar_id = ? AND src.deleted_at IS NULL AND src.uid != ''
GROUP BY uid
ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;

-- name: MoveTodoUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE todos SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: CountTodoUIDRowsInCalendar :one
-- Count the master rows and the override rows (live or soft-deleted) of
-- one UID in one calendar. The dirty-mark rule for a legacy override reads
-- it. See storage.MarkResourceDirty.
SELECT
    CAST(COALESCE(SUM(recurrence_id = ''), 0) AS INTEGER) AS masters,
    CAST(COALESCE(SUM(recurrence_id != ''), 0) AS INTEGER) AS overrides
FROM todos
WHERE calendar_id = ? AND uid = ?;

-- name: TodoUIDMasterCalendars :one
-- Count the calendars with a live master row of one UID, and give the
-- lowest of them. With one calendar, that calendar is the only one.
SELECT
    COUNT(DISTINCT calendar_id) AS calendars,
    CAST(COALESCE(MIN(calendar_id), 0) AS INTEGER) AS calendar_id
FROM todos
WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: CountLiveTodoRowsInCalendar :one
-- Count the live rows of one UID in one calendar. A local calendar has no
-- sync row, so the sync engine reads the rows to tell whether a moved
-- copy still exists there. See issue #841.
SELECT COUNT(*) FROM todos WHERE calendar_id = ? AND uid = ? AND deleted_at IS NULL;

-- name: CountLiveTodoRowsInMoveScope :one
-- Count the live rows of one UID outside calendar_id, in the calendars of
-- the account of calendar_id and in the local calendars. The restore of a
-- cancelled move refuses when a later move took the item there. A copy of
-- the UID in another account is another item. See issue #841.
SELECT COUNT(*)
FROM todos x
JOIN calendars c ON c.id = x.calendar_id
WHERE x.calendar_id != sqlc.arg(calendar_id)
  AND x.uid = sqlc.arg(uid)
  AND x.deleted_at IS NULL
  AND (
    c.account_id IS NULL OR c.account_id = 0
    OR c.account_id = (SELECT own.account_id FROM calendars own WHERE own.id = sqlc.arg(calendar_id))
  );

-- name: ListTodoRowIDsOfUIDInCalendar :many
-- The IDs of the rows of one UID (live and in the trash) in one calendar.
-- The push reads them before the first PUT of a resource. A domain move
-- keeps the row IDs, so a moved row names the target of a move. See issue
-- #841.
SELECT id FROM todos WHERE calendar_id = ? AND uid = ? ORDER BY id;

-- name: GetTodoCalendarOfRow :one
SELECT calendar_id FROM todos WHERE id = ?;

-- name: GetTodoCalendarByUIDAndRecurrenceID :one
-- The calendar of the row of one UID and recurrence ID, live or in the
-- trash, and whether the row is in the trash. The key spans calendars.
-- See issue #841.
SELECT calendar_id, CAST(deleted_at IS NOT NULL AS INTEGER) AS trashed FROM todos WHERE uid = ? AND recurrence_id = ?;
