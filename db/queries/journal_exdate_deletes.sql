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
