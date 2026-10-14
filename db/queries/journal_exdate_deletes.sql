-- name: RecordJournalExdateDelete :exec
-- Idempotent: deleting the same instance twice leaves exactly one log row.
INSERT INTO journal_exdate_deletes (calendar_id, uid, recurrence_id)
VALUES (?, ?, ?)
ON CONFLICT(uid, recurrence_id) DO UPDATE SET
    deleted_at = strftime('%Y-%m-%dT%H:%M:%SZ', 'now');

-- name: GetJournalExdateDeleteByUIDRecurrence :one
SELECT * FROM journal_exdate_deletes WHERE uid = ? AND recurrence_id = ?;

-- name: DeleteJournalExdateDelete :exec
DELETE FROM journal_exdate_deletes WHERE id = ?;

-- name: PurgeOldJournalExdateDeletes :execrows
DELETE FROM journal_exdate_deletes WHERE deleted_at < ?;

-- name: MoveJournalExdateDeletesUIDToCalendar :exec
-- Move every row of one UID (live or soft-deleted) from one calendar to
-- another. A local calendar move of a resource calls it. See issue #828.
UPDATE journal_exdate_deletes SET calendar_id = sqlc.arg(to_calendar_id)
WHERE calendar_id = sqlc.arg(from_calendar_id) AND uid = sqlc.arg(uid);
