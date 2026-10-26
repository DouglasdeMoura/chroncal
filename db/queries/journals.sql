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
SELECT * FROM journals WHERE (recurrence_rule IS NOT NULL OR (rdates IS NOT NULL AND rdates != '')) AND recurrence_id = '' AND deleted_at IS NULL;

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

-- name: ListDeletedJournalOverrideRecurrenceIDs :many
SELECT recurrence_id FROM journals
WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NOT NULL
ORDER BY recurrence_id;


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

-- NOTE: ON CONFLICT UPDATE clears deleted_at. This query resurrects
-- soft-deleted rows. Callers outside the pull path in the sync engine
-- must know this. The pull path is safe. The engine excludes
-- tombstoned UIDs before this query runs (engine.go loads tombstones
-- first and skips them during pull).
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
    delete_token = (SELECT COALESCE(MAX(prior.delete_token), 0) + 1 FROM journals AS prior),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE journals.id = ? AND journals.deleted_at IS NULL;

-- name: SoftDeleteJournalsByCalendarUID :exec
-- Soft-delete every live row of one UID in one calendar. A pull that sees
-- the server object of the UID gone in that calendar uses it. Another
-- calendar can hold the same UID (a move, or a copy), so the delete stays
-- in the pulled calendar.
UPDATE journals SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    delete_token = (SELECT COALESCE(MAX(prior.delete_token), 0) + 1 FROM journals AS prior),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE journals.calendar_id = ? AND journals.uid = ? AND journals.deleted_at IS NULL;

-- name: SoftDeleteJournalsByUID :exec
UPDATE journals SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now'),
    delete_token = (SELECT COALESCE(MAX(prior.delete_token), 0) + 1 FROM journals AS prior),
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE journals.uid = ? AND journals.deleted_at IS NULL;

-- name: RestoreJournal :exec
UPDATE journals SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE id = ? AND deleted_at IS NOT NULL;

-- name: RestoreJournalsByUIDAndDeleteToken :execrows
-- Restore the rows of one UID that one soft-delete statement hid (see the
-- column delete_token of migration 064).
UPDATE journals SET
    deleted_at = NULL,
    sequence = sequence + 1,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE uid = ? AND deleted_at IS NOT NULL AND delete_token = ?;

-- name: ListDeletedJournalOverrideRecurrenceIDsByDeleteToken :many
SELECT recurrence_id FROM journals
WHERE uid = ? AND recurrence_id != '' AND deleted_at IS NOT NULL AND delete_token = ?
ORDER BY recurrence_id;

-- name: RestoreJournalsByUID :execrows
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

-- name: ReassignJournalsCalendar :execrows
-- Bulk-move every journal (live or soft-deleted) from one calendar to another
-- during calendar migration. See ReassignEventsCalendar.
UPDATE journals SET
    calendar_id = ?,
    updated_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now')
WHERE calendar_id = ?;

-- name: CountJournalIdentitiesByCalendar :one
-- See CountEventIdentitiesByCalendar.
SELECT COUNT(DISTINCT uid) FROM journals WHERE calendar_id = ? AND uid != '' AND deleted_at IS NULL;

-- name: MarkJournalIdentitiesDirtyForMigration :exec
-- See MarkEventIdentitiesDirtyForMigration.
INSERT INTO sync_resources (calendar_id, uid, owner_type, dirty, sync_strategy)
SELECT ?, uid, 'journal', 1, 'sync-token'
FROM journals AS src
WHERE src.calendar_id = ? AND src.deleted_at IS NULL AND src.uid != ''
GROUP BY uid
ON CONFLICT(calendar_id, uid) DO UPDATE SET dirty = 1, rev = rev + 1;

-- name: MoveJournalUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE journals SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);

-- name: CountJournalUIDRowsInCalendar :one
-- Count the master rows and the override rows (live or soft-deleted) of
-- one UID in one calendar. The dirty-mark rule for a legacy override reads
-- it. See storage.MarkResourceDirty.
SELECT
    CAST(COALESCE(SUM(recurrence_id = ''), 0) AS INTEGER) AS masters,
    CAST(COALESCE(SUM(recurrence_id != ''), 0) AS INTEGER) AS overrides
FROM journals
WHERE calendar_id = ? AND uid = ?;

-- name: JournalUIDMasterCalendars :one
-- Count the calendars with a live master row of one UID, and give the
-- lowest of them. With one calendar, that calendar is the only one.
SELECT
    COUNT(DISTINCT calendar_id) AS calendars,
    CAST(COALESCE(MIN(calendar_id), 0) AS INTEGER) AS calendar_id
FROM journals
WHERE uid = ? AND recurrence_id = '' AND deleted_at IS NULL;

-- name: CountLiveJournalRowsInCalendar :one
-- Count the live rows of one UID in one calendar. A local calendar has no
-- sync row, so the sync engine reads the rows to tell whether a moved
-- copy still exists there. See issue #841.
SELECT COUNT(*) FROM journals WHERE calendar_id = ? AND uid = ? AND deleted_at IS NULL;

-- name: CountLiveJournalRowsInMoveScope :one
-- Count the live rows of one UID outside calendar_id, in the calendars of
-- the account of calendar_id and in the local calendars. The restore of a
-- cancelled move refuses when a later move took the item there. A copy of
-- the UID in another account is another item. See issue #841.
SELECT COUNT(*)
FROM journals x
JOIN calendars c ON c.id = x.calendar_id
WHERE x.calendar_id != sqlc.arg(calendar_id)
  AND x.uid = sqlc.arg(uid)
  AND x.deleted_at IS NULL
  AND (
    c.account_id IS NULL OR c.account_id = 0
    OR c.account_id = (SELECT own.account_id FROM calendars own WHERE own.id = sqlc.arg(calendar_id))
  );

-- name: ListJournalRowIDsOfUIDInCalendar :many
-- The IDs of the rows of one UID (live and in the trash) in one calendar.
-- The push reads them before the first PUT of a resource. A domain move
-- keeps the row IDs, so a moved row names the target of a move. See issue
-- #841.
SELECT id FROM journals WHERE calendar_id = ? AND uid = ? ORDER BY id;

-- name: GetJournalCalendarOfRow :one
SELECT calendar_id FROM journals WHERE id = ?;

-- name: GetJournalCalendarByUIDAndRecurrenceID :one
-- The ID and the calendar of the row of one UID and recurrence ID, live
-- or in the trash, and whether the row is in the trash. The key spans
-- calendars. See issue #841 and issue #916.
SELECT id, calendar_id, CAST(deleted_at IS NOT NULL AS INTEGER) AS trashed FROM journals WHERE uid = ? AND recurrence_id = ?;

-- name: ListJournalCalendarsOfUID :many
-- The calendar of each row of a UID, live or in the trash, and whether the
-- row is in the trash. A live row comes first, and a master comes before
-- an override. The import uses it for an item with no row of its own
-- (issue #882).
SELECT calendar_id, CAST(deleted_at IS NOT NULL AS INTEGER) AS trashed FROM journals
WHERE uid = ?
ORDER BY deleted_at IS NOT NULL, recurrence_id = '' DESC, id;
