-- name: ListJournals :many
SELECT * FROM journals WHERE status != 'CANCELLED' ORDER BY start_date, summary;

-- name: ListJournalsByCalendar :many
SELECT * FROM journals WHERE calendar_id = ? AND status != 'CANCELLED' ORDER BY start_date, summary;

-- name: ListJournalsByStatus :many
SELECT * FROM journals WHERE status = ? ORDER BY start_date, summary;

-- name: ListJournalsByStartDateRange :many
SELECT * FROM journals WHERE start_date >= ? AND start_date < ? ORDER BY start_date, summary;

-- name: ListAllJournals :many
SELECT * FROM journals ORDER BY start_date, summary;

-- name: ListRecurringJournals :many
SELECT * FROM journals WHERE recurrence_rule IS NOT NULL AND recurrence_id = '';

-- name: ListRecurringJournalsByCalendar :many
SELECT * FROM journals WHERE recurrence_rule IS NOT NULL AND recurrence_id = '' AND calendar_id = ?;


-- name: GetJournal :one
SELECT * FROM journals WHERE id = ?;

-- name: GetJournalByUID :one
SELECT * FROM journals WHERE uid = ? AND recurrence_id = '';

-- name: GetJournalByUIDAndRecurrenceID :one
SELECT * FROM journals WHERE uid = ? AND recurrence_id = ?;

-- name: ListJournalOverridesByUID :many
SELECT * FROM journals WHERE uid = ? AND recurrence_id != '' ORDER BY recurrence_id;


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
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
RETURNING *;

-- name: UpdateJournalExdates :exec
UPDATE journals SET exdates = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now') WHERE id = ?;

-- name: DeleteJournal :exec
DELETE FROM journals WHERE id = ?;
