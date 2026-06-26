package storage

import "context"

// ListAttendeesByEventIDs returns attendees for the given event IDs. The ids
// are de-duplicated and chunked so the IN clause never exceeds SQLite's
// host-parameter cap on wide recurrence expansions (issue #303).
// Hand-written because database/sql does not support slice parameters.
func (q *Queries) ListAttendeesByEventIDs(ctx context.Context, ids []int64) ([]EventAttendee, error) {
	return loadByIDChunks(ctx, ids, func(ctx context.Context, chunk []int64) ([]EventAttendee, error) {
		placeholders, args := expandInPlaceholders(chunk)
		query := "SELECT id, event_id, email, name, rsvp_status, role, organizer, cutype, rsvp, sent_by, delegated_to, delegated_from, member, dir, language FROM event_attendees WHERE event_id IN (" + placeholders + ") ORDER BY event_id, organizer DESC, name"

		rows, err := q.db.QueryContext(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var items []EventAttendee
		for rows.Next() {
			var i EventAttendee
			if err := rows.Scan(
				&i.ID, &i.EventID, &i.Email, &i.Name,
				&i.RsvpStatus, &i.Role, &i.Organizer, &i.Cutype,
				&i.Rsvp, &i.SentBy, &i.DelegatedTo, &i.DelegatedFrom,
				&i.Member, &i.Dir, &i.Language,
			); err != nil {
				return nil, err
			}
			items = append(items, i)
		}
		return items, rows.Err()
	})
}
