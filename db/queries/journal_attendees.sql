-- name: CreateJournalAttendee :one
INSERT INTO journal_attendees (journal_id, email, name, rsvp_status, role, organizer, cutype, rsvp, sent_by, delegated_to, delegated_from, member, dir, language)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING *;

-- name: ListJournalAttendeesByJournalID :many
SELECT * FROM journal_attendees WHERE journal_id = ? ORDER BY organizer DESC, name;

-- name: DeleteJournalAttendeesByJournalID :exec
DELETE FROM journal_attendees WHERE journal_id = ?;
