-- name: ListJournals :many
SELECT * FROM journals
WHERE status != 'CANCELLED' AND deleted_at IS NULL
ORDER BY start_date, summary;

-- name: ListJournalsByCalendar :many
SELECT * FROM journals
WHERE calendar_id = ? AND status != 'CANCELLED' AND deleted_at IS NULL
ORDER BY start_date, summary;

-- name: ListJournalsByStatus :many
SELECT * FROM journals WHERE status = ? AND deleted_at IS NULL ORDER BY start_date, summary;

-- name: ListJournalsByStartDateRange :many
SELECT * FROM journals WHERE start_date >= ? AND start_date < ? AND deleted_at IS NULL ORDER BY start_date, summary;

-- name: ListAllJournals :many
SELECT * FROM journals WHERE deleted_at IS NULL ORDER BY start_date, summary;

-- name: ListRecurringJournals :many
SELECT * FROM journals WHERE recurrence_rule IS NOT NULL AND recurrence_id = '' AND deleted_at IS NULL;

-- name: ListRecurringJournalsByCalendar :many
SELECT * FROM journals WHERE recurrence_rule IS NOT NULL AND recurrence_id = '' AND calendar_id = ? AND deleted_at IS NULL;


-- name: GetJournal :one
SELECT * FROM journals WHERE id = ? AND deleted_at IS NULL;

-- name: GetJournalIncludingDeleted :one
SELECT * FROM journals WHERE id = ?;

-- name: GetJournalByUID :one
SELECT * FROM journals WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: GetJournalByUIDIncludingDeleted :one
SELECT * FROM journals WHERE uid = ? AND recurrence_id = '';

-- name: GetJournalByUIDAndRecurrenceID :one
SELECT * FROM journals WHERE uid = ? AND recurrence_id = ? AND deleted_at IS NULL;

-- name: ListJournalOverridesByUID :many
SELECT * FROM journals WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NULL ORDER BY recurrence_id;


-- name: DeleteJournalsByUID :exec
DELETE FROM journals WHERE uid = ?;

-- name: CreateJournal :one
INSERT INTO journals (
    uid, calendar_id, summary, description,
    start_date, status, class, url,
    recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateJournal :one
UPDATE journals SET
    summary = ?, description = ?,
    start_date = ?,
    status = ?, calendar_id = ?,
    class = ?, url = ?,
    recurrence_rule = ?, timezone = ?,
    sequence = sequence + 1,
    exdates = ?, rdates = ?,
    dtstamp = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? RETURNING *;

-- name: UpsertJournalByUID :one
INSERT INTO journals (
    uid, calendar_id, summary, description,
    start_date, status, class, url,
    recurrence_rule, timezone, sequence, exdates, rdates, recurrence_id, dtstamp
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    calendar_id = excluded.calendar_id,
    summary = excluded.summary, description = excluded.description,
    start_date = excluded.start_date,
    status = excluded.status,
    class = excluded.class, url = excluded.url,
    recurrence_rule = excluded.recurrence_rule,
    timezone = excluded.timezone,
    sequence = MAX(excluded.sequence, journals.sequence + 1),
    exdates = excluded.exdates, rdates = excluded.rdates,
    dtstamp = excluded.dtstamp,
    deleted_at = NULL,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: UpdateJournalExdates :exec
UPDATE journals SET exdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: DeleteJournal :exec
DELETE FROM journals WHERE id = ?;

-- name: SoftDeleteJournal :exec
UPDATE journals SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NULL;

-- name: SoftDeleteJournalsByUID :exec
UPDATE journals SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NULL;

-- name: RestoreJournal :exec
UPDATE journals SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NOT NULL;

-- name: RestoreJournalsByUID :exec
UPDATE journals SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NOT NULL;

-- name: PurgeSoftDeletedJournals :execrows
DELETE FROM journals WHERE deleted_at IS NOT NULL AND deleted_at < ?;

-- name: PurgeJournalByID :execrows
DELETE FROM journals WHERE id = ? AND deleted_at IS NOT NULL;

-- name: ListDeletedJournalsByCalendar :many
SELECT * FROM journals
WHERE calendar_id = ? AND deleted_at IS NOT NULL
ORDER BY deleted_at DESC;
